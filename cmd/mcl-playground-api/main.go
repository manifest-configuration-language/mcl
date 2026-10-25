package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
)

const (
	maxSourceBytes      = 64 * 1024
	maxProjectFileSize  = 16 * 1024 * 1024
	maxProjectBytes     = 32 * 1024 * 1024
	maxProjectFiles     = 4096
	maxProjectPathSize  = 512
	maxProjectPathDepth = 64
	maxRequestBytes     = 64 * 1024 * 1024
	maxShareIDLength    = 36
	maxStoredBytes      = 5 * 1024 * 1024 * 1024
	cleanupBatchSize    = 1000
)

const compressedPayloadMarker = "MCL-SHARE-GZIP\x00"

type projectFile struct {
	Encoding string  `json:"encoding"`
	Content  *string `json:"content"`
}

type projectSnapshot struct {
	Version    int                    `json:"version"`
	Source     string                 `json:"source"`
	Files      map[string]projectFile `json:"files"`
	ActivePath optionalString         `json:"activePath"`
}

type optionalString struct {
	value   string
	present bool
}

func (value *optionalString) UnmarshalJSONFrom(decoder *jsontext.Decoder) error {
	raw, err := decoder.ReadValue()
	if err != nil {
		return err
	}
	if raw.Kind() == jsontext.KindNull {
		return errors.New("activePath must be omitted or a string")
	}
	if err := jsonv2.Unmarshal(raw, &value.value); err != nil {
		return err
	}
	value.present = true

	return nil
}

type shareAPI struct {
	database        *pgxpool.Pool
	expiresAfter    time.Duration
	createLimiter   *clientRateLimiter
	readLimiter     *clientRateLimiter
	requestCapacity chan struct{}
}

type clientRateLimiter struct {
	mu          sync.Mutex
	limiters    map[string]clientLimiter
	lastCleanup time.Time
	limit       rate.Limit
	burst       int
}

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func main() {
	if err := run(); err != nil {
		slog.Error("Playground share API stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	listenAddress := os.Getenv("LISTEN_ADDR")
	if listenAddress == "" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		listenAddress = ":" + port
	}
	expiresAfter, err := durationFromEnv("SHARE_TTL", 90*24*time.Hour)
	if err != nil {
		return err
	}
	if expiresAfter < time.Hour || expiresAfter > 365*24*time.Hour {
		return errors.New("SHARE_TTL must be between 1 hour and 365 days")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	poolConfig.MaxConns = 4
	database, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	defer database.Close()

	startupCtx, cancelStartup := context.WithTimeout(ctx, 10*time.Second)
	defer cancelStartup()
	if err := database.Ping(startupCtx); err != nil {
		return fmt.Errorf("ping PostgreSQL: %w", err)
	}
	if err := initializeSchema(startupCtx, database); err != nil {
		return err
	}
	go removeExpired(ctx, database)

	api := &shareAPI{
		database:        database,
		expiresAfter:    expiresAfter,
		createLimiter:   newClientRateLimiter(rate.Every(time.Hour/10), 3),
		readLimiter:     newClientRateLimiter(rate.Every(time.Minute/60), 30),
		requestCapacity: make(chan struct{}, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", api.ready)
	mux.HandleFunc("POST /api/playground/snippets", api.create)
	mux.HandleFunc("GET /api/playground/snippets/{id}", api.get)

	server := &http.Server{
		Addr:              listenAddress,
		Handler:           requestLog(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("Listening", "address", listenAddress)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down HTTP server: %w", err)
	}

	return nil
}

func durationFromEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}

	return duration, nil
}

func initializeSchema(ctx context.Context, database *pgxpool.Pool) error {
	const createTable = `
CREATE TABLE IF NOT EXISTS playground_snippets (
	 id text PRIMARY KEY CHECK (length(id) = 36),
	 payload bytea NOT NULL CHECK (octet_length(payload) <= 67108864),
	 created_at timestamptz NOT NULL DEFAULT now(),
	 expires_at timestamptz NOT NULL
);
`
	if _, err := database.Exec(ctx, createTable); err != nil {
		return fmt.Errorf("initialize Playground share table: %w", err)
	}
	const createIndex = `
CREATE INDEX IF NOT EXISTS playground_snippets_expires_at_idx
	ON playground_snippets (expires_at);
`
	if _, err := database.Exec(ctx, createIndex); err != nil {
		return fmt.Errorf("initialize Playground share table: %w", err)
	}
	const createStorageTable = `
CREATE TABLE IF NOT EXISTS playground_share_storage (
	 id smallint PRIMARY KEY CHECK (id = 1),
	 stored_bytes bigint NOT NULL CHECK (stored_bytes >= 0)
);
`
	if _, err := database.Exec(ctx, createStorageTable); err != nil {
		return fmt.Errorf("initialize Playground share storage quota: %w", err)
	}
	const initializeStorageCounter = `
INSERT INTO playground_share_storage (id, stored_bytes) VALUES (1, 0)
	ON CONFLICT (id) DO NOTHING;
`
	if _, err := database.Exec(ctx, initializeStorageCounter); err != nil {
		return fmt.Errorf("initialize Playground share storage quota: %w", err)
	}
	return nil
}

func removeExpired(ctx context.Context, database *pgxpool.Pool) {
	cleanupExpired(ctx, database)
	if ctx.Err() != nil {
		return
	}

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanupExpired(ctx, database)
		}
	}
}

func cleanupExpired(ctx context.Context, database *pgxpool.Pool) {
	for {
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		transaction, err := database.Begin(cleanupCtx)
		if err != nil {
			cancel()
			slog.Error("Begin expired Playground share cleanup", "error", err)

			return
		}
		if _, err := lockStorageCounter(cleanupCtx, transaction); err != nil {
			_ = transaction.Rollback(cleanupCtx)
			cancel()
			slog.Error("Lock Playground share storage quota", "error", err)

			return
		}
		removedBytes, err := cleanupExpiredBatch(cleanupCtx, transaction)
		if err != nil {
			_ = transaction.Rollback(cleanupCtx)
			cancel()
			slog.Error("Remove expired Playground shares", "error", err)

			return
		}
		if err := transaction.Commit(cleanupCtx); err != nil {
			cancel()
			slog.Error("Commit expired Playground share cleanup", "error", err)

			return
		}
		cancel()
		if removedBytes == 0 {
			return
		}
	}
}

func lockStorageCounter(ctx context.Context, transaction pgx.Tx) (int64, error) {
	var storedBytes int64
	err := transaction.QueryRow(ctx, `
		SELECT stored_bytes
		FROM playground_share_storage
		WHERE id = 1
		FOR UPDATE
	`).Scan(&storedBytes)

	return storedBytes, err
}

func cleanupExpiredBatch(ctx context.Context, transaction pgx.Tx) (int64, error) {
	var removedBytes int64
	err := transaction.QueryRow(ctx, `
WITH expired AS (
	SELECT id
	FROM playground_snippets
	WHERE expires_at <= now()
	ORDER BY expires_at
	LIMIT $1
	FOR UPDATE SKIP LOCKED
), deleted AS (
	DELETE FROM playground_snippets AS snippets
	USING expired
	WHERE snippets.id = expired.id
	RETURNING octet_length(snippets.payload) AS payload_bytes
), stats AS (
	SELECT COALESCE(sum(payload_bytes), 0) AS removed_bytes
	FROM deleted
)
UPDATE playground_share_storage
SET stored_bytes = GREATEST(0, stored_bytes - stats.removed_bytes)
FROM stats
WHERE id = 1
RETURNING stats.removed_bytes
`, cleanupBatchSize).Scan(&removedBytes)

	return removedBytes, err
}

func compressPayload(payload []byte) ([]byte, error) {
	var compressed bytes.Buffer
	compressed.WriteString(compressedPayloadMarker)

	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		return nil, fmt.Errorf("compress share payload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish compressed share payload: %w", err)
	}
	if compressed.Len() >= len(payload) {
		return payload, nil
	}

	return compressed.Bytes(), nil
}

func decompressPayload(payload []byte) ([]byte, error) {
	if !bytes.HasPrefix(payload, []byte(compressedPayloadMarker)) {
		return payload, nil
	}

	reader, err := gzip.NewReader(bytes.NewReader(payload[len(compressedPayloadMarker):]))
	if err != nil {
		return nil, fmt.Errorf("open compressed share payload: %w", err)
	}
	defer reader.Close()

	decompressed, err := io.ReadAll(io.LimitReader(reader, maxRequestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("decompress share payload: %w", err)
	}
	if len(decompressed) > maxRequestBytes {
		return nil, errors.New("decompressed share payload exceeds the request size limit")
	}

	return decompressed, nil
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (api *shareAPI) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := api.database.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")

		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (api *shareAPI) create(w http.ResponseWriter, r *http.Request) {
	if !api.createLimiter.allow(clientAddress(r)) {
		w.Header().Set("Retry-After", "360")
		writeError(w, http.StatusTooManyRequests, "share creation rate limit exceeded")

		return
	}
	if contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || contentType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")

		return
	}
	select {
	case api.requestCapacity <- struct{}{}:
		defer func() { <-api.requestCapacity }()
	case <-r.Context().Done():
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "project snapshot exceeds the request size limit")

			return
		}
		writeError(w, http.StatusBadRequest, "could not read project snapshot")

		return
	}
	var snapshot projectSnapshot
	if err := jsonv2.Unmarshal(body, &snapshot, jsonv2.RejectUnknownMembers(true)); err != nil {
		writeError(w, http.StatusBadRequest, "invalid project snapshot")

		return
	}
	if err := validateSnapshot(snapshot); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}
	storedPayload, err := compressPayload(body)
	if err != nil {
		slog.Error("Compress Playground share", "error", err)
		writeError(w, http.StatusInternalServerError, "could not store project snapshot")

		return
	}

	id := uuid.NewV4().String()
	expiresAt := time.Now().UTC().Add(api.expiresAfter)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	transaction, err := api.database.Begin(ctx)
	if err != nil {
		slog.Error("Begin Playground share storage", "error", err)
		writeError(w, http.StatusInternalServerError, "could not store project snapshot")

		return
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := lockStorageCounter(ctx, transaction); err != nil {
		slog.Error("Lock Playground share storage quota", "error", err)
		writeError(w, http.StatusInternalServerError, "could not store project snapshot")

		return
	}
	if _, err := cleanupExpiredBatch(ctx, transaction); err != nil {
		slog.Error("Remove expired Playground shares", "error", err)
		writeError(w, http.StatusInternalServerError, "could not store project snapshot")

		return
	}
	storedBytes, err := lockStorageCounter(ctx, transaction)
	if err != nil {
		slog.Error("Read Playground share storage quota", "error", err)
		writeError(w, http.StatusInternalServerError, "could not store project snapshot")

		return
	}
	if storedBytes+int64(len(storedPayload)) > maxStoredBytes {
		writeError(w, http.StatusInsufficientStorage, "share storage is full")

		return
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE playground_share_storage
		SET stored_bytes = stored_bytes + $1
		WHERE id = 1
	`, int64(len(storedPayload))); err != nil {
		slog.Error("Update Playground share storage quota", "error", err)
		writeError(w, http.StatusInternalServerError, "could not store project snapshot")

		return
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO playground_snippets (id, payload, expires_at)
		VALUES ($1, $2, $3)
	`, id, storedPayload, expiresAt); err != nil {
		slog.Error("Store Playground share", "error", err)
		writeError(w, http.StatusInternalServerError, "could not store project snapshot")

		return
	}
	if err := transaction.Commit(ctx); err != nil {
		slog.Error("Store Playground share", "error", err)
		writeError(w, http.StatusInternalServerError, "could not store project snapshot")

		return
	}
	writeJSON(w, http.StatusCreated, struct {
		ID        string    `json:"id"`
		ExpiresAt time.Time `json:"expiresAt"`
	}{ID: id, ExpiresAt: expiresAt})
}

func (api *shareAPI) get(w http.ResponseWriter, r *http.Request) {
	if !api.readLimiter.allow(clientAddress(r)) {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "share retrieval rate limit exceeded")

		return
	}
	id := r.PathValue("id")
	if len(id) != maxShareIDLength {
		writeError(w, http.StatusNotFound, "project snapshot not found")

		return
	}
	parsedID, err := uuid.Parse(id)
	if err != nil || parsedID.String() != id {
		writeError(w, http.StatusNotFound, "project snapshot not found")

		return
	}
	select {
	case api.requestCapacity <- struct{}{}:
		defer func() { <-api.requestCapacity }()
	case <-r.Context().Done():
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var payload []byte
	if err := api.database.QueryRow(ctx, `
		SELECT payload
		FROM playground_snippets
		WHERE id = $1 AND expires_at > now()
	`, id).Scan(&payload); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "project snapshot not found")

			return
		}
		slog.Error("Load Playground share", "error", err)
		writeError(w, http.StatusInternalServerError, "could not load project snapshot")

		return
	}
	payload, err = decompressPayload(payload)
	if err != nil {
		slog.Error("Decompress Playground share", "error", err)
		writeError(w, http.StatusInternalServerError, "could not load project snapshot")

		return
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(payload); err != nil {
		slog.Error("Write Playground share", "error", err)
	}
}

func validateSnapshot(snapshot projectSnapshot) error {
	if snapshot.Version != 1 {
		return errors.New("unsupported project snapshot version")
	}
	if snapshot.Files == nil {
		return errors.New("project files must be an object")
	}
	if !utf8.ValidString(snapshot.Source) || len(snapshot.Source) > maxSourceBytes {
		return errors.New("project source exceeds the size limit")
	}
	if len(snapshot.Files)+1 > maxProjectFiles {
		return errors.New("project contains too many files")
	}
	paths := make(map[string]struct{}, len(snapshot.Files)+1)
	paths["main.mcl"] = struct{}{}
	totalBytes := len(snapshot.Source)
	for filePath, file := range snapshot.Files {
		if !validProjectPath(filePath) || filePath == "main.mcl" {
			return errors.New("project contains an invalid file path")
		}
		if _, ok := paths[filePath]; ok {
			return errors.New("project contains duplicate file paths")
		}
		paths[filePath] = struct{}{}
		fileBytes, err := projectFileBytes(file)
		if err != nil {
			return err
		}
		if fileBytes > maxProjectFileSize {
			return errors.New("project file exceeds the size limit")
		}
		totalBytes += fileBytes
		if totalBytes > maxProjectBytes {
			return errors.New("project exceeds the size limit")
		}
	}
	if !validFileSet(paths) {
		return errors.New("project file paths overlap files and directories")
	}
	if snapshot.ActivePath.present {
		if !validProjectPath(snapshot.ActivePath.value) {
			return errors.New("invalid active project path")
		}
		if snapshot.ActivePath.value != "main.mcl" {
			file, ok := snapshot.Files[snapshot.ActivePath.value]
			if !ok || file.Encoding != "utf8" {
				return errors.New("active project path must name a UTF-8 file")
			}
		}
	}

	return nil
}

func projectFileBytes(file projectFile) (int, error) {
	switch file.Encoding {
	case "utf8":
		if file.Content == nil || !utf8.ValidString(*file.Content) {
			return 0, errors.New("project file must contain valid UTF-8")
		}

		return len(*file.Content), nil
	case "binary":
		if file.Content == nil {
			return 0, errors.New("project file must include content")
		}
		content, err := base64.RawURLEncoding.Strict().DecodeString(*file.Content)
		if err != nil || base64.RawURLEncoding.EncodeToString(content) != *file.Content {
			return 0, errors.New("project binary file must use unpadded base64url")
		}

		return len(content), nil
	default:
		return 0, errors.New("project file encoding must be utf8 or binary")
	}
}

func validProjectPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || utf16Length(value) > maxProjectPathSize || strings.HasPrefix(value, "/") ||
		strings.ContainsAny(value, "\\\x00") || len(value) >= 2 && isASCIIAlpha(value[0]) && value[1] == ':' {
		return false
	}
	if strings.Count(value, "/") >= maxProjectPathDepth {
		return false
	}
	for segment := range strings.SplitSeq(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}

	return true
}

func utf16Length(value string) int {
	length := 0
	for _, character := range value {
		length += utf16.RuneLen(character)
	}

	return length
}

func isASCIIAlpha(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func validFileSet(paths map[string]struct{}) bool {
	directories := make(map[string]struct{}, len(paths))
	for filePath := range paths {
		for parent := path.Dir(filePath); parent != "."; parent = path.Dir(parent) {
			if _, ok := paths[parent]; ok {
				return false
			}
			directories[parent] = struct{}{}
		}
	}
	for directory := range directories {
		if _, ok := paths[directory]; ok {
			return false
		}
	}

	return true
}

func clientAddress(r *http.Request) string {
	address, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		remoteIP := net.ParseIP(address)
		if remoteIP != nil {
			// The API ingress policy permits only Envoy Gateway pods. Envoy replaces
			// this header for external requests; ignore it on other connections.
			if remoteIP.IsPrivate() {
				if forwardedIP := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Envoy-External-Address"))); forwardedIP != nil {
					return forwardedIP.String()
				}
			}
			return remoteIP.String()
		}

		return address
	}

	return r.RemoteAddr
}

func newClientRateLimiter(limit rate.Limit, burst int) *clientRateLimiter {
	return &clientRateLimiter{limiters: make(map[string]clientLimiter), limit: limit, burst: burst}
}

func (limiter *clientRateLimiter) allow(address string) bool {
	now := time.Now()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if now.Sub(limiter.lastCleanup) >= time.Minute {
		maps.DeleteFunc(limiter.limiters, func(_ string, entry clientLimiter) bool {
			return now.Sub(entry.lastSeen) > time.Hour
		})
		limiter.lastCleanup = now
	}
	entry, ok := limiter.limiters[address]
	if !ok {
		if len(limiter.limiters) >= 10_000 {
			return false
		}
		entry = clientLimiter{limiter: rate.NewLimiter(limiter.limit, limiter.burst)}
	}
	entry.lastSeen = now
	limiter.limiters[address] = entry

	return entry.limiter.AllowN(now, 1)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := jsonv2.MarshalWrite(w, value); err != nil {
		slog.Error("Write JSON response", "error", err)
	}
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("HTTP request", "method", r.Method, "route", r.Pattern, "duration", time.Since(started))
	})
}

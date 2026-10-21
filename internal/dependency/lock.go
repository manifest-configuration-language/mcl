package dependency

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	contentutil "github.com/manifest-configuration-language/mcl/internal/content"
	"github.com/manifest-configuration-language/mcl/internal/netpolicy"
	"github.com/manifest-configuration-language/mcl/internal/safepath"
)

const maxRemoteManifestSize = 16 << 20

const maxRemoteChartArchiveSize = 64 << 20

const maxRemoteGitTreeSize = 32 << 20

const schemeHTTPS = "https"

const maxKustomizeSourceBytes = 16 << 20

type Lock struct {
	identity ToolchainIdentity
	//nolint:containedctx // Lockは同一の依存更新処理で使う取得Contextを共有します。
	ctx        context.Context
	root       string
	update     bool
	manifests  map[string]string
	schemas    map[string]string
	charts     map[string]string
	ociCharts  map[string]map[string]ociChartLock
	gitSources map[string]gitSourceLock
	fetched    map[string][]byte
	filesystem *rootFilesystem
}

type ociChartLock struct {
	ManifestDigest   string `json:"manifestDigest"`
	ManifestSize     int64  `json:"manifestSize"`
	ChartLayerDigest string `json:"chartLayerDigest"`
	ChartLayerSize   int64  `json:"chartLayerSize"`
	ArchiveDigest    string `json:"archiveDigest"`
}

type gitSourceLock struct {
	Source        string `json:"source"`
	Revision      string `json:"revision"`
	Directory     string `json:"directory"`
	ArchiveDigest string `json:"archiveDigest"`
}

type lockFile struct {
	Version    int                                `json:"version"`
	Toolchain  toolchainLock                      `json:"toolchain"`
	Manifests  map[string]string                  `json:"manifests"`
	Schemas    map[string]string                  `json:"schemas"`
	Charts     map[string]string                  `json:"charts"`
	OCICharts  map[string]map[string]ociChartLock `json:"ociCharts"`
	GitSources map[string]gitSourceLock           `json:"gitSources"`
}

type toolchainLock struct {
	MCL            string `json:"mcl"`
	Revision       string `json:"revision"`
	Modified       string `json:"modified"`
	Go             string `json:"go"`
	Helm           string `json:"helm"`
	KustomizeAPI   string `json:"kustomizeAPI"`
	KustomizeKYAML string `json:"kustomizeKYAML"`
}

type ToolchainIdentity struct {
	MCL            string
	Revision       string
	Modified       string
	Go             string
	Helm           string
	KustomizeAPI   string
	KustomizeKYAML string
}

func Open(ctx context.Context, root string, update bool, identity ToolchainIdentity) (*Lock, error) {
	return openWithFilesystem(ctx, root, update, identity, newOSRootFilesystem(root))
}

// OpenWithFS loads the project lock and cached dependency content from a root-relative filesystem.
// Lock writes remain OS-backed because io/fs does not provide write operations.
func OpenWithFS(ctx context.Context, root string, update bool, identity ToolchainIdentity, files fs.FS) (*Lock, error) {
	if files == nil {
		return nil, errors.New("dependency filesystem is required")
	}
	if update {
		return nil, errors.New("dependency updates require an OS-backed filesystem")
	}

	return openWithFilesystem(ctx, root, update, identity, newRootFilesystem(root, files))
}

func openWithFilesystem(ctx context.Context, root string, update bool, identity ToolchainIdentity, files *rootFilesystem) (*Lock, error) {
	if ctx == nil {
		return nil, errors.New("dependency context is required")
	}
	lock := &Lock{
		ctx: ctx, root: root, update: update, identity: identity,
		manifests: make(map[string]string), schemas: make(map[string]string), charts: make(map[string]string),
		ociCharts: make(map[string]map[string]ociChartLock), gitSources: make(map[string]gitSourceLock),
		fetched: make(map[string][]byte), filesystem: files,
	}
	info, err := files.stat("mcl.lock")
	if errors.Is(err, fs.ErrNotExist) {
		return lock, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat dependency lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("dependency lock must be a regular file")
	}
	data, err := files.readLimitedFile("mcl.lock", maxRemoteManifestSize)
	if err != nil {
		return nil, fmt.Errorf("read dependency lock: %w", err)
	}
	var content lockFile
	if err := json.Unmarshal(data, &content, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("decode dependency lock: %w", err)
	}
	if content.Version < 1 || content.Version > 3 {
		return nil, fmt.Errorf("unsupported dependency lock version %d", content.Version)
	}
	if content.Version < 3 && !update {
		return nil, errors.New("dependency lock does not record the complete toolchain identity; run mcl update")
	}
	if content.Version == 3 && !lock.identity.compatibleWith(content.Toolchain) && !update {
		return nil, errors.New("dependency lock toolchain does not match this mcl build; run mcl update")
	}
	for source, digest := range content.Manifests {
		if _, err := ValidatedManifestURL(source); err != nil {
			return nil, fmt.Errorf("invalid URL in dependency lock: %w", err)
		}
		if !contentutil.IsSHA256(digest) {
			return nil, fmt.Errorf("invalid SHA-256 digest for %q", source)
		}
		lock.manifests[source] = digest
	}
	for source, digest := range content.Schemas {
		contentURL, _, err := SchemaSourceURL(source)
		if err != nil || contentURL != source {
			return nil, fmt.Errorf("invalid URL in dependency lock for Schema %q", source)
		}
		if !contentutil.IsSHA256(digest) {
			return nil, fmt.Errorf("invalid SHA-256 digest for Schema %q", source)
		}
		lock.schemas[source] = digest
	}
	for source, digest := range content.Charts {
		if _, err := ValidatedManifestURL(source); err != nil {
			return nil, fmt.Errorf("invalid URL in dependency lock for Helm Chart %q", source)
		}
		if !contentutil.IsSHA256(digest) {
			return nil, fmt.Errorf("invalid SHA-256 digest for Helm Chart %q", source)
		}
		lock.charts[source] = digest
	}
	for source, versions := range content.OCICharts {
		if err := ValidateOCIChartSource(source); err != nil {
			return nil, fmt.Errorf("invalid OCI Helm Chart source in dependency lock: %w", err)
		}
		lock.ociCharts[source] = make(map[string]ociChartLock, len(versions))
		for version, entry := range versions {
			if version == "" || !validOCIDigest(entry.ManifestDigest) || entry.ManifestSize <= 0 || entry.ManifestSize > 1<<20 || !validOCIDigest(entry.ChartLayerDigest) || entry.ChartLayerSize <= 0 || entry.ChartLayerSize > maxRemoteChartArchiveSize || !contentutil.IsSHA256(entry.ArchiveDigest) {
				return nil, fmt.Errorf("invalid OCI Helm Chart lock for %q version %q", source, version)
			}
			lock.ociCharts[source][version] = entry
		}
	}
	for key, entry := range content.GitSources {
		if err := ValidateGitSource(entry.Source); err != nil || !ValidGitRevision(entry.Revision) || ValidateGitDirectory(entry.Directory) != nil || key != GitSourceKey(entry.Source, entry.Revision, entry.Directory) || !contentutil.IsSHA256(entry.ArchiveDigest) {
			return nil, fmt.Errorf("invalid Git source lock for %q", key)
		}
		lock.gitSources[key] = entry
	}
	if update {
		clear(lock.manifests)
		clear(lock.schemas)
		clear(lock.charts)
		clear(lock.ociCharts)
		clear(lock.gitSources)
	}

	return lock, nil
}

func (lock *Lock) LoadManifest(source string) ([]byte, error) {
	if _, err := ValidatedManifestURL(source); err != nil {
		return nil, err
	}

	return lock.loadRemote(source, "manifest", lock.manifests)
}

func (lock *Lock) LoadSchema(source string) ([]byte, error) {
	contentURL, _, err := SchemaSourceURL(source)
	if err != nil {
		return nil, err
	}

	return lock.loadRemote(contentURL, "schema", lock.schemas)
}

func (lock *Lock) LoadChart(source string) ([]byte, error) {
	if _, err := ValidatedManifestURL(source); err != nil {
		return nil, err
	}

	return lock.loadRemote(source, "chart", lock.charts)
}

func (lock *Lock) LoadOCIChart(source, version string) ([]byte, error) {
	if err := ValidateOCIChartSource(source); err != nil {
		return nil, err
	}
	if version == "" {
		return nil, errors.New("OCI Helm Chart version is required")
	}
	if lock.update {
		key := "oci-chart\x00" + source + "\x00" + version
		if data, exists := lock.fetched[key]; exists {
			return data, nil
		}
		archive, metadata, err := fetchOCIChart(lock.ctx, source, version)
		if err != nil {
			return nil, err
		}
		metadata.ArchiveDigest = contentutil.SHA256(archive)
		if err := lock.writeCache(metadata.ArchiveDigest, archive); err != nil {
			return nil, err
		}
		if lock.ociCharts[source] == nil {
			lock.ociCharts[source] = make(map[string]ociChartLock)
		}
		lock.ociCharts[source][version] = metadata
		lock.fetched[key] = archive

		return archive, nil
	}
	entry, exists := lock.ociCharts[source][version]
	if !exists {
		return nil, fmt.Errorf("OCI Helm Chart %q version %q is not locked; run mcl update", source, version)
	}
	archive, err := lock.readCache(entry.ArchiveDigest, maxRemoteChartArchiveSize)
	if err != nil {
		return nil, err
	}
	if contentutil.SHA256(archive) != entry.ArchiveDigest {
		return nil, fmt.Errorf("cached OCI Helm Chart %q version %q does not match its locked archive SHA-256 digest", source, version)
	}
	if int64(len(archive)) != entry.ChartLayerSize || "sha256:"+contentutil.SHA256(archive) != entry.ChartLayerDigest {
		return nil, fmt.Errorf("cached OCI Helm Chart %q version %q does not match its locked layer digest or size", source, version)
	}

	return archive, nil
}

func (lock *Lock) LoadGitSource(source, revision, directory string) ([]byte, error) {
	if err := ValidateGitSource(source); err != nil {
		return nil, err
	}
	if !ValidGitRevision(revision) {
		return nil, errors.New("kustomize Git revision must be a complete 40-character lowercase commit SHA-1")
	}
	if err := ValidateGitDirectory(directory); err != nil {
		return nil, err
	}
	key := GitSourceKey(source, revision, directory)
	if lock.update {
		fetchKey := "git-source\x00" + key
		if data, exists := lock.fetched[fetchKey]; exists {
			return data, nil
		}
		archive, err := fetchGitTree(lock.ctx, source, revision, directory)
		if err != nil {
			return nil, err
		}
		digest := contentutil.SHA256(archive)
		if err := lock.writeCache(digest, archive); err != nil {
			return nil, err
		}
		lock.gitSources[key] = gitSourceLock{Source: source, Revision: revision, Directory: directory, ArchiveDigest: digest}
		lock.fetched[fetchKey] = archive

		return archive, nil
	}
	entry, exists := lock.gitSources[key]
	if !exists {
		return nil, fmt.Errorf("kustomize Git source %q revision %q directory %q is not locked; run mcl update", source, revision, directory)
	}
	archive, err := lock.readCache(entry.ArchiveDigest, maxRemoteGitTreeSize)
	if err != nil {
		return nil, err
	}
	if contentutil.SHA256(archive) != entry.ArchiveDigest {
		return nil, fmt.Errorf("cached Kustomize Git source %q does not match its locked SHA-256 digest", source)
	}

	return archive, nil
}

func (lock *Lock) loadRemote(source, category string, entries map[string]string) ([]byte, error) {
	if lock.update {
		key := category + "\x00" + source
		if data, exists := lock.fetched[key]; exists {
			return data, nil
		}
		data, err := fetchRemote(lock.ctx, source, category)
		if err != nil {
			return nil, err
		}
		digest := contentutil.SHA256(data)
		if err := lock.writeCache(digest, data); err != nil {
			return nil, err
		}
		entries[source] = digest
		lock.fetched[key] = data

		return data, nil
	}
	digest, exists := entries[source]
	if !exists {
		return nil, fmt.Errorf("%s URL %q is not locked; run mcl update", category, source)
	}
	maximumSize := int64(maxRemoteManifestSize)
	if category == "chart" {
		maximumSize = maxRemoteChartArchiveSize
	}
	data, err := lock.readCache(digest, maximumSize)
	if err != nil {
		return nil, err
	}
	if contentutil.SHA256(data) != digest {
		return nil, fmt.Errorf("cached %s %q does not match its locked SHA-256 digest", category, source)
	}

	return data, nil
}

func (lock *Lock) Save() error {
	if lock.filesystem == nil || !lock.filesystem.osBacked {
		return errors.New("dependency lock writes require an OS-backed filesystem")
	}
	content := lockFile{Version: 3, Toolchain: lock.identity.lock(), Manifests: lock.manifests, Schemas: lock.schemas, Charts: lock.charts, OCICharts: lock.ociCharts, GitSources: lock.gitSources}
	data, err := json.Marshal(content, json.Deterministic(true))
	if err != nil {
		return fmt.Errorf("encode dependency lock: %w", err)
	}
	formatted := jsontext.Value(data)
	if err := formatted.Indent(jsontext.WithIndent("  ")); err != nil {
		return fmt.Errorf("format dependency lock: %w", err)
	}

	return contentutil.WriteFileAtomic(filepath.Join(lock.root, "mcl.lock"), append(formatted, '\n'))
}

func (identity ToolchainIdentity) lock() toolchainLock {
	return toolchainLock(identity)
}

func (identity ToolchainIdentity) compatibleWith(locked toolchainLock) bool {
	return identity.MCL == locked.MCL && identity.Go == locked.Go && identity.Helm == locked.Helm && identity.KustomizeAPI == locked.KustomizeAPI && identity.KustomizeKYAML == locked.KustomizeKYAML
}

func (lock *Lock) IsUpdate() bool { return lock.update }

func (lock *Lock) cachePath(digest string) string {
	return filepath.Join(lock.root, ".mcl", "cache", "sha256", digest)
}

func (lock *Lock) readCache(digest string, maximumSize int64) ([]byte, error) {
	cachePath := lock.cachePath(digest)
	relative, err := filepath.Rel(lock.root, cachePath)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("cached dependency path escapes the project directory")
	}
	relative = filepath.ToSlash(relative)
	filesystem := lock.filesystem
	if filesystem == nil {
		filesystem = newOSRootFilesystem(lock.root)
	}
	info, err := filesystem.stat(relative)
	if err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("cached Manifest must be a regular file")
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("stat cached Manifest: %w", err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("cached Manifest with SHA-256 %s is missing; run mcl update", digest)
	}
	if filesystem.osBacked {
		if _, err := safepath.ResolveWithin(lock.root, cachePath); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("cached Manifest with SHA-256 %s is missing; run mcl update", digest)
			}
			if errors.Is(err, safepath.ErrOutsideRoot) {
				return nil, errors.New("cached Manifest escapes the project directory")
			}

			return nil, fmt.Errorf("resolve cached Manifest: %w", err)
		}
	}
	data, err := filesystem.readLimitedFile(relative, maximumSize)
	if err != nil {
		return nil, fmt.Errorf("read cached Manifest: %w", err)
	}

	return data, nil
}

func (lock *Lock) writeCache(digest string, data []byte) error {
	path := lock.cachePath(digest)
	directory := filepath.Dir(path)
	if err := ensureDirectoryWithin(lock.root, directory); err != nil {
		return fmt.Errorf("create dependency cache: %w", err)
	}

	return contentutil.WriteFileAtomic(path, data)
}

func ensureDirectoryWithin(root, target string) error {
	if !safepath.Within(root, target) {
		return errors.New("directory escapes the project directory")
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	current := root
	for part := range strings.SplitSeq(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("cache path contains a non-directory or symbolic link")
		}
	}

	return nil
}

func fetchRemote(ctx context.Context, source, category string) ([]byte, error) {
	transport, err := netpolicy.NewExternalHTTPTransport()
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("HTTPS redirects are not supported")
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, fmt.Errorf("create %s request: %w", category, err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch %s %q: %w", category, source, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s %q: unexpected HTTP status %s", category, source, response.Status)
	}
	maximumSize := int64(maxRemoteManifestSize)
	if category == "chart" {
		maximumSize = maxRemoteChartArchiveSize
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumSize+1))
	if err != nil {
		return nil, fmt.Errorf("read Manifest response: %w", err)
	}
	if int64(len(data)) > maximumSize {
		return nil, fmt.Errorf("%s response exceeds %d bytes", category, maximumSize)
	}

	return data, nil
}

func ValidatedManifestURL(source string) (*url.URL, error) {
	parsed, err := url.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("invalid Manifest URL: %w", err)
	}
	if parsed.Scheme != schemeHTTPS || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, errors.New("manifest URL must be an absolute HTTPS URL without user information or a fragment")
	}

	return parsed, nil
}

func SchemaSourceURL(source string) (string, string, error) {
	parsed, err := url.Parse(source)
	if err != nil {
		return "", "", fmt.Errorf("invalid Schema URL: %w", err)
	}
	if parsed.Scheme != schemeHTTPS || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return "", "", errors.New("schema URL must be an absolute HTTPS URL without user information")
	}
	resourceURL := parsed.String()
	content := parsed.Clone()
	content.Fragment = ""
	content.RawFragment = ""

	return content.String(), resourceURL, nil
}

func validOCIDigest(digest string) bool {
	value, found := strings.CutPrefix(digest, "sha256:")

	return found && contentutil.IsSHA256(value)
}

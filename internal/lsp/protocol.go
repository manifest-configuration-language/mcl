package lsp

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/pkg/mcl"
)

const (
	maximumMessageSize = 8 << 20
	maximumHeaderSize  = 8 << 10
)

// JSON-RPCとURI、MCL構文で共通して使う固定値。
const (
	jsonrpcVersion              = "2.0"
	uriSchemeFile               = "file"
	localhostHost               = "localhost"
	letKeyword                  = "let"
	fnKeyword                   = "fn"
	importKeyword               = "import"
	methodKey                   = "method"
	globPatternKey              = "globPattern"
	workspaceWatchedFilesMethod = "workspace/didChangeWatchedFiles"
	watchRegistrationID         = "mcl-didChangeWatchedFiles"
)

type request struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id"`
	Method  string         `json:"method"`
	Params  jsontext.Value `json:"params"`
	Result  jsontext.Value `json:"result"`
	Error   *protocolError `json:"error"`
}

type response struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id"`
	Result  any            `json:"result"`
	Error   *protocolError `json:"error,omitzero"`
}

type protocolError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeParams struct {
	RootURI          string            `json:"rootUri"`
	RootPath         string            `json:"rootPath"`
	WorkspaceFolders []workspaceFolder `json:"workspaceFolders"`
	Capabilities     struct {
		Workspace struct {
			DidChangeWatchedFiles struct {
				DynamicRegistration bool `json:"dynamicRegistration"`
			} `json:"didChangeWatchedFiles"`
		} `json:"workspace"`
	} `json:"capabilities"`
}

type workspaceFolder struct {
	URI string `json:"uri"`
}

type workspaceFolderChange struct {
	Event struct {
		Added   []workspaceFolder `json:"added"`
		Removed []workspaceFolder `json:"removed"`
	} `json:"event"`
}

type watchedFileChange struct {
	URI  string `json:"uri"`
	Type int    `json:"type"`
}

type watchedFilesParams struct {
	Changes []watchedFileChange `json:"changes"`
}

type formattingParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
}

func Run(input io.Reader, output io.Writer) error {
	s := server{reader: bufio.NewReaderSize(input, maximumHeaderSize), writer: output, documents: make(map[string]documentState), diagnosticURIs: make(map[string]struct{})}
	defer func() {
		if s.diagnosticCancel != nil {
			s.diagnosticCancel()
		}
	}()
	type messageResult struct {
		body []byte
		err  error
	}
	messages := make(chan messageResult)
	diagnostics := make(chan diagnosticResult)
	go func() {
		defer close(messages)
		for {
			body, err := s.readMessage()
			messages <- messageResult{body: body, err: err}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case message, ok := <-messages:
			if !ok || errors.Is(message.err, io.EOF) {
				return nil
			}
			if message.err != nil {
				return message.err
			}
			var req request
			if err := json.Unmarshal(message.body, &req); err != nil {
				if writeErr := s.write(response{JSONRPC: jsonrpcVersion, ID: jsontext.Value("null"), Error: &protocolError{Code: -32700, Message: "Invalid JSON"}}); writeErr != nil {
					return writeErr
				}

				continue
			}
			if err := s.handle(req, diagnostics); err != nil {
				return err
			}
			if req.Method == "exit" {
				if s.diagnosticCancel != nil {
					s.diagnosticCancel()
				}

				return nil
			}
		case result := <-diagnostics:
			if err := s.publishDiagnosticResult(result); err != nil {
				return err
			}
		}
	}
}

func (s *server) readMessage() ([]byte, error) {
	contentLength := -1
	headerSize := 0
	for {
		line, err := s.reader.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				return nil, errors.New("header line exceeds maximum size")
			}
			return nil, err
		}
		headerSize += len(line)
		if headerSize > maximumHeaderSize {
			return nil, errors.New("headers exceed maximum size")
		}
		headerLine := strings.TrimRight(string(line), "\r\n")
		if headerLine == "" {
			break
		}
		name, value, ok := strings.Cut(headerLine, ":")
		if ok && strings.EqualFold(name, "Content-Length") {
			if contentLength >= 0 {
				return nil, errors.New("duplicate Content-Length header")
			}
			contentLength, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil || contentLength < 0 || contentLength > maximumMessageSize {
				return nil, errors.New("invalid Content-Length header")
			}
		}
	}
	if contentLength < 0 {
		return nil, errors.New("missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(s.reader, body); err != nil {
		return nil, err
	}

	return body, nil
}

func (s *server) handle(req request, diagnostics chan<- diagnosticResult) error {
	if req.Method == "" {
		if strings.Trim(string(req.ID), "\"") == watchRegistrationID && s.watchRegistrationPending {
			s.watchRegistrationPending = false
			s.watchRegistered = req.Error == nil
		}

		return nil
	}
	switch req.Method {
	case "initialize":
		var params initializeParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.invalidParams(req)
		}
		s.canWatchMCLFiles = params.Capabilities.Workspace.DidChangeWatchedFiles.DynamicRegistration
		s.workspaceRoots = workspaceFolderPaths(params.WorkspaceFolders)
		if len(s.workspaceRoots) == 0 {
			if root := documentPath(params.RootURI); root != "" {
				s.workspaceRoots = []string{root}
			} else if params.RootPath != "" {
				if root, err := filepath.Abs(params.RootPath); err == nil {
					s.workspaceRoots = []string{root}
				}
			}
		}

		return s.write(response{JSONRPC: jsonrpcVersion, ID: req.ID, Result: map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync":           map[string]any{"openClose": true, "change": 1, "save": true},
				"completionProvider":         map[string]any{"triggerCharacters": []string{"."}},
				"documentFormattingProvider": true,
				"definitionProvider":         true,
				"renameProvider":             true,
				"workspace":                  map[string]any{"workspaceFolders": map[string]any{"supported": true, "changeNotifications": true}},
			},
			"serverInfo": map[string]any{"name": "mcl", "version": mcl.ToolchainVersion},
		}})
	case "textDocument/completion":
		var params completionParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.invalidParams(req)
		}
		items := s.completions(params)
		if state, exists := s.documents[params.TextDocument.URI]; exists && state.SyncLost {
			items = []completionItem{}
		}

		return s.write(response{JSONRPC: jsonrpcVersion, ID: req.ID, Result: map[string]any{"isIncomplete": false, "items": items}})
	case "textDocument/formatting":
		var params formattingParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.invalidParams(req)
		}

		result := s.formatDocument(params)
		if state, exists := s.documents[params.TextDocument.URI]; exists && state.SyncLost {
			result = []textEdit{}
		}

		return s.write(response{JSONRPC: jsonrpcVersion, ID: req.ID, Result: result})
	case "textDocument/definition":
		var params definitionParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.invalidParams(req)
		}

		result := s.definition(params)
		if state, exists := s.documents[params.TextDocument.URI]; exists && state.SyncLost {
			result = nil
		}

		return s.write(response{JSONRPC: jsonrpcVersion, ID: req.ID, Result: result})
	case "textDocument/rename":
		var params renameParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.invalidParams(req)
		}

		result := s.rename(params)
		if state, exists := s.documents[params.TextDocument.URI]; exists && state.SyncLost {
			result = nil
		}

		return s.write(response{JSONRPC: jsonrpcVersion, ID: req.ID, Result: result})
	case "workspace/didChangeWorkspaceFolders":
		var params workspaceFolderChange
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.invalidParams(req)
		}
		removed := make(map[string]struct{}, len(params.Event.Removed))
		for _, folder := range params.Event.Removed {
			if path := documentPath(folder.URI); path != "" {
				removed[filepath.Clean(path)] = struct{}{}
			}
		}
		roots := make([]string, 0, len(s.workspaceRoots)+len(params.Event.Added))
		for _, root := range s.workspaceRoots {
			if _, exists := removed[filepath.Clean(root)]; !exists {
				roots = append(roots, root)
			}
		}
		roots = append(roots, workspaceFolderPaths(params.Event.Added)...)
		slices.Sort(roots)
		s.workspaceRoots = slices.Compact(roots)
		s.watchRegistrationPending = false
		s.watchRegistered = false
		if s.renameIndex != nil {
			s.renameIndex.Invalidate()
		}

		return nil
	case "workspace/didChangeWatchedFiles":
		var params watchedFilesParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.invalidParams(req)
		}
		changes := make([]engine.DependentMCLFileChange, 0, len(params.Changes))
		changedURIs := make([]string, 0, len(params.Changes))
		invalidateRenameIndex := false
		for _, change := range params.Changes {
			path := documentPath(change.URI)
			if path == "" || change.Type < 1 || change.Type > 3 {
				continue
			}
			changedURIs = append(changedURIs, change.URI)
			if filepath.Base(path) == "mcl.toml" {
				invalidateRenameIndex = true

				continue
			}
			if filepath.Ext(path) != ".mcl" {
				continue
			}
			changes = append(changes, engine.DependentMCLFileChange{Path: path, Deleted: change.Type == 3})
		}
		if s.watchRegistered && s.renameIndex != nil {
			if invalidateRenameIndex {
				s.renameIndex.Invalidate()
			} else if err := s.renameIndex.ApplyFileChanges(changes); err != nil {
				return err
			}
		}
		if len(changedURIs) > 0 {
			s.scheduleDiagnosticsForChanges(changedURIs, diagnostics)
		}

		return nil
	case "shutdown":
		return s.write(response{JSONRPC: jsonrpcVersion, ID: req.ID, Result: nil})
	case "textDocument/didOpen":
		var params documentParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.invalidParams(req)
		}
		uri := params.TextDocument.URI
		if previous, exists := s.documents[uri]; exists && params.TextDocument.Version <= previous.Version {
			return nil
		}
		s.documents[uri] = documentState{Text: params.TextDocument.Text, Version: params.TextDocument.Version}

		s.scheduleDiagnostics(uri, diagnostics)

		return nil
	case "textDocument/didChange":
		var params documentParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.invalidParams(req)
		}
		if len(params.ContentChanges) == 0 {
			return nil
		}
		uri := params.TextDocument.URI
		state, exists := s.documents[uri]
		if exists && params.TextDocument.Version <= state.Version {
			return nil
		}
		if len(params.ContentChanges) != 1 || params.ContentChanges[0].Range != nil {
			if exists {
				state.SyncLost = true
				state.Diagnostics = nil
				s.documents[uri] = state
			}
			s.scheduleStaticDiagnostics(uri, diagnostics)

			return nil
		}
		updated, ok := applyContentChanges(state.Text, params.ContentChanges)
		if !ok {
			state.SyncLost = true
			state.Diagnostics = nil
			s.documents[uri] = state
			s.scheduleStaticDiagnostics(uri, diagnostics)

			return nil
		}
		s.documents[uri] = documentState{Text: updated, Version: params.TextDocument.Version, Dependencies: state.Dependencies, Diagnostics: state.Diagnostics}

		s.scheduleStaticDiagnostics(uri, diagnostics)

		return nil
	case "textDocument/didSave":
		var params documentParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.invalidParams(req)
		}
		if _, exists := s.documents[params.TextDocument.URI]; exists {
			s.scheduleDiagnostics(params.TextDocument.URI, diagnostics)
		}

		return nil
	case "textDocument/didClose":
		var params documentParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.invalidParams(req)
		}
		delete(s.documents, params.TextDocument.URI)

		s.scheduleDiagnostics(params.TextDocument.URI, diagnostics)

		return nil
	case "initialized":
		return s.registerMCLFileWatcher()
	case "exit":
		return nil
	default:
		if len(req.ID) != 0 {
			return s.write(response{JSONRPC: jsonrpcVersion, ID: req.ID, Error: &protocolError{Code: -32601, Message: "Method not found"}})
		}

		return nil
	}
}

func (s *server) invalidParams(req request) error {
	if len(req.ID) == 0 {
		return nil
	}

	return s.write(response{JSONRPC: jsonrpcVersion, ID: req.ID, Error: &protocolError{Code: -32602, Message: "Invalid params"}})
}

func (s *server) registerMCLFileWatcher() error {
	if !s.canWatchMCLFiles || s.watchRegistrationPending || s.watchRegistered {
		return nil
	}
	s.watchRegistrationPending = true
	request := map[string]any{
		"jsonrpc": jsonrpcVersion,
		"id":      jsontext.Value("\"" + watchRegistrationID + "\""),
		methodKey: "client/registerCapability",
		"params": map[string]any{"registrations": []map[string]any{{
			"id":      watchRegistrationID,
			methodKey: workspaceWatchedFilesMethod,
			"registerOptions": map[string]any{"watchers": []map[string]any{
				{globPatternKey: "**/*.mcl", "kind": 7},
				{globPatternKey: "mcl.toml", "kind": 7},
				{globPatternKey: "**/mcl.toml", "kind": 7},
			}},
		}}},
	}
	if err := s.write(request); err != nil {
		s.watchRegistrationPending = false

		return err
	}

	return nil
}

func (s *server) notify(method string, params any) error {
	return s.write(map[string]any{"jsonrpc": jsonrpcVersion, "method": method, "params": params})
}

func (s *server) write(message any) error {
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	frame := make([]byte, 0, len(body)+40)
	frame = fmt.Appendf(frame, "Content-Length: %d\r\n\r\n", len(body))
	frame = append(frame, body...)
	_, err = io.Copy(s.writer, bytes.NewReader(frame))

	return err
}

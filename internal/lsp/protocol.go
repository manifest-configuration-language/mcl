package lsp

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"path/filepath"
	"slices"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/pkg/mcl"
	"go.lsp.dev/jsonrpc2"
)

const (
	// Bound allocations made while reading frames from the editor process.
	maximumMessageSize = 8 << 20
	maximumHeaderSize  = 8 << 10
)

// JSON-RPCとURI、MCL構文で共通して使う固定値。
const (
	uriSchemeFile               = "file"
	localhostHost               = "localhost"
	letKeyword                  = "let"
	fnKeyword                   = "fn"
	importKeyword               = "import"
	globPatternKey              = "globPattern"
	workspaceWatchedFilesMethod = "workspace/didChangeWatchedFiles"
	watchRegistrationID         = "mcl-didChangeWatchedFiles"
)

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

func (s *server) handle(method string, rawParams jsontext.Value, notification bool, ctx context.Context) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.handleLocked(method, rawParams, notification, ctx)
}

func (s *server) handleLocked(method string, rawParams jsontext.Value, notification bool, ctx context.Context) (any, error) {
	diagnostics := s.diagnosticResults
	switch method {
	case "initialize":
		var params initializeParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
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

		return map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync":           map[string]any{"openClose": true, "change": 1, "save": true},
				"completionProvider":         map[string]any{"triggerCharacters": []string{"."}},
				"documentFormattingProvider": true,
				"definitionProvider":         true,
				"renameProvider":             true,
				"workspace":                  map[string]any{"workspaceFolders": map[string]any{"supported": true, "changeNotifications": true}},
			},
			"serverInfo": map[string]any{"name": "mcl", "version": mcl.ToolchainVersion},
		}, nil
	case "textDocument/completion":
		var params completionParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}
		//nolint:contextcheck // The engine completion APIs do not accept a context.
		items := s.completions(params)
		if state, exists := s.documents[params.TextDocument.URI]; exists && state.SyncLost {
			items = []completionItem{}
		}

		return map[string]any{"isIncomplete": false, "items": items}, nil
	case "textDocument/formatting":
		var params formattingParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}

		result := s.formatDocument(params)
		if state, exists := s.documents[params.TextDocument.URI]; exists && state.SyncLost {
			result = []textEdit{}
		}

		return result, nil
	case "textDocument/definition":
		var params definitionParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}

		result := s.definition(params)
		if state, exists := s.documents[params.TextDocument.URI]; exists && state.SyncLost {
			result = nil
		}

		return result, nil
	case "textDocument/rename":
		var params renameParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}

		//nolint:contextcheck // Schema completion APIs used by rename do not accept a context.
		result := s.rename(params)
		if state, exists := s.documents[params.TextDocument.URI]; exists && state.SyncLost {
			result = nil
		}

		return result, nil
	case "workspace/didChangeWorkspaceFolders":
		var params workspaceFolderChange
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
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
		s.watchRegistrationGen++
		if s.renameIndex != nil {
			s.renameIndex.Invalidate()
		}

		return nil, nil
	case "workspace/didChangeWatchedFiles":
		var params watchedFilesParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
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
				return nil, err
			}
		}
		if len(changedURIs) > 0 {
			s.scheduleDiagnosticsForChanges(context.WithoutCancel(ctx), changedURIs, diagnostics)
		}

		return nil, nil
	case "shutdown":
		return nil, nil
	case "textDocument/didOpen":
		var params documentParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}
		uri := params.TextDocument.URI
		if previous, exists := s.documents[uri]; exists && params.TextDocument.Version <= previous.Version {
			return nil, nil
		}
		s.documents[uri] = documentState{Text: params.TextDocument.Text, Version: params.TextDocument.Version}

		s.scheduleDiagnostics(context.WithoutCancel(ctx), uri, diagnostics)

		return nil, nil
	case "textDocument/didChange":
		var params documentParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}
		if len(params.ContentChanges) == 0 {
			return nil, nil
		}
		uri := params.TextDocument.URI
		state, exists := s.documents[uri]
		if exists && params.TextDocument.Version <= state.Version {
			return nil, nil
		}
		if len(params.ContentChanges) != 1 || params.ContentChanges[0].Range != nil {
			if exists {
				state.SyncLost = true
				state.Diagnostics = nil
				s.documents[uri] = state
			}
			s.scheduleStaticDiagnostics(context.WithoutCancel(ctx), uri, diagnostics)

			return nil, nil
		}
		updated, ok := applyContentChanges(state.Text, params.ContentChanges)
		if !ok {
			state.SyncLost = true
			state.Diagnostics = nil
			s.documents[uri] = state
			s.scheduleStaticDiagnostics(context.WithoutCancel(ctx), uri, diagnostics)

			return nil, nil
		}
		s.documents[uri] = documentState{Text: updated, Version: params.TextDocument.Version, Dependencies: state.Dependencies, Diagnostics: state.Diagnostics}

		s.scheduleStaticDiagnostics(context.WithoutCancel(ctx), uri, diagnostics)

		return nil, nil
	case "textDocument/didSave":
		var params documentParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}
		if _, exists := s.documents[params.TextDocument.URI]; exists {
			s.scheduleDiagnostics(context.WithoutCancel(ctx), params.TextDocument.URI, diagnostics)
		}

		return nil, nil
	case "textDocument/didClose":
		var params documentParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}
		delete(s.documents, params.TextDocument.URI)

		s.scheduleDiagnostics(context.WithoutCancel(ctx), params.TextDocument.URI, diagnostics)

		return nil, nil
	case "initialized":
		s.registerMCLFileWatcher()

		return nil, nil
	case "exit":
		select {
		case s.exitRequested <- struct{}{}:
		default:
		}

		return nil, nil
	default:
		if notification {
			return nil, nil
		}

		return nil, jsonrpc2.NewError(jsonrpc2.MethodNotFound, "Method not found")
	}
}

func (s *server) registerMCLFileWatcher() {
	if !s.canWatchMCLFiles || s.watchRegistrationPending || s.watchRegistered {
		return
	}
	s.watchRegistrationPending = true
	s.watchRegistrationGen++
	generation := s.watchRegistrationGen
	params := map[string]any{"registrations": []map[string]any{{
		"id":     watchRegistrationID,
		"method": workspaceWatchedFilesMethod,
		"registerOptions": map[string]any{"watchers": []map[string]any{
			{globPatternKey: "**/*.mcl", "kind": 7},
			{globPatternKey: "mcl.toml", "kind": 7},
			{globPatternKey: "**/mcl.toml", "kind": 7},
		}},
	}}}
	go func() {
		err := s.callback("client/registerCapability", params)
		success := err == nil
		result := watchRegistrationResult{generation: generation, success: success}
		select {
		case s.watchRegistrationResults <- result:
		case <-s.rpcDone:
		}
	}()
}

func invalidParamsError() error {
	return jsonrpc2.NewError(jsonrpc2.InvalidParams, "Invalid params")
}

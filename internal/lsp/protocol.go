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
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
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

type initializeParams = protocol.InitializeParams

type workspaceFolder = protocol.WorkspaceFolder
type workspaceFolderChange = protocol.DidChangeWorkspaceFoldersParams

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
	if isReadOnlyDocumentRequest(method) {
		s.mu.Lock()
		documents := make(map[string]documentState, len(s.documents))
		for uri, state := range s.documents {
			documents[uri] = state
		}
		s.mu.Unlock()

		return (&server{documents: documents}).handleLocked(method, rawParams, notification, ctx)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.handleLocked(method, rawParams, notification, ctx)
}

func isReadOnlyDocumentRequest(method string) bool {
	switch method {
	case "textDocument/completion", "textDocument/formatting", "textDocument/definition":
		return true
	default:
		return false
	}
}

func (s *server) dispatchReadOnly(result any, handler func(*server) (any, error)) error {
	s.mu.Lock()
	documents := make(map[string]documentState, len(s.documents))
	for uri, document := range s.documents {
		documents[uri] = document
	}
	s.mu.Unlock()
	state := &server{documents: documents}

	value, err := handler(state)
	if err != nil || result == nil {
		return err
	}
	rawResult, err := protocol.Marshal(value)
	if err != nil {
		return err
	}

	return protocol.Unmarshal(rawResult, result)
}

func (s *server) Initialize(ctx context.Context, params *protocol.InitializeParams) (*protocol.InitializeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initializeLocked(params), nil
}

func (s *server) Initialized(ctx context.Context, params *protocol.InitializedParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registerMCLFileWatcher()
	return nil
}

func (s *server) Shutdown(ctx context.Context) error {
	return nil
}

func (s *server) Exit(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestExit()
	return nil
}

func (s *server) Completion(ctx context.Context, params *protocol.CompletionParams) (protocol.CompletionResult, error) {
	var result protocol.CompletionResult
	err := s.dispatchReadOnly(&result, func(state *server) (any, error) {
		request := completionParams{}
		request.TextDocument.URI = string(params.TextDocument.URI)
		request.Position.Line = int(params.Position.Line)
		request.Position.Character = int(params.Position.Character)
		items := state.completions(request)
		if document, exists := state.documents[request.TextDocument.URI]; exists && document.SyncLost {
			items = []completionItem{}
		}

		return map[string]any{"isIncomplete": false, "items": items}, nil
	})

	return result, err
}

func (s *server) Formatting(ctx context.Context, params *protocol.DocumentFormattingParams) ([]protocol.TextEdit, error) {
	var result []protocol.TextEdit
	err := s.dispatchReadOnly(&result, func(state *server) (any, error) {
		request := formattingParams{}
		request.TextDocument.URI = string(params.TextDocument.URI)
		edits := state.formatDocument(request)
		if document, exists := state.documents[request.TextDocument.URI]; exists && document.SyncLost {
			edits = []textEdit{}
		}

		return edits, nil
	})

	return result, err
}

func (s *server) Definition(ctx context.Context, params *protocol.DefinitionParams) (protocol.DefinitionResult, error) {
	var result protocol.DefinitionResult
	err := s.dispatchReadOnly(&result, func(state *server) (any, error) {
		request := definitionParams{}
		request.TextDocument.URI = string(params.TextDocument.URI)
		request.Position.Line = int(params.Position.Line)
		request.Position.Character = int(params.Position.Character)
		location := state.definition(request)
		if document, exists := state.documents[request.TextDocument.URI]; exists && document.SyncLost {
			location = nil
		}

		return location, nil
	})

	return result, err
}

func (s *server) Rename(ctx context.Context, params *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var request renameParams
	if params != nil {
		request.TextDocument.URI = params.TextDocument.URI.String()
		request.Position = location{Line: int(params.Position.Line), Character: int(params.Position.Character)}
		request.NewName = params.NewName
	}
	edit := s.rename(request)
	if state, exists := s.documents[request.TextDocument.URI]; exists && state.SyncLost {
		edit = nil
	}
	return protocolWorkspaceEdit(edit), nil
}

func (s *server) DidChangeWorkspaceFolders(ctx context.Context, params *protocol.DidChangeWorkspaceFoldersParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.didChangeWorkspaceFoldersLocked(params)
	return nil
}

func (s *server) DidChangeWatchedFiles(ctx context.Context, params *protocol.DidChangeWatchedFilesParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var request watchedFilesParams
	if params != nil {
		request.Changes = make([]watchedFileChange, 0, len(params.Changes))
		for _, change := range params.Changes {
			request.Changes = append(request.Changes, watchedFileChange{URI: change.URI.String(), Type: int(change.Type)})
		}
	}
	return s.didChangeWatchedFilesLocked(ctx, request)
}

func (s *server) DidOpen(ctx context.Context, params *protocol.DidOpenTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var request documentParams
	if params != nil {
		request.TextDocument.URI = params.TextDocument.URI.String()
		request.TextDocument.Text = params.TextDocument.Text
		request.TextDocument.Version = int(params.TextDocument.Version)
	}
	s.didOpenLocked(ctx, request)
	return nil
}

func (s *server) DidChange(ctx context.Context, params *protocol.DidChangeTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var request documentParams
	if params != nil {
		changes := make([]contentChange, 0, len(params.ContentChanges))
		for _, change := range params.ContentChanges {
			var converted contentChange
			switch change := change.(type) {
			case *protocol.TextDocumentContentChangeWholeDocument:
				if change != nil {
					converted.Text = change.Text
				}
			case *protocol.TextDocumentContentChangePartial:
				if change != nil {
					converted.Text = change.Text
					converted.Range = &textRange{}
				}
			default:
				converted.Range = &textRange{}
			}
			changes = append(changes, converted)
		}
		request.TextDocument.URI = params.TextDocument.URI.String()
		request.TextDocument.Version = int(params.TextDocument.Version)
		request.ContentChanges = changes
	}
	s.didChangeLocked(ctx, request)
	return nil
}

func (s *server) DidSave(ctx context.Context, params *protocol.DidSaveTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	uri := ""
	if params != nil {
		uri = params.TextDocument.URI.String()
	}
	s.didSaveLocked(ctx, uri)
	return nil
}

func (s *server) DidClose(ctx context.Context, params *protocol.DidCloseTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	uri := ""
	if params != nil {
		uri = params.TextDocument.URI.String()
	}
	s.didCloseLocked(ctx, uri)
	return nil
}

func (s *server) Request(_ context.Context, method string, _ any) (any, error) {
	return nil, jsonrpc2.NewError(jsonrpc2.MethodNotFound, "Method not found: "+method)
}

func (s *server) initializeLocked(params *protocol.InitializeParams) *protocol.InitializeResult {
	if params == nil {
		params = &protocol.InitializeParams{}
	}
	if watchedFiles := params.Capabilities.Workspace.DidChangeWatchedFiles; watchedFiles != nil && watchedFiles.DynamicRegistration != nil {
		s.canWatchMCLFiles = *watchedFiles.DynamicRegistration
	}
	workspaceFolders, _ := params.WorkspaceFolders.Get()
	s.workspaceRoots = workspaceFolderPaths(workspaceFolders)
	if len(s.workspaceRoots) == 0 {
		rootURI := ""
		if params.RootURI != nil {
			rootURI = params.RootURI.String()
		}
		if root := documentPath(rootURI); root != "" {
			s.workspaceRoots = []string{root}
		} else if rootPath, ok := params.RootPath.Get(); ok && rootPath != "" {
			if root, err := filepath.Abs(rootPath); err == nil {
				s.workspaceRoots = []string{root}
			}
		}
	}
	changeKind := protocol.TextDocumentSyncKindFull
	return &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync: &protocol.TextDocumentSyncOptions{
				OpenClose: boolPointer(true),
				Change:    &changeKind,
				Save:      protocol.Boolean(true),
			},
			CompletionProvider:         &protocol.CompletionOptions{TriggerCharacters: []string{"."}},
			DocumentFormattingProvider: protocol.Boolean(true),
			DefinitionProvider:         protocol.Boolean(true),
			RenameProvider:             protocol.Boolean(true),
			Workspace: &protocol.WorkspaceOptions{WorkspaceFolders: &protocol.WorkspaceFoldersServerCapabilities{
				Supported:           boolPointer(true),
				ChangeNotifications: protocol.Boolean(true),
			}},
		},
		ServerInfo: protocol.ServerInfo{Name: "mcl", Version: protocol.NewOptional(mcl.ToolchainVersion)},
	}
}

func boolPointer(value bool) *bool { return &value }

func protocolWorkspaceEdit(edit *workspaceEdit) *protocol.WorkspaceEdit {
	result := &protocol.WorkspaceEdit{}
	if edit == nil {
		return result
	}
	result.Changes = make(map[uri.URI][]protocol.TextEdit, len(edit.Changes))
	for documentURI, edits := range edit.Changes {
		converted := make([]protocol.TextEdit, len(edits))
		for index, edit := range edits {
			converted[index] = protocol.TextEdit{
				Range: protocol.Range{
					Start: protocol.Position{Line: uint32(edit.Range.Start.Line), Character: uint32(edit.Range.Start.Character)},
					End:   protocol.Position{Line: uint32(edit.Range.End.Line), Character: uint32(edit.Range.End.Character)},
				},
				NewText: edit.NewText,
			}
		}
		result.Changes[uri.URI(documentURI)] = converted
	}
	return result
}

func (s *server) didChangeWorkspaceFoldersLocked(params *protocol.DidChangeWorkspaceFoldersParams) {
	if params == nil {
		return
	}
	removed := make(map[string]struct{}, len(params.Event.Removed))
	for _, folder := range params.Event.Removed {
		if path := documentPath(folder.URI.String()); path != "" {
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
}

func (s *server) didChangeWatchedFilesLocked(ctx context.Context, params watchedFilesParams) error {
	changes := make([]engine.DependentMCLFileChange, 0, len(params.Changes))
	changedURIs := make([]string, 0, len(params.Changes))
	invalidateRenameIndex := false
	for _, change := range params.Changes {
		uri := change.URI
		path := documentPath(uri)
		if path == "" || change.Type < 1 || change.Type > 3 {
			continue
		}
		changedURIs = append(changedURIs, uri)
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
		s.scheduleDiagnosticsForChanges(context.WithoutCancel(ctx), changedURIs, s.diagnosticResults)
	}
	return nil
}

func (s *server) didOpenLocked(ctx context.Context, params documentParams) {
	diagnostics := s.diagnosticResults
	uri := params.TextDocument.URI
	if previous, exists := s.documents[uri]; exists && params.TextDocument.Version <= previous.Version {
		return
	}
	s.documents[uri] = documentState{Text: params.TextDocument.Text, Version: params.TextDocument.Version}
	s.scheduleDiagnostics(context.WithoutCancel(ctx), uri, diagnostics)
}

func (s *server) didChangeLocked(ctx context.Context, params documentParams) {
	diagnostics := s.diagnosticResults
	if len(params.ContentChanges) == 0 {
		return
	}
	uri := params.TextDocument.URI
	state, exists := s.documents[uri]
	if exists && params.TextDocument.Version <= state.Version {
		return
	}
	if len(params.ContentChanges) != 1 || params.ContentChanges[0].Range != nil {
		if exists {
			state.SyncLost = true
			state.Diagnostics = nil
			s.documents[uri] = state
		}
		s.scheduleStaticDiagnostics(context.WithoutCancel(ctx), uri, diagnostics)
		return
	}
	updated, ok := applyContentChanges(state.Text, params.ContentChanges)
	if !ok {
		state.SyncLost = true
		state.Diagnostics = nil
		s.documents[uri] = state
		s.scheduleStaticDiagnostics(context.WithoutCancel(ctx), uri, diagnostics)
		return
	}
	s.documents[uri] = documentState{Text: updated, Version: params.TextDocument.Version, Dependencies: state.Dependencies, Diagnostics: state.Diagnostics}
	s.scheduleStaticDiagnostics(context.WithoutCancel(ctx), uri, diagnostics)
}

func (s *server) didSaveLocked(ctx context.Context, uri string) {
	if _, exists := s.documents[uri]; exists {
		s.scheduleDiagnostics(context.WithoutCancel(ctx), uri, s.diagnosticResults)
	}
}

func (s *server) didCloseLocked(ctx context.Context, uri string) {
	delete(s.documents, uri)
	s.scheduleDiagnostics(context.WithoutCancel(ctx), uri, s.diagnosticResults)
}

func (s *server) requestExit() {
	select {
	case s.exitRequested <- struct{}{}:
	default:
	}
}

func (s *server) handleLocked(method string, rawParams jsontext.Value, notification bool, ctx context.Context) (any, error) {
	switch method {
	case "initialize":
		var params initializeParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}
		return s.initializeLocked(&params), nil
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
		s.didChangeWorkspaceFoldersLocked(&params)
		return nil, nil
	case "workspace/didChangeWatchedFiles":
		var params watchedFilesParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}
		if err := s.didChangeWatchedFilesLocked(ctx, params); err != nil {
			return nil, err
		}
		return nil, nil
	case "shutdown":
		return nil, nil
	case "textDocument/didOpen":
		var params documentParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}
		s.didOpenLocked(ctx, params)
		return nil, nil
	case "textDocument/didChange":
		var params documentParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}
		s.didChangeLocked(ctx, params)
		return nil, nil
	case "textDocument/didSave":
		var params documentParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}
		s.didSaveLocked(ctx, params.TextDocument.URI)
		return nil, nil
	case "textDocument/didClose":
		var params documentParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, invalidParamsError()
		}
		s.didCloseLocked(ctx, params.TextDocument.URI)
		return nil, nil
	case "initialized":
		s.registerMCLFileWatcher()
		return nil, nil
	case "exit":
		s.requestExit()
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

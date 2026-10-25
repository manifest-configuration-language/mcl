package lsp

import (
	"context"
	"maps"
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

type workspaceFolder = protocol.WorkspaceFolder

func (s *server) readOnlySnapshot() *server {
	s.mu.Lock()
	if s.renameIndex == nil {
		s.renameIndex = &engine.DependentMCLSourceIndex{}
	}
	documents := make(map[string]documentState, len(s.documents))
	maps.Copy(documents, s.documents)
	workspaceRoots := slices.Clone(s.workspaceRoots)
	watchRegistered := s.watchRegistered
	renameIndex := s.renameIndex
	stateGeneration := s.stateGeneration
	s.mu.Unlock()

	return &server{documents: documents, workspaceRoots: workspaceRoots, watchRegistered: watchRegistered, renameIndex: renameIndex, stateGeneration: stateGeneration}
}

func (s *server) snapshotForRequest(ctx context.Context) *server {
	if snapshot, ok := ctx.Value(requestSnapshotContextKey{}).(*server); ok && snapshot != nil {
		return snapshot
	}

	return s.readOnlySnapshot()
}

func protocolCompletionItems(items []completionItem) protocol.CompletionItemSlice {
	result := make(protocol.CompletionItemSlice, len(items))
	for i, item := range items {
		result[i] = protocol.CompletionItem{Label: item.Label, Kind: protocolCompletionKind(item.Kind)}
		if item.InsertText != "" {
			result[i].InsertText = protocol.NewOptional(item.InsertText)
		}
		if item.Detail != "" {
			result[i].Detail = protocol.NewOptional(item.Detail)
		}
		if item.Documentation != "" {
			result[i].Documentation = protocol.String(item.Documentation)
		}
	}

	return result
}

func protocolCompletionKind(kind int) protocol.CompletionItemKind {
	switch kind {
	case 3:
		return protocol.CompletionItemKindFunction
	case 6:
		return protocol.CompletionItemKindVariable
	case 10:
		return protocol.CompletionItemKindProperty
	case 14:
		return protocol.CompletionItemKindKeyword
	default:
		return protocol.CompletionItemKindText
	}
}

func protocolTextEdits(edits []textEdit) []protocol.TextEdit {
	result := make([]protocol.TextEdit, len(edits))
	for i, edit := range edits {
		result[i] = protocol.TextEdit{
			Range: protocol.Range{
				Start: protocol.Position{Line: protocolPosition(edit.Range.Start.Line), Character: protocolPosition(edit.Range.Start.Character)},
				End:   protocol.Position{Line: protocolPosition(edit.Range.End.Line), Character: protocolPosition(edit.Range.End.Character)},
			},
			NewText: edit.NewText,
		}
	}

	return result
}

func protocolDefinitionResult(location *definitionLocation) protocol.DefinitionResult {
	if location == nil {
		return nil
	}

	return &protocol.Location{
		URI: uri.URI(location.URI),
		Range: protocol.Range{
			Start: protocol.Position{Line: protocolPosition(location.Range.Start.Line), Character: protocolPosition(location.Range.Start.Character)},
			End:   protocol.Position{Line: protocolPosition(location.Range.End.Line), Character: protocolPosition(location.Range.End.Character)},
		},
	}
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
	state := s.snapshotForRequest(ctx)
	if params == nil {
		return protocol.CompletionItemSlice{}, nil
	}
	request := completionParams{}
	request.TextDocument.URI = string(params.TextDocument.URI)
	request.Position.Line = int(params.Position.Line)
	request.Position.Character = int(params.Position.Character)
	items := state.completions(ctx, request)
	if document, exists := state.documents[request.TextDocument.URI]; exists && document.SyncLost {
		items = []completionItem{}
	}
	if ctx.Err() != nil {
		return nil, protocol.ErrRequestCancelled
	}
	if !s.documentSnapshotIsCurrent(state) {
		return nil, protocol.ErrContentModified
	}

	return &protocol.CompletionList{IsIncomplete: false, Items: protocolCompletionItems(items)}, nil
}

func (s *server) Formatting(ctx context.Context, params *protocol.DocumentFormattingParams) ([]protocol.TextEdit, error) {
	state := s.snapshotForRequest(ctx)
	if params == nil {
		return []protocol.TextEdit{}, nil
	}
	uri := params.TextDocument.URI.String()
	edits := state.formatDocument(uri)
	if document, exists := state.documents[uri]; exists && document.SyncLost {
		edits = []textEdit{}
	}
	if ctx.Err() != nil {
		return nil, protocol.ErrRequestCancelled
	}
	if !s.documentSnapshotIsCurrent(state) {
		return nil, protocol.ErrContentModified
	}

	return protocolTextEdits(edits), nil
}

func (s *server) Definition(ctx context.Context, params *protocol.DefinitionParams) (protocol.DefinitionResult, error) {
	state := s.snapshotForRequest(ctx)
	if params == nil {
		return nil, nil
	}
	request := definitionParams{}
	request.TextDocument.URI = string(params.TextDocument.URI)
	request.Position.Line = int(params.Position.Line)
	request.Position.Character = int(params.Position.Character)
	location := state.definition(request)
	if document, exists := state.documents[request.TextDocument.URI]; exists && document.SyncLost {
		location = nil
	}
	if ctx.Err() != nil {
		return nil, protocol.ErrRequestCancelled
	}
	if !s.documentSnapshotIsCurrent(state) {
		return nil, protocol.ErrContentModified
	}

	return protocolDefinitionResult(location), nil
}

func (s *server) Rename(ctx context.Context, params *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	if ctx.Err() != nil {
		return nil, protocol.ErrRequestCancelled
	}
	state := s.snapshotForRequest(ctx)
	var request renameParams
	if params != nil {
		request.TextDocument.URI = params.TextDocument.URI.String()
		request.Position = location{Line: int(params.Position.Line), Character: int(params.Position.Character)}
		request.NewName = params.NewName
	}

	edit := state.renameWithContext(ctx, request)
	if ctx.Err() != nil {
		return nil, protocol.ErrRequestCancelled
	}
	if document, exists := state.documents[request.TextDocument.URI]; exists && document.SyncLost {
		edit = nil
	}
	if !s.documentSnapshotIsCurrent(state) {
		return nil, protocol.ErrContentModified
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

	if params == nil {
		params = &protocol.DidChangeWatchedFilesParams{}
	}

	return s.didChangeWatchedFilesLocked(ctx, params)
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
					converted.Range = &textRange{
						Start: location{Line: int(change.Range.Start.Line), Character: int(change.Range.Start.Character)},
						End:   location{Line: int(change.Range.End.Line), Character: int(change.Range.End.Character)},
					}
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
	if workspace := params.Capabilities.Workspace; workspace != nil {
		if watchedFiles := workspace.DidChangeWatchedFiles; watchedFiles != nil && watchedFiles.DynamicRegistration != nil {
			s.canWatchMCLFiles = *watchedFiles.DynamicRegistration
		}
	}
	workspaceFolders, _ := params.WorkspaceFolders.Get()
	s.workspaceRoots = workspaceFolderPaths(workspaceFolders)
	if len(s.workspaceRoots) == 0 {
		rootURI := ""
		//nolint:staticcheck // RootURI remains a compatibility fallback for clients without workspaceFolders or rootUri.
		if params.RootURI != nil {
			//nolint:staticcheck // RootURI remains a compatibility fallback for clients without workspaceFolders or rootUri.
			rootURI = params.RootURI.String()
		}
		if root := documentPath(rootURI); root != "" {
			s.workspaceRoots = []string{root}
		} else {
			//nolint:staticcheck // RootPath remains a compatibility fallback for clients without workspaceFolders or rootUri.
			if rootPath, ok := params.RootPath.Get(); ok && rootPath != "" {
				if root, err := filepath.Abs(rootPath); err == nil {
					s.workspaceRoots = []string{root}
				}
			}
		}
	}
	changeKind := protocol.TextDocumentSyncKindFull

	return &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync: &protocol.TextDocumentSyncOptions{
				OpenClose: new(true),
				Change:    &changeKind,
				Save:      protocol.Boolean(true),
			},
			CompletionProvider:         &protocol.CompletionOptions{TriggerCharacters: []string{"."}},
			DocumentFormattingProvider: protocol.Boolean(true),
			DefinitionProvider:         protocol.Boolean(true),
			ReferencesProvider:         protocol.Boolean(true),
			RenameProvider:             protocol.Boolean(true),
			Workspace: &protocol.WorkspaceOptions{WorkspaceFolders: &protocol.WorkspaceFoldersServerCapabilities{
				Supported:           new(true),
				ChangeNotifications: protocol.Boolean(true),
			}},
		},
		ServerInfo: protocol.ServerInfo{Name: "mcl", Version: protocol.NewOptional(mcl.ToolchainVersion)},
	}
}

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
					Start: protocol.Position{Line: protocolPosition(edit.Range.Start.Line), Character: protocolPosition(edit.Range.Start.Character)},
					End:   protocol.Position{Line: protocolPosition(edit.Range.End.Line), Character: protocolPosition(edit.Range.End.Character)},
				},
				NewText: edit.NewText,
			}
		}
		result.Changes[uri.URI(documentURI)] = converted
	}

	return result
}

func protocolPosition(value int) uint32 {
	if value < 0 {
		return 0
	}
	if uint64(value) > uint64(^uint32(0)) {
		return ^uint32(0)
	}

	return uint32(value)
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
	s.stateGeneration++
	if s.renameIndex != nil {
		s.renameIndex.Invalidate()
	}
}

func (s *server) didChangeWatchedFilesLocked(ctx context.Context, params *protocol.DidChangeWatchedFilesParams) error {
	if params == nil {
		return nil
	}
	changes := make([]engine.DependentMCLFileChange, 0, len(params.Changes))
	changedURIs := make([]string, 0, len(params.Changes))
	invalidateRenameIndex := false
	for _, change := range params.Changes {
		uri := change.URI.String()
		path := documentPath(uri)
		if path == "" || change.Type < protocol.FileChangeTypeCreated || change.Type > protocol.FileChangeTypeDeleted {
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
		changes = append(changes, engine.DependentMCLFileChange{Path: path, Deleted: change.Type == protocol.FileChangeTypeDeleted})
	}
	if s.watchRegistered && s.renameIndex != nil {
		if invalidateRenameIndex {
			s.renameIndex.Invalidate()
		} else if err := s.renameIndex.ApplyFileChangesWithContext(ctx, changes); err != nil {
			return err
		}
	}
	if len(changedURIs) > 0 {
		s.stateGeneration++
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
	s.stateGeneration++
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
			s.stateGeneration++
		}
		s.scheduleStaticDiagnostics(context.WithoutCancel(ctx), uri, diagnostics)

		return
	}
	updated, ok := applyContentChanges(state.Text, params.ContentChanges)
	if !ok {
		state.SyncLost = true
		state.Diagnostics = nil
		s.documents[uri] = state
		s.stateGeneration++
		s.scheduleStaticDiagnostics(context.WithoutCancel(ctx), uri, diagnostics)

		return
	}
	s.documents[uri] = documentState{Text: updated, Version: params.TextDocument.Version, Dependencies: state.Dependencies, Diagnostics: state.Diagnostics}
	s.stateGeneration++
	s.scheduleStaticDiagnostics(context.WithoutCancel(ctx), uri, diagnostics)
}

func (s *server) didSaveLocked(ctx context.Context, uri string) {
	if _, exists := s.documents[uri]; exists {
		s.scheduleDiagnostics(context.WithoutCancel(ctx), uri, s.diagnosticResults)
	}
}

func (s *server) didCloseLocked(ctx context.Context, uri string) {
	if _, exists := s.documents[uri]; exists {
		delete(s.documents, uri)
		s.stateGeneration++
	}
	s.scheduleDiagnostics(context.WithoutCancel(ctx), uri, s.diagnosticResults)
}

func (s *server) requestExit() {
	select {
	case s.exitRequested <- struct{}{}:
	default:
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

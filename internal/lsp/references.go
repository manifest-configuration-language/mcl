package lsp

import (
	"context"
	"os"
	"path/filepath"
	"slices"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/languageservice"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

func projectReferenceResolvers(s *server) (languageservice.ReferenceImportResolver, languageservice.ReferenceSourceReader) {
	resolveImport := func(importer, modulePath string) (string, bool) {
		resolved, err := engine.ResolveImportPath(importer, modulePath)
		if err != nil {
			return "", false
		}

		return canonicalSourcePath(resolved)
	}
	readSource := func(path string) (string, bool) {
		if _, source, ok := s.openDocumentForPath(path); ok {
			return source, true
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", false
		}

		return string(data), true
	}

	return resolveImport, readSource
}

func (s *server) References(ctx context.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	if ctx.Err() != nil {
		return nil, protocol.ErrRequestCancelled
	}
	if params == nil {
		return []protocol.Location{}, nil
	}
	snapshot := s.snapshotForRequest(ctx)
	uriText := params.TextDocument.URI.String()
	state, exists := snapshot.documents[uriText]
	if !exists || state.SyncLost {
		return []protocol.Location{}, nil
	}
	path := documentPath(uriText)
	if path == "" {
		return []protocol.Location{}, nil
	}
	semantic, err := snapshot.semanticSnapshotForDocument(ctx, path, state.Text)
	if err != nil {
		if ctx.Err() != nil {
			return nil, protocol.ErrRequestCancelled
		}
		return []protocol.Location{}, nil
	}
	target, ok := languageservice.ReferenceTargetAtSnapshotWithContext(ctx, semantic, path, languageservice.Position{
		Line: int(params.Position.Line), Character: int(params.Position.Character),
	})
	if ctx.Err() != nil {
		return nil, protocol.ErrRequestCancelled
	}
	if !ok {
		if !s.documentSnapshotIsCurrent(snapshot) {
			return nil, protocol.ErrContentModified
		}

		return []protocol.Location{}, nil
	}
	canonicalTarget, ok := canonicalSourcePath(target.Path)
	if !ok {
		if !s.documentSnapshotIsCurrent(snapshot) {
			return nil, protocol.ErrContentModified
		}

		return []protocol.Location{}, nil
	}
	target.Path = canonicalTarget
	projectSemantic := semantic
	var resolved []languageservice.Reference
	if (target.Exported && !target.Local) || target.FunctionParameter {
		workspaceSources, workspaceSemantic, workspaceErr := snapshot.semanticSnapshotForWorkspace(ctx, target.Path)
		if ctx.Err() != nil {
			return nil, protocol.ErrRequestCancelled
		}
		if workspaceErr != nil {
			return nil, jsonrpc2.NewError(jsonrpc2.InternalError, "Unable to collect complete workspace sources for references")
		}
		projectSemantic = workspaceSemantic
		resolved = languageservice.ReferencesForSnapshotSourcesWithContext(ctx, projectSemantic, target, workspaceSources, params.Context.IncludeDeclaration)
	} else {
		resolved = languageservice.ReferencesForSnapshotWithContext(ctx, projectSemantic, target, params.Context.IncludeDeclaration)
	}
	if ctx.Err() != nil {
		return nil, protocol.ErrRequestCancelled
	}
	locations := make([]protocol.Location, 0, len(resolved))
	for _, reference := range resolved {
		if ctx.Err() != nil {
			return nil, protocol.ErrRequestCancelled
		}
		targetURI := filePathToURI(reference.Path)
		if openURI, _, ok := snapshot.openDocumentForPath(reference.Path); ok {
			targetURI = openURI
		}
		start, ok := lspPosition(reference.Range.Start)
		if !ok {
			continue
		}
		end, ok := lspPosition(reference.Range.End)
		if !ok {
			continue
		}
		locations = append(locations, protocol.Location{URI: uri.URI(targetURI), Range: protocol.Range{Start: start, End: end}})
	}
	if !s.documentSnapshotIsCurrent(snapshot) {
		return nil, protocol.ErrContentModified
	}

	return locations, nil
}

func lspPosition(position languageservice.Position) (protocol.Position, bool) {
	if position.Line < 0 || position.Character < 0 || uint64(position.Line) > uint64(^uint32(0)) || uint64(position.Character) > uint64(^uint32(0)) {
		return protocol.Position{}, false
	}

	return protocol.Position{
		Line:      uint32(position.Line),
		Character: uint32(position.Character),
	}, true
}

func canonicalSourcePath(path string) (string, bool) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}

	return filepath.Clean(absolute), true
}

func (s *server) documentSnapshotIsCurrent(snapshot *server) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stateGeneration != snapshot.stateGeneration {
		return false
	}
	if len(s.documents) != len(snapshot.documents) {
		return false
	}
	for uri, previous := range snapshot.documents {
		current, exists := s.documents[uri]
		if !exists || current.Version != previous.Version || current.SyncLost != previous.SyncLost {
			return false
		}
	}

	return slices.Equal(s.workspaceRoots, snapshot.workspaceRoots)
}

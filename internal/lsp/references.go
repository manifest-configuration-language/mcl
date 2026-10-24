package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/languageservice"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

func (s *server) References(_ context.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if params == nil {
		return []protocol.Location{}, nil
	}
	uriText := params.TextDocument.URI.String()
	state, exists := s.documents[uriText]
	if !exists || state.SyncLost {
		return []protocol.Location{}, nil
	}
	path := documentPath(uriText)
	if path == "" {
		return []protocol.Location{}, nil
	}
	resolver := func(importer, modulePath string) (string, bool) {
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
	target, ok := languageservice.ReferenceTargetAt(path, state.Text, languageservice.Position{
		Line:      int(params.Position.Line),
		Character: int(params.Position.Character),
	}, resolver, readSource)
	if !ok {
		return []protocol.Location{}, nil
	}
	canonicalTarget, ok := canonicalSourcePath(target.Path)
	if !ok {
		return []protocol.Location{}, nil
	}
	target.Path = canonicalTarget
	sources := map[string]string{path: state.Text}
	if target.Path != path {
		if source, ok := readSource(target.Path); ok {
			sources[target.Path] = source
		}
	}
	if target.Exported && !target.Local {
		overlays, _ := s.renameOverlays()
		if s.renameIndex == nil {
			s.renameIndex = &engine.DependentMCLSourceIndex{}
		}
		var dependent map[string]string
		var err error
		if s.watchRegistered {
			dependent, err = s.renameIndex.SourcesFromIndex(target.Path, s.workspaceRoots, overlays)
		} else {
			dependent, err = s.renameIndex.Sources(target.Path, s.workspaceRoots, overlays)
		}
		if err != nil {
			return nil, fmt.Errorf("collect project sources for references: %w", err)
		}
		sources = dependent
	}
	resolved := languageservice.ReferencesForProject(target, sources, resolver, params.Context.IncludeDeclaration)
	locations := make([]protocol.Location, 0, len(resolved))
	for _, reference := range resolved {
		targetURI := filePathToURI(reference.Path)
		if openURI, _, ok := s.openDocumentForPath(reference.Path); ok {
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
		locations = append(locations, protocol.Location{
			URI: uri.URI(targetURI),
			Range: protocol.Range{
				Start: start,
				End:   end,
			},
		})
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

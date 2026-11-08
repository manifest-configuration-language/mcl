package lsp

import (
	"context"
	"maps"
	"strings"
	"sync"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/languageservice"
	"github.com/manifest-configuration-language/mcl/internal/project"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

type projectSemanticSnapshotCache struct {
	mu sync.Mutex

	workspace         *project.WorkspaceSnapshot
	workspaceSemantic *languageservice.ProjectSemanticSnapshot

	documentGeneration uint64
	documentPath       string
	documentSources    map[string]string
	documentSemantic   *languageservice.ProjectSemanticSnapshot
}

func (cache *projectSemanticSnapshotCache) fromWorkspace(workspace *project.WorkspaceSnapshot, resolveImport languageservice.ReferenceImportResolver) *languageservice.ProjectSemanticSnapshot {
	if workspace == nil {
		return nil
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.workspace == workspace {
		return cache.workspaceSemantic
	}
	cache.workspace = workspace
	cache.workspaceSemantic = languageservice.NewProjectSemanticSnapshotWithResolver(workspace, resolveImport, cache.documentSemantic, cache.workspaceSemantic)

	return cache.workspaceSemantic
}

func (cache *projectSemanticSnapshotCache) fromDocument(generation uint64, path string, sources map[string]string, resolveImport languageservice.ReferenceImportResolver) (*languageservice.ProjectSemanticSnapshot, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.documentSemantic != nil && cache.documentGeneration == generation && cache.documentPath == path && maps.Equal(cache.documentSources, sources) {
		return cache.documentSemantic, nil
	}
	snapshot, err := languageservice.NewProjectSemanticSnapshotFromSourcesWithResolver(sources, resolveImport, cache.documentSemantic, cache.workspaceSemantic)
	if err != nil {
		return nil, err
	}
	cache.documentGeneration = generation
	cache.documentPath = path
	cache.documentSources = maps.Clone(sources)
	cache.documentSemantic = snapshot

	return snapshot, nil
}

func (s *server) semanticSnapshotForDocument(ctx context.Context, path, source string) (*languageservice.ProjectSemanticSnapshot, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	resolveImport, readSource := projectReferenceResolvers(s)
	sources := map[string]string{path: source}
	seen := map[string]struct{}{path: {}}
	pending := []string{path}
	for len(pending) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		importer := pending[0]
		pending = pending[1:]
		module, err := syntax.ParseAST(sources[importer])
		if err != nil {
			continue
		}
		for _, node := range module.Nodes {
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || declaration.Text || strings.HasPrefix(declaration.ModulePath, "std:") {
				continue
			}
			importedPath, resolved := resolveImport(importer, declaration.ModulePath)
			if !resolved {
				continue
			}
			if _, exists := seen[importedPath]; exists {
				continue
			}
			seen[importedPath] = struct{}{}
			importedSource, exists := readSource(importedPath)
			if !exists {
				continue
			}
			sources[importedPath] = importedSource
			pending = append(pending, importedPath)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cache := s.semanticCache
	if cache == nil {
		cache = &projectSemanticSnapshotCache{}
	}

	return cache.fromDocument(s.stateGeneration, path, sources, resolveImport)
}

func (s *server) semanticSnapshotForWorkspace(ctx context.Context, target string) (map[string]string, *languageservice.ProjectSemanticSnapshot, error) {
	if s.renameIndex == nil {
		s.renameIndex = &engine.DependentMCLSourceIndex{}
	}
	overlays, _ := s.renameOverlays()
	var sources map[string]string
	var workspace *project.WorkspaceSnapshot
	var err error
	if s.watchRegistered {
		sources, workspace, err = s.renameIndex.SourcesFromIndexWithSnapshotWithContext(ctx, target, s.workspaceRoots, overlays)
	} else {
		sources, workspace, err = s.renameIndex.SourcesWithSnapshotWithContext(ctx, target, s.workspaceRoots, overlays)
	}
	if err != nil {
		return nil, nil, err
	}
	resolveImport, _ := projectReferenceResolvers(s)
	cache := s.semanticCache
	if cache == nil {
		cache = &projectSemanticSnapshotCache{}
	}
	semantic := cache.fromWorkspace(workspace, resolveImport)

	return sources, semantic, nil
}

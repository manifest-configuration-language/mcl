// Package project contains immutable models for source workspaces.
package project

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

// WorkspaceSnapshot is an immutable view of the source files in one workspace generation.
// Values returned by Module are shared parsed syntax trees and must be treated as read-only.
type WorkspaceSnapshot struct {
	generation   uint64
	root         string
	searchRoots  []string
	modules      map[string]moduleSource
	paths        []string
	dependencies map[string][]string
	importers    map[string][]string
}

type moduleSource struct {
	content string
	hash    [sha256.Size]byte
	ast     syntax.ModuleAST
	imports []syntax.ImportDeclaration
}

// NewWorkspaceSnapshot parses and stores sources under normalized paths.
// The generation is supplied by the owner so related snapshots can share an explicit version sequence.
func NewWorkspaceSnapshot(generation uint64, root string, searchRoots []string, sources map[string]string) (*WorkspaceSnapshot, error) {
	modules := make(map[string]syntax.ModuleAST, len(sources))
	for path, content := range sources {
		ast, err := syntax.ParseAST(content)
		if err != nil {
			return nil, fmt.Errorf("parse module %q: %w", path, err)
		}
		modules[path] = ast
	}
	return NewWorkspaceSnapshotFromParsed(generation, root, searchRoots, sources, modules, nil, nil)
}

// NewWorkspaceSnapshotWithPrevious parses changed sources and reuses ASTs when the normalized path
// and SHA-256 digest match the previous snapshot.
func NewWorkspaceSnapshotWithPrevious(generation uint64, root string, searchRoots []string, sources map[string]string, previous *WorkspaceSnapshot) (*WorkspaceSnapshot, error) {
	normalizedSources, paths, err := normalizeSources(sources)
	if err != nil {
		return nil, err
	}
	modules := make(map[string]syntax.ModuleAST, len(paths))
	for _, path := range paths {
		content := normalizedSources[path]
		hash := sha256.Sum256([]byte(content))
		if previous != nil {
			if prior, ok := previous.modules[path]; ok && prior.hash == hash {
				modules[path] = prior.ast
				continue
			}
		}
		ast, err := syntax.ParseAST(content)
		if err != nil {
			return nil, fmt.Errorf("parse module %q: %w", path, err)
		}
		modules[path] = ast
	}
	return NewWorkspaceSnapshotFromParsed(generation, root, searchRoots, normalizedSources, modules, nil, nil)
}

// NewWorkspaceSnapshotFromParsed builds a snapshot from sources and their parsed ASTs without parsing.
// The module map must contain exactly one AST for each source path after path normalization.
// AST values are retained by reference and must be treated as read-only.
func NewWorkspaceSnapshotFromParsed(generation uint64, root string, searchRoots []string, sources map[string]string, modules map[string]syntax.ModuleAST, dependencies, importers map[string][]string) (*WorkspaceSnapshot, error) {
	normalizedSources, paths, err := normalizeSources(sources)
	if err != nil {
		return nil, err
	}
	normalizedModules := make(map[string]syntax.ModuleAST, len(modules))
	for path, ast := range modules {
		if path == "" {
			return nil, fmt.Errorf("module path %q is empty", path)
		}
		normalizedPath := normalizePath(path)
		if _, exists := normalizedModules[normalizedPath]; exists {
			return nil, fmt.Errorf("module paths normalize to the same path %q", normalizedPath)
		}
		normalizedModules[normalizedPath] = ast
	}
	if len(normalizedSources) != len(normalizedModules) {
		return nil, fmt.Errorf("source and module paths do not match")
	}
	clonedDependencies, err := clonePathLists(dependencies)
	if err != nil {
		return nil, err
	}
	clonedImporters, err := clonePathLists(importers)
	if err != nil {
		return nil, err
	}

	snapshot := &WorkspaceSnapshot{
		generation:   generation,
		root:         normalizePath(root),
		searchRoots:  make([]string, len(searchRoots)),
		modules:      make(map[string]moduleSource, len(paths)),
		paths:        paths,
		dependencies: clonedDependencies,
		importers:    clonedImporters,
	}
	for i, path := range searchRoots {
		snapshot.searchRoots[i] = normalizePath(path)
	}
	for _, path := range paths {
		ast, ok := normalizedModules[path]
		if !ok {
			return nil, fmt.Errorf("source path %q has no parsed module", path)
		}
		content := normalizedSources[path]
		snapshot.modules[path] = moduleSource{
			content: content,
			hash:    sha256.Sum256([]byte(content)),
			ast:     ast,
			imports: importsFrom(ast),
		}
	}
	for path := range normalizedModules {
		if _, ok := normalizedSources[path]; !ok {
			return nil, fmt.Errorf("parsed module path %q has no source", path)
		}
	}
	return snapshot, nil
}

// Generation returns the version assigned to this workspace snapshot.
func (snapshot *WorkspaceSnapshot) Generation() uint64 {
	return snapshot.generation
}

// Root returns the normalized workspace root path.
func (snapshot *WorkspaceSnapshot) Root() string {
	return snapshot.root
}

// SearchRoots returns a copy of the normalized module search roots.
func (snapshot *WorkspaceSnapshot) SearchRoots() []string {
	return slices.Clone(snapshot.searchRoots)
}

// Paths returns source paths in deterministic lexical order.
func (snapshot *WorkspaceSnapshot) Paths() []string {
	return slices.Clone(snapshot.paths)
}

// Sources returns a copy of the source map keyed by normalized path.
func (snapshot *WorkspaceSnapshot) Sources() map[string]string {
	sources := make(map[string]string, len(snapshot.modules))
	for path, module := range snapshot.modules {
		sources[path] = module.content
	}
	return sources
}

// Modules returns a copy of the parsed AST map keyed by normalized path.
// The AST values are shared with the snapshot and must not be mutated.
func (snapshot *WorkspaceSnapshot) Modules() map[string]syntax.ModuleAST {
	modules := make(map[string]syntax.ModuleAST, len(snapshot.modules))
	for path, module := range snapshot.modules {
		modules[path] = module.ast
	}
	return modules
}

// Dependencies returns a copy of the resolved dependency paths for path.
func (snapshot *WorkspaceSnapshot) Dependencies(path string) []string {
	return slices.Clone(snapshot.dependencies[normalizePath(path)])
}

// Importers returns a copy of the paths that import path.
func (snapshot *WorkspaceSnapshot) Importers(path string) []string {
	return slices.Clone(snapshot.importers[normalizePath(path)])
}

// DependentSources returns source content for path and every transitive importer present in the snapshot.
func (snapshot *WorkspaceSnapshot) DependentSources(ctx context.Context, target string) (map[string]string, error) {
	dependent := map[string]struct{}{normalizePath(target): {}}
	queue := []string{normalizePath(target)}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dependency := queue[0]
		queue = queue[1:]
		for _, importer := range snapshot.importers[dependency] {
			importer = normalizePath(importer)
			if _, exists := dependent[importer]; exists {
				continue
			}
			dependent[importer] = struct{}{}
			queue = append(queue, importer)
		}
	}

	sources := make(map[string]string, len(dependent))
	for path := range dependent {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if module, exists := snapshot.modules[path]; exists {
			sources[path] = module.content
		}
	}
	return sources, nil
}

// Source returns the source content and its SHA-256 digest for path.
func (snapshot *WorkspaceSnapshot) Source(path string) (string, [sha256.Size]byte, bool) {
	module, ok := snapshot.modules[normalizePath(path)]
	if !ok {
		return "", [sha256.Size]byte{}, false
	}
	return module.content, module.hash, true
}

// Module returns the parsed AST for path. The AST is shared with the snapshot and must not be mutated.
func (snapshot *WorkspaceSnapshot) Module(path string) (syntax.ModuleAST, bool) {
	module, ok := snapshot.modules[normalizePath(path)]
	if !ok {
		return syntax.ModuleAST{}, false
	}
	return module.ast, true
}

// Imports returns a copy of the raw import declarations for path.
func (snapshot *WorkspaceSnapshot) Imports(path string) ([]syntax.ImportDeclaration, bool) {
	module, ok := snapshot.modules[normalizePath(path)]
	if !ok {
		return nil, false
	}
	return slices.Clone(module.imports), true
}

func importsFrom(ast syntax.ModuleAST) []syntax.ImportDeclaration {
	var imports []syntax.ImportDeclaration
	for _, node := range ast.Nodes {
		if declaration, ok := node.(*syntax.ImportDeclaration); ok {
			imports = append(imports, *declaration)
		}
	}
	return imports
}

func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
}

func normalizeSources(sources map[string]string) (map[string]string, []string, error) {
	normalizedSources := make(map[string]string, len(sources))
	paths := make([]string, 0, len(sources))
	for path, content := range sources {
		if path == "" {
			return nil, nil, fmt.Errorf("source path %q is empty", path)
		}
		normalizedPath := normalizePath(path)
		if _, exists := normalizedSources[normalizedPath]; exists {
			return nil, nil, fmt.Errorf("source paths normalize to the same path %q", normalizedPath)
		}
		normalizedSources[normalizedPath] = content
		paths = append(paths, normalizedPath)
	}
	slices.Sort(paths)
	return normalizedSources, paths, nil
}

func clonePathLists(pathLists map[string][]string) (map[string][]string, error) {
	cloned := make(map[string][]string, len(pathLists))
	for path, values := range pathLists {
		normalizedPath := normalizePath(path)
		if _, exists := cloned[normalizedPath]; exists {
			return nil, fmt.Errorf("path list keys normalize to the same path %q", normalizedPath)
		}
		cloned[normalizedPath] = slices.Clone(values)
	}
	return cloned, nil
}

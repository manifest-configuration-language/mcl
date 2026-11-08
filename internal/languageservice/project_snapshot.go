package languageservice

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/manifest-configuration-language/mcl/internal/project"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

// ProjectSemanticSnapshot holds one immutable set of module sources and lazily caches their
// syntax and semantic indexes. Cached ASTs and indexes are shared and must be treated as read-only.
type ProjectSemanticSnapshot struct {
	generation    uint64
	modules       map[string]*projectSemanticModule
	paths         []string
	resolveImport ReferenceImportResolver
}

type projectSemanticModule struct {
	source        string
	parsedAST     func() (syntax.ModuleAST, error)
	semanticIndex func() (*SemanticIndex, error)
}

// NewProjectSemanticSnapshot reuses ASTs from a workspace snapshot and lazily builds semantic indexes.
func NewProjectSemanticSnapshot(workspace *project.WorkspaceSnapshot) *ProjectSemanticSnapshot {
	return NewProjectSemanticSnapshotWithResolver(workspace, nil)
}

// NewProjectSemanticSnapshotWithResolver reuses workspace ASTs and an optional project import resolver.
func NewProjectSemanticSnapshotWithResolver(workspace *project.WorkspaceSnapshot, resolveImport ReferenceImportResolver, previous ...*ProjectSemanticSnapshot) *ProjectSemanticSnapshot {
	snapshot := &ProjectSemanticSnapshot{modules: make(map[string]*projectSemanticModule)}
	if workspace == nil {
		snapshot.resolveImport = resolveImport

		return snapshot
	}
	snapshot.generation = workspace.Generation()
	snapshot.resolveImport = resolveImport
	snapshot.paths = workspace.Paths()
	for _, modulePath := range snapshot.paths {
		source, _, exists := workspace.Source(modulePath)
		if !exists {
			continue
		}
		if cached := reusableProjectSemanticModule(modulePath, source, previous); cached != nil {
			snapshot.modules[modulePath] = cached

			continue
		}
		module, hasAST := workspace.Module(modulePath)
		snapshot.modules[modulePath] = newProjectSemanticModule(source, module, hasAST)
	}

	return snapshot
}

// NewProjectSemanticSnapshotFromSources copies project sources and parses each module only when
// a language-service query needs its AST or semantic index.
func NewProjectSemanticSnapshotFromSources(sources map[string]string) (*ProjectSemanticSnapshot, error) {
	return NewProjectSemanticSnapshotFromSourcesWithResolver(sources, nil)
}

// NewProjectSemanticSnapshotFromSourcesWithResolver copies project sources and an optional import resolver.
func NewProjectSemanticSnapshotFromSourcesWithResolver(sources map[string]string, resolveImport ReferenceImportResolver, previous ...*ProjectSemanticSnapshot) (*ProjectSemanticSnapshot, error) {
	snapshot := &ProjectSemanticSnapshot{modules: make(map[string]*projectSemanticModule, len(sources)), resolveImport: resolveImport}
	for sourcePath, source := range sources {
		absolute := path.IsAbs(sourcePath) || filepath.IsAbs(sourcePath)
		if sourcePath == "" || (!absolute && (strings.Contains(sourcePath, `\`) || !fs.ValidPath(sourcePath) || sourcePath == ".")) {
			return nil, fmt.Errorf("invalid project source path %q", sourcePath)
		}
		normalizedPath := normalizeWorkspacePath(sourcePath)
		if _, exists := snapshot.modules[normalizedPath]; exists {
			return nil, fmt.Errorf("project source paths normalize to the same path %q", normalizedPath)
		}
		module := reusableProjectSemanticModule(normalizedPath, source, previous)
		if module == nil {
			module = newProjectSemanticModule(source, syntax.ModuleAST{}, false)
		}
		snapshot.modules[normalizedPath] = module
		snapshot.paths = append(snapshot.paths, normalizedPath)
	}
	slices.Sort(snapshot.paths)

	return snapshot, nil
}

func newProjectSemanticModule(source string, ast syntax.ModuleAST, parsed bool) *projectSemanticModule {
	module := &projectSemanticModule{source: source}
	module.parsedAST = sync.OnceValues(func() (syntax.ModuleAST, error) {
		if parsed {
			return ast, nil
		}

		return syntax.ParseAST(source)
	})
	module.semanticIndex = sync.OnceValues(func() (*SemanticIndex, error) {
		moduleAST, err := module.parsedAST()
		if err != nil {
			return nil, err
		}

		return AnalyzeModule(moduleAST), nil
	})

	return module
}

func reusableProjectSemanticModule(path, source string, snapshots []*ProjectSemanticSnapshot) *projectSemanticModule {
	for _, snapshot := range snapshots {
		if snapshot == nil {
			continue
		}
		module := snapshot.modules[normalizeWorkspacePath(path)]
		if module != nil && module.source == source {
			return module
		}
	}

	return nil
}

// Generation returns the workspace generation, or zero when no workspace was supplied.
func (snapshot *ProjectSemanticSnapshot) Generation() uint64 {
	if snapshot == nil {
		return 0
	}

	return snapshot.generation
}

// Source returns source content for path when it belongs to the snapshot.
func (snapshot *ProjectSemanticSnapshot) Source(path string) (string, bool) {
	if snapshot == nil {
		return "", false
	}
	module, exists := snapshot.modules[normalizeWorkspacePath(path)]
	if !exists {
		return "", false
	}

	return module.source, true
}

// Sources returns a copy of all source content keyed by normalized project path.
func (snapshot *ProjectSemanticSnapshot) Sources() map[string]string {
	sources := make(map[string]string)
	if snapshot == nil {
		return sources
	}
	for sourcePath, module := range snapshot.modules {
		sources[sourcePath] = module.source
	}

	return sources
}

// Module returns a shared parsed module AST. The AST must be treated as read-only.
func (snapshot *ProjectSemanticSnapshot) Module(path string) (syntax.ModuleAST, bool) {
	module := snapshot.module(path)
	if module == nil {
		return syntax.ModuleAST{}, false
	}
	ast, err := module.parsedAST()
	if err != nil {
		return syntax.ModuleAST{}, false
	}

	return ast, true
}

// SemanticIndex returns a shared semantic index. The index and its AST must be treated as read-only.
func (snapshot *ProjectSemanticSnapshot) SemanticIndex(path string) (*SemanticIndex, bool) {
	module := snapshot.module(path)
	if module == nil {
		return nil, false
	}
	index, err := module.semanticIndex()
	if err != nil {
		return nil, false
	}

	return index, true
}

// Paths returns workspace paths in deterministic lexical order.
func (snapshot *ProjectSemanticSnapshot) Paths() []string {
	if snapshot == nil {
		return []string{}
	}

	return slices.Clone(snapshot.paths)
}

// ResolveImport resolves an import relative to importer when the target source belongs to this snapshot.
func (snapshot *ProjectSemanticSnapshot) ResolveImport(importer, modulePath string) (string, bool) {
	if snapshot == nil || path.IsAbs(modulePath) || filepath.IsAbs(modulePath) || strings.Contains(modulePath, `\`) {
		return "", false
	}
	if snapshot.resolveImport != nil {
		resolved, ok := snapshot.resolveImport(importer, modulePath)
		if !ok {
			return "", false
		}
		normalized := normalizeWorkspacePath(resolved)
		if _, exists := snapshot.modules[normalized]; !exists {
			return "", false
		}

		return normalized, true
	}
	if path.IsAbs(importer) || filepath.IsAbs(filepath.FromSlash(importer)) {
		resolved := normalizeWorkspacePath(filepath.Join(filepath.Dir(filepath.FromSlash(importer)), filepath.FromSlash(modulePath)))
		if !filepath.IsAbs(filepath.FromSlash(resolved)) {
			return "", false
		}
		if _, exists := snapshot.modules[resolved]; !exists {
			return "", false
		}

		return resolved, true
	}
	resolved := path.Clean(path.Join(path.Dir(importer), modulePath))
	if !fs.ValidPath(resolved) {
		return "", false
	}
	if _, exists := snapshot.modules[normalizeWorkspacePath(resolved)]; !exists {
		return "", false
	}

	return resolved, true
}

func (snapshot *ProjectSemanticSnapshot) module(path string) *projectSemanticModule {
	if snapshot == nil {
		return nil
	}

	return snapshot.modules[normalizeWorkspacePath(path)]
}

func normalizeWorkspacePath(path string) string {
	if path == "" {
		return ""
	}

	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
}

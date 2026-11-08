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
	generation uint64
	modules    map[string]*projectSemanticModule
	paths      []string
}

type projectSemanticModule struct {
	source string
	once   sync.Once
	ast    syntax.ModuleAST
	index  *SemanticIndex
	err    error
}

// NewProjectSemanticSnapshot builds semantic indexes from an existing parsed workspace snapshot.
// The workspace ASTs are shared rather than parsed again.
func NewProjectSemanticSnapshot(workspace *project.WorkspaceSnapshot) *ProjectSemanticSnapshot {
	snapshot := &ProjectSemanticSnapshot{modules: make(map[string]*projectSemanticModule)}
	if workspace == nil {
		return snapshot
	}
	snapshot.generation = workspace.Generation()
	snapshot.paths = workspace.Paths()
	for _, modulePath := range snapshot.paths {
		module, exists := workspace.Module(modulePath)
		if !exists {
			continue
		}
		source, _, _ := workspace.Source(modulePath)
		snapshot.modules[modulePath] = &projectSemanticModule{
			source: source,
			ast:    module,
			index:  AnalyzeModule(module),
		}
	}

	return snapshot
}

// NewProjectSemanticSnapshotFromSources copies project sources and parses each module only when
// a language-service query needs its AST or semantic index.
func NewProjectSemanticSnapshotFromSources(sources map[string]string) (*ProjectSemanticSnapshot, error) {
	snapshot := &ProjectSemanticSnapshot{modules: make(map[string]*projectSemanticModule, len(sources))}
	for sourcePath, source := range sources {
		if !fs.ValidPath(sourcePath) || sourcePath == "." || strings.Contains(sourcePath, `\`) {
			return nil, fmt.Errorf("invalid project source path %q", sourcePath)
		}
		normalizedPath := normalizeWorkspacePath(sourcePath)
		if _, exists := snapshot.modules[normalizedPath]; exists {
			return nil, fmt.Errorf("project source paths normalize to the same path %q", normalizedPath)
		}
		snapshot.modules[normalizedPath] = &projectSemanticModule{source: source}
		snapshot.paths = append(snapshot.paths, normalizedPath)
	}
	slices.Sort(snapshot.paths)

	return snapshot, nil
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
	module.parse()
	if module.err != nil {
		return syntax.ModuleAST{}, false
	}

	return module.ast, true
}

// SemanticIndex returns a shared semantic index. The index and its AST must be treated as read-only.
func (snapshot *ProjectSemanticSnapshot) SemanticIndex(path string) (*SemanticIndex, bool) {
	module := snapshot.module(path)
	if module == nil {
		return nil, false
	}
	module.parse()
	if module.err != nil {
		return nil, false
	}

	return module.index, true
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
	if snapshot == nil || path.IsAbs(modulePath) || strings.Contains(modulePath, `\`) {
		return "", false
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

func (module *projectSemanticModule) parse() {
	module.once.Do(func() {
		if module.index != nil {
			return
		}
		module.ast, module.err = syntax.ParseAST(module.source)
		if module.err == nil {
			module.index = AnalyzeModule(module.ast)
		}
	})
}

func normalizeWorkspacePath(path string) string {
	if path == "" {
		return ""
	}

	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
}

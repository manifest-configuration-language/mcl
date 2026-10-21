package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/safepath"
)

type moduleGraph struct {
	root        string
	files       *projectFilesystem
	active      map[string]bool
	loaded      map[string]struct{}
	loadedBytes int64
}

type moduleImport struct {
	declaration *ImportDeclaration
	path        string
}

func (graph *moduleGraph) isActive(path string) bool {
	return graph.active[path]
}

func (graph *moduleGraph) enter(path string) error {
	if graph.active == nil {
		graph.active = make(map[string]bool)
	}
	if graph.active[path] {
		return fmt.Errorf("module import cycle includes %q", path)
	}
	graph.active[path] = true

	return nil
}

func (graph *moduleGraph) leave(path string) {
	delete(graph.active, path)
}

func (graph *moduleGraph) parse(path, source string) (ModuleAST, error) {
	if len(source) > maxMCLSourceBytes {
		return ModuleAST{}, fmt.Errorf("module %q exceeds the %d-byte limit", path, maxMCLSourceBytes)
	}
	if graph.loaded == nil {
		graph.loaded = make(map[string]struct{})
	}
	if _, loaded := graph.loaded[path]; !loaded {
		if len(graph.loaded) >= maxProjectMCLFiles {
			return ModuleAST{}, fmt.Errorf("workspace contains more than %d MCL files", maxProjectMCLFiles)
		}
		if graph.loadedBytes+int64(len(source)) > maxProjectMCLBytes {
			return ModuleAST{}, fmt.Errorf("project MCL sources exceed the %d-byte limit", maxProjectMCLBytes)
		}
		graph.loaded[path] = struct{}{}
		graph.loadedBytes += int64(len(source))
	}

	module, err := ParseAST(source)
	if err != nil {
		return ModuleAST{}, fmt.Errorf("%s: %w", path, sourceDiagnosticPath(path, err))
	}

	return module, nil
}

func (graph *moduleGraph) imports(importer string, module ModuleAST) ([]moduleImport, error) {
	imports := make([]moduleImport, 0)
	for _, node := range module.Nodes {
		declaration, ok := node.(*ImportDeclaration)
		if !ok || strings.HasPrefix(declaration.ModulePath, "std:") {
			continue
		}
		path, err := graph.resolveImport(importer, declaration.ModulePath)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", importer, declaration.Line, err)
		}
		imports = append(imports, moduleImport{declaration: declaration, path: path})
	}

	return imports, nil
}

func (graph *moduleGraph) resolveImport(importer, modulePath string) (string, error) {
	if filepath.IsAbs(modulePath) || path.IsAbs(modulePath) {
		return "", errors.New("absolute module paths are not allowed")
	}
	if graph.files != nil && !graph.files.osBacked {
		return graph.resolveImportInFS(importer, modulePath)
	}
	absoluteImporter, err := filepath.Abs(importer)
	if err != nil {
		return "", err
	}
	canonicalImporter, err := filepath.EvalSymlinks(absoluteImporter)
	if err != nil {
		canonicalDirectory, directoryErr := filepath.EvalSymlinks(filepath.Dir(absoluteImporter))
		if directoryErr != nil {
			return "", fmt.Errorf("resolve importer directory: %w", directoryErr)
		}
		canonicalImporter = filepath.Join(canonicalDirectory, filepath.Base(absoluteImporter))
	}
	joined, err := filepath.Abs(filepath.Join(filepath.Dir(canonicalImporter), filepath.FromSlash(modulePath)))
	if err != nil {
		return "", err
	}
	if err := graph.ensureWithinRoot(joined, modulePath); err != nil {
		return "", err
	}
	canonical, err := safepath.ResolveWithin(graph.root, joined)
	if err != nil {
		if errors.Is(err, safepath.ErrOutsideRoot) {
			return "", fmt.Errorf("module path %q escapes the project directory", modulePath)
		}

		return "", fmt.Errorf("resolve module %q: %w", modulePath, err)
	}

	return canonical, nil
}

func (graph *moduleGraph) resolveImportInFS(importer, modulePath string) (string, error) {
	importerPath, err := graph.files.relativePath(importer)
	if err != nil {
		return "", fmt.Errorf("resolve importer: %w", err)
	}
	joined := path.Clean(path.Join(path.Dir(importerPath), filepath.ToSlash(filepath.FromSlash(modulePath))))
	if !fs.ValidPath(joined) {
		return "", fmt.Errorf("module path %q escapes the project directory", modulePath)
	}
	if _, err := fs.Stat(graph.files.files, joined); err != nil {
		return "", fmt.Errorf("resolve module %q: %w", modulePath, err)
	}

	return filepath.Join(graph.root, filepath.FromSlash(joined)), nil
}

func (graph *moduleGraph) ensureWithinRoot(path, modulePath string) error {
	if !safepath.Within(graph.root, path) {
		return fmt.Errorf("module path %q escapes the project directory", modulePath)
	}

	return nil
}

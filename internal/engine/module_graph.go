package engine

import (
	"fmt"
	"strings"
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
	files := graph.files
	if files == nil {
		files = newOSProjectFilesystem(graph.root)
	}

	return files.resolveModuleImport(importer, modulePath)
}

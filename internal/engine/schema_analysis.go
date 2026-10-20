package engine

import (
	"fmt"
	"maps"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
)

type schemaAnalysisLoader struct {
	root         string
	graph        *moduleGraph
	entry        string
	entrySource  string
	dependencies *dependency.Lock
	cache        map[string]map[string]ResolvedType
	active       map[string]bool
	analysis     *TypeAnalysisContext
}

func (loader *schemaAnalysisLoader) analyze(path string, entry bool) (map[string]ResolvedType, error) {
	if loader.graph == nil {
		loader.graph = &moduleGraph{root: loader.root, active: loader.active}
	}
	if loader.graph.isActive(path) {
		return nil, fmt.Errorf("module import cycle includes %q", path)
	}
	if !entry {
		if types, cached := loader.cache[path]; cached {
			return types, nil
		}
	}
	if err := loader.graph.enter(path); err != nil {
		return nil, err
	}
	defer loader.graph.leave(path)

	source := loader.entrySource
	if path != loader.entry {
		data, err := readLimitedFileWithLimit(path, maxMCLSourceBytes)
		if err != nil {
			return nil, fmt.Errorf("read module %q: %w", path, err)
		}
		source = string(data)
	}
	module, err := loader.graph.parse(path, source)
	if err != nil {
		return nil, err
	}
	types, err := typeDefinitionsAST(module)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if loader.analysis == nil {
		loader.analysis = &TypeAnalysisContext{}
	}
	if err := loadTypeSchemaFiles(types, loader.root, path, loader.dependencies, loader.analysis); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	imports, err := loader.graph.imports(path, module)
	if err != nil {
		return nil, err
	}
	for _, imported := range imports {
		declaration := imported.declaration
		exportedTypes, err := loader.analyze(imported.path, false)
		if err != nil {
			return nil, err
		}
		for name, typeExpr := range exportedTypes {
			types[declaration.Name+"."+name] = typeExpr
		}
	}

	if !entry {
		scope := cloneTypeExprMap(types)
		scopeID := TypeScopeID(path)
		loader.analysis.registerScope(scopeID, scope)
		exported := make(map[string]ResolvedType)
		for _, node := range module.Nodes {
			declaration, ok := node.(*TypeDeclaration)
			if !ok || !declaration.Exported {
				continue
			}
			typeExpr := types[declaration.Name]
			typeExpr.ScopeID = scopeID
			exported[declaration.Name] = typeExpr
		}
		loader.cache[path] = exported

		return exported, nil
	}

	return types, nil
}

func cloneTypeExprMap(types map[string]ResolvedType) map[string]ResolvedType {
	cloned := make(map[string]ResolvedType, len(types))
	maps.Copy(cloned, types)

	return cloned
}

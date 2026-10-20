package lsp

import (
	"slices"

	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

func analyzeSource(source string) (*moduleSemanticIndex, error) {
	module, err := syntax.ParseAST(source)
	if err != nil {
		return nil, err
	}

	return analyzeModule(module), nil
}

// moduleSemanticIndexは1つのASTに含まれる宣言、参照、local scopeを共有する。
type moduleSemanticIndex struct {
	module       syntax.ModuleAST
	declarations map[string][]int
	references   []sourceReference
	locals       []scopedLocalBinding
	qualified    []qualifiedReference
	qualifiedTyp []qualifiedReference
}

func analyzeModule(module syntax.ModuleAST) *moduleSemanticIndex {
	index := &moduleSemanticIndex{
		module:       module,
		declarations: make(map[string][]int),
		references:   sourceReferences(module),
		locals:       scopedLocalBindings(module),
		qualified:    qualifiedSourceReferences(module),
		qualifiedTyp: qualifiedTypeReferences(module),
	}
	for declarationIndex, node := range module.Nodes {
		name, _, span, ok := astDeclarationInfo(node)
		if !ok || span.End <= span.Start {
			continue
		}
		index.declarations[name] = append(index.declarations[name], declarationIndex)
	}

	return index
}

func (index *moduleSemanticIndex) declarationAt(name string, offset int) int {
	for _, declarationIndex := range index.declarations[name] {
		declarationName, _, span, ok := astDeclarationInfo(index.module.Nodes[declarationIndex])
		if ok && declarationName == name && span.Start <= offset && offset <= span.End {
			return declarationIndex
		}
	}

	return -1
}

func (index *moduleSemanticIndex) resolveTopLevel(name string, offset int, typeName bool) int {
	if typeName {
		return topLevelTypeDefinition(index.module, name)
	}
	definition := -1
	for _, declarationIndex := range index.declarations[name] {
		node := index.module.Nodes[declarationIndex]
		declarationName, _, nameSpan, ok := astDeclarationInfo(node)
		if !ok || isTypeDeclaration(node) || declarationName != name || nameSpan.Start > offset || !isRenameableASTDeclaration(node) {
			continue
		}
		definition = declarationIndex
	}

	return definition
}

func (index *moduleSemanticIndex) localAt(name string, offset int) localBindingMatch {
	return localBindingAt(index.locals, name, offset)
}

func (index *moduleSemanticIndex) visibleLocalBindings(offset int) []localBinding {
	bindings := make([]scopedLocalBinding, 0)
	for _, binding := range index.locals {
		insideDeclaration := offset >= binding.declaration.Start && offset <= binding.declaration.End
		insideScope := offset >= binding.scope.Start && offset <= binding.scope.End
		if insideDeclaration || insideScope {
			bindings = append(bindings, binding)
		}
	}
	slices.SortFunc(bindings, func(left, right scopedLocalBinding) int {
		return (left.scope.End - left.scope.Start) - (right.scope.End - right.scope.Start)
	})
	result := make([]localBinding, len(bindings))
	for index, binding := range bindings {
		result[index] = localBinding{name: binding.name, declaration: binding.declaration}
	}

	return result
}

func (index *moduleSemanticIndex) visibleDeclarationNames(line int) []string {
	var names []string
	for name, declarationIndexes := range index.declarations {
		for _, declarationIndex := range declarationIndexes {
			_, declarationLine, _, ok := astDeclarationInfo(index.module.Nodes[declarationIndex])
			if ok && declarationLine < line {
				names = append(names, name)

				break
			}
		}
	}
	slices.Sort(names)

	return names
}

package languageservice

import (
	"slices"

	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

func AnalyzeSource(source string) (*SemanticIndex, error) {
	module, err := syntax.ParseAST(source)
	if err != nil {
		return nil, err
	}

	return AnalyzeModule(module), nil
}

// SemanticIndexは1つのASTに含まれる宣言、参照、local scopeを共有する。
type SemanticIndex struct {
	Module        syntax.ModuleAST
	Declarations  map[string][]int
	References    []SourceReference
	Locals        []ScopedLocalBinding
	Qualified     []QualifiedReference
	QualifiedType []QualifiedReference
}

func AnalyzeModule(module syntax.ModuleAST) *SemanticIndex {
	index := &SemanticIndex{
		Module:        module,
		Declarations:  make(map[string][]int),
		References:    SourceReferences(module),
		Locals:        ScopedLocalBindings(module),
		Qualified:     QualifiedSourceReferences(module),
		QualifiedType: QualifiedTypeReferences(module),
	}
	for declarationIndex, node := range module.Nodes {
		name, _, span, ok := ASTDeclarationInfo(node)
		if !ok || span.End <= span.Start {
			continue
		}
		index.Declarations[name] = append(index.Declarations[name], declarationIndex)
	}

	return index
}

func (index *SemanticIndex) DeclarationAt(name string, offset int) int {
	for _, declarationIndex := range index.Declarations[name] {
		declarationName, _, span, ok := ASTDeclarationInfo(index.Module.Nodes[declarationIndex])
		if ok && declarationName == name && span.Start <= offset && offset <= span.End {
			return declarationIndex
		}
	}

	return -1
}

func (index *SemanticIndex) ResolveTopLevel(name string, offset int, typeName bool) int {
	if typeName {
		return TopLevelTypeDefinition(index.Module, name)
	}
	definition := -1
	for _, declarationIndex := range index.Declarations[name] {
		node := index.Module.Nodes[declarationIndex]
		declarationName, _, nameSpan, ok := ASTDeclarationInfo(node)
		if !ok || IsTypeDeclaration(node) || declarationName != name || nameSpan.Start > offset || !IsRenameableASTDeclaration(node) {
			continue
		}
		definition = declarationIndex
	}

	return definition
}

func (index *SemanticIndex) LocalAt(name string, offset int) LocalBindingMatch {
	return LocalBindingAt(index.Locals, name, offset)
}

func (index *SemanticIndex) VisibleLocalBindings(offset int) []LocalBinding {
	bindings := make([]ScopedLocalBinding, 0)
	for _, binding := range index.Locals {
		insideDeclaration := offset >= binding.Declaration.Start && offset <= binding.Declaration.End
		insideScope := offset >= binding.Scope.Start && offset <= binding.Scope.End
		if insideDeclaration || insideScope {
			bindings = append(bindings, binding)
		}
	}
	slices.SortFunc(bindings, func(left, right ScopedLocalBinding) int {
		return (left.Scope.End - left.Scope.Start) - (right.Scope.End - right.Scope.Start)
	})
	result := make([]LocalBinding, len(bindings))
	for index, binding := range bindings {
		result[index] = LocalBinding{Name: binding.Name, Declaration: binding.Declaration}
	}

	return result
}

func (index *SemanticIndex) VisibleDeclarationNames(line int) []string {
	var names []string
	for name, declarationIndexes := range index.Declarations {
		for _, declarationIndex := range declarationIndexes {
			_, declarationLine, _, ok := ASTDeclarationInfo(index.Module.Nodes[declarationIndex])
			if ok && declarationLine < line {
				names = append(names, name)

				break
			}
		}
	}
	slices.Sort(names)

	return names
}

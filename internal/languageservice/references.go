package languageservice

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/sourceindex"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

// ReferenceTarget identifies a declaration resolved from an editor position.
type ReferenceTarget struct {
	Path        string
	Name        string
	TypeName    bool
	Declaration syntax.SourceSpan
	Local       bool
	Exported    bool
}

// Reference is a source location that resolves to a declaration.
type Reference struct {
	Path  string
	Range SourceRange
}

// ReferenceImportResolver resolves an import path relative to its importing file.
type ReferenceImportResolver func(importer, modulePath string) (string, bool)

// ReferenceSourceReader reads the current project version of a source file.
type ReferenceSourceReader func(path string) (string, bool)

// ReferenceTargetAt resolves the declaration at a source position, including imported exports.
func ReferenceTargetAt(path, source string, position Position, resolveImport ReferenceImportResolver, readSource ReferenceSourceReader) (ReferenceTarget, bool) {
	index, err := AnalyzeSource(source)
	if err != nil {
		return ReferenceTarget{}, false
	}
	offset, err := sourceindex.New(source).OffsetAt(sourceindex.Position{Line: position.Line, Character: position.Character})
	if err != nil {
		return ReferenceTarget{}, false
	}
	name, selected, ok := identifierAt(source, offset)
	if !ok {
		return ReferenceTarget{}, false
	}

	for _, reference := range append(append([]QualifiedReference(nil), index.Qualified...), index.QualifiedType...) {
		if !spanContains(reference.Span, offset) || index.LocalAt(reference.Alias, reference.Span.Start).Found {
			continue
		}
		for _, node := range index.Module.Nodes {
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || declaration.Name != reference.Alias || strings.HasPrefix(declaration.ModulePath, "std:") {
				continue
			}
			targetPath, ok := resolveImport(path, declaration.ModulePath)
			if !ok {
				return ReferenceTarget{}, false
			}
			targetSource, ok := readSource(targetPath)
			if !ok {
				return ReferenceTarget{}, false
			}
			targetModule, err := syntax.ParseAST(targetSource)
			if err != nil {
				return ReferenceTarget{}, false
			}
			var target ReferenceTarget
			found := false
			for _, candidate := range targetModule.Nodes {
				candidateName, _, span, valid := ASTDeclarationInfo(candidate)
				if !valid || candidateName != reference.Member || !ASTDeclarationExported(candidate) {
					continue
				}
				if found {
					return ReferenceTarget{}, false
				}
				target = ReferenceTarget{Path: targetPath, Name: candidateName, TypeName: IsTypeDeclaration(candidate), Declaration: span, Exported: true}
				found = true
			}

			return target, found
		}
	}

	if local := index.LocalAt(name, offset); local.Found {
		return ReferenceTarget{Path: path, Name: name, Declaration: local.Binding.Declaration, Local: true}, true
	}
	target, typeName, found := declarationAt(index, name, offset, selected)
	if !found {
		return ReferenceTarget{}, false
	}
	_, _, declaration, ok := ASTDeclarationInfo(index.Module.Nodes[target])
	if !ok {
		return ReferenceTarget{}, false
	}

	return ReferenceTarget{Path: path, Name: name, TypeName: typeName, Declaration: declaration, Exported: ASTDeclarationExported(index.Module.Nodes[target])}, true
}

// ReferencesForProject finds references to a resolved target in the supplied project snapshot.
func ReferencesForProject(target ReferenceTarget, sources map[string]string, resolveImport ReferenceImportResolver, includeDeclaration bool) []Reference {
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	var result []Reference
	for _, path := range paths {
		source := sources[path]
		index, err := AnalyzeSource(source)
		if err != nil {
			continue
		}
		if path == target.Path {
			if includeDeclaration {
				result = append(result, Reference{Path: path, Range: SourceRangeAt(source, target.Declaration)})
			}
			if target.Local {
				for _, reference := range index.References {
					if reference.Name != target.Name || reference.TypeName {
						continue
					}
					resolved := index.LocalAt(target.Name, reference.Span.Start)
					if resolved.Found && resolved.Binding.Declaration == target.Declaration {
						result = append(result, Reference{Path: path, Range: SourceRangeAt(source, reference.Span)})
					}
				}

				continue
			}
			declarationIndex := -1
			for i, node := range index.Module.Nodes {
				_, _, span, ok := ASTDeclarationInfo(node)
				if ok && span == target.Declaration {
					declarationIndex = i

					break
				}
			}
			if declarationIndex >= 0 {
				for _, reference := range index.TopLevelReferences(target.Name, declarationIndex, target.TypeName) {
					result = append(result, Reference{Path: path, Range: SourceRangeAt(source, reference.Span)})
				}
			}

			continue
		}
		if target.Local {
			continue
		}
		aliases := make(map[string]struct{})
		for _, node := range index.Module.Nodes {
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || strings.HasPrefix(declaration.ModulePath, "std:") {
				continue
			}
			resolved, ok := resolveImport(path, declaration.ModulePath)
			if ok && filepath.Clean(resolved) == filepath.Clean(target.Path) {
				aliases[declaration.Name] = struct{}{}
			}
		}
		for _, reference := range index.Qualified {
			if _, ok := aliases[reference.Alias]; !ok || reference.Member != target.Name || index.LocalAt(reference.Alias, reference.Span.Start).Found {
				continue
			}
			result = append(result, Reference{Path: path, Range: SourceRangeAt(source, reference.Span)})
		}
		if target.TypeName {
			for _, reference := range index.QualifiedType {
				if _, ok := aliases[reference.Alias]; !ok || reference.Member != target.Name || index.LocalAt(reference.Alias, reference.Span.Start).Found {
					continue
				}
				result = append(result, Reference{Path: path, Range: SourceRangeAt(source, reference.Span)})
			}
		}
	}

	return result
}

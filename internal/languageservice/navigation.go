package languageservice

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/sourceindex"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

// NavigationParams identifies a source position in a project-relative MCL file.
type NavigationParams struct {
	Entrypoint string
	Source     string
	Line       int
	Character  int
}

// SourceRange is a source range in zero-based UTF-16 coordinates.
type SourceRange struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Definition points to a declaration inside the in-memory project.
type Definition struct {
	Path  string      `json:"path"`
	Range SourceRange `json:"range"`
}

// Hover contains declaration information that is present in source syntax.
type Hover struct {
	Contents        string                        `json:"contents"`
	Range           SourceRange                   `json:"range"`
	StandardLibrary *StandardLibraryFunctionHover `json:"standardLibrary,omitempty"`
}

// StandardLibraryFunctionHover identifies a standard-library function without parsing its display text.
type StandardLibraryFunctionHover struct {
	Module string `json:"module"`
	Name   string `json:"name"`
}

// DefinitionForProjectFS finds a local or imported declaration without reading host files.
func DefinitionForProjectFS(params NavigationParams, projectFiles fs.FS) *Definition {
	resolved, ok := navigationTarget(params, projectFiles)
	if !ok {
		return nil
	}

	return &Definition{Path: resolved.path, Range: SourceRangeAt(resolved.source, resolved.Span)}
}

// HoverForProjectFS returns syntax-level declaration information from the in-memory project.
func HoverForProjectFS(params NavigationParams, projectFiles fs.FS) *Hover {
	resolved, ok := navigationTarget(params, projectFiles)
	if !ok {
		return standardLibraryHover(params, projectFiles)
	}
	contents := ""
	if resolved.node != nil {
		contents = DeclarationHover(resolved.node, resolved.source)
	} else if resolved.Span.Start >= 0 && resolved.Span.End <= len(resolved.source) && resolved.Span.Start < resolved.Span.End {
		name := resolved.source[resolved.Span.Start:resolved.Span.End]
		contents = "local " + name
	}
	if contents == "" {
		return nil
	}

	return &Hover{Contents: contents, Range: SourceRangeAt(resolved.source, resolved.Span)}
}

func standardLibraryHover(params NavigationParams, projectFiles fs.FS) *Hover {
	if projectFiles == nil || params.Entrypoint == "" {
		return nil
	}
	index, err := AnalyzeSource(params.Source)
	if err != nil {
		return nil
	}
	offset, err := sourceindex.New(params.Source).OffsetAt(sourceindex.Position{
		Line: params.Line, Character: params.Character,
	})
	if err != nil {
		return nil
	}

	for _, reference := range index.Qualified {
		if !spanContains(reference.Span, offset) || index.LocalAt(reference.Alias, reference.Span.Start).Found {
			continue
		}
		for _, node := range index.Module.Nodes {
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || declaration.Name != reference.Alias || !strings.HasPrefix(declaration.ModulePath, "std:") {
				continue
			}
			function, found := standardLibraryFunction(declaration.ModulePath, reference.Member)
			if !found {
				continue
			}

			return &Hover{
				Contents:        standardLibrarySignature(reference.Alias+"."+function.Name, function) + "\n" + function.Documentation,
				Range:           SourceRangeAt(params.Source, reference.Span),
				StandardLibrary: &StandardLibraryFunctionHover{Module: declaration.ModulePath, Name: function.Name},
			}
		}
	}

	for _, reference := range index.References {
		if !spanContains(reference.Span, offset) {
			continue
		}
		function, found := standardLibraryFunction("std:core", reference.Name)
		if !found {
			continue
		}

		return &Hover{
			Contents:        standardLibrarySignature(function.Name, function) + "\n" + function.Documentation,
			Range:           SourceRangeAt(params.Source, reference.Span),
			StandardLibrary: &StandardLibraryFunctionHover{Module: "std:core", Name: function.Name},
		}
	}

	return nil
}

func standardLibraryFunction(module, name string) (engine.StandardLibraryFunction, bool) {
	return engine.LookupStandardLibraryFunction(module, name)
}

func standardLibrarySignature(name string, function engine.StandardLibraryFunction) string {
	return fmt.Sprintf("%s(%s)", name, strings.Join(function.Parameters, ", "))
}

type navigationResult struct {
	path   string
	source string
	Span   syntax.SourceSpan
	node   syntax.ASTNode
}

func navigationTarget(params NavigationParams, projectFiles fs.FS) (navigationResult, bool) {
	if projectFiles == nil || params.Entrypoint == "" {
		return navigationResult{}, false
	}
	index, err := AnalyzeSource(params.Source)
	if err != nil {
		return navigationResult{}, false
	}
	position := sourceindex.Position{Line: params.Line, Character: params.Character}
	offset, err := sourceindex.New(params.Source).OffsetAt(position)
	if err != nil {
		return navigationResult{}, false
	}
	if target, found := importedNavigationTarget(params, projectFiles, index, offset); found {
		return target, true
	}
	for _, binding := range index.VisibleLocalBindings(offset) {
		if spanContains(binding.Declaration, offset) {
			return navigationResult{path: params.Entrypoint, source: params.Source, Span: binding.Declaration}, true
		}
	}
	for _, node := range index.Module.Nodes {
		_, _, span, ok := ASTDeclarationInfo(node)
		if ok && spanContains(span, offset) {
			return navigationResult{path: params.Entrypoint, source: params.Source, Span: span, node: node}, true
		}
	}
	for _, reference := range index.References {
		if !spanContains(reference.Span, offset) {
			continue
		}
		if local := index.LocalAt(reference.Name, offset); local.Found {
			return navigationResult{path: params.Entrypoint, source: params.Source, Span: local.Binding.Declaration}, true
		}
		declarationIndex := index.ResolveTopLevel(reference.Name, offset, reference.TypeName)
		if declarationIndex >= 0 {
			node := index.Module.Nodes[declarationIndex]
			_, _, span, ok := ASTDeclarationInfo(node)
			if ok {
				return navigationResult{path: params.Entrypoint, source: params.Source, Span: span, node: node}, true
			}
		}
	}

	return navigationResult{}, false
}

func importedNavigationTarget(params NavigationParams, projectFiles fs.FS, index *SemanticIndex, offset int) (navigationResult, bool) {
	references := append(append([]QualifiedReference(nil), index.Qualified...), index.QualifiedType...)
	for _, reference := range references {
		if !spanContains(reference.Span, offset) || index.LocalAt(reference.Alias, reference.Span.Start).Found {
			continue
		}
		for _, node := range index.Module.Nodes {
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || declaration.Name != reference.Alias || strings.HasPrefix(declaration.ModulePath, "std:") {
				continue
			}
			targetPath, resolved := ResolveProjectImport(params.Entrypoint, declaration.ModulePath, projectFiles)
			if !resolved {
				return navigationResult{}, false
			}
			sourceBytes, err := fs.ReadFile(projectFiles, targetPath)
			if err != nil {
				return navigationResult{}, false
			}
			source := string(sourceBytes)
			module, err := syntax.ParseAST(source)
			if err != nil {
				return navigationResult{}, false
			}
			var result navigationResult
			found := false
			for _, candidate := range module.Nodes {
				name, _, span, valid := ASTDeclarationInfo(candidate)
				if !valid || name != reference.Member || !ASTDeclarationExported(candidate) {
					continue
				}
				if found {
					return navigationResult{}, false
				}
				result = navigationResult{path: targetPath, source: source, Span: span, node: candidate}
				found = true
			}

			return result, found
		}
	}

	return navigationResult{}, false
}

func ResolveProjectImport(importer, modulePath string, projectFiles fs.FS) (string, bool) {
	if path.IsAbs(modulePath) || strings.Contains(modulePath, `\`) {
		return "", false
	}
	resolved := path.Clean(path.Join(path.Dir(importer), modulePath))
	if !fs.ValidPath(resolved) {
		return "", false
	}
	if _, err := fs.Stat(projectFiles, resolved); err != nil {
		return "", false
	}

	return resolved, true
}

func SourceRangeAt(source string, span syntax.SourceSpan) SourceRange {
	index := sourceindex.New(source)
	start := index.PositionAt(span.Start)
	end := index.PositionAt(span.End)

	return SourceRange{
		Start: Position{Line: start.Line, Character: start.Character},
		End:   Position{Line: end.Line, Character: end.Character},
	}
}

func spanContains(span syntax.SourceSpan, offset int) bool {
	return span.Start <= offset && offset <= span.End
}

func DeclarationHover(node syntax.ASTNode, source string) string {
	switch declaration := node.(type) {
	case *syntax.ImportDeclaration:
		return "module " + declaration.Name + " = \"" + declaration.ModulePath + "\""
	case *syntax.TypeDeclaration:
		prefix := "type "
		if declaration.Exported {
			prefix = "export " + prefix
		}

		return prefix + declaration.Name + " = " + TypeText(source, declaration.TypeSpec)
	case *syntax.LetDeclaration:
		prefix := "let "
		if declaration.Exported {
			prefix = "export " + prefix
		}
		contents := prefix + declaration.Name
		if declaration.Annotation != nil {
			contents += ": " + TypeText(source, declaration.Annotation)
		}

		return contents
	case *syntax.FunctionDeclaration:
		if declaration.Function == nil {
			return ""
		}
		prefix := "fn "
		if declaration.Exported {
			prefix = "export " + prefix
		}
		parameters := make([]string, 0, len(declaration.Function.Parameters))
		for _, parameter := range declaration.Function.Parameters {
			parameters = append(parameters, parameter.Name+": "+browserTypeExprText(source, parameter.Type))
		}
		contents := prefix + declaration.Name + "(" + strings.Join(parameters, ", ") + ")"
		if declaration.Function.ReturnType != nil {
			contents += " -> " + TypeText(source, declaration.Function.ReturnType)
		}

		return contents
	case *syntax.RuleDeclaration:
		if declaration.Rule == nil {
			return ""
		}
		prefix := "rule "
		if declaration.Exported {
			prefix = "export " + prefix
		}
		parameters := make([]string, 0, len(declaration.Rule.Parameters))
		for _, parameter := range declaration.Rule.Parameters {
			parameters = append(parameters, parameter.Name+": "+browserTypeExprText(source, parameter.Type))
		}

		return prefix + declaration.Name + "(" + strings.Join(parameters, ", ") + ")"
	default:
		return ""
	}
}

func TypeText(source string, typeExpr *syntax.TypeExpr) string {
	if typeExpr == nil {
		return ""
	}

	return browserTypeExprText(source, *typeExpr)
}

func browserTypeExprText(source string, typeExpr syntax.TypeExpr) string {
	span := typeExpr.Span
	if span.Start < 0 || span.End > len(source) || span.Start >= span.End {
		return ""
	}

	return strings.TrimSpace(source[span.Start:span.End])
}

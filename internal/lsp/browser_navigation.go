package lsp

import (
	"io/fs"
	"path"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/sourceindex"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

// BrowserNavigationParams identifies a source position in a project-relative MCL file.
type BrowserNavigationParams struct {
	Entrypoint string
	Source     string
	Line       int
	Character  int
}

// BrowserPosition is a zero-based UTF-16 source position.
type BrowserPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// BrowserRange is a source range in zero-based UTF-16 coordinates.
type BrowserRange struct {
	Start BrowserPosition `json:"start"`
	End   BrowserPosition `json:"end"`
}

// BrowserDefinition points to a declaration inside the in-memory project.
type BrowserDefinition struct {
	Path  string       `json:"path"`
	Range BrowserRange `json:"range"`
}

// BrowserHover contains declaration information that is present in source syntax.
type BrowserHover struct {
	Contents string       `json:"contents"`
	Range    BrowserRange `json:"range"`
}

// DefinitionForProjectFS finds a local or imported declaration without reading host files.
func DefinitionForProjectFS(params BrowserNavigationParams, projectFiles fs.FS) *BrowserDefinition {
	resolved, ok := browserNavigationTarget(params, projectFiles)
	if !ok {
		return nil
	}

	return &BrowserDefinition{Path: resolved.path, Range: browserSourceRange(resolved.source, resolved.span)}
}

// HoverForProjectFS returns syntax-level declaration information from the in-memory project.
func HoverForProjectFS(params BrowserNavigationParams, projectFiles fs.FS) *BrowserHover {
	resolved, ok := browserNavigationTarget(params, projectFiles)
	if !ok {
		return nil
	}
	contents := ""
	if resolved.node != nil {
		contents = browserDeclarationHover(resolved.node, resolved.source)
	} else if resolved.span.Start >= 0 && resolved.span.End <= len(resolved.source) && resolved.span.Start < resolved.span.End {
		name := resolved.source[resolved.span.Start:resolved.span.End]
		contents = "local " + name
	}
	if contents == "" {
		return nil
	}

	return &BrowserHover{Contents: contents, Range: browserSourceRange(resolved.source, resolved.span)}
}

type browserNavigationResult struct {
	path   string
	source string
	span   syntax.SourceSpan
	node   syntax.ASTNode
}

func browserNavigationTarget(params BrowserNavigationParams, projectFiles fs.FS) (browserNavigationResult, bool) {
	if projectFiles == nil || params.Entrypoint == "" {
		return browserNavigationResult{}, false
	}
	index, err := analyzeSource(params.Source)
	if err != nil {
		return browserNavigationResult{}, false
	}
	position := sourceindex.Position{Line: params.Line, Character: params.Character}
	offset, err := sourceindex.New(params.Source).OffsetAt(position)
	if err != nil {
		return browserNavigationResult{}, false
	}
	if target, found := browserImportedNavigationTarget(params, projectFiles, index, offset); found {
		return target, true
	}
	for _, binding := range index.visibleLocalBindings(offset) {
		if spanContains(binding.declaration, offset) {
			return browserNavigationResult{path: params.Entrypoint, source: params.Source, span: binding.declaration}, true
		}
	}
	for _, node := range index.module.Nodes {
		_, _, span, ok := astDeclarationInfo(node)
		if ok && spanContains(span, offset) {
			return browserNavigationResult{path: params.Entrypoint, source: params.Source, span: span, node: node}, true
		}
	}
	for _, reference := range index.references {
		if !spanContains(reference.span, offset) {
			continue
		}
		if local := index.localAt(reference.name, offset); local.found {
			return browserNavigationResult{path: params.Entrypoint, source: params.Source, span: local.binding.declaration}, true
		}
		declarationIndex := index.resolveTopLevel(reference.name, offset, reference.typeName)
		if declarationIndex >= 0 {
			node := index.module.Nodes[declarationIndex]
			_, _, span, ok := astDeclarationInfo(node)
			if ok {
				return browserNavigationResult{path: params.Entrypoint, source: params.Source, span: span, node: node}, true
			}
		}
	}

	return browserNavigationResult{}, false
}

func browserImportedNavigationTarget(params BrowserNavigationParams, projectFiles fs.FS, index *moduleSemanticIndex, offset int) (browserNavigationResult, bool) {
	references := append(append([]qualifiedReference(nil), index.qualified...), index.qualifiedTyp...)
	for _, reference := range references {
		if !spanContains(reference.span, offset) || index.localAt(reference.alias, reference.span.Start).found {
			continue
		}
		for _, node := range index.module.Nodes {
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || declaration.Name != reference.alias || strings.HasPrefix(declaration.ModulePath, "std:") {
				continue
			}
			targetPath, resolved := browserResolveProjectImport(params.Entrypoint, declaration.ModulePath, projectFiles)
			if !resolved {
				return browserNavigationResult{}, false
			}
			sourceBytes, err := fs.ReadFile(projectFiles, targetPath)
			if err != nil {
				return browserNavigationResult{}, false
			}
			source := string(sourceBytes)
			module, err := syntax.ParseAST(source)
			if err != nil {
				return browserNavigationResult{}, false
			}
			var result browserNavigationResult
			found := false
			for _, candidate := range module.Nodes {
				name, _, span, valid := astDeclarationInfo(candidate)
				if !valid || name != reference.member || !astDeclarationExported(candidate) {
					continue
				}
				if found {
					return browserNavigationResult{}, false
				}
				result = browserNavigationResult{path: targetPath, source: source, span: span, node: candidate}
				found = true
			}

			return result, found
		}
	}

	return browserNavigationResult{}, false
}

func browserResolveProjectImport(importer, modulePath string, projectFiles fs.FS) (string, bool) {
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

func browserSourceRange(source string, span syntax.SourceSpan) BrowserRange {
	index := sourceindex.New(source)
	start := index.PositionAt(span.Start)
	end := index.PositionAt(span.End)

	return BrowserRange{
		Start: BrowserPosition{Line: start.Line, Character: start.Character},
		End:   BrowserPosition{Line: end.Line, Character: end.Character},
	}
}

func spanContains(span syntax.SourceSpan, offset int) bool {
	return span.Start <= offset && offset <= span.End
}

func browserDeclarationHover(node syntax.ASTNode, source string) string {
	switch declaration := node.(type) {
	case *syntax.ImportDeclaration:
		return "module " + declaration.Name + " = \"" + declaration.ModulePath + "\""
	case *syntax.TypeDeclaration:
		prefix := "type "
		if declaration.Exported {
			prefix = "export " + prefix
		}
		return prefix + declaration.Name + " = " + browserTypeText(source, declaration.TypeSpec)
	case *syntax.LetDeclaration:
		prefix := "let "
		if declaration.Exported {
			prefix = "export " + prefix
		}
		contents := prefix + declaration.Name
		if declaration.Annotation != nil {
			contents += ": " + browserTypeText(source, declaration.Annotation)
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
			contents += " -> " + browserTypeText(source, declaration.Function.ReturnType)
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

func browserTypeText(source string, typeExpr *syntax.TypeExpr) string {
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

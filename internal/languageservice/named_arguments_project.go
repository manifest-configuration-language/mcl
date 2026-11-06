package languageservice

import (
	"io/fs"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

// ProjectNamedArgumentReference links a named call argument to an imported
// function parameter in another project file.
type ProjectNamedArgumentReference struct {
	Name            string
	Path            string
	Span            syntax.SourceSpan
	DeclarationPath string
	Declaration     syntax.SourceSpan
}

type importedFunctionModule struct {
	path      string
	functions map[string]*syntax.FunctionDeclaration
}

// NamedArgumentReferencesForProject resolves named arguments on qualified
// calls to exported functions available through projectFiles.
func NamedArgumentReferencesForProject(importerPath, source string, projectFiles fs.FS) []ProjectNamedArgumentReference {
	index, err := AnalyzeSource(source)
	if err != nil || projectFiles == nil {
		return nil
	}
	resolveImport := func(importer, modulePath string) (string, bool) {
		return ResolveProjectImport(importer, modulePath, projectFiles)
	}
	readSource := func(path string) (string, bool) {
		data, err := fs.ReadFile(projectFiles, path)

		return string(data), err == nil
	}

	return namedArgumentReferencesForSource(importerPath, index, resolveImport, readSource)
}

func namedArgumentReferencesForSource(sourcePath string, index *SemanticIndex, resolveImport ReferenceImportResolver, readSource ReferenceSourceReader) []ProjectNamedArgumentReference {
	if resolveImport == nil || readSource == nil {
		return nil
	}
	imports := make(map[string]string)
	ambiguousAliases := make(map[string]bool)
	for _, node := range index.Module.Nodes {
		declaration, ok := node.(*syntax.ImportDeclaration)
		if !ok || declaration.Text || strings.HasPrefix(declaration.ModulePath, "std:") {
			continue
		}
		if _, exists := imports[declaration.Name]; exists {
			ambiguousAliases[declaration.Name] = true

			continue
		}
		imports[declaration.Name] = declaration.ModulePath
	}
	modules := make(map[string]importedFunctionModule)
	var references []ProjectNamedArgumentReference
	VisitModuleExpressions(index.Module, func(expression syntax.Expr) {
		call, ok := expression.(*syntax.CallExpr)
		if !ok {
			return
		}
		callee, ok := call.Callee.(*syntax.ReferenceExpr)
		if !ok {
			return
		}
		alias, member, qualified := strings.Cut(callee.Name, ".")
		if !qualified || alias == "" || member == "" || strings.Contains(member, ".") || ambiguousAliases[alias] || index.LocalAt(alias, callee.ExprSpan().Start).Found {
			return
		}
		modulePath, imported := imports[alias]
		if !imported {
			return
		}
		module, loaded := modules[alias]
		if !loaded {
			module = loadImportedFunctionModuleUsing(sourcePath, modulePath, resolveImport, readSource)
			modules[alias] = module
		}
		function := module.functions[member]
		if function == nil {
			return
		}
		for argumentIndex, name := range call.ArgumentNames {
			if name == "" || argumentIndex >= len(call.ArgumentNameSpans) {
				continue
			}
			for _, parameter := range function.Function.Parameters {
				if parameter.Name == name {
					references = append(references, ProjectNamedArgumentReference{
						Name: name, Path: sourcePath, Span: call.ArgumentNameSpans[argumentIndex],
						DeclarationPath: module.path, Declaration: parameter.Span,
					})

					break
				}
			}
		}
	})

	return references
}

func loadImportedFunctionModuleUsing(importerPath, modulePath string, resolveImport ReferenceImportResolver, readSource ReferenceSourceReader) importedFunctionModule {
	targetPath, resolved := resolveImport(importerPath, modulePath)
	if !resolved {
		return importedFunctionModule{}
	}
	targetSource, found := readSource(targetPath)
	if !found {
		return importedFunctionModule{}
	}
	targetModule, err := syntax.ParseAST(targetSource)
	if err != nil {
		return importedFunctionModule{}
	}
	result := importedFunctionModule{path: targetPath, functions: make(map[string]*syntax.FunctionDeclaration)}
	ambiguousFunctions := make(map[string]bool)
	for _, node := range targetModule.Nodes {
		function, ok := node.(*syntax.FunctionDeclaration)
		if !ok || !function.Exported || function.Function == nil {
			continue
		}
		if _, exists := result.functions[function.Name]; exists {
			delete(result.functions, function.Name)
			ambiguousFunctions[function.Name] = true

			continue
		}
		if !ambiguousFunctions[function.Name] {
			result.functions[function.Name] = function
		}
	}

	return result
}

package languageservice

import (
	"context"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

// RenameForProjectFS renames a local symbol or exported declaration across the
// MCL files available in projectFiles.
func RenameForProjectFS(params NavigationParams, projectFiles fs.FS, newName string) *RenameResult {
	if !IsMCLIdentifier(newName) {
		return nil
	}
	snapshot, files, ok := projectSourceSnapshot(params, projectFiles)
	if !ok {
		return nil
	}
	sources := snapshot.Sources()
	resolveImport := ReferenceImportResolver(snapshot.ResolveImport)
	target, found := referenceTargetAtWithSnapshot(context.Background(), params.Entrypoint, params.Source, Position{Line: params.Line, Character: params.Character}, resolveImport, snapshot.Source, snapshot)
	if !found || target.Name == newName {
		return nil
	}
	if target.ImportAlias {
		index, exists := snapshot.SemanticIndex(params.Entrypoint)
		if !exists {
			return nil
		}
		return renameImportAliasWithIndex(params.Entrypoint, params.Source, target.Declaration, newName, index)
	}
	if target.FunctionParameter {
		return renameFunctionParameterForProjectSnapshot(target, newName, sources, resolveImport, snapshot)
	}
	if target.Local || !target.Exported {
		return RenameForSource(params.Entrypoint, params.Source, Position{Line: params.Line, Character: params.Character}, newName)
	}
	targetSource, exists := sources[target.Path]
	if !exists {
		return nil
	}
	index, exists := snapshot.SemanticIndex(target.Path)
	if !exists {
		return nil
	}
	declarationIndex := declarationIndexAtSpan(index, target.Declaration)
	if declarationIndex < 0 || !ASTDeclarationExported(index.Module.Nodes[declarationIndex]) || !IsRenameableASTDeclaration(index.Module.Nodes[declarationIndex]) {
		return nil
	}
	if !uniqueExportedDeclaration(index, target.Name, target.TypeName, declarationIndex) || exportedNameExists(index, newName) {
		return nil
	}
	spans := topLevelRenameSpans(targetSource, index, declarationIndex, target.Name, newName, target.TypeName)
	if spans == nil || !candidateParses(targetSource, spans, newName) {
		return nil
	}
	result := &RenameResult{Edits: make([]RenameEdit, 0, len(spans))}
	for _, span := range spans {
		result.Edits = append(result.Edits, RenameEdit{Path: target.Path, Range: SourceRangeAt(targetSource, span), NewText: newName})
	}
	var dependents engine.DependentMCLSourceIndex
	dependentSources, err := dependents.SourcesForProjectFS(filepath.Join(filepath.Clean("/"), "mcl-project"), target.Path, files, nil)
	if err != nil {
		return nil
	}
	for sourcePath, source := range dependentSources {
		if sourcePath == filepath.Join(filepath.Clean("/"), "mcl-project", filepath.FromSlash(target.Path)) {
			continue
		}
		relativePath, err := filepath.Rel(filepath.Join(filepath.Clean("/"), "mcl-project"), sourcePath)
		if err != nil {
			return nil
		}
		relativePath = filepath.ToSlash(relativePath)
		importerIndex, exists := snapshot.SemanticIndex(relativePath)
		if !exists {
			return nil
		}
		aliases, valid := aliasesForTarget(relativePath, importerIndex.Module, target.Path, resolveImport)
		if !valid {
			return nil
		}
		importerSpans, valid := qualifiedRenameSpans(source, importerIndex, aliases, target, newName)
		if !valid {
			return nil
		}
		if len(importerSpans) == 0 {
			continue
		}
		if !candidateParses(source, importerSpans, newName) {
			return nil
		}
		for _, span := range importerSpans {
			result.Edits = append(result.Edits, RenameEdit{Path: relativePath, Range: SourceRangeAt(source, span), NewText: newName})
		}
	}
	slices.SortFunc(result.Edits, func(left, right RenameEdit) int {
		if left.Path != right.Path {
			return strings.Compare(left.Path, right.Path)
		}
		if left.Range.Start.Line != right.Range.Start.Line {
			return left.Range.Start.Line - right.Range.Start.Line
		}

		return left.Range.Start.Character - right.Range.Start.Character
	})

	return result
}

// RenameFunctionParameterForProjectSources renames a function parameter across the supplied project snapshot.
func RenameFunctionParameterForProjectSources(target ReferenceTarget, newName string, sources map[string]string, resolveImport ReferenceImportResolver) *RenameResult {
	if !IsMCLIdentifier(newName) || !target.FunctionParameter {
		return nil
	}

	return renameFunctionParameterForProject(target, newName, sources, resolveImport)
}

// RenameFunctionParameterForSnapshot renames a parameter using cached semantic indexes.
func RenameFunctionParameterForSnapshot(target ReferenceTarget, newName string, sources map[string]string, snapshot *ProjectSemanticSnapshot) *RenameResult {
	if !IsMCLIdentifier(newName) || !target.FunctionParameter || snapshot == nil {
		return nil
	}

	return renameFunctionParameterForProjectSnapshot(target, newName, sources, snapshot.ResolveImport, snapshot)
}

func renameFunctionParameterForProject(target ReferenceTarget, newName string, sources map[string]string, resolveImport ReferenceImportResolver) *RenameResult {
	return renameFunctionParameterForProjectSnapshot(target, newName, sources, resolveImport, nil)
}

func renameFunctionParameterForProjectSnapshot(target ReferenceTarget, newName string, sources map[string]string, resolveImport ReferenceImportResolver, snapshot *ProjectSemanticSnapshot) *RenameResult {
	targetSource, exists := sources[target.Path]
	if !exists {
		return nil
	}
	var semanticIndex *SemanticIndex
	if snapshot != nil {
		var exists bool
		semanticIndex, exists = snapshot.SemanticIndex(target.Path)
		if !exists {
			return nil
		}
	} else {
		var err error
		semanticIndex, err = AnalyzeSource(targetSource)
		if err != nil {
			return nil
		}
	}
	parameterFunction := ""
	functionNames := make(map[string]int)
	for _, node := range semanticIndex.Module.Nodes {
		function, ok := node.(*syntax.FunctionDeclaration)
		if !ok || function.Function == nil {
			continue
		}
		functionNames[function.Name]++
		for _, parameter := range function.Function.Parameters {
			if parameter.Span == target.Declaration {
				parameterFunction = function.Name
			}
		}
	}
	if parameterFunction == "" || functionNames[parameterFunction] != 1 {
		return nil
	}
	var binding *ScopedLocalBinding
	for bindingIndex := range semanticIndex.Locals {
		candidate := &semanticIndex.Locals[bindingIndex]
		if candidate.Name == target.Name && candidate.Declaration == target.Declaration {
			binding = candidate

			break
		}
	}
	if binding == nil {
		return nil
	}
	spans := localRenameSpans(targetSource, target.Name, newName, *binding, semanticIndex.Locals, semanticIndex.References)
	if spans == nil {
		return nil
	}
	for _, reference := range semanticIndex.NamedArguments {
		if reference.Declaration == target.Declaration {
			spans = append(spans, reference.Span)
		}
	}
	spans = uniqueValidSpans(targetSource, target.Name, spans)
	if spans == nil || !candidateParses(targetSource, spans, newName) {
		return nil
	}
	result := &RenameResult{Edits: make([]RenameEdit, 0, len(spans))}
	for _, span := range spans {
		result.Edits = append(result.Edits, RenameEdit{Path: target.Path, Range: SourceRangeAt(targetSource, span), NewText: newName})
	}

	paths := make([]string, 0, len(sources))
	for sourcePath := range sources {
		paths = append(paths, sourcePath)
	}
	slices.Sort(paths)
	for _, sourcePath := range paths {
		source := sources[sourcePath]
		var importerIndex *SemanticIndex
		if snapshot != nil {
			var exists bool
			importerIndex, exists = snapshot.SemanticIndex(sourcePath)
			if !exists {
				return nil
			}
		} else {
			var err error
			importerIndex, err = AnalyzeSource(source)
			if err != nil {
				return nil
			}
		}
		aliasesToTarget := make(map[string]bool)
		aliasCounts := make(map[string]int)
		for _, node := range importerIndex.Module.Nodes {
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || strings.HasPrefix(declaration.ModulePath, "std:") {
				continue
			}
			aliasCounts[declaration.Name]++
			if declaration.Text {
				continue
			}
			resolvedPath, resolved := resolveImport(sourcePath, declaration.ModulePath)
			if !resolved || resolvedPath != target.Path {
				continue
			}
			aliasesToTarget[declaration.Name] = true
		}
		for alias := range aliasesToTarget {
			if aliasCounts[alias] > 1 {
				return nil
			}
		}
		readSource := func(path string) (string, bool) {
			source, exists := sources[path]

			return source, exists
		}
		var importerSpans []syntax.SourceSpan
		for _, reference := range namedArgumentReferencesForSnapshot(sourcePath, importerIndex, resolveImport, readSource, snapshot) {
			if reference.DeclarationPath == target.Path && reference.Declaration == target.Declaration {
				importerSpans = append(importerSpans, reference.Span)
			}
		}
		if len(importerSpans) == 0 {
			continue
		}
		importerSpans = uniqueValidSpans(source, target.Name, importerSpans)
		if importerSpans == nil || !candidateParses(source, importerSpans, newName) {
			return nil
		}
		for _, span := range importerSpans {
			result.Edits = append(result.Edits, RenameEdit{Path: sourcePath, Range: SourceRangeAt(source, span), NewText: newName})
		}
	}
	slices.SortFunc(result.Edits, func(left, right RenameEdit) int {
		if left.Path != right.Path {
			return strings.Compare(left.Path, right.Path)
		}
		if left.Range.Start.Line != right.Range.Start.Line {
			return left.Range.Start.Line - right.Range.Start.Line
		}

		return left.Range.Start.Character - right.Range.Start.Character
	})

	return result
}

func renameImportAlias(sourcePath, source string, declarationSpan syntax.SourceSpan, newName string) *RenameResult {
	index, err := AnalyzeSource(source)
	if err != nil {
		return nil
	}
	return renameImportAliasWithIndex(sourcePath, source, declarationSpan, newName, index)
}

func renameImportAliasWithIndex(sourcePath, source string, declarationSpan syntax.SourceSpan, newName string, index *SemanticIndex) *RenameResult {
	var declaration *syntax.ImportDeclaration
	for _, node := range index.Module.Nodes {
		candidate, ok := node.(*syntax.ImportDeclaration)
		if ok && candidate.NameSpan == declarationSpan {
			if declaration != nil {
				return nil
			}
			declaration = candidate
		}
	}
	if declaration == nil {
		return nil
	}
	for _, node := range index.Module.Nodes {
		name, _, span, ok := ASTDeclarationInfo(node)
		if ok && name == newName && span != declarationSpan {
			return nil
		}
	}
	spans := []syntax.SourceSpan{declarationSpan}
	for _, reference := range append(append([]QualifiedReference(nil), index.Qualified...), index.QualifiedType...) {
		if reference.Alias != declaration.Name || index.LocalAt(declaration.Name, reference.Span.Start).Found {
			continue
		}
		if index.LocalAt(newName, reference.Span.Start).Found {
			return nil
		}
		aliasSpan, ok := qualifiedAliasSpan(source, declaration.Name, reference.Span)
		if !ok {
			return nil
		}
		spans = append(spans, aliasSpan)
	}
	spans = uniqueValidSpans(source, declaration.Name, spans)
	if spans == nil || !candidateParses(source, spans, newName) {
		return nil
	}
	result := &RenameResult{Edits: make([]RenameEdit, len(spans))}
	for index, span := range spans {
		result.Edits[index] = RenameEdit{Path: sourcePath, Range: SourceRangeAt(source, span), NewText: newName}
	}
	slices.SortFunc(result.Edits, func(left, right RenameEdit) int {
		if left.Range.Start.Line != right.Range.Start.Line {
			return left.Range.Start.Line - right.Range.Start.Line
		}

		return left.Range.Start.Character - right.Range.Start.Character
	})

	return result
}

func qualifiedAliasSpan(source, alias string, member syntax.SourceSpan) (syntax.SourceSpan, bool) {
	if member.Start > len(source) || member.End > len(source) {
		return syntax.SourceSpan{}, false
	}
	start := member.Start - len(alias) - 1
	if start >= 0 && source[start:member.Start] == alias+"." {
		return syntax.SourceSpan{Start: start, End: start + len(alias)}, true
	}
	start = member.Start - len(alias) - 2
	if start < 0 || source[start:member.Start] != alias+"?." {
		return syntax.SourceSpan{}, false
	}

	return syntax.SourceSpan{Start: start, End: start + len(alias)}, true
}

func declarationIndexAtSpan(index *SemanticIndex, span syntax.SourceSpan) int {
	for declarationIndex, node := range index.Module.Nodes {
		_, _, candidateSpan, ok := ASTDeclarationInfo(node)
		if ok && candidateSpan == span {
			return declarationIndex
		}
	}

	return -1
}

func uniqueExportedDeclaration(index *SemanticIndex, name string, typeName bool, target int) bool {
	count := 0
	for declarationIndex, node := range index.Module.Nodes {
		candidateName, _, _, ok := ASTDeclarationInfo(node)
		if ok && candidateName == name && IsTypeDeclaration(node) == typeName && ASTDeclarationExported(node) {
			count++
			if declarationIndex != target {
				return false
			}
		}
	}

	return count == 1
}

func exportedNameExists(index *SemanticIndex, name string) bool {
	for _, node := range index.Module.Nodes {
		candidateName, _, _, ok := ASTDeclarationInfo(node)
		if ok && candidateName == name && ASTDeclarationExported(node) {
			return true
		}
	}

	return false
}

func aliasesForTarget(importer string, module syntax.ModuleAST, targetPath string, resolveImport ReferenceImportResolver) (map[string]struct{}, bool) {
	aliases := make(map[string]struct{})
	for _, node := range module.Nodes {
		declaration, ok := node.(*syntax.ImportDeclaration)
		if !ok || strings.HasPrefix(declaration.ModulePath, "std:") {
			continue
		}
		resolved, ok := resolveImport(importer, declaration.ModulePath)
		if !ok || resolved != targetPath {
			continue
		}
		if _, exists := aliases[declaration.Name]; exists {
			return nil, false
		}
		aliases[declaration.Name] = struct{}{}
	}

	return aliases, true
}

func qualifiedRenameSpans(source string, index *SemanticIndex, aliases map[string]struct{}, target ReferenceTarget, newName string) ([]syntax.SourceSpan, bool) {
	references := index.Qualified
	if target.TypeName {
		references = index.QualifiedType
	}
	var spans []syntax.SourceSpan
	for _, reference := range references {
		if reference.Member != target.Name {
			continue
		}
		if _, imported := aliases[reference.Alias]; !imported || index.LocalAt(reference.Alias, reference.Span.Start).Found {
			continue
		}
		if index.LocalAt(newName, reference.Span.Start).Found {
			return nil, false
		}
		spans = append(spans, reference.Span)
	}
	if len(spans) == 0 {
		return nil, true
	}

	validSpans := uniqueValidSpans(source, target.Name, spans)
	if validSpans == nil {
		return nil, false
	}

	return validSpans, true
}

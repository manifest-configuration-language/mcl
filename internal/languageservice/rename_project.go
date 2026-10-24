package languageservice

import (
	"io/fs"
	"path"
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
	sources, files, ok := projectSourceSnapshot(params, projectFiles)
	if !ok {
		return nil
	}
	resolveImport := func(importer, modulePath string) (string, bool) {
		if path.IsAbs(modulePath) || strings.Contains(modulePath, `\`) {
			return "", false
		}
		resolved := path.Clean(path.Join(path.Dir(importer), modulePath))
		if !fs.ValidPath(resolved) {
			return "", false
		}
		_, exists := sources[resolved]

		return resolved, exists
	}
	target, found := ReferenceTargetAt(params.Entrypoint, params.Source, Position{Line: params.Line, Character: params.Character}, resolveImport, func(sourcePath string) (string, bool) {
		source, exists := sources[sourcePath]

		return source, exists
	})
	if !found || target.Name == newName {
		return nil
	}
	if target.ImportAlias {
		return renameImportAlias(params.Entrypoint, params.Source, target.Declaration, newName)
	}
	if target.Local || !target.Exported {
		return RenameForSource(params.Entrypoint, params.Source, Position{Line: params.Line, Character: params.Character}, newName)
	}
	targetSource, exists := sources[target.Path]
	if !exists {
		return nil
	}
	index, err := AnalyzeSource(targetSource)
	if err != nil {
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
		importerIndex, err := AnalyzeSource(source)
		if err != nil {
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

func renameImportAlias(sourcePath, source string, declarationSpan syntax.SourceSpan, newName string) *RenameResult {
	index, err := AnalyzeSource(source)
	if err != nil {
		return nil
	}
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

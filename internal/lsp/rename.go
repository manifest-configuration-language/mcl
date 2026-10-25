package lsp

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/languageservice"
	"github.com/manifest-configuration-language/mcl/internal/sourceindex"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

type definitionParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	} `json:"position"`
}

type renameParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position location `json:"position"`
	NewName  string   `json:"newName"`
}

type textEdit struct {
	Range struct {
		Start location `json:"start"`
		End   location `json:"end"`
	} `json:"range"`
	NewText string `json:"newText"`
}

type workspaceEdit struct {
	Changes map[string][]textEdit `json:"changes"`
}

type definitionLocation struct {
	URI   string `json:"uri"`
	Range struct {
		Start location `json:"start"`
		End   location `json:"end"`
	} `json:"range"`
}

type location struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

func (s *server) definition(params definitionParams) *definitionLocation {
	state, exists := s.documents[params.TextDocument.URI]
	if !exists {
		return nil
	}
	lines := strings.Split(state.Text, "\n")
	if params.Position.Line < 0 || params.Position.Line >= len(lines) {
		return nil
	}
	name, ok := wordAtUTF16(lines[params.Position.Line], params.Position.Character)
	if !ok {
		return nil
	}
	if offset, err := offsetAtUTF16(state.Text, params.Position); err == nil {
		if index, err := analyzeSource(state.Text); err == nil {
			module := index.Module
			if result, handled := s.importedDefinition(params.TextDocument.URI, module, offset, index); handled {
				return result
			}
			for _, binding := range index.VisibleLocalBindings(offset) {
				if binding.Name == name {
					return sourceDefinitionLocation(params.TextDocument.URI, state.Text, binding.Declaration)
				}
			}
			if declarationIndex := index.DeclarationAt(name, offset); declarationIndex >= 0 {
				_, _, nameSpan, _ := astDeclarationInfo(module.Nodes[declarationIndex])

				return sourceDefinitionLocation(params.TextDocument.URI, state.Text, nameSpan)
			}
			declarationIndex := index.ResolveTopLevel(name, offset, false)
			if declarationIndex < 0 {
				return nil
			}
			_, _, nameSpan, _ := astDeclarationInfo(module.Nodes[declarationIndex])

			return sourceDefinitionLocation(params.TextDocument.URI, state.Text, nameSpan)
		}
	}
	var result *definitionLocation
	for lineNumber, line := range lines {
		declaration, start, exists := declaredName(line)
		if !exists || declaration != name {
			continue
		}
		loc := &definitionLocation{URI: params.TextDocument.URI}
		loc.Range.Start = location{Line: lineNumber, Character: start}
		loc.Range.End = location{Line: lineNumber, Character: start + utf16Length([]rune(declaration))}
		if lineNumber <= params.Position.Line {
			result = loc
		}
	}

	return result
}

func (s *server) importedDefinition(uri string, current syntax.ModuleAST, offset int, index *moduleSemanticIndex) (*definitionLocation, bool) {
	currentPath := documentPath(uri)
	if currentPath == "" {
		return nil, false
	}
	references := append(slices.Clone(index.Qualified), index.QualifiedType...)
	for _, reference := range references {
		if offset < reference.Span.Start || offset > reference.Span.End {
			continue
		}
		var targetPath string
		foundImport := false
		for _, node := range current.Nodes {
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || declaration.Name != reference.Alias {
				continue
			}
			foundImport = true
			if index.LocalAt(reference.Alias, reference.Span.Start).Found || strings.HasPrefix(declaration.ModulePath, "std:") {
				return nil, true
			}
			resolved, err := engine.ResolveImportPath(currentPath, declaration.ModulePath)
			if err != nil {
				return nil, true
			}
			if targetPath != "" && targetPath != resolved {
				return nil, true
			}
			targetPath = resolved
		}
		if targetPath == "" {
			if foundImport {
				return nil, true
			}

			continue
		}
		targetURI := filePathToURI(targetPath)
		source, err := os.ReadFile(targetPath)
		if err != nil {
			return nil, true
		}
		for openURI, document := range s.documents {
			if documentPath(openURI) == targetPath {
				targetURI = openURI
				source = []byte(document.Text)

				break
			}
		}
		module, err := syntax.ParseAST(string(source))
		if err != nil {
			return nil, true
		}
		var declarationSpan syntax.SourceSpan
		for _, node := range module.Nodes {
			name, _, span, ok := astDeclarationInfo(node)
			if !ok || name != reference.Member || !astDeclarationExported(node) {
				continue
			}
			if declarationSpan.End > declarationSpan.Start {
				return nil, true
			}
			declarationSpan = span
		}
		if declarationSpan.End <= declarationSpan.Start {
			return nil, true
		}

		return sourceDefinitionLocation(targetURI, string(source), declarationSpan), true
	}

	return nil, false
}

type schemaFieldOccurrence struct {
	name       string
	parentPath []string
	annotation string
	identity   string
	entry      syntax.SourceSpan
	siblings   []string
}

func (s *server) renameSchemaField(ctx context.Context, params renameParams, name string, offset int, module syntax.ModuleAST, source string) (*workspaceEdit, bool) {
	if ctx.Err() != nil {
		return nil, true
	}
	path := documentPath(params.TextDocument.URI)
	if path == "" {
		return nil, false
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, false
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}

	var occurrences []schemaFieldOccurrence
	annotationByBinding := make(map[string]string)
	pathByBinding := make(map[string][]string)
	functionReturnSchemas := schemaFunctionReturnSchemas(module, source)
	for _, node := range module.Nodes {
		if ctx.Err() != nil {
			return nil, true
		}
		declaration, ok := node.(*syntax.LetDeclaration)
		if !ok || declaration.Expression == nil {
			continue
		}
		if declaration.Annotation != nil {
			annotationSpan := declaration.Annotation.Span
			if annotationSpan.Start >= 0 && annotationSpan.End <= len(source) && annotationSpan.Start < annotationSpan.End {
				annotationByBinding[declaration.Name] = source[annotationSpan.Start:annotationSpan.End]
			}
		} else {
			annotationByBinding[declaration.Name], pathByBinding[declaration.Name] = schemaBindingFromExpression(declaration.Expression, annotationByBinding, pathByBinding, functionReturnSchemas)
		}
		if annotation := annotationByBinding[declaration.Name]; annotation != "" {
			collectSchemaFieldOccurrences(declaration.Expression, annotation, pathByBinding[declaration.Name], &occurrences)
		}
	}
	var target *schemaFieldOccurrence
	for index := range occurrences {
		if ctx.Err() != nil {
			return nil, true
		}
		occurrence := &occurrences[index]
		fieldStart, fieldEnd, ok := schemaFieldSourceRange(source, occurrence)
		if !ok || occurrence.name != name || offset < fieldStart || offset > fieldEnd {
			continue
		}
		fields, ok := schemaFieldsAtOccurrence(ctx, path, source, occurrence, index)
		if !ok || !slices.Contains(fields, occurrence.name) {
			continue
		}
		if target != nil && (target.annotation != occurrence.annotation || !slices.Equal(target.parentPath, occurrence.parentPath)) {
			return nil, true
		}
		target = occurrence
	}
	if target == nil {
		for _, reference := range schemaFieldReferenceOccurrences(module, source, annotationByBinding, pathByBinding) {
			if ctx.Err() != nil {
				return nil, true
			}
			if reference.entry.Start > offset || offset > reference.entry.End || reference.name != name {
				continue
			}
			fields, fieldErr := engine.SchemaTypeFieldsWithContext(ctx, path, source, reference.annotation, reference.parentPath)
			if fieldErr != nil || !slices.Contains(fields, reference.name) {
				continue
			}
			if slices.Contains(fields, params.NewName) {
				return nil, true
			}
			for index := range occurrences {
				if ctx.Err() != nil {
					return nil, true
				}
				occurrence := &occurrences[index]
				if occurrence.name != reference.name || occurrence.annotation != reference.annotation || !slices.Equal(occurrence.parentPath, reference.parentPath) {
					continue
				}
				fields, ok := schemaFieldsAtOccurrence(ctx, path, source, occurrence, index)
				if ok && slices.Contains(fields, occurrence.name) {
					target = occurrence

					break
				}
			}
			if target != nil {
				break
			}
			target = &schemaFieldOccurrence{
				name: reference.name, parentPath: slices.Clone(reference.parentPath),
				annotation: reference.annotation, entry: reference.entry,
			}

			break
		}
	}
	if target == nil {
		return nil, false
	}
	overlays, openURIs := s.renameOverlays()
	identity := schemaAnnotationIdentityWithSources(path, module, target.annotation, overlays, make(map[string]bool))

	changes := make(map[string][]textEdit)
	uriByPath := map[string]string{filepath.Clean(path): params.TextDocument.URI}
	sourceByPath := map[string]string{filepath.Clean(path): source}
	moduleByPath := map[string]syntax.ModuleAST{filepath.Clean(path): module}
	annotationByPath := map[string]map[string]string{filepath.Clean(path): annotationByBinding}
	bindingsPathByPath := map[string]map[string][]string{filepath.Clean(path): pathByBinding}
	if s.renameIndex == nil {
		s.renameIndex = &engine.DependentMCLSourceIndex{}
	}
	var sources map[string]string
	if s.watchRegistered {
		sources, err = s.renameIndex.SourcesFromIndexWithContext(ctx, path, s.workspaceRoots, overlays)
	} else {
		sources, err = s.renameIndex.SourcesWithContext(ctx, path, s.workspaceRoots, overlays)
	}
	if err != nil {
		return nil, true
	}
	for candidatePath, candidateSource := range sources {
		if ctx.Err() != nil {
			return nil, true
		}
		candidatePath, pathErr := filepath.Abs(candidatePath)
		if pathErr != nil {
			return nil, true
		}
		if resolved, resolveErr := filepath.EvalSymlinks(candidatePath); resolveErr == nil {
			candidatePath = resolved
		}
		candidatePath = filepath.Clean(candidatePath)
		if candidatePath == filepath.Clean(path) {
			continue
		}
		candidateModule, parseErr := syntax.ParseAST(candidateSource)
		if parseErr != nil {
			return nil, true
		}
		candidateAnnotations, candidatePaths := schemaDocumentOccurrences(candidateModule, candidateSource)
		candidateURI := openURIs[candidatePath]
		if candidateURI == "" {
			candidateURI = filePathToURI(candidatePath)
		}
		uriByPath[candidatePath] = candidateURI
		sourceByPath[candidatePath] = candidateSource
		moduleByPath[candidatePath] = candidateModule
		annotationByPath[candidatePath] = candidateAnnotations
		bindingsPathByPath[candidatePath] = candidatePaths
	}

	for documentPath, documentSource := range sourceByPath {
		if ctx.Err() != nil {
			return nil, true
		}
		documentModule := moduleByPath[documentPath]
		documentAnnotations := annotationByPath[documentPath]
		documentPaths := bindingsPathByPath[documentPath]
		var documentOccurrences []schemaFieldOccurrence
		for _, node := range documentModule.Nodes {
			declaration, ok := node.(*syntax.LetDeclaration)
			if !ok || declaration.Expression == nil || documentAnnotations[declaration.Name] == "" {
				continue
			}
			collectSchemaFieldOccurrences(declaration.Expression, documentAnnotations[declaration.Name], documentPaths[declaration.Name], &documentOccurrences)
		}
		for index := range documentOccurrences {
			documentOccurrences[index].identity = schemaAnnotationIdentityWithSources(documentPath, documentModule, documentOccurrences[index].annotation, overlays, make(map[string]bool))
		}
		spans := make(map[syntax.SourceSpan]struct{})
		for index := range documentOccurrences {
			occurrence := &documentOccurrences[index]
			if occurrence.name != target.name || occurrence.identity != identity || !slices.Equal(occurrence.parentPath, target.parentPath) {
				continue
			}
			if slices.Contains(occurrence.siblings, params.NewName) {
				return nil, true
			}
			fields, valid := schemaFieldsAtOccurrence(ctx, documentPath, documentSource, occurrence, index)
			if !valid || !slices.Contains(fields, occurrence.name) {
				return nil, true
			}
			if slices.Contains(fields, params.NewName) {
				return nil, true
			}
			start, end, valid := schemaFieldSourceRange(documentSource, occurrence)
			if !valid {
				return nil, true
			}
			spans[syntax.SourceSpan{Start: start, End: end}] = struct{}{}
		}
		for _, reference := range schemaFieldReferenceOccurrences(documentModule, documentSource, documentAnnotations, documentPaths) {
			if reference.name != target.name || schemaAnnotationIdentityWithSources(documentPath, documentModule, reference.annotation, overlays, make(map[string]bool)) != identity || !slices.Equal(reference.parentPath, target.parentPath) {
				continue
			}
			fields, fieldErr := engine.SchemaTypeFieldsWithContext(ctx, documentPath, documentSource, reference.annotation, reference.parentPath)
			if fieldErr != nil {
				return nil, true
			}
			if !slices.Contains(fields, reference.name) {
				continue
			}
			if slices.Contains(fields, params.NewName) {
				return nil, true
			}
			spans[reference.entry] = struct{}{}
		}
		if len(spans) == 0 {
			continue
		}
		editSpans := make([]syntax.SourceSpan, 0, len(spans))
		for span := range spans {
			editSpans = append(editSpans, span)
		}
		slices.SortFunc(editSpans, func(left, right syntax.SourceSpan) int { return right.Start - left.Start })
		candidate := documentSource
		for _, span := range editSpans {
			candidate = candidate[:span.Start] + params.NewName + candidate[span.End:]
		}
		if _, parseErr := syntax.ParseAST(candidate); parseErr != nil {
			return nil, true
		}
		documentIndex := sourceindex.New(documentSource)
		edits := make([]textEdit, len(editSpans))
		for index, span := range editSpans {
			edits[index].Range.Start = sourcePositionAt(documentIndex, span.Start)
			edits[index].Range.End = sourcePositionAt(documentIndex, span.End)
			edits[index].NewText = params.NewName
		}
		slices.SortFunc(edits, func(left, right textEdit) int {
			if left.Range.Start.Line != right.Range.Start.Line {
				return left.Range.Start.Line - right.Range.Start.Line
			}

			return left.Range.Start.Character - right.Range.Start.Character
		})
		changes[uriByPath[documentPath]] = edits
	}
	if len(changes) == 0 {
		return nil, true
	}

	return &workspaceEdit{Changes: changes}, true
}

func collectSchemaFieldOccurrences(expression syntax.Expr, annotation string, parentPath []string, occurrences *[]schemaFieldOccurrence) {
	switch expression := expression.(type) {
	case *syntax.MapExpr:
		siblings := make([]string, 0, len(expression.Entries))
		for _, entry := range expression.Entries {
			if entry.KeyExpr == nil {
				siblings = append(siblings, entry.Key)
			}
		}
		for _, entry := range expression.Entries {
			if entry.KeyExpr == nil {
				*occurrences = append(*occurrences, schemaFieldOccurrence{name: entry.Key, parentPath: slices.Clone(parentPath), annotation: annotation, entry: entry.KeySpan, siblings: slices.Clone(siblings)})
				collectSchemaFieldOccurrences(entry.Value, annotation, append(slices.Clone(parentPath), entry.Key), occurrences)

				continue
			}
			collectSchemaFieldOccurrences(entry.KeyExpr, annotation, parentPath, occurrences)
			collectSchemaFieldOccurrences(entry.Value, annotation, parentPath, occurrences)
		}
	case *syntax.ListExpr:
		for _, item := range expression.Items {
			collectSchemaFieldOccurrences(item, annotation, append(slices.Clone(parentPath), "[]"), occurrences)
		}
	case *syntax.ForExpr:
		collectSchemaFieldOccurrences(expression.Iterable, annotation, parentPath, occurrences)
		collectSchemaFieldOccurrences(expression.Body, annotation, append(slices.Clone(parentPath), "[]"), occurrences)
	case *syntax.BinaryExpr:
		collectSchemaFieldOccurrences(expression.Left, annotation, parentPath, occurrences)
		collectSchemaFieldOccurrences(expression.Right, annotation, parentPath, occurrences)
	case *syntax.UnaryExpr:
		collectSchemaFieldOccurrences(expression.Operand, annotation, parentPath, occurrences)
	case *syntax.IndexExpr:
		collectSchemaFieldOccurrences(expression.Base, annotation, parentPath, occurrences)
		collectSchemaFieldOccurrences(expression.Key, annotation, parentPath, occurrences)
	case *syntax.CallExpr:
		collectSchemaFieldOccurrences(expression.Callee, annotation, parentPath, occurrences)
		for _, argument := range expression.Arguments {
			collectSchemaFieldOccurrences(argument, annotation, parentPath, occurrences)
		}
	case *syntax.LambdaExpr:
		collectSchemaFieldOccurrences(expression.Body, annotation, parentPath, occurrences)
	case *syntax.DeferredReferenceExpr:
		collectSchemaFieldOccurrences(expression.Target, annotation, parentPath, occurrences)
	case *syntax.ConditionalExpr:
		collectSchemaFieldOccurrences(expression.Condition, annotation, parentPath, occurrences)
		collectSchemaFieldOccurrences(expression.Then, annotation, parentPath, occurrences)
		collectSchemaFieldOccurrences(expression.Else, annotation, parentPath, occurrences)
	case *syntax.NodeExpr:
		collectSchemaFieldOccurrences(expression.Body, annotation, parentPath, occurrences)
	case *syntax.OverlayExpr:
		collectSchemaFieldOccurrences(expression.Base, annotation, parentPath, occurrences)
		collectSchemaFieldOccurrences(expression.Patch, annotation, parentPath, occurrences)
	case *syntax.RemovalExpr:
		collectSchemaFieldOccurrences(expression.Base, annotation, parentPath, occurrences)
	}
}

func schemaFieldSourceRange(source string, occurrence *schemaFieldOccurrence) (int, int, bool) {
	span := occurrence.entry
	if span.Start < 0 || span.End > len(source) || span.Start >= span.End {
		return 0, 0, false
	}
	text := source[span.Start:span.End]
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		if text[1:len(text)-1] != occurrence.name {
			return 0, 0, false
		}

		return span.Start + 1, span.End - 1, true
	}
	if text != occurrence.name {
		return 0, 0, false
	}

	return span.Start, span.End, true
}

func schemaFieldsAtOccurrence(ctx context.Context, path, source string, occurrence *schemaFieldOccurrence, index int) ([]string, bool) {
	start, end, ok := schemaFieldSourceRange(source, occurrence)
	if !ok {
		return nil, false
	}
	marker := fmt.Sprintf("__mcl_schema_rename_%d__", index)
	for strings.Contains(source, marker) {
		marker += "_"
	}
	span := occurrence.entry
	probe := source[:span.Start] + marker + source[span.End:]
	fields, err := engine.SchemaFieldCompletionsWithContext(ctx, path, probe, marker)
	if err != nil || start < span.Start || end > span.End {
		return nil, false
	}

	return fields, true
}

func schemaFieldReferenceOccurrences(module syntax.ModuleAST, source string, annotationByBinding map[string]string, pathByBinding map[string][]string) []schemaFieldOccurrence {
	index := analyzeModule(module)
	var occurrences []schemaFieldOccurrence
	languageservice.VisitModuleExpressions(module, func(expression syntax.Expr) {
		var text string
		var span syntax.SourceSpan
		switch expression := expression.(type) {
		case *syntax.ReferenceExpr:
			text, span = expression.Name, expression.ExprSpan()
		case *syntax.OptionalReferenceExpr:
			text, span = expression.Path, expression.ExprSpan()
		default:
			return
		}
		parts, offsets := splitSchemaReference(text)
		if len(parts) < 2 || annotationByBinding[parts[0]] == "" || index.LocalAt(parts[0], span.Start).Found {
			return
		}
		for partIndex := 1; partIndex < len(parts); partIndex++ {
			fieldSpan := syntax.SourceSpan{Start: span.Start + offsets[partIndex], End: span.Start + offsets[partIndex] + len(parts[partIndex])}
			if fieldSpan.Start < 0 || fieldSpan.End > len(source) || source[fieldSpan.Start:fieldSpan.End] != parts[partIndex] {
				continue
			}
			occurrences = append(occurrences, schemaFieldOccurrence{name: parts[partIndex], parentPath: append(slices.Clone(pathByBinding[parts[0]]), parts[1:partIndex]...), annotation: annotationByBinding[parts[0]], entry: fieldSpan})
		}
	})

	return occurrences
}

func splitSchemaReference(reference string) ([]string, []int) {
	var parts []string
	var offsets []int
	for start := 0; start < len(reference); {
		end := start
		for end < len(reference) && reference[end] != '.' && reference[end] != '?' {
			end++
		}
		if end == start || !isMCLIdentifier(reference[start:end]) {
			return nil, nil
		}
		parts = append(parts, reference[start:end])
		offsets = append(offsets, start)
		if end == len(reference) {
			break
		}
		if reference[end] == '?' {
			end++
			if end == len(reference) || reference[end] != '.' {
				return nil, nil
			}
		}
		start = end + 1
	}

	return parts, offsets
}

func schemaDocumentOccurrences(module syntax.ModuleAST, source string) (map[string]string, map[string][]string) {
	annotations := make(map[string]string)
	paths := make(map[string][]string)
	functionReturnSchemas := schemaFunctionReturnSchemas(module, source)
	for _, node := range module.Nodes {
		declaration, ok := node.(*syntax.LetDeclaration)
		if !ok || declaration.Expression == nil {
			continue
		}
		if declaration.Annotation != nil {
			span := declaration.Annotation.Span
			if span.Start >= 0 && span.End <= len(source) && span.Start < span.End {
				annotations[declaration.Name] = source[span.Start:span.End]
			}

			continue
		}
		annotations[declaration.Name], paths[declaration.Name] = schemaBindingFromExpression(declaration.Expression, annotations, paths, functionReturnSchemas)
	}

	return annotations, paths
}

func schemaBindingFromExpression(expression syntax.Expr, annotations map[string]string, paths map[string][]string, functionReturns map[string]string) (string, []string) {
	switch expression := expression.(type) {
	case *syntax.ReferenceExpr:
		parts, _ := splitSchemaReference(expression.Name)
		if len(parts) == 0 {
			return "", nil
		}

		return annotations[parts[0]], append(slices.Clone(paths[parts[0]]), parts[1:]...)
	case *syntax.OptionalReferenceExpr:
		parts, _ := splitSchemaReference(expression.Path)
		if len(parts) == 0 {
			return "", nil
		}

		return annotations[parts[0]], append(slices.Clone(paths[parts[0]]), parts[1:]...)
	case *syntax.CallExpr:
		if callee, ok := expression.Callee.(*syntax.ReferenceExpr); ok {
			return functionReturns[callee.Name], nil
		}
	case *syntax.OverlayExpr:
		return schemaBindingFromExpression(expression.Base, annotations, paths, functionReturns)
	case *syntax.NodeExpr:
		return schemaBindingFromExpression(expression.Body, annotations, paths, functionReturns)
	case *syntax.ConditionalExpr:
		thenAnnotation, thenPath := schemaBindingFromExpression(expression.Then, annotations, paths, functionReturns)
		elseAnnotation, elsePath := schemaBindingFromExpression(expression.Else, annotations, paths, functionReturns)
		if thenAnnotation == elseAnnotation && slices.Equal(thenPath, elsePath) {
			return thenAnnotation, thenPath
		}
	}

	return "", nil
}

func schemaFunctionReturnSchemas(module syntax.ModuleAST, source string) map[string]string {
	schemas := make(map[string]string)
	for _, node := range module.Nodes {
		declaration, ok := node.(*syntax.FunctionDeclaration)
		if !ok || declaration.Function == nil || declaration.Function.ReturnType == nil {
			continue
		}
		span := declaration.Function.ReturnType.Span
		if span.Start >= 0 && span.End <= len(source) && span.Start < span.End {
			schemas[declaration.Name] = source[span.Start:span.End]
		}
	}

	return schemas
}

func schemaAnnotationIdentityWithSources(path string, module syntax.ModuleAST, annotation string, overlays map[string]string, seen map[string]bool) string {
	if annotation == "" {
		return ""
	}
	path, err := filepath.Abs(path)
	if err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
			path = resolved
		}
	}
	path = filepath.Clean(path)
	nameEnd := len(annotation)
	for index, r := range annotation {
		if r == '<' || r == '?' {
			nameEnd = index

			break
		}
	}
	qualifiedName := annotation[:nameEnd]
	if alias, member, ok := strings.Cut(qualifiedName, "."); ok {
		for _, node := range module.Nodes {
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || declaration.Name != alias {
				continue
			}
			if resolved, resolveErr := engine.ResolveImportPath(path, declaration.ModulePath); resolveErr == nil {
				resolved, resolveErr = filepath.Abs(resolved)
				if resolveErr != nil {
					break
				}
				if canonical, canonicalErr := filepath.EvalSymlinks(resolved); canonicalErr == nil {
					resolved = canonical
				}
				resolved = filepath.Clean(resolved)
				key := resolved + "::" + member
				if seen["import:"+key] {
					return key
				}
				seen["import:"+key] = true
				importedSource := overlays[resolved]
				if importedSource == "" {
					if data, readErr := os.ReadFile(resolved); readErr == nil {
						importedSource = string(data)
					}
				}
				if importedModule, parseErr := syntax.ParseAST(importedSource); parseErr == nil {
					return schemaAnnotationIdentityWithSources(resolved, importedModule, member+annotation[nameEnd:], overlays, seen)
				}

				return key + annotation[nameEnd:]
			}
		}
	}
	for _, node := range module.Nodes {
		declaration, ok := node.(*syntax.TypeDeclaration)
		if ok && declaration.Name == qualifiedName {
			if declaration.TypeSpec != nil && declaration.TypeSpec.SchemaPath != "" {
				schemaPath := declaration.TypeSpec.SchemaPath
				if !filepath.IsAbs(schemaPath) {
					schemaPath = filepath.Join(filepath.Dir(path), filepath.FromSlash(schemaPath))
				}
				if absolute, absErr := filepath.Abs(schemaPath); absErr == nil {
					schemaPath = absolute
				}
				if resolved, resolveErr := filepath.EvalSymlinks(schemaPath); resolveErr == nil {
					schemaPath = resolved
				}

				return "schema:" + filepath.Clean(schemaPath) + annotation[nameEnd:]
			}
			if declaration.TypeSpec != nil && declaration.TypeSpec.Name != "" && len(declaration.TypeSpec.Fields) == 0 && len(declaration.TypeSpec.Options) == 0 {
				key := path + "::" + qualifiedName
				if seen["type:"+key] {
					return key + annotation[nameEnd:]
				}
				seen["type:"+key] = true

				return schemaAnnotationIdentityWithSources(path, module, declaration.TypeSpec.Name, overlays, seen) + annotation[nameEnd:]
			}

			return path + "::" + annotation
		}
	}

	return path + "::" + annotation
}

func (s *server) renameOverlays() (map[string]string, map[string]string) {
	overlays := make(map[string]string, len(s.documents))
	openURIs := make(map[string]string, len(s.documents))
	for _, uri := range slices.Sorted(maps.Keys(s.documents)) {
		document := s.documents[uri]
		path := documentPath(uri)
		if path == "" {
			continue
		}
		path, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
			path = resolved
		}
		path = filepath.Clean(path)
		overlays[path] = document.Text
		openURIs[path] = uri
	}

	return overlays, openURIs
}

func (s *server) rename(params renameParams) *workspaceEdit {
	return s.renameWithContext(context.Background(), params)
}

func (s *server) renameWithContext(ctx context.Context, params renameParams) *workspaceEdit {
	if ctx.Err() != nil {
		return nil
	}
	state, exists := s.documents[params.TextDocument.URI]
	if !exists || !isMCLIdentifier(params.NewName) {
		return nil
	}
	lines := strings.Split(state.Text, "\n")
	if params.Position.Line < 0 || params.Position.Line >= len(lines) {
		return nil
	}
	name, ok := wordAtUTF16(lines[params.Position.Line], params.Position.Character)
	if !ok || name == params.NewName {
		return nil
	}
	offset, err := offsetAtUTF16(state.Text, params.Position)
	if err != nil {
		return nil
	}
	index, err := analyzeSource(state.Text)
	if err != nil {
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	module := index.Module
	if edit, handled := s.renameImportedExport(ctx, params, name, offset, module, index); handled {
		return edit
	}
	if match := index.LocalAt(name, offset); match.Found {
		result := languageservice.RenameForSource(
			params.TextDocument.URI,
			state.Text,
			languageservice.Position{Line: params.Position.Line, Character: params.Position.Character},
			params.NewName,
		)
		if result == nil || len(result.Edits) == 0 {
			return nil
		}
		edits := make([]textEdit, len(result.Edits))
		for index, edit := range result.Edits {
			if ctx.Err() != nil {
				return nil
			}
			if edit.Path != params.TextDocument.URI {
				return nil
			}
			edits[index].Range.Start = location{Line: edit.Range.Start.Line, Character: edit.Range.Start.Character}
			edits[index].Range.End = location{Line: edit.Range.End.Line, Character: edit.Range.End.Character}
			edits[index].NewText = edit.NewText
		}

		return &workspaceEdit{Changes: map[string][]textEdit{params.TextDocument.URI: edits}}
	}
	if edit, handled := s.renameSchemaField(ctx, params, name, offset, module, state.Text); handled {
		return edit
	}
	references := index.References
	target := index.DeclarationAt(name, offset)
	if target < 0 {
		for _, reference := range references {
			if ctx.Err() != nil {
				return nil
			}
			if reference.Name != name || offset < reference.Span.Start || offset > reference.Span.Start+len(name) {
				continue
			}
			resolved := index.ResolveTopLevel(name, reference.Span.Start, reference.TypeName)
			if resolved < 0 || target >= 0 && target != resolved {
				return nil
			}
			target = resolved
		}
	}
	if target < 0 || astDeclarationExported(module.Nodes[target]) || !isRenameableASTDeclaration(module.Nodes[target]) {
		return nil
	}
	for index, node := range module.Nodes {
		if ctx.Err() != nil {
			return nil
		}
		declarationName, _, _, ok := astDeclarationInfo(node)
		if !ok {
			continue
		}
		if index != target && declarationName == name {
			return nil
		}
		if index != target && declarationName == params.NewName {
			return nil
		}
	}
	for _, reference := range references {
		if ctx.Err() != nil {
			return nil
		}
		if reference.TypeName == isTypeDeclaration(module.Nodes[target]) && reference.Name == params.NewName {
			return nil
		}
	}

	_, _, declarationSpan, _ := astDeclarationInfo(module.Nodes[target])
	spans := []syntax.SourceSpan{declarationSpan}
	seen := map[syntax.SourceSpan]struct{}{declarationSpan: {}}
	for _, reference := range index.TopLevelReferences(name, target, isTypeDeclaration(module.Nodes[target])) {
		if ctx.Err() != nil {
			return nil
		}
		if index.LocalAt(params.NewName, reference.Span.Start).Found {
			return nil
		}
		span := syntax.SourceSpan{Start: reference.Span.Start, End: reference.Span.Start + len(name)}
		if span.Start < 0 || span.End > len(state.Text) || state.Text[span.Start:span.End] != name {
			return nil
		}
		if _, exists := seen[span]; exists {
			continue
		}
		seen[span] = struct{}{}
		spans = append(spans, span)
	}
	editSpans := slices.Clone(spans)
	slices.SortFunc(editSpans, func(left, right syntax.SourceSpan) int {
		return right.Start - left.Start
	})
	candidate := state.Text
	for _, span := range editSpans {
		if ctx.Err() != nil {
			return nil
		}
		candidate = candidate[:span.Start] + params.NewName + candidate[span.End:]
	}
	if _, err := syntax.ParseAST(candidate); err != nil {
		return nil
	}
	stateIndex := sourceindex.New(state.Text)
	edits := make([]textEdit, len(spans))
	for index, span := range spans {
		if ctx.Err() != nil {
			return nil
		}
		edits[index].Range.Start = sourcePositionAt(stateIndex, span.Start)
		edits[index].Range.End = sourcePositionAt(stateIndex, span.End)
		edits[index].NewText = params.NewName
	}
	slices.SortFunc(edits, func(left, right textEdit) int {
		if left.Range.Start.Line != right.Range.Start.Line {
			return left.Range.Start.Line - right.Range.Start.Line
		}

		return left.Range.Start.Character - right.Range.Start.Character
	})

	return &workspaceEdit{Changes: map[string][]textEdit{params.TextDocument.URI: edits}}
}

func (s *server) renameImportedExport(ctx context.Context, params renameParams, name string, offset int, current syntax.ModuleAST, index *moduleSemanticIndex) (*workspaceEdit, bool) {
	if ctx.Err() != nil {
		return nil, true
	}
	currentPath := documentPath(params.TextDocument.URI)
	if currentPath == "" {
		return nil, false
	}
	targetPath := ""
	targetName := name
	for _, node := range current.Nodes {
		if ctx.Err() != nil {
			return nil, true
		}
		declarationName, _, nameSpan, ok := astDeclarationInfo(node)
		if ok && astDeclarationExported(node) && isRenameableASTDeclaration(node) && declarationName == name && nameSpan.Start <= offset && offset <= nameSpan.End {
			targetPath = currentPath

			break
		}
	}
	if targetPath == "" {
		for _, node := range current.Nodes {
			if ctx.Err() != nil {
				return nil, true
			}
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || !isMCLIdentifier(declaration.Name) {
				continue
			}
			resolved, err := engine.ResolveImportPath(currentPath, declaration.ModulePath)
			if err != nil {
				continue
			}
			for _, reference := range index.Qualified {
				if ctx.Err() != nil {
					return nil, true
				}
				if reference.Span.Start > offset || offset > reference.Span.End {
					continue
				}
				if reference.Alias == declaration.Name && reference.Member == name && !index.LocalAt(reference.Alias, reference.Span.Start).Found {
					targetPath = resolved
					targetName = reference.Member

					break
				}
			}
			if targetPath == "" {
				for _, reference := range index.QualifiedType {
					if ctx.Err() != nil {
						return nil, true
					}
					if reference.Alias != declaration.Name || reference.Member != name {
						continue
					}
					if reference.Span.Start <= offset && offset <= reference.Span.End && !index.LocalAt(reference.Alias, reference.Span.Start).Found {
						targetPath = resolved
						targetName = reference.Member

						break
					}
				}
			}
			if targetPath != "" {
				break
			}
		}
	}
	if targetPath == "" {
		return nil, false
	}
	overlays := make(map[string]string, len(s.documents))
	openURIs := make(map[string]string, len(s.documents))
	openDocumentURIs := slices.Sorted(maps.Keys(s.documents))
	for _, uri := range openDocumentURIs {
		if ctx.Err() != nil {
			return nil, true
		}
		document := s.documents[uri]
		path := documentPath(uri)
		if path == "" {
			continue
		}
		path, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
			path = resolved
		}
		path = filepath.Clean(path)
		overlays[path] = document.Text
		openURIs[path] = uri
	}
	if s.renameIndex == nil {
		s.renameIndex = &engine.DependentMCLSourceIndex{}
	}
	var sources map[string]string
	var err error
	if s.watchRegistered {
		sources, err = s.renameIndex.SourcesFromIndexWithContext(ctx, targetPath, s.workspaceRoots, overlays)
	} else {
		sources, err = s.renameIndex.SourcesWithContext(ctx, targetPath, s.workspaceRoots, overlays)
	}
	if err != nil {
		return nil, true
	}
	parsedDocuments := make(map[string]syntax.ModuleAST, len(sources))
	semanticDocuments := make(map[string]*moduleSemanticIndex, len(sources))
	paths := make(map[string]string, len(sources))
	texts := make(map[string]string, len(sources))
	for path, source := range sources {
		if ctx.Err() != nil {
			return nil, true
		}
		uri := openURIs[path]
		if uri == "" {
			uri = filePathToURI(path)
		}
		parsed, err := syntax.ParseAST(source)
		if err != nil {
			continue
		}
		parsedDocuments[uri] = parsed
		semanticDocuments[uri] = analyzeModule(parsed)
		paths[uri] = path
		texts[uri] = source
	}
	declarationURI := ""
	var declaration syntax.ASTNode
	for uri, parsed := range parsedDocuments {
		if ctx.Err() != nil {
			return nil, true
		}
		if paths[uri] != targetPath {
			continue
		}
		for _, node := range parsed.Nodes {
			if ctx.Err() != nil {
				return nil, true
			}
			declarationName, _, _, ok := astDeclarationInfo(node)
			if ok && astDeclarationExported(node) && declarationName == targetName && isRenameableASTDeclaration(node) {
				if declarationURI != "" {
					return nil, true
				}
				declarationURI, declaration = uri, node
			}
		}
	}
	if declarationURI == "" {
		return nil, false
	}
	_, _, declarationSpan, _ := astDeclarationInfo(declaration)
	declarationIndex := -1
	for index, node := range parsedDocuments[declarationURI].Nodes {
		if ctx.Err() != nil {
			return nil, true
		}
		declarationName, _, nameSpan, ok := astDeclarationInfo(node)
		if ok && nameSpan == declarationSpan && declarationName == targetName {
			declarationIndex = index

			break
		}
	}
	if declarationIndex < 0 {
		return nil, true
	}
	for _, node := range parsedDocuments[declarationURI].Nodes {
		if ctx.Err() != nil {
			return nil, true
		}
		declarationName, _, nameSpan, ok := astDeclarationInfo(node)
		if !ok || declarationName != params.NewName || nameSpan == declarationSpan {
			continue
		}
		if isRenameConflictASTDeclaration(node) || isTypeDeclaration(declaration) && isTypeDeclaration(node) {
			return nil, true
		}
	}
	changes := make(map[string][]textEdit)
	indices := make(map[string]*sourceindex.Index)
	addEdit := func(uri, source string, span syntax.SourceSpan) bool {
		if span.Start < 0 || span.End > len(source) || span.Start >= span.End || source[span.Start:span.End] != targetName {
			return false
		}
		index := indices[uri]
		if index == nil {
			index = sourceindex.New(source)
			indices[uri] = index
		}
		var edit textEdit
		edit.Range.Start = sourcePositionAt(index, span.Start)
		edit.Range.End = sourcePositionAt(index, span.End)
		edit.NewText = params.NewName
		changes[uri] = append(changes[uri], edit)

		return true
	}
	if !addEdit(declarationURI, texts[declarationURI], declarationSpan) {
		return nil, true
	}
	for uri, parsed := range parsedDocuments {
		if ctx.Err() != nil {
			return nil, true
		}
		if paths[uri] == targetPath {
			index := semanticDocuments[uri]
			for _, reference := range index.TopLevelReferences(targetName, declarationIndex, isTypeDeclaration(declaration)) {
				if ctx.Err() != nil {
					return nil, true
				}
				span := syntax.SourceSpan{Start: reference.Span.Start, End: reference.Span.Start + len(targetName)}
				if !addEdit(uri, texts[uri], span) {
					return nil, true
				}
			}

			continue
		}
		aliases := make(map[string]bool)
		for _, node := range parsed.Nodes {
			if ctx.Err() != nil {
				return nil, true
			}
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok {
				continue
			}
			resolved, err := engine.ResolveImportPath(paths[uri], declaration.ModulePath)
			if err == nil && resolved == targetPath {
				aliases[declaration.Name] = true
			}
		}
		if len(aliases) == 0 {
			continue
		}
		index := semanticDocuments[uri]
		for _, reference := range index.Qualified {
			if !aliases[reference.Alias] || reference.Member != targetName || index.LocalAt(reference.Alias, reference.Span.Start).Found {
				continue
			}
			if !addEdit(uri, texts[uri], reference.Span) {
				return nil, true
			}
		}
		if isTypeDeclaration(declaration) {
			for _, reference := range index.QualifiedType {
				if !aliases[reference.Alias] || reference.Member != targetName || index.LocalAt(reference.Alias, reference.Span.Start).Found {
					continue
				}
				if !addEdit(uri, texts[uri], reference.Span) {
					return nil, true
				}
			}
		}
	}
	for uri := range changes {
		slices.SortFunc(changes[uri], func(left, right textEdit) int {
			if left.Range.Start.Line != right.Range.Start.Line {
				return left.Range.Start.Line - right.Range.Start.Line
			}

			return left.Range.Start.Character - right.Range.Start.Character
		})
	}

	return &workspaceEdit{Changes: changes}, true
}

func isRenameConflictASTDeclaration(node syntax.ASTNode) bool {
	switch node.(type) {
	case *syntax.LetDeclaration, *syntax.FunctionDeclaration, *syntax.RuleDeclaration, *syntax.ImportDeclaration:
		return true
	default:
		return false
	}
}

func sourceDefinitionLocation(uri, source string, span syntax.SourceSpan) *definitionLocation {
	result := &definitionLocation{URI: uri}
	index := sourceindex.New(source)
	result.Range.Start = sourcePositionAt(index, span.Start)
	result.Range.End = sourcePositionAt(index, span.End)

	return result
}

func wordAtUTF16(line string, character int) (string, bool) {
	runes := []rune(line)
	index := runeIndexAtUTF16(runes, character)
	if index == len(runes) || !isIdentifierRune(runes[index]) {
		if index == 0 || !isIdentifierRune(runes[index-1]) {
			return "", false
		}
		index--
	}
	start, end := index, index+1
	for start > 0 && isIdentifierRune(runes[start-1]) {
		start--
	}
	for end < len(runes) && isIdentifierRune(runes[end]) {
		end++
	}

	return string(runes[start:end]), true
}

package lsp

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/manifest-configuration-language/mcl/internal/engine"
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
			module := index.module
			if result, handled := s.importedDefinition(params.TextDocument.URI, module, offset, index); handled {
				return result
			}
			for _, binding := range index.visibleLocalBindings(offset) {
				if binding.name == name {
					return sourceDefinitionLocation(params.TextDocument.URI, state.Text, binding.declaration)
				}
			}
			if declarationIndex := index.declarationAt(name, offset); declarationIndex >= 0 {
				_, _, nameSpan, _ := astDeclarationInfo(module.Nodes[declarationIndex])

				return sourceDefinitionLocation(params.TextDocument.URI, state.Text, nameSpan)
			}
			declarationIndex := index.resolveTopLevel(name, offset, false)
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
	references := append(slices.Clone(index.qualified), index.qualifiedTyp...)
	for _, reference := range references {
		if offset < reference.span.Start || offset > reference.span.End {
			continue
		}
		var targetPath string
		foundImport := false
		for _, node := range current.Nodes {
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || declaration.Name != reference.alias {
				continue
			}
			foundImport = true
			if index.localAt(reference.alias, reference.span.Start).found || strings.HasPrefix(declaration.ModulePath, "std:") {
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
			if !ok || name != reference.member || !astDeclarationExported(node) {
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

func qualifiedTypeReferences(module syntax.ModuleAST) []qualifiedReference {
	var result []qualifiedReference
	var visit func(syntax.TypeExpr)
	visit = func(typeExpr syntax.TypeExpr) {
		if alias, member, ok := strings.CutLast(typeExpr.Name, "."); ok && alias != "" && member != "" && typeExpr.Span.End-typeExpr.Span.Start >= len(typeExpr.Name) {
			memberOffset := typeExpr.Span.Start + len(alias) + 1
			result = append(result, qualifiedReference{
				alias:  alias,
				member: member,
				span:   syntax.SourceSpan{Start: memberOffset, End: memberOffset + len(member)},
			})
		}
		for _, child := range typeExpr.Arguments {
			visit(child)
		}
		for _, child := range typeExpr.Options {
			visit(child)
		}
		for _, field := range typeExpr.Fields {
			visit(field.Type)
		}
		for _, parameter := range typeExpr.Parameters {
			visit(parameter.Type)
		}
		if typeExpr.Element != nil {
			visit(*typeExpr.Element)
		}
		if typeExpr.ReturnType != nil {
			visit(*typeExpr.ReturnType)
		}
	}
	visitPointer := func(typeExpr *syntax.TypeExpr) {
		if typeExpr != nil {
			visit(*typeExpr)
		}
	}
	for _, node := range module.Nodes {
		switch declaration := node.(type) {
		case *syntax.TypeDeclaration:
			if declaration.TypeSpec != nil {
				visit(*declaration.TypeSpec)
			}
		case *syntax.LetDeclaration:
			visitPointer(declaration.Annotation)
		case *syntax.FunctionDeclaration:
			if declaration.Function == nil {
				continue
			}
			for _, parameter := range declaration.Function.Parameters {
				visit(parameter.Type)
			}
			visitPointer(declaration.Function.ReturnType)
		case *syntax.RuleDeclaration:
			if declaration.Rule == nil {
				continue
			}
			for _, parameter := range declaration.Rule.Parameters {
				visit(parameter.Type)
			}
		case *syntax.ValidateStatement:
			visitPointer(declaration.QueryType)
		}
	}

	return result
}

type sourceReference struct {
	name     string
	span     syntax.SourceSpan
	typeName bool
}

type schemaFieldOccurrence struct {
	name       string
	parentPath []string
	annotation string
	identity   string
	entry      syntax.SourceSpan
	siblings   []string
}

func (s *server) renameSchemaField(params renameParams, name string, offset int, module syntax.ModuleAST, source string) (*workspaceEdit, bool) {
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
		occurrence := &occurrences[index]
		fieldStart, fieldEnd, ok := schemaFieldSourceRange(source, occurrence)
		if !ok || occurrence.name != name || offset < fieldStart || offset > fieldEnd {
			continue
		}
		fields, ok := schemaFieldsAtOccurrence(path, source, occurrence, index)
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
			if reference.entry.Start > offset || offset > reference.entry.End || reference.name != name {
				continue
			}
			fields, fieldErr := engine.SchemaTypeFields(path, source, reference.annotation, reference.parentPath)
			if fieldErr != nil || !slices.Contains(fields, reference.name) {
				continue
			}
			if slices.Contains(fields, params.NewName) {
				return nil, true
			}
			for index := range occurrences {
				occurrence := &occurrences[index]
				if occurrence.name != reference.name || occurrence.annotation != reference.annotation || !slices.Equal(occurrence.parentPath, reference.parentPath) {
					continue
				}
				fields, ok := schemaFieldsAtOccurrence(path, source, occurrence, index)
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
		sources, err = s.renameIndex.SourcesFromIndex(path, s.workspaceRoots, overlays)
	} else {
		sources, err = s.renameIndex.Sources(path, s.workspaceRoots, overlays)
	}
	if err != nil {
		return nil, true
	}
	for candidatePath, candidateSource := range sources {
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
			fields, valid := schemaFieldsAtOccurrence(documentPath, documentSource, occurrence, index)
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
			fields, fieldErr := engine.SchemaTypeFields(documentPath, documentSource, reference.annotation, reference.parentPath)
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

func schemaFieldsAtOccurrence(path, source string, occurrence *schemaFieldOccurrence, index int) ([]string, bool) {
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
	fields, err := engine.SchemaFieldCompletionsWithError(path, probe, marker)
	if err != nil || start < span.Start || end > span.End {
		return nil, false
	}

	return fields, true
}

func schemaFieldReferenceOccurrences(module syntax.ModuleAST, source string, annotationByBinding map[string]string, pathByBinding map[string][]string) []schemaFieldOccurrence {
	index := analyzeModule(module)
	var occurrences []schemaFieldOccurrence
	visitModuleExpressions(module, func(expression syntax.Expr) {
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
		if len(parts) < 2 || annotationByBinding[parts[0]] == "" || index.localAt(parts[0], span.Start).found {
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
	module := index.module
	if edit, handled := s.renameImportedExport(params, name, offset, module, index); handled {
		return edit
	}
	if match := index.localAt(name, offset); match.found {
		return renameLocalBinding(state.Text, params.TextDocument.URI, params.NewName, name, match.binding, index.locals, index.references)
	}
	if edit, handled := s.renameSchemaField(params, name, offset, module, state.Text); handled {
		return edit
	}
	references := index.references
	target := index.declarationAt(name, offset)
	if target < 0 {
		for _, reference := range references {
			if reference.name != name || offset < reference.span.Start || offset > reference.span.Start+len(name) {
				continue
			}
			resolved := index.resolveTopLevel(name, reference.span.Start, reference.typeName)
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
		if reference.typeName == isTypeDeclaration(module.Nodes[target]) && reference.name == params.NewName {
			return nil
		}
	}

	_, _, declarationSpan, _ := astDeclarationInfo(module.Nodes[target])
	spans := []syntax.SourceSpan{declarationSpan}
	seen := map[syntax.SourceSpan]struct{}{declarationSpan: {}}
	for _, reference := range references {
		if reference.name != name || reference.typeName != isTypeDeclaration(module.Nodes[target]) || !reference.typeName && index.localAt(name, reference.span.Start).found {
			continue
		}
		resolved := index.resolveTopLevel(name, reference.span.Start, reference.typeName)
		if resolved != target {
			continue
		}
		if index.localAt(params.NewName, reference.span.Start).found {
			return nil
		}
		span := syntax.SourceSpan{Start: reference.span.Start, End: reference.span.Start + len(name)}
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
		candidate = candidate[:span.Start] + params.NewName + candidate[span.End:]
	}
	if _, err := syntax.ParseAST(candidate); err != nil {
		return nil
	}
	stateIndex := sourceindex.New(state.Text)
	edits := make([]textEdit, len(spans))
	for index, span := range spans {
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

func (s *server) renameImportedExport(params renameParams, name string, offset int, current syntax.ModuleAST, index *moduleSemanticIndex) (*workspaceEdit, bool) {
	currentPath := documentPath(params.TextDocument.URI)
	if currentPath == "" {
		return nil, false
	}
	targetPath := ""
	targetName := name
	for _, node := range current.Nodes {
		declarationName, _, nameSpan, ok := astDeclarationInfo(node)
		if ok && astDeclarationExported(node) && isRenameableASTDeclaration(node) && declarationName == name && nameSpan.Start <= offset && offset <= nameSpan.End {
			targetPath = currentPath

			break
		}
	}
	if targetPath == "" {
		for _, node := range current.Nodes {
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || !isMCLIdentifier(declaration.Name) {
				continue
			}
			resolved, err := engine.ResolveImportPath(currentPath, declaration.ModulePath)
			if err != nil {
				continue
			}
			for _, reference := range index.qualified {
				if reference.span.Start > offset || offset > reference.span.End {
					continue
				}
				if reference.alias == declaration.Name && reference.member == name && !index.localAt(reference.alias, reference.span.Start).found {
					targetPath = resolved
					targetName = reference.member

					break
				}
			}
			if targetPath == "" {
				for _, reference := range index.qualifiedTyp {
					if reference.alias != declaration.Name || reference.member != name {
						continue
					}
					if reference.span.Start <= offset && offset <= reference.span.End && !index.localAt(reference.alias, reference.span.Start).found {
						targetPath = resolved
						targetName = reference.member

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
		sources, err = s.renameIndex.SourcesFromIndex(targetPath, s.workspaceRoots, overlays)
	} else {
		sources, err = s.renameIndex.Sources(targetPath, s.workspaceRoots, overlays)
	}
	if err != nil {
		return nil, true
	}
	parsedDocuments := make(map[string]syntax.ModuleAST, len(sources))
	semanticDocuments := make(map[string]*moduleSemanticIndex, len(sources))
	paths := make(map[string]string, len(sources))
	texts := make(map[string]string, len(sources))
	for path, source := range sources {
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
		if paths[uri] != targetPath {
			continue
		}
		for _, node := range parsed.Nodes {
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
		if paths[uri] == targetPath {
			index := semanticDocuments[uri]
			for _, reference := range index.references {
				if reference.name != targetName || reference.typeName != isTypeDeclaration(declaration) || !reference.typeName && index.localAt(targetName, reference.span.Start).found {
					continue
				}
				resolved := index.resolveTopLevel(targetName, reference.span.Start, reference.typeName)
				if resolved != declarationIndex {
					continue
				}
				span := syntax.SourceSpan{Start: reference.span.Start, End: reference.span.Start + len(targetName)}
				if !addEdit(uri, texts[uri], span) {
					return nil, true
				}
			}

			continue
		}
		aliases := make(map[string]bool)
		for _, node := range parsed.Nodes {
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
		for _, reference := range index.qualified {
			if !aliases[reference.alias] || reference.member != targetName || index.localAt(reference.alias, reference.span.Start).found {
				continue
			}
			if !addEdit(uri, texts[uri], reference.span) {
				return nil, true
			}
		}
		if isTypeDeclaration(declaration) {
			for _, reference := range index.qualifiedTyp {
				if !aliases[reference.alias] || reference.member != targetName || index.localAt(reference.alias, reference.span.Start).found {
					continue
				}
				if !addEdit(uri, texts[uri], reference.span) {
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

type qualifiedReference struct {
	alias  string
	member string
	span   syntax.SourceSpan
}

func qualifiedSourceReferences(module syntax.ModuleAST) []qualifiedReference {
	var result []qualifiedReference
	visitModuleExpressions(module, func(expression syntax.Expr) {
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
		parts := strings.FieldsFunc(text, func(r rune) bool { return r == '.' || r == '?' })
		if len(parts) < 2 || span.End-span.Start < len(text) {
			return
		}
		separator := strings.Index(text, "?.")
		separatorSize := 2
		if separator < 0 {
			separator = strings.IndexByte(text, '.')
			separatorSize = 1
		}
		memberOffset := separator + separatorSize
		if separator >= 0 && strings.HasPrefix(text[memberOffset:], parts[1]) {
			result = append(result, qualifiedReference{alias: parts[0], member: parts[1], span: syntax.SourceSpan{Start: span.Start + memberOffset, End: span.Start + memberOffset + len(parts[1])}})
		}
	})

	return result
}

func sourceReferences(module syntax.ModuleAST) []sourceReference {
	var references []sourceReference
	visitModuleExpressions(module, func(expression syntax.Expr) {
		collectExprSourceReferences(expression, &references)
	})
	var collectType func(syntax.TypeExpr)
	collectType = func(typeExpr syntax.TypeExpr) {
		if isQualifiedMCLIdentifier(typeExpr.Name) && typeExpr.Span.End-typeExpr.Span.Start >= len(typeExpr.Name) {
			references = append(references, sourceReference{name: typeExpr.Name, span: syntax.SourceSpan{Start: typeExpr.Span.Start, End: typeExpr.Span.Start + len(typeExpr.Name)}, typeName: true})
		}
		for _, argument := range typeExpr.Arguments {
			collectType(argument)
		}
		for _, option := range typeExpr.Options {
			collectType(option)
		}
		for _, field := range typeExpr.Fields {
			collectType(field.Type)
		}
		if typeExpr.Element != nil {
			collectType(*typeExpr.Element)
		}
		for _, parameter := range typeExpr.Parameters {
			collectType(parameter.Type)
		}
		if typeExpr.ReturnType != nil {
			collectType(*typeExpr.ReturnType)
		}
	}
	for _, node := range module.Nodes {
		switch declaration := node.(type) {
		case *syntax.TypeDeclaration:
			if declaration.TypeSpec != nil {
				collectType(*declaration.TypeSpec)
			}
		case *syntax.LetDeclaration:
			if declaration.Annotation != nil {
				collectType(*declaration.Annotation)
			}
		case *syntax.FunctionDeclaration:
			if declaration.Function != nil {
				for _, parameter := range declaration.Function.Parameters {
					collectType(parameter.Type)
				}
				if declaration.Function.ReturnType != nil {
					collectType(*declaration.Function.ReturnType)
				}
			}
		case *syntax.RuleDeclaration:
			if declaration.Rule != nil {
				for _, parameter := range declaration.Rule.Parameters {
					collectType(parameter.Type)
				}
			}
		case *syntax.ValidateStatement:
			if declaration.QueryType != nil {
				collectType(*declaration.QueryType)
			}
			if declaration.QuerySchema != nil {
				collectType(*declaration.QuerySchema)
			}
		}
	}

	return references
}

func visitModuleExpressions(module syntax.ModuleAST, visit func(syntax.Expr)) {
	visitRoot := func(expression syntax.Expr) {
		if expression != nil {
			syntax.WalkExpr(expression, visit)
		}
	}
	for _, node := range module.Nodes {
		switch node := node.(type) {
		case *syntax.LetDeclaration:
			visitRoot(node.Expression)
		case *syntax.FunctionDeclaration:
			if node.Function != nil {
				visitRoot(node.Function.Expression)
			}
		case *syntax.RuleDeclaration:
			if node.Rule != nil {
				for _, requirement := range node.Rule.Requirements {
					visitRoot(requirement.Expression)
				}
			}
		case *syntax.EmitStatement:
			visitRoot(node.Expression)
		case *syntax.ValidateStatement:
			visitRoot(node.Expression)
			visitRoot(node.RuleExpr)
			visitRoot(node.QueryWhere)
		}
	}
}

func collectExprSourceReferences(expression syntax.Expr, references *[]sourceReference) {
	switch expression := expression.(type) {
	case *syntax.ReferenceExpr:
		name := expression.Name
		if index := strings.Index(name, "?."); index >= 0 {
			name = name[:index]
		} else if index := strings.IndexByte(name, '.'); index >= 0 {
			name = name[:index]
		}
		span := expression.ExprSpan()
		if isMCLIdentifier(name) && span.End-span.Start >= len(name) {
			*references = append(*references, sourceReference{name: name, span: span})
		}
	case *syntax.OptionalReferenceExpr:
		name := expression.Path
		if index := strings.Index(name, "?."); index >= 0 {
			name = name[:index]
		} else if index := strings.IndexByte(name, '.'); index >= 0 {
			name = name[:index]
		}
		span := expression.ExprSpan()
		if isMCLIdentifier(name) && span.End-span.Start >= len(name) {
			*references = append(*references, sourceReference{name: name, span: span})
		}
	}
}

func topLevelTypeDefinition(module syntax.ModuleAST, name string) int {
	definition := -1
	for index, node := range module.Nodes {
		declaration, ok := node.(*syntax.TypeDeclaration)
		if !ok || declaration.Name != name {
			continue
		}
		if definition >= 0 {
			return -1
		}
		definition = index
	}

	return definition
}

func isTypeDeclaration(node syntax.ASTNode) bool {
	_, ok := node.(*syntax.TypeDeclaration)

	return ok
}

func isRenameableASTDeclaration(node syntax.ASTNode) bool {
	switch node.(type) {
	case *syntax.LetDeclaration, *syntax.FunctionDeclaration, *syntax.RuleDeclaration, *syntax.TypeDeclaration:
		return true
	default:
		return false
	}
}

func astDeclarationExported(node syntax.ASTNode) bool {
	switch declaration := node.(type) {
	case *syntax.LetDeclaration:
		return declaration.Exported
	case *syntax.FunctionDeclaration:
		return declaration.Exported
	case *syntax.RuleDeclaration:
		return declaration.Exported
	case *syntax.TypeDeclaration:
		return declaration.Exported
	default:
		return false
	}
}

func isRenameConflictASTDeclaration(node syntax.ASTNode) bool {
	switch node.(type) {
	case *syntax.LetDeclaration, *syntax.FunctionDeclaration, *syntax.RuleDeclaration, *syntax.ImportDeclaration:
		return true
	default:
		return false
	}
}

type scopedLocalBinding struct {
	name        string
	declaration syntax.SourceSpan
	scope       syntax.SourceSpan
}

type localBindingMatch struct {
	binding scopedLocalBinding
	found   bool
}

func scopedLocalBindings(module syntax.ModuleAST) []scopedLocalBinding {
	var bindings []scopedLocalBinding
	add := func(name string, declaration, scope syntax.SourceSpan) {
		if name == "" || declaration.End <= declaration.Start || scope.End <= scope.Start {
			return
		}
		bindings = append(bindings, scopedLocalBinding{name: name, declaration: declaration, scope: scope})
	}
	for _, node := range module.Nodes {
		switch node := node.(type) {
		case *syntax.FunctionDeclaration:
			if node.Function != nil {
				for _, parameter := range node.Function.Parameters {
					add(parameter.Name, parameter.Span, node.Function.BodySpan())
				}
				collectLocalExprScopes(node.Function.Expression, add)
			}
		case *syntax.LetDeclaration:
			if node.Expression != nil {
				collectLocalExprScopes(node.Expression, add)
			}
		case *syntax.EmitStatement:
			if node.Expression != nil {
				collectLocalExprScopes(node.Expression, add)
			}
		case *syntax.ValidateStatement:
			if node.Expression != nil {
				collectLocalExprScopes(node.Expression, add)
			}
			collectLocalExprScopes(node.RuleExpr, add)
			if node.QueryWhere != nil {
				collectLocalExprScopes(node.QueryWhere, add)
			}
		case *syntax.RuleDeclaration:
			if node.Rule == nil {
				continue
			}
			for _, requirement := range node.Rule.Requirements {
				for _, parameter := range node.Rule.Parameters {
					add(parameter.Name, parameter.Span, requirement.Span)
				}
				collectLocalExprScopes(requirement.Expression, add)
			}
		}
	}

	return bindings
}

func localBindingAt(bindings []scopedLocalBinding, name string, offset int) localBindingMatch {
	var match localBindingMatch
	for _, binding := range bindings {
		inDeclaration := offset >= binding.declaration.Start && offset <= binding.declaration.End
		inScope := offset >= binding.scope.Start && offset <= binding.scope.End
		if binding.name != name || !inDeclaration && !inScope {
			continue
		}
		if !match.found || binding.scope.End-binding.scope.Start < match.binding.scope.End-match.binding.scope.Start {
			match = localBindingMatch{binding: binding, found: true}
		}
	}

	return match
}

func collectLocalExprScopes(expression syntax.Expr, add func(string, syntax.SourceSpan, syntax.SourceSpan)) {
	syntax.WalkExpr(expression, func(expression syntax.Expr) {
		switch expression := expression.(type) {
		case *syntax.ForExpr:
			add(expression.Variable, expression.VariableSpan, expression.Body.ExprSpan())
		case *syntax.LambdaExpr:
			for index, parameter := range expression.Parameters {
				if index < len(expression.ParameterSpans) {
					add(parameter, expression.ParameterSpans[index], expression.Body.ExprSpan())
				}
			}
		}
	})
}

func renameLocalBinding(source, uri, newName, oldName string, target scopedLocalBinding, bindings []scopedLocalBinding, references []sourceReference) *workspaceEdit {
	for _, binding := range bindings {
		if binding.name != newName || binding.declaration == target.declaration {
			continue
		}
		if binding.scope.Start <= target.scope.End && target.scope.Start <= binding.scope.End {
			return nil
		}
	}
	spans := []syntax.SourceSpan{target.declaration}
	for _, reference := range references {
		if reference.name != oldName {
			continue
		}
		resolved := localBindingAt(bindings, oldName, reference.span.Start)
		if !resolved.found || resolved.binding.declaration != target.declaration {
			continue
		}
		if localBindingAt(bindings, newName, reference.span.Start).found {
			return nil
		}
		spans = append(spans, syntax.SourceSpan{Start: reference.span.Start, End: reference.span.Start + len(oldName)})
	}
	seen := make(map[syntax.SourceSpan]struct{}, len(spans))
	unique := spans[:0]
	for _, span := range spans {
		if span.Start < 0 || span.End > len(source) || source[span.Start:span.End] != oldName {
			return nil
		}
		if _, exists := seen[span]; exists {
			continue
		}
		seen[span] = struct{}{}
		unique = append(unique, span)
	}
	spans = unique
	editSpans := slices.Clone(spans)
	slices.SortFunc(editSpans, func(left, right syntax.SourceSpan) int {
		return right.Start - left.Start
	})
	candidate := source
	for _, span := range editSpans {
		candidate = candidate[:span.Start] + newName + candidate[span.End:]
	}
	if _, err := syntax.ParseAST(candidate); err != nil {
		return nil
	}
	sourceIndex := sourceindex.New(source)
	edits := make([]textEdit, len(spans))
	for index, span := range spans {
		edits[index].Range.Start = sourcePositionAt(sourceIndex, span.Start)
		edits[index].Range.End = sourcePositionAt(sourceIndex, span.End)
		edits[index].NewText = newName
	}
	slices.SortFunc(edits, func(left, right textEdit) int {
		if left.Range.Start.Line != right.Range.Start.Line {
			return left.Range.Start.Line - right.Range.Start.Line
		}

		return left.Range.Start.Character - right.Range.Start.Character
	})

	return &workspaceEdit{Changes: map[string][]textEdit{uri: edits}}
}

type localBinding struct {
	name        string
	declaration syntax.SourceSpan
}

func astDeclarationInfo(node syntax.ASTNode) (string, int, syntax.SourceSpan, bool) {
	switch declaration := node.(type) {
	case *syntax.ImportDeclaration:
		return declaration.Name, declaration.Line, declaration.NameSpan, true
	case *syntax.TypeDeclaration:
		return declaration.Name, declaration.Line, declaration.NameSpan, true
	case *syntax.RuleDeclaration:
		return declaration.Name, declaration.Line, declaration.NameSpan, true
	case *syntax.FunctionDeclaration:
		return declaration.Name, declaration.Line, declaration.NameSpan, true
	case *syntax.LetDeclaration:
		return declaration.Name, declaration.Line, declaration.NameSpan, true
	default:
		return "", 0, syntax.SourceSpan{}, false
	}
}

func partialLocalBindings(source string, cursorOffset int) []localBinding {
	if cursorOffset < 0 || cursorOffset > len(source) {
		return nil
	}
	prefix := source[:cursorOffset]
	if index, err := analyzeSource(prefix); err == nil {
		return index.visibleLocalBindings(cursorOffset)
	}
	tokens, err := syntax.Lex(prefix)
	if err != nil {
		return nil
	}
	if bindings := partialLambdaLocalBindings(prefix, tokens); len(bindings) != 0 {
		return bindings
	}
	closers, ok := missingDelimiterClosers(prefix, tokens)
	if !ok {
		return nil
	}
	separator := " "
	if prefix == "" || strings.HasSuffix(prefix, "\n") || strings.HasSuffix(prefix, "\r") || strings.HasSuffix(prefix, "\t") || strings.HasSuffix(prefix, " ") {
		separator = ""
	}
	for _, ending := range []string{separator + "null", "\nnull"} {
		candidate := prefix + ending + closers
		if index, parseErr := analyzeSource(candidate); parseErr == nil {
			return index.visibleLocalBindings(cursorOffset)
		}
	}

	return nil
}

func partialLambdaLocalBindings(prefix string, tokens []syntax.Token) []localBinding {
	for _, token := range tokens {
		if token.Kind != syntax.TokenLexeme || prefix[token.Lexeme.Start:token.Lexeme.End] != fnKeyword {
			continue
		}
		lambdaStart := token.Lexeme.Start
		fragment := prefix[lambdaStart:]
		fragmentTokens, err := syntax.Lex(fragment)
		if err != nil {
			continue
		}
		closers, ok := missingDelimiterClosers(fragment, fragmentTokens)
		if !ok {
			continue
		}
		separator := " "
		if strings.HasSuffix(fragment, "\n") || strings.HasSuffix(fragment, "\r") || strings.HasSuffix(fragment, "\t") || strings.HasSuffix(fragment, " ") {
			separator = ""
		}
		for _, ending := range []string{"", separator + "null", "\nnull"} {
			declaration := "let __mcl_partial = "
			candidate := declaration + fragment + ending + closers
			module, parseErr := syntax.ParseAST(candidate)
			if parseErr != nil {
				continue
			}
			index := analyzeModule(module)
			syntheticCursor := len(declaration) + len(fragment)
			bindings := index.visibleLocalBindings(syntheticCursor)
			for bindingIndex := range bindings {
				bindings[bindingIndex].declaration.Start += lambdaStart - len(declaration)
				bindings[bindingIndex].declaration.End += lambdaStart - len(declaration)
			}
			if len(bindings) != 0 {
				return bindings
			}
		}
	}

	return nil
}

func missingDelimiterClosers(source string, tokens []syntax.Token) (string, bool) {
	openers := make([]string, 0)
	for _, token := range tokens {
		if token.Kind != syntax.TokenLexeme {
			continue
		}
		lexeme := source[token.Lexeme.Start:token.Lexeme.End]
		switch lexeme {
		case "(", "[", "{":
			openers = append(openers, lexeme)
		case ")", "]", "}":
			if len(openers) == 0 {
				return "", false
			}
			match := openers[len(openers)-1] == "(" && lexeme == ")" || openers[len(openers)-1] == "[" && lexeme == "]" || openers[len(openers)-1] == "{" && lexeme == "}"
			if !match {
				return "", false
			}
			openers = openers[:len(openers)-1]
		}
	}
	closers := make([]byte, 0, len(openers))
	for _, opener := range slices.Backward(openers) {
		switch opener {
		case "(":
			closers = append(closers, ')')
		case "[":
			closers = append(closers, ']')
		case "{":
			closers = append(closers, '}')
		}
	}

	return string(closers), true
}

func sourceDefinitionLocation(uri, source string, span syntax.SourceSpan) *definitionLocation {
	result := &definitionLocation{URI: uri}
	index := sourceindex.New(source)
	result.Range.Start = sourcePositionAt(index, span.Start)
	result.Range.End = sourcePositionAt(index, span.End)

	return result
}

func declaredName(line string) (string, int, bool) {
	tokens, err := syntax.Lex(line)
	if err != nil {
		return "", 0, false
	}
	lexemes := make([]syntax.Token, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind == syntax.TokenLexeme {
			lexemes = append(lexemes, token)
		}
	}
	text := func(index int) string {
		return line[lexemes[index].Lexeme.Start:lexemes[index].Lexeme.End]
	}
	if len(lexemes) == 0 {
		return "", 0, false
	}
	keywordIndex := 0
	if text(0) == "export" {
		keywordIndex++
	}
	if keywordIndex >= len(lexemes) {
		return "", 0, false
	}
	keyword := text(keywordIndex)
	nameIndex := keywordIndex + 1
	if keyword == "import" {
		nameIndex = -1
		for index := keywordIndex + 1; index+1 < len(lexemes); index++ {
			if text(index) == "as" {
				nameIndex = index + 1

				break
			}
		}
	} else if keyword != "let" && keyword != "type" && keyword != "rule" && keyword != "fn" {
		return "", 0, false
	}
	if nameIndex < 0 || nameIndex >= len(lexemes) {
		return "", 0, false
	}
	name := text(nameIndex)
	if !isMCLIdentifier(name) {
		return "", 0, false
	}
	start := lexemes[nameIndex].Lexeme.Start

	return name, utf16Length([]rune(line[:start])), true
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

func utf16Length(runes []rune) int {
	length := 0
	for _, char := range runes {
		length += utf16.RuneLen(char)
	}

	return length
}

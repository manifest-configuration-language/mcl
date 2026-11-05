package engine

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

func validateParameterDeclarations(module ModuleAST, state *EvaluationState, entry bool) error {
	declared := make(map[string]struct{})
	for _, node := range module.Nodes {
		declaration, isDeclaration := node.(*LetDeclaration)
		if !isDeclaration || declaration == nil || !declaration.Parameter {
			continue
		}
		if !entry {
			return sourceDiagnostic(declaration.SourcePath, declaration.Span, errors.New("parameters can only be declared in the entry module"))
		}
		if _, exists := declared[declaration.Name]; exists {
			return sourceDiagnostic(declaration.SourcePath, declaration.NameSpan, fmt.Errorf("parameter %q is declared more than once", declaration.Name))
		}
		declared[declaration.Name] = struct{}{}
		if declaration.Annotation == nil {
			return sourceDiagnostic(declaration.SourcePath, declaration.Span, fmt.Errorf("parameter %q requires a type", declaration.Name))
		}
	}
	if !entry {
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(state.Parameters)) {
		if _, exists := declared[name]; !exists {
			return fmt.Errorf("unknown parameter %q", name)
		}
	}

	return nil
}

func evaluateParameterDeclaration(declaration *LetDeclaration, state *EvaluationState) error {
	if _, exists := state.Environment[declaration.Name]; exists {
		return sourceDiagnostic(declaration.SourcePath, declaration.NameSpan, fmt.Errorf("%q is already defined", declaration.Name))
	}
	expected, err := resolveTypeFromSyntax(*declaration.Annotation, state)
	if err != nil {
		return sourceDiagnostic(declaration.SourcePath, declaration.Annotation.Span, err)
	}
	if err := validateTypeExpr(expected, state.Types, make(map[string]bool)); err != nil {
		return sourceDiagnostic(declaration.SourcePath, declaration.Annotation.Span, err)
	}
	var value RuntimeValue
	parameterSource, hasParameterSource := state.ParameterSources[declaration.Name]
	if raw, provided := state.Parameters[declaration.Name]; provided {
		value, err = runtimeValueFromJSON(raw, declaration.Name, parameterSource, hasParameterSource)
		if err != nil {
			return sourceDiagnostic(declaration.SourcePath, declaration.NameSpan, fmt.Errorf("parameter %q: %w", declaration.Name, err))
		}
	} else if declaration.Expression != nil {
		analysis, known, analysisErr := inferStaticExprAnalysisWithExpected(declaration.Expression, &expected, state.StaticTypes, state.Types)
		if analysisErr != nil {
			return sourceDiagnostic(declaration.SourcePath, declaration.Expression.ExprSpan(), analysisErr)
		}
		typed, supported, typedErr := typedExpressionFromKnownAnalysis(declaration.Expression, state, analysis, known)
		if typedErr != nil {
			return typedErr
		}
		if !supported {
			return sourceDiagnostic(declaration.SourcePath, declaration.Expression.ExprSpan(), fmt.Errorf("expression type %T is not supported by the typed evaluator", declaration.Expression))
		}
		value, err = evaluateRuntimeExpression(typed, state)
		if err != nil {
			return err
		}
	} else {
		return sourceDiagnostic(declaration.SourcePath, declaration.NameSpan, fmt.Errorf("parameter %q is required", declaration.Name))
	}
	if _, supplied := state.Parameters[declaration.Name]; !supplied {
		value.Value.SourcePath = declaration.SourcePath
		value.Value.Span = declaration.NameSpan
	}
	var analysis *TypeAnalysisContext
	if state.Module != nil {
		analysis = state.Module.Analysis
	}
	if err := checkRuntimeType(value, expected, state.Types, make(map[string]bool), analysis); err != nil {
		return parameterTypeDiagnostic(declaration, expected, value, parameterSource, hasParameterSource, err)
	}
	value.EnsureTyping().TypeProvenance = append(value.EnsureTyping().TypeProvenance, TypeProvenance{Type: expected, Types: maps.Clone(state.Types)})
	state.Environment[declaration.Name] = value
	state.StaticTypes[declaration.Name] = TypeAnalysis{Type: expected}

	return nil
}

func parameterTypeDiagnostic(declaration *LetDeclaration, expected ResolvedType, value RuntimeValue, source ParameterSource, hasSource bool, cause error) error {
	name := declaration.Name
	relativePath := []runtimeTypePathSegment(nil)
	expectedName := staticTypeName(expected)
	actualName := kindName(value.Kind())
	if failure, ok := runtimeTypeFailureOf(cause); ok {
		relativePath = slices.Clone(failure.Path)
		if failure.Expected != "" {
			expectedName = failure.Expected
		}
		if failure.Actual != "" {
			actualName = failure.Actual
		}
	} else if unknown, ok := errors.AsType[unknownTypeField](cause); ok {
		relativePath = slices.Clone(unknown.path)
		if len(relativePath) == 0 {
			relativePath = []runtimeTypePathSegment{{Value: unknown.name}}
		}
		expectedName = "declared fields"
		actualName = "unknown field"
	}
	pointer := parameterJSONPointer(name)
	for _, part := range relativePath {
		pointer = appendParameterJSONPointer(pointer, part.Value)
	}
	displayPath := parameterJSONDisplayPath(name, relativePath)
	message := fmt.Sprintf("parameter %q does not match its declared type %s\npath: %s\npointer: %s\nexpected: %s\nactual: %s", name, staticTypeName(expected), displayPath, pointer, expectedName, actualName)
	if hasSource && source.Label != "" {
		message += "\nsource: " + source.Label
	}

	primaryPath := declaration.SourcePath
	primarySpan := declaration.NameSpan
	var related []SourceDiagnosticLocation
	if diagnostic, ok := SourceDiagnosticFromError(cause); ok {
		related = append(related, diagnostic.Related...)
	}
	if hasSource && source.SourcePath != "" {
		locationPointer := pointer
		if actualName == "missing" && len(relativePath) != 0 {
			locationPointer = parameterJSONPointer(name)
			for _, part := range relativePath[:len(relativePath)-1] {
				locationPointer = appendParameterJSONPointer(locationPointer, part.Value)
			}
		}
		if location, exists := source.Locations[locationPointer]; exists {
			span := location.Span
			if actualName == "unknown field" && location.KeySpan.End > location.KeySpan.Start {
				span = location.KeySpan
			}
			if span.End > span.Start {
				primaryPath = source.SourcePath
				primarySpan = span
			}
		}
	}
	if primaryPath != declaration.SourcePath || primarySpan != declaration.NameSpan {
		related = append(related, SourceDiagnosticLocation{Path: declaration.SourcePath, Span: declaration.NameSpan, Message: "Parameter declaration"})
	}

	return SourceDiagnosticError{Path: primaryPath, Code: codeMCLType, Message: message, Span: primarySpan, Related: related, OmitSourceSnippet: true, Cause: cause}
}

func runtimeValueFromJSON(raw jsontext.Value, name string, source ParameterSource, hasSource bool) (RuntimeValue, error) {
	if len(raw) > maxRuntimeParameterBytes {
		return RuntimeValue{}, fmt.Errorf("parameter value exceeds %d bytes", maxRuntimeParameterBytes)
	}
	if !raw.IsValid() {
		return RuntimeValue{}, errors.New("parameter value is not valid JSON")
	}

	return runtimeValueFromJSONAtDepth(raw, 0, source, hasSource, parameterJSONPointer(name))
}

const maxRuntimeParameterBytes = 64 << 20

func runtimeValueFromJSONAtDepth(raw jsontext.Value, depth int, source ParameterSource, hasSource bool, pointer string) (RuntimeValue, error) {
	if depth > maxCallDepth {
		return RuntimeValue{}, fmt.Errorf("parameter value nesting exceeds %d levels", maxCallDepth)
	}
	metadata := runtimeValueData{SourcePath: source.SourcePath}
	if hasSource {
		if location, exists := source.Locations[pointer]; exists {
			metadata.Span = location.Span
		}
	}
	switch raw.Kind() {
	case jsontext.KindNull:
		metadata.Kind = Null

		return RuntimeValue{Value: metadata}, nil
	case jsontext.KindFalse:
		metadata.Kind = Bool

		return RuntimeValue{Value: metadata}, nil
	case jsontext.KindTrue:
		metadata.Kind = Bool
		metadata.Bool = true

		return RuntimeValue{Value: metadata}, nil
	case jsontext.KindString:
		var value string
		if err := jsonv2.Unmarshal(raw, &value); err != nil {
			return RuntimeValue{}, fmt.Errorf("decode parameter string: %w", err)
		}

		metadata.Kind = String
		metadata.Text = value

		return RuntimeValue{Value: metadata}, nil
	case jsontext.KindNumber:
		kind := Integer
		if strings.ContainsAny(string(raw), ".eE") {
			kind = Decimal
		}

		metadata.Kind = kind
		metadata.Text = string(raw)

		return RuntimeValue{Value: metadata}, nil
	case jsontext.KindBeginArray:
		decoder := jsontext.NewDecoder(bytes.NewReader(raw))
		if _, err := decoder.ReadToken(); err != nil {
			return RuntimeValue{}, fmt.Errorf("read parameter array: %w", err)
		}
		metadata.Kind = List
		result := RuntimeValue{Value: metadata}
		for decoder.PeekKind() != jsontext.KindEndArray {
			item, err := decoder.ReadValue()
			if err != nil {
				return RuntimeValue{}, fmt.Errorf("read parameter array item: %w", err)
			}
			index := len(result.List)
			value, err := runtimeValueFromJSONAtDepth(item, depth+1, source, hasSource, appendParameterJSONPointer(pointer, strconv.Itoa(index)))
			if err != nil {
				return RuntimeValue{}, fmt.Errorf("array item %d: %w", len(result.List), err)
			}
			result.List = append(result.List, value)
		}
		if _, err := decoder.ReadToken(); err != nil {
			return RuntimeValue{}, fmt.Errorf("read parameter array end: %w", err)
		}

		return result, nil
	case jsontext.KindBeginObject:
		decoder := jsontext.NewDecoder(bytes.NewReader(raw))
		if _, err := decoder.ReadToken(); err != nil {
			return RuntimeValue{}, fmt.Errorf("read parameter object: %w", err)
		}
		metadata.Kind = Map
		result := RuntimeValue{Value: metadata}
		seen := make(map[string]struct{})
		for decoder.PeekKind() != jsontext.KindEndObject {
			key, err := decoder.ReadToken()
			if err != nil {
				return RuntimeValue{}, fmt.Errorf("read parameter object key: %w", err)
			}
			if key.Kind() != jsontext.KindString {
				return RuntimeValue{}, errors.New("parameter object key must be a string")
			}
			name := key.String()
			if _, exists := seen[name]; exists {
				return RuntimeValue{}, fmt.Errorf("parameter object contains duplicate key %q", name)
			}
			seen[name] = struct{}{}
			item, err := decoder.ReadValue()
			if err != nil {
				return RuntimeValue{}, fmt.Errorf("read parameter field %q: %w", name, err)
			}
			childPointer := appendParameterJSONPointer(pointer, name)
			location := source.Locations[childPointer]
			value, err := runtimeValueFromJSONAtDepth(item, depth+1, source, hasSource, childPointer)
			if err != nil {
				return RuntimeValue{}, fmt.Errorf("parameter field %q: %w", name, err)
			}
			entry := RuntimeEntry{Key: name, Value: value}
			if hasSource && location.KeySpan.End > location.KeySpan.Start {
				entry.KeySpan = location.KeySpan
				entry.KeySourcePath = source.SourcePath
				entry.KeySourceLocations = []SourceDiagnosticLocation{{Path: source.SourcePath, Span: location.KeySpan, Message: "Parameter key"}}
			}
			result.Map = append(result.Map, entry)
		}
		if _, err := decoder.ReadToken(); err != nil {
			return RuntimeValue{}, fmt.Errorf("read parameter object end: %w", err)
		}

		return result, nil
	default:
		return RuntimeValue{}, fmt.Errorf("unsupported JSON kind %s", raw.Kind())
	}
}

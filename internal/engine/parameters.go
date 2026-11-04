package engine

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
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
	if raw, provided := state.Parameters[declaration.Name]; provided {
		value, err = runtimeValueFromJSON(raw)
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
	value.Value.SourcePath = declaration.SourcePath
	value.Value.Span = declaration.NameSpan
	var analysis *TypeAnalysisContext
	if state.Module != nil {
		analysis = state.Module.Analysis
	}
	if err := checkRuntimeType(value, expected, state.Types, make(map[string]bool), analysis); err != nil {
		return sourceDiagnostic(declaration.SourcePath, declaration.NameSpan, fmt.Errorf("parameter %q does not match its declared type: %w", declaration.Name, err))
	}
	value.EnsureTyping().TypeProvenance = append(value.EnsureTyping().TypeProvenance, TypeProvenance{Type: expected, Types: maps.Clone(state.Types)})
	state.Environment[declaration.Name] = value
	state.StaticTypes[declaration.Name] = TypeAnalysis{Type: expected}

	return nil
}

func runtimeValueFromJSON(raw jsontext.Value) (RuntimeValue, error) {
	if len(raw) > maxRuntimeParameterBytes {
		return RuntimeValue{}, fmt.Errorf("parameter value exceeds %d bytes", maxRuntimeParameterBytes)
	}
	if !raw.IsValid() {
		return RuntimeValue{}, errors.New("parameter value is not valid JSON")
	}

	return runtimeValueFromJSONAtDepth(raw, 0)
}

const maxRuntimeParameterBytes = 64 << 20

func runtimeValueFromJSONAtDepth(raw jsontext.Value, depth int) (RuntimeValue, error) {
	if depth > maxCallDepth {
		return RuntimeValue{}, fmt.Errorf("parameter value nesting exceeds %d levels", maxCallDepth)
	}
	switch raw.Kind() {
	case jsontext.KindNull:
		return RuntimeValue{Value: runtimeValueData{Kind: Null}}, nil
	case jsontext.KindFalse:
		return RuntimeValue{Value: runtimeValueData{Kind: Bool}}, nil
	case jsontext.KindTrue:
		return RuntimeValue{Value: runtimeValueData{Kind: Bool, Bool: true}}, nil
	case jsontext.KindString:
		var value string
		if err := jsonv2.Unmarshal(raw, &value); err != nil {
			return RuntimeValue{}, fmt.Errorf("decode parameter string: %w", err)
		}

		return RuntimeValue{Value: runtimeValueData{Kind: String, Text: value}}, nil
	case jsontext.KindNumber:
		kind := Integer
		if strings.ContainsAny(string(raw), ".eE") {
			kind = Decimal
		}

		return RuntimeValue{Value: runtimeValueData{Kind: kind, Text: string(raw)}}, nil
	case jsontext.KindBeginArray:
		decoder := jsontext.NewDecoder(bytes.NewReader(raw))
		if _, err := decoder.ReadToken(); err != nil {
			return RuntimeValue{}, fmt.Errorf("read parameter array: %w", err)
		}
		result := RuntimeValue{Value: runtimeValueData{Kind: List}}
		for decoder.PeekKind() != jsontext.KindEndArray {
			item, err := decoder.ReadValue()
			if err != nil {
				return RuntimeValue{}, fmt.Errorf("read parameter array item: %w", err)
			}
			value, err := runtimeValueFromJSONAtDepth(item, depth+1)
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
		result := RuntimeValue{Value: runtimeValueData{Kind: Map}}
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
			value, err := runtimeValueFromJSONAtDepth(item, depth+1)
			if err != nil {
				return RuntimeValue{}, fmt.Errorf("parameter field %q: %w", name, err)
			}
			result.Map = append(result.Map, RuntimeEntry{Key: name, Value: value})
		}
		if _, err := decoder.ReadToken(); err != nil {
			return RuntimeValue{}, fmt.Errorf("read parameter object end: %w", err)
		}

		return result, nil
	default:
		return RuntimeValue{}, fmt.Errorf("unsupported JSON kind %s", raw.Kind())
	}
}

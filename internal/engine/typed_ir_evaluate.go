package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

func evaluateRuntimeOptionalReference(text string, context typedEvaluationContext) (RuntimeValue, error) {
	root := referenceRoot(text)
	value, exists := context.runtimeEnv[root]
	if !exists {
		return RuntimeValue{}, sourceDiagnostic(context.sourcePath, SourceSpan{}, fmt.Errorf("%w %q", errUndefinedName, root))
	}
	if context.stack == nil {
		context.stack = make(map[string]bool)
	}
	if context.stack[root] {
		return RuntimeValue{}, sourceDiagnostic(context.sourcePath, SourceSpan{}, fmt.Errorf("cyclic definition involving %q", root))
	}
	context.stack[root] = true
	defer delete(context.stack, root)
	_, rest, _ := strings.Cut(text, root)
	chain := false
	for rest != "" {
		optional := strings.HasPrefix(rest, "?.")
		if optional {
			rest = rest[2:]
			chain = true
		} else if strings.HasPrefix(rest, ".") {
			rest = rest[1:]
		} else {
			return RuntimeValue{}, fmt.Errorf("invalid optional field path %q", text)
		}
		fieldEnd := len(rest)
		if next := strings.Index(rest, "?."); next >= 0 && next < fieldEnd {
			fieldEnd = next
		}
		if next := strings.IndexByte(rest, '.'); next >= 0 && next < fieldEnd {
			fieldEnd = next
		}
		field := rest[:fieldEnd]
		rest = rest[fieldEnd:]
		value = runtimeNodeContents(value)
		if value.Kind() == Null {
			if chain {
				return value, nil
			}

			return RuntimeValue{}, fmt.Errorf("cannot select field %q from Null", field)
		}
		if value.Kind() != Map {
			return RuntimeValue{}, fmt.Errorf("cannot select field %q from a non-map value", field)
		}
		selected, found := value.MapLookup(field)
		if !found {
			if chain {
				return RuntimeValue{Value: runtimeValueData{Kind: Null, SourcePath: context.sourcePath}}, nil
			}

			return RuntimeValue{}, fmt.Errorf("field %q does not exist", field)
		}
		value = selected
	}

	return value, nil
}

func runtimeValueWithExpressionSources(value RuntimeValue, sourcePath string, sourceSpan SourceSpan, dependencies ...RuntimeValue) RuntimeValue {
	result := value
	locations := slices.Clone(result.Value.SourceLocations)
	for _, dependency := range dependencies {
		appendRuntimeValueSourceLocations(&locations, dependency)
	}
	result.Value.Span = sourceSpan
	result.Value.SourcePath = sourcePath
	result.Value.SourceLocations = locations

	return result
}

func appendRuntimeValueSourceLocations(locations *[]SourceDiagnosticLocation, value RuntimeValue) {
	metadata := value.Metadata()
	*locations = appendSourceLocations(*locations, metadata.SourceLocations, metadata.SourcePath, metadata.Span, "Expression operand")
}

func (expression typedExpression) evaluate(context typedEvaluationContext) (RuntimeValue, error) {
	if err := context.allocations.consume(1); err != nil {
		return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, err)
	}
	if err := context.budget.consumeStep(); err != nil {
		return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, err)
	}
	value := RuntimeValue{Value: runtimeValueData{Span: expression.span, SourcePath: context.sourcePath}}
	switch expression.kind {
	case typedExpressionNull:
		value.Value.Kind = Null
	case typedExpressionBool:
		value.Value.Kind = Bool
		value.Value.Bool = expression.bool
	case typedExpressionInteger:
		value.Value.Kind = Integer
		value.Value.Text = expression.text
	case typedExpressionDecimal:
		value.Value.Kind = Decimal
		value.Value.Text = expression.text
	case typedExpressionString:
		value.Value.Kind = String
		value.Value.Text = expression.text
	case typedExpressionReference:
		root, rest, _ := strings.Cut(expression.text, ".")
		resolved, exists := context.runtimeEnv[root]
		if !exists {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, fmt.Errorf("%w %q", errUndefinedName, root))
		}
		if context.stack == nil {
			context.stack = make(map[string]bool)
		}
		if context.stack[root] {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, fmt.Errorf("cyclic definition involving %q", root))
		}
		context.stack[root] = true
		defer delete(context.stack, root)
		if err := context.budget.consumeStep(); err != nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, err)
		}
		for rest != "" {
			field, remaining, found := strings.Cut(rest, ".")
			if !found {
				rest = ""
			} else {
				rest = remaining
			}
			resolved = runtimeNodeContents(resolved)
			if resolved.Kind() != Map {
				return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, fmt.Errorf("cannot select field %q from a non-map value", field))
			}
			selected, found := resolved.MapLookup(field)
			if !found {
				return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, fmt.Errorf("field %q does not exist", field))
			}
			resolved = selected
		}
		value = resolved
	case typedExpressionList:
		value.Value.Kind = List
		value.List = make([]RuntimeValue, 0, len(expression.items))
		for _, item := range expression.items {
			resolved, err := item.evaluate(context)
			if err != nil {
				return RuntimeValue{}, err
			}
			value.List = append(value.List, resolved)
		}
	case typedExpressionMap:
		value.Value.Kind = Map
		value.Map = make([]RuntimeEntry, 0, len(expression.entries))
		for _, entry := range expression.entries {
			resolved, err := entry.value.evaluate(context)
			if err != nil {
				return RuntimeValue{}, err
			}
			result := RuntimeEntry{
				Key: entry.key, KeySpan: entry.keySpan, KeySourcePath: context.sourcePath, Value: resolved,
			}
			if entry.keyExpression != nil {
				key, err := entry.keyExpression.evaluate(context)
				if err != nil {
					return RuntimeValue{}, err
				}
				if key.IsDeferred() {
					keyExpression := cloneTypedExpression(*entry.keyExpression)
					result.KeyExpression = &keyExpression
					result.KeyScope = maps.Clone(context.runtimeEnv)
				} else if key.Kind() == String {
					result.Key = key.Value.Text
				} else {
					return RuntimeValue{}, sourceDiagnostic(context.sourcePath, entry.keySpan, typeDiagnostic(fmt.Errorf("dynamic map key must be String, got %s", kindName(key.Kind()))))
				}
			}
			value.Map = append(value.Map, result)
		}
	case typedExpressionIndexAccess:
		if expression.left == nil || expression.right == nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("invalid index access"))
		}
		base, err := expression.left.evaluate(context)
		if err != nil {
			return RuntimeValue{}, err
		}
		key, err := expression.right.evaluate(context)
		if err != nil {
			return RuntimeValue{}, err
		}
		if runtimeValueHasDeferred(base) || runtimeValueHasDeferred(key) {
			return RuntimeValue{
				Value: runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
				Deferred: &typedContinuation{Index: &typedIndexContinuation{
					Base: base, Key: key, SourcePath: context.sourcePath, Span: expression.span,
				}},
			}, nil
		}
		indexed, err := applyRuntimeIndexAccess(base, key)
		if err != nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, err)
		}

		return runtimeValueWithExpressionSources(indexed, context.sourcePath, expression.span, base, key), nil
	case typedExpressionNot:
		if expression.operand == nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("invalid unary expression"))
		}
		operand, err := expression.operand.evaluate(context)
		if err != nil {
			return RuntimeValue{}, err
		}
		if operand.IsDeferred() {
			return RuntimeValue{
				Value: runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
				Deferred: &typedContinuation{Unary: &typedUnaryContinuation{
					Operand: operand, SourcePath: context.sourcePath, Span: expression.span,
				}},
			}, nil
		}
		if operand.Kind() != Bool {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, typeDiagnostic(fmt.Errorf("not expects Bool, got %s", kindName(operand.Kind()))))
		}

		result := RuntimeValue{Value: runtimeValueData{Kind: Bool, Bool: !operand.BoolValue()}}

		return runtimeValueWithExpressionSources(result, context.sourcePath, expression.span, operand), nil
	case typedExpressionConditional:
		if expression.condition == nil || expression.thenBranch == nil || expression.elseBranch == nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("invalid conditional expression"))
		}
		condition, err := expression.condition.evaluate(context)
		if err != nil {
			return RuntimeValue{}, err
		}
		if condition.IsDeferred() {
			return RuntimeValue{
				Value: runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
				Deferred: &typedContinuation{Conditional: &typedConditionalContinuation{
					Condition: condition, ThenBranch: cloneTypedExpression(*expression.thenBranch),
					ElseBranch: cloneTypedExpression(*expression.elseBranch), Scope: maps.Clone(context.runtimeEnv),
				}},
			}, nil
		}
		if condition.Kind() != Bool {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, typeDiagnostic(fmt.Errorf("if condition must be Bool, got %s", kindName(condition.Kind()))))
		}
		if condition.BoolValue() {
			branch, err := expression.thenBranch.evaluate(context)
			if err != nil {
				return RuntimeValue{}, err
			}

			return runtimeValueWithExpressionSources(branch, context.sourcePath, expression.span, condition, branch), nil
		}

		branch, err := expression.elseBranch.evaluate(context)
		if err != nil {
			return RuntimeValue{}, err
		}

		return runtimeValueWithExpressionSources(branch, context.sourcePath, expression.span, condition, branch), nil
	case typedExpressionBinary:
		if expression.left == nil || expression.right == nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("invalid binary expression"))
		}
		left, err := expression.left.evaluate(context)
		if err != nil {
			return RuntimeValue{}, err
		}
		if left.IsDeferred() && (expression.operator == typedOperatorAnd || expression.operator == typedOperatorOr || expression.operator == typedOperatorCoalesce) {
			rightExpression := cloneTypedExpression(*expression.right)

			return RuntimeValue{
				Value: runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
				Deferred: &typedContinuation{Binary: &typedBinaryContinuation{
					Operator: expression.operator, Left: left, RightExpression: &rightExpression, Scope: maps.Clone(context.runtimeEnv),
				}},
			}, nil
		}
		//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
		switch expression.operator {
		case typedOperatorAnd, typedOperatorOr:
			if left.Kind() != Bool {
				return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, typeDiagnostic(fmt.Errorf("%s expects Bool operands", expression.operator.token())))
			}
			if expression.operator == typedOperatorAnd && !left.BoolValue() || expression.operator == typedOperatorOr && left.BoolValue() {
				return runtimeValueWithExpressionSources(left, context.sourcePath, expression.span, left), nil
			}
		case typedOperatorCoalesce:
			if left.Kind() != Null {
				return runtimeValueWithExpressionSources(left, context.sourcePath, expression.span, left), nil
			}
		}
		right, err := expression.right.evaluate(context)
		if err != nil {
			return RuntimeValue{}, err
		}
		if left.IsDeferred() || right.IsDeferred() {
			return RuntimeValue{
				Value: runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
				Deferred: &typedContinuation{Binary: &typedBinaryContinuation{
					Operator: expression.operator, Left: left, Right: right, Scope: maps.Clone(context.runtimeEnv),
				}},
			}, nil
		}
		result, err := applyRuntimeTypedBinary(expression.operator, left, right)
		if err != nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, err)
		}

		return runtimeValueWithExpressionSources(result, context.sourcePath, expression.span, left, right), nil
	case typedExpressionLambda:
		if expression.operand == nil || expression.lambdaTyping == nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("invalid Lambda expression"))
		}

		return RuntimeValue{
			Value: runtimeValueData{Kind: Lambda, Span: expression.span, SourcePath: context.sourcePath},
			Lambda: &typedLambda{
				Parameters: slices.Clone(expression.parameters), ParameterSpans: slices.Clone(expression.paramSpans),
				Body: *expression.operand, Scope: maps.Clone(context.runtimeEnv), ScopeSpan: expression.span,
				Typing: expression.lambdaTyping, ModuleContext: context.module,
			},
		}, nil
	case typedExpressionCall:
		if expression.builtinID == "" && expression.callee == nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("invalid function call"))
		}
		var callee RuntimeValue
		var err error
		if expression.builtinID != "" {
			callee = RuntimeValue{Value: runtimeValueData{Kind: Function, Text: string(expression.builtinID)}}
		} else {
			callee, err = expression.callee.evaluate(context)
			if err != nil {
				return RuntimeValue{}, err
			}
		}
		arguments := make([]RuntimeValue, len(expression.args))
		for index, argument := range expression.args {
			arguments[index], err = argument.evaluate(context)
			if err != nil {
				return RuntimeValue{}, err
			}
		}
		state := &EvaluationState{
			Environment: context.runtimeEnv, StaticTypes: context.staticTypes, Types: context.types,
			Stack: context.stack, Budget: context.budget, Allocations: context.allocations,
			SourcePath: context.sourcePath, Module: context.module,
		}
		if callee.Rule != nil {
			ordered, ok := functionCallRuntimeArguments(callee.Rule.Parameters(), arguments, expression.argNames)
			if !ok || len(ordered) != 1 {
				return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("rule call expects exactly one argument"))
			}

			return RuntimeValue{
				Value:    runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
				Deferred: &typedContinuation{Rule: callee.Rule, Arguments: ordered},
			}, nil
		}
		if (callee.Lambda != nil || callee.Kind() == Function) && slices.ContainsFunc(arguments, runtimeValueHasDeferred) {
			return RuntimeValue{
				Value: runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
				Deferred: &typedContinuation{Call: &runtimeCallContinuation{
					Callee: callee, Arguments: slices.Clone(arguments), Names: slices.Clone(expression.argNames),
				}},
			}, nil
		}
		if callee.Lambda != nil {
			result, err := callRuntimeLambda(callee, arguments, expression.argNames, state)
			if err != nil {
				return RuntimeValue{}, err
			}

			calleeReference := RuntimeValue{Value: runtimeValueData{Span: expression.callee.span, SourcePath: context.sourcePath}}
			dependencies := append([]RuntimeValue{callee, calleeReference}, arguments...)

			return runtimeValueWithExpressionSources(result, context.sourcePath, expression.span, dependencies...), nil
		}
		if callee.Kind() == Function {
			if callee.ModuleContext != nil {
				state.Module = callee.ModuleContext
			}
			result, err := callRuntimeStandard(callee.Value.Text, arguments, expression.argNames, state)
			if err != nil {
				return RuntimeValue{}, err
			}

			calleeReference := RuntimeValue{Value: runtimeValueData{Span: expression.callee.span, SourcePath: context.sourcePath}}
			dependencies := append([]RuntimeValue{callee, calleeReference}, arguments...)

			return runtimeValueWithExpressionSources(result, context.sourcePath, expression.span, dependencies...), nil
		}

		return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, typeDiagnostic(fmt.Errorf("cannot call a %s value", kindName(callee.Kind()))))
	case typedExpressionOptionalReference:
		value, err := evaluateRuntimeOptionalReference(expression.text, context)
		if err != nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, err)
		}

		return runtimeValueWithExpressionSources(value, context.sourcePath, expression.span, value), nil
	case typedExpressionDeferredReference:
		if len(expression.args) == 0 || len(expression.args) > 2 {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("ref expects a Node and an optional child Node"))
		}
		value := RuntimeValue{
			Value:    runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
			Deferred: &typedContinuation{Reference: &runtimeReference{Path: slices.Clone(expression.paths)}},
		}
		for index, argument := range expression.args {
			target, err := argument.evaluate(context)
			if err != nil {
				return RuntimeValue{}, err
			}
			if target.Kind() != Node || target.NodeIdentity() == nil {
				return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, typeDiagnostic(fmt.Errorf("ref argument %d must be a Node", index+1)))
			}
			value.Deferred.Reference.Targets = append(value.Deferred.Reference.Targets, target.NodeIdentity())
		}

		return value, nil
	case typedExpressionNode:
		if expression.operand == nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("invalid Node expression"))
		}
		body, err := expression.operand.evaluate(context)
		if err != nil {
			return RuntimeValue{}, err
		}
		if body.Kind() != Map && body.Kind() != List && body.Kind() != AdapterBundle {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, typeDiagnostic(fmt.Errorf("node expects a Map or List, got %s", kindName(body.Kind()))))
		}

		return RuntimeValue{
			Value: runtimeValueData{Kind: Node, Span: expression.span, SourcePath: context.sourcePath, NodeID: &NodeIdentity{originPath: context.sourcePath, originSpan: expression.span}},
			Base:  &body,
		}, nil
	case typedExpressionFor:
		if expression.left == nil || expression.thenBranch == nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("invalid for expression"))
		}
		source, err := expression.left.evaluate(context)
		if err != nil {
			return RuntimeValue{}, err
		}
		if runtimeValueHasDeferred(source) {
			return RuntimeValue{
				Value: runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
				Deferred: &typedContinuation{For: &typedForContinuation{
					Source: source, Body: cloneTypedExpression(*expression.thenBranch),
					LoopName: expression.loopName, Scope: maps.Clone(context.runtimeEnv),
					SourcePath: context.sourcePath, Span: expression.span,
				}},
			}, nil
		}
		items, isList := runtimeListValue(source)
		if !isList {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, typeDiagnostic(fmt.Errorf("for expression expects a List, got %s", kindName(source.Kind()))))
		}
		result := RuntimeValue{Value: runtimeValueData{Kind: List, Span: expression.span, SourcePath: context.sourcePath}, List: make([]RuntimeValue, 0, len(items.List))}
		for _, item := range items.List {
			if err := context.budget.consumeIteration(); err != nil {
				return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, err)
			}
			childContext := context
			childContext.runtimeEnv = maps.Clone(context.runtimeEnv)
			childContext.runtimeEnv[expression.loopName] = item
			value, err := expression.thenBranch.evaluate(childContext)
			if err != nil {
				return RuntimeValue{}, err
			}
			result.List = append(result.List, value)
		}

		return result, nil
	case typedExpressionOverlay:
		if expression.left == nil || expression.right == nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("invalid with expression"))
		}
		base, err := expression.left.evaluate(context)
		if err != nil {
			return RuntimeValue{}, err
		}
		patch, err := expression.right.evaluate(context)
		if err != nil {
			return RuntimeValue{}, err
		}
		if runtimeValueHasDeferred(base) || runtimeValueHasDeferred(patch) {
			return RuntimeValue{
				Value: runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
				Deferred: &typedContinuation{Overlay: &typedOverlayContinuation{
					Base: base, Patch: patch, SourcePath: context.sourcePath, Span: expression.span,
				}},
			}, nil
		}
		merged, err := mergeRuntimeValues(base, patch)
		if err != nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, err)
		}
		if err := context.allocations.chargeValueGraph(merged); err != nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, err)
		}

		return merged, nil
	case typedExpressionRemoval:
		if expression.operand == nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("invalid without expression"))
		}
		value, err := expression.operand.evaluate(context)
		if err != nil {
			return RuntimeValue{}, err
		}
		if runtimeValueHasDeferred(value) {
			return RuntimeValue{
				Value: runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
				Deferred: &typedContinuation{Removal: &typedRemovalContinuation{
					Value: value, Paths: slices.Clone(expression.paths), SourcePath: context.sourcePath, Span: expression.span,
				}},
			}, nil
		}
		for _, path := range expression.paths {
			value, err = removeRuntimePath(value, strings.Split(strings.TrimPrefix(path, "."), "."))
			if err != nil {
				return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, fmt.Errorf("path %s: %w", path, err))
			}
		}
		if err := validateRuntimeInheritedConstraints(value); err != nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, err)
		}
		if err := context.allocations.chargeValueGraph(value); err != nil {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, err)
		}

		return value, nil
	}

	return value, nil
}

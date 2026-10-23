package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type activeReference struct {
	node *NodeIdentity
	path string
}

func resolveRuntimeBundle(bundle *RuntimeBundle, state *EvaluationState) error {
	if bundle == nil || state == nil {
		return errors.New("runtime Bundle and evaluation state are required")
	}
	nodes, err := indexRuntimeBundleNodes(bundle, state)
	if err != nil {
		return err
	}
	state.NodeIndex = nodes
	index := newRuntimeBundleIndex(bundle)
	if state.ActiveReferences == nil {
		state.ActiveReferences = make(map[activeReference]bool)
	}
	if state.ActiveRules == nil {
		state.ActiveRules = make(map[*runtimeRuleIdentity]bool)
	}
	keyStates := make(map[*RuntimeValue][]uint8)
	var resolve func(*RuntimeValue) error
	var resolveOccurrence func(*RuntimeValue, *runtimeOrderNode) error
	var resolveKeys func(*RuntimeValue) error
	resolve = func(value *RuntimeValue) error {
		return resolveOccurrence(value, nil)
	}
	resolveKeys = func(value *RuntimeValue) error {
		if value == nil || value.Kind() != Map {
			return nil
		}
		states := keyStates[value]
		if len(states) != len(value.Map) {
			states = make([]uint8, len(value.Map))
			keyStates[value] = states
		}
		keys := make(map[string]bool, len(value.Map))
		for index := range value.Map {
			entry := &value.Map[index]
			if err := state.Budget.consumeStep(); err != nil {
				return sourceDiagnostic(entry.KeySourcePath, entry.KeySpan, err)
			}
			if entry.KeyExpression != nil && states[index] != 2 {
				states[index] = 1
				key, err := evaluateTypedContinuation(&typedContinuation{Expression: *entry.KeyExpression, Scope: entry.KeyScope}, state)
				if err != nil {
					states[index] = 0

					return sourceDiagnostic(entry.KeySourcePath, entry.KeySpan, fmt.Errorf("resolve dynamic map key: %w", err))
				}
				if err := resolve(&key); err != nil {
					states[index] = 0

					return sourceDiagnostic(entry.KeySourcePath, entry.KeySpan, fmt.Errorf("resolve dynamic map key: %w", err))
				}
				if key.Kind() != String {
					states[index] = 0

					return sourceDiagnostic(entry.KeySourcePath, entry.KeySpan, typeDiagnostic(fmt.Errorf("dynamic map key must be String, got %s", kindName(key.Kind()))))
				}
				entry.Key = key.Value.Text
				entry.KeyExpression = nil
				entry.KeyScope = nil
				states[index] = 2
			}
			if states[index] == 1 {
				continue
			}
			if keys[entry.Key] {
				return sourceDiagnostic(entry.KeySourcePath, entry.KeySpan, fmt.Errorf("duplicate key %q after dynamic key evaluation", entry.Key))
			}
			keys[entry.Key] = true
		}

		return nil
	}
	resolveOccurrence = func(value *RuntimeValue, occurrence *runtimeOrderNode) error {
		if value == nil {
			return nil
		}
		if err := state.Budget.consumeStep(); err != nil {
			path, span := value.Source()

			return sourceDiagnostic(path, span, err)
		}
		if value.Deferred != nil {
			var resolved RuntimeValue
			if value.Deferred.Reference != nil {
				resolvedReference, err := resolveRuntimeNodeReference(value.Deferred.Reference, state, resolve, resolveKeys)
				if err != nil {
					path, span := value.Source()
					related := unresolvedRuntimeNodeLocations(value.Deferred.Reference)
					*value = RuntimeValue{}

					return sourceDiagnosticWithCode(path, span, "E401", err, related)
				}
				resolved = resolvedReference
			} else if value.Deferred.Rule != nil {
				arguments := slices.Clone(value.Deferred.Arguments)
				for index := range arguments {
					if err := resolve(&arguments[index]); err != nil {
						return err
					}
				}
				if len(arguments) != 1 {
					return errors.New("rule call expects exactly one argument")
				}
				failures, err := invokeRuntimeRule(*value.Deferred.Rule, arguments[0], state, resolve)
				if err != nil {
					return err
				}
				resolved = RuntimeValue{Value: value.Metadata(), RuleResult: &runtimeRuleResult{Failures: failures}}
			} else if value.Deferred.Binary != nil {
				var err error
				resolved, err = resolveTypedBinaryContinuation(value.Deferred.Binary, state, resolve)
				if err != nil {
					return sourceDiagnostic(value.Value.SourcePath, value.Value.Span, err)
				}
			} else if value.Deferred.Conditional != nil {
				var err error
				resolved, err = resolveTypedConditionalContinuation(value.Deferred.Conditional, state, resolve)
				if err != nil {
					return sourceDiagnostic(value.Value.SourcePath, value.Value.Span, err)
				}
			} else if value.Deferred.Predicate != nil {
				var err error
				resolved, err = resolveRuntimePredicateContinuation(value.Deferred.Predicate, state, resolve)
				if err != nil {
					return sourceDiagnostic(value.Value.SourcePath, value.Value.Span, err)
				}
			} else if value.Deferred.Call != nil {
				var err error
				resolved, err = resolveRuntimeCallContinuation(value.Deferred.Call, state, resolve)
				if err != nil {
					return sourceDiagnostic(value.Value.SourcePath, value.Value.Span, err)
				}
			} else if value.Deferred.Unary != nil {
				var err error
				resolved, err = resolveTypedUnaryContinuation(value.Deferred.Unary, resolve)
				if err != nil {
					return sourceDiagnostic(value.Value.SourcePath, value.Value.Span, err)
				}
			} else if value.Deferred.Index != nil {
				var err error
				resolved, err = resolveTypedIndexContinuation(value.Deferred.Index, resolve)
				if err != nil {
					return sourceDiagnostic(value.Value.SourcePath, value.Value.Span, err)
				}
			} else if value.Deferred.For != nil {
				var err error
				resolved, err = resolveTypedForContinuation(value.Deferred.For, state, resolve)
				if err != nil {
					return sourceDiagnostic(value.Value.SourcePath, value.Value.Span, err)
				}
			} else if value.Deferred.Overlay != nil {
				var err error
				resolved, err = resolveTypedOverlayContinuation(value.Deferred.Overlay, state, resolve)
				if err != nil {
					return sourceDiagnostic(value.Value.SourcePath, value.Value.Span, err)
				}
			} else if value.Deferred.Removal != nil {
				var err error
				resolved, err = resolveTypedRemovalContinuation(value.Deferred.Removal, state, resolve)
				if err != nil {
					return sourceDiagnostic(value.Value.SourcePath, value.Value.Span, err)
				}
			} else {
				var err error
				resolved, err = evaluateTypedContinuation(value.Deferred, state)
				if err != nil {
					path, span := value.Source()

					return sourceDiagnostic(path, span, err)
				}
			}
			*value = resolved
			if occurrence != nil {
				if err := index.replace(occurrence, state); err != nil {
					return err
				}
			}
		}
		//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
		switch value.Kind() {
		case Node:
			if value.NodeIdentity() == nil || value.Base == nil {
				return errors.New("invalid Node value")
			}
			if occurrence != nil && len(occurrence.children) > 0 {
				return resolveOccurrence(value.Base, occurrence.children[0])
			}

			return resolve(value.Base)
		case List, AdapterBundle:
			for index := range value.List {
				var childOccurrence *runtimeOrderNode
				if occurrence != nil && index < len(occurrence.children) {
					childOccurrence = occurrence.children[index]
				}
				if err := resolveOccurrence(&value.List[index], childOccurrence); err != nil {
					return fmt.Errorf("item %d: %w", index, err)
				}
			}
		case Map:
			if err := resolveKeys(value); err != nil {
				return err
			}
			for index := range value.Map {
				var childOccurrence *runtimeOrderNode
				if occurrence != nil && index < len(occurrence.children) {
					childOccurrence = occurrence.children[index]
				}
				if err := resolveOccurrence(&value.Map[index].Value, childOccurrence); err != nil {
					return fmt.Errorf("field %q: %w", value.Map[index].Key, err)
				}
			}
		}

		return nil
	}
	for artifactIndex := range bundle.Artifacts {
		if err := resolveOccurrence(&bundle.Artifacts[artifactIndex].Value, index.roots[artifactIndex]); err != nil {
			return fmt.Errorf("emitted value %d: %w", artifactIndex+1, err)
		}
		if err := rejectRuntimeRuleValues(bundle.Artifacts[artifactIndex].Value); err != nil {
			return fmt.Errorf("emitted value %d: %w", artifactIndex+1, err)
		}
		if _, err := runtimeValueToYAMLNode(bundle.Artifacts[artifactIndex].Value); err != nil {
			return fmt.Errorf("emitted value %d: %w", artifactIndex+1, err)
		}
	}
	for index := range bundle.PendingChecks {
		check := &bundle.PendingChecks[index]
		if err := resolve(&check.Value); err != nil {
			return fmt.Errorf("%s: %w", check.Message, err)
		}
		if err := checkRuntimeType(check.Value, check.Annotation, check.Types, make(map[string]bool)); err != nil {
			return fmt.Errorf("%s: %w", check.Message, err)
		}
	}
	for index := range bundle.Artifacts {
		annotateBuiltinSchemaProvenanceRuntime(&bundle.Artifacts[index].Value)
	}
	for _, validation := range bundle.PendingValidations {
		if validation.Output {
			if err := state.Allocations.consume(len(bundle.Artifacts) + 1); err != nil {
				return sourceDiagnostic(validation.SourcePath, validation.Span, err)
			}
			values := make([]RuntimeValue, len(bundle.Artifacts))
			for index := range bundle.Artifacts {
				values[index] = bundle.Artifacts[index].Value
				values[index].Value.setLifecycleMetadata(nil)
			}
			validation.Value = RuntimeValue{Value: runtimeValueData{Kind: List, SourcePath: validation.SourcePath, Span: validation.Span}, List: values}
		} else if err := resolve(&validation.Value); err != nil {
			return fmt.Errorf("line %d: %w", validation.Line, err)
		}
		if validation.Each {
			if validation.QueryType == nil && validation.QuerySchema == nil && validation.QueryWhere == nil {
				return fmt.Errorf("line %d: descendants query is missing its type or selector", validation.Line)
			}
			if validation.QueryType != nil {
				if err := validateTypeExpr(*validation.QueryType, validation.Types, make(map[string]bool)); err != nil {
					return fmt.Errorf("line %d: descendants query: %w", validation.Line, err)
				}
			}
			if validation.QuerySchema != nil {
				if err := validateTypeExpr(*validation.QuerySchema, validation.Types, make(map[string]bool)); err != nil {
					return fmt.Errorf("line %d: descendants schema query: %w", validation.Line, err)
				}
			}
			if !validation.MinMatchesSet && (validation.Each || validation.QuerySchema != nil) {
				validation.MinMatches = 1
			}
			matches := 0
			for _, candidate := range runtimeBundleDescendants(*bundle) {
				if err := state.Budget.consumeIteration(); err != nil {
					path, span := validation.Value.Source()

					return sourceDiagnostic(path, span, err)
				}
				selected := true
				if validation.QueryWhere != nil {
					candidate = runtimeMapContents(candidate)
					if candidate.Kind() != Map {
						continue
					}
					predicate, err := callRuntimeLambda(*validation.QueryWhere, []RuntimeValue{candidate}, nil, state)
					if err != nil {
						return fmt.Errorf("line %d: descendants selector: %w", validation.Line, err)
					}
					if err := resolve(&predicate); err != nil {
						return fmt.Errorf("line %d: descendants selector: %w", validation.Line, err)
					}
					if predicate.Kind() != Bool {
						return fmt.Errorf("line %d: descendants selector must return Bool, got %s", validation.Line, kindName(predicate.Kind()))
					}
					selected = predicate.Value.Bool
				} else if validation.QueryType != nil {
					if hasProvenance, matches := runtimeValueTypeProvenance(candidate, *validation.QueryType, validation.Types); hasProvenance {
						selected = matches
					} else {
						selected = checkRuntimeType(candidate, *validation.QueryType, validation.Types, make(map[string]bool)) == nil
					}
				} else if validation.QuerySchema != nil {
					selected = checkRuntimeType(candidate, *validation.QuerySchema, validation.Types, make(map[string]bool)) == nil
				}
				if !selected {
					continue
				}
				matches++
				validation.Each = false
				validation.Value = candidate
				if err := validateRuntimeRule(validation, state, resolve); err != nil {
					return err
				}
			}
			if matches < validation.MinMatches {
				return fmt.Errorf("line %d: descendants matched %d values, fewer than the required %d", validation.Line, matches, validation.MinMatches)
			}
		} else if err := validateRuntimeRule(validation, state, resolve); err != nil {
			return err
		}
	}
	if err := validateRuntimeBundleCustomResources(bundle.Artifacts); err != nil {
		return err
	}
	for index := range bundle.Artifacts {
		artifact := &bundle.Artifacts[index]
		if err := validateBuiltinKubernetesResource(artifact.Value); err != nil {
			metadata := artifact.Value.Metadata()

			return sourceDiagnostic(metadata.SourcePath, metadata.Span, fmt.Errorf("emitted value %d: %w", index+1, err))
		}
	}
	_, err = indexRuntimeBundleNodes(bundle, state)

	return err
}

func resolveRuntimeCallContinuation(continuation *runtimeCallContinuation, state *EvaluationState, resolve func(*RuntimeValue) error) (RuntimeValue, error) {
	callee := continuation.Callee
	if err := resolve(&callee); err != nil {
		return RuntimeValue{}, err
	}
	arguments := slices.Clone(continuation.Arguments)
	for index := range arguments {
		if err := resolve(&arguments[index]); err != nil {
			return RuntimeValue{}, err
		}
	}
	var result RuntimeValue
	var err error
	if callee.Lambda != nil {
		result, err = callRuntimeLambda(callee, arguments, continuation.Names, state)
	} else if callee.Kind() == Function {
		callState := *state
		if callee.ModuleContext != nil {
			callState.Module = callee.ModuleContext
		}
		result, err = callRuntimeStandard(callee.Value.Text, arguments, continuation.Names, &callState)
	} else {
		return RuntimeValue{}, typeDiagnostic(fmt.Errorf("cannot call a %s value", kindName(callee.Kind())))
	}
	if err != nil {
		return RuntimeValue{}, err
	}
	if err := resolve(&result); err != nil {
		return RuntimeValue{}, err
	}

	return result, nil
}

func resolveRuntimePredicateContinuation(continuation *runtimePredicateContinuation, state *EvaluationState, resolve func(*RuntimeValue) error) (RuntimeValue, error) {
	if len(continuation.Arguments) != 2 {
		return RuntimeValue{}, errors.New("predicate continuation has invalid arguments")
	}
	items, ok := runtimeListValue(continuation.Arguments[0])
	if !ok {
		return RuntimeValue{}, errors.New("predicate continuation expects a List")
	}
	result := RuntimeValue{Value: runtimeListResultMetadata(continuation.Arguments[0]), List: make([]RuntimeValue, 0, len(items.List))}
	matches := 0
	var match RuntimeValue
	for index, item := range items.List {
		if err := state.Budget.consumeIteration(); err != nil {
			return RuntimeValue{}, err
		}
		predicate, err := callRuntimeCallable(continuation.Arguments[1], []RuntimeValue{item}, state)
		if err != nil {
			return RuntimeValue{}, fmt.Errorf("%s item %d: %w", continuation.Name, index, err)
		}
		if err := resolve(&predicate); err != nil {
			return RuntimeValue{}, err
		}
		if predicate.Kind() != Bool {
			return RuntimeValue{}, fmt.Errorf("%s predicate for item %d returned %s, expected Bool", continuation.Name, index, kindName(predicate.Kind()))
		}
		if !predicate.BoolValue() {
			continue
		}
		match = item
		matches++
		if continuation.Name == fnFilter {
			result.List = append(result.List, item)
		}
	}
	if continuation.Name == fnExactlyOne {
		if matches != 1 {
			return RuntimeValue{}, fmt.Errorf("exactlyOne found %d matching values, expected exactly one", matches)
		}

		return match, nil
	}

	return result, nil
}

func resolveTypedBinaryContinuation(continuation *typedBinaryContinuation, state *EvaluationState, resolve func(*RuntimeValue) error) (RuntimeValue, error) {
	left := continuation.Left
	if err := resolve(&left); err != nil {
		return RuntimeValue{}, err
	}
	if (continuation.Operator == typedOperatorAnd || continuation.Operator == typedOperatorOr) && left.Kind() != Bool {
		return RuntimeValue{}, typeDiagnostic(fmt.Errorf("%s expects Bool operands", continuation.Operator.token()))
	}
	shortCircuits := continuation.Operator == typedOperatorAnd && left.Kind() == Bool && !left.BoolValue() ||
		continuation.Operator == typedOperatorOr && left.Kind() == Bool && left.BoolValue() ||
		continuation.Operator == typedOperatorCoalesce && left.Kind() != Null
	if shortCircuits {
		return left, nil
	}
	right := continuation.Right
	if continuation.RightExpression != nil {
		var err error
		right, err = evaluateTypedContinuation(&typedContinuation{Expression: *continuation.RightExpression, Scope: continuation.Scope}, state)
		if err != nil {
			return RuntimeValue{}, err
		}
	}
	if err := resolve(&right); err != nil {
		return RuntimeValue{}, err
	}
	result, err := applyRuntimeTypedBinary(continuation.Operator, left, right)
	if err != nil {
		return RuntimeValue{}, err
	}

	return result, nil
}

func resolveTypedConditionalContinuation(continuation *typedConditionalContinuation, state *EvaluationState, resolve func(*RuntimeValue) error) (RuntimeValue, error) {
	condition := continuation.Condition
	if err := resolve(&condition); err != nil {
		return RuntimeValue{}, err
	}
	if condition.Kind() != Bool {
		return RuntimeValue{}, typeDiagnostic(fmt.Errorf("if condition must be Bool, got %s", kindName(condition.Kind())))
	}
	branch := continuation.ElseBranch
	if condition.BoolValue() {
		branch = continuation.ThenBranch
	}
	resolved, err := evaluateTypedContinuation(&typedContinuation{Expression: branch, Scope: continuation.Scope}, state)
	if err != nil {
		return RuntimeValue{}, err
	}
	if err := resolve(&resolved); err != nil {
		return RuntimeValue{}, err
	}

	return resolved, nil
}

func resolveTypedUnaryContinuation(continuation *typedUnaryContinuation, resolve func(*RuntimeValue) error) (RuntimeValue, error) {
	operand := continuation.Operand
	if err := resolve(&operand); err != nil {
		return RuntimeValue{}, err
	}
	if operand.Kind() != Bool {
		return RuntimeValue{}, typeDiagnostic(fmt.Errorf("not expects Bool, got %s", kindName(operand.Kind())))
	}
	result := RuntimeValue{Value: runtimeValueData{Kind: Bool, Bool: !operand.BoolValue()}}

	return runtimeValueWithExpressionSources(result, continuation.SourcePath, continuation.Span, operand), nil
}

func resolveTypedIndexContinuation(continuation *typedIndexContinuation, resolve func(*RuntimeValue) error) (RuntimeValue, error) {
	base, key := continuation.Base, continuation.Key
	if err := resolve(&base); err != nil {
		return RuntimeValue{}, err
	}
	if err := resolve(&key); err != nil {
		return RuntimeValue{}, err
	}

	indexed, err := applyRuntimeIndexAccess(base, key)
	if err != nil {
		return RuntimeValue{}, err
	}

	return runtimeValueWithExpressionSources(indexed, continuation.SourcePath, continuation.Span, base, key), nil
}

func resolveTypedForContinuation(continuation *typedForContinuation, state *EvaluationState, resolve func(*RuntimeValue) error) (RuntimeValue, error) {
	source := continuation.Source
	if err := resolve(&source); err != nil {
		return RuntimeValue{}, err
	}
	items, isList := runtimeListValue(source)
	if !isList {
		return RuntimeValue{}, typeDiagnostic(fmt.Errorf("for expression expects a List, got %s", kindName(source.Kind())))
	}
	result := RuntimeValue{Value: runtimeValueData{Kind: List, Span: continuation.Span, SourcePath: continuation.SourcePath}, List: make([]RuntimeValue, 0, len(items.List))}
	for _, item := range items.List {
		if err := state.Budget.consumeIteration(); err != nil {
			return RuntimeValue{}, err
		}
		environment := maps.Clone(continuation.Scope)
		if environment == nil {
			environment = make(map[string]RuntimeValue)
		}
		environment[continuation.LoopName] = item
		value, err := evaluateTypedContinuation(&typedContinuation{Expression: continuation.Body, Scope: environment}, state)
		if err != nil {
			return RuntimeValue{}, err
		}
		if err := resolve(&value); err != nil {
			return RuntimeValue{}, err
		}
		result.List = append(result.List, value)
	}

	return runtimeValueWithExpressionSources(result, continuation.SourcePath, continuation.Span, source), nil
}

func resolveTypedOverlayContinuation(continuation *typedOverlayContinuation, state *EvaluationState, resolve func(*RuntimeValue) error) (RuntimeValue, error) {
	base, patch := continuation.Base, continuation.Patch
	if err := resolve(&base); err != nil {
		return RuntimeValue{}, err
	}
	if err := resolve(&patch); err != nil {
		return RuntimeValue{}, err
	}
	merged, err := mergeRuntimeValues(base, patch)
	if err != nil {
		return RuntimeValue{}, err
	}
	if err := state.Allocations.chargeValueGraph(merged); err != nil {
		return RuntimeValue{}, err
	}

	return runtimeValueWithExpressionSources(merged, continuation.SourcePath, continuation.Span, base, patch), nil
}

func resolveTypedRemovalContinuation(continuation *typedRemovalContinuation, state *EvaluationState, resolve func(*RuntimeValue) error) (RuntimeValue, error) {
	value := continuation.Value
	if err := resolve(&value); err != nil {
		return RuntimeValue{}, err
	}
	var err error
	for _, path := range continuation.Paths {
		value, err = removeRuntimePath(value, strings.Split(strings.TrimPrefix(path, "."), "."))
		if err != nil {
			return RuntimeValue{}, fmt.Errorf("path %s: %w", path, err)
		}
	}
	if err := validateRuntimeInheritedConstraints(value); err != nil {
		return RuntimeValue{}, err
	}
	if err := state.Allocations.chargeValueGraph(value); err != nil {
		return RuntimeValue{}, err
	}

	return runtimeValueWithExpressionSources(value, continuation.SourcePath, continuation.Span, continuation.Value), nil
}

func unresolvedRuntimeNodeLocations(reference *runtimeReference) []SourceDiagnosticLocation {
	locations := make([]SourceDiagnosticLocation, 0, len(reference.Targets))
	labels := []string{"Referenced Node definition", "Referenced child Node definition"}
	for index, identity := range reference.Targets {
		if identity == nil || identity.originPath == "" || identity.originSpan.End <= identity.originSpan.Start {
			continue
		}
		locations = append(locations, SourceDiagnosticLocation{Path: identity.originPath, Span: identity.originSpan, Message: labels[min(index, len(labels)-1)]})
	}

	return locations
}

func runtimeValueTypeProvenance(value RuntimeValue, typeExpr ResolvedType, types map[string]ResolvedType) (bool, bool) {
	typing := value.Metadata().Typing
	if typing == nil || len(typing.TypeProvenance) == 0 {
		return false, false
	}
	for _, provenance := range typing.TypeProvenance {
		matches, compatible := staticTypeAssignable(provenance.Type, typeExpr, provenance.Types)
		if compatible && matches {
			return true, true
		}
		matches, compatible = staticTypeAssignable(provenance.Type, typeExpr, types)
		if compatible && matches {
			return true, true
		}
	}

	return true, false
}

func runtimeBundleDescendants(bundle RuntimeBundle) []RuntimeValue {
	values := make([]RuntimeValue, len(bundle.Artifacts))
	for index := range bundle.Artifacts {
		values[index] = bundle.Artifacts[index].Value
	}

	return runtimeDescendantValues(values)
}

func runtimeDescendantValues(values []RuntimeValue) []RuntimeValue {
	result := make([]RuntimeValue, 0)
	var visit func(RuntimeValue)
	visit = func(value RuntimeValue) {
		if value.Kind() == Node && value.Base != nil {
			result = append(result, value)
			visit(*value.Base)

			return
		}
		if value.Kind() == Map {
			result = append(result, value)
			for _, entry := range value.Map {
				visit(entry.Value)
			}

			return
		}
		if value.Kind() == List || value.Kind() == AdapterBundle {
			result = append(result, value)
			for _, item := range value.List {
				visit(item)
			}
		}
	}
	for _, value := range values {
		visit(value)
	}

	return result
}

func indexRuntimeBundleNodes(bundle *RuntimeBundle, state *EvaluationState) (map[*NodeIdentity]runtimeNodeRecord, error) {
	nodes := make(map[*NodeIdentity]runtimeNodeRecord)
	count := 0
	var visit func(*RuntimeValue, []*NodeIdentity) error
	visit = func(value *RuntimeValue, ancestors []*NodeIdentity) error {
		count++
		if count > maxBundleValueNodes {
			path, span := value.Source()

			return sourceDiagnostic(path, span, fmt.Errorf("final Bundle value node limit exceeds %d", maxBundleValueNodes))
		}
		if err := state.Budget.consumeStep(); err != nil {
			path, span := value.Source()

			return sourceDiagnostic(path, span, err)
		}
		if value.Kind() == Node {
			identity := value.NodeIdentity()
			if identity == nil || value.Base == nil {
				return errors.New("invalid Node value")
			}
			if _, exists := nodes[identity]; exists {
				return errors.New("the same Node appears more than once in the final output")
			}
			ancestors = append(slices.Clone(ancestors), identity)
			node := *value
			nodes[identity] = runtimeNodeRecord{value: &node, ancestors: ancestors}

			return visit(value.Base, ancestors)
		}
		//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
		switch value.Kind() {
		case List, AdapterBundle:
			for index := range value.List {
				if err := visit(&value.List[index], ancestors); err != nil {
					return fmt.Errorf("item %d: %w", index, err)
				}
			}
		case Map:
			for index := range value.Map {
				if err := visit(&value.Map[index].Value, ancestors); err != nil {
					return fmt.Errorf("field %q: %w", value.Map[index].Key, err)
				}
			}
		}

		return nil
	}
	for index := range bundle.Artifacts {
		if err := visit(&bundle.Artifacts[index].Value, nil); err != nil {
			return nil, fmt.Errorf("emitted value %d: %w", index+1, err)
		}
	}

	return nodes, nil
}

func resolveRuntimeNodeReference(reference *runtimeReference, state *EvaluationState, resolve func(*RuntimeValue) error, resolveKeys func(*RuntimeValue) error) (RuntimeValue, error) {
	if len(reference.Targets) < 1 || len(reference.Targets) > 2 {
		return RuntimeValue{}, errors.New("ref expects a Node and an optional child Node")
	}
	root, exists := state.NodeIndex[reference.Targets[0]]
	if !exists {
		return RuntimeValue{}, errors.New("ref target is not present in the final output")
	}
	target := root
	if len(reference.Targets) == 2 {
		child, exists := state.NodeIndex[reference.Targets[1]]
		if !exists || !slices.Contains(child.ancestors, reference.Targets[0]) {
			return RuntimeValue{}, errors.New("ref child Node is not present inside its root Node")
		}
		target = child
	}
	selected := target.value.Base
	if len(reference.Path) == 0 {
		key := activeReference{node: target.value.NodeIdentity()}
		if state.ActiveReferences[key] {
			return RuntimeValue{}, errors.New("cyclic reference involving a Node")
		}
		state.ActiveReferences[key] = true
		defer delete(state.ActiveReferences, key)
		if err := resolve(selected); err != nil {
			return RuntimeValue{}, err
		}

		return *selected, nil
	}
	for index, field := range reference.Path {
		for selected != nil && selected.Kind() == Node && selected.Base != nil {
			selected = selected.Base
		}
		if selected == nil || selected.Kind() != Map {
			return RuntimeValue{}, fmt.Errorf("cannot select ref field %q from a non-map value", field)
		}
		if err := resolveKeys(selected); err != nil {
			return RuntimeValue{}, err
		}
		found := false
		for entryIndex := range selected.Map {
			if selected.Map[entryIndex].Key == field {
				selected = &selected.Map[entryIndex].Value
				found = true

				break
			}
		}
		if !found {
			for entryIndex := range selected.Map {
				if selected.Map[entryIndex].KeyExpression != nil {
					return RuntimeValue{}, errors.New("cyclic reference involving a dynamic map key")
				}
			}

			return RuntimeValue{}, fmt.Errorf("ref field %q does not exist", field)
		}
		key := activeReference{node: target.value.NodeIdentity(), path: referencePathKey(reference.Path[:index+1])}
		if state.ActiveReferences[key] {
			return RuntimeValue{}, errors.New("cyclic reference involving a Node")
		}
		state.ActiveReferences[key] = true
		if err := resolve(selected); err != nil {
			delete(state.ActiveReferences, key)

			return RuntimeValue{}, err
		}
		delete(state.ActiveReferences, key)
	}

	return *selected, nil
}

func rejectRuntimeRuleValues(value RuntimeValue) error {
	if value.Kind() == Rule || value.Rule != nil || value.IsRuleResult() || value.Deferred != nil {
		path, span := value.Source()

		return sourceDiagnostic(path, span, errors.New("rule values cannot be emitted"))
	}
	if value.Kind() == Node && value.Base != nil {
		return rejectRuntimeRuleValues(*value.Base)
	}
	for index, item := range value.List {
		if err := rejectRuntimeRuleValues(item); err != nil {
			return fmt.Errorf("item %d: %w", index, err)
		}
	}
	for _, entry := range value.Map {
		if err := rejectRuntimeRuleValues(entry.Value); err != nil {
			return fmt.Errorf("field %q: %w", entry.Key, err)
		}
	}

	return nil
}

func validateRuntimeRule(validation RuntimeValidation, state *EvaluationState, resolve func(*RuntimeValue) error) error {
	if len(validation.Rule.ParameterDefinitions) != 1 {
		return fmt.Errorf("rule %q must declare exactly one parameter", validation.Rule.Name)
	}
	failures, err := invokeRuntimeRule(validation.Rule, validation.Value, state, resolve)
	if err != nil {
		return fmt.Errorf("line %d: %w", validation.Line, err)
	}
	if len(failures) == 0 {
		return nil
	}
	diagnostics := make([]error, len(failures))
	for index, failure := range failures {
		related := []SourceDiagnosticLocation{{Path: failure.Path, Span: failure.Span, Message: fmt.Sprintf("Rule %q requirement: %s", failure.Rule, failure.Expression)}}
		message := fmt.Sprintf("rule %q requirement %q failed; actual %s", failure.Rule, failure.Expression, failure.Actual)
		if failure.Message != "" {
			message = fmt.Sprintf("rule %q: %s; actual %s", failure.Rule, failure.Message, failure.Actual)
		}
		if failure.Target != "" {
			message += "; target " + failure.Target
		}
		path, span := validation.Value.Source()
		if span.End <= span.Start {
			path, span = failure.Path, failure.Span
		}
		diagnostics[index] = SourceDiagnosticError{Path: path, Code: "E501", Message: message, Span: span, Related: related, Cause: errors.New(message)}
	}

	return errors.Join(diagnostics...)
}

func invokeRuntimeRule(rule RuntimeRule, value RuntimeValue, state *EvaluationState, resolve func(*RuntimeValue) error) ([]RuleFailure, error) {
	leave, err := state.Budget.enterCall()
	if err != nil {
		path, span := value.Source()

		return nil, sourceDiagnostic(path, span, err)
	}
	defer leave()
	if len(rule.ParameterDefinitions) != 1 {
		return nil, fmt.Errorf("rule %q must declare exactly one parameter", rule.Name)
	}
	if rule.Identity != nil {
		if state.ActiveRules[rule.Identity] {
			return nil, fmt.Errorf("cyclic Rule call involving %q", rule.Name)
		}
		state.ActiveRules[rule.Identity] = true
		defer delete(state.ActiveRules, rule.Identity)
	}
	if err := resolve(&value); err != nil {
		return nil, fmt.Errorf("resolve Rule target for %q: %w", rule.Name, err)
	}
	parameter := rule.ParameterDefinitions[0]
	if err := checkRuntimeType(value, parameter.Type, rule.Types, make(map[string]bool)); err != nil {
		return nil, fmt.Errorf("rule %q argument %q does not match its type: %w", rule.Name, parameter.Name, err)
	}
	child := *state
	child.Environment = maps.Clone(rule.Closure)
	if child.Environment == nil {
		child.Environment = make(map[string]RuntimeValue)
	}
	child.Environment[parameter.Name] = value
	child.StaticTypes = maps.Clone(rule.StaticTypes)
	child.StaticTypes[parameter.Name] = TypeAnalysis{Type: parameter.Type}
	child.Types = maps.Clone(rule.Types)
	if rule.ModuleContext != nil {
		child.Module = rule.ModuleContext
	}
	var failures []RuleFailure
	for _, requirement := range rule.Requirements {
		child.SourcePath = requirement.SourcePath
		condition, actual, err := evaluateRuntimeRuleRequirement(requirement.Expression, &child, resolve)
		if err != nil {
			return nil, fmt.Errorf("line %d: rule %q require %q: %w", requirement.Line, rule.Name, requirement.Source, err)
		}
		if condition.IsRuleResult() {
			failures = append(failures, condition.RuleResult.Failures...)

			continue
		}
		if condition.Kind() != Bool {
			return nil, typeDiagnostic(fmt.Errorf("line %d: rule %q require %q must be Bool, got %s", requirement.Line, rule.Name, requirement.Source, kindName(condition.Kind())))
		}
		if !condition.Value.Bool {
			if actual == "" {
				actual = "require result: false"
			}
			failures = append(failures, RuleFailure{
				Rule: rule.Name, Expression: requirement.Source, Message: requirement.Message, Line: requirement.Line,
				Path: requirement.SourcePath, Span: requirement.Span, Actual: actual,
				Target: runtimeKubernetesResourceTarget(value),
			})
		}
	}

	return failures, nil
}

func evaluateRuntimeRuleRequirement(expression typedExpression, state *EvaluationState, resolve func(*RuntimeValue) error) (RuntimeValue, string, error) {
	if expression.kind != typedExpressionBinary || !isBooleanComparisonOperator(expression.operator) || expression.left == nil || expression.right == nil {
		condition, err := evaluateTypedExpressionRuntime(expression, state)
		if err != nil {
			return RuntimeValue{}, "", err
		}
		if err := resolve(&condition); err != nil {
			return RuntimeValue{}, "", err
		}

		return condition, "", nil
	}
	leftIsLiteral := isTypedScalarLiteral(*expression.left)
	context := typedEvaluationContext{
		runtimeEnv: state.Environment, staticTypes: state.StaticTypes, types: state.Types,
		stack: state.Stack, budget: state.Budget, sourcePath: state.SourcePath, module: state.Module,
	}
	if err := runtimeAllocationBudgetOf(state).consume(1); err != nil {
		return RuntimeValue{}, "", sourceDiagnostic(state.SourcePath, expression.span, err)
	}
	if err := state.Budget.consumeStep(); err != nil {
		return RuntimeValue{}, "", sourceDiagnostic(state.SourcePath, expression.span, err)
	}
	left, err := expression.left.evaluate(context)
	if err != nil {
		return RuntimeValue{}, "", err
	}
	if err := resolve(&left); err != nil {
		return RuntimeValue{}, "", err
	}
	right, err := expression.right.evaluate(context)
	if err != nil {
		return RuntimeValue{}, "", err
	}
	if err := resolve(&right); err != nil {
		return RuntimeValue{}, "", err
	}
	condition, err := applyRuntimeTypedBinary(expression.operator, left, right)
	if err != nil {
		return RuntimeValue{}, "", sourceDiagnostic(state.SourcePath, expression.span, err)
	}
	if err := resolve(&condition); err != nil {
		return RuntimeValue{}, "", err
	}
	actual := left
	if leftIsLiteral {
		actual = right
	}

	return condition, formatRuntimeDiagnosticValue(actual), nil
}

func isTypedScalarLiteral(expression typedExpression) bool {
	switch expression.kind {
	case typedExpressionNull, typedExpressionBool, typedExpressionInteger, typedExpressionDecimal, typedExpressionString:
		return true
	default:
		return false
	}
}

func runtimeKubernetesResourceTarget(value RuntimeValue) string {
	value = runtimeNodeContents(value)
	apiVersion, hasAPIVersion := runtimeStringField(value, "apiVersion")
	kind, hasKind := runtimeStringField(value, "kind")
	metadata, hasMetadata := value.MapLookup("metadata")
	if !hasAPIVersion || !hasKind || !hasMetadata {
		return ""
	}
	name, hasName := runtimeStringField(metadata, "name")
	if !hasName || name == "" {
		return ""
	}
	group, version, hasGroup := strings.Cut(apiVersion, "/")
	if !hasGroup {
		group = ""
		version = apiVersion
	} else if group == "" || strings.Contains(version, "/") {
		return ""
	}
	if !validKubernetesAPIVersion(version) {
		return ""
	}
	namespace := ""
	if namespaceValue, exists := runtimeNodeContents(metadata).MapLookup("namespace"); exists {
		if namespaceValue.Kind() != String {
			return ""
		}
		namespace = namespaceValue.TextValue()
	}
	if descriptor, known := builtinKubernetesResourceDescriptors[kubernetesResourceKey{group: group, kind: kind}]; known && descriptor.scope == kubernetesResourceCluster {
		namespace = ""
	}
	if namespace == "" {
		return apiVersion + "/" + kind + "/" + name
	}

	return apiVersion + "/" + kind + "/" + namespace + "/" + name
}

func formatRuntimeDiagnosticValue(value RuntimeValue) string {
	node, err := runtimeValueToYAMLNode(value)
	if err != nil {
		return kindName(value.Kind()) + " value"
	}
	data, err := yaml.Marshal(node)
	if err != nil {
		return kindName(value.Kind()) + " value"
	}

	return strings.TrimSpace(string(data))
}

func referencePathKey(path []string) string {
	var builder strings.Builder
	for _, field := range path {
		fmt.Fprintf(&builder, "%d:", len(field))
		builder.WriteString(field)
	}

	return builder.String()
}

func nodeContents(value Value) Value {
	for value.Kind == Node && value.Base != nil {
		value = *value.Base
	}

	return value
}

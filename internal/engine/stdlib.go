package engine

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type builtin struct {
	name            string
	module          string
	exportName      string
	parameters      []string
	inference       builtinInference
	evaluation      builtinEvaluation
	staticInference func([]Expr, []ResolvedType, []bool, map[string]ResolvedType) (ResolvedType, bool, error)
	pureEvaluation  func([]RuntimeValue) (RuntimeValue, error)
	deferred        builtinDeferredEvaluation
}

// StandardLibraryFunction describes the exported names registered for a standard-library function.
type StandardLibraryFunction struct {
	Module     string
	Name       string
	Parameters []string
}

// StandardLibraryFunctions returns the exported standard-library function names and parameters.
func StandardLibraryFunctions() []StandardLibraryFunction {
	functions := make([]StandardLibraryFunction, 0, len(builtinRegistry))
	for _, item := range builtinRegistry {
		functions = append(functions, StandardLibraryFunction{
			Module: item.module, Name: item.exportName, Parameters: append([]string(nil), item.parameters...),
		})
	}

	return functions
}

const stdlibListModule = "std:list"

type builtinInference uint8

const (
	builtinInferenceStatic builtinInference = iota
	builtinInferenceHigherOrder
	builtinInferenceAdapter
)

type builtinEvaluation uint8

const (
	builtinEvaluationPure builtinEvaluation = iota
	builtinEvaluationHigherOrder
	builtinEvaluationAdapter
)

type builtinDeferredEvaluation uint8

const (
	builtinDeferredEvaluationStandard builtinDeferredEvaluation = iota
	builtinDeferredEvaluationClone
	builtinDeferredEvaluationPredicate
)

var builtinRegistry = []builtin{
	{name: fnLen, module: "std:core", exportName: fnLen, parameters: []string{paramValue}, inference: builtinInferenceStatic, evaluation: builtinEvaluationPure, staticInference: inferStaticLenBuiltin, pureEvaluation: evaluateLenBuiltin},
	{name: fnReplace, module: "std:core", exportName: fnReplace, parameters: []string{paramValue}, inference: builtinInferenceStatic, evaluation: builtinEvaluationPure, staticInference: inferStaticReplaceBuiltin, pureEvaluation: evaluateReplaceBuiltin},
	{name: fnClone, module: "std:core", exportName: fnClone, parameters: []string{paramValue}, inference: builtinInferenceStatic, evaluation: builtinEvaluationPure, staticInference: inferStaticCloneBuiltin, pureEvaluation: evaluateCloneBuiltin, deferred: builtinDeferredEvaluationClone},
	{name: fnContains, module: stdlibListModule, exportName: fnContains, parameters: []string{paramValues, paramValue}, inference: builtinInferenceStatic, evaluation: builtinEvaluationPure, staticInference: inferStaticContainsBuiltin, pureEvaluation: evaluateContainsBuiltin},
	{name: fnConcat, module: stdlibListModule, exportName: fnConcat, parameters: []string{"first", "second"}, inference: builtinInferenceStatic, evaluation: builtinEvaluationPure, staticInference: inferStaticConcatBuiltin, pureEvaluation: evaluateConcatBuiltin},
	{name: fnUnion, module: stdlibListModule, exportName: fnUnion, parameters: []string{"first", "second"}, inference: builtinInferenceStatic, evaluation: builtinEvaluationPure, staticInference: inferStaticUnionBuiltin, pureEvaluation: evaluateUnionBuiltin},
	{name: fnReplaceAt, module: stdlibListModule, exportName: fnReplaceAt, parameters: []string{paramValues, paramIndex, paramValue}, inference: builtinInferenceStatic, evaluation: builtinEvaluationPure, staticInference: inferStaticReplaceAtBuiltin, pureEvaluation: evaluateReplaceAtBuiltin},
	{name: fnMergeByKey, module: stdlibListModule, exportName: fnMergeByKey, parameters: []string{"existing", "overrides", paramKey}, inference: builtinInferenceStatic, evaluation: builtinEvaluationPure, staticInference: inferStaticMergeByKeyBuiltin, pureEvaluation: evaluateMergeByKeyBuiltin},
	{name: fnMap, module: stdlibListModule, exportName: fnMap, parameters: []string{paramValues, "transform"}, inference: builtinInferenceHigherOrder, evaluation: builtinEvaluationHigherOrder},
	{name: fnFilter, module: stdlibListModule, exportName: fnFilter, parameters: []string{paramValues, "predicate"}, inference: builtinInferenceHigherOrder, evaluation: builtinEvaluationHigherOrder, deferred: builtinDeferredEvaluationPredicate},
	{name: fnExactlyOne, module: stdlibListModule, exportName: fnExactlyOne, parameters: []string{paramValues, "predicate"}, inference: builtinInferenceHigherOrder, evaluation: builtinEvaluationHigherOrder, deferred: builtinDeferredEvaluationPredicate},
	{name: fnFold, module: stdlibListModule, exportName: fnFold, parameters: []string{paramValues, "initial", "combine"}, inference: builtinInferenceHigherOrder, evaluation: builtinEvaluationHigherOrder},
	{name: fnKeyOf, module: "std:map", exportName: fnKeyOf, parameters: []string{"mapping", paramKey}, inference: builtinInferenceStatic, evaluation: builtinEvaluationPure, staticInference: inferStaticKeyOfBuiltin, pureEvaluation: evaluateKeyOfBuiltin},
	{name: fnKustomizeBuild, module: "std:kustomize", exportName: "build", parameters: []string{paramSource, "revision", "directory"}, inference: builtinInferenceAdapter, evaluation: builtinEvaluationAdapter},
	{name: fnManifestLoad, module: "std:manifest", exportName: "load", parameters: []string{paramSource}, inference: builtinInferenceAdapter, evaluation: builtinEvaluationAdapter},
	{name: fnHelmRender, module: "std:helm", exportName: "render", parameters: []string{paramSource, "options"}, inference: builtinInferenceAdapter, evaluation: builtinEvaluationAdapter},
	{name: fnLifecycleAttach, module: "std:lifecycle", exportName: "attach", parameters: []string{paramValue, "events", "priority", "wait", "cleanup"}, inference: builtinInferenceStatic, evaluation: builtinEvaluationPure, staticInference: inferStaticLifecycleAttachBuiltin, pureEvaluation: lifecycleAttach},
}

var standardFunctions = func() map[string]struct{} {
	functions := make(map[string]struct{}, len(builtinRegistry))
	for _, item := range builtinRegistry {
		functions[item.name] = struct{}{}
	}

	return functions
}()

var standardFunctionParameters = func() map[string][]string {
	parameters := make(map[string][]string, len(builtinRegistry))
	for _, item := range builtinRegistry {
		parameters[item.name] = item.parameters
	}

	return parameters
}()

func builtinByName(name string) (builtin, bool) {
	for _, item := range builtinRegistry {
		if item.name == name {
			return item, true
		}
	}

	return builtin{}, false
}

func arrangeRuntimeArguments(function string, arguments []RuntimeValue, names []string) ([]RuntimeValue, error) {
	parameters := standardFunctionParameters[function]
	if len(names) == 0 {
		return arguments, nil
	}
	if len(names) != len(arguments) {
		return nil, fmt.Errorf("invalid argument metadata for %s", function)
	}
	ordered := make([]RuntimeValue, len(parameters))
	assigned := make([]bool, len(parameters))
	position := 0
	for index, name := range names {
		if name == "" {
			for position < len(assigned) && assigned[position] {
				position++
			}
			if position >= len(ordered) {
				return nil, fmt.Errorf("too many positional arguments for %s", function)
			}
			ordered[position] = arguments[index]
			assigned[position] = true
			position++

			continue
		}
		parameter := slices.Index(parameters, name)
		if parameter < 0 {
			return nil, fmt.Errorf("%s has no argument named %q", function, name)
		}
		if assigned[parameter] {
			return nil, fmt.Errorf("argument %q for %s was provided more than once", name, function)
		}
		ordered[parameter] = arguments[index]
		assigned[parameter] = true
	}
	if slices.Contains(assigned, false) {
		return nil, fmt.Errorf("%s expects %d arguments", function, len(parameters))
	}

	return ordered, nil
}

func standardRuntimeLibrary(path string) ([]RuntimeEntry, bool) {
	entries := make([]RuntimeEntry, 0)
	knownModule := false
	for _, item := range builtinRegistry {
		if item.module != path {
			continue
		}
		knownModule = true
		entries = append(entries, RuntimeEntry{Key: item.exportName, Value: RuntimeValue{Value: runtimeValueData{Kind: Function, Text: item.name}}})
	}

	return entries, knownModule
}

func callRuntimeStandard(name string, arguments []RuntimeValue, names []string, state *EvaluationState) (RuntimeValue, error) {
	item, exists := builtinByName(name)
	if !exists {
		return RuntimeValue{}, fmt.Errorf("unknown standard function %q", name)
	}
	ordered, err := arrangeRuntimeArguments(name, arguments, names)
	if err != nil {
		return RuntimeValue{}, err
	}
	if item.pureEvaluation != nil {
		if name == fnClone {
			if len(ordered) != 1 {
				return RuntimeValue{}, fmt.Errorf("%s expects 1 arguments", name)
			}
			identities := make(map[*NodeIdentity]*NodeIdentity)

			return cloneRuntimeValueGraphWithBudget(ordered[0], identities, runtimeAllocationBudgetOf(state))
		}
		value, err := item.pureEvaluation(ordered)
		if err == nil && (value.Kind() == List || value.Kind() == Map || value.Kind() == AdapterBundle) {
			err = runtimeAllocationBudgetOf(state).chargeValueGraph(value)
		}

		return value, err
	}
	if item.evaluation == builtinEvaluationAdapter {
		return callRuntimeAdapter(name, ordered, state)
	}
	if state == nil {
		return RuntimeValue{}, errors.New("evaluation state is nil")
	}
	value, err := callRuntimeHigherOrder(name, ordered, state)
	if err == nil && (value.Kind() == List || value.Kind() == Map || value.Kind() == AdapterBundle) {
		err = runtimeAllocationBudgetOf(state).chargeValueGraph(value)
	}

	return value, err
}

func runtimeAllocationBudgetOf(state *EvaluationState) *runtimeAllocationBudget {
	if state == nil {
		return nil
	}
	if state.Allocations == nil {
		state.Allocations = &runtimeAllocationBudget{}
	}

	return state.Allocations
}

func callRuntimeAdapter(name string, arguments []RuntimeValue, state *EvaluationState) (RuntimeValue, error) {
	if state == nil || state.Module == nil {
		return RuntimeValue{}, errors.New("adapter evaluation requires module context")
	}
	switch name {
	case fnKustomizeBuild:
		return kustomizeBuild(arguments, state)
	case fnManifestLoad:
		return manifestLoad(arguments, state)
	case fnHelmRender:
		return helmRender(arguments, state)
	default:
		return RuntimeValue{}, fmt.Errorf("unknown adapter function %q", name)
	}
}

func callRuntimeHigherOrder(name string, arguments []RuntimeValue, state *EvaluationState) (RuntimeValue, error) {
	if len(arguments) == 0 {
		return RuntimeValue{}, fmt.Errorf("%s expects a List as its first argument", name)
	}
	items, isList := runtimeListValue(arguments[0])
	if !isList {
		return RuntimeValue{}, fmt.Errorf("%s expects a List as its first argument", name)
	}
	if name == fnMap || name == fnFilter || name == fnExactlyOne {
		if len(arguments) != 2 || !isRuntimeCallable(arguments[1]) {
			return RuntimeValue{}, fmt.Errorf("%s expects a List and a predicate", name)
		}
		if name == fnExactlyOne {
			var match RuntimeValue
			matches := 0
			for index, value := range items.List {
				if err := state.Budget.consumeIteration(); err != nil {
					path, span := arguments[0].Source()

					return RuntimeValue{}, sourceDiagnostic(path, span, err)
				}
				predicate, err := callRuntimeCallable(arguments[1], []RuntimeValue{value}, state)
				if err != nil {
					return RuntimeValue{}, fmt.Errorf("exactlyOne item %d: %w", index, err)
				}
				if predicate.IsDeferred() {
					return deferredRuntimePredicate(name, arguments), nil
				}
				if predicate.Kind() != Bool {
					return RuntimeValue{}, fmt.Errorf("exactlyOne predicate for item %d returned %s, expected Bool", index, kindName(predicate.Kind()))
				}
				if predicate.Value.Bool {
					match, matches = value, matches+1
				}
			}
			if matches != 1 {
				return RuntimeValue{}, fmt.Errorf("exactlyOne found %d matching values, expected exactly one", matches)
			}

			return match, nil
		}
		result := RuntimeValue{Value: runtimeListResultMetadata(arguments[0]), List: make([]RuntimeValue, 0, len(items.List))}
		for index, value := range items.List {
			if err := state.Budget.consumeIteration(); err != nil {
				path, span := arguments[0].Source()

				return RuntimeValue{}, sourceDiagnostic(path, span, err)
			}
			transformed, err := callRuntimeCallable(arguments[1], []RuntimeValue{value}, state)
			if err != nil {
				return RuntimeValue{}, fmt.Errorf("%s item %d: %w", name, index, err)
			}
			if name == fnMap {
				result.List = append(result.List, transformed)

				continue
			}
			if transformed.IsDeferred() {
				return deferredRuntimePredicate(name, arguments), nil
			}
			if transformed.Kind() != Bool {
				return RuntimeValue{}, fmt.Errorf("filter predicate for item %d returned %s, expected Bool", index, kindName(transformed.Kind()))
			}
			if transformed.Value.Bool {
				result.List = append(result.List, value)
			}
		}

		return result, nil
	}
	if name != fnFold || len(arguments) != 3 || !isRuntimeCallable(arguments[2]) {
		return RuntimeValue{}, errors.New("fold expects a List, an initial value, and a lambda")
	}
	accumulator := arguments[1]
	for index, value := range items.List {
		if err := state.Budget.consumeIteration(); err != nil {
			path, span := arguments[0].Source()

			return RuntimeValue{}, sourceDiagnostic(path, span, err)
		}
		var err error
		accumulator, err = callRuntimeCallable(arguments[2], []RuntimeValue{accumulator, value}, state)
		if err != nil {
			return RuntimeValue{}, fmt.Errorf("fold item %d: %w", index, err)
		}
	}

	return accumulator, nil
}

func deferredRuntimePredicate(name string, arguments []RuntimeValue) RuntimeValue {
	return RuntimeValue{
		Value: runtimeValueData{Kind: Null},
		Deferred: &typedContinuation{Predicate: &runtimePredicateContinuation{
			Name: name, Arguments: slices.Clone(arguments),
		}},
	}
}

func isRuntimeCallable(value RuntimeValue) bool {
	return value.Lambda != nil || value.Kind() == Function
}

func callRuntimeCallable(function RuntimeValue, arguments []RuntimeValue, state *EvaluationState) (RuntimeValue, error) {
	if function.Lambda != nil {
		return callRuntimeLambda(function, arguments, nil, state)
	}
	if function.Kind() != Function {
		return RuntimeValue{}, fmt.Errorf("cannot call a %s value", kindName(function.Kind()))
	}
	if function.ModuleContext != nil {
		if state == nil {
			return RuntimeValue{}, errors.New("evaluation state is nil")
		}
		moduleState := *state
		moduleState.Module = function.ModuleContext
		state = &moduleState
	}

	return callRuntimeStandard(function.Value.Text, arguments, nil, state)
}

func evaluateMergeByKeyBuiltin(arguments []RuntimeValue) (RuntimeValue, error) {
	if len(arguments) != 3 {
		return RuntimeValue{}, fmt.Errorf("%s expects 3 arguments", fnMergeByKey)
	}

	return mergeByKey(arguments[0], arguments[1], arguments[2])
}

func evaluateKeyOfBuiltin(arguments []RuntimeValue) (RuntimeValue, error) {
	if len(arguments) != 2 {
		return RuntimeValue{}, fmt.Errorf("%s expects 2 arguments", fnKeyOf)
	}
	mapping, key := arguments[0], arguments[1]
	if mapping.Kind() != Map || key.Kind() != String {
		return RuntimeValue{}, errors.New("keyOf expects a Map and a String")
	}
	for _, entry := range mapping.Entries() {
		if entry.Key == key.Value.Text {
			return key, nil
		}
	}
	available := make([]string, 0, len(mapping.Entries()))
	for _, entry := range mapping.Entries() {
		available = append(available, entry.Key)
	}

	return RuntimeValue{}, fmt.Errorf("key %q does not exist; available keys: %s", key.Value.Text, strings.Join(available, ", "))
}

func evaluateConcatBuiltin(arguments []RuntimeValue) (RuntimeValue, error) {
	return evaluateListCombinationBuiltin(fnConcat, arguments, false)
}

func evaluateUnionBuiltin(arguments []RuntimeValue) (RuntimeValue, error) {
	return evaluateListCombinationBuiltin(fnUnion, arguments, true)
}

func evaluateListCombinationBuiltin(name string, arguments []RuntimeValue, deduplicate bool) (RuntimeValue, error) {
	if len(arguments) != 2 {
		return RuntimeValue{}, fmt.Errorf("%s expects 2 arguments", name)
	}
	left, right := arguments[0], arguments[1]
	leftItems, leftIsList := runtimeListValue(left)
	rightItems, rightIsList := runtimeListValue(right)
	if !leftIsList || !rightIsList {
		return RuntimeValue{}, fmt.Errorf("%s expects two Lists", name)
	}
	result := RuntimeValue{Value: runtimeListResultMetadata(left), List: make([]RuntimeValue, 0, len(leftItems.List)+len(rightItems.List))}
	for _, item := range append(slices.Clone(leftItems.List), rightItems.List...) {
		if deduplicate && slices.ContainsFunc(result.List, func(existing RuntimeValue) bool { return equalRuntimeValues(existing, item) }) {
			continue
		}
		result.List = append(result.List, item)
	}

	return result, nil
}

func evaluateReplaceAtBuiltin(arguments []RuntimeValue) (RuntimeValue, error) {
	if len(arguments) != 3 {
		return RuntimeValue{}, fmt.Errorf("%s expects 3 arguments", fnReplaceAt)
	}
	list, index := arguments[0], arguments[1]
	items, isList := runtimeListValue(list)
	if !isList || index.Kind() != Integer {
		return RuntimeValue{}, errors.New("replaceAt expects a List and an Integer index")
	}
	position, err := strconv.Atoi(index.Value.Text)
	if err != nil || position < 0 || position >= len(items.List) {
		return RuntimeValue{}, fmt.Errorf("replaceAt index %q is out of range", index.Value.Text)
	}
	result := RuntimeValue{Value: runtimeListResultMetadata(list), List: slices.Clone(items.List)}
	result.List[position] = arguments[2]

	return result, nil
}

func evaluateCloneBuiltin(arguments []RuntimeValue) (RuntimeValue, error) {
	if len(arguments) != 1 {
		return RuntimeValue{}, fmt.Errorf("%s expects 1 arguments", fnClone)
	}

	identities := make(map[*NodeIdentity]*NodeIdentity)

	return cloneRuntimeValueGraph(arguments[0], identities), nil
}

func evaluateReplaceBuiltin(arguments []RuntimeValue) (RuntimeValue, error) {
	if len(arguments) != 1 {
		return RuntimeValue{}, fmt.Errorf("%s expects 1 arguments", fnReplace)
	}

	base := arguments[0]

	return RuntimeValue{Value: runtimeValueData{Kind: Replacement}, Base: &base}, nil
}

func evaluateContainsBuiltin(arguments []RuntimeValue) (RuntimeValue, error) {
	if len(arguments) != 2 {
		return RuntimeValue{}, fmt.Errorf("%s expects 2 arguments", fnContains)
	}
	container, item := arguments[0], arguments[1]
	switch container.Kind() {
	case List, AdapterBundle:
		items, ok := runtimeListValue(container)
		if !ok {
			return RuntimeValue{}, errors.New("adapter Bundle must contain a List")
		}
		for _, candidate := range items.List {
			if equalRuntimeValues(candidate, item) {
				return RuntimeValue{Value: runtimeValueData{Kind: Bool, Bool: true}}, nil
			}
		}
	case String:
		if item.Kind() != String {
			return RuntimeValue{}, errors.New("contains expects a String search value")
		}

		return RuntimeValue{Value: runtimeValueData{Kind: Bool, Bool: strings.Contains(container.Value.Text, item.Value.Text)}}, nil
	default:
		return RuntimeValue{}, fmt.Errorf("contains does not accept %s", kindName(container.Kind()))
	}

	return RuntimeValue{Value: runtimeValueData{Kind: Bool}}, nil
}

func evaluateLenBuiltin(arguments []RuntimeValue) (RuntimeValue, error) {
	if len(arguments) != 1 {
		return RuntimeValue{}, fmt.Errorf("%s expects 1 arguments", fnLen)
	}
	var length int
	switch arguments[0].Kind() {
	case String:
		length = utf8.RuneCountInString(arguments[0].Value.Text)
	case List, AdapterBundle:
		items, ok := runtimeListValue(arguments[0])
		if !ok {
			return RuntimeValue{}, errors.New("adapter Bundle must contain a List")
		}
		length = len(items.List)
	case Map:
		length = len(arguments[0].Entries())
	default:
		return RuntimeValue{}, fmt.Errorf("len does not accept %s", kindName(arguments[0].Kind()))
	}

	return RuntimeValue{Value: runtimeValueData{Kind: Integer, Text: strconv.Itoa(length)}}, nil
}

func mergeByKey(base, overrides, key RuntimeValue) (RuntimeValue, error) {
	baseItems, baseIsList := runtimeListValue(base)
	overrideItems, overridesAreList := runtimeListValue(overrides)
	if !baseIsList || !overridesAreList || key.Kind() != String {
		return RuntimeValue{}, errors.New("mergeByKey expects two Lists and a String key")
	}
	if err := validateRuntimeMergeKeys(baseItems.List, key.Value.Text); err != nil {
		return RuntimeValue{}, fmt.Errorf("base list: %w", err)
	}
	if err := validateRuntimeMergeKeys(overrideItems.List, key.Value.Text); err != nil {
		return RuntimeValue{}, fmt.Errorf("override list: %w", err)
	}
	result := RuntimeValue{Value: runtimeListResultMetadata(base), List: slices.Clone(baseItems.List)}
	indices := make(map[string][]int, len(result.List))
	linear := false
	for index, item := range result.List {
		itemKey, _ := runtimeMapValue(item, key.Value.Text)
		if fingerprint, ok := runtimeMergeKeyFingerprint(itemKey); ok {
			indices[fingerprint] = append(indices[fingerprint], index)
		} else {
			linear = true
		}
	}
	for _, override := range overrideItems.List {
		overrideKey, exists := runtimeMapValue(override, key.Value.Text)
		if !exists {
			return RuntimeValue{}, fmt.Errorf("mergeByKey item is missing key %q", key.Value.Text)
		}
		fingerprint, indexable := runtimeMergeKeyFingerprint(overrideKey)
		found := false
		if linear || !indexable {
			linear = true
			for index, existing := range result.List {
				existingKey, _ := runtimeMapValue(existing, key.Value.Text)
				if !equalRuntimeValues(existingKey, overrideKey) {
					continue
				}
				merged, err := mergeRuntimeValues(existing, override)
				if err != nil {
					return RuntimeValue{}, err
				}
				result.List[index] = merged
				found = true

				break
			}
		} else {
			for _, index := range indices[fingerprint] {
				existing := result.List[index]
				existingKey, _ := runtimeMapValue(existing, key.Value.Text)
				if !equalRuntimeValues(existingKey, overrideKey) {
					continue
				}
				merged, err := mergeRuntimeValues(existing, override)
				if err != nil {
					return RuntimeValue{}, err
				}
				result.List[index] = merged
				found = true

				break
			}
		}
		if !found {
			if indexable && !linear {
				indices[fingerprint] = append(indices[fingerprint], len(result.List))
			}
			result.List = append(result.List, override)
		}
	}

	return result, nil
}

func lifecycleAttach(arguments []RuntimeValue) (RuntimeValue, error) {
	if len(arguments) != 5 {
		return RuntimeValue{}, errors.New("lifecycle.attach expects 5 arguments")
	}
	value := arguments[0]
	if runtimeMapContents(value).Kind() != Map || value.Kind() == AdapterBundle {
		return RuntimeValue{}, errors.New("lifecycle.attach expects a Map value")
	}
	if value.Metadata().lifecycleMetadata() != nil {
		return RuntimeValue{}, errors.New("lifecycle.attach cannot replace existing lifecycle metadata")
	}
	eventValues := arguments[1]
	if (eventValues.Kind() != List && eventValues.Kind() != AdapterBundle) || len(eventValues.ListItems()) == 0 {
		return RuntimeValue{}, errors.New("lifecycle.attach events must be a non-empty List")
	}
	lifecycle := &Lifecycle{Priority: 0}
	for _, event := range eventValues.ListItems() {
		if event.Kind() != String || !validLifecycleEvent(event.Value.Text) || event.Value.Text == phaseMain {
			return RuntimeValue{}, errors.New("lifecycle.attach contains an invalid event")
		}
		if slices.Contains(lifecycle.Events, event.Value.Text) {
			return RuntimeValue{}, fmt.Errorf("lifecycle.attach event %q is duplicated", event.Value.Text)
		}
		lifecycle.Events = append(lifecycle.Events, event.Value.Text)
	}
	if arguments[2].Kind() != Integer {
		return RuntimeValue{}, errors.New("lifecycle.attach priority must be an Integer")
	}
	priority, err := strconv.Atoi(arguments[2].Value.Text)
	if err != nil {
		return RuntimeValue{}, errors.New("lifecycle.attach priority is outside the supported range")
	}
	lifecycle.Priority = priority
	if arguments[3].Kind() != Bool {
		return RuntimeValue{}, errors.New("lifecycle.attach wait must be a Bool")
	}
	lifecycle.Wait = arguments[3].Value.Bool
	if arguments[4].Kind() != List && arguments[4].Kind() != AdapterBundle {
		return RuntimeValue{}, errors.New("lifecycle.attach cleanup must be a List")
	}
	for _, cleanup := range arguments[4].ListItems() {
		if cleanup.Kind() != String {
			return RuntimeValue{}, errors.New("lifecycle.attach cleanup must contain only Strings")
		}
		switch cleanup.Value.Text {
		case cleanupBeforeNextRun, cleanupOnSuccess, cleanupOnFailure:
		default:
			return RuntimeValue{}, fmt.Errorf("lifecycle.attach has an unsupported cleanup policy %q", cleanup.Value.Text)
		}
		if slices.Contains(lifecycle.Cleanup, cleanup.Value.Text) {
			return RuntimeValue{}, fmt.Errorf("lifecycle.attach cleanup policy %q is duplicated", cleanup.Value.Text)
		}
		lifecycle.Cleanup = append(lifecycle.Cleanup, cleanup.Value.Text)
	}
	value.Value.setLifecycleMetadata(lifecycle)

	return value, nil
}

func validateRuntimeMergeKeys(values []RuntimeValue, key string) error {
	keys := make(map[string][]RuntimeValue, len(values))
	prior := make([]RuntimeValue, 0, len(values))
	linear := false
	for _, value := range values {
		if runtimeMapContents(value).Kind() != Map {
			return errors.New("list items must be Maps")
		}
		itemKey, exists := runtimeMapValue(value, key)
		if !exists {
			return fmt.Errorf("item is missing key %q", key)
		}
		fingerprint, indexable := runtimeMergeKeyFingerprint(itemKey)
		if linear || !indexable {
			linear = true
			for _, existing := range prior {
				if equalRuntimeValues(existing, itemKey) {
					return fmt.Errorf("duplicate value for key %q", key)
				}
			}
		} else {
			for _, existing := range keys[fingerprint] {
				if equalRuntimeValues(existing, itemKey) {
					return fmt.Errorf("duplicate value for key %q", key)
				}
			}
			keys[fingerprint] = append(keys[fingerprint], itemKey)
		}
		prior = append(prior, itemKey)
	}

	return nil
}

func runtimeMergeKeyFingerprint(value RuntimeValue) (string, bool) {
	var builder strings.Builder
	if !writeRuntimeMergeKeyFingerprint(&builder, value) {
		return "", false
	}

	return builder.String(), true
}

func writeRuntimeMergeKeyFingerprint(builder *strings.Builder, value RuntimeValue) bool {
	builder.WriteByte(byte(value.Kind()))
	switch value.Kind() {
	case Null:
		return true
	case Node:
		if value.NodeIdentity() == nil {
			return false
		}
		text := fmt.Sprintf("%p", value.NodeIdentity())
		builder.WriteString(strconv.Itoa(len(text)))
		builder.WriteByte(':')
		builder.WriteString(text)
	case Bool:
		builder.WriteString(strconv.FormatBool(value.Value.Bool))
	case Integer, String:
		builder.WriteString(strconv.Itoa(len(value.Value.Text)))
		builder.WriteByte(':')
		builder.WriteString(value.Value.Text)
	case Decimal:
		decimalValue, err := parseRuntimeDecimal(value.Value.Text)
		if err != nil {
			return false
		}
		if err := validateRuntimeDecimal(decimalValue); err != nil {
			return false
		}
		text := decimalValue.String()
		builder.WriteString(strconv.Itoa(len(text)))
		builder.WriteByte(':')
		builder.WriteString(text)
	case List, AdapterBundle:
		items, ok := runtimeListValue(value)
		if !ok {
			return false
		}
		builder.WriteString(strconv.Itoa(len(items.List)))
		builder.WriteByte(':')
		for _, item := range items.List {
			if !writeRuntimeMergeKeyFingerprint(builder, item) {
				return false
			}
		}
	case Map:
		builder.WriteString(strconv.Itoa(len(value.Entries())))
		builder.WriteByte(':')
		keys := make([]string, 0, len(value.Entries()))
		seen := make(map[string]struct{}, len(value.Entries()))
		for _, entry := range value.Entries() {
			if _, exists := seen[entry.Key]; exists {
				return false
			}
			seen[entry.Key] = struct{}{}
			keys = append(keys, entry.Key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			builder.WriteString(strconv.Itoa(len(key)))
			builder.WriteByte(':')
			builder.WriteString(key)
			item, _ := runtimeMapValue(value, key)
			if !writeRuntimeMergeKeyFingerprint(builder, item) {
				return false
			}
		}
	default:
		return false
	}

	return true
}

func runtimeListValue(value RuntimeValue) (RuntimeValue, bool) {
	if value.Kind() != List && value.Kind() != AdapterBundle {
		return RuntimeValue{}, false
	}

	return value, true
}

func runtimeListResultMetadata(value RuntimeValue) runtimeValueData {
	metadata := value.Metadata()
	metadata.Kind = List
	metadata.setLifecycleMetadata(nil)

	return metadata
}

func runtimeMapContents(value RuntimeValue) RuntimeValue {
	for value.Kind() == Node && value.Base != nil {
		value = *value.Base
	}

	return value
}

func runtimeMapValue(value RuntimeValue, key string) (RuntimeValue, bool) {
	value = runtimeMapContents(value)

	return value.MapLookup(key)
}

func mapValue(value Value, key string) (Value, bool) {
	for _, entry := range value.Map {
		if entry.Key == key {
			return entry.Value, true
		}
	}

	return Value{}, false
}

func equalRuntimeValues(left, right RuntimeValue) bool {
	leftItems, leftIsList := runtimeListValue(left)
	rightItems, rightIsList := runtimeListValue(right)
	if leftIsList && rightIsList {
		if len(leftItems.List) != len(rightItems.List) {
			return false
		}
		for index := range leftItems.List {
			if !equalRuntimeValues(leftItems.List[index], rightItems.List[index]) {
				return false
			}
		}

		return true
	}
	if left.Kind() != right.Kind() {
		return false
	}
	switch left.Kind() {
	case Null:
		return true
	case Node:
		return left.NodeIdentity() != nil && left.NodeIdentity() == right.NodeIdentity()
	case Bool:
		return left.Value.Bool == right.Value.Bool
	case Decimal:
		leftDecimal, leftErr := parseRuntimeDecimal(left.Value.Text)
		rightDecimal, rightErr := parseRuntimeDecimal(right.Value.Text)

		return leftErr == nil && rightErr == nil && compareRuntimeDecimals(leftDecimal, rightDecimal) == 0
	case Integer, String:
		return left.Value.Text == right.Value.Text
	case List, AdapterBundle:
		return false
	case Map:
		if len(left.Entries()) != len(right.Entries()) {
			return false
		}
		for _, entry := range left.Entries() {
			other, exists := runtimeMapValue(right, entry.Key)
			if !exists || !equalRuntimeValues(entry.Value, other) {
				return false
			}
		}

		return true
	default:
		return false
	}
}

func mergeRuntimeValues(base, patch RuntimeValue) (RuntimeValue, error) {
	if patch.Kind() == Null {
		patch.Value.SourceLocations = mergedOverrideSourceLocations(base.Metadata(), patch.Metadata())
		inheritRuntimeTypeProvenance(&patch, base, patch)
		if err := validateRuntimeInheritedConstraints(patch); err != nil {
			return RuntimeValue{}, err
		}

		return patch, nil
	}
	baseContents, patchContents := runtimeMapContents(base), runtimeMapContents(patch)
	if base.Kind() == Node && base.Base != nil {
		merged, err := mergeRuntimeValues(*base.Base, patch)
		if err != nil {
			return RuntimeValue{}, err
		}
		base.Base = &merged

		return base, nil
	}
	listKindsMatch := (baseContents.Kind() == List || baseContents.Kind() == AdapterBundle) && (patchContents.Kind() == List || patchContents.Kind() == AdapterBundle)
	if baseContents.Kind() != patchContents.Kind() && !listKindsMatch {
		return RuntimeValue{}, mergeKindMismatch{message: fmt.Sprintf("cannot merge %s with %s; use an explicit replacement", kindName(base.Kind()), kindName(patch.Kind()))}
	}
	metadata := base.Metadata().lifecycleMetadata()
	if metadata == nil {
		metadata = patch.Metadata().lifecycleMetadata()
	}
	switch baseContents.Kind() {
	case Map:
		result := RuntimeValue{Value: baseContents.Metadata(), Map: slices.Clone(baseContents.Map)}
		result.Value.SourceLocations = mergedSourceLocations(baseContents.Metadata(), patchContents.Metadata())
		indices := make(map[string]int, len(result.Map))
		for index, entry := range result.Map {
			if _, exists := indices[entry.Key]; !exists {
				indices[entry.Key] = index
			}
		}
		for _, override := range patchContents.Map {
			index, exists := indices[override.Key]
			if exists {
				if override.Value.Kind() == Replacement {
					if override.Value.Base == nil {
						return RuntimeValue{}, errors.New("invalid replace expression")
					}
					replacement := *override.Value.Base
					existing := result.Map[index].Value
					if (existing.Kind() == List || existing.Kind() == AdapterBundle) && (replacement.Kind() == List || replacement.Kind() == AdapterBundle) {
						replacement.Value.Kind = existing.Kind()
					}
					if replacement.Metadata().lifecycleMetadata() == nil {
						replacement.Value.setLifecycleMetadata(existing.Metadata().lifecycleMetadata())
					}
					replacement.Value.SourceLocations = mergedOverrideSourceLocations(existing.Metadata(), replacement.Metadata())
					inheritRuntimeTypeProvenance(&replacement, existing, replacement)
					if err := validateRuntimeInheritedConstraints(replacement); err != nil {
						return RuntimeValue{}, fmt.Errorf("field %q: %w", override.Key, err)
					}
					result.Map[index] = mergeRuntimeEntrySourceLocations(result.Map[index], override)
					result.Map[index].Value = replacement

					continue
				}
				merged, err := mergeRuntimeValues(result.Map[index].Value, override.Value)
				if err != nil {
					return RuntimeValue{}, fmt.Errorf("field %q: %w", override.Key, err)
				}
				result.Map[index] = mergeRuntimeEntrySourceLocations(result.Map[index], override)
				result.Map[index].Value = merged

				continue
			}
			if override.Value.Kind() == Replacement {
				return RuntimeValue{}, fmt.Errorf("replace requires an existing field %q", override.Key)
			}
			indices[override.Key] = len(result.Map)
			result.Map = append(result.Map, override)
		}
		result.Value.setLifecycleMetadata(metadata)
		inheritRuntimeTypeProvenance(&result, baseContents, patchContents)
		if err := validateRuntimeInheritedConstraints(result); err != nil {
			return RuntimeValue{}, err
		}

		return result, nil
	case List, AdapterBundle:
		if baseContents.Kind() != patchContents.Kind() && baseContents.Kind() == AdapterBundle {
			patchContents.Value.Kind = AdapterBundle
		}
		patchContents.Value.setLifecycleMetadata(metadata)
		patchContents.Value.SourceLocations = mergedOverrideSourceLocations(baseContents.Metadata(), patchContents.Metadata())
		inheritRuntimeTypeProvenance(&patchContents, baseContents, patchContents)
		if err := validateRuntimeInheritedConstraints(patchContents); err != nil {
			return RuntimeValue{}, err
		}

		return patchContents, nil
	case Bool, Integer, Decimal, String:
		patchContents.Value.Kind = baseContents.Kind()
		patchContents.Value.setLifecycleMetadata(metadata)
		patchContents.Value.SourceLocations = mergedOverrideSourceLocations(baseContents.Metadata(), patchContents.Metadata())
		inheritRuntimeTypeProvenance(&patchContents, baseContents, patchContents)
		if err := validateRuntimeInheritedConstraints(patchContents); err != nil {
			return RuntimeValue{}, err
		}

		return patchContents, nil
	case Null:
		return RuntimeValue{}, fmt.Errorf("cannot replace Null with %s using with", kindName(patchContents.Kind()))
	default:
		return RuntimeValue{}, fmt.Errorf("cannot merge %s", kindName(baseContents.Kind()))
	}
}

func mergeRuntimeEntrySourceLocations(base, patch RuntimeEntry) RuntimeEntry {
	baseLocations := appendSourceLocations(nil, base.KeySourceLocations, base.KeySourcePath, base.KeySpan, "Base map key")
	patchLocations := appendSourceLocations(nil, patch.KeySourceLocations, patch.KeySourcePath, patch.KeySpan, "Overlay map key")
	base.KeySourceLocations = appendSourceLocations(baseLocations, patchLocations, "", SourceSpan{}, "")
	if patch.KeySourcePath != "" && patch.KeySpan.End > patch.KeySpan.Start {
		base.KeySourcePath, base.KeySpan = patch.KeySourcePath, patch.KeySpan
	}

	return base
}

func inheritRuntimeTypeProvenance(value *RuntimeValue, base, patch RuntimeValue) {
	if value == nil {
		return
	}
	inheritRuntimeSchemaProvenance(value, base, patch)
	var origins []TypeProvenance
	if base.Value.Typing != nil {
		origins = base.Value.Typing.TypeProvenance
	}
	if len(origins) == 0 && patch.Value.Typing != nil {
		origins = patch.Value.Typing.TypeProvenance
	}
	if len(origins) == 0 {
		return
	}
	value.EnsureTyping().TypeProvenance = slices.Clone(origins)
}

func validateRuntimeInheritedConstraints(value RuntimeValue) error {
	if value.Value.Typing == nil {
		return nil
	}
	for _, origin := range value.Value.Typing.TypeProvenance {
		if typeProvenanceCoveredByUnion(origin, value.Value.Typing.TypeProvenance) {
			continue
		}
		if err := checkRuntimeType(value, origin.Type, origin.Types, make(map[string]bool)); err != nil {
			return fmt.Errorf("value does not satisfy its declared type: %w", err)
		}
	}
	if provenance := value.Value.Typing.SchemaProvenance; provenance != nil && provenance.Type != nil {
		if err := checkRuntimeType(value, *provenance.Type, nil, make(map[string]bool)); err != nil {
			return fmt.Errorf("value does not satisfy its schema: %w", err)
		}
	}

	return nil
}

func applyRuntimeIndexAccess(base, key RuntimeValue) (RuntimeValue, error) {
	base = runtimeMapContents(base)
	switch base.Kind() {
	case Map:
		if key.Kind() != String {
			return RuntimeValue{}, typeDiagnostic(fmt.Errorf("map index must be String, got %s", kindName(key.Kind())))
		}
		if value, exists := base.MapLookup(key.Value.Text); exists {
			return value, nil
		}

		return RuntimeValue{}, fmt.Errorf("map key %q does not exist", key.Value.Text)
	case List, AdapterBundle:
		if key.Kind() != Integer {
			return RuntimeValue{}, typeDiagnostic(fmt.Errorf("list index must be Int, got %s", kindName(key.Kind())))
		}
		index, err := parseRuntimeInteger(key.Value.Text)
		if err != nil || !index.IsInt64() {
			return RuntimeValue{}, fmt.Errorf("list index %q is out of range", key.Value.Text)
		}
		position := index.Int64()
		if position < 0 || position >= int64(len(base.List)) {
			return RuntimeValue{}, fmt.Errorf("list index %d is out of range for length %d", position, len(base.List))
		}

		return base.List[position], nil
	default:
		return RuntimeValue{}, typeDiagnostic(fmt.Errorf("cannot index a %s value", kindName(base.Kind())))
	}
}

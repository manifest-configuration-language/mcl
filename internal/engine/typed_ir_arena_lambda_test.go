package engine

import "testing"

func TestRuntimeLambdaBodyDefaultsAndLocalsShareArenaAfterClone(t *testing.T) {
	stringType := ResolvedType{Name: typeString}
	expression := typedExpression{
		kind: typedExpressionList,
		items: []typedExpression{
			{kind: typedExpressionReference, text: "combined", resolvedType: stringType},
			{kind: typedExpressionString, text: "suffix", resolvedType: stringType},
			{
				kind: typedExpressionBinary, operator: typedOperatorAdd, resolvedType: stringType,
				left:  &typedExpression{kind: typedExpressionReference, text: "prefix", resolvedType: stringType},
				right: &typedExpression{kind: typedExpressionReference, text: "suffix", resolvedType: stringType},
			},
		},
	}
	unit, rootID := newTypedUnit(expression)
	root, ok := unit.node(rootID)
	if !ok || len(root.items) != 3 {
		t.Fatalf("arena root = %#v, want body, default, and local expression IDs", root)
	}
	defaults := []typedExpressionRef{{}, newTypedExpressionRef(typedExpression{arenaUnit: unit, arenaID: root.items[1].id})}
	returnType := stringType
	lambda := RuntimeValue{
		Value: runtimeValueData{Kind: Lambda, SourcePath: "test.mcl"},
		Lambda: &typedLambda{
			Parameters: []string{"prefix", "suffix"}, Defaults: defaults,
			Locals: []typedLocalBinding{{
				Name: "combined", Expression: newTypedExpressionRef(typedExpression{arenaUnit: unit, arenaID: root.items[2].id}),
				Annotation: &stringType,
			}},
			Body: newTypedExpressionRef(typedExpression{arenaUnit: unit, arenaID: root.items[0].id}),
			Typing: &LambdaTyping{
				ParameterTypes: []ResolvedType{stringType, stringType}, ReturnType: &returnType,
				TypeScope: map[string]TypeAnalysis{
					"prefix": {Type: stringType}, "suffix": {Type: stringType}, "combined": {Type: stringType},
				},
				Types: make(map[string]ResolvedType),
			},
			ModuleContext: &ModuleContext{Analysis: &TypeAnalysisContext{}},
		},
	}
	cloned := cloneRuntimeValueGraph(lambda, nil)
	if cloned.Lambda.Body.Unit != unit || cloned.Lambda.Body.ID != lambda.Lambda.Body.ID {
		t.Fatal("cloning RuntimeValue copied the Lambda body instead of sharing its arena")
	}
	if len(cloned.Lambda.Defaults) != 2 || cloned.Lambda.Defaults[0].Unit != nil || cloned.Lambda.Defaults[1].Unit != unit {
		t.Fatalf("cloned Lambda defaults = %#v, want an absent default and a shared arena reference", cloned.Lambda.Defaults)
	}
	if len(cloned.Lambda.Locals) != 1 || cloned.Lambda.Locals[0].Expression.Unit != unit {
		t.Fatalf("cloned Lambda locals = %#v, want a shared arena reference", cloned.Lambda.Locals)
	}

	state := &EvaluationState{
		EvaluationSession: &EvaluationSession{Budget: &functionBudget{}, Allocations: &runtimeAllocationBudget{}},
		Environment:       make(map[string]RuntimeValue),
		Types:             make(map[string]ResolvedType),
		Module:            cloned.Lambda.ModuleContext,
	}
	result, err := callRuntimeLambda(cloned, []RuntimeValue{{Value: runtimeValueData{Kind: String, Text: "prefix"}}}, nil, state)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind() != String || result.TextValue() != "prefixsuffix" {
		t.Fatalf("Lambda returned %#v, want prefixsuffix", result)
	}
}

func TestEvaluatedArenaLambdaRetainsBodyReference(t *testing.T) {
	body := typedExpression{kind: typedExpressionReference, text: "value"}
	typing := &LambdaTyping{
		ParameterTypes: []ResolvedType{{Name: typeString}}, ReturnType: new(ResolvedType{Name: typeString}),
		TypeScope:     map[string]TypeAnalysis{"value": {Type: ResolvedType{Name: typeString}}},
		Types:         make(map[string]ResolvedType),
		InferenceBody: &body,
	}
	expression := typedExpression{
		kind: typedExpressionLambda, parameters: []string{"value"},
		operand: &body, lambdaTyping: typing,
	}
	unit, lambdaID := newTypedUnit(expression)
	lambdaNode, ok := unit.node(lambdaID)
	if !ok || !lambdaNode.operand.present {
		t.Fatal("arena Lambda has no body node")
	}
	state := &EvaluationState{
		EvaluationSession: &EvaluationSession{Budget: &functionBudget{}, Allocations: &runtimeAllocationBudget{}},
		Environment:       make(map[string]RuntimeValue),
		Types:             make(map[string]ResolvedType),
	}
	lambda, err := evaluateTypedExpressionRuntime(typedExpression{arenaUnit: unit, arenaID: lambdaID}, state)
	if err != nil {
		t.Fatal(err)
	}
	if lambda.Lambda.Body.Unit != unit || lambda.Lambda.Body.ID != lambdaNode.operand.id {
		t.Fatalf("evaluated Lambda body ref = (%p, %d), want (%p, %d)", lambda.Lambda.Body.Unit, lambda.Lambda.Body.ID, unit, lambdaNode.operand.id)
	}
	cloned := cloneRuntimeValueGraph(lambda, nil)
	if cloned.Lambda.Typing.InferenceBody == nil || cloned.Lambda.Typing.InferenceBody.arenaUnit != unit || cloned.Lambda.Typing.InferenceBody.arenaID != lambdaNode.operand.id {
		t.Fatal("cloned Lambda inference body did not retain the body arena reference")
	}
	result, err := callRuntimeLambda(cloned, []RuntimeValue{{Value: runtimeValueData{Kind: String, Text: "value"}}}, nil, state)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind() != String || result.TextValue() != "value" {
		t.Fatalf("arena Lambda returned %#v, want value", result)
	}
}

func TestArenaLambdaReturnValidationPreservesListSpreadElementTypes(t *testing.T) {
	integerType := ResolvedType{Name: typeInt}
	listElementType := integerType
	listType := ResolvedType{Name: typeList, Element: &listElementType}
	body := typedExpression{
		kind: typedExpressionList, resolvedType: listType,
		items: []typedExpression{
			{
				kind: typedExpressionReference, text: "items", resolvedType: listType, spread: true,
			},
			{kind: typedExpressionInteger, text: "3", resolvedType: integerType},
		},
	}
	unit, bodyID := newTypedUnit(body)
	lambda := RuntimeValue{
		Value: runtimeValueData{Kind: Lambda},
		Lambda: &typedLambda{
			Parameters: []string{"items"}, Body: newTypedExpressionRef(typedExpression{arenaUnit: unit, arenaID: bodyID}),
			Typing: &LambdaTyping{
				ParameterTypes: []ResolvedType{listType}, ReturnType: &listType,
				TypeScope: map[string]TypeAnalysis{"items": {Type: listType}}, Types: make(map[string]ResolvedType),
			},
		},
	}
	state := &EvaluationState{
		EvaluationSession: &EvaluationSession{Budget: &functionBudget{}, Allocations: &runtimeAllocationBudget{}},
		Environment:       make(map[string]RuntimeValue), Types: make(map[string]ResolvedType),
	}
	argument := RuntimeValue{
		Value: runtimeValueData{Kind: List},
		List: []RuntimeValue{
			{Value: runtimeValueData{Kind: Integer, Text: "1"}},
			{Value: runtimeValueData{Kind: Integer, Text: "2"}},
		},
	}
	result, err := callRuntimeLambda(lambda, []RuntimeValue{argument}, nil, state)
	if err != nil {
		t.Fatalf("callRuntimeLambda() error = %v, want spread-aware List<Int> return", err)
	}
	if result.Kind() != List || len(result.List) != 3 {
		t.Fatalf("Lambda result = %#v, want [1, 2, 3]", result)
	}
	for index, want := range []string{"1", "2", "3"} {
		if result.List[index].Kind() != Integer || result.List[index].TextValue() != want {
			t.Errorf("Lambda result[%d] = %#v, want Integer %s", index, result.List[index], want)
		}
	}
}

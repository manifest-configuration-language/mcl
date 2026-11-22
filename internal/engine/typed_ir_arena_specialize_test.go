package engine

import "testing"

func TestArenaLambdaBodySpecializesBeforeCall(t *testing.T) {
	typeVariable := ResolvedType{Name: lambdaTypeVariableName("test.mcl", SourceSpan{Start: 1, End: 2}, 0)}
	body := typedExpression{
		kind: typedExpressionReference, text: "value", genericVariable: typeVariable.Name,
		resolvedType: typeVariable,
	}
	lambdaExpression := typedExpression{
		kind:       typedExpressionLambda,
		parameters: []string{"value"},
		operand:    &body,
		lambdaTyping: &LambdaTyping{
			ParameterTypes: []ResolvedType{typeVariable},
			ReturnType:     new(typeVariable),
			TypeScope:      map[string]TypeAnalysis{"value": {Type: typeVariable}},
			Types:          make(map[string]ResolvedType),
		},
	}
	unit, lambdaID := newTypedUnit(lambdaExpression)
	state := &EvaluationState{
		EvaluationSession: &EvaluationSession{Budget: &functionBudget{}, Allocations: &runtimeAllocationBudget{}},
		Types:             make(map[string]ResolvedType),
		Environment:       make(map[string]RuntimeValue),
	}
	lambda, err := evaluateTypedExpressionRuntime(typedExpression{arenaUnit: unit, arenaID: lambdaID}, state)
	if err != nil {
		t.Fatal(err)
	}
	argument := RuntimeValue{Value: runtimeValueData{Kind: Integer, Text: "42"}}
	result, err := callRuntimeLambda(lambda, []RuntimeValue{argument}, nil, state)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind() != Integer || result.TextValue() != "42" {
		t.Fatalf("generic arena Lambda returned %#v, want Integer 42", result)
	}

	lambdaNode, ok := unit.node(lambdaID)
	if !ok || !lambdaNode.operand.present {
		t.Fatal("arena Lambda lost its body reference")
	}
	node, ok := unit.node(lambdaNode.operand.id)
	if !ok || node.genericVariable != typeVariable.Name || node.resolvedType.Name != typeVariable.Name {
		t.Fatalf("specialization changed immutable arena node: %#v", node)
	}
}

func TestSpecializationRejectsInvalidArenaID(t *testing.T) {
	unit, _ := newTypedUnit(typedExpression{kind: typedExpressionNull})
	if _, err := specializeTypedExpression(typedExpression{arenaUnit: unit, arenaID: 1}, nil, nil); err == nil {
		t.Fatal("specialization accepted an ExprID outside the typed arena")
	}
}

package engine

import "testing"

func TestRuntimeFrameLookupUsesNearestBinding(t *testing.T) {
	root := newRuntimeRootFrame(map[string]RuntimeValue{
		"shared": runtimeFrameTestValue("root"),
		"root":   runtimeFrameTestValue("root-only"),
	})
	childBindings := map[string]RuntimeValue{"shared": runtimeFrameTestValue("child")}
	child := root.child(childBindings)
	childBindings["shared"] = runtimeFrameTestValue("changed")

	for name, want := range map[string]string{"shared": "child", "root": "root-only"} {
		got, ok := child.lookup(name)
		if !ok || got.TextValue() != want {
			t.Errorf("lookup(%q) = (%q, %t), want (%q, true)", name, got.TextValue(), ok, want)
		}
	}
	if _, ok := child.lookup("missing"); ok {
		t.Fatal("lookup(missing) found a binding")
	}
}

func TestLambdaCapturesRuntimeFrame(t *testing.T) {
	t.Parallel()
	bindings := map[string]RuntimeValue{"outer": runtimeFrameTestValue("outer-value")}
	state := &EvaluationState{
		EvaluationSession: &EvaluationSession{Budget: &functionBudget{}, Allocations: &runtimeAllocationBudget{}},
		Environment:       bindings,
		Types:             make(map[string]ResolvedType),
		Stack:             make(map[string]bool),
	}
	context := typedEvaluationContext{runtimeEnv: bindings, budget: state.Budget, allocations: state.Allocations}
	lambda, err := (typedExpression{
		kind: typedExpressionLambda, operand: &typedExpression{kind: typedExpressionReference, text: "outer"},
		lambdaTyping: &LambdaTyping{},
	}).evaluate(context)
	if err != nil {
		t.Fatal(err)
	}
	if lambda.Lambda.Frame == nil || lambda.Lambda.Scope != nil {
		t.Fatalf("captured scope = (frame %p, map %v), want frame-backed scope", lambda.Lambda.Frame, lambda.Lambda.Scope)
	}
	result, err := callRuntimeLambda(lambda, nil, nil, state)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.TextValue(); got != "outer-value" {
		t.Fatalf("Lambda returned %q, want captured value", got)
	}
}

func TestNewRuntimeRootFrameCopiesBindings(t *testing.T) {
	bindings := map[string]RuntimeValue{"name": runtimeFrameTestValue("before")}
	root := newRuntimeRootFrame(bindings)
	bindings["name"] = runtimeFrameTestValue("after")
	bindings["added"] = runtimeFrameTestValue("new")

	got, ok := root.lookup("name")
	if !ok || got.TextValue() != "before" {
		t.Fatalf("lookup(name) = (%q, %t), want (%q, true)", got.TextValue(), ok, "before")
	}
	if _, ok := root.lookup("added"); ok {
		t.Fatal("root frame observed a binding added to its source map")
	}
}

func TestCloningLambdaFramePreservesNodeCloneSemantics(t *testing.T) {
	identity := &NodeIdentity{}
	node := RuntimeValue{Value: runtimeValueData{Kind: Node, NodeID: identity}}
	lambda := RuntimeValue{
		Value:  runtimeValueData{Kind: Lambda},
		Lambda: &typedLambda{Frame: newRuntimeRootFrame(map[string]RuntimeValue{"resource": node})},
	}

	cloned := cloneRuntimeValueGraph(lambda, nil)
	captured, ok := cloned.Lambda.Frame.lookup("resource")
	if !ok {
		t.Fatal("cloned Lambda lost its captured resource")
	}
	if captured.NodeIdentity() == nil || captured.NodeIdentity() == identity {
		t.Fatalf("cloned captured Node identity = %p, want a distinct non-nil identity", captured.NodeIdentity())
	}
}

func runtimeFrameTestValue(text string) RuntimeValue {
	return RuntimeValue{Value: runtimeValueData{Kind: String, Text: text}}
}

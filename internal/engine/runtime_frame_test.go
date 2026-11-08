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

func TestCloningDeferredForSharesImmutableTypedUnit(t *testing.T) {
	unit, bodyID := newTypedUnit(typedExpression{kind: typedExpressionString, text: "body"})
	deferred := RuntimeValue{Deferred: &typedContinuation{For: &typedForContinuation{Unit: unit, Body: bodyID}}}

	cloned := cloneRuntimeValueGraph(deferred, nil)
	if cloned.Deferred.For.Unit != unit {
		t.Fatal("cloned deferred for expression copied its immutable typed unit")
	}
	if cloned.Deferred.For.Body != deferred.Deferred.For.Body {
		t.Fatalf("cloned body ID = %d, want %d", cloned.Deferred.For.Body, deferred.Deferred.For.Body)
	}
	body, ok := cloned.Deferred.For.Unit.node(cloned.Deferred.For.Body)
	if !ok || body.kind != typedExpressionString || body.text != "body" {
		t.Fatalf("cloned body lookup = (%#v, %t), want string body", body, ok)
	}
}

func TestCloningDeferredFramePreservesCyclesAndNodeIdentity(t *testing.T) {
	identity := &NodeIdentity{}
	frame := &runtimeFrame{bindings: make(map[string]RuntimeValue)}
	deferred := RuntimeValue{Deferred: &typedContinuation{Frame: frame}}
	frame.bindings["node"] = RuntimeValue{Value: runtimeValueData{Kind: Node, NodeID: identity}}
	frame.bindings["deferred"] = deferred

	if size := runtimeValueGraphSize(deferred); size == 0 {
		t.Fatal("runtimeValueGraphSize() returned zero for a deferred frame cycle")
	}
	cloned := cloneRuntimeValueGraph(deferred, nil)
	clonedFrame := cloned.Deferred.Frame
	if clonedFrame == nil || clonedFrame == frame {
		t.Fatalf("cloned frame = %p, want a distinct non-nil frame", clonedFrame)
	}
	capturedNode, ok := clonedFrame.lookup("node")
	if !ok || capturedNode.NodeIdentity() == nil || capturedNode.NodeIdentity() == identity {
		t.Fatalf("cloned node identity = %p, want a distinct non-nil identity", capturedNode.NodeIdentity())
	}
	capturedDeferred, ok := clonedFrame.lookup("deferred")
	if !ok || capturedDeferred.Deferred == nil || capturedDeferred.Deferred.Frame != clonedFrame {
		t.Fatalf("cloned frame cycle points to %#v, want its cloned frame", capturedDeferred.Deferred)
	}
}

func TestCloningDynamicKeyFramePreservesNodeIdentity(t *testing.T) {
	identity := &NodeIdentity{}
	frame := newRuntimeRootFrame(map[string]RuntimeValue{
		"target": {Value: runtimeValueData{Kind: Node, NodeID: identity}},
	})
	expression := typedExpression{kind: typedExpressionReference, text: "target"}
	value := RuntimeValue{
		Value: runtimeValueData{Kind: Map},
		Map:   []RuntimeEntry{{KeyExpression: &expression, KeyFrame: frame}},
	}

	cloned := cloneRuntimeValueGraph(value, nil)
	clonedFrame := cloned.Map[0].KeyFrame
	if clonedFrame == nil || clonedFrame == frame {
		t.Fatalf("cloned dynamic key frame = %p, want a distinct non-nil frame", clonedFrame)
	}
	captured, ok := clonedFrame.lookup("target")
	if !ok || captured.NodeIdentity() == nil || captured.NodeIdentity() == identity {
		t.Fatalf("cloned dynamic key target identity = %p, want a distinct non-nil identity", captured.NodeIdentity())
	}
}

func runtimeFrameTestValue(text string) RuntimeValue {
	return RuntimeValue{Value: runtimeValueData{Kind: String, Text: text}}
}

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

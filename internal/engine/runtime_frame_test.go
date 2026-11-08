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

func runtimeFrameTestValue(text string) RuntimeValue {
	return RuntimeValue{Value: runtimeValueData{Kind: String, Text: text}}
}

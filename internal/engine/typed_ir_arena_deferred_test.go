package engine

import "testing"

func TestDeferredExpressionContinuationsRetainArenaIDsOnClone(t *testing.T) {
	unit, rootID := newTypedUnit(typedExpression{
		kind: typedExpressionList,
		items: []typedExpression{
			{
				kind: typedExpressionBinary, operator: typedOperatorAnd,
				left:  &typedExpression{kind: typedExpressionBool, bool: true},
				right: &typedExpression{kind: typedExpressionReference, text: "right"},
			},
			{
				kind:      typedExpressionConditional,
				condition: &typedExpression{kind: typedExpressionReference, text: "condition"},
				thenBranch: &typedExpression{
					kind: typedExpressionReference, text: "then",
				},
				elseBranch: &typedExpression{kind: typedExpressionReference, text: "else"},
			},
			{
				kind: typedExpressionMap,
				entries: []typedMapEntry{{
					keyExpression: &typedExpression{kind: typedExpressionReference, text: "key"},
					value:         typedExpression{kind: typedExpressionString, text: "value"},
				}},
			},
		},
	})
	root, ok := unit.node(rootID)
	if !ok || len(root.items) != 3 {
		t.Fatalf("arena root = %#v, want three child nodes", root)
	}
	binary, ok := unit.node(root.items[0].id)
	if !ok || !binary.right.present {
		t.Fatalf("arena binary = %#v, want a right child ID", binary)
	}
	conditional, ok := unit.node(root.items[1].id)
	if !ok || !conditional.thenBranch.present || !conditional.elseBranch.present {
		t.Fatalf("arena conditional = %#v, want branch IDs", conditional)
	}
	mapNode, ok := unit.node(root.items[2].id)
	if !ok || len(mapNode.entries) != 1 || !mapNode.entries[0].keyExpression.present {
		t.Fatalf("arena map = %#v, want a dynamic key ID", mapNode)
	}

	right := newTypedExpressionRef(typedExpression{arenaUnit: unit, arenaID: binary.right.id})
	thenBranch := newTypedExpressionRef(typedExpression{arenaUnit: unit, arenaID: conditional.thenBranch.id})
	elseBranch := newTypedExpressionRef(typedExpression{arenaUnit: unit, arenaID: conditional.elseBranch.id})
	key := newTypedExpressionRef(typedExpression{arenaUnit: unit, arenaID: mapNode.entries[0].keyExpression.id})
	if right.Unit != unit || thenBranch.Unit != unit || elseBranch.Unit != unit || key.Unit != unit {
		t.Fatal("arena expression references copied an existing unit")
	}
	value := RuntimeValue{
		Value: runtimeValueData{Kind: Map},
		Map:   []RuntimeEntry{{KeyExpression: &key}},
		Deferred: &typedContinuation{
			Binary: &typedBinaryContinuation{RightExpression: &right},
			Conditional: &typedConditionalContinuation{
				ThenBranch: thenBranch, ElseBranch: elseBranch,
			},
		},
	}
	cloned := cloneRuntimeValueGraph(value, nil)
	if len(cloned.Map) != 1 || cloned.Deferred == nil || cloned.Deferred.Binary == nil || cloned.Deferred.Binary.RightExpression == nil || cloned.Deferred.Conditional == nil || cloned.Map[0].KeyExpression == nil {
		t.Fatalf("cloned deferred value = %#v, want its map and both continuations", cloned)
	}
	if cloned.Map[0].KeyExpression.Unit != unit || cloned.Map[0].KeyExpression.ID != key.ID {
		t.Fatal("cloning RuntimeValue did not preserve the dynamic key arena reference")
	}
	if cloned.Deferred.Binary.RightExpression.Unit != unit || cloned.Deferred.Binary.RightExpression.ID != right.ID {
		t.Fatal("cloning RuntimeValue did not preserve the binary RHS arena reference")
	}
	if cloned.Deferred.Conditional.ThenBranch.Unit != unit || cloned.Deferred.Conditional.ThenBranch.ID != thenBranch.ID || cloned.Deferred.Conditional.ElseBranch.ID != elseBranch.ID {
		t.Fatal("cloning RuntimeValue did not preserve the conditional branch arena references")
	}

	state := &EvaluationState{EvaluationSession: &EvaluationSession{Budget: &functionBudget{}, Allocations: &runtimeAllocationBudget{}}}
	if _, err := evaluateTypedExpressionReference(typedExpressionRef{Unit: unit, ID: ExprID(len(unit.nodes)) /* #nosec G115 -- this test constructs fewer than ten arena nodes. */}, state, nil); err == nil {
		t.Fatal("evaluation accepted an invalid arena reference")
	}
}

func TestDeferredArenaOperatorsUseCapturedFrameAndResolveNestedValues(t *testing.T) {
	unit, rootID := newTypedUnit(typedExpression{kind: typedExpressionList, items: []typedExpression{
		{kind: typedExpressionReference, text: "right"},
		{kind: typedExpressionReference, text: "selected"},
	}})
	root, ok := unit.node(rootID)
	if !ok || len(root.items) != 2 {
		t.Fatalf("arena root = %#v, want two child IDs", root)
	}
	frame := newRuntimeRootFrame(map[string]RuntimeValue{
		"right": {Value: runtimeValueData{Kind: Bool, Bool: true}},
		"selected": {Deferred: &typedContinuation{
			Expression: typedExpression{kind: typedExpressionString, text: "captured"},
		}},
	})
	leftPending := &typedContinuation{}
	falsePending := &typedContinuation{}
	conditionPending := &typedContinuation{}
	state := &EvaluationState{
		EvaluationSession: &EvaluationSession{Budget: &functionBudget{}, Allocations: &runtimeAllocationBudget{}},
		Environment:       map[string]RuntimeValue{"right": {Value: runtimeValueData{Kind: Bool, Bool: false}}, "selected": {Value: runtimeValueData{Kind: String, Text: "wrong-frame"}}},
	}
	resolve := func(value *RuntimeValue) error {
		switch value.Deferred {
		case leftPending, conditionPending:
			*value = RuntimeValue{Value: runtimeValueData{Kind: Bool, Bool: true}}
		case falsePending:
			*value = RuntimeValue{Value: runtimeValueData{Kind: Bool, Bool: false}}
		case frame.bindings["selected"].Deferred:
			*value = RuntimeValue{Value: runtimeValueData{Kind: String, Text: "captured"}}
		}

		return nil
	}

	rightRef := newTypedExpressionRef(typedExpression{arenaUnit: unit, arenaID: root.items[0].id})
	binary, err := resolveTypedBinaryContinuation(&typedBinaryContinuation{
		Operator: typedOperatorAnd, Left: RuntimeValue{Deferred: leftPending},
		RightExpression: &rightRef, Frame: frame,
	}, state, resolve)
	if err != nil || binary.Kind() != Bool || !binary.BoolValue() {
		t.Fatalf("deferred binary = (%#v, %v), want true from the captured frame", binary, err)
	}
	shortCircuited, err := resolveTypedBinaryContinuation(&typedBinaryContinuation{
		Operator: typedOperatorAnd, Left: RuntimeValue{Deferred: falsePending},
		RightExpression: &typedExpressionRef{Unit: unit, ID: ExprID(len(unit.nodes)) /* #nosec G115 -- this test constructs fewer than ten arena nodes. */}, Frame: frame,
	}, state, resolve)
	if err != nil || shortCircuited.Kind() != Bool || shortCircuited.BoolValue() {
		t.Fatalf("short-circuited deferred binary = (%#v, %v), want false without evaluating its invalid RHS", shortCircuited, err)
	}
	conditional, err := resolveTypedConditionalContinuation(&typedConditionalContinuation{
		Condition:  RuntimeValue{Deferred: conditionPending},
		ThenBranch: newTypedExpressionRef(typedExpression{arenaUnit: unit, arenaID: root.items[1].id}),
		ElseBranch: typedExpressionRef{Unit: unit, ID: ExprID(len(unit.nodes)) /* #nosec G115 -- this test constructs fewer than ten arena nodes. */},
		Frame:      frame,
	}, state, resolve)
	if err != nil || conditional.Kind() != String || conditional.TextValue() != "captured" {
		t.Fatalf("deferred conditional = (%#v, %v), want captured nested value from its frame", conditional, err)
	}
}

func TestDynamicArenaMapKeyRetainsFrameAndResolvesNestedDeferredValue(t *testing.T) {
	unit, bodyID := newTypedUnit(typedExpression{
		kind: typedExpressionMap,
		entries: []typedMapEntry{{
			keyExpression: &typedExpression{kind: typedExpressionReference, text: "key"},
			value:         typedExpression{kind: typedExpressionString, text: "value"},
		}},
	})
	state := &EvaluationState{
		EvaluationSession: &EvaluationSession{Budget: &functionBudget{}, Allocations: &runtimeAllocationBudget{}},
		Environment:       map[string]RuntimeValue{"key": {Value: runtimeValueData{Kind: String, Text: "wrong-environment"}}},
	}
	frame := newRuntimeRootFrame(map[string]RuntimeValue{
		"key": {Deferred: &typedContinuation{Expression: typedExpression{kind: typedExpressionString, text: "captured-key"}}},
	})
	value, err := evaluateTypedExpressionRuntimeInScope(typedExpression{arenaUnit: unit, arenaID: bodyID}, state, nil, frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Map) != 1 || value.Map[0].KeyExpression == nil {
		t.Fatalf("evaluated map = %#v, want one deferred dynamic key", value.Map)
	}
	if value.Map[0].KeyExpression.Unit != unit {
		t.Fatal("dynamic map key did not retain its source arena")
	}
	cloned := cloneRuntimeValueGraph(value, nil)
	if len(cloned.Map) != 1 || cloned.Map[0].KeyExpression == nil || cloned.Map[0].KeyExpression.Unit != unit || cloned.Map[0].KeyExpression.ID != value.Map[0].KeyExpression.ID {
		t.Fatal("cloning dynamic map did not share its immutable key arena reference")
	}
	bundle := &RuntimeBundle{Artifacts: []RuntimeArtifact{{Value: cloned}}}
	if err := resolveRuntimeBundle(bundle, state); err != nil {
		t.Fatal(err)
	}
	resolved := bundle.Artifacts[0].Value
	if len(resolved.Map) != 1 {
		t.Fatalf("resolved map = %#v, want one entry", resolved.Map)
	}
	if resolved.Map[0].Key != "captured-key" || resolved.Map[0].KeyExpression != nil {
		t.Fatalf("resolved dynamic key = (%q, %#v), want captured-key and no deferred reference", resolved.Map[0].Key, resolved.Map[0].KeyExpression)
	}
}

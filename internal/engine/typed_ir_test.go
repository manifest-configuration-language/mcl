package engine

import (
	"strings"
	"testing"
)

func TestTypedBuiltinCallUsesBuiltinIDOutsideSchemaPath(t *testing.T) {
	t.Parallel()
	module, err := ParseAST("let result = len(\"x\")\n")
	if err != nil {
		t.Fatal(err)
	}
	call, ok := module.Nodes[0].(*LetDeclaration).Expression.(*CallExpr)
	if !ok {
		t.Fatalf("expression = %T, want a CallExpr", module.Nodes[0].(*LetDeclaration).Expression)
	}
	inferred, known, err := inferStaticExprType(call, nil, nil)
	if err != nil || !known {
		t.Fatalf("inferStaticExprType() = (%#v, %t, %v), want a known type", inferred, known, err)
	}
	item, exists := builtinByName(fnLen)
	if !exists {
		t.Fatalf("builtin %q is missing from the registry", fnLen)
	}
	typed, ok := typeBuiltinCallExpression(call, inferred, item, typedEvaluationContext{})
	if !ok {
		t.Fatal("typeBuiltinCallExpression() rejected a valid builtin call")
	}
	if typed.builtinID != builtinID(fnLen) {
		t.Fatalf("typed builtin ID = %q, want %q", typed.builtinID, fnLen)
	}
	if typed.callee == nil || typed.callee.resolvedType.SchemaPath != "" {
		t.Fatalf("builtin callee type = %#v, want no schema path", typed.callee)
	}
	value, err := evaluateRuntimeExpression(typed, &EvaluationState{})
	if err != nil {
		t.Fatal(err)
	}
	if value.Kind() != Integer || value.TextValue() != "1" {
		t.Fatalf("builtin call result = %#v, want integer 1", value)
	}
}

func TestTypePrimitiveMapWithConstantDynamicKeys(t *testing.T) {
	t.Parallel()

	const source = "let value =\n  key(\"a\" + \"pi\"): \"first\"\n  key(\"\" + \"worker\"): \"second\"\n"
	module, err := ParseAST(source)
	if err != nil {
		t.Fatal(err)
	}
	declaration := module.Nodes[0].(*LetDeclaration)
	expression := declaration.Expression
	inferred, known, err := inferStaticExprType(expression, nil, nil)
	if err != nil || !known {
		t.Fatalf("inferStaticExprType() = (%v, %v), want a known type", inferred, err)
	}
	typed, ok := typeExpression(expression, inferred)
	if !ok {
		t.Fatal("typeExpression() did not lower a primitive map with constant dynamic keys")
	}
	gotRuntime, err := evaluateRuntimeExpression(typed, &EvaluationState{SourcePath: declaration.SourcePath})
	if err != nil {
		t.Fatal(err)
	}
	got, err := runtimeValueToValue(gotRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Map) != 2 || got.Map[0].Key != "api" || got.Map[1].Key != "worker" {
		t.Fatalf("typed map entries = %#v, want ordered resolved constant keys", got.Map)
	}
	if got.Map[0].Value.Kind != String || got.Map[0].Value.Text != "first" || got.Map[1].Value.Kind != String || got.Map[1].Value.Text != "second" {
		t.Fatalf("typed map values = %#v, want first and second", got.Map)
	}
	mapExpression, ok := expression.(*MapExpr)
	if !ok {
		t.Fatalf("expression = %#v, want a Map", expression)
	}
	for index, entry := range mapExpression.Entries {
		if got.Map[index].KeySpan != entry.KeySpan || got.Map[index].KeySourcePath != declaration.SourcePath {
			t.Fatalf("typed map entry %d provenance = (%v, %q), want (%v, %q)", index, got.Map[index].KeySpan, got.Map[index].KeySourcePath, entry.KeySpan, declaration.SourcePath)
		}
		if source[entry.KeySpan.Start:entry.KeySpan.End] == "" {
			t.Fatalf("entry %d has an empty key span", index)
		}
	}
}

func TestTypePrimitiveMapWithDynamicKeysFallbackBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
	}{
		{name: "runtime key", source: "let value =\n  key(name): \"api\"\n"},
		{name: "heterogeneous values", source: "let value =\n  key(\"name\"): \"api\"\n  key(\"port\"): 8080\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			module, err := ParseAST(test.source)
			if err != nil {
				t.Fatal(err)
			}
			expression := module.Nodes[0].(*LetDeclaration).Expression
			inferred, _, err := inferStaticExprType(expression, nil, nil)
			if err != nil {
				t.Fatalf("inferStaticExprType() error = %v", err)
			}
			if _, ok := typeExpression(expression, inferred); ok {
				t.Fatal("typeExpression() lowered a dynamic key outside the supported constant String boundary")
			}
		})
	}
}

func TestTypePrimitiveMapWithConstantDynamicKeyPreservesDuplicateDiagnostic(t *testing.T) {
	t.Parallel()

	_, err := EvaluateSource("main.mcl", "let value =\n  key(\"name\"): \"api\"\n  name: \"worker\"\nemit value\n")
	if err == nil || !strings.Contains(err.Error(), `duplicate key "name" after dynamic key evaluation`) {
		t.Fatalf("EvaluateSource() error = %v, want duplicate dynamic key diagnostic", err)
	}
}

func TestTypePrimitiveConditionalMapExpression(t *testing.T) {
	t.Parallel()

	module, err := ParseAST("let value =\n  if true:\n    name: \"api\"\n  else:\n    name: \"worker\"\n")
	if err != nil {
		t.Fatal(err)
	}
	declaration, ok := module.Nodes[0].(*LetDeclaration)
	if !ok || declaration.Expression == nil {
		t.Fatal("ParseAST() did not produce a let expression")
	}
	inferred, known, err := inferStaticExprType(declaration.Expression, nil, nil)
	if err != nil || !known {
		t.Fatalf("inferStaticExprType() = (%v, %v), want a known type", inferred, err)
	}

	typed, ok := typeExpression(declaration.Expression, inferred)
	if !ok {
		t.Fatal("typeExpression() did not lower a primitive map conditional")
	}
	valueRuntime, err := evaluateRuntimeExpression(typed, &EvaluationState{})
	if err != nil {
		t.Fatal(err)
	}
	value, err := runtimeValueToValue(valueRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if value.Kind != Map || len(value.Map) != 1 || value.Map[0].Key != "name" || value.Map[0].Value.Kind != String || value.Map[0].Value.Text != "api" {
		t.Fatalf("typed expression value = %#v, want a map containing name = api", value)
	}
}

func TestTypePrimitiveConditionalExpressionWithDifferentScalarBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		condition string
		wantKind  Kind
		wantText  string
	}{
		{name: "then integer", condition: "true", wantKind: Integer, wantText: "42"},
		{name: "else string", condition: "false", wantKind: String, wantText: "worker"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := "let value =\n  if " + test.condition + ":\n    42\n  else:\n    \"worker\"\n"
			module, err := ParseAST(source)
			if err != nil {
				t.Fatal(err)
			}
			declaration := module.Nodes[0].(*LetDeclaration)
			expression := declaration.Expression
			inferred, known, err := inferStaticExprType(expression, nil, nil)
			if err != nil || !known {
				t.Fatalf("inferStaticExprType() = (%v, %v), want a known union type", inferred, err)
			}
			typed, ok := typeExpression(expression, inferred)
			if !ok {
				t.Fatal("typeExpression() did not lower a conditional with different primitive scalar branches")
			}
			if typed.kind != typedExpressionConditional || typed.thenBranch.kind != typedExpressionInteger || typed.thenBranch.text != "42" || typed.elseBranch.kind != typedExpressionString || typed.elseBranch.text != "worker" {
				t.Fatalf("typed branches = (%#v, %#v), want integer 42 and string worker", typed.thenBranch, typed.elseBranch)
			}
			conditional := expression.(*ConditionalExpr)
			if typed.span != expression.ExprSpan() || typed.thenBranch.span != conditional.Then.ExprSpan() || typed.elseBranch.span != conditional.Else.ExprSpan() {
				t.Fatalf("typed spans = (%v, %v, %v), want source expression and branch spans", typed.span, typed.thenBranch.span, typed.elseBranch.span)
			}

			gotRuntime, err := evaluateRuntimeExpression(typed, &EvaluationState{SourcePath: declaration.SourcePath})
			if err != nil {
				t.Fatal(err)
			}
			got, err := runtimeValueToValue(gotRuntime)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != test.wantKind || got.Text != test.wantText {
				t.Fatalf("typed conditional result = (%v, %q), want (%v, %q)", got.Kind, got.Text, test.wantKind, test.wantText)
			}
		})
	}
}

func TestTypePrimitiveConditionalExpressionRejectsMismatchedScalarUnion(t *testing.T) {
	t.Parallel()

	module, err := ParseAST("let value =\n  if true:\n    42\n  else:\n    \"worker\"\n")
	if err != nil {
		t.Fatal(err)
	}
	expression := module.Nodes[0].(*LetDeclaration).Expression
	wrongInferred := ResolvedType{Options: []ResolvedType{{Name: typeInt}, {Name: typeBool}}}
	if _, ok := typeExpression(expression, wrongInferred); ok {
		t.Fatal("typeExpression() lowered scalar branches against an unrelated inferred union")
	}
}

func TestTypeNestedPrimitiveMapAndListIndexAccess(t *testing.T) {
	t.Parallel()

	module, err := ParseAST("let value =\n  metadata:\n    ports:\n      - 8080\n")
	if err != nil {
		t.Fatal(err)
	}
	declaration, ok := module.Nodes[0].(*LetDeclaration)
	if !ok || declaration.Expression == nil {
		t.Fatal("ParseAST() did not produce a map expression")
	}

	mapExpression := declaration.Expression
	metadataKey := &StringExpr{Value: "metadata"}
	metadataAccess := &IndexExpr{Base: mapExpression, Key: metadataKey}
	portsKey := &StringExpr{Value: "ports"}
	portsAccess := &IndexExpr{Base: metadataAccess, Key: portsKey}
	index := &IntegerExpr{Value: "0"}
	listAccess := &IndexExpr{Base: portsAccess, Key: index}

	inferred, known, err := inferStaticExprType(listAccess, nil, nil)
	if err != nil || !known {
		t.Fatalf("inferStaticExprType() = (%v, %v), want a known type", inferred, err)
	}
	typed, ok := typeExpression(listAccess, inferred)
	if !ok {
		t.Fatal("typeExpression() did not lower nested Map and List index access")
	}
	valueRuntime, err := evaluateRuntimeExpression(typed, &EvaluationState{})
	if err != nil {
		t.Fatal(err)
	}
	value, err := runtimeValueToValue(valueRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if value.Kind != Integer || value.Text != "8080" {
		t.Fatalf("typed expression value = %#v, want integer 8080", value)
	}
}

func TestTypeNestedConditionalMapExpression(t *testing.T) {
	t.Parallel()

	module, err := ParseAST("let value =\n  if true:\n    metadata:\n      name: \"api\"\n  else:\n    metadata:\n      name: \"worker\"\n")
	if err != nil {
		t.Fatal(err)
	}
	declaration, ok := module.Nodes[0].(*LetDeclaration)
	if !ok || declaration.Expression == nil {
		t.Fatal("ParseAST() did not produce a let expression")
	}
	inferred, known, err := inferStaticExprType(declaration.Expression, nil, nil)
	if err != nil || !known {
		t.Fatalf("inferStaticExprType() = (%v, %v), want a known type", inferred, err)
	}

	typed, ok := typeExpression(declaration.Expression, inferred)
	if !ok {
		t.Fatal("typeExpression() did not lower a nested map conditional")
	}
	valueRuntime, err := evaluateRuntimeExpression(typed, &EvaluationState{})
	if err != nil {
		t.Fatal(err)
	}
	value, err := runtimeValueToValue(valueRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if value.Kind != Map || len(value.Map) != 1 || value.Map[0].Key != "metadata" || value.Map[0].Value.Kind != Map || len(value.Map[0].Value.Map) != 1 || value.Map[0].Value.Map[0].Key != "name" || value.Map[0].Value.Map[0].Value.Kind != String || value.Map[0].Value.Map[0].Value.Text != "api" {
		t.Fatalf("typed expression value = %#v, want metadata.name = api", value)
	}
}

func TestTypeConditionalMapWithPrimitiveListField(t *testing.T) {
	t.Parallel()

	module, err := ParseAST("let value =\n  if true:\n    metadata:\n      ports:\n        - 8080\n  else:\n    metadata:\n      ports:\n        - 9090\n")
	if err != nil {
		t.Fatal(err)
	}
	declaration, ok := module.Nodes[0].(*LetDeclaration)
	if !ok || declaration.Expression == nil {
		t.Fatal("ParseAST() did not produce a let expression")
	}
	inferred, known, err := inferStaticExprType(declaration.Expression, nil, nil)
	if err != nil || !known {
		t.Fatalf("inferStaticExprType() = (%v, %v), want a known type", inferred, err)
	}

	typed, ok := typeExpression(declaration.Expression, inferred)
	if !ok {
		t.Fatal("typeExpression() did not lower a conditional Map with a primitive List field")
	}
	valueRuntime, err := evaluateRuntimeExpression(typed, &EvaluationState{})
	if err != nil {
		t.Fatal(err)
	}
	value, err := runtimeValueToValue(valueRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if value.Kind != Map || len(value.Map) != 1 || value.Map[0].Key != "metadata" || value.Map[0].Value.Kind != Map || len(value.Map[0].Value.Map) != 1 || value.Map[0].Value.Map[0].Key != "ports" || value.Map[0].Value.Map[0].Value.Kind != List || len(value.Map[0].Value.Map[0].Value.List) != 1 || value.Map[0].Value.Map[0].Value.List[0].Kind != Integer || value.Map[0].Value.Map[0].Value.List[0].Text != "8080" {
		t.Fatalf("typed expression value = %#v, want metadata.ports = [8080]", value)
	}
}

func TestTypeConditionalMapWithNestedPrimitiveListField(t *testing.T) {
	t.Parallel()

	module, err := ParseAST("let value =\n  if true:\n    ports:\n      -\n        - 8080\n      -\n        - 8081\n  else:\n    ports:\n      -\n        - 9090\n      -\n        - 9091\n")
	if err != nil {
		t.Fatal(err)
	}
	declaration, ok := module.Nodes[0].(*LetDeclaration)
	if !ok || declaration.Expression == nil {
		t.Fatal("ParseAST() did not produce a let expression")
	}
	inferred, known, err := inferStaticExprType(declaration.Expression, nil, nil)
	if err != nil || !known {
		t.Fatalf("inferStaticExprType() = (%v, %v), want a known type", inferred, err)
	}

	typed, ok := typeExpression(declaration.Expression, inferred)
	if !ok {
		t.Fatal("typeExpression() did not lower a conditional Map with a nested primitive List field")
	}
	valueRuntime, err := evaluateRuntimeExpression(typed, &EvaluationState{})
	if err != nil {
		t.Fatal(err)
	}
	value, err := runtimeValueToValue(valueRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if value.Kind != Map || len(value.Map) != 1 || value.Map[0].Key != "ports" || value.Map[0].Value.Kind != List || len(value.Map[0].Value.List) != 2 || value.Map[0].Value.List[0].Kind != List || len(value.Map[0].Value.List[0].List) != 1 || value.Map[0].Value.List[0].List[0].Kind != Integer || value.Map[0].Value.List[0].List[0].Text != "8080" {
		t.Fatalf("typed expression value = %#v, want ports = [[8080], [8081]]", value)
	}
}

func TestTypeConditionalMapWithMismatchedNestedListShapeFallsBack(t *testing.T) {
	t.Parallel()

	module, err := ParseAST("let value =\n  if true:\n    ports:\n      -\n        - 8080\n  else:\n    ports:\n      - 9090\n")
	if err != nil {
		t.Fatal(err)
	}
	declaration, ok := module.Nodes[0].(*LetDeclaration)
	if !ok || declaration.Expression == nil {
		t.Fatal("ParseAST() did not produce a let expression")
	}
	inferred, known, err := inferStaticExprType(declaration.Expression, nil, nil)
	if err != nil || !known {
		t.Fatalf("inferStaticExprType() = (%v, %v), want a known type", inferred, err)
	}

	if _, ok := typeExpression(declaration.Expression, inferred); ok {
		t.Fatal("typeExpression() lowered conditional Maps with mismatched nested List shapes")
	}
}

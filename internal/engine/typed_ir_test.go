package engine

import (
	"strings"
	"testing"
	"testing/fstest"
)

const (
	falseLiteral    = "false"
	emptyListBranch = "    []"
	secretListItem  = "    - secret"
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

func TestTypedEvaluatorSupportsImportedEmptyListInRecord(t *testing.T) {
	t.Parallel()

	files := fstest.MapFS{
		"chart.mcl": &fstest.MapFile{Data: []byte("export let apiVersions: List<String> = []\n")},
	}
	source := "import \"./chart.mcl\" as chart\nlet options =\n  apiVersions: chart.apiVersions\nemit options.apiVersions\n"
	if _, err := EvaluateProjectSourceContext(t.Context(), "/virtual-project", "main.mcl", source, files, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTypedEvaluatorSupportsImportedEmptyMapInRecord(t *testing.T) {
	t.Parallel()

	files := fstest.MapFS{
		"chart.mcl": &fstest.MapFile{Data: []byte("export let selector: Map<String, Any> = {}\n")},
	}
	source := "import \"./chart.mcl\" as chart\nlet options =\n  selector: chart.selector\nemit options.selector\n"
	if _, err := EvaluateProjectSourceContext(t.Context(), "/virtual-project", "main.mcl", source, files, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTypedEvaluatorPropagatesExpectedTypeToEmptyNodeConfigFields(t *testing.T) {
	t.Parallel()

	const source = `type NodeConfigBody =
  metadata: Map<String, Any>
  nodeSelector: Map<String, String>
  tolerations: List<Map<String, Any>>
type NodeConfig = Node<NodeConfigBody>
let config: NodeConfig = node:
  metadata:
    name: "default"
  nodeSelector: {}
  tolerations: []
fn newNodeConfig() -> NodeConfig = node:
  metadata:
    name: "default"
  nodeSelector: {}
  tolerations: []
emit config
emit newNodeConfig()
`
	if _, err := EvaluateSource("main.mcl", source); err != nil {
		t.Fatalf("EvaluateSource() error = %v, want empty nodeConfig collections to inherit their declared field types", err)
	}
	invalidSource := strings.Replace(source, "nodeSelector: {}", "nodeSelector: {worker: 1}", 1)
	if _, err := EvaluateSource("invalid.mcl", invalidSource); err == nil {
		t.Fatal("EvaluateSource() accepted a nonempty field with a value incompatible with its declared type")
	}
}

func TestTypedEvaluatorPropagatesExpectedTypeThroughNestedListAndNode(t *testing.T) {
	t.Parallel()
	stringType := ResolvedType{Name: typeString}
	mapType := ResolvedType{Name: typeMap, Element: &stringType}
	nodeType := ResolvedType{Name: typeNode, Arguments: []ResolvedType{mapType}}
	listType := ResolvedType{Name: typeList, Element: &nodeType}

	for _, test := range []struct {
		name    string
		source  string
		support bool
	}{
		{name: "unknown reference", source: "let values =\n  - value: unresolved\n", support: true},
		{name: "incompatible scalar", source: "let values =\n  - value: 1\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			module, err := ParseAST(test.source)
			if err != nil {
				t.Fatal(err)
			}
			expression := module.Nodes[0].(*LetDeclaration).Expression
			list, ok := expression.(*ListExpr)
			if !ok || len(list.Items) != 1 {
				t.Fatalf("expression = %#v, want one-item ListExpr", expression)
			}
			list.Items[0] = &NodeExpr{ExprBase: ExprBase{Span: list.Items[0].ExprSpan()}, Body: list.Items[0]}
			_, supported := typeExpressionWithInferredExpected(expression, listType, typedEvaluationContext{})
			if supported != test.support {
				t.Fatalf("typeExpressionWithInferredExpected() supported = %t, want %t", supported, test.support)
			}
		})
	}
}

func TestFunctionLocalBindingsAreAvailableDuringTypedLowering(t *testing.T) {
	t.Parallel()

	const source = `export fn doublePlusOne(x: Int) -> Int:
  let y = x + 1
  y * 2

emit doublePlusOne(3)
`
	bundle, err := EvaluateSource("main.mcl", source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v, want function-local bindings to be available while lowering expressions", err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(output); !strings.Contains(got, "8") {
		t.Fatalf("Render() = %s, want the function result 8", output)
	}
}

func TestImportedFunctionLocalAnnotationUsesDefiningTypeScope(t *testing.T) {
	t.Parallel()

	files := fstest.MapFS{
		"lib.mcl": &fstest.MapFile{Data: []byte(`type Local = { value: String }
export fn read() -> String:
  let item: Local = { value: "ok" }
  item.value
`)},
	}
	const source = `import "./lib.mcl" as lib
emit lib.read()
`
	bundle, err := EvaluateProjectSourceContext(t.Context(), "/virtual-project", "main.mcl", source, files, nil)
	if err != nil {
		t.Fatalf("EvaluateProjectSourceContext() error = %v, want imported local annotations to use their defining type scope", err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"ok"`) {
		t.Fatalf("Render() = %s, want the imported function result", output)
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
		{name: "else string", condition: falseLiteral, wantKind: String, wantText: "worker"},
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

func TestEvaluateConditionalListWithEmptyBranch(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		enabled     string
		thenBranch  string
		elseBranch  string
		wantEmitted int
	}{
		{name: "then-resource", enabled: "true", thenBranch: secretListItem, elseBranch: emptyListBranch, wantEmitted: 1},
		{name: "else-empty", enabled: falseLiteral, thenBranch: secretListItem, elseBranch: emptyListBranch},
		{name: "then-empty", enabled: "true", thenBranch: emptyListBranch, elseBranch: secretListItem},
		{name: "else-resource", enabled: falseLiteral, thenBranch: emptyListBranch, elseBranch: secretListItem, wantEmitted: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := "let profile =\n  cloudflareOriginCert: " + test.enabled + "\n" +
				"let secret = node:\n  apiVersion: \"v1\"\n  kind: \"Secret\"\n  metadata:\n    name: \"cloudflare-origin-cert\"\n" +
				"let extraResources =\n  if profile.cloudflareOriginCert:\n" + test.thenBranch + "\n  else:\n" + test.elseBranch + "\n" +
				"emit each extraResources\n"
			bundle, err := EvaluateSourceContext(t.Context(), "main.mcl", source)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(bundle.Artifacts); got != test.wantEmitted {
				t.Fatalf("conditional resource count = %d, want %d", got, test.wantEmitted)
			}
			if test.wantEmitted == 1 {
				kind := ""
				resource := nodeContents(bundle.Artifacts[0].Value)
				for _, entry := range resource.Map {
					if entry.Key == "kind" {
						kind = entry.Value.Text
					}
				}
				if kind != "Secret" {
					t.Fatalf("conditional resource kind = %q, want Secret", kind)
				}
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
	metadataKey := &StringExpr{Value: metadataFieldName}
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

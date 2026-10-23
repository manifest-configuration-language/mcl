package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEvaluateAndRenderBundle(t *testing.T) {
	t.Parallel()
	source := `let app = "sample"
let labels =
  app: app
  "app.kubernetes.io/name": app
let deployment =
  apiVersion: "apps/v1"
  kind: "Deployment"
  metadata:
    name: app
    labels: labels
  spec:
    replicas: 2
    selector:
      matchLabels: labels
    template:
      metadata:
        labels: labels
      spec:
        containers:
          - name: "api"
            image: "example.test/api:v1"
emit deployment
`
	module, err := ParseAST(source)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := EvaluateAST(module)
	if err != nil {
		t.Fatal(err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	repeatedOutput, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if string(repeatedOutput) != string(output) {
		t.Fatalf("repeated Render() changed successful output:\nfirst:\n%s\nsecond:\n%s", output, repeatedOutput)
	}
	for _, expected := range []string{`apiVersion: "apps/v1"`, `name: "sample"`, `replicas: 2`, `image: "example.test/api:v1"`} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("rendered output does not contain %q:\n%s", expected, output)
		}
	}
}

func BenchmarkParseAST(b *testing.B) {
	for _, input := range benchmarkAnalysisSources() {
		b.Run(input.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ParseAST(input.source); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEvaluateAST(b *testing.B) {
	for _, input := range benchmarkAnalysisSources() {
		b.Run(input.name, func(b *testing.B) {
			module, err := ParseAST(input.source)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := EvaluateAST(module); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchmarkAnalysisSources() []struct{ name, source string } {
	var deepMapBuilder strings.Builder
	deepMapBuilder.WriteString("let value =\n")
	for depth := range 4 {
		deepMapBuilder.WriteString(strings.Repeat(" ", (depth+1)*2) + "level:\n")
	}
	deepMapBuilder.WriteString(strings.Repeat(" ", 10) + "leaf: 1\n")
	deepMap := deepMapBuilder.String()

	return []struct{ name, source string }{
		{name: "large-integer", source: "let value = " + strings.Repeat("9", 1024) + "\n"},
		{name: "deep-map", source: deepMap},
	}
}

func TestHelmRenderBuildsManifestHookAndCRDBundle(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chartPath := filepath.Join(root, "chart")
	files := map[string]string{
		"Chart.yaml":          "apiVersion: v2\nname: sample\nversion: 1.0.0\n",
		"templates/hook.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}\n  annotations:\n    helm.sh/hook: pre-install\n    helm.sh/hook-weight: \"-2\"\n    helm.sh/hook-delete-policy: hook-succeeded\n",
		"crds/sample.yaml":    "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: samples.example.test\nspec:\n  group: example.test\n  scope: Namespaced\n  names:\n    plural: samples\n    singular: sample\n    kind: Sample\n    listKind: SampleList\n  versions:\n    - name: v1\n      served: true\n      storage: true\n      schema:\n        openAPIV3Schema:\n          type: object\n",
	}
	for name, content := range files {
		path := filepath.Join(chartPath, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	module := &ModuleContext{Context: t.Context(), ProjectRoot: root}
	bundle, err := helmRender([]RuntimeValue{
		{Value: runtimeValueData{Kind: String, Text: "chart"}},
		{Value: runtimeValueData{Kind: Map}, Map: []RuntimeEntry{
			{Key: "kubeVersion", Value: RuntimeValue{Value: runtimeValueData{Kind: String, Text: "1.36.0"}}},
			{Key: "apiVersions", Value: RuntimeValue{Value: runtimeValueData{Kind: List}, List: []RuntimeValue{{Value: runtimeValueData{Kind: String, Text: "apps/v1"}}}}},
		}},
	}, &EvaluationState{SourcePath: filepath.Join(root, "main.mcl"), Module: module})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Kind() != AdapterBundle || len(bundle.List) != 2 {
		t.Fatalf("helm.render returned %#v; want an AdapterBundle with the hook and CRD", bundle)
	}

	var hookFound, crdFound bool
	for _, document := range bundle.List {
		kind := ""
		for _, entry := range document.Map {
			if entry.Key == "kind" {
				kind = entry.Value.TextValue()
			}
		}
		switch kind {
		case "ConfigMap":
			hookFound = true
			lifecycle := document.Metadata().lifecycleMetadata()
			if lifecycle == nil || len(lifecycle.Events) != 1 || lifecycle.Events[0] != phaseBeforeCreate || lifecycle.Priority != -2 || len(lifecycle.Cleanup) != 1 || lifecycle.Cleanup[0] != "on-success" {
				t.Errorf("Helm hook lifecycle = %#v; want before-create event, weight -2, and on-success cleanup", lifecycle)
			}
		case "CustomResourceDefinition":
			crdFound = true
			lifecycle := document.Metadata().lifecycleMetadata()
			if lifecycle == nil || len(lifecycle.Events) != 1 || lifecycle.Events[0] != phaseBootstrap || !lifecycle.Wait {
				t.Errorf("Helm CRD lifecycle = %#v; want bootstrap event with wait enabled", lifecycle)
			}
		}
	}
	if !hookFound || !crdFound {
		t.Errorf("Helm bundle kinds: hook=%t, CRD=%t; want both", hookFound, crdFound)
	}
}

func TestParseNestedListsWithDashOnlyItems(t *testing.T) {
	t.Parallel()

	source := "let values =\n  -\n    - 1\n  -\n    - 2\nlet mapped =\n  nested:\n    -\n      - 3\nlet choice =\n  if true:\n    -\n      - 4\n  else:\n    -\n      - 5\n"
	module, err := ParseAST(source)
	if err != nil {
		t.Fatal(err)
	}
	assertListOfInteger := func(expression Expr, want string) {
		t.Helper()
		list, isList := expression.(*ListExpr)
		if !isList || len(list.Items) != 1 {
			t.Fatalf("list expression = %#v, want a list containing %s", expression, want)
		}
		integer, isInteger := list.Items[0].(*IntegerExpr)
		if !isInteger || integer.Value != want {
			t.Fatalf("list expression = %#v, want a list containing %s", expression, want)
		}
		listSpan, itemSpan := list.ExprSpan(), integer.ExprSpan()
		if listSpan.Start < 0 || listSpan.End > len(source) || listSpan.Start >= listSpan.End || itemSpan.Start < listSpan.Start || itemSpan.End > listSpan.End || source[itemSpan.Start:itemSpan.End] != want {
			t.Fatalf("list and item spans are invalid for %s: list=%#v item=%#v", want, listSpan, itemSpan)
		}
	}
	declaration := func(index int) *LetDeclaration {
		t.Helper()
		value, ok := module.Nodes[index].(*LetDeclaration)
		if !ok || value.Expression == nil {
			t.Fatalf("node %d is not a let declaration with an expression", index)
		}

		return value
	}

	values, isList := declaration(0).Expression.(*ListExpr)
	if !isList || len(values.Items) != 2 {
		t.Fatalf("values expression = %#v, want a List with two nested Lists", values)
	}
	assertListOfInteger(values.Items[0], "1")
	assertListOfInteger(values.Items[1], "2")

	mapped, isMap := declaration(1).Expression.(*MapExpr)
	if !isMap || len(mapped.Entries) != 1 {
		t.Fatalf("mapped expression = %#v, want a Map with one entry", mapped)
	}
	assertNested := func(expression Expr, want string) {
		t.Helper()
		outer, isList := expression.(*ListExpr)
		if !isList || len(outer.Items) != 1 {
			t.Fatalf("nested expression = %#v, want an outer List", expression)
		}
		assertListOfInteger(outer.Items[0], want)
	}
	assertNested(mapped.Entries[0].Value, "3")

	choice, isConditional := declaration(2).Expression.(*ConditionalExpr)
	if !isConditional || choice.Then == nil || choice.Else == nil {
		t.Fatalf("choice expression = %#v, want a conditional with two branches", choice)
	}
	assertNested(choice.Then, "4")
	assertNested(choice.Else, "5")

	formatterSource := "let values =\n  -\n    - 1\n  -\n    - 2\nlet mapped =\n  nested:\n    -\n      - 3\n"
	formatted, err := FormatSource(formatterSource)
	if err != nil {
		t.Fatal(err)
	}
	formattedModule, err := ParseAST(formatted)
	if err != nil {
		t.Fatalf("formatted source did not parse: %v", err)
	}
	formattedValues, ok := formattedModule.Nodes[0].(*LetDeclaration)
	if !ok || formattedValues.Expression == nil {
		t.Fatalf("formatted values expression = %#v, want two nested List items", formattedValues)
	}
	formattedList, isList := formattedValues.Expression.(*ListExpr)
	if !isList || len(formattedList.Items) != 2 {
		t.Fatalf("formatted values expression = %#v, want two nested List items", formattedValues)
	}
	for index, want := range []string{"1", "2"} {
		item, isList := formattedList.Items[index].(*ListExpr)
		if !isList || len(item.Items) != 1 {
			t.Fatalf("formatted nested item = %#v, want a List containing %s", formattedList.Items[index], want)
		}
		integer, isInteger := item.Items[0].(*IntegerExpr)
		if !isInteger || integer.Value != want {
			t.Fatalf("formatted nested item = %#v, want a List containing %s", item, want)
		}
	}
}

func TestParseRejectsAmbiguousOrInvalidMaps(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"let value =\n  name: \"one\"\n  name: \"two\"\n",
		"let value =\n\tname: \"tab\"\n",
		"let value =\n  name: \"one\"\n    extra: \"bad indentation\"\n",
	} {
		if _, err := ParseAST(source); err == nil {
			t.Errorf("ParseAST(%q) succeeded; want an error", source)
		}
	}
	module, err := ParseAST("let value =\n  name: unquoted\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluateAST(module); err == nil {
		t.Fatal("undefined unquoted value succeeded; want an error")
	}
}

func TestEvaluateRejectsMissingReferencesAndFields(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"let value = missing\nemit value\n",
		"let value =\n  name: \"sample\"\nemit value.missing\n",
	} {
		module, err := ParseAST(source)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := EvaluateAST(module); err == nil {
			t.Errorf("EvaluateAST(%q) succeeded; want an error", source)
		}
	}
}

func TestEvaluateReportsSelfAndMutualDefinitionCycles(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"let value = value\nemit value\n",
		"let first = second\nlet second = first\nemit first\n",
	} {
		module, err := ParseAST(source)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := EvaluateAST(module); err == nil || !strings.Contains(err.Error(), "cyclic definitions") {
			t.Errorf("EvaluateAST() error = %v, want a cycle diagnostic", err)
		}
	}
}

func TestFormatSourceNormalizesIndentationAndPreservesComments(t *testing.T) {
	t.Parallel()
	source := "# resource\nlet value =\n    key: \"value\"   # inline\nemit value\n"
	formatted, err := FormatSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(formatted, "# resource") || !strings.Contains(formatted, "# inline") {
		t.Fatalf("formatter removed comments:\n%s", formatted)
	}
	if !strings.Contains(formatted, "  key: \"value\"   # inline") {
		t.Fatalf("formatter did not normalize indentation:\n%s", formatted)
	}
	if _, err := ParseAST(formatted); err != nil {
		t.Fatalf("formatted source is invalid: %v", err)
	}

	nestedSource := "let value =\n    outer:\n# nested\n        key: \"value\"\nemit value\n"
	nestedFormatted, err := FormatSource(nestedSource)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nestedFormatted, "    # nested\n    key: \"value\"") {
		t.Fatalf("formatter did not keep nested trivia with its block:\n%s", nestedFormatted)
	}

	callSource := "let value = length(\n# argument\n  \"sample\",\n)\nemit value\n"
	callFormatted, err := FormatSource(callSource)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(callFormatted, "length(\n  # argument\n  \"sample\"") {
		t.Fatalf("formatter did not keep call trivia with its declaration:\n%s", callFormatted)
	}
}

func TestNumberSyntaxRejectsLeadingZeroes(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"let value = 0123\n", "let value = 01.5\n", "let value = +1\n"} {
		if _, err := ParseAST(source); err == nil {
			t.Errorf("ParseAST(%q) succeeded; want an error", source)
		}
	}
	if _, err := ParseAST("let value = 123456789012345678901234567890\n"); err != nil {
		t.Fatalf("large exact integer was rejected: %v", err)
	}
}

func TestLintTracksValuesReachableFromEmittedBundle(t *testing.T) {
	t.Parallel()
	module, err := ParseAST("let used = \"value\"\nlet transitivelyUsed = used\nlet unused = \"unused\"\nemit transitivelyUsed\n")
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := LintAST(module)
	if len(diagnostics) != 1 || diagnostics[0].Line != 3 {
		t.Fatalf("LintAST() = %#v, want only the unreachable definition at line 3", diagnostics)
	}
}

func TestRenderSeparatesEmittedDocumentsOnce(t *testing.T) {
	t.Parallel()
	module, err := ParseAST("let first = \"one\"\nlet second = \"two\"\nemit first\nemit second\n")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := EvaluateAST(module)
	if err != nil {
		t.Fatal(err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(output), "---") != 1 {
		t.Fatalf("rendered output contains an unexpected document separator:\n%s", output)
	}
}

func TestWithMergesMapsAndReplacesLists(t *testing.T) {
	t.Parallel()
	source := `let defaults =
  securityContext:
    allowPrivilegeEscalation: false
    capabilities:
      drop:
        - "ALL"
  ports:
    - 8080
let container = defaults with:
  name: "api"
  securityContext:
    capabilities:
      add:
        - "NET_BIND_SERVICE"
  ports:
    - 9090
emit defaults
emit container
`
	module, err := ParseAST(source)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := EvaluateAST(module)
	if err != nil {
		t.Fatal(err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`allowPrivilegeEscalation: false`, `drop:`, `- "ALL"`, `add:`, `- "NET_BIND_SERVICE"`, `- 9090`, `name: "api"`} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("rendered output does not contain %q:\n%s", expected, output)
		}
	}
	if strings.Count(string(output), "- 8080") != 1 || strings.Count(string(output), "- 9090") != 1 {
		t.Errorf("with changed the base list or failed to replace it in the merged value:\n%s", output)
	}
	if strings.Count(string(output), "add:") != 1 || strings.Count(string(output), `name: "api"`) != 1 {
		t.Errorf("with changed the base map while building the merged value:\n%s", output)
	}
}

func TestCloneAssignsDistinctNodeIdentity(t *testing.T) {
	t.Parallel()
	source := `let original = node:
  apiVersion: "v1"
  kind: "ConfigMap"
  metadata:
    name: "sample"
let cloned = clone(original)
emit original
emit cloned
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Artifacts) != 2 {
		t.Fatalf("EvaluateSource() emitted %d artifacts; want original and clone", len(bundle.Artifacts))
	}
	originalIdentity := bundle.Artifacts[0].Value.NodeID
	clonedIdentity := bundle.Artifacts[1].Value.NodeID
	if originalIdentity == nil || clonedIdentity == nil {
		t.Fatalf("Node identities = (%p, %p); want both identities", originalIdentity, clonedIdentity)
	}
	if originalIdentity == clonedIdentity {
		t.Fatal("clone(original) retained the original Node identity")
	}
}

func TestWithRejectsMismatchedValueTypes(t *testing.T) {
	t.Parallel()
	module, err := ParseAST("let base =\n  value: {}\nlet merged = base with:\n  value: \"replacement\"\nemit merged\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluateAST(module); err == nil {
		t.Fatal("with replaced a map with a string; want an error")
	}
}

func TestWithoutRemovesFieldPathsWithoutChangingTheBase(t *testing.T) {
	t.Parallel()
	source := `let base =
  securityContext:
    allowPrivilegeEscalation: false
    capabilities:
      drop:
        - "ALL"
  name: "api"
let modified = base without:
  - .securityContext.capabilities.drop
emit base
emit modified
`
	module, err := ParseAST(source)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := EvaluateAST(module)
	if err != nil {
		t.Fatal(err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(output), "drop:") != 1 {
		t.Fatalf("without changed the original value or retained the removed path:\n%s", output)
	}
}

func TestWithoutRejectsMissingFieldPaths(t *testing.T) {
	t.Parallel()
	module, err := ParseAST("let base =\n  name: \"api\"\nlet modified = base without:\n  - .missing\nemit modified\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluateAST(module); err == nil {
		t.Fatal("without removed a missing path; want an error")
	}
}

func TestEvaluateFileImportsOnlyExplicitExports(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modules := filepath.Join(root, "modules")
	if err := os.MkdirAll(modules, 0o755); err != nil {
		t.Fatal(err)
	}
	module := "let privateValue = \"hidden\"\nexport let defaults =\n  securityContext:\n    allowPrivilegeEscalation: false\n"
	if err := os.WriteFile(filepath.Join(modules, "defaults.mcl"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	source := "import \"./modules/defaults.mcl\" as shared\nlet container = shared.defaults with:\n  name: \"api\"\nemit container\n"
	bundle, err := EvaluateSource(filepath.Join(root, "main.mcl"), source)
	if err != nil {
		t.Fatal(err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `name: "api"`) || !strings.Contains(string(output), "allowPrivilegeEscalation: false") {
		t.Fatalf("imported exports were not available:\n%s", output)
	}

	privateSource := "import \"./modules/defaults.mcl\" as shared\nemit shared.privateValue\n"
	parsedModule, err := ParseAST(privateSource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluateSource(filepath.Join(root, "private.mcl"), privateSource); err == nil {
		t.Fatal("private module value was accessible; want an error")
	}
	if _, err := EvaluateAST(parsedModule); err == nil {
		t.Fatal("Evaluate unexpectedly accepted an import without a module path")
	}
}

func TestEvaluateFileRejectsImportCyclesAndPathEscapes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.mcl"), []byte("import \"./b.mcl\" as b\nexport let value = \"a\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.mcl"), []byte("import \"./a.mcl\" as a\nexport let value = \"b\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluateFile(filepath.Join(root, "a.mcl")); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("EvaluateFile() error = %v, want an import cycle diagnostic", err)
	}
	if _, err := EvaluateSource(filepath.Join(root, "main.mcl"), "import \"../outside.mcl\" as outside\n"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("EvaluateSource() error = %v, want a path escape diagnostic", err)
	}
}

func TestImportedModuleCannotEmit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "library.mcl"), []byte("let value = \"hidden output\"\nemit value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluateSource(filepath.Join(root, "main.mcl"), "import \"./library.mcl\" as library\nemit library.value\n"); err == nil || !strings.Contains(err.Error(), "cannot emit") {
		t.Fatalf("EvaluateSource() error = %v, want imported emit diagnostic", err)
	}
}

func TestImportedModuleCannotValidate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	module := "rule valid(value: String):\n  require value == \"valid\"\nemit \"entry\"\nvalidate \"invalid\" using valid\n"
	if err := os.WriteFile(filepath.Join(root, "library.mcl"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	source := "import \"./library.mcl\" as library\n"
	if _, err := EvaluateSource(filepath.Join(root, "main.mcl"), source); err == nil {
		t.Fatal("EvaluateSource() succeeded after silently dropping imported validation")
	}
}

func TestEmitEachExpandsListsAndKubernetesValidationRejectsListArtifacts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	listSource := "let values =\n  - \"one\"\n  - \"two\"\nemit each values\n"
	bundle, err := EvaluateSource(filepath.Join(root, "each.mcl"), listSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Artifacts) != 2 {
		t.Fatalf("EvaluateSource() artifacts = %d, want 2", len(bundle.Artifacts))
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(output), "---") != 1 {
		t.Fatalf("Render() emitted %d YAML documents, want 2:\n%s", strings.Count(string(output), "---")+1, output)
	}

	ordinaryList, err := EvaluateSource(filepath.Join(root, "list.mcl"), "let values =\n  - \"one\"\n  - \"two\"\nemit values\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBundleOutput(ordinaryList); err == nil || !strings.Contains(err.Error(), "emit each") {
		t.Fatalf("ValidateBundleOutput() error = %v, want emit each guidance", err)
	}
}

func TestRuleComparisonResolvesDeferredReferencesBeforeApplyingOperator(t *testing.T) {
	t.Parallel()
	source := `let config = node:
  metadata:
    name: "api"
rule matches(value: String):
  require value == ref(config).metadata.name
emit config
validate "api" using matches
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source); err != nil {
		t.Fatalf("EvaluateSource() error = %v, want deferred reference comparison to pass", err)
	}
}

func TestDeferredReferencesResolveBeforeExpressionOperators(t *testing.T) {
	t.Parallel()
	source := `import "std:list" as lists
let config = node:
  enabled: true
  count: 2
  optional: null
  items:
    - "a"
    - "b"
let values =
  - 1
  - 2
  - 3
let increment = fn(value) => value + 1
let arithmetic = ref(config).count + 1
let comparison = ref(config).count == 2
let conjunction = ref(config).enabled and true
let fallback = ref(config).optional ?? "fallback"
let selected = if ref(config).enabled:
  "enabled"
else:
  "disabled"
let mapped = lists.map(values, fn(value) => value + ref(config).count)
let mappedNamed = lists.map(values, increment)
let mappedValue = mappedNamed[0] + 1
let filtered = lists.filter(values, fn(value) => value == ref(config).count)
let single = lists.exactlyOne(values, fn(value) => value == ref(config).count)
let itemCount = len(ref(config).items)
let called = increment(ref(config).count)
emit config
emit arithmetic
emit comparison
emit conjunction
emit fallback
emit selected
emit len(mapped)
emit mappedValue
emit len(filtered)
emit single
emit itemCount
emit called
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v, want deferred expressions to resolve: %v", err, err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"3", "true", `"fallback"`, `"enabled"`, "1", "2"} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("rendered output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestDeferredReferencesPropagateThroughCompositeExpressions(t *testing.T) {
	t.Parallel()
	source := `let config = node:
  enabled: true
  values:
    - "first"
    - "second"
  details:
    keep: "kept"
    remove: "removed"
let negated = not ref(config).enabled
let indexed = ref(config).values[1]
let copied = for item in ref(config).values:
  item
let merged = ref(config).details with:
  added: "added"
let removed = ref(config).details without:
  - .remove
emit config
emit negated
emit indexed
emit len(copied)
emit merged
emit removed
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v, want deferred references in composite expressions to resolve", err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"false", `"second"`, "2", `added: "added"`, `keep: "kept"`} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("rendered output does not contain %q:\n%s", expected, output)
		}
	}
	if got := strings.Count(string(output), `remove: "removed"`); got != 2 {
		t.Errorf("without removed field count = %d, want it only in config and merged outputs:\n%s", got, output)
	}
}

func TestDynamicMapKeyResolvesAgainstCapturedScope(t *testing.T) {
	t.Parallel()
	source := `let target = node:
  metadata:
    name: "chosen"
let labels =
  key(ref(target).metadata.name): "value"
emit target
emit labels
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v, want deferred key to resolve: %v", err, err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `chosen: "value"`) {
		t.Fatalf("rendered output does not contain the resolved dynamic key:\n%s", output)
	}
	duplicateSource := `let target = node:
  metadata:
    name: "chosen"
let labels =
  key(ref(target).metadata.name): "value"
  chosen: "other"
emit target
emit labels
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), duplicateSource); err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("EvaluateSource() error = %v, want duplicate key diagnostic", err)
	}
}

func TestKnownKubernetesKindWithUnknownVersionPassesWithoutSchema(t *testing.T) {
	t.Parallel()
	source := `let resource =
  apiVersion: "apps/v99"
  kind: "Deployment"
  metadata:
    name: "sample"
emit resource
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source); err != nil {
		t.Fatalf("EvaluateSource() error = %v, want unsupported GVK to pass without schema validation", err)
	}
}

func TestUnknownKubernetesResourceWithoutSchemaPassesValidation(t *testing.T) {
	t.Parallel()
	source := `let resource =
  apiVersion: "example.test/v1"
  kind: "Widget"
  metadata:
    name: "sample"
emit resource
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source); err != nil {
		t.Fatalf("EvaluateSource() error = %v, want unknown GVK to pass without schema validation", err)
	}
}

func TestAdditionalCatalogResourceWithoutSchemaPassesValidation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	config := `[kubernetes.resourceCatalog]
revision = "mcl-kubernetes-resource-catalog-v1"

[[kubernetes.resourceCatalog.additionalResources]]
apiGroup = "widgets.example.test"
kind = "Widget"
resourceName = "widgets"
scope = "Namespaced"
`
	if err := os.WriteFile(filepath.Join(root, "mcl.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	source := `let resource =
  apiVersion: "widgets.example.test/v1"
  kind: "Widget"
  metadata:
    name: "sample"
emit resource
`
	if _, err := EvaluateSource(filepath.Join(root, "main.mcl"), source); err != nil {
		t.Fatalf("EvaluateSource() error = %v, want registered resource without schema to pass", err)
	}
}

func TestCustomResourceWithInBundleSchemaPassesValidation(t *testing.T) {
	t.Parallel()
	source := `let crd =
  apiVersion: "apiextensions.k8s.io/v1"
  kind: "CustomResourceDefinition"
  metadata:
    name: "widgets.example.test"
  spec:
    group: "example.test"
    scope: "Namespaced"
    names:
      plural: "widgets"
      singular: "widget"
      kind: "Widget"
      listKind: "WidgetList"
    versions:
      - name: "v1"
        served: true
        storage: true
        schema:
          openAPIV3Schema:
            type: "object"
            properties:
              apiVersion:
                type: "string"
              kind:
                type: "string"
              metadata:
                type: "object"
              spec:
                type: "object"
                properties:
                  size:
                    type: "integer"
let resource =
  apiVersion: "example.test/v1"
  kind: "Widget"
  metadata:
    name: "sample"
  spec:
    size: 2
emit crd
emit resource
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source); err != nil {
		t.Fatalf("EvaluateSource() error = %v, want the in-bundle CRD schema to validate its resource", err)
	}
}

func TestListTransformationsReturnOrdinaryListsForAdapterBundles(t *testing.T) {
	t.Parallel()
	bundle := RuntimeValue{
		Value: runtimeValueData{Kind: AdapterBundle},
		List:  []RuntimeValue{{Value: runtimeValueData{Kind: String, Text: "one"}}},
	}
	lambda := RuntimeValue{
		Value:  runtimeValueData{Kind: Lambda},
		Lambda: &typedLambda{Parameters: []string{paramValue}, Scope: map[string]RuntimeValue{}, Body: typedExpression{kind: typedExpressionReference, text: paramValue}},
	}
	state := &EvaluationState{Budget: &functionBudget{}, Allocations: &runtimeAllocationBudget{}}
	mapped, err := callRuntimeStandard(fnMap, []RuntimeValue{bundle, lambda}, nil, state)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.Kind() != List {
		t.Fatalf("map(AdapterBundle, fn) Kind() = %s, want List", kindName(mapped.Kind()))
	}
	expression := typedExpression{
		kind:       typedExpressionFor,
		loopName:   "value",
		left:       &typedExpression{kind: typedExpressionReference, text: "items"},
		thenBranch: &typedExpression{kind: typedExpressionReference, text: "value"},
	}
	state.Environment = map[string]RuntimeValue{"items": bundle}
	iterated, err := evaluateRuntimeExpression(expression, state)
	if err != nil {
		t.Fatal(err)
	}
	if iterated.Kind() != List {
		t.Fatalf("for over AdapterBundle Kind() = %s, want List", kindName(iterated.Kind()))
	}
	concatenated, err := callRuntimeStandard(fnConcat, []RuntimeValue{bundle, {Value: runtimeValueData{Kind: List}}}, nil, state)
	if err != nil {
		t.Fatal(err)
	}
	if concatenated.Kind() != List {
		t.Fatalf("concat(AdapterBundle, List) Kind() = %s, want List", kindName(concatenated.Kind()))
	}
	replaced, err := callRuntimeStandard(fnReplaceAt, []RuntimeValue{
		bundle, {Value: runtimeValueData{Kind: Integer, Text: "0"}}, {Value: runtimeValueData{Kind: String, Text: "two"}},
	}, nil, state)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Kind() != List {
		t.Fatalf("replaceAt(AdapterBundle, ...) Kind() = %s, want List", kindName(replaced.Kind()))
	}
}

func TestStandardFunctionsOperateOnGenericValues(t *testing.T) {
	t.Parallel()
	source := `import "std:map" as maps
import "std:list" as lists
let config =
  data:
    APP_MODE: "production"
let names =
  - "api"
  - "worker"
let extraNames =
  - "proxy"
let result =
  key: maps.keyOf(config.data, "APP_MODE")
  hasWorker: lists.contains(names, "worker")
  count: len(names)
  unique: lists.union(names, extraNames)
emit result
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatal(err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`key: "APP_MODE"`, "hasWorker: true", "count: 2", `- "proxy"`} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("standard library output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestFunctionTypesAcceptMultipleArguments(t *testing.T) {
	t.Parallel()
	source := `type Formatter = Function<Int, String, Bool, String>
fn format(count: Int, name: String, enabled: Bool) -> String = name
let callback: Formatter = format
fn add(accumulator: Int, item: Int) -> Int = accumulator + item
let combine: Function<Int, Int, Int> = add
let values =
  - 1
  - 2
let total = fold(values, 0, combine)
emit callback(2, "api", true)
emit total
`
	module, err := ParseAST(source)
	if err != nil {
		t.Fatal(err)
	}
	types, err := typeDefinitionsAST(module)
	if err != nil {
		t.Fatal(err)
	}
	resolved, known := resolveStaticAlias(ResolvedType{Name: "Formatter"}, types, make(map[string]bool))
	if !known || len(resolved.Parameters) != 3 {
		t.Fatalf("resolved function type = %#v, want three parameters", resolved)
	}
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v, want a three-argument function type: %v", err, err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"api"`) || !strings.Contains(string(output), "3") {
		t.Fatalf("Render() = %s, want function result", output)
	}

	wrongArity := strings.Replace(source, `callback(2, "api", true)`, `callback(2, "api")`, 1)
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "wrong-arity.mcl"), wrongArity); err == nil {
		t.Fatal("EvaluateSource() succeeded for a call with too few function arguments")
	}
}

func TestValidateEachRequiresMatchesUnlessZeroIsExplicit(t *testing.T) {
	t.Parallel()
	rule := "type IntMap = Map<String, Int>\nrule selected(value: Map<String, Int>):\n  require true\n"
	noMatches := rule + "emit \"ready\"\nvalidate each descendants(output, IntMap) using selected\n"
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "no-matches.mcl"), noMatches); err == nil || !strings.Contains(err.Error(), "fewer than the required 1") {
		t.Fatalf("EvaluateSource() error = %v, want zero matches to fail by default", err)
	}
	allowEmpty := rule + "emit \"ready\"\nvalidate each descendants(output, IntMap, minMatches = 0) using selected\n"
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "allow-empty.mcl"), allowEmpty); err != nil {
		t.Fatalf("EvaluateSource() error = %v, want explicit minMatches = 0 to allow an empty selection", err)
	}
	oneMatch := rule + "let resource =\n  count: 1\nemit resource\nvalidate each descendants(output, IntMap) using selected\n"
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "one-match.mcl"), oneMatch); err != nil {
		t.Fatalf("EvaluateSource() error = %v, want one match to pass", err)
	}
}

func TestValidateEachTypeQueryReportsRuleTypeMismatchForAnnotatedCandidate(t *testing.T) {
	t.Parallel()
	queryType := ResolvedType{Name: typeMap, Element: &ResolvedType{Name: typeInt}}
	candidate := RuntimeValue{
		Value: runtimeValueData{Kind: Map, Typing: &ValueTyping{TypeProvenance: []TypeProvenance{{Type: queryType}}}},
		Map:   []RuntimeEntry{{Key: "name", Value: RuntimeValue{Value: runtimeValueData{Kind: String, Text: "api"}}}},
	}
	rule := RuntimeRule{
		Name:                 "selected",
		ParameterDefinitions: []RuleParameter{{Name: "value", Type: queryType}},
	}
	bundle := RuntimeBundle{Artifacts: []RuntimeArtifact{{Value: candidate}}, PendingValidations: []RuntimeValidation{{
		Rule: rule, Line: 1, Each: true, QueryType: &queryType, MinMatches: 0, MinMatchesSet: true,
	}}}
	state := &EvaluationState{Budget: &functionBudget{}, Allocations: &runtimeAllocationBudget{}}
	if err := resolveRuntimeBundle(&bundle, state); err == nil || !strings.Contains(err.Error(), "does not match its type") {
		t.Fatalf("resolveRuntimeBundle() error = %v, want a Rule argument type diagnostic", err)
	}
}

func TestMergeByKeyUpdatesMatchesAndAppendsNewEntries(t *testing.T) {
	t.Parallel()
	source := `let base =
  - name: "api"
    image: "example.test/api:v1"
  - name: "worker"
    image: "example.test/worker:v1"
let overrides =
  - name: "api"
    image: "example.test/api:v2"
  - name: "proxy"
    image: "example.test/proxy:v1"
let result = mergeByKey(base, overrides, "name")
emit each result
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatal(err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(output), "name:") != 3 || !strings.Contains(string(output), `image: "example.test/api:v2"`) || !strings.Contains(string(output), `name: "proxy"`) {
		t.Fatalf("mergeByKey returned unexpected items:\n%s", output)
	}
}

func TestMergeByKeyAcceptsNodeWrappedMaps(t *testing.T) {
	t.Parallel()
	createNode := func(name, image string) RuntimeValue {
		contents := RuntimeValue{Value: runtimeValueData{Kind: Map}, Map: []RuntimeEntry{
			{Key: "name", Value: RuntimeValue{Value: runtimeValueData{Kind: String, Text: name}}},
			{Key: "image", Value: RuntimeValue{Value: runtimeValueData{Kind: String, Text: image}}},
		}}

		return RuntimeValue{Value: runtimeValueData{Kind: Node, NodeID: &NodeIdentity{}}, Base: &contents}
	}
	base := RuntimeValue{Value: runtimeValueData{Kind: List}, List: []RuntimeValue{createNode("api", "v1")}}
	overrides := RuntimeValue{Value: runtimeValueData{Kind: List}, List: []RuntimeValue{createNode("api", "v2")}}
	result, err := mergeByKey(base, overrides, RuntimeValue{Value: runtimeValueData{Kind: String, Text: "name"}})
	if err != nil {
		t.Fatalf("mergeByKey() error = %v, want Node-wrapped Maps to merge", err)
	}
	merged, exists := runtimeMapValue(result.List[0], "image")
	if !exists || merged.TextValue() != "v2" {
		t.Fatalf("mergeByKey() image = %#v, want v2", merged)
	}
}

func TestKeyOfReportsMissingKeys(t *testing.T) {
	t.Parallel()
	source := `let config =
  data:
    MODE: "production"
let key = keyOf(config.data, "MISSING")
emit key
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source); err == nil || !strings.Contains(err.Error(), "available keys: MODE") {
		t.Fatalf("EvaluateSource() error = %v, want available-key diagnostics", err)
	}
}

func TestUnionRemovesDuplicatesFromBothLists(t *testing.T) {
	t.Parallel()
	source := `let repeated =
  - "api"
  - "api"
let result = union(repeated, repeated)
emit each result
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatal(err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(output), `"api"`) != 1 {
		t.Fatalf("union did not remove duplicates from its inputs:\n%s", output)
	}
}

func TestMergeByKeyRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()
	source := `let base =
  - name: "api"
  - name: "api"
let result = mergeByKey(base, [], "name")
emit result
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source); err == nil || !strings.Contains(err.Error(), "duplicate value") {
		t.Fatalf("EvaluateSource() error = %v, want duplicate merge key diagnostic", err)
	}
}

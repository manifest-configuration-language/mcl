package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manifest-configuration-language/mcl/internal/sourceindex"
	"github.com/manifest-configuration-language/mcl/pkg/mcl"
)

func TestDiffParsesInterspersedFlagsWithBundleCRDs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for name, resourceName := range map[string]string{"before": "sample", "after": "updated"} {
		directory := filepath.Join(root, name)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		source := "let crd =\n" +
			"  apiVersion: \"apiextensions.k8s.io/v1\"\n" +
			"  kind: \"CustomResourceDefinition\"\n" +
			"  metadata:\n" +
			"    name: \"widgets.example.test.walnuts.dev\"\n" +
			"  spec:\n" +
			"    group: \"example.test.walnuts.dev\"\n" +
			"    scope: \"Namespaced\"\n" +
			"    names:\n" +
			"      plural: \"widgets\"\n" +
			"      singular: \"widget\"\n" +
			"      kind: \"Widget\"\n" +
			"      listKind: \"WidgetList\"\n" +
			"    versions:\n" +
			"      - name: \"v1\"\n" +
			"        served: true\n" +
			"        storage: true\n" +
			"        schema:\n" +
			"          openAPIV3Schema:\n" +
			"            type: \"object\"\n" +
			"            properties:\n" +
			"              apiVersion:\n" +
			"                type: \"string\"\n" +
			"              kind:\n" +
			"                type: \"string\"\n" +
			"              metadata:\n" +
			"                type: \"object\"\n" +
			"let widget =\n  apiVersion: \"example.test.walnuts.dev/v1\"\n  kind: \"Widget\"\n  metadata:\n    name: \"" + resourceName + "\"\nemit crd\nemit widget\n"
		if err := os.WriteFile(filepath.Join(directory, "main.mcl"), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := filepath.Join(root, "before", "main.mcl")
	after := filepath.Join(root, "after", "main.mcl")
	if err := run(t.Context(), []string{"diff", before, "--operation", "create", after}); err != nil {
		t.Fatalf("run(diff) error = %v", err)
	}
}

func TestRenderCommandMatchesBundlePhaseOutput(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "main.mcl")
	source := `let resource =
  apiVersion: "v1"
  kind: "ConfigMap"
  metadata:
    name: "sample"
let hook = lifecycle.attach(resource, ["before.create"], 2, false, [])
emit hook
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := mcl.EvaluateSource(path, source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v", err)
	}

	for _, phase := range []string{"main", "before.create"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			command := newRootCommand([]string{"render", path, "--phase", phase})
			command.SetOut(&output)
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatalf("render command error = %v", err)
			}
			want, err := mcl.RenderPhase(bundle, phase)
			if err != nil {
				t.Fatalf("RenderPhase(%q) error = %v", phase, err)
			}
			if !bytes.Equal(output.Bytes(), want) {
				t.Fatalf("render command output differs from RenderPhase(%q):\n got: %s\nwant: %s", phase, output.Bytes(), want)
			}
		})
	}
}

func TestParseParameterInputs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	firstPath := filepath.Join(root, "cluster.json")
	if err := os.WriteFile(firstPath, []byte(`{"cluster":{"version":"1.35"},"enabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(root, "settings.json")
	if err := os.WriteFile(secondPath, []byte(`{"limits":{"cpu":"2"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	parameters, sources, err := parseParameterInputs([]string{`replicas=3`}, []string{firstPath, secondPath})
	if err != nil {
		t.Fatalf("parseParameterInputs() error = %v", err)
	}
	for name, want := range map[string]string{
		"cluster":  `{"version":"1.35"}`,
		"enabled":  "true",
		"limits":   `{"cpu":"2"}`,
		"replicas": "3",
	} {
		if got := string(parameters[name]); got != want {
			t.Errorf("parameter %q = %s, want %s", name, got, want)
		}
	}
	if sources["cluster"].SourcePath != firstPath || sources["cluster"].Locations["/cluster/version"].Span.End <= sources["cluster"].Locations["/cluster/version"].Span.Start {
		t.Fatalf("cluster source locations = %#v, want source path and nested value span", sources["cluster"])
	}
	if sources["replicas"].Label != "--parameter replicas" || sources["replicas"].SourcePath != "" {
		t.Fatalf("replicas source = %#v, want inline parameter label without a file location", sources["replicas"])
	}

	duplicatePath := filepath.Join(root, "duplicate.json")
	if err := os.WriteFile(duplicatePath, []byte(`{"cluster":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = parseParameterInputs(nil, []string{firstPath, duplicatePath})
	if err == nil || !strings.Contains(err.Error(), `parameter "cluster" was provided more than once`) {
		t.Fatalf("duplicate parameter error = %v", err)
	}
	_, _, err = parseParameterInputs([]string{`cluster={}`}, []string{firstPath})
	if err == nil || !strings.Contains(err.Error(), `--parameter-file`) {
		t.Fatalf("parameter and file duplicate error = %v", err)
	}
}

func TestParseParameterInputsRequiresJSONObject(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"array":          `[]`,
		"scalar":         `true`,
		"null":           `null`,
		"invalid":        `{"key":`,
		"duplicate keys": `{"key":1,"key":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "parameters.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := parseParameterInputs(nil, []string{path})
			if err == nil {
				t.Fatal("parseParameterInputs() succeeded for non-object or invalid JSON")
			}
		})
	}
}

func TestCheckUsesParameterFileAndEnforcesType(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sourcePath := filepath.Join(root, "main.mcl")
	source := "param cluster: String\nlet resource =\n  apiVersion: \"v1\"\n  kind: \"ConfigMap\"\n  metadata:\n    name: cluster\nemit resource\n"
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	parameterPath := filepath.Join(root, "parameters.json")
	parameterSource := "{\n  \"cluster\": 42\n}\n"
	if err := os.WriteFile(parameterPath, []byte(parameterSource), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(t.Context(), []string{"check", sourcePath, "--parameter-file", parameterPath})
	if err == nil || !strings.Contains(err.Error(), "expected: String") || !strings.Contains(err.Error(), "actual: Int") || !strings.Contains(err.Error(), "pointer: /cluster") {
		t.Fatalf("run(check --parameter-file) error = %v, want parameter type error", err)
	}
	diagnostic, ok := mcl.SourceDiagnosticFromError(err)
	if !ok {
		t.Fatalf("SourceDiagnosticFromError(%v) = false", err)
	}
	valueStart := strings.Index(parameterSource, "42")
	if diagnostic.Path != parameterPath || diagnostic.Span != (mcl.SourceSpan{Start: valueStart, End: valueStart + 2}) {
		t.Fatalf("parameter diagnostic location = (%q, %+v), want (%q, %d-%d)", diagnostic.Path, diagnostic.Span, parameterPath, valueStart, valueStart+2)
	}
	if !strings.Contains(diagnostic.Message, "source: "+parameterPath) || !strings.Contains(diagnostic.Message, "path: cluster") {
		t.Fatalf("parameter diagnostic message = %q, want source and display path", diagnostic.Message)
	}
	if len(diagnostic.Related) == 0 || filepath.Base(diagnostic.Related[0].Path) != filepath.Base(sourcePath) {
		t.Fatalf("parameter declaration related locations = %#v, want MCL declaration", diagnostic.Related)
	}
}

func TestParameterDiagnosticPrintsLocationWithoutSourceSnippet(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sourcePath := filepath.Join(root, "main.mcl")
	source := "param cluster: String\nparam token: String\nlet resource =\n  apiVersion: \"v1\"\n  kind: \"ConfigMap\"\n  metadata:\n    name: cluster\nemit resource\n"
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	parameterPath := filepath.Join(root, "parameters.json")
	const secret = "PARAMETER_SECRET_SENTINEL"
	parameterSource := `{"cluster":42,"token":"` + secret + `"}`
	if err := os.WriteFile(parameterPath, []byte(parameterSource), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(t.Context(), []string{"check", sourcePath, "--parameter-file", parameterPath})
	if err == nil {
		t.Fatal("run(check --parameter-file) succeeded with an invalid parameter type")
	}
	diagnostic, ok := mcl.SourceDiagnosticFromError(err)
	if !ok {
		t.Fatalf("SourceDiagnosticFromError(%v) = false", err)
	}
	var output bytes.Buffer
	writeSourceDiagnostic(&output, diagnostic, map[string]indexedSource{
		parameterPath: {content: []byte(parameterSource), index: sourceindex.New(parameterSource)},
		sourcePath:    {content: []byte(source), index: sourceindex.New(source)},
	})
	text := output.String()
	if strings.Contains(text, secret) {
		t.Fatalf("diagnostic output contains parameter source secret %q: %s", secret, text)
	}
	if !strings.Contains(text, parameterPath+":1:") || !strings.Contains(text, "pointer: /cluster") {
		t.Fatalf("diagnostic output = %q, want parameter file line/column and JSON Pointer", text)
	}
}

func TestFormatCheckAndDenyLintWarnings(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	formatPath := filepath.Join(root, "format.mcl")
	if err := os.WriteFile(formatPath, []byte("let value =\n    key: \"value\"\nemit value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := formatCommand([]string{formatPath, "--check"}); err == nil {
		t.Fatal("formatCommand(--check) succeeded for unformatted source")
	}

	lintPath := filepath.Join(root, "lint.mcl")
	if err := os.WriteFile(lintPath, []byte("let unused = \"value\"\nemit \"output\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lintCommand([]string{lintPath, "--deny-warnings"}); err == nil {
		t.Fatal("lintCommand(--deny-warnings) succeeded with a warning")
	}
}

func TestFormatWritePreservesFilePermissions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "format.mcl")
	if err := os.WriteFile(path, []byte("let value =\n    key: \"value\"\nemit value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := formatCommand([]string{"-w", path}); err != nil {
		t.Fatalf("formatCommand(-w) error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("formatted file permissions = %#o, want %#o", got, 0o640)
	}
}

func TestOperationOutputPermissionsAndRefusalPreserveExistingFiles(t *testing.T) {
	t.Parallel()

	outputDir := filepath.Join(t.TempDir(), "output")
	files := []mcl.RenderedFile{{Name: "resource.yaml", Content: []byte("kind: ConfigMap\n")}}
	if err := writeOperationFiles(outputDir, files); err != nil {
		t.Fatalf("writeOperationFiles() error = %v", err)
	}
	info, err := os.Stat(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("operation output directory permissions = %#o, want %#o", got, 0o700)
	}
	for _, name := range []string{"resource.yaml", operationOutputManifestName} {
		info, err := os.Stat(filepath.Join(outputDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("operation output %q permissions = %#o, want %#o", name, got, 0o600)
		}
	}

	resourcePath := filepath.Join(outputDir, "resource.yaml")
	if err := os.WriteFile(resourcePath, []byte("locally modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeOperationFiles(outputDir, []mcl.RenderedFile{{Name: "resource.yaml", Content: []byte("kind: Secret\n")}}); err == nil {
		t.Fatal("writeOperationFiles() replaced a modified managed output")
	}
	content, err := os.ReadFile(resourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "locally modified\n" {
		t.Fatalf("refused replacement changed existing output to %q", content)
	}
}

package engine

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/manifest-configuration-language/mcl/internal/helmrender"
)

const (
	helmFSTestChartSource = "charts/app"
	helmKubeVersionOption = "kubeVersion"
)

func TestHelmRenderReadsRootRelativeProjectFS(t *testing.T) {
	t.Parallel()

	root := filepath.Clean(t.TempDir())
	files := fstest.MapFS{
		"charts/app/Chart.yaml":               &fstest.MapFile{Data: []byte("apiVersion: v2\nname: app\nversion: 1.0.0\n")},
		"charts/app/templates/configmap.yaml": &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}\n")},
	}
	module := &ModuleContext{
		Context:     t.Context(),
		ProjectRoot: root,
		filesystem:  newProjectFilesystem(root, files),
	}
	bundle, err := helmRender([]RuntimeValue{
		{Value: runtimeValueData{Kind: String, Text: helmFSTestChartSource}},
		{Value: runtimeValueData{Kind: Map}, Map: []RuntimeEntry{{
			Key: helmKubeVersionOption, Value: RuntimeValue{Value: runtimeValueData{Kind: String, Text: testKubeVersion}},
		}, {
			Key: helmAPIVersionsOption, Value: RuntimeValue{Value: runtimeValueData{Kind: List}},
		}}},
	}, &EvaluationState{SourcePath: filepath.Join(root, "main.mcl"), Module: module})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Kind() != AdapterBundle || len(bundle.List) != 1 {
		t.Fatalf("helm.render returned %#v; want one rendered resource", bundle)
	}
	var kind, name string
	for _, entry := range bundle.List[0].Map {
		switch entry.Key {
		case "kind":
			kind = entry.Value.TextValue()
		case metadataFieldName:
			for _, metadataEntry := range entry.Value.Map {
				if metadataEntry.Key == "name" {
					name = metadataEntry.Value.TextValue()
				}
			}
		}
	}
	if kind != "ConfigMap" || name != "mcl" {
		t.Fatalf("rendered resource kind/name = %q/%q; want ConfigMap/mcl", kind, name)
	}
}

func TestHelmRenderAllowNondeterministicOutput(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(t.TempDir())
	files := fstest.MapFS{
		"charts/app/Chart.yaml":            &fstest.MapFile{Data: []byte("apiVersion: v2\nname: app\nversion: 1.0.0\n")},
		"charts/app/templates/secret.yaml": &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: generated\ndata:\n  value: {{ randAlphaNum 24 | b64enc | quote }}\n")},
	}
	arguments := []RuntimeValue{
		{Value: runtimeValueData{Kind: String, Text: helmFSTestChartSource}},
		{Value: runtimeValueData{Kind: Map}, Map: []RuntimeEntry{{
			Key: helmKubeVersionOption, Value: RuntimeValue{Value: runtimeValueData{Kind: String, Text: testKubeVersion}},
		}, {
			Key: helmAPIVersionsOption, Value: RuntimeValue{Value: runtimeValueData{Kind: List}},
		}}},
	}
	module := &ModuleContext{Context: t.Context(), ProjectRoot: root, filesystem: newProjectFilesystem(root, files)}
	loaded, err := helmrender.Load(helmrender.LoadRequest{
		Source: helmFSTestChartSource, ProjectRoot: root, SourcePath: filepath.Join(root, "main.mcl"), SourceFS: module.filesystem.files,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepare := func(allow bool) helmrender.Prepared {
		prepared, err := helmrender.Prepare(loaded, helmrender.RenderOptions{
			Values: map[string]any{}, KubeVersion: testKubeVersion, APIVersions: []string{}, AllowNondeterministicOutput: allow,
		}, root)
		if err != nil {
			t.Fatal(err)
		}

		return prepared
	}
	allowPrepared := prepare(true)
	defaultPrepared := prepare(false)
	if allowPrepared.CacheKey == defaultPrepared.CacheKey {
		t.Fatal("render cache key does not include nondeterministic-output mode")
	}
	state := &EvaluationState{SourcePath: filepath.Join(root, "main.mcl"), Module: module}
	if _, err := helmRender(arguments, state); err == nil || !strings.Contains(err.Error(), "MCL-HELM-NONDETERMINISTIC") {
		t.Fatalf("helm.render error = %v, want nondeterministic output error", err)
	}
	module.AllowNondeterministicOutput = true
	bundle, err := helmRender(arguments, state)
	if err != nil {
		t.Fatalf("helm.render with nondeterministic output allowed: %v", err)
	}
	if bundle.Kind() != AdapterBundle || len(bundle.List) != 1 {
		t.Fatalf("helm.render returned %#v, want one generated Secret", bundle)
	}
	secret := bundle.List[0]
	if secret.Kind() != Map {
		t.Fatalf("helm.render did not return a Secret map: %#v", secret)
	}
	foundGeneratedValue := false
	for _, field := range secret.Entries() {
		if field.Key == "data" {
			for _, dataField := range field.Value.Entries() {
				if dataField.Key == paramValue && dataField.Value.Kind() == String && dataField.Value.TextValue() != "" {
					foundGeneratedValue = true
				}
			}
		}
	}
	if !foundGeneratedValue {
		t.Fatalf("helm.render did not return generated Secret data: %#v", secret)
	}
	if _, cached := helmRenderedCache.get(allowPrepared.CacheKey); cached {
		t.Fatal("nondeterministic output was stored in the Helm render cache")
	}
}

func BenchmarkHelmRenderValidatedCacheHit(b *testing.B) {
	const cacheKey = "benchmark-validated-helm-render-cache-hit"
	value := helmRenderCacheValue{rendered: map[string]string{"chart/templates/configmap.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cached\n"}}
	helmRenderedCache.set(cacheKey, value, helmRenderCacheWeight(value))
	prepared := helmPreparedRender{cacheKey: cacheKey, cacheKeyAvailable: true}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := renderHelmTemplates(b.Context(), prepared, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func TestHelmRenderPreservesClippedBlockScalarTrailingNewline(t *testing.T) {
	t.Parallel()

	root := filepath.Clean(t.TempDir())
	files := fstest.MapFS{
		"charts/app/Chart.yaml": &fstest.MapFile{Data: []byte("apiVersion: v2\nname: app\nversion: 1.0.0\n")},
		"charts/app/templates/configmap.yaml": &fstest.MapFile{Data: []byte(
			"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\ndata:\n  global: |\n    {\n      \"keepLatestMaintenanceJobs\": 3\n    }",
		)},
	}
	module := &ModuleContext{
		Context:     t.Context(),
		ProjectRoot: root,
		filesystem:  newProjectFilesystem(root, files),
	}
	bundle, err := helmRender([]RuntimeValue{
		{Value: runtimeValueData{Kind: String, Text: helmFSTestChartSource}},
		{Value: runtimeValueData{Kind: Map}, Map: []RuntimeEntry{{
			Key: helmKubeVersionOption, Value: RuntimeValue{Value: runtimeValueData{Kind: String, Text: testKubeVersion}},
		}, {
			Key: helmAPIVersionsOption, Value: RuntimeValue{Value: runtimeValueData{Kind: List}},
		}}},
	}, &EvaluationState{SourcePath: filepath.Join(root, "main.mcl"), Module: module})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Kind() != AdapterBundle || len(bundle.List) != 1 {
		t.Fatalf("helm.render returned %#v; want one rendered resource", bundle)
	}
	var got string
	for _, entry := range bundle.List[0].Map {
		if entry.Key != "data" {
			continue
		}
		for _, dataEntry := range entry.Value.Map {
			if dataEntry.Key == "global" {
				got = dataEntry.Value.TextValue()
			}
		}
	}
	const want = "{\n  \"keepLatestMaintenanceJobs\": 3\n}\n"
	if got != want {
		t.Fatalf("helm.render data.global = %q, want %q", got, want)
	}
}

func TestHelmValueDecimalRoundTripsWithoutPrecisionLoss(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		input     string
		want      float64
		wantError bool
	}{
		{input: "0.0483", want: 0.0483},
		{input: "1.0000000000000001", wantError: true},
	} {
		t.Run(test.input, func(t *testing.T) {
			t.Parallel()

			value := RuntimeValue{Value: runtimeValueData{Kind: Decimal, Text: test.input}}
			got, err := helmValue(value)
			if test.wantError {
				if err == nil {
					t.Fatalf("helmValue(%q) = %v, want precision-loss error", test.input, got)
				}

				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if number, ok := got.(float64); !ok || number != test.want {
				t.Fatalf("helmValue(%q) = %#v, want %v", test.input, got, test.want)
			}
		})
	}
}

func TestHelmRenderUsesDeterministicNow(t *testing.T) {
	t.Parallel()

	const (
		entryPath      = "main.mcl"
		chartTemplate  = "{{- if now }}\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: deterministic-now\ndata:\n  timestamp: {{ now.Format \"2006-01-02T15:04:05Z07:00\" | quote }}\n{{- end }}\n"
		fixedTimeOne   = "2025-01-02T03:04:05+09:00"
		fixedTimeTwo   = "2025-01-01T18:04:06Z"
		formattedTime1 = "2025-01-01T18:04:05Z"
		formattedTime2 = "2025-01-01T18:04:06Z"
	)
	root := filepath.Join(t.TempDir(), "project")
	files := fstest.MapFS{
		"chart/Chart.yaml":               &fstest.MapFile{Data: []byte("apiVersion: v2\nname: sample\nversion: 1.0.0\n")},
		"chart/templates/configmap.yaml": &fstest.MapFile{Data: []byte(chartTemplate)},
	}
	baseSource := `import "std:helm" as helm
let apiVersions: List<String> = []
let options =
  releaseName: "sample"
  namespace: "default"
  kubeVersion: "1.36.0"
  apiVersions: apiVersions
`
	render := func(nowOption string) (Bundle, error) {
		source := baseSource + nowOption + `let resources = helm.render("chart", options)
emit each resources
`

		return EvaluateProjectSourceContext(t.Context(), root, entryPath, source, files, nil)
	}
	for _, test := range []struct {
		name      string
		nowOption string
		wantTime  string
	}{
		{name: "offset-normalized-to-utc", nowOption: "  now: \"" + fixedTimeOne + "\"\n", wantTime: formattedTime1},
		{name: "cache-distinguishes-time", nowOption: "  now: \"" + fixedTimeTwo + "\"\n", wantTime: formattedTime2},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			bundle, err := render(test.nowOption)
			if err != nil {
				t.Fatal(err)
			}
			output, sourceMap, err := RenderWithSourceMap(bundle)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(output), `timestamp: "`+test.wantTime+`"`) {
				t.Fatalf("rendered timestamp is missing %q:\n%s", test.wantTime, output)
			}
			mappedTimestamp := false
			for _, document := range sourceMap.Documents {
				for _, entry := range document.Entries {
					if entry.Path != "/data/timestamp" {
						continue
					}
					for _, origin := range entry.Origins {
						if (origin.Kind == SourceOriginHelmExpression || origin.Kind == SourceOriginHelmBranch) && strings.HasSuffix(origin.Path, "chart/templates/configmap.yaml") {
							mappedTimestamp = true
						}
					}
				}
			}
			if !mappedTimestamp {
				t.Fatalf("Helm source map did not attribute /data/timestamp to its template: %#v", sourceMap)
			}
		})
	}
	if _, err := render(""); err == nil || !strings.Contains(err.Error(), "helm now is disabled in hermetic rendering") {
		t.Fatalf("helm.render without now error = %v, want hermetic now error", err)
	}
}

func TestHelmRemoteChartDiagnosticsReadInMemorySources(t *testing.T) {
	t.Parallel()

	chartPath := filepath.Join(t.TempDir(), ".mcl", "cache", "helm-sources", strings.Repeat("a", 64))
	sourcePath := filepath.Join(chartPath, "templates", "configmap.yaml")
	source := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Values.name }}\n")
	sources := map[string][]byte{"templates/configmap.yaml": source}
	path, got, ok := helmTemplateSource(chartPath, "example", "example/templates/configmap.yaml", sources)
	if !ok || path != sourcePath || string(got) != string(source) {
		t.Fatalf("helmTemplateSource() = (%q, %q, %t)", path, got, ok)
	}

	diagnostic, ok := helmTemplateSourceDiagnostic(errors.New("parse error at (example/templates/configmap.yaml:2): unexpected token"), chartPath, "example", sources)
	if !ok {
		t.Fatal("helmTemplateSourceDiagnostic() did not locate the in-memory template")
	}
	if diagnostic.Path != sourcePath || diagnostic.Span.Start != len("apiVersion: v1\n") || diagnostic.Span.End != len("apiVersion: v1\nkind: ConfigMap") {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
	if _, ok := helmLocalChartSourcePath("example/../outside.yaml", "example", chartPath, sources); ok {
		t.Fatal("helmLocalChartSourcePath() accepted a path outside the chart")
	}
}

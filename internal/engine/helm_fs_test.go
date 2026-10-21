package engine

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
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
		{Value: runtimeValueData{Kind: String, Text: "charts/app"}},
		{Value: runtimeValueData{Kind: Map}, Map: []RuntimeEntry{{
			Key: "kubeVersion", Value: RuntimeValue{Value: runtimeValueData{Kind: String, Text: "1.36.0"}},
		}, {
			Key: "apiVersions", Value: RuntimeValue{Value: runtimeValueData{Kind: List}},
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

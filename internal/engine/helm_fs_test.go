package engine

import (
	"path/filepath"
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

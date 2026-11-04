package engine

import (
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestProjectCRDCatalogEnforcesResourceScopeAndIdentity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		scope     string
		resources string
		wantError string
	}{
		{
			name:  "cluster scoped resources reject namespace",
			scope: "Cluster",
			resources: `let resource =
  apiVersion: "example.test/v1"
  kind: "Widget"
  metadata:
    name: "sample"
    namespace: "default"
emit resource
`,
			wantError: "must not set metadata.namespace",
		},
		{
			name:  "namespaced identity normalizes default namespace",
			scope: "Namespaced",
			resources: `let implicitNamespace =
  apiVersion: "example.test/v1"
  kind: "Widget"
  metadata:
    name: "sample"
let explicitNamespace =
  apiVersion: "example.test/v1"
  kind: "Widget"
  metadata:
    name: "sample"
    namespace: "default"
emit implicitNamespace
emit explicitNamespace
`,
			wantError: "duplicate Kubernetes resource",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(string(filepath.Separator), "virtual-project")
			files := fstest.MapFS{
				projectConfigFileName: &fstest.MapFile{Data: []byte("[[crd_schemas]]\npath = \"crds/widget.yaml\"\n")},
				"crds/widget.yaml": &fstest.MapFile{Data: []byte(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.test
spec:
  group: example.test
  scope: ` + test.scope + `
  names:
    plural: widgets
    singular: widget
    kind: Widget
    listKind: WidgetList
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
`)},
			}
			bundle, err := EvaluateProjectSourceContext(t.Context(), root, projectFileEntrypoint, test.resources, files, nil)
			if err != nil {
				t.Fatalf("EvaluateProjectSourceContext() error = %v", err)
			}
			_, err = Render(bundle)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Render() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

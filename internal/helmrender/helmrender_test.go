package helmrender

import (
	"strings"
	"testing"

	"helm.sh/helm/v4/pkg/chart/common"
	v2 "helm.sh/helm/v4/pkg/chart/v2"
)

func TestRenderedFieldMappingsWithBlockTemplate(t *testing.T) {
	t.Parallel()
	templateSource := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: example\ndata:\n  value: {{ block \"config-value\" . }}{{ .Values.value }}{{ end }}\n")
	chartSource := Chart{source: &v2.Chart{
		Metadata:  &v2.Metadata{Name: "example", Version: "0.1.0", APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{Name: "templates/configmap.yaml", Data: templateSource}},
	}}
	values := map[string]any{"Values": map[string]any{"value": "rendered"}}

	rendered, err := Render(t.Context(), chartSource, values)
	if err != nil {
		t.Fatalf("render valid block template: %v", err)
	}
	if !strings.Contains(rendered["example/templates/configmap.yaml"], "value: rendered") {
		t.Fatalf("rendered output did not contain the block value: %q", rendered)
	}

	if _, err := RenderedFieldMappings(t.Context(), chartSource, values, rendered, func(string) ([]Document, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("instrumentation failed for valid block template: %v", err)
	}
}

func TestPrepareRequiresExplicitKubernetesCapabilities(t *testing.T) {
	t.Parallel()
	loaded := Chart{source: &v2.Chart{Metadata: &v2.Metadata{Name: "example", Version: "0.1.0", APIVersion: v2.APIVersionV2}}}
	if _, err := Prepare(loaded, RenderOptions{}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "requires kubeVersion") {
		t.Fatalf("Prepare() error = %v, want missing kubeVersion error", err)
	}
	options := RenderOptions{KubeVersion: "1.36.0"}
	if _, err := Prepare(loaded, options, t.TempDir()); err == nil || !strings.Contains(err.Error(), "requires apiVersions") {
		t.Fatalf("Prepare() error = %v, want missing apiVersions error", err)
	}
	options.APIVersions = []string{}
	if _, err := Prepare(loaded, options, t.TempDir()); err != nil {
		t.Fatalf("Prepare() with explicit empty apiVersions: %v", err)
	}
}

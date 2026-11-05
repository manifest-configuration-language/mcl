package engine

import (
	"strings"
	"testing"
)

func TestCRDWebhookServicePortDefaultingMatchesKubernetes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		port      string
		wantPort  int32
		wantError bool
	}{
		{name: "omitted port defaults to 443", wantPort: 443},
		{name: "explicit zero remains invalid", port: "          port: 0\n", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.test
spec:
  group: example.test
  scope: Namespaced
  names:
    plural: widgets
    singular: widget
    kind: Widget
    listKind: WidgetList
  conversion:
    strategy: Webhook
    webhook:
      conversionReviewVersions: [v1]
      clientConfig:
        service:
          namespace: system
          name: webhook
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
`
			source = strings.Replace(source, "          name: webhook\n", "          name: webhook\n"+test.port, 1)
			documents, err := decodeRuntimeYAMLDocuments([]byte(source), "crd.yaml")
			if err != nil {
				t.Fatal(err)
			}
			internal, err := runtimeCustomResourceDefinitionAsInternal(documents.List[0])
			if err != nil {
				t.Fatalf("runtimeCustomResourceDefinitionAsInternal() error = %v", err)
			}
			port := internal.Spec.Conversion.WebhookClientConfig.Service.Port
			if port != test.wantPort {
				t.Fatalf("webhook service port = %d, want %d", port, test.wantPort)
			}

			err = validateRuntimeCustomResourceDefinition(t.Context(), documents.List[0])
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "port") {
					t.Fatalf("validateRuntimeCustomResourceDefinition() error = %v, want invalid port error", err)
				}

				return
			}
			if err != nil {
				t.Fatalf("validateRuntimeCustomResourceDefinition() error = %v", err)
			}
		})
	}
}

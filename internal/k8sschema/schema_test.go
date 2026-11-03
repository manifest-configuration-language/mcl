package k8sschema

import "testing"

const (
	apiVersionField = "apiVersion"
	metadataField   = "metadata"
)

func TestBuiltinOpenAPILimitsResourcesToSupportedKubernetesVersion(t *testing.T) {
	t.Parallel()

	if _, exists, err := BuiltinOpenAPI("admissionregistration.k8s.io/v1", "MutatingAdmissionPolicy"); err != nil {
		t.Fatalf("load current schema: %v", err)
	} else if !exists {
		t.Fatal("schema for admissionregistration.k8s.io/v1 MutatingAdmissionPolicy is not available")
	}
	if _, exists, err := BuiltinOpenAPI("extensions/v1beta1", "Ingress"); err != nil {
		t.Fatalf("load removed schema: %v", err)
	} else if exists {
		t.Fatal("removed extensions/v1beta1 Ingress schema is available")
	}
}

func TestBuiltinOpenAPIPrefersCurrentDefinition(t *testing.T) {
	t.Parallel()

	resource, exists, err := BuiltinOpenAPI("v1", "Pod")
	if err != nil {
		t.Fatalf("load Pod schema: %v", err)
	}
	if !exists {
		t.Fatal("Pod schema is not available")
	}
	if field := resource.Lookup("spec", "os"); field == nil {
		t.Fatal("current Pod schema field spec.os is missing")
	}
}

func TestValidateBuiltinResolvesCurrentDefinitions(t *testing.T) {
	t.Parallel()

	value := map[string]any{
		apiVersionField: "v1",
		"kind":          "Pod",
		metadataField:   map[string]any{"name": "sample"},
		"spec": map[string]any{
			"containers": []any{map[string]any{"name": "app", "image": "alpine"}},
		},
	}
	if errors := ValidateBuiltin("v1", "Pod", value); len(errors) > 0 {
		t.Fatalf("validate Pod against current definitions: %v", errors)
	}
}

func TestValidateBuiltinTreatsNullObjectFieldsAsOmitted(t *testing.T) {
	t.Parallel()

	configMap := map[string]any{
		apiVersionField: "v1",
		"kind":          "ConfigMap",
		metadataField:   map[string]any{"name": "sample", "annotations": nil},
	}
	if errors := ValidateBuiltin("v1", "ConfigMap", configMap); len(errors) > 0 {
		t.Fatalf("validate ConfigMap with a null optional field: %v", errors)
	}
	configMap["data"] = map[string]any{"key": nil}
	if errors := ValidateBuiltin("v1", "ConfigMap", configMap); len(errors) == 0 {
		t.Fatal("ConfigMap with a null additionalProperties value passed validation")
	}

	pod := map[string]any{
		apiVersionField: "v1",
		"kind":          "Pod",
		metadataField:   map[string]any{"name": "sample"},
		"spec":          map[string]any{"containers": nil},
	}
	if errors := ValidateBuiltin("v1", "Pod", pod); len(errors) == 0 {
		t.Fatal("Pod with a null required field passed validation")
	}
}

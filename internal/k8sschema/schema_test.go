package k8sschema

import "testing"

const (
	apiVersionField = "apiVersion"
	metadataField   = "metadata"
)

func TestBuiltinOpenAPICombinesCurrentAndLegacyGVKs(t *testing.T) {
	t.Parallel()

	for _, resource := range []struct {
		apiVersion string
		kind       string
	}{
		{apiVersion: "admissionregistration.k8s.io/v1", kind: "MutatingAdmissionPolicy"},
		{apiVersion: "extensions/v1beta1", kind: "Ingress"},
	} {
		if _, exists, err := BuiltinOpenAPI(resource.apiVersion, resource.kind); err != nil {
			t.Fatalf("load schema for %s %s: %v", resource.apiVersion, resource.kind, err)
		} else if !exists {
			t.Errorf("schema for %s %s is not available", resource.apiVersion, resource.kind)
		}
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

func TestValidateBuiltinRetainsLegacyGVK(t *testing.T) {
	t.Parallel()

	value := map[string]any{
		apiVersionField: "extensions/v1beta1",
		"kind":          "Ingress",
		metadataField:   map[string]any{"name": 42},
	}
	if errors := ValidateBuiltin("extensions/v1beta1", "Ingress", value); len(errors) == 0 {
		t.Fatal("legacy Ingress schema did not validate the metadata name type")
	}
}

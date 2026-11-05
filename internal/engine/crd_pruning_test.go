package engine

import (
	"strings"
	"testing"
)

func TestRuntimeCustomResourceUnknownFieldDiagnostics(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		schema string
		object string
		want   []string
	}{
		{
			name:   "root unknown field",
			schema: "properties:\n  known:\n    type: string\n",
			object: "known: ok\nunknown: value\n",
			want:   []string{"resource.unknown"},
		},
		{
			name:   "nested list unknown field",
			schema: "properties:\n  items:\n    type: array\n    items:\n      type: object\n      properties:\n        name:\n          type: string\n",
			object: "items:\n  - name: first\n    unknown: value\n",
			want:   []string{"resource.items[0].unknown"},
		},
		{
			name:   "metadata unknown field",
			schema: "properties:\n  known:\n    type: string\n",
			object: "metadata:\n  name: sample\n  unrecognized: value\nknown: ok\n",
			want:   []string{"resource.metadata.unrecognized"},
		},
		{
			name:   "preserved unknown fields",
			schema: "x-kubernetes-preserve-unknown-fields: true\n",
			object: "freeform:\n  nested: value\n",
		},
		{
			name:   "additional properties",
			schema: "properties:\n  freeform:\n    type: object\n    additionalProperties:\n      type: string\n",
			object: "freeform:\n  key: value\n",
		},
		{
			name:   "defaulted required field",
			schema: "required: [mode]\nproperties:\n  mode:\n    type: string\n    default: safe\n",
			object: "# defaults apply\n",
		},
		{
			name:   "structural and CEL validation",
			schema: "properties:\n  count:\n    type: integer\n    x-kubernetes-validations:\n      - rule: self > 0\n        message: count must be positive\n  name:\n    type: string\n",
			object: "count: 0\nname: 12\n",
			want:   []string{"count must be positive", "must be of type string"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateCRDTestResource(t, test.schema, test.object)
			if len(test.want) == 0 {
				if err != nil {
					t.Fatalf("validateCRDTestResource() error = %v, want success", err)
				}

				return
			}
			if err == nil {
				t.Fatal("validateCRDTestResource() succeeded, want validation errors")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("validateCRDTestResource() error = %v, want substring %q", err, want)
				}
			}
		})
	}
}

func TestRuntimeCustomResourceMultipleUnknownFieldDiagnosticsAreDeterministic(t *testing.T) {
	t.Parallel()
	schema := "properties:\n  known:\n    type: string\n"
	object := "metadata:\n  name: sample\n  unknownMetadata: value\n  labels:\n    'Bad Key': first\n    'Also Bad': second\n  annotations:\n    'Bad Key': first\n    'Also Bad': second\nknown: ok\nunknownRoot: value\n"
	first := validateCRDTestResource(t, schema, object)
	if first == nil {
		t.Fatal("validateCRDTestResource() succeeded, want validation errors")
	}
	for range 10 {
		if got := validateCRDTestResource(t, schema, object); got == nil || got.Error() != first.Error() {
			t.Fatalf("validation error = %v, want deterministic error %q", got, first)
		}
	}
}

func TestCRDCatalogCompilesSchemaOnlyForEmittedCustomResources(t *testing.T) {
	t.Parallel()
	crd := strings.Replace(projectCRD("example.test", "Sample", "samples"), "type: object", "type: 42", 1)
	documents, err := decodeRuntimeYAMLDocuments([]byte(crd), "catalog-crd.yaml")
	if err != nil {
		t.Fatal(err)
	}
	key := customResourceDefinitionKey{group: "example.test", version: "v1", kind: "Sample"}
	catalog := make(map[customResourceDefinitionKey]crdVersionSchema)
	if err := registerCatalogCustomResourceDefinition(catalog, documents.List[0]); err != nil {
		t.Fatalf("registerCatalogCustomResourceDefinition() error = %v", err)
	}
	if schema := catalog[key]; schema.validator != nil || schema.structural != nil || schema.cel != nil || schema.openAPISchema.Kind() != Map {
		t.Fatalf("catalog schema = %#v, want raw OpenAPI schema without compiled validators", schema)
	}
	unrelated, err := decodeRuntimeYAMLDocuments([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: sample\n"), "unrelated.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateRuntimeBundleCustomResourcesWithCatalog(t.Context(), []RuntimeArtifact{{Value: unrelated.List[0]}}, catalog); err != nil {
		t.Fatalf("validateRuntimeBundleCustomResourcesWithCatalog() with unrelated resource error = %v", err)
	}
	resource, err := decodeRuntimeYAMLDocuments([]byte("apiVersion: example.test/v1\nkind: Sample\nmetadata:\n  name: sample\n"), "resource.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = validateRuntimeBundleCustomResourcesWithCatalog(t.Context(), []RuntimeArtifact{{Value: resource.List[0]}}, catalog)
	if err == nil || !strings.Contains(err.Error(), "compile schema for Sample example.test/v1") {
		t.Fatalf("validateRuntimeBundleCustomResourcesWithCatalog() error = %v, want lazy schema compilation failure", err)
	}
	diagnostic, ok := SourceDiagnosticFromError(err)
	if !ok || diagnostic.Path != "catalog-crd.yaml" {
		t.Fatalf("compile error diagnostic = %#v, want the Catalog Schema source path", diagnostic)
	}
}

func TestCRDCatalogRejectsUnservedCustomResourceVersion(t *testing.T) {
	t.Parallel()
	crd := strings.Replace(projectCRD("example.test", "Sample", "samples"), "served: true", "served: false", 1)
	documents, err := decodeRuntimeYAMLDocuments([]byte(crd), "catalog-crd.yaml")
	if err != nil {
		t.Fatal(err)
	}
	catalog := make(map[customResourceDefinitionKey]crdVersionSchema)
	if err := registerCatalogCustomResourceDefinition(catalog, documents.List[0]); err != nil {
		t.Fatalf("registerCatalogCustomResourceDefinition() error = %v", err)
	}
	resource, err := decodeRuntimeYAMLDocuments([]byte("apiVersion: example.test/v1\nkind: Sample\nmetadata:\n  name: sample\n"), "resource.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = validateRuntimeBundleCustomResourcesWithCatalog(t.Context(), []RuntimeArtifact{{Value: resource.List[0]}}, catalog)
	if err == nil || !strings.Contains(err.Error(), `uses unserved version "example.test/v1"`) {
		t.Fatalf("validateRuntimeBundleCustomResourcesWithCatalog() error = %v, want unserved-version error", err)
	}
}

func TestRuntimeCRDValidatesSchemaWithoutMatchingCustomResource(t *testing.T) {
	t.Parallel()
	crd := strings.Replace(projectCRD("example.test", "Sample", "samples"), "type: object", "type: 42", 1)
	documents, err := decodeRuntimeYAMLDocuments([]byte(crd), "output-crd.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = validateRuntimeBundleCustomResourcesWithCatalog(t.Context(), []RuntimeArtifact{{Value: documents.List[0]}}, nil)
	if err == nil || !strings.Contains(err.Error(), "CustomResourceDefinition") {
		t.Fatalf("validateRuntimeBundleCustomResourcesWithCatalog() error = %v, want full output CRD validation error", err)
	}
}

func validateCRDTestResource(t *testing.T, schema, object string) error {
	t.Helper()
	crd := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: samples.example.test\nspec:\n  group: example.test\n  scope: Namespaced\n  names:\n    plural: samples\n    singular: sample\n    kind: Sample\n    listKind: SampleList\n  versions:\n    - name: v1\n      served: true\n      storage: true\n      schema:\n        openAPIV3Schema:\n          type: object\n" + indentCRDTestYAML(schema, 10) + "status:\n  storedVersions: [v1]\n"
	crdDocuments, err := decodeRuntimeYAMLDocuments([]byte(crd), "crd.yaml")
	if err != nil {
		t.Fatal(err)
	}
	schemas := make(map[customResourceDefinitionKey]crdVersionSchema)
	if err := registerRuntimeCustomResourceDefinition(t.Context(), schemas, crdDocuments.List[0]); err != nil {
		t.Fatalf("registerRuntimeCustomResourceDefinition() error = %v", err)
	}
	resource := "apiVersion: example.test/v1\nkind: Sample\n" + indentCRDTestYAML(object, 0)
	resourceDocuments, err := decodeRuntimeYAMLDocuments([]byte(resource), "resource.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = validateRuntimeBundleCustomResourcesWithCatalog(t.Context(), []RuntimeArtifact{{Value: resourceDocuments.List[0]}}, schemas)

	return err
}

func indentCRDTestYAML(value string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	for index := range lines {
		lines[index] = prefix + lines[index]
	}

	return strings.Join(lines, "\n") + "\n"
}

package k8sschema

import (
	"testing"

	"k8s.io/kube-openapi/pkg/validation/spec"
)

func TestValidateBuiltinUnknownFieldsInLists(t *testing.T) {
	t.Parallel()

	itemSchema := spec.Schema{
		SchemaProps: spec.SchemaProps{
			Type:       spec.StringOrArray{schemaTypeObject},
			Properties: map[string]spec.Schema{"known": {SchemaProps: spec.SchemaProps{Type: spec.StringOrArray{"string"}}}},
		},
	}
	listSchema := spec.Schema{
		SchemaProps: spec.SchemaProps{
			Type:  spec.StringOrArray{"array"},
			Items: &spec.SchemaOrArray{Schema: &itemSchema},
		},
	}

	if errors := validateBuiltinUnknownFields(&listSchema, nil, []any{map[string]any{"known": "value", "unknown": true}}); len(errors) != 1 {
		t.Fatalf("expected one unknown field error in list item, got %v", errors)
	}

	refList := spec.Schema{SchemaProps: spec.SchemaProps{Ref: spec.MustCreateRef("#/definitions/List")}}
	composedList := spec.Schema{SchemaProps: spec.SchemaProps{OneOf: []spec.Schema{refList}}}
	definitions := spec.Definitions{
		"List": {SchemaProps: spec.SchemaProps{AnyOf: []spec.Schema{listSchema}}},
	}
	if errors := validateBuiltinUnknownFields(&composedList, definitions, []any{map[string]any{"unknown": true}}); len(errors) != 1 {
		t.Fatalf("expected unknown field error through oneOf, anyOf, and $ref, got %v", errors)
	}

	permissiveItems := []struct {
		name       string
		itemSchema spec.Schema
	}{
		{
			name: "additionalProperties",
			itemSchema: spec.Schema{SchemaProps: spec.SchemaProps{
				Type:                 spec.StringOrArray{schemaTypeObject},
				AdditionalProperties: &spec.SchemaOrBool{Allows: true},
			}},
		},
		{
			name: "preserveUnknownFields",
			itemSchema: spec.Schema{VendorExtensible: spec.VendorExtensible{Extensions: spec.Extensions{
				"x-kubernetes-preserve-unknown-fields": true,
			}}, SchemaProps: spec.SchemaProps{Type: spec.StringOrArray{schemaTypeObject}}},
		},
	}
	for _, test := range permissiveItems {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			list := spec.Schema{SchemaProps: spec.SchemaProps{
				Type:  spec.StringOrArray{"array"},
				Items: &spec.SchemaOrArray{Schema: &test.itemSchema},
			}}
			if errors := validateBuiltinUnknownFields(&list, nil, []any{map[string]any{"unknown": true}}); len(errors) != 0 {
				t.Fatalf("unexpected unknown field errors: %v", errors)
			}
		})
	}
}

func TestValidateBuiltinUnknownFieldsUsesValidCompositionBranches(t *testing.T) {
	t.Parallel()

	branch := func(kind, field string) spec.Schema {
		return spec.Schema{SchemaProps: spec.SchemaProps{
			Type:     spec.StringOrArray{schemaTypeObject},
			Required: []string{"kind", field},
			Properties: map[string]spec.Schema{
				"kind": {SchemaProps: spec.SchemaProps{Type: spec.StringOrArray{"string"}, Enum: []any{kind}}},
				field:  {SchemaProps: spec.SchemaProps{Type: spec.StringOrArray{"string"}}},
			},
		}}
	}

	for _, composition := range []struct {
		name   string
		branch func([]spec.Schema) spec.Schema
	}{
		{name: "oneOf", branch: func(branches []spec.Schema) spec.Schema {
			return spec.Schema{SchemaProps: spec.SchemaProps{OneOf: branches}}
		}},
		{name: "anyOf", branch: func(branches []spec.Schema) spec.Schema {
			return spec.Schema{SchemaProps: spec.SchemaProps{AnyOf: branches}}
		}},
	} {
		t.Run(composition.name, func(t *testing.T) {
			t.Parallel()
			schema := composition.branch([]spec.Schema{branch("a", "alpha"), branch("b", "beta")})
			value := map[string]any{"kind": "a", "alpha": "value", "beta": "unexpected"}
			if errors := validateBuiltinUnknownFields(&schema, nil, value); len(errors) != 1 {
				t.Fatalf("expected invalid-branch field to be rejected, got %v", errors)
			}
		})
	}
}

// Package k8sschemaはKubernetes Schemaを言語非依存の型モデルへ変換します。
package k8sschema

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/kustomize/kyaml/openapi"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

const maxDepth = 128

var builtinValidators sync.Map //nolint:gochecknoglobals

type Type struct {
	Kind                 string
	Element              *Type
	Options              []Type
	Fields               []Field
	OpenRecord           bool
	AdditionalProperties *Type
}

// ValidateBuiltinは組み込みKubernetes Schemaでインスタンスを検証します。
func ValidateBuiltin(apiVersion, kind string, value any) []error {
	resource := openapi.SchemaForResourceType(yaml.TypeMeta{APIVersion: apiVersion, Kind: kind})
	if resource == nil || resource.Schema == nil {
		return nil
	}
	key := apiVersion + "/" + kind
	compiled, exists := builtinValidators.Load(key)
	if !exists {
		created, err := compileBuiltinValidator(resource.Schema)
		if err != nil {
			return []error{err}
		}
		stored, _ := builtinValidators.LoadOrStore(key, created)
		compiled = stored
	}
	validator := compiled.(*jsonschema.Schema)
	if err := validator.Validate(value); err != nil {
		return []error{err}
	}

	return nil
}

// HasBuiltinSchemaは組み込みSchemaが指定したGVKに登録されているかを返します。
func HasBuiltinSchema(apiVersion, kind string) bool {
	resource := openapi.SchemaForResourceType(yaml.TypeMeta{APIVersion: apiVersion, Kind: kind})

	return resource != nil && resource.Schema != nil
}

func compileBuiltinValidator(resource *spec.Schema) (*jsonschema.Schema, error) {
	root := openapi.Schema()
	document, err := json.Marshal(struct {
		Schema      string           `json:"$schema"`
		Definitions spec.Definitions `json:"definitions"`
		AllOf       []spec.Schema    `json:"allOf"`
	}{
		Schema:      "http://json-schema.org/draft-04/schema#",
		Definitions: root.Definitions,
		AllOf:       []spec.Schema{*resource},
	})
	if err != nil {
		return nil, fmt.Errorf("encode built-in Kubernetes schema: %w", err)
	}
	var schemaDocument map[string]any
	if err := json.Unmarshal(document, &schemaDocument); err != nil {
		return nil, fmt.Errorf("decode built-in Kubernetes schema: %w", err)
	}
	normalizeKubernetesSchemaExtensions(schemaDocument)
	document, err = json.Marshal(schemaDocument)
	if err != nil {
		return nil, fmt.Errorf("encode normalized Kubernetes schema: %w", err)
	}
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return nil, fmt.Errorf("parse built-in Kubernetes schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	const resourceURL = "mcl://kubernetes/builtin-resource.json"
	if err := compiler.AddResource(resourceURL, parsed); err != nil {
		return nil, fmt.Errorf("register built-in Kubernetes schema: %w", err)
	}
	compiled, err := compiler.Compile(resourceURL)
	if err != nil {
		return nil, fmt.Errorf("compile built-in Kubernetes schema: %w", err)
	}

	return compiled, nil
}

func normalizeKubernetesSchemaExtensions(value any) {
	switch value := value.(type) {
	case map[string]any:
		if value["x-kubernetes-int-or-string"] == true || value["format"] == "int-or-string" {
			value["type"] = []string{"string", "integer"}
			delete(value, "x-kubernetes-int-or-string")
			delete(value, "format")
		}
		for _, child := range value {
			normalizeKubernetesSchemaExtensions(child)
		}
	case []any:
		for _, child := range value {
			normalizeKubernetesSchemaExtensions(child)
		}
	}
}

type Field struct {
	Name     string
	Type     Type
	Optional bool
}

func Builtin(apiVersion, kind string) (Type, bool) {
	resource := openapi.SchemaForResourceType(yaml.TypeMeta{APIVersion: apiVersion, Kind: kind})
	if resource == nil || resource.Schema == nil {
		return Type{}, false
	}

	return openAPISchema(resource, make(map[string]*Type), 0), true
}

func FromStructural(schema *structuralschema.Structural) Type {
	return structuralSchema(schema, 0)
}

func FieldType(schema Type, fieldName string) (Type, bool) {
	if len(schema.Options) > 0 {
		var result Type
		found := false
		for _, option := range schema.Options {
			child, exists := FieldType(option, fieldName)
			if !exists {
				continue
			}
			if !found {
				result = child
				found = true
			} else {
				result = union(result, child)
			}
		}
		if found {
			return result, true
		}
	}
	for _, field := range schema.Fields {
		if field.Name == fieldName {
			return field.Type, true
		}
	}
	if schema.OpenRecord {
		if schema.AdditionalProperties != nil {
			return *schema.AdditionalProperties, true
		}

		return Type{Kind: "Any"}, true
	}

	return Type{}, false
}

func ElementType(schema Type) (Type, bool) {
	if schema.Kind == "List" && schema.Element != nil {
		return *schema.Element, true
	}
	for _, option := range schema.Options {
		if element, exists := ElementType(option); exists {
			return element, true
		}
	}

	return Type{}, false
}

func openAPISchema(resource *openapi.ResourceSchema, refs map[string]*Type, depth int) Type {
	if resource == nil || resource.Schema == nil || depth > maxDepth {
		return Type{Kind: "Any"}
	}
	schema := resource.Schema
	if ref := schema.Ref.String(); ref != "" {
		if existing, exists := refs[ref]; exists {
			return *existing
		}
		resolved := &Type{Kind: "Any"}
		refs[ref] = resolved
		definition, err := openapi.Resolve(&schema.Ref, openapi.Schema())
		if err != nil {
			return *resolved
		}
		*resolved = openAPISchema(&openapi.ResourceSchema{Schema: definition}, refs, depth+1)

		return *resolved
	}
	if len(schema.AllOf) > 0 || len(schema.AnyOf) > 0 || len(schema.OneOf) > 0 || schema.Not != nil || len(schema.PatternProperties) > 0 {
		return Type{Kind: "Any"}
	}
	types := []string(schema.Type)
	if len(types) == 0 && len(schema.Properties) > 0 {
		types = []string{"object"}
	}
	var inferred Type
	set := false
	for _, schemaKind := range types {
		current := openAPISchemaKind(schemaKind, resource, refs, depth)
		if !set {
			inferred, set = current, true
		} else {
			inferred = union(inferred, current)
		}
	}
	if !set {
		inferred = Type{Kind: "Any"}
	}
	if schema.Nullable && inferred.Kind != "Null" {
		inferred = union(inferred, Type{Kind: "Null"})
	}

	return inferred
}

func openAPISchemaKind(kind string, resource *openapi.ResourceSchema, refs map[string]*Type, depth int) Type {
	schema := resource.Schema
	switch kind {
	case "null":
		return Type{Kind: "Null"}
	case "boolean":
		return Type{Kind: "Bool"}
	case "integer":
		return Type{Kind: "Int"}
	case "number":
		return Type{Options: []Type{{Kind: "Int"}, {Kind: "Decimal"}}}
	case "string":
		return Type{Kind: "String"}
	case "array":
		element := Type{Kind: "Any"}
		if schema.Items != nil {
			if schema.Items.Schema != nil {
				element = openAPISchema(&openapi.ResourceSchema{Schema: schema.Items.Schema}, refs, depth+1)
			} else if len(schema.Items.Schemas) > 0 {
				options := make([]Type, len(schema.Items.Schemas))
				for index := range schema.Items.Schemas {
					options[index] = openAPISchema(&openapi.ResourceSchema{Schema: &schema.Items.Schemas[index]}, refs, depth+1)
				}
				element = Type{Options: options}
			}
		}

		return Type{Kind: "List", Element: &element}
	case "object":
		required := make(map[string]bool, len(schema.Required))
		for _, name := range schema.Required {
			required[name] = true
		}
		fields := make([]Field, 0, len(schema.Properties))
		for name := range schema.Properties {
			property := schema.Properties[name]
			fields = append(fields, Field{Name: name, Type: openAPISchema(&openapi.ResourceSchema{Schema: &property}, refs, depth+1), Optional: !required[name]})
		}
		slices.SortFunc(fields, func(left, right Field) int { return strings.Compare(left.Name, right.Name) })
		open := schema.AdditionalProperties == nil || schema.AdditionalProperties.Allows || schema.AdditionalProperties.Schema != nil
		var additional *Type
		if schema.AdditionalProperties != nil {
			if schema.AdditionalProperties.Schema != nil {
				value := openAPISchema(&openapi.ResourceSchema{Schema: schema.AdditionalProperties.Schema}, refs, depth+1)
				additional = &value
			} else if !schema.AdditionalProperties.Allows {
				open = false
			}
		}

		return Type{Fields: fields, OpenRecord: open, AdditionalProperties: additional}
	default:
		return Type{Kind: "Any"}
	}
}

func structuralSchema(schema *structuralschema.Structural, depth int) Type {
	if schema == nil || depth > maxDepth {
		return Type{Kind: "Any"}
	}
	var inferred Type
	switch schema.Type {
	case "null":
		inferred = Type{Kind: "Null"}
	case "boolean":
		inferred = Type{Kind: "Bool"}
	case "integer":
		inferred = Type{Kind: "Int"}
	case "number":
		inferred = Type{Options: []Type{{Kind: "Int"}, {Kind: "Decimal"}}}
	case "string":
		inferred = Type{Kind: "String"}
	case "array":
		element := structuralSchema(schema.Items, depth+1)
		inferred = Type{Kind: "List", Element: &element}
	case "object":
		required := make(map[string]bool)
		if schema.ValueValidation != nil {
			for _, name := range schema.ValueValidation.Required {
				required[name] = true
			}
		}
		fields := make([]Field, 0, len(schema.Properties))
		for name, property := range schema.Properties {
			fields = append(fields, Field{Name: name, Type: structuralSchema(&property, depth+1), Optional: !required[name]})
		}
		slices.SortFunc(fields, func(left, right Field) int { return strings.Compare(left.Name, right.Name) })
		open := schema.XPreserveUnknownFields
		var additional *Type
		if schema.AdditionalProperties != nil {
			open = open || schema.AdditionalProperties.Bool || schema.AdditionalProperties.Structural != nil
			if schema.AdditionalProperties.Structural != nil {
				value := structuralSchema(schema.AdditionalProperties.Structural, depth+1)
				additional = &value
			}
		}
		inferred = Type{Fields: fields, OpenRecord: open, AdditionalProperties: additional}
	default:
		inferred = Type{Kind: "Any"}
	}
	if schema.Nullable && inferred.Kind != "Null" {
		inferred = union(inferred, Type{Kind: "Null"})
	}

	return inferred
}

func union(left, right Type) Type {
	options := make([]Type, 0, 2)
	if len(left.Options) > 0 {
		options = append(options, left.Options...)
	} else {
		options = append(options, left)
	}
	if len(right.Options) > 0 {
		options = append(options, right.Options...)
	} else {
		options = append(options, right)
	}

	return Type{Options: options}
}

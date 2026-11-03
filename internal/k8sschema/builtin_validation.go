package k8sschema

import (
	"fmt"

	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

func validateBuiltinUnknownFields(schema *spec.Schema, definitions spec.Definitions, value any) []error {
	return validateUnknownFields([]*spec.Schema{schema}, definitions, value, field.NewPath("resource"), 0)
}

func validateUnknownFields(schemas []*spec.Schema, definitions spec.Definitions, value any, path *field.Path, depth int) []error {
	if depth > maxDepth {
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	properties := make(map[string][]*spec.Schema)
	additionalSchemas := make([]*spec.Schema, 0)
	allowAdditional := false
	objectSchema := false
	allowUnknown := false
	visited := make(map[*spec.Schema]struct{})
	var visit func(*spec.Schema)
	visit = func(schema *spec.Schema) {
		if schema == nil {
			return
		}
		if _, exists := visited[schema]; exists {
			return
		}
		visited[schema] = struct{}{}
		if schema.Ref.String() != "" {
			visit(resolveOpenAPIReference(schema, definitions))
		}
		if len(schema.Properties) > 0 || schema.Type.Contains("object") {
			objectSchema = true
		}
		if schema.Extensions["x-kubernetes-preserve-unknown-fields"] == true || schema.Extensions["x-kubernetes-embedded-resource"] == true {
			allowUnknown = true
		}
		for name, property := range schema.Properties {
			propertyCopy := property
			properties[name] = append(properties[name], &propertyCopy)
		}
		if schema.AdditionalProperties != nil {
			objectSchema = true
			if schema.AdditionalProperties.Allows || schema.AdditionalProperties.Schema != nil {
				allowAdditional = true
			}
			if schema.AdditionalProperties.Schema != nil {
				additionalSchemas = append(additionalSchemas, schema.AdditionalProperties.Schema)
			}
		}
		for index := range schema.AllOf {
			visit(&schema.AllOf[index])
		}
		for index := range schema.AnyOf {
			visit(&schema.AnyOf[index])
		}
		for index := range schema.OneOf {
			visit(&schema.OneOf[index])
		}
	}
	for _, schema := range schemas {
		visit(schema)
	}
	if !objectSchema {
		return nil
	}

	var validationErrors []error
	for name, childValue := range object {
		childPath := path.Child(name)
		if childSchemas := properties[name]; len(childSchemas) > 0 {
			validationErrors = append(validationErrors, validateUnknownFields(childSchemas, definitions, childValue, childPath, depth+1)...)
			continue
		}
		if len(additionalSchemas) > 0 {
			validationErrors = append(validationErrors, validateUnknownFields(additionalSchemas, definitions, childValue, childPath, depth+1)...)
			continue
		}
		if allowAdditional || allowUnknown {
			continue
		}
		validationErrors = append(validationErrors, fmt.Errorf("%s: unknown field", childPath.String()))
	}

	return validationErrors
}

func builtinStructuralSchema(schema *spec.Schema, definitions spec.Definitions, depth int) *structuralschema.Structural {
	if schema == nil || depth > maxDepth {
		return nil
	}
	original := schema
	if schema.Ref.String() != "" {
		resolved := resolveOpenAPIReference(schema, definitions)
		if resolved == nil {
			return nil
		}
		schema = resolved
	}
	structural := &structuralschema.Structural{}
	if len(schema.Type) > 0 {
		structural.Type = schema.Type[0]
	}
	structural.XPreserveUnknownFields = schemaExtension(schema, original, "x-kubernetes-preserve-unknown-fields") == true
	if listType, ok := schemaExtension(schema, original, "x-kubernetes-list-type").(string); ok {
		structural.XListType = &listType
	}
	structural.XListMapKeys = stringSlice(schemaExtension(schema, original, "x-kubernetes-list-map-keys"))
	if schema.Items != nil && schema.Items.Schema != nil {
		structural.Items = builtinStructuralSchema(schema.Items.Schema, definitions, depth+1)
	}
	if len(schema.Properties) > 0 {
		structural.Properties = make(map[string]structuralschema.Structural, len(schema.Properties))
		for name, property := range schema.Properties {
			child := builtinStructuralSchema(&property, definitions, depth+1)
			if child != nil {
				structural.Properties[name] = *child
			}
		}
	}
	if additional := schema.AdditionalProperties; additional != nil {
		structural.AdditionalProperties = &structuralschema.StructuralOrBool{Bool: additional.Allows}
		if additional.Schema != nil {
			structural.AdditionalProperties.Structural = builtinStructuralSchema(additional.Schema, definitions, depth+1)
		}
	}

	return structural
}

func schemaExtension(schema, original *spec.Schema, name string) any {
	if original != schema {
		if value, exists := original.Extensions[name]; exists {
			return value
		}
	}

	return schema.Extensions[name]
}

func stringSlice(value any) []string {
	switch value := value.(type) {
	case []string:
		return value
	case []any:
		result := make([]string, 0, len(value))
		for _, item := range value {
			if item, ok := item.(string); ok {
				result = append(result, item)
			}
		}
		return result
	default:
		return nil
	}
}

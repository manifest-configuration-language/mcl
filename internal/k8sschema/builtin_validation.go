package k8sschema

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"slices"

	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

func validateBuiltinUnknownFields(schema *spec.Schema, definitions spec.Definitions, value any) []error {
	validator := &builtinValidator{}

	return validateBuiltinUnknownFieldsWithBranchValidator(schema, definitions, value, func(branch *spec.Schema, value any) bool {
		return validator.validateBranch(branch, definitions, value)
	})
}

func validateBuiltinUnknownFieldsWithBranchValidator(schema *spec.Schema, definitions spec.Definitions, value any, validateBranch func(*spec.Schema, any) bool) []error {
	return validateUnknownFields([]*spec.Schema{schema}, definitions, value, field.NewPath("resource"), 0, validateBranch)
}

func (validator *builtinValidator) validateBranch(schema *spec.Schema, definitions spec.Definitions, value any) bool {
	keyBytes, err := json.Marshal(schema)
	if err != nil {
		return false
	}
	key := string(keyBytes)
	compiled, exists := validator.branchValidators.Load(key)
	if !exists {
		created, err := compileBuiltinValidator(schema, definitions)
		if err != nil {
			return false
		}
		stored, _ := validator.branchValidators.LoadOrStore(key, created)
		compiled = stored
	}

	return compiled.(*builtinValidator).schema.Validate(value) == nil
}

func validateUnknownFields(schemas []*spec.Schema, definitions spec.Definitions, value any, path *field.Path, depth int, validateBranch func(*spec.Schema, any) bool) []error {
	if depth > maxDepth {
		return []error{fmt.Errorf("%s: schema validation depth exceeds %d", path.String(), maxDepth)}
	}
	if values, ok := value.([]any); ok {
		itemSchemas := make([]*spec.Schema, 0)
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
			if schema.Items != nil {
				if schema.Items.Schema != nil {
					itemSchemas = append(itemSchemas, schema.Items.Schema)
				}
				for index := range schema.Items.Schemas {
					itemSchemas = append(itemSchemas, &schema.Items.Schemas[index])
				}
			}
			for index := range schema.AllOf {
				visit(&schema.AllOf[index])
			}
			for index := range schema.AnyOf {
				if validateBranch(&schema.AnyOf[index], value) {
					visit(&schema.AnyOf[index])
				}
			}
			matchingOneOf := -1
			oneOfMatches := 0
			for index := range schema.OneOf {
				if validateBranch(&schema.OneOf[index], value) {
					matchingOneOf = index
					oneOfMatches++
				}
			}
			if oneOfMatches == 1 {
				visit(&schema.OneOf[matchingOneOf])
			}
		}
		for _, schema := range schemas {
			visit(schema)
		}
		var validationErrors []error
		for index, item := range values {
			validationErrors = append(validationErrors, validateUnknownFields(itemSchemas, definitions, item, path.Index(index), depth+1, validateBranch)...)
		}

		return validationErrors
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
			if validateBranch(&schema.AnyOf[index], value) {
				visit(&schema.AnyOf[index])
			}
		}
		matchingOneOf := -1
		oneOfMatches := 0
		for index := range schema.OneOf {
			if validateBranch(&schema.OneOf[index], value) {
				matchingOneOf = index
				oneOfMatches++
			}
		}
		if oneOfMatches == 1 {
			visit(&schema.OneOf[matchingOneOf])
		}
	}
	for _, schema := range schemas {
		visit(schema)
	}
	if !objectSchema {
		return nil
	}

	var validationErrors []error
	for _, name := range slices.Sorted(maps.Keys(object)) {
		childValue := object[name]
		childPath := path.Child(name)
		if childSchemas := properties[name]; len(childSchemas) > 0 {
			validationErrors = append(validationErrors, validateUnknownFields(childSchemas, definitions, childValue, childPath, depth+1, validateBranch)...)

			continue
		}
		if len(additionalSchemas) > 0 {
			validationErrors = append(validationErrors, validateUnknownFields(additionalSchemas, definitions, childValue, childPath, depth+1, validateBranch)...)

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
	visited := make(map[*spec.Schema]struct{})
	var visit func(*spec.Schema, int) *structuralschema.Structural
	visit = func(schema *spec.Schema, depth int) *structuralschema.Structural {
		if schema == nil || depth > maxDepth {
			return nil
		}
		if _, exists := visited[schema]; exists {
			return nil
		}
		visited[schema] = struct{}{}

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
			structural.Items = visit(schema.Items.Schema, depth+1)
		}
		if len(schema.Properties) > 0 {
			structural.Properties = make(map[string]structuralschema.Structural, len(schema.Properties))
			for name, property := range schema.Properties {
				child := visit(&property, depth+1)
				if child != nil {
					structural.Properties[name] = *child
				}
			}
		}
		if additional := schema.AdditionalProperties; additional != nil {
			structural.AdditionalProperties = &structuralschema.StructuralOrBool{Bool: additional.Allows}
			if additional.Schema != nil {
				structural.AdditionalProperties.Structural = visit(additional.Schema, depth+1)
			}
		}

		return structural
	}

	return visit(schema, depth)
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

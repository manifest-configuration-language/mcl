package engine

import (
	"github.com/manifest-configuration-language/mcl/internal/k8sschema"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
)

func builtinKubernetesSchemaType(apiVersion, kind string) (ResolvedType, bool) {
	schema, exists := k8sschema.Builtin(apiVersion, kind)
	if !exists {
		return ResolvedType{}, false
	}

	return mclSchemaType(schema), true
}

func structuralSchemaType(schema *structuralschema.Structural, _ int) ResolvedType {
	return mclSchemaType(k8sschema.FromStructural(schema))
}

func mclSchemaType(schema k8sschema.Type) ResolvedType {
	if len(schema.Options) > 0 {
		result := mclSchemaType(schema.Options[0])
		for _, option := range schema.Options[1:] {
			result = joinStaticTypes(result, mclSchemaType(option))
		}

		return result
	}
	result := ResolvedType{Name: schema.Kind}
	if schema.Element != nil {
		element := mclSchemaType(*schema.Element)
		result.Element = &element
	}
	if len(schema.Fields) > 0 || schema.Fields != nil {
		result.Fields = make([]TypeField, len(schema.Fields))
		for index, field := range schema.Fields {
			result.Fields[index] = TypeField{Name: field.Name, Type: mclSchemaType(field.Type), Optional: field.Optional}
		}
	}
	result.OpenRecord = schema.OpenRecord
	if schema.AdditionalProperties != nil {
		additional := mclSchemaType(*schema.AdditionalProperties)
		result.AdditionalProperties = &additional
	}

	return result
}

func schemaFieldType(schemaType ResolvedType, fieldName string) (*ResolvedType, bool) {
	field, exists := k8sschema.FieldType(k8sschemaType(schemaType), fieldName)
	if !exists {
		return nil, false
	}
	result := mclSchemaType(field)

	return &result, true
}

func schemaElementType(schemaType ResolvedType) (*ResolvedType, bool) {
	element, exists := k8sschema.ElementType(k8sschemaType(schemaType))
	if !exists {
		return nil, false
	}
	result := mclSchemaType(element)

	return &result, true
}

func k8sschemaType(schema ResolvedType) k8sschema.Type {
	result := k8sschema.Type{Kind: schema.Name, OpenRecord: schema.OpenRecord}
	if schema.Name == typeMap || schema.Fields != nil || schema.OpenRecord {
		result.Kind = ""
	}
	if schema.Element != nil {
		element := k8sschemaType(*schema.Element)
		result.Element = &element
	}
	if schema.AdditionalProperties != nil {
		additional := k8sschemaType(*schema.AdditionalProperties)
		result.AdditionalProperties = &additional
	}
	if len(schema.Options) > 0 {
		result.Options = make([]k8sschema.Type, len(schema.Options))
		for index, option := range schema.Options {
			result.Options[index] = k8sschemaType(option)
		}
	}
	if schema.Fields != nil {
		result.Fields = make([]k8sschema.Field, len(schema.Fields))
		for index, field := range schema.Fields {
			result.Fields[index] = k8sschema.Field{Name: field.Name, Type: k8sschemaType(field.Type), Optional: field.Optional}
		}
	}

	return result
}

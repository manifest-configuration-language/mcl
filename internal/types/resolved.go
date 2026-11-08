// Package types defines semantic type data shared by compilation and evaluation.
package types

import (
	"slices"

	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

// ResolvedType is a type expression after names and schema references are resolved.
type ResolvedType struct {
	Span                 syntax.SourceSpan
	Name                 string
	StringLiteral        string
	StringLiteralKnown   bool
	TypeParameters       []string
	Arguments            []ResolvedType
	SchemaPath           string
	SchemaID             SchemaID
	ScopeID              TypeScopeID
	Element              *ResolvedType
	Options              []ResolvedType
	Fields               []TypeField
	OpenRecord           bool
	AdditionalProperties *ResolvedType
	Optional             bool
	Parameters           []FunctionTypeParameter
	ReturnType           *ResolvedType
}

type SchemaID string

type TypeScopeID string

type FunctionTypeParameter struct {
	Name     string
	Type     ResolvedType
	Optional bool
}

type TypeField struct {
	Name     string
	NameSpan syntax.SourceSpan
	Type     ResolvedType
	Optional bool
}

// Clone returns a deep copy of a resolved type and all nested type data.
func Clone(typeExpression ResolvedType) ResolvedType {
	typeExpression.TypeParameters = slices.Clone(typeExpression.TypeParameters)
	typeExpression.Arguments = cloneTypes(typeExpression.Arguments)
	typeExpression.Options = cloneTypes(typeExpression.Options)
	typeExpression.Fields = slices.Clone(typeExpression.Fields)
	for index := range typeExpression.Fields {
		typeExpression.Fields[index].Type = Clone(typeExpression.Fields[index].Type)
	}
	typeExpression.Element = cloneTypePointer(typeExpression.Element)
	typeExpression.AdditionalProperties = cloneTypePointer(typeExpression.AdditionalProperties)
	typeExpression.Parameters = slices.Clone(typeExpression.Parameters)
	for index := range typeExpression.Parameters {
		typeExpression.Parameters[index].Type = Clone(typeExpression.Parameters[index].Type)
	}
	typeExpression.ReturnType = cloneTypePointer(typeExpression.ReturnType)

	return typeExpression
}

func cloneTypes(types []ResolvedType) []ResolvedType {
	cloned := slices.Clone(types)
	for index := range cloned {
		cloned[index] = Clone(cloned[index])
	}

	return cloned
}

func cloneTypePointer(typeExpression *ResolvedType) *ResolvedType {
	if typeExpression == nil {
		return nil
	}

	cloned := Clone(*typeExpression)

	return &cloned
}

// Substitute replaces generic type variables while leaving its input types unchanged.
func Substitute(typeExpression ResolvedType, bindings map[string]ResolvedType) ResolvedType {
	if replacement, exists := bindings[typeExpression.Name]; exists && len(typeExpression.Arguments) == 0 {
		return replacement
	}
	typeExpression.Options = slices.Clone(typeExpression.Options)
	for index := range typeExpression.Options {
		typeExpression.Options[index] = Substitute(typeExpression.Options[index], bindings)
	}
	typeExpression.Arguments = slices.Clone(typeExpression.Arguments)
	for index := range typeExpression.Arguments {
		typeExpression.Arguments[index] = Substitute(typeExpression.Arguments[index], bindings)
	}
	typeExpression.Element = substituteTypePointer(typeExpression.Element, bindings)
	typeExpression.Fields = slices.Clone(typeExpression.Fields)
	for index := range typeExpression.Fields {
		typeExpression.Fields[index].Type = Substitute(typeExpression.Fields[index].Type, bindings)
	}
	typeExpression.AdditionalProperties = substituteTypePointer(typeExpression.AdditionalProperties, bindings)
	typeExpression.Parameters = slices.Clone(typeExpression.Parameters)
	for index := range typeExpression.Parameters {
		typeExpression.Parameters[index].Type = Substitute(typeExpression.Parameters[index].Type, bindings)
	}
	typeExpression.ReturnType = substituteTypePointer(typeExpression.ReturnType, bindings)

	return typeExpression
}

func substituteTypePointer(typeExpression *ResolvedType, bindings map[string]ResolvedType) *ResolvedType {
	if typeExpression == nil {
		return nil
	}

	substituted := Substitute(*typeExpression, bindings)

	return &substituted
}

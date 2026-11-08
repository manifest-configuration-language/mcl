// Package types defines semantic type data shared by compilation and evaluation.
package types

import "github.com/manifest-configuration-language/mcl/internal/syntax"

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

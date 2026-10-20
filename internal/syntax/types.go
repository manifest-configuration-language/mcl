package syntax

type TypeExpr struct {
	TypeParameters []string

	Span                SourceSpan
	Name                string
	ConstantString      string
	ConstantStringKnown bool
	SchemaPath          string
	Arguments           []TypeExpr
	Element             *TypeExpr
	Options             []TypeExpr
	Fields              []TypeField
	Parameters          []FunctionTypeParameter
	ReturnType          *TypeExpr
}

type TypeField struct {
	Name     string
	NameSpan SourceSpan
	Type     TypeExpr
	Optional bool
}

type FunctionTypeParameter struct {
	Name string
	Type TypeExpr
}

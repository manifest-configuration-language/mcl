package syntax

type ASTNode interface {
	astNode()
}

type ModuleAST struct {
	Nodes  []ASTNode
	Trivia []SourceTrivia
}

type SourceTrivia struct {
	Span SourceSpan
	Text string
}

type NodeMetadata struct {
	SourcePath string
	Line       int
	Span       SourceSpan
}

type ImportDeclaration struct {
	NodeMetadata
	Name           string
	ModulePath     string
	NameSpan       SourceSpan
	ModulePathSpan SourceSpan
}

type TypeDeclaration struct {
	NodeMetadata
	Name     string
	Exported bool
	TypeSpec *TypeExpr
	NameSpan SourceSpan
}

type RuleDeclaration struct {
	NodeMetadata
	Name     string
	Exported bool
	Rule     *RuleDefinition
	NameSpan SourceSpan
}

type FunctionDeclaration struct {
	NodeMetadata
	Name     string
	Exported bool
	Function *FunctionDefinition
	NameSpan SourceSpan
}

type LetDeclaration struct {
	NodeMetadata
	Name       string
	Exported   bool
	Expression Expr
	Annotation *TypeExpr
	NameSpan   SourceSpan
}

type EmitStatement struct {
	NodeMetadata
	Expression Expr
	Each       bool
}

type ValidateStatement struct {
	NodeMetadata
	Expression         Expr
	RuleExpr           Expr
	RuleName           string
	Each               bool
	Output             bool
	QueryType          *TypeExpr
	QuerySchema        *TypeExpr
	QueryWhere         Expr
	QueryMinMatches    int
	QueryMinMatchesSet bool
}

type FunctionDefinition struct {
	Parameters []RuleParameter
	ReturnType *TypeExpr
	Expression Expr
}

type RuleParameter struct {
	Name string
	Type TypeExpr
	Span SourceSpan
}

type RuleRequirement struct {
	Expression Expr
	Source     string
	Message    string
	SourcePath string
	Line       int
	Span       SourceSpan
}

type RuleDefinition struct {
	Parameters   []RuleParameter
	Requirements []RuleRequirement
}

func (*ImportDeclaration) astNode()   {}
func (*TypeDeclaration) astNode()     {}
func (*RuleDeclaration) astNode()     {}
func (*FunctionDeclaration) astNode() {}
func (*LetDeclaration) astNode()      {}
func (*EmitStatement) astNode()       {}
func (*ValidateStatement) astNode()   {}

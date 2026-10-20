package engine

import "github.com/manifest-configuration-language/mcl/internal/syntax"

type (
	ASTNode             = syntax.ASTNode
	ModuleAST           = syntax.ModuleAST
	SourceTrivia        = syntax.SourceTrivia
	NodeMetadata        = syntax.NodeMetadata
	ImportDeclaration   = syntax.ImportDeclaration
	TypeDeclaration     = syntax.TypeDeclaration
	RuleDeclaration     = syntax.RuleDeclaration
	FunctionDeclaration = syntax.FunctionDeclaration
	LetDeclaration      = syntax.LetDeclaration
	EmitStatement       = syntax.EmitStatement
	ValidateStatement   = syntax.ValidateStatement
	FunctionDefinition  = syntax.FunctionDefinition
	RuleRequirement     = syntax.RuleRequirement
	RuleDefinition      = syntax.RuleDefinition
)

type RuleParameter struct {
	Name string
	Type ResolvedType
	Span SourceSpan
}

func functionBodySpan(definition *FunctionDefinition) SourceSpan {
	if definition == nil {
		return SourceSpan{}
	}

	return expressionTreeSpan(definition.Expression)
}

func expressionTreeSpan(expression Expr) SourceSpan {
	var span SourceSpan
	WalkExpr(expression, func(expression Expr) {
		span = mergeASTSpans(span, expression.ExprSpan())
		if mapping, ok := expression.(*MapExpr); ok {
			for _, entry := range mapping.Entries {
				span = mergeASTSpans(span, entry.KeySpan)
			}
		}
	})

	return span
}

func mergeASTSpans(left, right SourceSpan) SourceSpan {
	if right.End > left.End {
		left.End = right.End
	}
	if left.Start == 0 || right.Start > 0 && right.Start < left.Start {
		left.Start = right.Start
	}

	return left
}

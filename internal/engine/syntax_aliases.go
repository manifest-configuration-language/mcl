package engine

import "github.com/manifest-configuration-language/mcl/internal/syntax"

type SourceSpan = syntax.SourceSpan

type (
	Expr                  = syntax.Expr
	ExprBase              = syntax.ExprBase
	ExprMapEntry          = syntax.ExprMapEntry
	ExprCallDelimiters    = syntax.ExprCallDelimiters
	NullExpr              = syntax.NullExpr
	BoolExpr              = syntax.BoolExpr
	IntegerExpr           = syntax.IntegerExpr
	DecimalExpr           = syntax.DecimalExpr
	StringExpr            = syntax.StringExpr
	EmptyListExpr         = syntax.EmptyListExpr
	EmptyMapExpr          = syntax.EmptyMapExpr
	ListExpr              = syntax.ListExpr
	MapExpr               = syntax.MapExpr
	ReferenceExpr         = syntax.ReferenceExpr
	OptionalReferenceExpr = syntax.OptionalReferenceExpr
	PathExpr              = syntax.PathExpr
	CallExpr              = syntax.CallExpr
	BinaryExpr            = syntax.BinaryExpr
	UnaryExpr             = syntax.UnaryExpr
	IndexExpr             = syntax.IndexExpr
	LambdaExpr            = syntax.LambdaExpr
	DeferredReferenceExpr = syntax.DeferredReferenceExpr
	ConditionalExpr       = syntax.ConditionalExpr
	ForExpr               = syntax.ForExpr
	NodeExpr              = syntax.NodeExpr
	OverlayExpr           = syntax.OverlayExpr
	RemovalExpr           = syntax.RemovalExpr
)

func WalkExpr(expression Expr, visit func(Expr)) {
	syntax.WalkExpr(expression, visit)
}

func isNilExpr(expression Expr) bool {
	if expression == nil {
		return true
	}
	switch expression := expression.(type) {
	case *NullExpr:
		return expression == nil
	case *BoolExpr:
		return expression == nil
	case *IntegerExpr:
		return expression == nil
	case *DecimalExpr:
		return expression == nil
	case *StringExpr:
		return expression == nil
	case *EmptyListExpr:
		return expression == nil
	case *EmptyMapExpr:
		return expression == nil
	case *ListExpr:
		return expression == nil
	case *MapExpr:
		return expression == nil
	case *ReferenceExpr:
		return expression == nil
	case *OptionalReferenceExpr:
		return expression == nil
	case *PathExpr:
		return expression == nil
	case *CallExpr:
		return expression == nil
	case *BinaryExpr:
		return expression == nil
	case *UnaryExpr:
		return expression == nil
	case *IndexExpr:
		return expression == nil
	case *LambdaExpr:
		return expression == nil
	case *DeferredReferenceExpr:
		return expression == nil
	case *ConditionalExpr:
		return expression == nil
	case *ForExpr:
		return expression == nil
	case *NodeExpr:
		return expression == nil
	case *OverlayExpr:
		return expression == nil
	case *RemovalExpr:
		return expression == nil
	default:
		return false
	}
}

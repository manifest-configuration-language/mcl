package syntax

type SourceSpan struct {
	Start int
	End   int
}

type ExprCallDelimiters struct {
	Open  SourceSpan
	Close SourceSpan
}

// ExprはMCLの式を表す閉じたAST interfaceです。
type Expr interface {
	exprNode()
	ExprSpan() SourceSpan
}

// ExprBaseは式の共通source spanを保持します。
type ExprBase struct {
	Span SourceSpan
}

func (base ExprBase) ExprSpan() SourceSpan { return base.Span }

// ExprMapEntryはMap式の1項目です。
type ExprMapEntry struct {
	Key     string
	KeyExpr Expr
	KeySpan SourceSpan
	Value   Expr
}

// NullExprはnull literalを表します。
type NullExpr struct{ ExprBase }

// BoolExprはBool literalを表します。
type BoolExpr struct {
	ExprBase
	Value bool
}

// IntegerExprはInteger literalを表します。
type IntegerExpr struct {
	ExprBase
	Value string
}

// DecimalExprはDecimal literalを表します。
type DecimalExpr struct {
	ExprBase
	Value string
}

// StringExprはString literalを表します。
type StringExpr struct {
	ExprBase
	Value string
}

// EmptyListExprは空のList literalを表します。
type EmptyListExpr struct{ ExprBase }

// EmptyMapExprは空のMap literalを表します。
type EmptyMapExpr struct{ ExprBase }

// ListExprはList expressionを表します。
type ListExpr struct {
	ExprBase
	Items []Expr
}

// MapExprはMap expressionを表します。
type MapExpr struct {
	ExprBase
	Entries []ExprMapEntry
}

// ReferenceExprは参照式を表します。
type ReferenceExpr struct {
	ExprBase
	Name string
}

// OptionalReferenceExprはoptional reference expressionを表します。
type OptionalReferenceExpr struct {
	ExprBase
	Path string
}

// PathExprはroot-relative path expressionを表します。
type PathExpr struct {
	ExprBase
	Path string
}

// CallExprはfunction call expressionを表します。
type CallExpr struct {
	ExprBase
	Delimiters    ExprCallDelimiters
	Callee        Expr
	Arguments     []Expr
	ArgumentNames []string
}

// BinaryExprはbinary expressionを表します。
type BinaryExpr struct {
	ExprBase
	Operator    string
	Left, Right Expr
}

// UnaryExprはunary expressionを表します。
type UnaryExpr struct {
	ExprBase
	Operator string
	Operand  Expr
}

// IndexExprはindex access expressionを表します。
type IndexExpr struct {
	ExprBase
	Base, Key Expr
}

// LambdaExprはlambda expressionを表します。
type LambdaExpr struct {
	ExprBase
	Parameters     []string
	ParameterSpans []SourceSpan
	Body           Expr
}

// DeferredReferenceExprはdeferred reference expressionを表します。
type DeferredReferenceExpr struct {
	ExprBase
	Delimiters ExprCallDelimiters
	Target     Expr
	Child      Expr
	Path       []string
}

// ConditionalExprはconditional expressionを表します。
type ConditionalExpr struct {
	ExprBase
	Condition, Then, Else Expr
}

// ForExprはfor expressionを表します。
type ForExpr struct {
	ExprBase
	Variable       string
	VariableSpan   SourceSpan
	Iterable, Body Expr
}

// NodeExprはNode expressionを表します。
type NodeExpr struct {
	ExprBase
	Body Expr
}

// OverlayExprはoverlay expressionを表します。
type OverlayExpr struct {
	ExprBase
	Base, Patch Expr
}

// RemovalExprはfield removal expressionを表します。
type RemovalExpr struct {
	ExprBase
	Base      Expr
	Paths     []string
	PathSpans []SourceSpan
}

func (*NullExpr) exprNode()              {}
func (*BoolExpr) exprNode()              {}
func (*IntegerExpr) exprNode()           {}
func (*DecimalExpr) exprNode()           {}
func (*StringExpr) exprNode()            {}
func (*EmptyListExpr) exprNode()         {}
func (*EmptyMapExpr) exprNode()          {}
func (*ListExpr) exprNode()              {}
func (*MapExpr) exprNode()               {}
func (*ReferenceExpr) exprNode()         {}
func (*OptionalReferenceExpr) exprNode() {}
func (*PathExpr) exprNode()              {}
func (*CallExpr) exprNode()              {}
func (*BinaryExpr) exprNode()            {}
func (*UnaryExpr) exprNode()             {}
func (*IndexExpr) exprNode()             {}
func (*LambdaExpr) exprNode()            {}
func (*DeferredReferenceExpr) exprNode() {}
func (*ConditionalExpr) exprNode()       {}
func (*ForExpr) exprNode()               {}
func (*NodeExpr) exprNode()              {}
func (*OverlayExpr) exprNode()           {}
func (*RemovalExpr) exprNode()           {}

// WalkExprは式と子式をソース順に訪問します。
func WalkExpr(expression Expr, visit func(Expr)) {
	if isNilExpr(expression) || visit == nil {
		return
	}
	visit(expression)
	var children []Expr
	switch node := expression.(type) {
	case *ListExpr:
		children = node.Items
	case *MapExpr:
		for _, entry := range node.Entries {
			if entry.KeyExpr != nil {
				children = append(children, entry.KeyExpr)
			}
			children = append(children, entry.Value)
		}
	case *CallExpr:
		children = append(children, node.Callee)
		children = append(children, node.Arguments...)
	case *BinaryExpr:
		children = []Expr{node.Left, node.Right}
	case *UnaryExpr:
		children = []Expr{node.Operand}
	case *IndexExpr:
		children = []Expr{node.Base, node.Key}
	case *LambdaExpr:
		children = []Expr{node.Body}
	case *DeferredReferenceExpr:
		children = []Expr{node.Target, node.Child}
	case *ConditionalExpr:
		children = []Expr{node.Condition, node.Then, node.Else}
	case *ForExpr:
		children = []Expr{node.Iterable, node.Body}
	case *NodeExpr:
		children = []Expr{node.Body}
	case *OverlayExpr:
		children = []Expr{node.Base, node.Patch}
	case *RemovalExpr:
		children = []Expr{node.Base}
	}
	for _, child := range children {
		WalkExpr(child, visit)
	}
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

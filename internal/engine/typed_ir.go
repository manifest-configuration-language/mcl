package engine

import "slices"

// ExprID is an index into one immutable typed expression unit.
type ExprID uint32

// typedUnit stores a flat immutable arena. Child expressions are referenced only
// by ExprID so continuations can retain a body without retaining its tree.
type typedUnit struct {
	nodes []typedNode
}

func newTypedUnit(expression typedExpression) (*typedUnit, ExprID) {
	unit := &typedUnit{}
	return unit, unit.add(expression)
}

type typedNodeChild struct {
	id      ExprID
	present bool
}

type typedNodeItem struct {
	id     ExprID
	spread bool
}

type typedNodeMapEntry struct {
	key           string
	keySpan       SourceSpan
	keyExpression typedNodeChild
	value         ExprID
}

type typedNode struct {
	kind            typedExpressionKind
	builtinID       builtinID
	span            SourceSpan
	resolvedType    ResolvedType
	analysisFacts   TypeAnalysisFacts
	scalarType      typedScalarType
	bool            bool
	text            string
	genericVariable string
	items           []typedNodeItem
	entries         []typedNodeMapEntry
	operand         typedNodeChild
	operator        typedOperator
	left            typedNodeChild
	right           typedNodeChild
	condition       typedNodeChild
	thenBranch      typedNodeChild
	elseBranch      typedNodeChild
	callee          typedNodeChild
	args            []ExprID
	argNames        []string
	parameters      []string
	paramSpans      []SourceSpan
	lambdaTyping    *LambdaTyping
	paths           []string
	loopName        string
}

func (unit *typedUnit) add(expression typedExpression) ExprID {
	if uint64(len(unit.nodes)) > uint64(^uint32(0)) {
		panic("typed expression arena exceeds the ExprID address range")
	}
	id := ExprID(len(unit.nodes)) // #nosec G115 -- the node count is checked against the uint32 limit before conversion.
	unit.nodes = append(unit.nodes, typedNode{})
	node := typedNode{
		kind: expression.kind, builtinID: expression.builtinID, span: expression.span,
		resolvedType: expression.resolvedType, analysisFacts: expression.analysisFacts,
		scalarType: expression.scalarType, bool: expression.bool, text: expression.text,
		genericVariable: expression.genericVariable, operator: expression.operator,
		argNames: slices.Clone(expression.argNames), parameters: slices.Clone(expression.parameters),
		paramSpans: slices.Clone(expression.paramSpans), lambdaTyping: expression.lambdaTyping,
		paths: slices.Clone(expression.paths), loopName: expression.loopName,
	}
	addChild := func(child *typedExpression) typedNodeChild {
		if child == nil {
			return typedNodeChild{}
		}
		return typedNodeChild{id: unit.add(*child), present: true}
	}
	node.operand = addChild(expression.operand)
	node.left = addChild(expression.left)
	node.right = addChild(expression.right)
	node.condition = addChild(expression.condition)
	node.thenBranch = addChild(expression.thenBranch)
	node.elseBranch = addChild(expression.elseBranch)
	node.callee = addChild(expression.callee)
	if len(expression.items) != 0 {
		node.items = make([]typedNodeItem, len(expression.items))
		for index := range expression.items {
			node.items[index] = typedNodeItem{id: unit.add(expression.items[index]), spread: expression.items[index].spread}
		}
	}
	if len(expression.entries) != 0 {
		node.entries = make([]typedNodeMapEntry, len(expression.entries))
		for index := range expression.entries {
			entry := expression.entries[index]
			node.entries[index] = typedNodeMapEntry{
				key: entry.key, keySpan: entry.keySpan, keyExpression: addChild(entry.keyExpression),
				value: unit.add(entry.value),
			}
		}
	}
	if len(expression.args) != 0 {
		node.args = make([]ExprID, len(expression.args))
		for index := range expression.args {
			node.args[index] = unit.add(expression.args[index])
		}
	}
	unit.nodes[id] = node
	return id
}

func (unit *typedUnit) node(id ExprID) (typedNode, bool) {
	if unit == nil || uint64(id) >= uint64(len(unit.nodes)) {
		return typedNode{}, false
	}

	return unit.nodes[id], true
}

type typedExpressionKind uint8

const (
	typedExpressionNull typedExpressionKind = iota
	typedExpressionBool
	typedExpressionInteger
	typedExpressionDecimal
	typedExpressionString
	typedExpressionReference
	typedExpressionList
	typedExpressionMap
	typedExpressionIndexAccess
	typedExpressionNot
	typedExpressionConditional
	typedExpressionBinary
	typedExpressionLambda
	typedExpressionCall
	typedExpressionOptionalReference
	typedExpressionDeferredReference
	typedExpressionNode
	typedExpressionFor
	typedExpressionOverlay
	typedExpressionRemoval
)

type typedOperator uint8

const (
	typedOperatorEqual typedOperator = iota
	typedOperatorNotEqual
	typedOperatorLess
	typedOperatorLessEqual
	typedOperatorGreater
	typedOperatorGreaterEqual
	typedOperatorAdd
	typedOperatorSubtract
	typedOperatorMultiply
	typedOperatorDivide
	typedOperatorModulo
	typedOperatorAnd
	typedOperatorOr
	typedOperatorCoalesce
	typedOperatorIn
)

type builtinID string

type typedScalarKind uint8

const (
	typedScalarNull typedScalarKind = iota
	typedScalarBool
	typedScalarInt
	typedScalarDecimal
	typedScalarString
)

type typedScalarType struct {
	kind                typedScalarKind
	constantString      string
	constantStringKnown bool
}

type typedExpression struct {
	kind            typedExpressionKind
	builtinID       builtinID
	span            SourceSpan
	resolvedType    ResolvedType
	analysisFacts   TypeAnalysisFacts
	scalarType      typedScalarType
	bool            bool
	text            string
	genericVariable string
	items           []typedExpression
	spread          bool
	entries         []typedMapEntry
	operand         *typedExpression
	operator        typedOperator
	left            *typedExpression
	right           *typedExpression
	condition       *typedExpression
	thenBranch      *typedExpression
	elseBranch      *typedExpression
	callee          *typedExpression
	args            []typedExpression
	argNames        []string
	parameters      []string
	paramSpans      []SourceSpan
	lambdaTyping    *LambdaTyping
	paths           []string
	loopName        string
	// Arena fields make temporary child views and captured continuations point
	// back to immutable arena storage instead of copying recursive descendants.
	arenaUnit *typedUnit
	arenaID   ExprID
}

// expression returns a one-node evaluation view whose children remain arena IDs.
func (unit *typedUnit) expression(id ExprID) (typedExpression, bool) {
	node, ok := unit.node(id)
	if !ok {
		return typedExpression{}, false
	}
	expression := typedExpression{
		kind: node.kind, builtinID: node.builtinID, span: node.span,
		resolvedType: node.resolvedType, analysisFacts: node.analysisFacts,
		scalarType: node.scalarType, bool: node.bool, text: node.text,
		genericVariable: node.genericVariable, operator: node.operator,
		argNames: node.argNames, parameters: node.parameters, paramSpans: node.paramSpans,
		lambdaTyping: node.lambdaTyping, paths: node.paths, loopName: node.loopName,
	}
	child := func(reference typedNodeChild) *typedExpression {
		if !reference.present {
			return nil
		}
		return &typedExpression{arenaUnit: unit, arenaID: reference.id}
	}
	expression.operand = child(node.operand)
	expression.left = child(node.left)
	expression.right = child(node.right)
	expression.condition = child(node.condition)
	expression.thenBranch = child(node.thenBranch)
	expression.elseBranch = child(node.elseBranch)
	expression.callee = child(node.callee)
	if len(node.items) > 0 {
		expression.items = make([]typedExpression, len(node.items))
		for index, item := range node.items {
			expression.items[index] = typedExpression{arenaUnit: unit, arenaID: item.id, spread: item.spread}
		}
	}
	if len(node.entries) > 0 {
		expression.entries = make([]typedMapEntry, len(node.entries))
		for index, entry := range node.entries {
			expression.entries[index] = typedMapEntry{
				key: entry.key, keySpan: entry.keySpan, keyExpression: child(entry.keyExpression),
				value: typedExpression{arenaUnit: unit, arenaID: entry.value},
			}
		}
	}
	if len(node.args) > 0 {
		expression.args = make([]typedExpression, len(node.args))
		for index, arg := range node.args {
			expression.args[index] = typedExpression{arenaUnit: unit, arenaID: arg}
		}
	}

	return expression, true
}

type typedMapEntry struct {
	key           string
	keySpan       SourceSpan
	keyExpression *typedExpression
	value         typedExpression
}

type typedEvaluationContext struct {
	runtimeEnv  map[string]RuntimeValue
	frame       *runtimeFrame
	staticTypes map[string]TypeAnalysis
	types       map[string]ResolvedType
	analysis    *typedAnalysisCache
	stack       map[string]bool
	budget      *functionBudget
	allocations *runtimeAllocationBudget
	sourcePath  string
	module      *ModuleContext
}

func (context typedEvaluationContext) runtimeBinding(name string) (RuntimeValue, bool) {
	if context.frame != nil {
		if value, exists := context.frame.lookup(name); exists {
			return value, true
		}
	}

	value, exists := context.runtimeEnv[name]
	return value, exists
}

func (context typedEvaluationContext) captureRuntimeFrame() *runtimeFrame {
	if context.frame != nil {
		return context.frame
	}

	return newRuntimeRootFrame(context.runtimeEnv)
}

func (context typedEvaluationContext) captureLambdaScope() (map[string]RuntimeValue, *runtimeFrame) {
	if context.frame != nil {
		return nil, context.frame
	}

	return nil, newRuntimeRootFrame(context.runtimeEnv)
}

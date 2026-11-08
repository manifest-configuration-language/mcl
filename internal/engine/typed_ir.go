package engine

import "maps"

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

func (context typedEvaluationContext) captureRuntimeScope() map[string]RuntimeValue {
	if context.frame != nil {
		return context.frame.flatten()
	}

	return maps.Clone(context.runtimeEnv)
}

func (context typedEvaluationContext) captureLambdaScope() (map[string]RuntimeValue, *runtimeFrame) {
	if context.frame != nil {
		return nil, context.frame
	}

	return nil, newRuntimeRootFrame(context.runtimeEnv)
}

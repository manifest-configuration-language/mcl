package engine

import (
	"errors"
	"fmt"
)

func (expression typedExpression) evaluateBinary(context typedEvaluationContext) (RuntimeValue, error) {
	if expression.left == nil || expression.right == nil {
		return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, errors.New("invalid binary expression"))
	}
	left, err := expression.left.evaluate(context)
	if err != nil {
		return RuntimeValue{}, err
	}
	if left.IsDeferred() && (expression.operator == typedOperatorAnd || expression.operator == typedOperatorOr || expression.operator == typedOperatorCoalesce) {
		rightExpression := cloneTypedExpression(*expression.right)

		return RuntimeValue{
			Value: runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
			Deferred: &typedContinuation{Binary: &typedBinaryContinuation{
				Operator: expression.operator, Left: left, RightExpression: &rightExpression, Scope: context.captureRuntimeScope(),
			}},
		}, nil
	}
	//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
	switch expression.operator {
	case typedOperatorAnd, typedOperatorOr:
		if left.Kind() != Bool {
			return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, typeDiagnostic(fmt.Errorf("%s expects Bool operands", expression.operator.token())))
		}
		if expression.operator == typedOperatorAnd && !left.BoolValue() || expression.operator == typedOperatorOr && left.BoolValue() {
			return runtimeValueWithExpressionSources(left, context.sourcePath, expression.span, left), nil
		}
	case typedOperatorCoalesce:
		if left.Kind() != Null {
			return runtimeValueWithExpressionSources(left, context.sourcePath, expression.span, left), nil
		}
	}
	right, err := expression.right.evaluate(context)
	if err != nil {
		return RuntimeValue{}, err
	}
	if left.IsDeferred() || right.IsDeferred() {
		return RuntimeValue{
			Value: runtimeValueData{Kind: Null, Span: expression.span, SourcePath: context.sourcePath},
			Deferred: &typedContinuation{Binary: &typedBinaryContinuation{
				Operator: expression.operator, Left: left, Right: right, Scope: context.captureRuntimeScope(),
			}},
		}, nil
	}
	result, err := applyRuntimeTypedBinary(expression.operator, left, right)
	if err != nil {
		return RuntimeValue{}, sourceDiagnostic(context.sourcePath, expression.span, err)
	}

	return runtimeValueWithExpressionSources(result, context.sourcePath, expression.span, left, right), nil
}

package engine

import (
	"errors"
	"fmt"
	"slices"
)

func typedOperatorFromToken(token string) (typedOperator, bool) {
	switch token {
	case "==":
		return typedOperatorEqual, true
	case "!=":
		return typedOperatorNotEqual, true
	case "<":
		return typedOperatorLess, true
	case "<=":
		return typedOperatorLessEqual, true
	case ">":
		return typedOperatorGreater, true
	case ">=":
		return typedOperatorGreaterEqual, true
	case "+":
		return typedOperatorAdd, true
	case "-":
		return typedOperatorSubtract, true
	case "*":
		return typedOperatorMultiply, true
	case "/":
		return typedOperatorDivide, true
	case "%":
		return typedOperatorModulo, true
	case operatorAnd:
		return typedOperatorAnd, true
	case operatorOr:
		return typedOperatorOr, true
	case "??":
		return typedOperatorCoalesce, true
	case "in":
		return typedOperatorIn, true
	default:
		return 0, false
	}
}

func (operator typedOperator) token() string {
	switch operator {
	case typedOperatorEqual:
		return "=="
	case typedOperatorNotEqual:
		return "!="
	case typedOperatorLess:
		return "<"
	case typedOperatorLessEqual:
		return "<="
	case typedOperatorGreater:
		return ">"
	case typedOperatorGreaterEqual:
		return ">="
	case typedOperatorAdd:
		return "+"
	case typedOperatorSubtract:
		return "-"
	case typedOperatorMultiply:
		return "*"
	case typedOperatorDivide:
		return "/"
	case typedOperatorModulo:
		return "%"
	case typedOperatorAnd:
		return operatorAnd
	case typedOperatorOr:
		return operatorOr
	case typedOperatorCoalesce:
		return "??"
	case typedOperatorIn:
		return "in"
	default:
		return ""
	}
}

func isBooleanResultOperator(operator typedOperator) bool {
	return isBooleanComparisonOperator(operator) || isLogicalOperator(operator)
}

func isBooleanComparisonOperator(operator typedOperator) bool {
	return operator == typedOperatorEqual || operator == typedOperatorNotEqual || operator == typedOperatorIn || isOrderedComparisonOperator(operator)
}

func isLogicalOperator(operator typedOperator) bool {
	return operator == typedOperatorAnd || operator == typedOperatorOr
}

func isArithmeticOperator(operator typedOperator) bool {
	return operator == typedOperatorAdd || operator == typedOperatorSubtract || operator == typedOperatorMultiply || operator == typedOperatorDivide || operator == typedOperatorModulo
}

func isOrderedComparisonOperator(operator typedOperator) bool {
	return operator == typedOperatorLess || operator == typedOperatorLessEqual || operator == typedOperatorGreater || operator == typedOperatorGreaterEqual
}

func isOrderedPrimitiveType(kind typedScalarKind) bool {
	return kind == typedScalarInt || kind == typedScalarDecimal || kind == typedScalarString
}

func validTypedBinaryTypes(operator typedOperator, inferred, left, right ResolvedType, types map[string]ResolvedType) bool {
	if operator != typedOperatorCoalesce {
		leftOptions, leftKnown := flattenStaticTypeOptions(left, types)
		rightOptions, rightKnown := flattenStaticTypeOptions(right, types)
		if leftKnown && rightKnown && (len(leftOptions) > 1 || len(rightOptions) > 1) {
			for _, leftOption := range leftOptions {
				for _, rightOption := range rightOptions {
					if !validTypedBinaryTypesCore(operator, inferred, leftOption, rightOption, types) {
						return false
					}
				}
			}

			return true
		}
	}

	return validTypedBinaryTypesCore(operator, inferred, left, right, types)
}

func validTypedBinaryTypesCore(operator typedOperator, inferred, left, right ResolvedType, types map[string]ResolvedType) bool {
	if isLogicalOperator(operator) {
		for _, operand := range []ResolvedType{left, right} {
			if !staticTypeCompatibility(operand, ResolvedType{Name: typeBool}, types).isAssignable() {
				return false
			}
		}

		return staticTypeCompatibility(ResolvedType{Name: typeBool}, inferred, types).isAssignable()
	}
	if operator == typedOperatorIn {
		if !staticTypeCompatibility(ResolvedType{Name: typeBool}, inferred, types).isAssignable() {
			return false
		}
		if _, isList := staticListElement(right, types); isList {
			return true
		}
		if !isStaticMapType(right, types) {
			return false
		}

		return staticTypeCompatibility(left, ResolvedType{Name: typeString}, types).isAssignable()
	}
	if operator == typedOperatorCoalesce {
		left, hasNull := removeNullStaticType(left)
		for _, operand := range []ResolvedType{left, right} {
			if hasNull && operand.Name == typeNull {
				continue
			}
			if !staticTypeCompatibility(operand, inferred, types).isAssignable() {
				return false
			}
		}

		return true
	}
	leftResolved, leftKnown := resolveStaticAlias(left, types, make(map[string]bool))
	rightResolved, rightKnown := resolveStaticAlias(right, types, make(map[string]bool))
	if !leftKnown || !rightKnown {
		return false
	}
	if isOrderedComparisonOperator(operator) {
		leftScalar, leftIsScalar := typedScalarTypeFromTypeExpr(leftResolved)
		rightScalar, rightIsScalar := typedScalarTypeFromTypeExpr(rightResolved)

		return staticTypeCompatibility(ResolvedType{Name: typeBool}, inferred, types).isAssignable() && leftIsScalar && rightIsScalar && leftScalar.kind == rightScalar.kind && isOrderedPrimitiveType(leftScalar.kind)
	}
	if operator == typedOperatorEqual || operator == typedOperatorNotEqual {
		return staticTypeCompatibility(ResolvedType{Name: typeBool}, inferred, types).isAssignable()
	}
	if operator == typedOperatorAdd && leftResolved.Name == typeString && rightResolved.Name == typeString {
		return true
	}
	if !isArithmeticOperator(operator) {
		return false
	}

	leftScalar, leftIsScalar := typedScalarTypeFromTypeExpr(leftResolved)
	rightScalar, rightIsScalar := typedScalarTypeFromTypeExpr(rightResolved)
	if !leftIsScalar || !rightIsScalar || leftScalar.kind != rightScalar.kind {
		return false
	}

	return leftScalar.kind == typedScalarInt || leftScalar.kind == typedScalarDecimal
}

func applyRuntimeTypedBinary(operator typedOperator, left, right RuntimeValue) (RuntimeValue, error) {
	if operator == typedOperatorAnd || operator == typedOperatorOr {
		if right.Kind() != Bool {
			return RuntimeValue{}, typeDiagnostic(fmt.Errorf("%s expects Bool operands", operator.token()))
		}

		return right, nil
	}
	if operator == typedOperatorCoalesce {
		if left.Kind() != Null {
			return left, nil
		}

		return right, nil
	}
	if operator == typedOperatorEqual || operator == typedOperatorNotEqual {
		equal := equalRuntimeValues(left, right)

		return RuntimeValue{Value: runtimeValueData{Kind: Bool, Bool: equal == (operator == typedOperatorEqual)}}, nil
	}
	if operator == typedOperatorIn {
		switch right.Kind() {
		case List, AdapterBundle:
			items := right.List

			return RuntimeValue{Value: runtimeValueData{Kind: Bool, Bool: slices.ContainsFunc(items, func(item RuntimeValue) bool { return equalRuntimeValues(left, item) })}}, nil
		case Map:
			if left.Kind() != String {
				return RuntimeValue{}, typeDiagnostic(errors.New("map membership expects a String key"))
			}
			_, exists := right.MapLookup(left.TextValue())

			return RuntimeValue{Value: runtimeValueData{Kind: Bool, Bool: exists}}, nil
		default:
			return RuntimeValue{}, typeDiagnostic(fmt.Errorf("in expects a List or Map on the right, got %s", kindName(right.Kind())))
		}
	}
	if isOrderedComparisonOperator(operator) {
		comparison, err := compareValues(left.Metadata(), right.Metadata())
		if err != nil {
			return RuntimeValue{}, err
		}
		var result bool
		//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
		switch operator {
		case typedOperatorLess:
			result = comparison < 0
		case typedOperatorLessEqual:
			result = comparison <= 0
		case typedOperatorGreater:
			result = comparison > 0
		case typedOperatorGreaterEqual:
			result = comparison >= 0
		}

		return RuntimeValue{Value: runtimeValueData{Kind: Bool, Bool: result}}, nil
	}
	if operator == typedOperatorAdd && left.Kind() == String && right.Kind() == String {
		return RuntimeValue{Value: runtimeValueData{Kind: String, Text: left.TextValue() + right.TextValue()}}, nil
	}
	if operator == typedOperatorAdd || isArithmeticOperator(operator) {
		result, err := arithmetic(operator.token(), left.Metadata(), right.Metadata())
		if err != nil {
			return RuntimeValue{}, err
		}

		return RuntimeValue{Value: result}, nil
	}

	return RuntimeValue{}, fmt.Errorf("unsupported operator %q for %s and %s", operator.token(), kindName(left.Kind()), kindName(right.Kind()))
}

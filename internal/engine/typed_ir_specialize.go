package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

func specializeTypedExpression(expression typedExpression, substitutions map[string]ResolvedType, types map[string]ResolvedType) (typedExpression, error) {
	expression.resolvedType = substituteTypeParameters(expression.resolvedType, substitutions)
	var err error
	expression.analysisFacts, err = specializeTypeAnalysisFacts(expression.analysisFacts, substitutions, types)
	if err != nil {
		return typedExpression{}, err
	}
	if expression.genericVariable != "" {
		if baseType, exists := substitutions[expression.genericVariable]; exists {
			resolved, known, err := specializedReferenceType(expression.text, baseType, types)
			if err != nil {
				return typedExpression{}, err
			}
			if !known {
				return typedExpression{}, fmt.Errorf("cannot infer generic reference %q", expression.text)
			}
			expression.resolvedType = resolved
		}
	}
	if err := specializeTypedChild(&expression.operand, substitutions, types); err != nil {
		return typedExpression{}, err
	}
	if err := specializeTypedChild(&expression.left, substitutions, types); err != nil {
		return typedExpression{}, err
	}
	if err := specializeTypedChild(&expression.right, substitutions, types); err != nil {
		return typedExpression{}, err
	}
	if err := specializeTypedChild(&expression.condition, substitutions, types); err != nil {
		return typedExpression{}, err
	}
	if err := specializeTypedChild(&expression.thenBranch, substitutions, types); err != nil {
		return typedExpression{}, err
	}
	if err := specializeTypedChild(&expression.elseBranch, substitutions, types); err != nil {
		return typedExpression{}, err
	}
	if err := specializeTypedChild(&expression.callee, substitutions, types); err != nil {
		return typedExpression{}, err
	}
	expression.items = slices.Clone(expression.items)
	for index := range expression.items {
		item, err := specializeTypedExpression(expression.items[index], substitutions, types)
		if err != nil {
			return typedExpression{}, err
		}
		expression.items[index] = item
	}
	expression.entries = slices.Clone(expression.entries)
	for index := range expression.entries {
		if expression.entries[index].keyExpression != nil {
			key, specializeErr := specializeTypedExpression(*expression.entries[index].keyExpression, substitutions, types)
			if specializeErr != nil {
				return typedExpression{}, specializeErr
			}
			expression.entries[index].keyExpression = &key
		}
		value, err := specializeTypedExpression(expression.entries[index].value, substitutions, types)
		if err != nil {
			return typedExpression{}, err
		}
		expression.entries[index].value = value
	}
	if expression.kind == typedExpressionList {
		var elementType ResolvedType
		for _, item := range expression.items {
			elementType = joinStaticTypes(elementType, item.resolvedType)
		}
		if len(expression.items) == 0 {
			elementType = ResolvedType{Name: typeAny}
		}
		expression.resolvedType = ResolvedType{Name: typeList, Element: &elementType}
	}
	if expression.kind == typedExpressionMap {
		fields := make([]TypeField, 0, len(expression.entries))
		for _, entry := range expression.entries {
			fields = append(fields, TypeField{Name: entry.key, NameSpan: entry.keySpan, Type: entry.value.resolvedType})
		}
		expression.resolvedType = ResolvedType{Fields: fields}
	}
	expression.args = slices.Clone(expression.args)
	for index := range expression.args {
		argument, err := specializeTypedExpression(expression.args[index], substitutions, types)
		if err != nil {
			return typedExpression{}, err
		}
		expression.args[index] = argument
	}
	if expression.lambdaTyping != nil {
		typing := *expression.lambdaTyping
		typing.ParameterTypes = slices.Clone(typing.ParameterTypes)
		for index := range typing.ParameterTypes {
			typing.ParameterTypes[index] = substituteTypeParameters(typing.ParameterTypes[index], substitutions)
		}
		if typing.ReturnType != nil {
			returnType := substituteTypeParameters(*typing.ReturnType, substitutions)
			typing.ReturnType = &returnType
		}
		if typing.InferenceBody != nil {
			inferenceBody, err := specializeTypedExpression(*typing.InferenceBody, substitutions, types)
			if err != nil {
				return typedExpression{}, err
			}
			typing.InferenceBody = &inferenceBody
		}
		typing.TypeScope = maps.Clone(typing.TypeScope)
		for name, analysis := range typing.TypeScope {
			analysis.Type = substituteTypeParameters(analysis.Type, substitutions)
			typing.TypeScope[name] = analysis
		}
		expression.lambdaTyping = &typing
	}
	//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
	switch expression.kind {
	case typedExpressionNot:
		if expression.operand == nil {
			return typedExpression{}, errors.New("invalid generic unary expression")
		}
		if isLambdaTypeVariable(expression.operand.resolvedType) {
			break
		}
		if !isStaticallyAssignable(*expression.operand, ResolvedType{Name: typeBool}, types) {
			return typedExpression{}, errors.New("not expects a Bool operand")
		}
		expression.resolvedType = ResolvedType{Name: typeBool}
	case typedExpressionBinary:
		if expression.left == nil || expression.right == nil {
			return typedExpression{}, errors.New("invalid generic binary expression")
		}
		if isLambdaTypeVariable(expression.left.resolvedType) || isLambdaTypeVariable(expression.right.resolvedType) {
			break
		}
		resultType, valid := specializedBinaryResultType(expression.operator, expression.left.resolvedType, expression.right.resolvedType, types)
		if !valid {
			return typedExpression{}, fmt.Errorf("operator %s does not accept %s and %s", expression.operator.token(), staticTypeName(expression.left.resolvedType), staticTypeName(expression.right.resolvedType))
		}
		expression.resolvedType = resultType
	case typedExpressionConditional:
		if expression.condition == nil || expression.thenBranch == nil || expression.elseBranch == nil || !isStaticallyAssignable(*expression.condition, ResolvedType{Name: typeBool}, types) {
			if expression.condition != nil && isLambdaTypeVariable(expression.condition.resolvedType) && expression.thenBranch != nil && expression.elseBranch != nil {
				break
			}

			return typedExpression{}, errors.New("if condition must be Bool")
		}
		expression.resolvedType = joinStaticTypes(expression.thenBranch.resolvedType, expression.elseBranch.resolvedType)
	case typedExpressionIndexAccess:
		if expression.left == nil || expression.right == nil {
			return typedExpression{}, errors.New("invalid generic index expression")
		}
		if isLambdaTypeVariable(expression.left.resolvedType) || isLambdaTypeVariable(expression.right.resolvedType) {
			break
		}
		baseType, baseKnown := resolveStaticAlias(expression.left.resolvedType, types, make(map[string]bool))
		keyType, keyKnown := resolveStaticAlias(expression.right.resolvedType, types, make(map[string]bool))
		if !baseKnown || !keyKnown {
			return typedExpression{}, errors.New("cannot infer generic index types")
		}
		resultType, known, indexErr := staticIndexExprTypeWithConstant(baseType, keyType, types, expression.right.analysisFacts.ConstantString, expression.right.analysisFacts.ConstantStringKnown)
		if indexErr != nil || !known {
			if indexErr != nil {
				return typedExpression{}, indexErr
			}

			return typedExpression{}, errors.New("cannot infer generic index result type")
		}
		expression.resolvedType = resultType
	}

	return expression, nil
}

func specializeTypeAnalysisFacts(facts TypeAnalysisFacts, substitutions map[string]ResolvedType, types map[string]ResolvedType) (TypeAnalysisFacts, error) {
	facts = cloneTypeAnalysisFacts(facts)
	if facts.Replacement != nil {
		replacement := substituteTypeParameters(*facts.Replacement, substitutions)
		facts.Replacement = &replacement
	}
	for name, fieldFacts := range facts.Fields {
		specialized, err := specializeTypeAnalysisFacts(fieldFacts, substitutions, types)
		if err != nil {
			return TypeAnalysisFacts{}, err
		}
		facts.Fields[name] = specialized
	}
	for index, elementFacts := range facts.Elements {
		specialized, err := specializeTypeAnalysisFacts(elementFacts, substitutions, types)
		if err != nil {
			return TypeAnalysisFacts{}, err
		}
		facts.Elements[index] = specialized
	}
	if facts.FunctionInference == nil {
		return facts, nil
	}
	inference := *facts.FunctionInference
	inference.Bindings = maps.Clone(inference.Bindings)
	for name, analysis := range inference.Bindings {
		analysis.Type = substituteTypeParameters(analysis.Type, substitutions)
		var err error
		analysis.Facts, err = specializeTypeAnalysisFacts(analysis.Facts, substitutions, types)
		if err != nil {
			return TypeAnalysisFacts{}, err
		}
		inference.Bindings[name] = analysis
	}
	if inference.TypedBody != nil {
		typedBody, err := specializeTypedExpression(*inference.TypedBody, substitutions, types)
		if err != nil {
			return TypeAnalysisFacts{}, err
		}
		typedBody.analysisFacts.FunctionInference = nil
		inference.TypedBody = &typedBody
	}
	facts.FunctionInference = &inference

	return facts, nil
}

func specializeTypedChild(child **typedExpression, substitutions map[string]ResolvedType, types map[string]ResolvedType) error {
	if *child == nil {
		return nil
	}
	childExpression := **child
	specialized, err := specializeTypedExpression(childExpression, substitutions, types)
	if err != nil {
		return err
	}
	*child = &specialized

	return nil
}

func specializedReferenceType(reference string, base ResolvedType, types map[string]ResolvedType) (ResolvedType, bool, error) {
	_, path, _ := strings.Cut(reference, ".")
	for path != "" {
		field, rest, _ := strings.Cut(path, ".")
		fieldType, known, exists, optional := staticField(base, field, types, make(map[string]bool))
		if !known {
			return ResolvedType{}, false, nil
		}
		if !exists {
			return ResolvedType{}, false, fmt.Errorf("field %q does not exist in the inferred type", field)
		}
		if optional {
			return ResolvedType{}, false, nil
		}
		base = fieldType
		path = rest
	}

	return base, true, nil
}

func isStaticallyAssignable(expression typedExpression, expected ResolvedType, types map[string]ResolvedType) bool {
	return staticTypeCompatibility(expression.resolvedType, expected, types).isAssignable()
}

func specializedBinaryResultType(operator typedOperator, left, right ResolvedType, types map[string]ResolvedType) (ResolvedType, bool) {
	if isLogicalOperator(operator) {
		return ResolvedType{Name: typeBool}, isStaticallyAssignable(typedExpression{resolvedType: left}, ResolvedType{Name: typeBool}, types) && isStaticallyAssignable(typedExpression{resolvedType: right}, ResolvedType{Name: typeBool}, types)
	}
	if isBooleanComparisonOperator(operator) {
		return ResolvedType{Name: typeBool}, validTypedBinaryTypes(operator, ResolvedType{Name: typeBool}, left, right, types)
	}
	left, leftKnown := resolveStaticAlias(left, types, make(map[string]bool))
	right, rightKnown := resolveStaticAlias(right, types, make(map[string]bool))
	if !leftKnown || !rightKnown {
		return ResolvedType{}, false
	}
	if operator == typedOperatorCoalesce {
		withoutNull, hasNull := removeNullStaticType(left)
		if !hasNull {
			return withoutNull, true
		}
		if withoutNull.Name == typeNull {
			return right, true
		}

		return joinStaticTypes(withoutNull, right), true
	}
	if operator == typedOperatorAdd && left.Name == typeString && right.Name == typeString {
		return ResolvedType{Name: typeString}, true
	}
	if left.Name == right.Name && (left.Name == typeInt || left.Name == typeDecimal) && isArithmeticOperator(operator) {
		return left, true
	}

	return ResolvedType{}, false
}

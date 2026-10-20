package engine

import (
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
)

func inferStaticExprType(expression Expr, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (ResolvedType, bool, error) {
	inferred, known, err := inferStaticExprTypeImpl(expression, bindings, types)

	return inferred, known, typeDiagnostic(err)
}

func inferStaticExprAnalysis(expression Expr, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (TypeAnalysis, bool, error) {
	typeExpr, known, err := inferStaticExprType(expression, bindings, types)
	if err != nil || !known {
		return TypeAnalysis{}, known, err
	}
	analysis := TypeAnalysis{Type: typeExpr}
	switch node := expression.(type) {
	case *StringExpr:
		analysis.Facts.ConstantString = node.Value
		analysis.Facts.ConstantStringKnown = true
	case *EmptyListExpr:
		analysis.Facts.BundleKnown = true
		analysis.Facts.Elements = []TypeAnalysisFacts{}
	case *ListExpr:
		analysis.Facts.BundleKnown = true
		analysis.Facts.Elements = make([]TypeAnalysisFacts, len(node.Items))
		for index, item := range node.Items {
			itemAnalysis, itemKnown, itemErr := inferStaticExprAnalysis(item, bindings, types)
			if itemErr != nil {
				return TypeAnalysis{}, false, fmt.Errorf("item %d: %w", index, itemErr)
			}
			if itemKnown {
				analysis.Facts.Elements[index] = cloneTypeAnalysisFacts(itemAnalysis.Facts)
			}
		}
	case *EmptyMapExpr:
		analysis.Facts.Fields = map[string]TypeAnalysisFacts{}
	case *MapExpr:
		analysis.Facts.Fields = make(map[string]TypeAnalysisFacts, len(node.Entries))
		for _, entry := range node.Entries {
			if entry.KeyExpr != nil {
				continue
			}
			fieldAnalysis, fieldKnown, fieldErr := inferStaticExprAnalysis(entry.Value, bindings, types)
			if fieldErr != nil {
				return TypeAnalysis{}, false, fmt.Errorf("field %q: %w", entry.Key, fieldErr)
			}
			if fieldKnown {
				analysis.Facts.Fields[entry.Key] = cloneTypeAnalysisFacts(fieldAnalysis.Facts)
			}
		}
	case *ReferenceExpr:
		root, path, _ := strings.Cut(node.Name, ".")
		if binding, exists := bindings[root]; exists {
			analysis = binding
			analysis.Type = typeExpr
			currentType := binding.Type
			currentFacts := cloneTypeAnalysisFacts(binding.Facts)
			optional := false
			for path != "" {
				field, tail, _ := strings.Cut(path, ".")
				fieldType, _, _, fieldOptional := staticField(currentType, field, types, make(map[string]bool))
				optional = optional || fieldOptional
				currentFacts = cloneTypeAnalysisFacts(currentFacts.Fields[field])
				currentType = fieldType
				path = tail
			}
			if !optional && root != node.Name {
				analysis.Facts = currentFacts
			}
		}
	case *IndexExpr:
		baseAnalysis, baseKnown, baseErr := inferStaticExprAnalysis(node.Base, bindings, types)
		keyAnalysis, keyKnown, keyErr := inferStaticExprAnalysis(node.Key, bindings, types)
		if baseErr != nil || keyErr != nil {
			return TypeAnalysis{}, false, errors.Join(baseErr, keyErr)
		}
		if baseKnown && keyKnown {
			if keyAnalysis.Facts.ConstantStringKnown {
				analysis.Facts = cloneTypeAnalysisFacts(baseAnalysis.Facts.Fields[keyAnalysis.Facts.ConstantString])
			} else if index, ok := node.Key.(*IntegerExpr); ok {
				if parsed, parseErr := strconv.Atoi(index.Value); parseErr == nil && parsed >= 0 && parsed < len(baseAnalysis.Facts.Elements) {
					analysis.Facts = cloneTypeAnalysisFacts(baseAnalysis.Facts.Elements[parsed])
				}
			}
		}
	case *LambdaExpr:
		analysis.Facts.FunctionInference = &FunctionInference{Body: node.Body, Bindings: maps.Clone(bindings)}
	case *CallExpr:
		if call, ok := replacementCall(expression); ok {
			replacement, replacementKnown, replacementErr := inferStaticExprType(call.Arguments[0], bindings, types)
			if replacementErr != nil || !replacementKnown {
				return TypeAnalysis{}, replacementKnown, replacementErr
			}
			analysis.Facts.Replacement = &replacement
		} else if facts, factsKnown, factsErr := inferStaticExprCallFacts(node, bindings, types); factsErr != nil {
			return TypeAnalysis{}, false, factsErr
		} else if factsKnown {
			analysis.Facts = facts
		}
	case *BinaryExpr:
		if node.Operator == "+" || node.Operator == "??" {
			left, leftKnown, leftErr := inferStaticExprAnalysis(node.Left, bindings, types)
			right, rightKnown, rightErr := inferStaticExprAnalysis(node.Right, bindings, types)
			if leftErr != nil || rightErr != nil {
				return TypeAnalysis{}, false, errors.Join(leftErr, rightErr)
			}
			if node.Operator == "??" {
				leftType, leftTypeKnown := removeNullStaticType(left.Type)
				if leftKnown && leftTypeKnown && leftType.Name == typeNull {
					analysis.Facts = right.Facts
				} else if leftKnown && rightKnown {
					analysis.Facts = joinTypeAnalysisFacts(left.Facts, right.Facts)
				}
			} else if leftKnown && rightKnown && left.Type.Name == typeString && right.Type.Name == typeString && left.Facts.ConstantStringKnown && right.Facts.ConstantStringKnown {
				analysis.Facts.ConstantString = left.Facts.ConstantString + right.Facts.ConstantString
				analysis.Facts.ConstantStringKnown = true
			}
		}
	case *ConditionalExpr:
		thenAnalysis, thenKnown, thenErr := inferStaticExprAnalysis(node.Then, narrowStaticBindings(node.Condition, bindings, types, true), types)
		elseAnalysis, elseKnown, elseErr := inferStaticExprAnalysis(node.Else, narrowStaticBindings(node.Condition, bindings, types, false), types)
		if thenErr != nil || elseErr != nil {
			return TypeAnalysis{}, false, errors.Join(thenErr, elseErr)
		}
		if thenKnown && elseKnown {
			analysis.Facts = joinTypeAnalysisFacts(thenAnalysis.Facts, elseAnalysis.Facts)
		}
	case *ForExpr:
		source, sourceKnown, sourceErr := inferStaticExprAnalysis(node.Iterable, bindings, types)
		if sourceErr != nil {
			return TypeAnalysis{}, false, sourceErr
		}
		if sourceKnown {
			analysis.Facts.Bundle = source.Facts.Bundle
			analysis.Facts.BundleKnown = source.Facts.BundleKnown
		}
	}

	return analysis, true, nil
}

func inferStaticExprCallFacts(call *CallExpr, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (TypeAnalysisFacts, bool, error) {
	functionName := ""
	if reference, ok := call.Callee.(*ReferenceExpr); ok {
		functionName, _ = staticFunctionName(reference.Name, bindings, types)
	}
	arguments, ok := staticExprCallArguments(functionName, call.Arguments, call.ArgumentNames)
	if !ok {
		arguments = call.Arguments
	}
	argumentAnalyses := make([]TypeAnalysis, len(arguments))
	argumentKnown := make([]bool, len(arguments))
	for index, argument := range arguments {
		analysis, known, err := inferStaticExprAnalysis(argument, bindings, types)
		if err != nil {
			return TypeAnalysisFacts{}, false, fmt.Errorf("argument %d: %w", index+1, err)
		}
		argumentAnalyses[index], argumentKnown[index] = analysis, known
	}
	facts := TypeAnalysisFacts{}
	switch functionName {
	case fnClone:
		if len(argumentAnalyses) > 0 && argumentKnown[0] {
			facts = cloneTypeAnalysisFacts(argumentAnalyses[0].Facts)
		}
	case fnConcat, fnUnion, fnMergeByKey:
		if len(argumentAnalyses) >= 2 && argumentKnown[0] && argumentKnown[1] {
			facts.Bundle = argumentAnalyses[0].Facts.Bundle || argumentAnalyses[1].Facts.Bundle
			facts.BundleKnown = argumentAnalyses[0].Facts.BundleKnown && argumentAnalyses[1].Facts.BundleKnown
		}
	case fnReplaceAt, fnMap, fnFilter, fnExactlyOne:
		if len(argumentAnalyses) > 0 && argumentKnown[0] {
			facts.Bundle = argumentAnalyses[0].Facts.Bundle
			facts.BundleKnown = argumentAnalyses[0].Facts.BundleKnown
		}
	case fnKeyOf:
		if len(argumentAnalyses) > 1 && argumentKnown[1] {
			facts.ConstantString = argumentAnalyses[1].Facts.ConstantString
			facts.ConstantStringKnown = argumentAnalyses[1].Facts.ConstantStringKnown
		}
	default:
		if item, exists := builtinByName(functionName); exists && item.inference == builtinInferenceAdapter {
			facts.Bundle = true
			facts.BundleKnown = true
		} else if callee, known, err := inferStaticExprAnalysis(call.Callee, bindings, types); err != nil {
			return TypeAnalysisFacts{}, false, err
		} else if known && callee.Facts.ReturnBundleParameterKnown && callee.Facts.ReturnBundleParameter >= 0 && callee.Facts.ReturnBundleParameter < len(argumentAnalyses) && argumentKnown[callee.Facts.ReturnBundleParameter] {
			facts.Bundle = argumentAnalyses[callee.Facts.ReturnBundleParameter].Facts.Bundle
			facts.BundleKnown = argumentAnalyses[callee.Facts.ReturnBundleParameter].Facts.BundleKnown
		}
	}

	return facts, true, nil
}

func joinTypeAnalysisFacts(left, right TypeAnalysisFacts) TypeAnalysisFacts {
	var facts TypeAnalysisFacts
	if left.Replacement != nil && right.Replacement != nil && equalStaticTypes(*left.Replacement, *right.Replacement) {
		replacement := *left.Replacement
		facts.Replacement = &replacement
	}
	if len(left.Fields) > 0 && len(left.Fields) == len(right.Fields) {
		facts.Fields = make(map[string]TypeAnalysisFacts, len(left.Fields))
		for name, leftField := range left.Fields {
			if rightField, exists := right.Fields[name]; exists {
				facts.Fields[name] = joinTypeAnalysisFacts(leftField, rightField)
			}
		}
		if len(facts.Fields) != len(left.Fields) {
			facts.Fields = nil
		}
	}
	if len(left.Elements) == len(right.Elements) && left.Elements != nil {
		facts.Elements = make([]TypeAnalysisFacts, len(left.Elements))
		for index := range left.Elements {
			facts.Elements[index] = joinTypeAnalysisFacts(left.Elements[index], right.Elements[index])
		}
	}
	if left.ConstantStringKnown && right.ConstantStringKnown && left.ConstantString == right.ConstantString {
		facts.ConstantString = left.ConstantString
		facts.ConstantStringKnown = true
	}
	if left.BundleKnown && right.BundleKnown && left.Bundle == right.Bundle {
		facts.Bundle = left.Bundle
		facts.BundleKnown = true
	}
	if left.ReturnBundleParameterKnown && right.ReturnBundleParameterKnown && left.ReturnBundleParameter == right.ReturnBundleParameter {
		facts.ReturnBundleParameter = left.ReturnBundleParameter
		facts.ReturnBundleParameterKnown = true
	}

	return facts
}

func replacementCall(expression Expr) (*CallExpr, bool) {
	call, ok := expression.(*CallExpr)
	if !ok || len(call.Arguments) != 1 {
		return nil, false
	}
	callee, ok := call.Callee.(*ReferenceExpr)

	return call, ok && callee.Name == "replace"
}

func inferStaticExprTypeImpl(expression Expr, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (ResolvedType, bool, error) {
	switch node := expression.(type) {
	case *NullExpr:
		return ResolvedType{Name: typeNull}, true, nil
	case *BoolExpr:
		return ResolvedType{Name: typeBool}, true, nil
	case *IntegerExpr:
		return ResolvedType{Name: typeInt}, true, nil
	case *DecimalExpr:
		return ResolvedType{Name: typeDecimal}, true, nil
	case *StringExpr:
		return ResolvedType{Name: typeString}, true, nil
	case *EmptyListExpr:
		return ResolvedType{Name: typeList, Element: new(ResolvedType{Name: typeAny})}, true, nil
	case *ListExpr:
		var element ResolvedType
		knownElement := false
		unknownElement := false
		for index, item := range node.Items {
			itemType, known, err := inferStaticExprType(item, bindings, types)
			if err != nil {
				return ResolvedType{}, false, fmt.Errorf("item %d: %w", index, err)
			}
			if !known {
				unknownElement = true

				continue
			}
			if !knownElement {
				element = itemType
				knownElement = true
			} else {
				element = joinStaticTypes(element, itemType)
			}
		}
		if unknownElement {
			for _, item := range node.Items {
				if containsLambdaTypeVariable(item, bindings) {
					return ResolvedType{}, false, nil
				}
			}

			return ResolvedType{Name: typeList}, true, nil
		}
		if len(node.Items) == 0 {
			element = ResolvedType{Name: typeAny}
		}

		return ResolvedType{Name: typeList, Element: &element}, true, nil
	case *EmptyMapExpr:
		return ResolvedType{Fields: []TypeField{}}, true, nil
	case *MapExpr:
		fields := make([]TypeField, 0, len(node.Entries))
		hasDynamicKey := false
		unknownDynamicKey := false
		unknownDynamicValue := false
		for _, entry := range node.Entries {
			if entry.KeyExpr != nil {
				keyType, known, err := inferStaticExprType(entry.KeyExpr, bindings, types)
				if err != nil {
					return ResolvedType{}, false, fmt.Errorf("dynamic key: %w", err)
				}
				if !known {
					unknownDynamicKey = true
				} else {
					matches, compatible := staticTypeAssignable(keyType, ResolvedType{Name: typeString}, types)
					if !compatible {
						unknownDynamicKey = true
					} else if !matches {
						return ResolvedType{}, false, fmt.Errorf("dynamic map key must be String, got %s", staticTypeName(keyType))
					} else {
						hasDynamicKey = true
					}
				}
			}
			fieldType, known, err := inferStaticExprType(entry.Value, bindings, types)
			if err != nil {
				return ResolvedType{}, false, fmt.Errorf("field %q: %w", entry.Key, err)
			}
			if !known {
				unknownDynamicValue = true
				fieldType = ResolvedType{}
			}
			fields = append(fields, TypeField{Name: entry.Key, Type: fieldType})
		}
		if unknownDynamicKey || unknownDynamicValue && hasDynamicKey {
			return ResolvedType{}, false, nil
		}
		if hasDynamicKey {
			var elem ResolvedType
			for _, field := range fields {
				if !staticTypeIsKnown(field.Type) {
					return ResolvedType{}, false, nil
				}
				elem = joinStaticTypes(elem, field.Type)
			}

			return ResolvedType{Name: typeMap, Element: &elem}, true, nil
		}

		return ResolvedType{Fields: fields}, true, nil
	case *ReferenceExpr:
		root, path, _ := strings.Cut(node.Name, ".")
		binding, exists := bindings[root]
		if !exists {
			return ResolvedType{}, false, nil
		}
		current := binding.Type
		optional := false
		for path != "" {
			field, tail, _ := strings.Cut(path, ".")
			fieldType, known, exists, fieldOptional := staticField(current, field, types, make(map[string]bool))
			if !known {
				return ResolvedType{}, false, nil
			}
			if !exists {
				return ResolvedType{}, false, fmt.Errorf("field %q does not exist in the inferred type", field)
			}
			optional = optional || fieldOptional
			current = fieldType
			path = tail
		}
		if optional {
			return ResolvedType{}, false, nil
		}

		return current, staticTypeIsKnown(current), nil
	case *OptionalReferenceExpr:
		return inferStaticOptionalReference(node.Path, bindings, types)
	case *IndexExpr:
		base, baseKnown, err := inferStaticExprType(node.Base, bindings, types)
		if err != nil {
			return ResolvedType{}, baseKnown, err
		}
		key, keyKnown, err := inferStaticExprType(node.Key, bindings, types)
		if err != nil {
			return ResolvedType{}, false, err
		}
		keyAnalysis, keyAnalysisKnown, err := inferStaticExprAnalysis(node.Key, bindings, types)
		if err != nil {
			return ResolvedType{}, false, err
		}
		if !baseKnown || !keyKnown {
			return ResolvedType{}, false, nil
		}
		base, baseKnown = resolveStaticAlias(base, types, make(map[string]bool))
		key, keyKnown = resolveStaticAlias(key, types, make(map[string]bool))
		if !baseKnown || !keyKnown {
			return ResolvedType{}, false, nil
		}

		return staticIndexExprTypeWithConstant(base, key, types, keyAnalysis.Facts.ConstantString, keyAnalysisKnown && keyAnalysis.Facts.ConstantStringKnown)
	case *NodeExpr:
		baseType, known, err := inferStaticExprType(node.Body, bindings, types)
		if err != nil || !known {
			return ResolvedType{}, known, err
		}

		return ResolvedType{Name: typeNode, Arguments: []ResolvedType{baseType}}, true, nil
	case *DeferredReferenceExpr:
		var childKnown bool
		var childUnknown bool
		var childType ResolvedType
		if node.Child != nil {
			inferredChildType, known, childErr := inferStaticExprType(node.Child, bindings, types)
			if childErr != nil {
				return ResolvedType{}, false, childErr
			}
			childUnknown = !known
			childKnown = known && inferredChildType.Name == typeNode && len(inferredChildType.Arguments) == 1
			if known && !childKnown {
				return ResolvedType{}, false, nil
			}
			if childKnown {
				childType = inferredChildType.Arguments[0]
			}
		}
		if node.Target == nil {
			return ResolvedType{}, false, nil
		}
		targetType, known, err := inferStaticExprType(node.Target, bindings, types)
		if err != nil {
			return ResolvedType{}, known, err
		}
		if !known || childUnknown {
			return ResolvedType{}, false, nil
		}
		targetType, known = resolveStaticAlias(targetType, types, make(map[string]bool))
		if !known {
			return ResolvedType{}, false, nil
		}
		if targetType.Name == typeNode && len(targetType.Arguments) == 1 {
			targetType = targetType.Arguments[0]
		}
		if childKnown {
			targetType = childType
		}
		for _, part := range node.Path {
			fieldType, fieldKnown, exists, optional := staticField(targetType, part, types, make(map[string]bool))
			if !fieldKnown || !exists || optional {
				return ResolvedType{}, false, nil
			}
			targetType = fieldType
		}

		return targetType, true, nil
	case *OverlayExpr:
		baseType, baseKnown, err := inferStaticExprType(node.Base, bindings, types)
		if err != nil {
			return ResolvedType{}, baseKnown, err
		}
		patch, patchKnown, err := inferStaticExprAnalysis(node.Patch, bindings, types)
		if err != nil {
			return ResolvedType{}, patchKnown, err
		}
		if !baseKnown || !patchKnown {
			return ResolvedType{}, false, nil
		}

		if patch.Facts.Replacement != nil {
			matches, compatible := staticTypeAssignable(*patch.Facts.Replacement, baseType, types)
			if compatible && !matches {
				return ResolvedType{}, false, fmt.Errorf("replacement value does not match %s", staticTypeName(baseType))
			}
			if !compatible {
				return ResolvedType{}, false, nil
			}

			return baseType, true, nil
		}

		return mergeStaticTypes(baseType, patch.Type, types)
	case *RemovalExpr:
		baseType, known, err := inferStaticExprType(node.Base, bindings, types)
		if err != nil || !known {
			return ResolvedType{}, known, err
		}
		for _, path := range node.Paths {
			baseType, known, err = removeStaticTypePath(baseType, strings.Split(strings.TrimPrefix(path, "."), "."), types)
			if err != nil || !known {
				return ResolvedType{}, known, err
			}
		}

		return baseType, true, nil
	case *LambdaExpr:
		parameters := make([]FunctionTypeParameter, len(node.Parameters))
		lambdaBindings := maps.Clone(bindings)
		if lambdaBindings == nil {
			lambdaBindings = make(map[string]TypeAnalysis)
		}
		for index, name := range node.Parameters {
			delete(lambdaBindings, name)
			parameters[index] = FunctionTypeParameter{Name: name}
		}
		var returnType *ResolvedType
		if node.Body != nil {
			inferred, known, err := inferStaticExprType(node.Body, lambdaBindings, types)
			if err != nil {
				return ResolvedType{}, false, fmt.Errorf("lambda body: %w", err)
			}
			if known {
				returnType = new(inferred)
			}
		}

		return ResolvedType{Name: typeFunction, Parameters: parameters, ReturnType: returnType}, true, nil
	case *CallExpr:
		return inferStaticExprCall(node, bindings, types)
	case *BinaryExpr:
		return inferStaticExprBinary(node, bindings, types)
	case *UnaryExpr:
		if node.Operator != operatorNot {
			return ResolvedType{}, false, nil
		}
		operand, known, err := inferStaticExprType(node.Operand, bindings, types)
		if err != nil || !known {
			return ResolvedType{}, known, err
		}
		matches, compatible := staticTypeAssignable(operand, ResolvedType{Name: typeBool}, types)
		if compatible && !matches {
			return ResolvedType{}, false, fmt.Errorf("not expects Bool, got %s", staticTypeName(operand))
		}

		return ResolvedType{Name: typeBool}, true, nil
	case *ConditionalExpr:
		if literal, ok := node.Condition.(*BoolExpr); ok {
			if literal.Value {
				return inferStaticExprType(node.Then, bindings, types)
			}

			return inferStaticExprType(node.Else, bindings, types)
		}
		condition, conditionKnown, err := inferStaticExprType(node.Condition, bindings, types)
		if err != nil {
			return ResolvedType{}, false, err
		}
		if conditionKnown {
			matches, compatible := staticTypeAssignable(condition, ResolvedType{Name: typeBool}, types)
			if compatible && !matches {
				return ResolvedType{}, false, fmt.Errorf("if condition must be Bool, got %s", staticTypeName(condition))
			}
		}
		thenType, thenKnown, err := inferStaticExprType(node.Then, narrowStaticBindings(node.Condition, bindings, types, true), types)
		if err != nil {
			return ResolvedType{}, false, fmt.Errorf("if branch: %w", err)
		}
		elseType, elseKnown, err := inferStaticExprType(node.Else, narrowStaticBindings(node.Condition, bindings, types, false), types)
		if err != nil {
			return ResolvedType{}, false, fmt.Errorf("else branch: %w", err)
		}
		if !thenKnown || !elseKnown {
			return ResolvedType{}, false, nil
		}

		return joinStaticTypes(thenType, elseType), true, nil
	case *ForExpr:
		sourceType, sourceKnown, err := inferStaticExprType(node.Iterable, bindings, types)
		if err != nil {
			return ResolvedType{}, sourceKnown, err
		}
		if !sourceKnown {
			if bodyErr := inferStaticForBodyError(node, bindings, types); bodyErr != nil {
				return ResolvedType{}, false, bodyErr
			}

			return ResolvedType{}, false, nil
		}
		resolved, resolvable := resolveStaticAlias(sourceType, types, make(map[string]bool))
		if !resolvable {
			if bodyErr := inferStaticForBodyError(node, bindings, types); bodyErr != nil {
				return ResolvedType{}, false, bodyErr
			}

			return ResolvedType{}, false, nil
		}
		options, optionsKnown := flattenStaticTypeOptions(resolved, types)
		if !optionsKnown {
			if bodyErr := inferStaticForBodyError(node, bindings, types); bodyErr != nil {
				return ResolvedType{}, false, bodyErr
			}

			return ResolvedType{}, false, nil
		}
		var elementType ResolvedType
		for index, option := range options {
			optionElement, isList := staticListElement(option, types)
			if !isList {
				if len(options) == 1 {
					return ResolvedType{}, false, fmt.Errorf("for expression expects a List, got %s", staticTypeName(option))
				}

				return ResolvedType{}, false, fmt.Errorf("for expression requires every union option to be a List, got %s", staticTypeName(option))
			}
			if index == 0 {
				elementType = optionElement
			} else {
				elementType = joinStaticTypes(elementType, optionElement)
			}
		}
		loopBindings := maps.Clone(bindings)
		if loopBindings == nil {
			loopBindings = make(map[string]TypeAnalysis)
		}
		loopBindings[node.Variable] = TypeAnalysis{Type: elementType}
		resultType, resultKnown, err := inferStaticExprType(node.Body, loopBindings, types)
		if err != nil {
			return ResolvedType{}, false, fmt.Errorf("for expression body: %w", err)
		}
		if !resultKnown {
			return ResolvedType{}, false, nil
		}

		return ResolvedType{Name: typeList, Element: &resultType}, true, nil
	default:
		return ResolvedType{}, false, nil
	}
}

func inferStaticForBodyError(expression *ForExpr, bindings map[string]TypeAnalysis, types map[string]ResolvedType) error {
	loopBindings := maps.Clone(bindings)
	if loopBindings == nil {
		loopBindings = make(map[string]TypeAnalysis)
	}
	loopBindings[expression.Variable] = TypeAnalysis{}
	if _, _, err := inferStaticExprType(expression.Body, loopBindings, types); err != nil {
		return fmt.Errorf("for expression body: %w", err)
	}

	return nil
}

func narrowStaticBindings(condition Expr, bindings map[string]TypeAnalysis, types map[string]ResolvedType, truthy bool) map[string]TypeAnalysis {
	if unary, ok := condition.(*UnaryExpr); ok && unary.Operator == operatorNot {
		return narrowStaticBindings(unary.Operand, bindings, types, !truthy)
	}
	binary, ok := condition.(*BinaryExpr)
	if !ok {
		return bindings
	}
	if binary.Operator == "&&" && truthy {
		return narrowStaticBindings(binary.Right, narrowStaticBindings(binary.Left, bindings, types, true), types, true)
	}
	if binary.Operator == "||" && !truthy {
		return narrowStaticBindings(binary.Right, narrowStaticBindings(binary.Left, bindings, types, false), types, false)
	}
	if binary.Operator != "==" && binary.Operator != "!=" {
		return bindings
	}
	target, literal := binary.Left, binary.Right
	if !isStaticNarrowingTarget(target) {
		target, literal = literal, target
	}
	if !isStaticNarrowingTarget(target) || !isStaticNarrowingLiteral(literal) {
		return bindings
	}
	name := staticNarrowingName(target)
	root := referenceRoot(name)
	binding, exists := bindings[root]
	if !exists {
		return bindings
	}
	current := binding.Type
	current, currentKnown := resolveStaticAlias(current, types, make(map[string]bool))
	if !currentKnown || len(current.Options) == 0 {
		return bindings
	}
	literalType, known, err := inferStaticExprType(literal, bindings, types)
	if err != nil || !known {
		return bindings
	}
	wantEqual := truthy == (binary.Operator == "==")
	options := make([]ResolvedType, 0, len(current.Options))
	for _, option := range current.Options {
		candidate, resolved := resolveStaticAlias(option, types, make(map[string]bool))
		if !resolved {
			return bindings
		}
		targetType, targetKnown, targetErr := inferStaticExprType(target, map[string]TypeAnalysis{root: {Type: candidate}}, types)
		if targetErr != nil || !targetKnown {
			if !wantEqual {
				options = append(options, option)
			}

			continue
		}
		targetOptions := targetType.Options
		if len(targetOptions) == 0 {
			targetOptions = []ResolvedType{targetType}
		}
		canEqual, definitelyEqual := false, true
		for _, targetOption := range targetOptions {
			if resolvedTargetOption, resolved := resolveStaticAlias(targetOption, types, make(map[string]bool)); resolved {
				targetOption = resolvedTargetOption
			}
			matches, compatible := staticTypeAssignable(targetOption, literalType, types)
			optionCanEqual := !compatible || matches
			optionDefinitelyEqual := matches && compatible && targetOption.Name == typeNull
			if targetOption.Name == typeString && literalType.Name == typeString {
				literalAnalysis, literalAnalysisKnown, _ := inferStaticExprAnalysis(literal, bindings, types)
				if targetOption.StringLiteralKnown && literalAnalysisKnown && literalAnalysis.Facts.ConstantStringKnown {
					optionDefinitelyEqual = targetOption.StringLiteral == literalAnalysis.Facts.ConstantString
					optionCanEqual = optionDefinitelyEqual
				} else {
					optionCanEqual = true
				}
			}
			canEqual = canEqual || optionCanEqual
			definitelyEqual = definitelyEqual && optionDefinitelyEqual
		}
		if wantEqual && canEqual || !wantEqual && !definitelyEqual {
			options = append(options, option)
		}
	}
	if len(options) == 0 || len(options) == len(current.Options) {
		return bindings
	}
	narrowed := maps.Clone(bindings)
	current.Options = options
	narrowed[root] = TypeAnalysis{Type: current, Facts: binding.Facts}

	return narrowed
}

func staticNarrowingName(expression Expr) string {
	switch node := expression.(type) {
	case *ReferenceExpr:
		return node.Name
	case *OptionalReferenceExpr:
		return node.Path
	default:
		return ""
	}
}

func isStaticNarrowingTarget(expression Expr) bool {
	switch node := expression.(type) {
	case *ReferenceExpr:
		return node.Name != ""
	case *OptionalReferenceExpr:
		return node.Path != ""
	default:
		return false
	}
}

func isStaticNarrowingLiteral(expression Expr) bool {
	switch expression.(type) {
	case *NullExpr, *BoolExpr, *IntegerExpr, *DecimalExpr, *StringExpr:
		return true
	default:
		return false
	}
}

func validateBooleanExprSelector(expression Expr, bindings map[string]TypeAnalysis, types map[string]ResolvedType) error {
	lambda, isLambda := expression.(*LambdaExpr)
	var selectorType ResolvedType
	var known bool
	var err error
	if isLambda {
		if lambda.Body == nil || len(lambda.Parameters) != 1 {
			return errors.New("selector must be a one-argument function")
		}
		scope := maps.Clone(bindings)
		if scope == nil {
			scope = make(map[string]TypeAnalysis)
		}
		scope[lambda.Parameters[0]] = TypeAnalysis{Type: ResolvedType{Name: typeAny}}
		selectorType, known, err = inferStaticExprType(lambda.Body, scope, types)
	} else {
		selectorType, known, err = inferStaticExprType(expression, bindings, types)
	}
	if err != nil || !known {
		return err
	}
	if !isLambda {
		selectorType, known = resolveStaticAlias(selectorType, types, make(map[string]bool))
		if !known || selectorType.Name == typeAny {
			return nil
		}
		if selectorType.Name != typeFunction {
			return errors.New("selector must be a function")
		}
		if len(selectorType.Parameters) != 1 {
			return errors.New("selector must be a one-argument function")
		}
		if selectorType.ReturnType == nil {
			return nil
		}
		selectorType = *selectorType.ReturnType
	}
	if selectorType.Name == typeAny {
		return nil
	}
	matches, compatible := staticTypeAssignable(selectorType, ResolvedType{Name: typeBool}, types)
	if compatible && !matches {
		return fmt.Errorf("selector must return Bool, got %s", staticTypeName(selectorType))
	}

	return nil
}

func staticIndexExprTypeWithConstant(base, key ResolvedType, types map[string]ResolvedType, constantKey string, constantKeyKnown bool) (ResolvedType, bool, error) {
	if base.Name == typeNode && len(base.Arguments) == 1 {
		return staticIndexExprTypeWithConstant(base.Arguments[0], key, types, constantKey, constantKeyKnown)
	}
	if len(base.Options) > 0 {
		var result ResolvedType
		unknownOption := false
		resultKnown := false
		for _, option := range base.Options {
			resolved, known := resolveStaticAlias(option, types, make(map[string]bool))
			if !known {
				unknownOption = true

				continue
			}
			optionType, known, err := staticIndexExprTypeWithConstant(resolved, key, types, constantKey, constantKeyKnown)
			if err != nil {
				return ResolvedType{}, known, err
			}
			if !known {
				unknownOption = true

				continue
			}
			if !resultKnown {
				result = optionType
				resultKnown = true
			} else {
				result = joinStaticTypes(result, optionType)
			}
		}
		if unknownOption {
			return ResolvedType{}, false, nil
		}

		return result, true, nil
	}
	if base.Name == typeAny {
		return ResolvedType{}, false, nil
	}
	if base.Fields != nil || base.OpenRecord {
		if !staticIndexKeyIs(key, typeString, types) {
			return ResolvedType{}, false, fmt.Errorf("map index must be String, got %s", staticTypeName(key))
		}
		if constantKeyKnown {
			for _, field := range base.Fields {
				if field.Name == constantKey {
					return field.Type, staticTypeIsKnown(field.Type), nil
				}
			}
			if base.Element != nil {
				return *base.Element, true, nil
			}
			if base.OpenRecord {
				if base.AdditionalProperties != nil {
					return *base.AdditionalProperties, staticTypeIsKnown(*base.AdditionalProperties), nil
				}

				return ResolvedType{Name: typeAny}, true, nil
			}

			return ResolvedType{}, false, fmt.Errorf("field %q does not exist in the inferred type", constantKey)
		}
		var result ResolvedType
		for index, field := range base.Fields {
			if !staticTypeIsKnown(field.Type) {
				return ResolvedType{}, false, nil
			}
			if index == 0 {
				result = field.Type
			} else {
				result = joinStaticTypes(result, field.Type)
			}
		}
		if base.OpenRecord {
			additional := ResolvedType{Name: typeAny}
			if base.AdditionalProperties != nil {
				additional = *base.AdditionalProperties
			}
			if len(base.Fields) == 0 {
				return additional, staticTypeIsKnown(additional), nil
			}

			return joinStaticTypes(result, additional), staticTypeIsKnown(additional), nil
		}

		return result, len(base.Fields) > 0, nil
	}
	if base.Name == typeList {
		if !staticIndexKeyIs(key, typeInt, types) {
			return ResolvedType{}, false, fmt.Errorf("list index must be Int, got %s", staticTypeName(key))
		}
		if base.Element == nil {
			return ResolvedType{}, false, nil
		}

		return *base.Element, true, nil
	}
	if base.Name == typeMap {
		if !staticIndexKeyIs(key, typeString, types) {
			return ResolvedType{}, false, fmt.Errorf("map index must be String, got %s", staticTypeName(key))
		}
		if base.Element == nil {
			return ResolvedType{}, false, nil
		}

		return *base.Element, true, nil
	}

	return ResolvedType{}, false, fmt.Errorf("cannot index a %s value", staticTypeName(base))
}

func inferStaticExprBinary(expression Expr, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (ResolvedType, bool, error) {
	node, ok := expression.(*BinaryExpr)
	if !ok || node.Left == nil || node.Right == nil {
		return ResolvedType{}, false, nil
	}
	left, leftKnown, err := inferStaticExprType(node.Left, bindings, types)
	if err != nil {
		return ResolvedType{}, false, err
	}
	right, rightKnown, err := inferStaticExprType(node.Right, bindings, types)
	if err != nil {
		return ResolvedType{}, false, err
	}
	if leftKnown {
		left, leftKnown = resolveStaticAlias(left, types, make(map[string]bool))
	}
	if rightKnown {
		right, rightKnown = resolveStaticAlias(right, types, make(map[string]bool))
	}
	sameOperand := isStaticNarrowingTarget(node.Left) && isStaticNarrowingTarget(node.Right) && node.Left.(interface{ ExprSpan() SourceSpan }) != nil && staticNarrowingName(node.Left) == staticNarrowingName(node.Right) && fmt.Sprintf("%T", node.Left) == fmt.Sprintf("%T", node.Right)
	if len(left.Options) > 0 || len(right.Options) > 0 {
		return inferStaticUnionBinary(node.Operator, left, right, sameOperand, types)
	}
	switch node.Operator {
	case operatorAnd, operatorOr:
		for _, operand := range []struct {
			typeExpr ResolvedType
			known    bool
		}{{left, leftKnown}, {right, rightKnown}} {
			if !operand.known {
				continue
			}
			matches, compatible := staticTypeAssignable(operand.typeExpr, ResolvedType{Name: typeBool}, types)
			if compatible && !matches {
				return ResolvedType{}, false, fmt.Errorf("%s expects Bool operands", node.Operator)
			}
		}

		return ResolvedType{Name: typeBool}, true, nil
	case "==", "!=":
		return ResolvedType{Name: typeBool}, true, nil
	case "<", "<=", ">", ">=":
		if leftKnown && rightKnown && sameOperand {
			if _, valid := staticSameOperandResultType(left, types, func(option ResolvedType) bool {
				return option.Name == typeInt || option.Name == typeDecimal || option.Name == typeString
			}); valid {
				return ResolvedType{Name: typeBool}, true, nil
			}
		}
		if leftKnown && left.Name != typeInt && left.Name != typeDecimal && left.Name != typeString {
			return ResolvedType{}, false, fmt.Errorf("%s values cannot be ordered", staticTypeName(left))
		}
		if rightKnown && right.Name != typeInt && right.Name != typeDecimal && right.Name != typeString {
			return ResolvedType{}, false, fmt.Errorf("%s values cannot be ordered", staticTypeName(right))
		}
		if leftKnown && rightKnown && len(left.Options) == 0 && len(right.Options) == 0 && !equalStaticTypes(left, right) {
			return ResolvedType{}, false, fmt.Errorf("cannot compare %s with %s", staticTypeName(left), staticTypeName(right))
		}

		return ResolvedType{Name: typeBool}, true, nil
	case "in":
		if rightKnown {
			container, known := resolveStaticAlias(right, types, make(map[string]bool))
			isContainer, _ := staticMembershipContainer(container, types)
			if known && !isContainer {
				return ResolvedType{}, false, fmt.Errorf("in expects a List or Map on the right, got %s", staticTypeName(container))
			}
			if known && leftKnown {
				options, optionsKnown := flattenStaticTypeOptions(container, types)
				if !optionsKnown {
					return ResolvedType{}, false, nil
				}
				for _, option := range options {
					if option.Name == typeNode && len(option.Arguments) == 1 {
						option = option.Arguments[0]
					}
					var expected ResolvedType
					isMapOption := false
					switch {
					case option.Name == typeList:
						if option.Element == nil {
							continue
						}
						expected = *option.Element
					case option.Name == typeMap || option.Fields != nil || option.OpenRecord:
						expected = ResolvedType{Name: typeString}
						isMapOption = true
					default:
						continue
					}
					matches, compatible := staticTypeAssignable(left, expected, types)
					if compatible && !matches {
						if isMapOption {
							return ResolvedType{}, false, errors.New("map membership expects a String key")
						}

						return ResolvedType{}, false, fmt.Errorf("list membership expects %s, got %s", staticTypeName(expected), staticTypeName(left))
					}
				}
			}
		}

		return ResolvedType{Name: typeBool}, true, nil
	case "??":
		if leftKnown {
			withoutNull, hasNull := removeNullStaticType(left)
			if !hasNull {
				return withoutNull, true, nil
			}
			if withoutNull.Name == typeNull {
				return right, rightKnown, nil
			}
			if !rightKnown {
				return ResolvedType{}, false, nil
			}

			return joinStaticTypes(withoutNull, right), true, nil
		}
	case "+":
		if leftKnown && left.Name != typeString && left.Name != typeInt && left.Name != typeDecimal {
			return ResolvedType{}, false, errors.New("operator + expects two numeric operands of the same type or two String operands")
		}
		if rightKnown && right.Name != typeString && right.Name != typeInt && right.Name != typeDecimal {
			return ResolvedType{}, false, errors.New("operator + expects two numeric operands of the same type or two String operands")
		}
		if !leftKnown || !rightKnown {
			return ResolvedType{}, false, nil
		}
		if sameOperand {
			if result, valid := staticSameOperandResultType(left, types, func(option ResolvedType) bool {
				return option.Name == typeString || option.Name == typeInt || option.Name == typeDecimal
			}); valid {
				return result, true, nil
			}
		}
		if left.Name == typeString && right.Name == typeString {
			return ResolvedType{Name: typeString}, true, nil
		}
		if left.Name == typeInt && right.Name == typeInt {
			return ResolvedType{Name: typeInt}, true, nil
		}
		if left.Name == typeDecimal && right.Name == typeDecimal {
			return ResolvedType{Name: typeDecimal}, true, nil
		}

		return ResolvedType{}, false, errors.New("operator + expects two numeric operands of the same type or two String operands")
	case "-", "*", "/", "%":
		if leftKnown && rightKnown && sameOperand {
			if result, valid := staticSameOperandResultType(left, types, func(option ResolvedType) bool {
				return option.Name == typeInt || option.Name == typeDecimal
			}); valid {
				return result, true, nil
			}
		}
		if leftKnown && left.Name != typeInt && left.Name != typeDecimal || rightKnown && right.Name != typeInt && right.Name != typeDecimal {
			return ResolvedType{}, false, fmt.Errorf("operator %s expects numeric operands (Int or Decimal)", node.Operator)
		}
		if leftKnown && rightKnown {
			if left.Name != right.Name {
				return ResolvedType{}, false, fmt.Errorf("operator %s expects operands of the same numeric type", node.Operator)
			}

			return ResolvedType{Name: left.Name}, true, nil
		}
	}

	return ResolvedType{}, false, nil
}

func inferStaticUnionBinary(operator string, left, right ResolvedType, sameOperand bool, types map[string]ResolvedType) (ResolvedType, bool, error) {
	leftOptions, leftKnown := flattenStaticTypeOptions(left, types)
	rightOptions, rightKnown := flattenStaticTypeOptions(right, types)
	if !leftKnown || !rightKnown {
		return ResolvedType{}, false, nil
	}
	if sameOperand {
		if !equalStaticTypes(left, right) || len(leftOptions) != len(rightOptions) {
			return ResolvedType{}, false, nil
		}
		rightOptions = leftOptions
	}
	var result ResolvedType
	resultKnown := false
	unknownCombination := false
	for leftIndex, leftOption := range leftOptions {
		for rightIndex, rightOption := range rightOptions {
			if sameOperand && leftIndex != rightIndex {
				continue
			}
			leftName, rightName := "left", "right"
			if sameOperand {
				leftName, rightName = "operand", "operand"
			}
			leftExpression := &ReferenceExpr{Name: leftName}
			rightExpression := &ReferenceExpr{Name: rightName}
			inferred, known, err := inferStaticExprBinary(
				&BinaryExpr{Operator: operator, Left: leftExpression, Right: rightExpression},
				map[string]TypeAnalysis{leftName: {Type: leftOption}, rightName: {Type: rightOption}},
				types,
			)
			if err != nil {
				return ResolvedType{}, false, err
			}
			if !known {
				unknownCombination = true

				continue
			}
			if !resultKnown {
				result = inferred
				resultKnown = true
			} else {
				result = joinStaticTypes(result, inferred)
			}
		}
	}
	if unknownCombination {
		return ResolvedType{}, false, nil
	}

	return result, staticTypeIsKnown(result), nil
}

func flattenStaticTypeOptions(typeExpr ResolvedType, types map[string]ResolvedType) ([]ResolvedType, bool) {
	if len(typeExpr.Options) == 0 {
		resolved, known := resolveStaticAlias(typeExpr, types, make(map[string]bool))
		if !known {
			return nil, false
		}
		if len(resolved.Options) == 0 {
			return []ResolvedType{resolved}, true
		}
		typeExpr = resolved
	}
	options := make([]ResolvedType, 0, len(typeExpr.Options))
	for _, option := range typeExpr.Options {
		resolvedOptions, known := flattenStaticTypeOptions(option, types)
		if !known {
			return nil, false
		}
		options = append(options, resolvedOptions...)
	}

	return options, true
}

package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

func inferStaticExprCall(expression Expr, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (ResolvedType, bool, error) {
	call, ok := expression.(*CallExpr)
	if !ok || call.Callee == nil {
		return ResolvedType{}, false, nil
	}
	calleeAnalysis, calleeKnown, err := inferStaticExprAnalysis(call.Callee, bindings, types)
	if err != nil {
		return ResolvedType{}, false, err
	}
	calleeType := calleeAnalysis.Type
	if calleeKnown {
		calleeType, calleeKnown = resolveStaticAlias(calleeType, types, make(map[string]bool))
		if calleeKnown {
			calleeAnalysis.Type = calleeType
		}
	}
	if calleeKnown && len(calleeType.Options) > 0 {
		var result ResolvedType
		for index, option := range calleeType.Options {
			option, known := resolveStaticAlias(option, types, make(map[string]bool))
			if !known || option.Name != typeFunction || option.ReturnType == nil && calleeAnalysis.Facts.FunctionInference == nil {
				return ResolvedType{}, false, nil
			}
			optionAnalysis := TypeAnalysis{Type: option, Facts: calleeAnalysis.Facts}
			inferred, known, err := inferStaticFunctionCall(optionAnalysis, call.Arguments, call.ArgumentNames, bindings, types)
			if err != nil || !known {
				return ResolvedType{}, known, err
			}
			if index == 0 {
				result = inferred
			} else {
				result = joinStaticTypes(result, inferred)
			}
		}

		return result, true, nil
	}
	if calleeKnown && calleeType.Name == typeFunction && (calleeType.ReturnType != nil || calleeAnalysis.Facts.FunctionInference != nil) {
		return inferStaticFunctionCall(calleeAnalysis, call.Arguments, call.ArgumentNames, bindings, types)
	}
	if func() bool {
		_, ok := call.Callee.(*ReferenceExpr)

		return !ok
	}() {
		return ResolvedType{}, false, nil
	}
	function, ok := staticFunctionName(call.Callee.(*ReferenceExpr).Name, bindings, types)
	if !ok {
		return ResolvedType{}, false, nil
	}
	arguments, ok := staticExprCallArguments(function, call.Arguments, call.ArgumentNames)
	if !ok {
		return ResolvedType{}, false, nil
	}
	item, builtinExists := builtinByName(function)
	if builtinExists && len(arguments) != len(item.parameters) {
		return ResolvedType{}, false, fmt.Errorf("%s expects %d arguments", function, len(item.parameters))
	}
	argumentTypes := make([]ResolvedType, len(arguments))
	argumentAnalyses := make([]TypeAnalysis, len(arguments))
	known := make([]bool, len(arguments))
	for index, argument := range arguments {
		analysis, isKnown, err := inferStaticExprAnalysis(argument, bindings, types)
		if err != nil {
			return ResolvedType{}, false, fmt.Errorf("argument %d: %w", index+1, err)
		}
		typeExpr := analysis.Type
		if isKnown {
			typeExpr, isKnown = resolveStaticAlias(typeExpr, types, make(map[string]bool))
		}
		argumentTypes[index], known[index] = typeExpr, isKnown
		argumentAnalyses[index] = analysis
	}
	if builtinExists {
		if function == fnKeyOf {
			return inferStaticKeyOfBuiltinWithAnalysis(arguments, argumentTypes, known, types, argumentAnalyses...)
		}
		if function == fnMergeByKey {
			return inferStaticMergeByKeyBuiltinWithAnalysis(arguments, argumentTypes, known, types, argumentAnalyses...)
		}
		if item.staticInference != nil {
			return item.staticInference(arguments, argumentTypes, known, types)
		}
		//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
		switch item.inference {
		case builtinInferenceHigherOrder:
			return inferStaticHigherOrderBuiltin(item, arguments, argumentTypes, known, bindings, types)
		case builtinInferenceAdapter:
			return inferStaticAdapterBuiltin(item, arguments, argumentTypes, known)
		}
	}

	return ResolvedType{}, false, nil
}

func inferStaticFunctionCall(calleeAnalysis TypeAnalysis, callArguments []Expr, argumentNames []string, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (ResolvedType, bool, error) {
	calleeType := calleeAnalysis.Type
	arguments, ok := staticExprFunctionCallArguments(calleeType.Parameters, callArguments, argumentNames)
	if !ok {
		return ResolvedType{}, false, fmt.Errorf("function call has %d arguments and %d names, but signature expects %d arguments", len(callArguments), len(argumentNames), len(calleeType.Parameters))
	}
	argumentTypes := make([]ResolvedType, len(arguments))
	argumentKnown := make([]bool, len(arguments))
	for index, argument := range arguments {
		argumentType, known, err := inferStaticExprCallArgument(argument, calleeType.Parameters[index].Type, bindings, types)
		if err != nil {
			return ResolvedType{}, false, fmt.Errorf("argument %d: %w", index+1, err)
		}
		argumentTypes[index], argumentKnown[index] = argumentType, known
		if !known {
			continue
		}
		matches, compatible := staticTypeAssignable(argumentType, calleeType.Parameters[index].Type, types)
		if compatible && !matches {
			return ResolvedType{}, false, fmt.Errorf("argument %q expects %s, got %s", calleeType.Parameters[index].Name, staticTypeName(calleeType.Parameters[index].Type), staticTypeName(argumentType))
		}
	}
	if inference := calleeAnalysis.Facts.FunctionInference; inference != nil {
		lambdaBindings := maps.Clone(inference.Bindings)
		if lambdaBindings == nil {
			lambdaBindings = make(map[string]TypeAnalysis)
		}
		typeSubstitutions := make(map[string]ResolvedType)
		for index, parameter := range calleeType.Parameters {
			if isLambdaTypeVariable(parameter.Type) {
				if argumentKnown[index] {
					typeSubstitutions[parameter.Type.Name] = argumentTypes[index]
					lambdaBindings[parameter.Name] = TypeAnalysis{Type: argumentTypes[index]}
				} else {
					delete(lambdaBindings, parameter.Name)
				}
			} else if parameter.Type.Name != "" || parameter.Type.Fields != nil || parameter.Type.OpenRecord || parameter.Type.Element != nil || len(parameter.Type.Options) > 0 {
				lambdaBindings[parameter.Name] = TypeAnalysis{Type: parameter.Type}
			} else if argumentKnown[index] {
				lambdaBindings[parameter.Name] = TypeAnalysis{Type: argumentTypes[index]}
			} else {
				delete(lambdaBindings, parameter.Name)
			}
		}
		if inference.TypedBody != nil {
			specialized, err := specializeTypedExpression(*inference.TypedBody, typeSubstitutions, types)
			if err != nil {
				return ResolvedType{}, false, fmt.Errorf("lambda body: %w", err)
			}
			returnType := specialized.resolvedType
			if calleeType.ReturnType != nil {
				expectedReturnType := substituteTypeParameters(*calleeType.ReturnType, typeSubstitutions)
				matches, compatible := staticTypeAssignable(returnType, expectedReturnType, types)
				if compatible && !matches {
					return ResolvedType{}, false, fmt.Errorf("lambda returns %s, declared %s", staticTypeName(returnType), staticTypeName(expectedReturnType))
				}
				if !containsLambdaTypeVariableType(expectedReturnType) {
					returnType = expectedReturnType
				}
			}

			return returnType, true, nil
		}
		if inference.Body == nil {
			return ResolvedType{}, false, nil
		}
		returnType, known, err := inferStaticExprType(inference.Body, lambdaBindings, types)
		if err != nil {
			return ResolvedType{}, false, fmt.Errorf("lambda body: %w", err)
		}
		if known {
			if calleeType.ReturnType != nil {
				expectedReturnType := substituteTypeParameters(*calleeType.ReturnType, typeSubstitutions)
				matches, compatible := staticTypeAssignable(returnType, expectedReturnType, types)
				if compatible && !matches {
					return ResolvedType{}, false, fmt.Errorf("lambda returns %s, declared %s", staticTypeName(returnType), staticTypeName(expectedReturnType))
				}
				returnType = expectedReturnType
			}

			return returnType, true, nil
		}
		if calleeType.ReturnType == nil {
			return ResolvedType{}, false, nil
		}
	}
	returnType := *calleeType.ReturnType
	typeSubstitutions := make(map[string]ResolvedType)
	for index, parameter := range calleeType.Parameters {
		if isLambdaTypeVariable(parameter.Type) && argumentKnown[index] {
			typeSubstitutions[parameter.Type.Name] = argumentTypes[index]
		}
	}
	returnType = substituteTypeParameters(returnType, typeSubstitutions)

	return returnType, true, nil
}

func containsLambdaTypeVariableType(typeExpr ResolvedType) bool {
	if isLambdaTypeVariable(typeExpr) {
		return true
	}
	if slices.ContainsFunc(typeExpr.Arguments, containsLambdaTypeVariableType) {
		return true
	}
	for _, parameter := range typeExpr.Parameters {
		if containsLambdaTypeVariableType(parameter.Type) {
			return true
		}
	}
	if typeExpr.ReturnType != nil && containsLambdaTypeVariableType(*typeExpr.ReturnType) {
		return true
	}
	if typeExpr.Element != nil && containsLambdaTypeVariableType(*typeExpr.Element) {
		return true
	}
	for _, field := range typeExpr.Fields {
		if containsLambdaTypeVariableType(field.Type) {
			return true
		}
	}

	return slices.ContainsFunc(typeExpr.Options, containsLambdaTypeVariableType)
}

func inferStaticLifecycleAttachBuiltin(_ []Expr, argumentTypes []ResolvedType, known []bool, types map[string]ResolvedType) (ResolvedType, bool, error) {
	if len(argumentTypes) != 5 || len(known) != 5 || !known[0] {
		return ResolvedType{}, false, nil
	}
	mapType := ResolvedType{Name: typeMap}
	matches, compatible := staticTypeAssignable(argumentTypes[0], mapType, types)
	if compatible && !matches {
		return ResolvedType{}, false, errors.New("lifecycle.attach value must be a Map")
	}
	if known[1] {
		stringType := ResolvedType{Name: typeString}
		eventsType := ResolvedType{Name: typeList, Element: &stringType}
		matches, compatible := staticTypeAssignable(argumentTypes[1], eventsType, types)
		if compatible && !matches {
			return ResolvedType{}, false, errors.New("lifecycle.attach events must be a List of Strings")
		}
	}
	if known[2] && argumentTypes[2].Name != typeInt {
		return ResolvedType{}, false, errors.New("lifecycle.attach priority must be an Integer")
	}
	if known[3] && argumentTypes[3].Name != typeBool {
		return ResolvedType{}, false, errors.New("lifecycle.attach wait must be a Bool")
	}
	if known[4] {
		stringType := ResolvedType{Name: typeString}
		cleanupType := ResolvedType{Name: typeList, Element: &stringType}
		matches, compatible := staticTypeAssignable(argumentTypes[4], cleanupType, types)
		if compatible && !matches {
			return ResolvedType{}, false, errors.New("lifecycle.attach cleanup must be a List of Strings")
		}
	}

	return argumentTypes[0], true, nil
}

func inferStaticKeyOfBuiltin(arguments []Expr, argumentTypes []ResolvedType, known []bool, types map[string]ResolvedType) (ResolvedType, bool, error) {
	return inferStaticKeyOfBuiltinWithAnalysis(arguments, argumentTypes, known, types)
}

func inferStaticKeyOfBuiltinWithAnalysis(arguments []Expr, argumentTypes []ResolvedType, known []bool, types map[string]ResolvedType, analyses ...TypeAnalysis) (ResolvedType, bool, error) {
	if len(arguments) != 2 || len(argumentTypes) != 2 || len(known) != 2 {
		return ResolvedType{}, false, nil
	}
	mappingKnown, keyKnown := known[0], known[1]
	mappingType, keyType := argumentTypes[0], argumentTypes[1]
	if mappingKnown && mappingType.Name != typeAny && !staticMapType(mappingType, types) {
		return ResolvedType{}, false, errors.New("keyOf expects a Map and a String")
	}
	if keyKnown && keyType.Name != typeString && keyType.Name != typeAny {
		return ResolvedType{}, false, errors.New("keyOf expects a Map and a String")
	}
	if keyKnown && keyType.Name == typeString {
		key, constant := staticExprStringConstant(arguments[1])
		if !constant && len(analyses) > 1 && analyses[1].Facts.ConstantStringKnown {
			key, constant = analyses[1].Facts.ConstantString, true
		}
		if mappingKnown && mappingType.Name != typeMap && (mappingType.Fields != nil || mappingType.OpenRecord) && constant {
			_, _, exists, _ := staticField(mappingType, key, types, make(map[string]bool))
			if !exists {
				resolved, known := resolveStaticAlias(mappingType, types, make(map[string]bool))
				if known {
					available := make([]string, 0, len(resolved.Fields))
					for _, field := range resolved.Fields {
						available = append(available, field.Name)
					}
					slices.Sort(available)

					return ResolvedType{}, false, fmt.Errorf("key %q does not exist; available keys: %s", key, strings.Join(available, ", "))
				}

				return ResolvedType{}, false, fmt.Errorf("key %q does not exist in the inferred Map", key)
			}
		}
	}
	if keyKnown && keyType.Name == typeString || mappingKnown && mappingType.Name != typeAny {
		return ResolvedType{Name: typeString}, true, nil
	}

	return ResolvedType{}, false, nil
}

func inferStaticMergeByKeyBuiltin(arguments []Expr, argumentTypes []ResolvedType, known []bool, types map[string]ResolvedType) (ResolvedType, bool, error) {
	return inferStaticMergeByKeyBuiltinWithAnalysis(arguments, argumentTypes, known, types)
}

func inferStaticMergeByKeyBuiltinWithAnalysis(arguments []Expr, argumentTypes []ResolvedType, known []bool, types map[string]ResolvedType, analyses ...TypeAnalysis) (ResolvedType, bool, error) {
	if len(arguments) != 3 || len(argumentTypes) != 3 || len(known) != 3 || !known[0] || !known[1] || !known[2] {
		return ResolvedType{}, false, nil
	}
	left, leftList := staticListElement(argumentTypes[0], types)
	right, rightList := staticListElement(argumentTypes[1], types)
	if !leftList || !rightList || argumentTypes[2].Name != typeString {
		return ResolvedType{}, false, errors.New("mergeByKey expects two Lists and a String key")
	}
	leftMap, leftKnown := resolveStaticAlias(left, types, make(map[string]bool))
	rightMap, rightKnown := resolveStaticAlias(right, types, make(map[string]bool))
	if leftKnown && leftMap.Name != typeAny && !staticMapType(leftMap, types) || rightKnown && rightMap.Name != typeAny && !staticMapType(rightMap, types) {
		return ResolvedType{}, false, errors.New("mergeByKey list items must be Maps")
	}
	key, constant := staticExprStringConstant(arguments[2])
	if !constant && len(analyses) > 2 && analyses[2].Facts.ConstantStringKnown {
		key, constant = analyses[2].Facts.ConstantString, true
	}
	if constant {
		for _, itemType := range []ResolvedType{leftMap, rightMap} {
			_, known, exists, optional := staticField(itemType, key, types, make(map[string]bool))
			if known && (!exists || optional) {
				return ResolvedType{}, false, fmt.Errorf("mergeByKey items must contain key %q", key)
			}
		}
		if hasStaticDuplicateExprMergeKey(arguments[0], key) || hasStaticDuplicateExprMergeKey(arguments[1], key) {
			return ResolvedType{}, false, fmt.Errorf("mergeByKey contains duplicate value for key %q", key)
		}
	}
	element := joinStaticTypes(leftMap, rightMap)
	if leftMap.Name == typeAny {
		element = rightMap
	} else if rightMap.Name == typeAny {
		element = leftMap
	}

	return ResolvedType{Name: typeList, Element: &element}, true, nil
}

func inferStaticConcatBuiltin(_ []Expr, argumentTypes []ResolvedType, known []bool, types map[string]ResolvedType) (ResolvedType, bool, error) {
	return inferStaticListCombinationBuiltin(fnConcat, argumentTypes, known, types)
}

func inferStaticUnionBuiltin(_ []Expr, argumentTypes []ResolvedType, known []bool, types map[string]ResolvedType) (ResolvedType, bool, error) {
	return inferStaticListCombinationBuiltin(fnUnion, argumentTypes, known, types)
}

func inferStaticListCombinationBuiltin(function string, argumentTypes []ResolvedType, known []bool, types map[string]ResolvedType) (ResolvedType, bool, error) {
	if len(argumentTypes) != 2 || len(known) != 2 || !known[0] || !known[1] {
		return ResolvedType{}, false, nil
	}
	left, leftList := staticListElement(argumentTypes[0], types)
	right, rightList := staticListElement(argumentTypes[1], types)
	if !leftList || !rightList {
		return ResolvedType{}, false, fmt.Errorf("%s expects two Lists", function)
	}
	element := joinStaticTypes(left, right)

	return ResolvedType{Name: typeList, Element: &element}, true, nil
}

func inferStaticReplaceAtBuiltin(_ []Expr, argumentTypes []ResolvedType, known []bool, types map[string]ResolvedType) (ResolvedType, bool, error) {
	if len(argumentTypes) != 3 || len(known) != 3 || !known[0] || !known[1] {
		return ResolvedType{}, false, nil
	}
	elementType, isList := staticListElement(argumentTypes[0], types)
	if !isList {
		return ResolvedType{}, false, errors.New("replaceAt expects a List and an Integer index")
	}
	if argumentTypes[1].Name != typeInt {
		return ResolvedType{}, false, errors.New("replaceAt expects a List and an Integer index")
	}
	if known[2] {
		matches, compatible := staticTypeAssignable(argumentTypes[2], elementType, types)
		if compatible && !matches {
			return ResolvedType{}, false, fmt.Errorf("replaceAt value expects %s, got %s", staticTypeName(elementType), staticTypeName(argumentTypes[2]))
		}
	}

	return argumentTypes[0], true, nil
}

func inferStaticCloneBuiltin(_ []Expr, argumentTypes []ResolvedType, known []bool, _ map[string]ResolvedType) (ResolvedType, bool, error) {
	if len(argumentTypes) != 1 || len(known) != 1 || !known[0] {
		return ResolvedType{}, false, nil
	}

	return argumentTypes[0], true, nil
}

func inferStaticLenBuiltin(_ []Expr, argumentTypes []ResolvedType, known []bool, types map[string]ResolvedType) (ResolvedType, bool, error) {
	if len(argumentTypes) != 1 || len(known) != 1 {
		return ResolvedType{}, false, nil
	}
	if known[0] && !staticLenType(argumentTypes[0], types) {
		return ResolvedType{}, false, fmt.Errorf("len does not accept %s", staticTypeName(argumentTypes[0]))
	}

	return ResolvedType{Name: typeInt}, true, nil
}

func inferStaticReplaceBuiltin(_ []Expr, argumentTypes []ResolvedType, known []bool, _ map[string]ResolvedType) (ResolvedType, bool, error) {
	if len(argumentTypes) != 1 || len(known) != 1 || !known[0] {
		return ResolvedType{}, false, nil
	}

	return argumentTypes[0], true, nil
}

func inferStaticContainsBuiltin(_ []Expr, argumentTypes []ResolvedType, known []bool, types map[string]ResolvedType) (ResolvedType, bool, error) {
	if len(argumentTypes) != 2 || len(known) != 2 || !known[0] {
		return ResolvedType{}, false, nil
	}
	validContainer, includesString := staticContainsContainer(argumentTypes[0], types)
	if !validContainer {
		return ResolvedType{}, false, fmt.Errorf("contains does not accept %s", staticTypeName(argumentTypes[0]))
	}
	if includesString && known[1] && argumentTypes[1].Name != typeString && argumentTypes[1].Name != typeAny {
		return ResolvedType{}, false, errors.New("contains expects a String search value")
	}
	if known[1] {
		for _, elementType := range staticContainsListElements(argumentTypes[0], types) {
			matches, compatible := staticTypeAssignable(argumentTypes[1], elementType, types)
			if compatible && !matches {
				return ResolvedType{}, false, fmt.Errorf("contains search value expects %s, got %s", staticTypeName(elementType), staticTypeName(argumentTypes[1]))
			}
		}
	}

	return ResolvedType{Name: typeBool}, true, nil
}

func staticContainsListElements(typeExpr ResolvedType, types map[string]ResolvedType) []ResolvedType {
	resolved, known := resolveStaticAlias(typeExpr, types, make(map[string]bool))
	if !known {
		return nil
	}
	if resolved.Name == typeNode && len(resolved.Arguments) == 1 {
		return staticContainsListElements(resolved.Arguments[0], types)
	}
	if len(resolved.Options) > 0 {
		var elements []ResolvedType
		for _, option := range resolved.Options {
			elements = append(elements, staticContainsListElements(option, types)...)
		}

		return elements
	}
	if resolved.Name != typeList {
		return nil
	}
	if resolved.Element == nil {
		return []ResolvedType{{Name: typeAny}}
	}

	return []ResolvedType{*resolved.Element}
}

func inferStaticHigherOrderBuiltin(item builtin, arguments []Expr, argumentTypes []ResolvedType, known []bool, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (ResolvedType, bool, error) {
	function := item.name
	if len(arguments) == 3 {
		if !known[0] || !known[1] {
			return ResolvedType{}, false, nil
		}
		element, isList := staticListElement(argumentTypes[0], types)
		if !isList {
			return ResolvedType{}, false, errors.New("fold expects a List")
		}
		lambda, isLambda := arguments[2].(*LambdaExpr)
		if !isLambda {
			functionAnalysis, functionAnalysisKnown, analysisErr := inferStaticExprAnalysis(arguments[2], bindings, types)
			if analysisErr != nil {
				return ResolvedType{}, false, analysisErr
			}
			if known[2] && argumentTypes[2].Name == typeFunction && argumentTypes[2].SchemaPath != "" {
				return inferStaticFoldFunction(functionAnalysis, argumentTypes[1], element, types)
			}
			functionType, functionKnown := resolveStaticAlias(argumentTypes[2], types, make(map[string]bool))
			if known[2] && functionKnown && functionType.Name != typeFunction && functionType.Name != typeAny {
				return ResolvedType{}, false, errors.New("fold expects a lambda")
			}
			if known[2] && functionAnalysisKnown && functionKnown && functionType.Name == typeFunction {
				return inferStaticFoldFunction(functionAnalysis, argumentTypes[1], element, types)
			}

			return ResolvedType{}, false, nil
		}
		if lambda.Body == nil || len(lambda.Parameters) != 2 {
			return ResolvedType{}, false, errors.New("fold combine expects a two-argument lambda")
		}
		bindingsForLambda := maps.Clone(bindings)
		if bindingsForLambda == nil {
			bindingsForLambda = make(map[string]TypeAnalysis)
		}
		bindingsForLambda[lambda.Parameters[0]] = TypeAnalysis{Type: argumentTypes[1]}
		bindingsForLambda[lambda.Parameters[1]] = TypeAnalysis{Type: element}
		resultType, resultKnown, err := inferStaticExprType(lambda.Body, bindingsForLambda, types)
		if err != nil {
			return ResolvedType{}, false, fmt.Errorf("fold lambda: %w", err)
		}
		if !resultKnown {
			return ResolvedType{}, false, nil
		}
		matches, compatible := staticTypeAssignable(resultType, argumentTypes[1], types)
		if compatible && !matches {
			return ResolvedType{}, false, fmt.Errorf("fold combine must return %s, got %s", staticTypeName(argumentTypes[1]), staticTypeName(resultType))
		}

		return argumentTypes[1], true, nil
	}
	if len(arguments) != 2 || !known[0] {
		return ResolvedType{}, false, nil
	}
	element, isList := staticListElement(argumentTypes[0], types)
	if !isList {
		return ResolvedType{}, false, fmt.Errorf("%s expects a List", function)
	}
	lambda, isLambda := arguments[1].(*LambdaExpr)
	if !isLambda {
		functionAnalysis, functionAnalysisKnown, analysisErr := inferStaticExprAnalysis(arguments[1], bindings, types)
		if analysisErr != nil {
			return ResolvedType{}, false, analysisErr
		}
		if known[1] && argumentTypes[1].Name == typeFunction && argumentTypes[1].SchemaPath != "" {
			return inferStaticHigherOrder(function, functionAnalysis, []ResolvedType{element}, argumentTypes[0], types)
		}
		functionType, functionKnown := resolveStaticAlias(argumentTypes[1], types, make(map[string]bool))
		if known[1] && functionKnown && functionType.Name != typeFunction && functionType.Name != typeAny {
			return ResolvedType{}, false, fmt.Errorf("%s expects a lambda", function)
		}
		if known[1] && functionAnalysisKnown && functionKnown && functionType.Name == typeFunction {
			return inferStaticHigherOrder(function, functionAnalysis, []ResolvedType{element}, argumentTypes[0], types)
		}

		return ResolvedType{}, false, nil
	}
	if lambda.Body == nil || len(lambda.Parameters) != 1 {
		return ResolvedType{}, false, fmt.Errorf("%s transform expects a one-argument lambda", function)
	}
	bindingsForLambda := maps.Clone(bindings)
	if bindingsForLambda == nil {
		bindingsForLambda = make(map[string]TypeAnalysis)
	}
	bindingsForLambda[lambda.Parameters[0]] = TypeAnalysis{Type: element}
	resultType, resultKnown, err := inferStaticExprType(lambda.Body, bindingsForLambda, types)
	if err != nil {
		return ResolvedType{}, false, fmt.Errorf("%s lambda: %w", function, err)
	}
	if function == fnFilter || function == fnExactlyOne {
		if resultKnown {
			matches, compatible := staticTypeAssignable(resultType, ResolvedType{Name: typeBool}, types)
			if compatible && !matches {
				return ResolvedType{}, false, fmt.Errorf("%s predicate must return Bool, got %s", function, staticTypeName(resultType))
			}
		}
		if function == fnExactlyOne {
			return element, true, nil
		}

		return argumentTypes[0], true, nil
	}
	if !resultKnown {
		return ResolvedType{}, false, nil
	}

	return ResolvedType{Name: typeList, Element: &resultType}, true, nil
}

func inferStaticFoldFunction(functionType TypeAnalysis, initial, element ResolvedType, types map[string]ResolvedType) (ResolvedType, bool, error) {
	inferred, known, err := inferStaticHigherOrder(fnFold, functionType, []ResolvedType{initial, element}, initial, types)
	if err != nil || !known {
		return inferred, known, err
	}
	matches, compatible := staticTypeAssignable(inferred, initial, types)
	if compatible && !matches {
		return ResolvedType{}, false, fmt.Errorf("fold combine must return %s, got %s", staticTypeName(initial), staticTypeName(inferred))
	}

	return initial, true, nil
}

func inferStaticBuiltinCallback(function, builtinName string, inputs []ResolvedType, types map[string]ResolvedType) (ResolvedType, bool, error) {
	item, exists := builtinByName(builtinName)
	if !exists || len(item.parameters) != len(inputs) {
		return ResolvedType{}, false, fmt.Errorf("%s function expects %d parameters", function, len(inputs))
	}
	arguments := make([]Expr, len(inputs))
	bindings := make(map[string]TypeAnalysis, len(inputs))
	known := make([]bool, len(inputs))
	for index, input := range inputs {
		name := fmt.Sprintf("$callback%d", index)
		arguments[index] = &ReferenceExpr{Name: name}
		bindings[name] = TypeAnalysis{Type: input}
		known[index] = true
	}
	if item.staticInference != nil {
		return item.staticInference(arguments, inputs, known, types)
	}
	switch item.inference {
	case builtinInferenceHigherOrder:
		return inferStaticHigherOrderBuiltin(item, arguments, inputs, known, bindings, types)
	case builtinInferenceAdapter:
		return inferStaticAdapterBuiltin(item, arguments, inputs, known)
	default:
		return ResolvedType{}, false, nil
	}
}

func inferStaticAdapterBuiltin(item builtin, arguments []Expr, argumentTypes []ResolvedType, known []bool) (ResolvedType, bool, error) {
	stringArguments := len(arguments)
	switch item.name {
	case fnHelmRender:
		stringArguments = 1
	case fnKustomizeBuild:
		stringArguments = 3
	}
	for index := range stringArguments {
		if known[index] && argumentTypes[index].Name != typeString && argumentTypes[index].Name != typeAny {
			return ResolvedType{}, false, fmt.Errorf("%s expects String arguments", item.name)
		}
	}
	if item.name == fnHelmRender && known[1] && argumentTypes[1].Fields == nil && !argumentTypes[1].OpenRecord && argumentTypes[1].Name != typeAny {
		return ResolvedType{}, false, errors.New("helm.render options must be a Map")
	}
	anyType := ResolvedType{Name: typeAny}

	return ResolvedType{Name: typeList, Element: &anyType}, true, nil
}

func inferStaticExprCallArgument(argument Expr, expected ResolvedType, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (ResolvedType, bool, error) {
	if func() bool {
		_, ok := argument.(*ConditionalExpr)

		return ok
	}() {
		return inferStaticConditionalCallArgument(argument, expected, bindings, types)
	}
	lambda, isLambda := argument.(*LambdaExpr)
	if !isLambda || lambda.Body == nil {
		return inferStaticExprType(argument, bindings, types)
	}
	expected, known := resolveStaticAlias(expected, types, make(map[string]bool))
	if !known || expected.Name != typeFunction || len(expected.Parameters) != len(lambda.Parameters) {
		return inferStaticExprType(argument, bindings, types)
	}
	lambdaBindings := maps.Clone(bindings)
	if lambdaBindings == nil {
		lambdaBindings = make(map[string]TypeAnalysis)
	}
	parameters := make([]FunctionTypeParameter, len(lambda.Parameters))
	for index, name := range lambda.Parameters {
		parameterType := expected.Parameters[index].Type
		lambdaBindings[name] = TypeAnalysis{Type: parameterType}
		parameters[index] = FunctionTypeParameter{Name: name, Type: parameterType}
	}
	returnType := expected.ReturnType
	inferred, returnKnown, err := inferStaticExprType(lambda.Body, lambdaBindings, types)
	if err != nil {
		return ResolvedType{}, false, fmt.Errorf("lambda body: %w", err)
	}
	if returnKnown {
		if returnType != nil {
			matches, compatible := staticTypeAssignable(inferred, *returnType, types)
			if compatible && !matches {
				return ResolvedType{}, false, fmt.Errorf("lambda returns %s, declared %s", staticTypeName(inferred), staticTypeName(*returnType))
			}
		} else {
			returnType = new(inferred)
		}
	}

	return ResolvedType{Name: typeFunction, Parameters: parameters, ReturnType: returnType}, true, nil
}

func inferStaticConditionalCallArgument(expression Expr, expected ResolvedType, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (ResolvedType, bool, error) {
	conditional, ok := expression.(*ConditionalExpr)
	if !ok || conditional.Condition == nil || conditional.Then == nil || conditional.Else == nil {
		return ResolvedType{}, false, nil
	}
	if func() bool {
		_, ok := conditional.Condition.(*BoolExpr)

		return ok
	}() {
		branch := conditional.Else
		if conditional.Condition.(*BoolExpr).Value {
			branch = conditional.Then
		}

		return inferStaticExprCallArgument(branch, expected, bindings, types)
	}
	conditionType, known, err := inferStaticExprType(conditional.Condition, bindings, types)
	if err != nil {
		return ResolvedType{}, false, fmt.Errorf("if condition: %w", err)
	}
	if known {
		matches, compatible := staticTypeAssignable(conditionType, ResolvedType{Name: typeBool}, types)
		if compatible && !matches {
			return ResolvedType{}, false, fmt.Errorf("if condition must be Bool, got %s", staticTypeName(conditionType))
		}
	}
	thenBindings := narrowStaticBindings(conditional.Condition, bindings, types, true)
	thenType, thenKnown, err := inferStaticExprCallArgument(conditional.Then, expected, thenBindings, types)
	if err != nil {
		return ResolvedType{}, false, fmt.Errorf("if branch: %w", err)
	}
	elseBindings := narrowStaticBindings(conditional.Condition, bindings, types, false)
	elseType, elseKnown, err := inferStaticExprCallArgument(conditional.Else, expected, elseBindings, types)
	if err != nil {
		return ResolvedType{}, false, fmt.Errorf("else branch: %w", err)
	}
	if !thenKnown || !elseKnown {
		return ResolvedType{}, false, nil
	}

	return joinStaticTypes(thenType, elseType), true, nil
}

func staticExprFunctionCallArguments(parameters []FunctionTypeParameter, arguments []Expr, names []string) ([]Expr, bool) {
	if len(arguments) != len(parameters) || len(names) != len(arguments) {
		return nil, false
	}
	ordered := make([]Expr, len(parameters))
	assigned := make([]bool, len(parameters))
	position := 0
	for index, name := range names {
		if name == "" {
			for position < len(assigned) && assigned[position] {
				position++
			}
			if position >= len(ordered) {
				return nil, false
			}
			ordered[position], assigned[position] = arguments[index], true
			position++

			continue
		}
		parameter := -1
		for candidate, item := range parameters {
			if item.Name == name {
				parameter = candidate

				break
			}
		}
		if parameter < 0 || assigned[parameter] {
			return nil, false
		}
		ordered[parameter], assigned[parameter] = arguments[index], true
	}

	return ordered, !slices.Contains(assigned, false)
}

func staticExprCallArguments(function string, arguments []Expr, names []string) ([]Expr, bool) {
	item, exists := builtinByName(function)
	if !exists || len(names) == 0 {
		return arguments, true
	}
	parameters := item.parameters
	if len(arguments) != len(names) {
		return nil, false
	}
	ordered := make([]Expr, len(parameters))
	assigned := make([]bool, len(parameters))
	position := 0
	for index, name := range names {
		if name == "" {
			for position < len(assigned) && assigned[position] {
				position++
			}
			if position >= len(ordered) {
				return nil, false
			}
			ordered[position], assigned[position] = arguments[index], true
			position++

			continue
		}
		parameter := slices.Index(parameters, name)
		if parameter < 0 || assigned[parameter] {
			return nil, false
		}
		ordered[parameter], assigned[parameter] = arguments[index], true
	}

	return ordered, !slices.Contains(assigned, false)
}

func staticExprStringConstant(expression Expr) (string, bool) {
	switch node := expression.(type) {
	case *StringExpr:
		return node.Value, true
	case *BinaryExpr:
		if node.Operator != "+" {
			return "", false
		}
		left, leftKnown := staticExprStringConstant(node.Left)
		if !leftKnown {
			return "", false
		}
		right, rightKnown := staticExprStringConstant(node.Right)

		return left + right, rightKnown
	default:
		return "", false
	}
}

func hasStaticDuplicateExprMergeKey(list Expr, key string) bool {
	node, ok := list.(*ListExpr)
	if !ok {
		return false
	}
	var knownValues []Value
	for _, item := range node.Items {
		mapNode, ok := item.(*MapExpr)
		if !ok {
			continue
		}
		for _, entry := range mapNode.Entries {
			if entry.Key != key {
				continue
			}
			value, known := staticMergeKeyExprValue(entry.Value)
			if known && slices.ContainsFunc(knownValues, func(existing Value) bool { return equalValues(existing, value) }) {
				return true
			}
			if known {
				knownValues = append(knownValues, value)
			}

			break
		}
	}

	return false
}

func staticMergeKeyExprValue(expression Expr) (Value, bool) {
	switch node := expression.(type) {
	case *NullExpr:
		return Value{Kind: Null}, true
	case *BoolExpr:
		return Value{Kind: Bool, Bool: node.Value}, true
	case *IntegerExpr:
		return Value{Kind: Integer, Text: node.Value}, true
	case *DecimalExpr:
		return Value{Kind: Decimal, Text: node.Value}, true
	case *StringExpr:
		return Value{Kind: String, Text: node.Value}, true
	case *BinaryExpr:
		if node.Operator == "+" {
			if text, known := staticExprStringConstant(expression); known {
				return Value{Kind: String, Text: text}, true
			}
		}
	}

	return Value{}, false
}

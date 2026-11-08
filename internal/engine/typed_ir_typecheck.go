package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

func typedExpressionFromExpr(expression Expr, state *EvaluationState) (typedExpression, bool, error) {
	if state == nil {
		return typedExpression{}, false, errors.New("evaluation state is nil")
	}
	context := typedEvaluationContextFromState(state)
	analysis, known, err := context.inferAnalysis(expression)
	if err != nil {
		return typedExpression{}, false, err
	}

	return typedExpressionFromAnalysis(expression, analysis, known, context)
}

func typedExpressionFromKnownAnalysis(expression Expr, state *EvaluationState, analysis TypeAnalysis, known bool) (typedExpression, bool, error) {
	if state == nil {
		return typedExpression{}, false, errors.New("evaluation state is nil")
	}

	return typedExpressionFromKnownAnalysisWithStaticTypes(expression, state, analysis, known, state.StaticTypes)
}

func typedExpressionFromKnownAnalysisWithStaticTypes(expression Expr, state *EvaluationState, analysis TypeAnalysis, known bool, staticTypes map[string]TypeAnalysis) (typedExpression, bool, error) {
	if state == nil {
		return typedExpression{}, false, errors.New("evaluation state is nil")
	}
	context := typedEvaluationContextFromState(state)
	context.staticTypes = staticTypes
	context.analysis.results[expression] = typedAnalysisResult{analysis: cloneTypeAnalysis(analysis), known: known}

	return typedExpressionFromAnalysis(expression, analysis, known, context)
}

func typedEvaluationContextFromState(state *EvaluationState) typedEvaluationContext {
	return typedEvaluationContext{
		runtimeEnv: state.Environment, frame: state.RuntimeFrame, staticTypes: state.StaticTypes, types: state.Types,
		stack: state.Stack, budget: state.Budget, sourcePath: state.SourcePath, module: state.Module,
		analysis: newTypedAnalysisCache(),
	}
}

func typedExpressionFromAnalysis(expression Expr, analysis TypeAnalysis, known bool, context typedEvaluationContext) (typedExpression, bool, error) {
	inferred := analysis.Type
	if !known {
		if _, isReference := expression.(*ReferenceExpr); !isReference {
			return typedExpression{}, false, nil
		}
		inferred = ResolvedType{Name: typeAny}
	}
	if err := builtinKubernetesStaticExpressionDiagnostic(expression, context); err != nil {
		return typedExpression{}, false, err
	}
	result, supported := typeExpression(expression, inferred, context)
	if supported {
		result.analysisFacts = analysis.Facts
		if lambda, ok := expression.(*LambdaExpr); ok {
			inference := result.analysisFacts.FunctionInference
			if inference == nil {
				inference = &FunctionInference{Body: lambda.Body, Bindings: maps.Clone(context.staticTypes)}
			} else {
				clonedInference := *inference
				clonedInference.Bindings = maps.Clone(inference.Bindings)
				inference = &clonedInference
			}
			if result.operand != nil {
				typedBody := *result.operand
				inference.TypedBody = &typedBody
			}
			result.analysisFacts.FunctionInference = inference
		}
	}

	return result, supported, nil
}

func typeExpression(expression Expr, inferred ResolvedType, contexts ...typedEvaluationContext) (typed typedExpression, supported bool) {
	context := typedContext(contexts)
	context.analysis = context.analysisCache()
	defer func() {
		if supported && typed.resolvedType.Name == "" {
			typed.resolvedType = inferred
		}
		if supported {
			if analysis, known, err := context.inferAnalysis(expression); err == nil && known {
				typed.analysisFacts = analysis.Facts
			}
		}
	}()
	switch node := expression.(type) {
	case *EmptyListExpr, *ListExpr:
		return typePrimitiveListExpression(expression, inferred, context)
	case *EmptyMapExpr, *MapExpr:
		return typePrimitiveMapExpression(expression, inferred, context)
	case *IndexExpr:
		return typeRuntimeIndexAccess(node, inferred, context)
	case *ConditionalExpr:
		return typePrimitiveConditionalExpression(node, inferred, context)
	case *LambdaExpr:
		return typeLambdaExpression(node, inferred, context)
	case *CallExpr:
		return typeCallExpression(node, inferred, context)
	case *BinaryExpr:
		return typeBinaryExpression(node, inferred, context)
	case *OptionalReferenceExpr:
		if node.Path == "" {
			return typedExpression{}, false
		}

		return typedExpression{kind: typedExpressionOptionalReference, span: node.Span, text: node.Path}, true
	case *DeferredReferenceExpr:
		if node.Target == nil {
			return typedExpression{}, false
		}
		arguments := make([]typedExpression, 0, 2)
		for _, argument := range []Expr{node.Target, node.Child} {
			if isNilExpr(argument) {
				continue
			}
			argumentType, known, err := context.inferType(argument)
			if err != nil || !known || argumentType.Name != typeNode {
				return typedExpression{}, false
			}
			child, ok := typeExpression(argument, argumentType, context)
			if !ok {
				return typedExpression{}, false
			}
			arguments = append(arguments, child)
		}

		return typedExpression{kind: typedExpressionDeferredReference, span: node.Span, paths: slices.Clone(node.Path), args: arguments}, true
	case *NodeExpr:
		expectedNode, expectedNodeKnown := resolveStaticAlias(inferred, context.types, make(map[string]bool))
		if expectedNodeKnown && expectedNode.Name == typeNode && len(expectedNode.Arguments) == 1 {
			body, ok := typeExpressionWithInferredExpected(node.Body, expectedNode.Arguments[0], context)
			if !ok {
				return typedExpression{}, false
			}

			return typedExpression{kind: typedExpressionNode, span: node.Span, operand: &body}, true
		}
		bodyType, known, err := context.inferType(node.Body)
		if err != nil || !known {
			return typedExpression{}, false
		}
		body, ok := typeExpression(node.Body, bodyType, context)
		if !ok {
			return typedExpression{}, false
		}

		return typedExpression{kind: typedExpressionNode, span: node.Span, operand: &body}, true
	case *ForExpr:
		return typeForExpression(node, inferred, context)
	case *OverlayExpr:
		return typeOverlayExpression(node, context)
	case *RemovalExpr:
		return typeRemovalExpression(node, context)
	case *ReferenceExpr:
		if node.Name == "" {
			return typedExpression{}, false
		}
		if strings.Contains(node.Name, ".") {
			_, hasNull := removeNullStaticType(inferred)
			if hasNull {
				root, path, _ := strings.Cut(node.Name, ".")

				return typedExpression{kind: typedExpressionOptionalReference, span: node.Span, text: root + "?." + path}, true
			}
		}

		return typedExpression{kind: typedExpressionReference, span: node.Span, text: node.Name}, true
	}
	inferredType, ok := typedScalarTypeFromTypeExpr(inferred)
	if !ok {
		return typedExpression{}, false
	}
	typed = typedExpression{span: expression.ExprSpan(), scalarType: inferredType}
	switch node := expression.(type) {
	case *NullExpr:
		if inferredType.kind != typedScalarNull {
			return typedExpression{}, false
		}
		typed.kind = typedExpressionNull
	case *BoolExpr:
		if inferredType.kind != typedScalarBool {
			return typedExpression{}, false
		}
		typed.kind = typedExpressionBool
		typed.bool = node.Value
	case *IntegerExpr:
		if inferredType.kind != typedScalarInt {
			return typedExpression{}, false
		}
		typed.kind = typedExpressionInteger
		typed.text = node.Value
	case *DecimalExpr:
		if inferredType.kind != typedScalarDecimal {
			return typedExpression{}, false
		}
		typed.kind = typedExpressionDecimal
		typed.text = node.Value
	case *StringExpr:
		if inferredType.kind != typedScalarString || inferred.StringLiteralKnown && inferred.StringLiteral != node.Value {
			return typedExpression{}, false
		}
		typed.kind = typedExpressionString
		typed.text = node.Value
	case *UnaryExpr:
		if inferredType.kind != typedScalarBool || node.Operator != operatorNot {
			return typedExpression{}, false
		}
		operandType, known, err := context.inferType(node.Operand)
		if err != nil || !known {
			return typedExpression{}, false
		}
		if !staticTypeCompatibility(operandType, ResolvedType{Name: typeBool}, context.types).isAssignable() {
			return typedExpression{}, false
		}
		operand, ok := typeExpression(node.Operand, operandType, context)
		if !ok {
			return typedExpression{}, false
		}
		typed.kind = typedExpressionNot
		typed.operand = &operand
	default:
		return typedExpression{}, false
	}

	return typed, true
}

func builtinKubernetesSchemaTypeForNode(body Expr, context typedEvaluationContext) (ResolvedType, bool) {
	if context.module == nil {
		return ResolvedType{}, false
	}
	apiVersion, kind, ok := builtinKubernetesLiteralGVK(body)
	if !ok {
		return ResolvedType{}, false
	}

	return builtinKubernetesSchemaTypeForVersion(context.module.KubernetesVersion, apiVersion, kind)
}

func builtinKubernetesLiteralGVK(body Expr) (string, string, bool) {
	bodyMap, ok := body.(*MapExpr)
	if !ok {
		return "", "", false
	}
	fields := make(map[string]string, 2)
	for _, entry := range bodyMap.Entries {
		if entry.KeyExpr != nil || entry.Key != "apiVersion" && entry.Key != "kind" {
			continue
		}
		value, isString := entry.Value.(*StringExpr)
		if !isString {
			continue
		}
		fields[entry.Key] = value.Value
	}
	apiVersion, hasAPIVersion := fields["apiVersion"]
	kind, hasKind := fields["kind"]
	if !hasAPIVersion || !hasKind {
		return "", "", false
	}

	return apiVersion, kind, true
}

func builtinKubernetesStaticTypeDiagnostic(expression Expr, expected ResolvedType, context typedEvaluationContext, apiVersion, kind string) error {
	return builtinKubernetesStaticTypeDiagnosticAtPath(expression, expected, context, apiVersion, kind, nil)
}

func builtinKubernetesStaticExpressionDiagnostic(expression Expr, context typedEvaluationContext) error {
	var diagnostic error
	WalkExpr(expression, func(expression Expr) {
		if diagnostic != nil {
			return
		}
		node, ok := expression.(*NodeExpr)
		if !ok {
			return
		}
		schemaType, exists := builtinKubernetesSchemaTypeForNode(node.Body, context)
		if !exists {
			return
		}
		apiVersion, kind, _ := builtinKubernetesLiteralGVK(node.Body)
		diagnostic = builtinKubernetesStaticTypeDiagnostic(node.Body, schemaType, context, apiVersion, kind)
	})

	return diagnostic
}

func builtinKubernetesStaticTypeDiagnosticAtPath(expression Expr, expected ResolvedType, context typedEvaluationContext, apiVersion, kind string, path []string) error {
	switch node := expression.(type) {
	case *MapExpr:
		for _, entry := range node.Entries {
			fieldPath := append(slices.Clone(path), entry.Key)
			fieldType, exists := staticMapValueType(expected, entry.Key, entry.KeyExpr == nil, context.types)
			if !exists {
				if entry.KeyExpr == nil {
					return builtinKubernetesStaticSchemaDiagnostic(context, apiVersion, kind, fieldPath, entry.Value.ExprSpan(), "unknown field")
				}

				continue
			}
			if err := builtinKubernetesStaticTypeDiagnosticAtPath(entry.Value, fieldType, context, apiVersion, kind, fieldPath); err != nil {
				return err
			}
		}
	case *ListExpr:
		elementType, isList := staticListElement(expected, context.types)
		if !isList {
			return nil
		}
		for index, item := range node.Items {
			itemPath := append(slices.Clone(path), strconv.Itoa(index))
			if index < len(node.Spreads) && node.Spreads[index] {
				expectedList := ResolvedType{Name: typeList, Element: &elementType}
				if _, isLiteralList := item.(*ListExpr); isLiteralList {
					if err := builtinKubernetesStaticTypeDiagnosticAtPath(item, expectedList, context, apiVersion, kind, itemPath); err != nil {
						return err
					}

					continue
				}
				actual, known, err := context.inferType(item)
				if err != nil {
					return err
				}
				if !known || actual.Name == typeAny {
					continue
				}
				actualElement, isSpreadList := staticListElement(actual, context.types)
				if !isSpreadList {
					return builtinKubernetesStaticSchemaDiagnostic(context, apiVersion, kind, itemPath, item.ExprSpan(), fmt.Sprintf("spread expects List<%s>, got %s", staticTypeName(elementType), staticTypeName(actual)))
				}
				if staticTypeCompatibility(actualElement, elementType, context.types).isDefinitelyIncompatible() {
					return builtinKubernetesStaticSchemaDiagnostic(context, apiVersion, kind, itemPath, item.ExprSpan(), fmt.Sprintf("spread expects List<%s>, got %s", staticTypeName(elementType), staticTypeName(actual)))
				}

				continue
			}
			if err := builtinKubernetesStaticTypeDiagnosticAtPath(item, elementType, context, apiVersion, kind, itemPath); err != nil {
				return err
			}
		}
	case *NodeExpr:
		if expectedNode, known := resolveStaticAlias(expected, context.types, make(map[string]bool)); known && expectedNode.Name == typeNode && len(expectedNode.Arguments) == 1 {
			return builtinKubernetesStaticTypeDiagnosticAtPath(node.Body, expectedNode.Arguments[0], context, apiVersion, kind, path)
		}
	case *StringExpr, *BoolExpr, *IntegerExpr, *DecimalExpr, *NullExpr:
		actual, known, err := context.inferType(expression)
		if err != nil {
			return err
		}
		if !known || actual.Name == typeAny || builtinKubernetesStaticTypeHasDynamicCollection(actual, expected, context.types) {
			return nil
		}
		if staticTypeCompatibility(actual, expected, context.types).isDefinitelyIncompatible() {
			return builtinKubernetesStaticSchemaDiagnostic(context, apiVersion, kind, path, expression.ExprSpan(), fmt.Sprintf("expected %s, got %s", staticTypeName(expected), staticTypeName(actual)))
		}
	default:
		actual, known, err := context.inferType(expression)
		if err != nil {
			return err
		}
		if !known || actual.Name == typeAny || builtinKubernetesStaticTypeHasDynamicCollection(actual, expected, context.types) {
			return nil
		}
		if staticTypeCompatibility(actual, expected, context.types).isDefinitelyIncompatible() {
			return builtinKubernetesStaticSchemaDiagnostic(context, apiVersion, kind, path, expression.ExprSpan(), fmt.Sprintf("expected %s, got %s", staticTypeName(expected), staticTypeName(actual)))
		}
	}

	return nil
}

func builtinKubernetesStaticTypeHasDynamicCollection(actual, expected ResolvedType, types map[string]ResolvedType) bool {
	if !staticTypeContainsAny(actual) {
		return false
	}
	_, actualList := staticListElement(actual, types)
	_, expectedList := staticListElement(expected, types)
	if actualList && expectedList {
		return true
	}

	return isStaticMapType(actual, types) && isStaticMapType(expected, types)
}

func builtinKubernetesStaticSchemaDiagnostic(context typedEvaluationContext, apiVersion, kind string, path []string, span SourceSpan, detail string) error {
	message := fmt.Sprintf("kubernetes resource %s (%s): %s: %s", kind, apiVersion, strings.Join(path, "."), detail)

	return SourceDiagnosticError{Path: context.sourcePath, Code: "MCL-K8S", Message: message, Span: span, Cause: errors.New(detail)}
}

func typeBinaryExpression(node *BinaryExpr, inferred ResolvedType, context typedEvaluationContext) (typedExpression, bool) {
	operator, ok := typedOperatorFromToken(node.Operator)
	if !ok {
		return typedExpression{}, false
	}
	leftType, leftKnown, err := context.inferType(node.Left)
	if err != nil || !leftKnown {
		return typedExpression{}, false
	}
	rightType, rightKnown, err := context.inferType(node.Right)
	if err != nil || !rightKnown {
		return typedExpression{}, false
	}
	if operator == typedOperatorCoalesce {
		leftWithoutNull, hasNull := removeNullStaticType(leftType)
		if hasNull {
			switch node.Right.(type) {
			case *EmptyListExpr:
				if _, isList := staticListElement(leftWithoutNull, context.types); isList {
					rightType = leftWithoutNull
				}
			case *EmptyMapExpr:
				if isStaticMapType(leftWithoutNull, context.types) {
					rightType = leftWithoutNull
				}
			}
		}
	}
	if !validTypedBinaryTypes(operator, inferred, leftType, rightType, context.types) {
		return typedExpression{}, false
	}
	left, leftOK := typeExpression(node.Left, leftType, context)
	right, rightOK := typeExpression(node.Right, rightType, context)
	if !leftOK || !rightOK {
		return typedExpression{}, false
	}

	return typedExpression{kind: typedExpressionBinary, span: node.Span, operator: operator, left: &left, right: &right}, true
}

func typeLambdaExpression(expression Expr, inferred ResolvedType, context typedEvaluationContext) (typedExpression, bool) {
	node, ok := expression.(*LambdaExpr)
	if !ok || node.Body == nil || inferred.Name != typeFunction || len(node.Parameters) != len(inferred.Parameters) {
		return typedExpression{}, false
	}
	bindings := maps.Clone(context.staticTypes)
	if bindings == nil {
		bindings = make(map[string]TypeAnalysis)
	}
	parameterTypes := make([]ResolvedType, len(inferred.Parameters))
	for index, parameter := range node.Parameters {
		parameterType := inferred.Parameters[index].Type
		if !staticTypeIsKnown(parameterType) {
			parameterType = ResolvedType{Name: lambdaTypeVariableName(context.sourcePath, expression.ExprSpan(), index)}
		}
		parameterTypes[index] = parameterType
		bindings[parameter] = TypeAnalysis{Type: parameterType}
	}
	scopedContext := context
	scopedContext.staticTypes = bindings
	scopedContext.analysis = newTypedAnalysisCache()
	bodyType, known, err := scopedContext.inferType(node.Body)
	var body typedExpression
	if err != nil || !known {
		if !containsLambdaTypeVariable(node.Body, bindings) {
			return typedExpression{}, false
		}
		var ok bool
		body, bodyType, ok = typeGenericLambdaBody(node.Body, scopedContext)
		if !ok {
			return typedExpression{}, false
		}
	} else {
		var ok bool
		body, ok = typeExpression(node.Body, bodyType, scopedContext)
		if !ok && containsLambdaTypeVariable(node.Body, bindings) {
			body, bodyType, ok = typeGenericLambdaBody(node.Body, scopedContext)
		}
		if !ok {
			return typedExpression{}, false
		}
	}
	returnType := bodyType
	if inferred.ReturnType != nil && !containsLambdaTypeVariableType(bodyType) {
		if staticTypeCompatibility(bodyType, *inferred.ReturnType, context.types).isDefinitelyIncompatible() {
			return typedExpression{}, false
		}
		returnType = *inferred.ReturnType
	}
	resolvedFunctionType := inferred
	resolvedFunctionType.ReturnType = new(returnType)

	return typedExpression{
		kind: typedExpressionLambda, span: expression.ExprSpan(), resolvedType: resolvedFunctionType, parameters: slices.Clone(node.Parameters),
		paramSpans: slices.Clone(node.ParameterSpans), operand: &body,
		lambdaTyping: &LambdaTyping{
			ParameterTypes: parameterTypes, ReturnType: new(returnType), TypeScope: maps.Clone(context.staticTypes),
			Types: maps.Clone(context.types), InferenceBody: &body,
		},
	}, true
}

const lambdaTypeVariablePrefix = "\x00mcl.lambda:"

func lambdaTypeVariableName(sourcePath string, span SourceSpan, index int) string {
	return fmt.Sprintf("%s%s:%d:%d:%d", lambdaTypeVariablePrefix, sourcePath, span.Start, span.End, index)
}

func isLambdaTypeVariable(typeExpr ResolvedType) bool {
	return strings.HasPrefix(typeExpr.Name, lambdaTypeVariablePrefix)
}

func containsLambdaTypeVariable(expression Expr, bindings map[string]TypeAnalysis) bool {
	for _, name := range expressionReferences(expression) {
		if isLambdaTypeVariable(bindings[name].Type) {
			return true
		}
	}

	return false
}

func typeGenericLambdaBody(expression Expr, context typedEvaluationContext) (typedExpression, ResolvedType, bool) {
	inferred, known, err := context.inferType(expression)
	if err == nil && known && !containsLambdaTypeVariable(expression, context.staticTypes) {
		typed, ok := typeExpression(expression, inferred, context)

		return typed, inferred, ok
	}
	switch node := expression.(type) {
	case *ListExpr:
		items := make([]typedExpression, 0, len(node.Items))
		var elem ResolvedType
		for index, item := range node.Items {
			typedItem, itemType, ok := typeGenericLambdaBody(item, context)
			if !ok {
				return typedExpression{}, ResolvedType{}, false
			}
			if index < len(node.Spreads) && node.Spreads[index] {
				element, isList := staticListElement(itemType, context.types)
				if !isList {
					return typedExpression{}, ResolvedType{}, false
				}
				typedItem.spread = true
				itemType = element
			}
			items = append(items, typedItem)
			elem = joinStaticTypes(elem, itemType)
		}
		if len(items) == 0 {
			elem = ResolvedType{Name: typeAny}
		}
		typ := ResolvedType{Name: typeList, Element: &elem}

		return typedExpression{kind: typedExpressionList, span: node.Span, resolvedType: typ, items: items}, typ, true
	case *EmptyListExpr:
		elem := ResolvedType{Name: typeAny}
		typ := ResolvedType{Name: typeList, Element: &elem}

		return typedExpression{kind: typedExpressionList, span: node.Span, resolvedType: typ}, typ, true
	case *MapExpr:
		entries := make([]typedMapEntry, 0, len(node.Entries))
		fields := make([]TypeField, 0, len(node.Entries))
		for _, entry := range node.Entries {
			if entry.KeyExpr != nil {
				return typedExpression{}, ResolvedType{}, false
			}
			value, valueType, ok := typeGenericLambdaBody(entry.Value, context)
			if !ok {
				return typedExpression{}, ResolvedType{}, false
			}
			entries = append(entries, typedMapEntry{key: entry.Key, keySpan: entry.KeySpan, value: value})
			fields = append(fields, TypeField{Name: entry.Key, NameSpan: entry.KeySpan, Type: valueType})
		}
		typ := ResolvedType{Fields: fields}

		return typedExpression{kind: typedExpressionMap, span: node.Span, resolvedType: typ, entries: entries}, typ, true
	case *UnaryExpr:
		if node.Operator != operatorNot {
			return typedExpression{}, ResolvedType{}, false
		}
		operand, _, ok := typeGenericLambdaBody(node.Operand, context)
		if !ok || !containsLambdaTypeVariable(node.Operand, context.staticTypes) {
			return typedExpression{}, ResolvedType{}, false
		}
		typ := ResolvedType{Name: typeBool}

		return typedExpression{kind: typedExpressionNot, span: node.Span, resolvedType: typ, operand: &operand}, typ, true
	case *BinaryExpr:
		operator, ok := typedOperatorFromToken(node.Operator)
		if !ok {
			return typedExpression{}, ResolvedType{}, false
		}
		left, leftType, leftOK := typeGenericLambdaBody(node.Left, context)
		right, rightType, rightOK := typeGenericLambdaBody(node.Right, context)
		if !leftOK || !rightOK || !containsLambdaTypeVariable(expression, context.staticTypes) {
			return typedExpression{}, ResolvedType{}, false
		}
		typ := ResolvedType{Name: typeAny}
		if isBooleanResultOperator(operator) {
			typ = ResolvedType{Name: typeBool}
		} else if operator == typedOperatorCoalesce {
			typ = joinStaticTypes(leftType, rightType)
		} else if isLambdaTypeVariable(leftType) {
			typ = leftType
		} else if isLambdaTypeVariable(rightType) {
			typ = rightType
		}

		return typedExpression{kind: typedExpressionBinary, span: node.Span, resolvedType: typ, operator: operator, left: &left, right: &right}, typ, true
	case *ConditionalExpr:
		condition, _, conditionOK := typeGenericLambdaBody(node.Condition, context)
		thenBranch, thenType, thenOK := typeGenericLambdaBody(node.Then, context)
		elseBranch, elseType, elseOK := typeGenericLambdaBody(node.Else, context)
		if !conditionOK || !thenOK || !elseOK || !containsLambdaTypeVariable(expression, context.staticTypes) {
			return typedExpression{}, ResolvedType{}, false
		}
		typ := joinStaticTypes(thenType, elseType)

		return typedExpression{kind: typedExpressionConditional, span: node.Span, resolvedType: typ, condition: &condition, thenBranch: &thenBranch, elseBranch: &elseBranch}, typ, true
	case *IndexExpr:
		base, _, baseOK := typeGenericLambdaBody(node.Base, context)
		key, _, keyOK := typeGenericLambdaBody(node.Key, context)
		if !baseOK || !keyOK || !containsLambdaTypeVariable(expression, context.staticTypes) {
			return typedExpression{}, ResolvedType{}, false
		}
		typ := ResolvedType{Name: typeAny}

		return typedExpression{kind: typedExpressionIndexAccess, span: node.Span, resolvedType: typ, left: &base, right: &key}, typ, true
	case *ReferenceExpr:
		root, _, _ := strings.Cut(node.Name, ".")
		variable := context.staticTypes[root].Type
		if node.Name == "" || !isLambdaTypeVariable(variable) {
			return typedExpression{}, ResolvedType{}, false
		}

		return typedExpression{kind: typedExpressionReference, span: node.Span, resolvedType: variable, text: node.Name, genericVariable: variable.Name}, variable, true
	default:
		return typedExpression{}, ResolvedType{}, false
	}
}

func typeCallExpression(expression Expr, inferred ResolvedType, context typedEvaluationContext) (typedExpression, bool) {
	node, ok := expression.(*CallExpr)
	if !ok || node.Callee == nil || len(node.Arguments) == 0 && node.ArgumentNames != nil || len(node.ArgumentNames) != 0 && len(node.ArgumentNames) != len(node.Arguments) {
		return typedExpression{}, false
	}
	if reference, isReference := node.Callee.(*ReferenceExpr); isReference {
		if function, found := staticFunctionName(reference.Name, context.staticTypes, context.types); found {
			if item, builtinFunction := builtinByName(function); builtinFunction {
				return typeBuiltinCallExpression(expression, inferred, item, context)
			}
		}
	}
	calleeType, known, err := context.inferType(node.Callee)
	if err != nil || !known {
		return typedExpression{}, false
	}
	calleeType, known = resolveStaticAlias(calleeType, context.types, make(map[string]bool))
	if !known {
		return typedExpression{}, false
	}
	calleeOptions := calleeType.Options
	if len(calleeOptions) == 0 {
		calleeOptions = []ResolvedType{calleeType}
	}
	var functionType ResolvedType
	resolvedOptions := make([]ResolvedType, len(calleeOptions))
	for index, option := range calleeOptions {
		option, optionKnown := resolveStaticAlias(option, context.types, make(map[string]bool))
		if !optionKnown || option.Name != typeFunction {
			return typedExpression{}, false
		}
		resolvedOptions[index] = option
		if index == 0 {
			functionType = option
		}
	}
	resolvedCalleeType := calleeType
	callee, ok := typeExpression(node.Callee, resolvedCalleeType, context)
	if !ok {
		return typedExpression{}, false
	}
	names := slices.Clone(node.ArgumentNames)
	if len(names) == 0 {
		names = make([]string, len(node.Arguments))
	}
	argumentParameters := make([][]int, len(resolvedOptions))
	for optionIndex, option := range resolvedOptions {
		argumentParameters[optionIndex], ok = functionArgumentParameterIndices(option.Parameters, len(node.Arguments), names)
		if !ok {
			return typedExpression{}, false
		}
	}
	arguments := make([]typedExpression, len(node.Arguments))
	typeSubstitutions := make([]map[string]ResolvedType, len(resolvedOptions))
	for index := range typeSubstitutions {
		typeSubstitutions[index] = make(map[string]ResolvedType)
	}
	for index, argument := range node.Arguments {
		argumentType, argumentKnown, argumentErr := context.inferType(argument)
		if argumentErr != nil {
			return typedExpression{}, false
		}
		if !argumentKnown || func() bool {
			_, ok := argument.(*LambdaExpr)

			return ok
		}() {
			firstParameterIndex := argumentParameters[0][index]
			firstParameter := functionType.Parameters[firstParameterIndex].Type
			for optionIndex, option := range resolvedOptions[1:] {
				parameterIndex := argumentParameters[optionIndex+1][index]
				if !equalStaticTypes(firstParameter, option.Parameters[parameterIndex].Type) {
					return typedExpression{}, false
				}
			}
			argumentType, argumentKnown, argumentErr = inferStaticExprCallArgument(argument, firstParameter, context.staticTypes, context.types)
			if argumentErr != nil || !argumentKnown {
				return typedExpression{}, false
			}
		}
		for optionIndex, option := range resolvedOptions {
			parameterIndex := argumentParameters[optionIndex][index]
			parameterType := option.Parameters[parameterIndex].Type
			if staticTypeCompatibility(argumentType, parameterType, context.types).isDefinitelyIncompatible() {
				return typedExpression{}, false
			}
			if isLambdaTypeVariable(parameterType) {
				typeSubstitutions[optionIndex][parameterType.Name] = argumentType
			}
		}
		arguments[index], ok = typeExpression(argument, argumentType, context)
		if !ok {
			return typedExpression{}, false
		}
	}
	for index, option := range resolvedOptions {
		if option.ReturnType == nil {
			continue
		}
		returnType := substituteTypeParameters(*option.ReturnType, typeSubstitutions[index])
		if !staticTypeCompatibility(returnType, inferred, context.types).isAssignable() {
			return typedExpression{}, false
		}
	}

	return typedExpression{
		kind: typedExpressionCall, span: expression.ExprSpan(), callee: &callee, args: arguments,
		argNames: slices.Clone(node.ArgumentNames),
	}, true
}

func typeBuiltinCallExpression(expression Expr, inferred ResolvedType, item builtin, context typedEvaluationContext) (typedExpression, bool) {
	node, ok := expression.(*CallExpr)
	if !ok {
		return typedExpression{}, false
	}
	names := slices.Clone(node.ArgumentNames)
	if len(names) == 0 {
		names = make([]string, len(node.Arguments))
	}
	_, ok = staticExprFunctionCallArguments(builtinParameters(item), node.Arguments, names)
	if !ok {
		return typedExpression{}, false
	}
	calleeType := ResolvedType{Name: typeFunction}
	callee, ok := typeExpression(node.Callee, calleeType, context)
	if !ok {
		return typedExpression{}, false
	}
	arguments := make([]typedExpression, len(node.Arguments))
	for index, argument := range node.Arguments {
		argumentType, known, err := context.inferType(argument)
		if err != nil || !known {
			return typedExpression{}, false
		}
		arguments[index], ok = typeExpression(argument, argumentType, context)
		if !ok {
			return typedExpression{}, false
		}
	}

	return typedExpression{
		kind: typedExpressionCall, span: expression.ExprSpan(), resolvedType: inferred, builtinID: builtinID(item.name),
		callee: &callee, args: arguments, argNames: slices.Clone(node.ArgumentNames),
	}, true
}

func builtinParameters(item builtin) []FunctionTypeParameter {
	parameters := make([]FunctionTypeParameter, len(item.parameters))
	for index, name := range item.parameters {
		parameters[index] = FunctionTypeParameter{Name: name}
	}

	return parameters
}

func typeForExpression(expression Expr, inferred ResolvedType, context typedEvaluationContext) (typedExpression, bool) {
	node, ok := expression.(*ForExpr)
	if !ok || node.Iterable == nil || node.Body == nil || node.Variable == "" {
		return typedExpression{}, false
	}
	sourceType, known, err := context.inferType(node.Iterable)
	if err != nil || !known {
		return typedExpression{}, false
	}
	elementType, isList := staticListElement(sourceType, context.types)
	if !isList || inferred.Name != typeList || inferred.Element == nil {
		return typedExpression{}, false
	}
	bindings := maps.Clone(context.staticTypes)
	if bindings == nil {
		bindings = make(map[string]TypeAnalysis)
	}
	bindings[node.Variable] = TypeAnalysis{Type: elementType}
	body, ok := typeExpression(node.Body, *inferred.Element, typedEvaluationContext{
		runtimeEnv: context.runtimeEnv, staticTypes: bindings, types: context.types,
		stack: context.stack, budget: context.budget, sourcePath: context.sourcePath, module: context.module,
	})
	if !ok {
		return typedExpression{}, false
	}
	source, ok := typeExpression(node.Iterable, sourceType, context)
	if !ok {
		return typedExpression{}, false
	}

	return typedExpression{kind: typedExpressionFor, span: expression.ExprSpan(), left: &source, thenBranch: &body, loopName: node.Variable}, true
}

func typeOverlayExpression(expression Expr, context typedEvaluationContext) (typedExpression, bool) {
	node, ok := expression.(*OverlayExpr)
	if !ok {
		return typedExpression{}, false
	}
	leftType, leftKnown, err := context.inferType(node.Base)
	if err != nil || !leftKnown {
		return typedExpression{}, false
	}
	rightType, rightKnown, err := context.inferType(node.Patch)
	if err != nil || !rightKnown {
		return typedExpression{}, false
	}
	left, ok := typeExpression(node.Base, leftType, context)
	if !ok {
		return typedExpression{}, false
	}
	right, ok := typeExpression(node.Patch, rightType, context)
	if !ok {
		return typedExpression{}, false
	}

	return typedExpression{kind: typedExpressionOverlay, span: expression.ExprSpan(), left: &left, right: &right}, true
}

func typeRemovalExpression(expression Expr, context typedEvaluationContext) (typedExpression, bool) {
	node, ok := expression.(*RemovalExpr)
	if !ok || len(node.Paths) == 0 {
		return typedExpression{}, false
	}
	baseType, known, err := context.inferType(node.Base)
	if err != nil || !known {
		return typedExpression{}, false
	}
	base, ok := typeExpression(node.Base, baseType, context)
	if !ok {
		return typedExpression{}, false
	}

	return typedExpression{kind: typedExpressionRemoval, span: expression.ExprSpan(), operand: &base, paths: slices.Clone(node.Paths)}, true
}

func typePrimitiveConditionalExpression(expression Expr, inferred ResolvedType, context typedEvaluationContext) (typedExpression, bool) {
	node, ok := expression.(*ConditionalExpr)
	if !ok {
		return typedExpression{}, false
	}
	condition, ok := typeBooleanSubexpression(node.Condition, context)
	if !ok {
		return typedExpression{}, false
	}
	thenBindings := narrowStaticBindings(node.Condition, context.staticTypes, context.types, true)
	elseBindings := narrowStaticBindings(node.Condition, context.staticTypes, context.types, false)
	thenContext := context
	thenContext.staticTypes = thenBindings
	thenContext.analysis = newTypedAnalysisCache()
	thenType, thenKnown, thenErr := thenContext.inferType(node.Then)
	elseContext := context
	elseContext.staticTypes = elseBindings
	elseContext.analysis = newTypedAnalysisCache()
	elseType, elseKnown, elseErr := elseContext.inferType(node.Else)
	if thenErr != nil || elseErr != nil || !thenKnown || !elseKnown {
		return typedExpression{}, false
	}
	branchTypes := []ResolvedType{thenType, elseType}
	if literal, isLiteral := node.Condition.(*BoolExpr); isLiteral {
		activeBranch := 1
		if literal.Value {
			activeBranch = 0
		}
		activeType := branchTypes[activeBranch]
		if isPrimitiveScalarType(activeType) && equalStaticTypes(activeType, inferred) {
			branchTypes = []ResolvedType{activeType}
		}
	}
	for index, branchType := range branchTypes {
		branch := node.Then
		if index == 1 {
			branch = node.Else
		}
		if containsEmptyCollectionLiteral(branch) {
			if !staticExpressionAssignableWithEmptyCollections(branch, branchType, inferred, context) {
				return typedExpression{}, false
			}

			continue
		}
		if !staticTypeCompatibility(branchType, inferred, context.types).isAssignable() {
			return typedExpression{}, false
		}
	}
	thenBranch, ok := typeExpression(node.Then, thenType, thenContext)
	if containsEmptyCollectionLiteral(node.Then) {
		thenBranch, ok = typeExpressionWithExpectedCollections(node.Then, thenType, inferred, thenContext)
	}
	if !ok {
		return typedExpression{}, false
	}
	elseBranch, ok := typeExpression(node.Else, elseType, elseContext)
	if containsEmptyCollectionLiteral(node.Else) {
		elseBranch, ok = typeExpressionWithExpectedCollections(node.Else, elseType, inferred, elseContext)
	}
	if !ok {
		return typedExpression{}, false
	}

	return typedExpression{
		kind:       typedExpressionConditional,
		span:       expression.ExprSpan(),
		condition:  &condition,
		thenBranch: &thenBranch,
		elseBranch: &elseBranch,
	}, true
}

func isPrimitiveScalarType(resolved ResolvedType) bool {
	switch resolved.Name {
	case typeBool, typeDecimal, typeInt, typeString:
		return len(resolved.Options) == 0 && len(resolved.Fields) == 0 && len(resolved.Parameters) == 0
	default:
		return false
	}
}

package engine

import "slices"

func typePrimitiveListExpression(expression Expr, inferred ResolvedType, contexts ...typedEvaluationContext) (typedExpression, bool) {
	context := typedContext(contexts)
	if _, ok := expression.(*EmptyListExpr); ok {
		if _, isList := staticListElement(inferred, context.types); !isList {
			return typedExpression{}, false
		}

		return typedExpression{kind: typedExpressionList, span: expression.ExprSpan(), resolvedType: inferred}, true
	}
	node, ok := expression.(*ListExpr)
	if !ok {
		return typedExpression{}, false
	}
	elementType, isList := staticListElement(inferred, context.types)
	if !isList {
		return typedExpression{}, false
	}
	typed := typedExpression{kind: typedExpressionList, span: node.Span, items: make([]typedExpression, 0, len(node.Items))}
	for _, item := range node.Items {
		itemType, known, err := context.inferType(item)
		if err != nil {
			return typedExpression{}, false
		}
		if !known {
			typedItem, ok := typeExpressionWithInferredExpected(item, elementType, context)
			if !ok {
				return typedExpression{}, false
			}
			typed.items = append(typed.items, typedItem)
			continue
		}
		if !typedExpressionAssignable(item, itemType, elementType, context) {
			return typedExpression{}, false
		}
		typedItem, ok := typeExpressionWithExpectedCollections(item, itemType, elementType, context)
		if !ok {
			return typedExpression{}, false
		}
		typed.items = append(typed.items, typedItem)
	}

	return typed, true
}

func typePrimitiveMapExpression(expression Expr, inferred ResolvedType, contexts ...typedEvaluationContext) (typedExpression, bool) {
	context := typedContext(contexts)
	node, ok := expression.(*MapExpr)
	if !ok {
		if _, empty := expression.(*EmptyMapExpr); empty && isStaticMapType(inferred, context.types) {
			return typedExpression{kind: typedExpressionMap, span: expression.ExprSpan()}, true
		}

		return typedExpression{}, false
	}
	if !isStaticMapType(inferred, context.types) {
		return typedExpression{}, false
	}
	typed := typedExpression{kind: typedExpressionMap, span: node.Span, entries: make([]typedMapEntry, 0, len(node.Entries))}
	seen := make(map[string]struct{}, len(node.Entries))
	var dynamicValueType *ResolvedType
	for _, entry := range node.Entries {
		key := entry.Key
		var keyExpression *typedExpression
		if entry.KeyExpr != nil {
			keyType, known, err := context.inferType(entry.KeyExpr)
			if err != nil || !known || !staticIndexKeyIs(keyType, typeString, context.types) {
				return typedExpression{}, false
			}
			child, ok := typeExpression(entry.KeyExpr, keyType, context)
			if !ok {
				return typedExpression{}, false
			}
			keyExpression = &child
		} else if _, exists := seen[key]; exists {
			return typedExpression{}, false
		} else {
			seen[key] = struct{}{}
		}
		fieldType, exists := staticMapValueType(inferred, key, keyExpression == nil, context.types)
		if !exists {
			return typedExpression{}, false
		}
		valueType, known, err := context.inferType(entry.Value)
		if err != nil {
			return typedExpression{}, false
		}
		if !known {
			if keyExpression != nil {
				if dynamicValueType != nil && !equalStaticTypes(*dynamicValueType, fieldType) {
					return typedExpression{}, false
				}
				if dynamicValueType == nil {
					dynamicValueType = new(fieldType)
				}
			}
			value, ok := typeExpressionWithInferredExpected(entry.Value, fieldType, context)
			if !ok {
				return typedExpression{}, false
			}
			typed.entries = append(typed.entries, typedMapEntry{key: key, keySpan: entry.KeySpan, keyExpression: keyExpression, value: value})
			continue
		}
		if keyExpression != nil {
			if dynamicValueType != nil && !equalStaticTypes(*dynamicValueType, valueType) {
				return typedExpression{}, false
			}
			if dynamicValueType == nil {
				dynamicValueType = new(valueType)
			}
		}
		if !typedExpressionAssignable(entry.Value, valueType, fieldType, context) {
			return typedExpression{}, false
		}
		value, ok := typeExpressionWithExpectedCollections(entry.Value, valueType, fieldType, context)
		if !ok {
			return typedExpression{}, false
		}
		typed.entries = append(typed.entries, typedMapEntry{key: key, keySpan: entry.KeySpan, keyExpression: keyExpression, value: value})
	}

	return typed, true
}

func typeExpressionWithInferredExpected(expression Expr, expected ResolvedType, context typedEvaluationContext) (typedExpression, bool) {
	inferred, known, err := inferStaticExprTypeWithExpected(expression, expected, context.staticTypes, context.types)
	if err != nil || !known {
		return typedExpression{}, false
	}
	actual, actualKnown, err := context.inferType(expression)
	if err != nil {
		return typedExpression{}, false
	}
	if actualKnown && !typedExpressionAssignable(expression, actual, expected, context) {
		return typedExpression{}, false
	}

	return typeExpression(expression, inferred, context)
}

func typeExpressionWithExpectedCollections(expression Expr, actual, expected ResolvedType, context typedEvaluationContext) (typedExpression, bool) {
	inferred, known, err := inferStaticExprTypeWithExpected(expression, expected, context.staticTypes, context.types)
	if err != nil || !known || !typedExpressionAssignable(expression, actual, expected, context) {
		return typedExpression{}, false
	}

	return typeExpression(expression, inferred, context)
}

func typedExpressionAssignable(expression Expr, actual, expected ResolvedType, context typedEvaluationContext) bool {
	compatibility := staticTypeCompatibility(actual, expected, context.types)
	if compatibility.isAssignable() {
		return true
	}
	if containsEmptyCollectionLiteral(expression) {
		return staticExpressionAssignableWithEmptyCollections(expression, actual, expected, context)
	}
	switch expression.(type) {
	case *ListExpr, *MapExpr, *NodeExpr, *ConditionalExpr:
		return true
	case *ReferenceExpr:
		return !compatibility.isDefinitelyIncompatible()
	default:
		return compatibility.isAssignable()
	}
}

func staticExpressionAssignableWithEmptyCollections(expression Expr, actual, expected ResolvedType, context typedEvaluationContext) bool {
	if staticTypeCompatibility(actual, expected, context.types).isAssignable() {
		return true
	}
	if !containsEmptyCollectionLiteral(expression) {
		return false
	}

	switch node := expression.(type) {
	case *EmptyListExpr:
		_, isList := staticListElement(expected, context.types)

		return isList
	case *EmptyMapExpr:
		return isStaticMapType(expected, context.types)
	case *ListExpr:
		elementType, isList := staticListElement(expected, context.types)
		if !isList {
			return false
		}
		for _, item := range node.Items {
			itemType, known, err := context.inferType(item)
			if err != nil || !known || !staticExpressionAssignableWithEmptyCollections(item, itemType, elementType, context) {
				return false
			}
		}

		return true
	case *MapExpr:
		if !isStaticMapType(expected, context.types) {
			return false
		}
		for _, entry := range node.Entries {
			valueType, exists := staticMapValueType(expected, entry.Key, entry.KeyExpr == nil, context.types)
			if !exists {
				return false
			}
			actualValueType, known, err := context.inferType(entry.Value)
			if err != nil || !known || !staticExpressionAssignableWithEmptyCollections(entry.Value, actualValueType, valueType, context) {
				return false
			}
		}
		resolvedExpected, known := resolveStaticAlias(expected, context.types, make(map[string]bool))
		if !known || resolvedExpected.Name == typeMap || resolvedExpected.OpenRecord {
			return true
		}
		for _, field := range resolvedExpected.Fields {
			if field.Optional {
				continue
			}
			found := false
			for _, entry := range node.Entries {
				if entry.KeyExpr == nil && entry.Key == field.Name {
					found = true

					break
				}
			}
			if !found {
				return false
			}
		}

		return true
	case *ConditionalExpr:
		return staticExpressionAssignableWithEmptyCollections(node.Then, actual, expected, context) && staticExpressionAssignableWithEmptyCollections(node.Else, actual, expected, context)
	default:
		return staticTypeCompatibility(actual, expected, context.types).isAssignable()
	}
}

func containsEmptyCollectionLiteral(expression Expr) bool {
	switch node := expression.(type) {
	case *EmptyListExpr, *EmptyMapExpr:
		return true
	case *ListExpr:
		return slices.ContainsFunc(node.Items, containsEmptyCollectionLiteral)
	case *MapExpr:
		for _, entry := range node.Entries {
			if containsEmptyCollectionLiteral(entry.Value) {
				return true
			}
		}
	case *ConditionalExpr:
		return containsEmptyCollectionLiteral(node.Then) || containsEmptyCollectionLiteral(node.Else)
	case *NodeExpr:
		return containsEmptyCollectionLiteral(node.Body)
	}

	return false
}

func isStaticMapType(typeExpr ResolvedType, types map[string]ResolvedType) bool {
	resolved, known := resolveStaticAlias(typeExpr, types, make(map[string]bool))
	if !known {
		return false
	}
	if resolved.Name == typeNode && len(resolved.Arguments) == 1 {
		return isStaticMapType(resolved.Arguments[0], types)
	}

	return resolved.Name == typeMap || resolved.Fields != nil || resolved.OpenRecord
}

func staticMapValueType(typeExpr ResolvedType, key string, hasConstantKey bool, types map[string]ResolvedType) (ResolvedType, bool) {
	resolved, known := resolveStaticAlias(typeExpr, types, make(map[string]bool))
	if !known {
		return ResolvedType{}, false
	}
	if resolved.Name == typeNode && len(resolved.Arguments) == 1 {
		return staticMapValueType(resolved.Arguments[0], key, hasConstantKey, types)
	}
	if resolved.Fields != nil || resolved.OpenRecord {
		if hasConstantKey {
			for _, field := range resolved.Fields {
				if field.Name == key {
					return field.Type, true
				}
			}
		}
		if resolved.Element != nil {
			return *resolved.Element, true
		}
		if resolved.AdditionalProperties != nil {
			return *resolved.AdditionalProperties, true
		}

		return ResolvedType{}, false
	}
	if resolved.Name == typeMap && resolved.Element != nil {
		return *resolved.Element, true
	}

	return ResolvedType{}, false
}

func typeRuntimeIndexAccess(expression Expr, inferred ResolvedType, context typedEvaluationContext) (typedExpression, bool) {
	node, ok := expression.(*IndexExpr)
	if !ok {
		return typedExpression{}, false
	}
	baseType, baseKnown, err := context.inferType(node.Base)
	if err != nil || !baseKnown {
		return typedExpression{}, false
	}
	keyType, keyKnown, err := context.inferType(node.Key)
	if err != nil || !keyKnown {
		return typedExpression{}, false
	}
	resolvedBaseType, baseResolvable := resolveStaticAlias(baseType, context.types, make(map[string]bool))
	resolvedKeyType, keyResolvable := resolveStaticAlias(keyType, context.types, make(map[string]bool))
	if !baseResolvable || !keyResolvable {
		return typedExpression{}, false
	}
	keyAnalysis, keyAnalysisKnown, err := context.inferAnalysis(node.Key)
	if err != nil {
		return typedExpression{}, false
	}
	indexType, indexKnown, err := staticIndexExprTypeWithConstant(resolvedBaseType, resolvedKeyType, context.types, keyAnalysis.Facts.ConstantString, keyAnalysisKnown && keyAnalysis.Facts.ConstantStringKnown)
	if err != nil || !indexKnown {
		return typedExpression{}, false
	}
	if !staticTypeCompatibility(indexType, inferred, context.types).isAssignable() {
		return typedExpression{}, false
	}
	base, ok := typeExpression(node.Base, baseType, context)
	if !ok {
		return typedExpression{}, false
	}
	key, ok := typeExpression(node.Key, keyType, context)
	if !ok {
		return typedExpression{}, false
	}

	return typedExpression{kind: typedExpressionIndexAccess, span: node.Span, left: &base, right: &key}, true
}

func typeBooleanSubexpression(expression Expr, contexts ...typedEvaluationContext) (typedExpression, bool) {
	context := typedContext(contexts)
	inferred, known, err := context.inferType(expression)
	if err != nil || !known {
		return typedExpression{}, false
	}
	if !staticTypeCompatibility(inferred, ResolvedType{Name: typeBool}, context.types).isAssignable() {
		return typedExpression{}, false
	}

	return typeExpression(expression, inferred, context)
}

func typedContext(contexts []typedEvaluationContext) typedEvaluationContext {
	if len(contexts) == 0 {
		return typedEvaluationContext{}
	}

	return contexts[0]
}

func typedScalarTypeFromTypeExpr(typeExpr ResolvedType) (typedScalarType, bool) {
	if len(typeExpr.TypeParameters) != 0 || len(typeExpr.Arguments) != 0 || typeExpr.SchemaPath != "" || typeExpr.Element != nil || len(typeExpr.Options) != 0 || len(typeExpr.Fields) != 0 || typeExpr.OpenRecord || typeExpr.AdditionalProperties != nil || typeExpr.Optional || len(typeExpr.Parameters) != 0 || typeExpr.ReturnType != nil {
		return typedScalarType{}, false
	}
	var kind typedScalarKind
	switch typeExpr.Name {
	case typeNull:
		kind = typedScalarNull
	case typeBool:
		kind = typedScalarBool
	case typeInt:
		kind = typedScalarInt
	case typeDecimal:
		kind = typedScalarDecimal
	case typeString:
		kind = typedScalarString
	default:
		return typedScalarType{}, false
	}
	if kind != typedScalarString && (typeExpr.StringLiteral != "" || typeExpr.StringLiteralKnown) {
		return typedScalarType{}, false
	}

	return typedScalarType{kind: kind, constantString: typeExpr.StringLiteral, constantStringKnown: typeExpr.StringLiteralKnown}, true
}

package engine

func typePrimitiveListExpression(expression Expr, inferred ResolvedType, contexts ...typedEvaluationContext) (typedExpression, bool) {
	context := typedContext(contexts)
	if empty, ok := expression.(*EmptyListExpr); ok {
		_ = empty
		if !hasTypedEmptyListType(inferred) {
			return typedExpression{}, false
		}

		return typedExpression{kind: typedExpressionList, span: expression.ExprSpan()}, true
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
		if err != nil || !known {
			return typedExpression{}, false
		}
		matches, compatible := staticTypeAssignable(itemType, elementType, context.types)
		if !compatible || !matches {
			return typedExpression{}, false
		}
		typedItem, ok := typeExpression(item, itemType, context)
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
		if err != nil || !known {
			return typedExpression{}, false
		}
		if keyExpression != nil {
			if dynamicValueType != nil && !equalStaticTypes(*dynamicValueType, valueType) {
				return typedExpression{}, false
			}
			if dynamicValueType == nil {
				dynamicValueType = new(valueType)
			}
		}
		matches, compatible := staticTypeAssignable(valueType, fieldType, context.types)
		if !compatible || !matches {
			return typedExpression{}, false
		}
		value, ok := typeExpression(entry.Value, valueType, context)
		if !ok {
			return typedExpression{}, false
		}
		typed.entries = append(typed.entries, typedMapEntry{key: key, keySpan: entry.KeySpan, keyExpression: keyExpression, value: value})
	}

	return typed, true
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
	matches, compatible := staticTypeAssignable(indexType, inferred, context.types)
	if !compatible || !matches {
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

func hasTypedEmptyListType(typeExpr ResolvedType) bool {
	return hasTypedPrimitiveListType(typeExpr) && isBareAnyTypeExpr(*typeExpr.Element)
}

func isBareAnyTypeExpr(typeExpr ResolvedType) bool {
	return typeExpr.Span == (SourceSpan{}) && typeExpr.Name == typeAny && typeExpr.StringLiteral == "" && !typeExpr.StringLiteralKnown && typeExpr.TypeParameters == nil && typeExpr.Arguments == nil && typeExpr.SchemaPath == "" && typeExpr.SchemaID == "" && typeExpr.ScopeID == "" && typeExpr.Element == nil && typeExpr.Options == nil && typeExpr.Fields == nil && !typeExpr.OpenRecord && typeExpr.AdditionalProperties == nil && !typeExpr.Optional && typeExpr.Parameters == nil && typeExpr.ReturnType == nil
}

func hasTypedPrimitiveListType(typeExpr ResolvedType) bool {
	return typeExpr.Span == (SourceSpan{}) && typeExpr.Name == typeList && typeExpr.StringLiteral == "" && !typeExpr.StringLiteralKnown && typeExpr.TypeParameters == nil && typeExpr.Arguments == nil && typeExpr.SchemaPath == "" && typeExpr.SchemaID == "" && typeExpr.ScopeID == "" && typeExpr.Element != nil && typeExpr.Options == nil && typeExpr.Fields == nil && !typeExpr.OpenRecord && typeExpr.AdditionalProperties == nil && !typeExpr.Optional && typeExpr.Parameters == nil && typeExpr.ReturnType == nil
}

func typeBooleanSubexpression(expression Expr, contexts ...typedEvaluationContext) (typedExpression, bool) {
	context := typedContext(contexts)
	inferred, known, err := context.inferType(expression)
	if err != nil || !known {
		return typedExpression{}, false
	}
	matches, compatible := staticTypeAssignable(inferred, ResolvedType{Name: typeBool}, context.types)
	if !compatible || !matches {
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

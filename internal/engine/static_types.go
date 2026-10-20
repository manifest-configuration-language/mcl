package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func staticIndexKeyIs(key ResolvedType, expected string, types map[string]ResolvedType) bool {
	key, known := resolveStaticAlias(key, types, make(map[string]bool))

	return known && (key.Name == expected || key.Name == typeAny)
}

func mergeStaticTypes(base, patch ResolvedType, types map[string]ResolvedType) (ResolvedType, bool, error) {
	resolvedBase, baseKnown := resolveStaticAlias(base, types, make(map[string]bool))
	resolvedPatch, patchKnown := resolveStaticAlias(patch, types, make(map[string]bool))
	if !baseKnown || !patchKnown {
		return ResolvedType{}, false, nil
	}
	if resolvedPatch.Name == typeNull {
		matches, known := staticTypeAssignable(resolvedPatch, base, types)
		if known && matches {
			return base, true, nil
		}

		return resolvedPatch, true, nil
	}
	if resolvedBase.Name == typeNull {
		return ResolvedType{}, false, fmt.Errorf("cannot replace Null with %s using with", staticTypeName(resolvedPatch))
	}
	if resolvedBase.Name == typeAny || resolvedPatch.Name == typeAny {
		return ResolvedType{}, false, nil
	}
	baseIsMap := resolvedBase.Name == typeMap || resolvedBase.Fields != nil || resolvedBase.OpenRecord
	patchIsMap := resolvedPatch.Name == typeMap || resolvedPatch.Fields != nil || resolvedPatch.OpenRecord
	if baseIsMap && patchIsMap && (resolvedBase.Fields == nil || resolvedPatch.Fields == nil) {
		if resolvedBase.Name == typeMap && resolvedBase.Element != nil && resolvedPatch.Fields != nil {
			for _, field := range resolvedPatch.Fields {
				matches, compatible := staticTypeAssignable(field.Type, *resolvedBase.Element, types)
				if compatible && !matches {
					return ResolvedType{}, false, fmt.Errorf("field %q cannot be merged into %s", field.Name, staticTypeName(resolvedBase))
				}
				if !compatible {
					return ResolvedType{}, false, nil
				}
			}
			if resolvedPatch.OpenRecord {
				additional := ResolvedType{Name: typeAny}
				if resolvedPatch.AdditionalProperties != nil {
					additional = *resolvedPatch.AdditionalProperties
				}
				matches, compatible := staticTypeAssignable(additional, *resolvedBase.Element, types)
				if compatible && !matches {
					return ResolvedType{}, false, fmt.Errorf("additional properties cannot be merged into %s", staticTypeName(resolvedBase))
				}
				if !compatible {
					return ResolvedType{}, false, nil
				}
			}

			additional := *resolvedBase.Element

			return ResolvedType{Fields: slices.Clone(resolvedPatch.Fields), OpenRecord: true, AdditionalProperties: &additional, SchemaID: base.SchemaID, ScopeID: base.ScopeID}, true, nil
		}

		return ResolvedType{}, false, nil
	}
	if resolvedBase.Fields != nil && resolvedPatch.Fields != nil {
		fields := slices.Clone(resolvedBase.Fields)
		for _, patchField := range resolvedPatch.Fields {
			index := slices.IndexFunc(fields, func(field TypeField) bool { return field.Name == patchField.Name })
			if index < 0 {
				additionalType := resolvedBase.AdditionalProperties
				if resolvedBase.Name == typeMap && resolvedBase.Element != nil {
					additionalType = resolvedBase.Element
				}
				if resolvedBase.OpenRecord {
					if additionalType == nil {
						fields = append(fields, patchField)

						continue
					}
					matches, compatible := staticTypeAssignable(patchField.Type, *additionalType, types)
					if compatible && !matches {
						return ResolvedType{}, false, fmt.Errorf("field %q cannot be merged into %s", patchField.Name, staticTypeName(resolvedBase))
					}
					if !compatible {
						return ResolvedType{}, false, nil
					}
					fields = append(fields, patchField)

					continue
				}
				if resolvedBase.Name == typeMap && resolvedBase.Element != nil {
					matches, compatible := staticTypeAssignable(patchField.Type, *resolvedBase.Element, types)
					if compatible && !matches {
						return ResolvedType{}, false, fmt.Errorf("field %q cannot be merged into %s", patchField.Name, staticTypeName(resolvedBase))
					}
					if !compatible {
						return ResolvedType{}, false, nil
					}
					fields = append(fields, patchField)

					continue
				}
				fields = append(fields, patchField)

				continue
			}
			merged, known, err := mergeStaticTypes(fields[index].Type, patchField.Type, types)
			if err != nil {
				return ResolvedType{}, false, fmt.Errorf("field %q: %w", patchField.Name, err)
			}
			if !known {
				return ResolvedType{}, false, nil
			}
			fields[index] = TypeField{Name: patchField.Name, Type: merged, Optional: fields[index].Optional && patchField.Optional}
		}
		merged := ResolvedType{Fields: fields, OpenRecord: resolvedBase.OpenRecord || resolvedPatch.OpenRecord, SchemaID: base.SchemaID, ScopeID: base.ScopeID}
		if merged.OpenRecord && resolvedBase.OpenRecord && resolvedPatch.OpenRecord && resolvedBase.AdditionalProperties != nil && resolvedPatch.AdditionalProperties != nil {
			merged.AdditionalProperties = new(joinStaticTypes(*resolvedBase.AdditionalProperties, *resolvedPatch.AdditionalProperties))
		} else if merged.OpenRecord && resolvedBase.OpenRecord && !resolvedPatch.OpenRecord {
			merged.AdditionalProperties = resolvedBase.AdditionalProperties
		} else if merged.OpenRecord && resolvedPatch.OpenRecord && !resolvedBase.OpenRecord {
			merged.AdditionalProperties = resolvedPatch.AdditionalProperties
		}

		return merged, true, nil
	}
	if resolvedBase.Name != resolvedPatch.Name {
		return ResolvedType{}, false, fmt.Errorf("cannot merge %s with %s; use an explicit replacement", staticTypeName(resolvedBase), staticTypeName(resolvedPatch))
	}
	switch resolvedBase.Name {
	case typeList, typeMap, typeBool, typeInt, typeDecimal, typeString:
		return resolvedPatch, true, nil
	default:
		return ResolvedType{}, false, fmt.Errorf("cannot merge %s", staticTypeName(resolvedBase))
	}
}

func removeStaticTypePath(typeExpr ResolvedType, path []string, types map[string]ResolvedType) (ResolvedType, bool, error) {
	schemaID := typeExpr.SchemaID
	scopeID := typeExpr.ScopeID
	resolved, known := resolveStaticAlias(typeExpr, types, make(map[string]bool))
	if !known || resolved.Fields == nil {
		return ResolvedType{}, false, nil
	}
	resolved.Fields = slices.Clone(resolved.Fields)
	if len(path) == 0 || path[0] == "" {
		return ResolvedType{}, false, errors.New("invalid field path")
	}
	index := slices.IndexFunc(resolved.Fields, func(field TypeField) bool { return field.Name == path[0] })
	if index < 0 {
		return ResolvedType{}, false, fmt.Errorf("field %q does not exist in the inferred type", path[0])
	}
	if len(path) == 1 {
		resolved.Fields = slices.Delete(resolved.Fields, index, index+1)
		resolved.SchemaID = schemaID
		resolved.ScopeID = scopeID

		return resolved, true, nil
	}
	child, known, err := removeStaticTypePath(resolved.Fields[index].Type, path[1:], types)
	if err != nil || !known {
		return ResolvedType{}, known, err
	}
	resolved.Fields[index].Type = child
	resolved.SchemaID = schemaID
	resolved.ScopeID = scopeID

	return resolved, true, nil
}

func inferStaticOptionalReference(text string, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (ResolvedType, bool, error) {
	root := referenceRoot(text)
	binding, exists := bindings[root]
	if !exists {
		return ResolvedType{}, false, nil
	}
	current := binding.Type
	rest := text[len(root):]
	chain := false
	for rest != "" {
		if strings.HasPrefix(rest, "?.") {
			rest = rest[2:]
			chain = true
		} else if strings.HasPrefix(rest, ".") {
			rest = rest[1:]
		} else {
			return ResolvedType{}, false, nil
		}
		fieldEnd := len(rest)
		if next := strings.Index(rest, "?."); next >= 0 && next < fieldEnd {
			fieldEnd = next
		}
		if next := strings.IndexByte(rest, '.'); next >= 0 && next < fieldEnd {
			fieldEnd = next
		}
		field := rest[:fieldEnd]
		if field == "" {
			return ResolvedType{}, false, nil
		}
		rest = rest[fieldEnd:]
		fieldType, known, found, optional := staticField(current, field, types, make(map[string]bool))
		if !known {
			return ResolvedType{}, false, nil
		}
		if !found {
			if chain {
				return ResolvedType{Name: typeNull}, true, nil
			}

			return ResolvedType{}, false, fmt.Errorf("field %q does not exist in the inferred type", field)
		}
		if optional && !chain {
			return ResolvedType{}, false, nil
		}
		current = fieldType
	}
	if !chain {
		return ResolvedType{}, false, nil
	}

	return joinStaticTypes(current, ResolvedType{Name: typeNull}), true, nil
}

func staticSameOperandResultType(typeExpr ResolvedType, types map[string]ResolvedType, valid func(ResolvedType) bool) (ResolvedType, bool) {
	options := typeExpr.Options
	if len(options) == 0 {
		options = []ResolvedType{typeExpr}
	}
	var result ResolvedType
	for index, option := range options {
		resolved, known := resolveStaticAlias(option, types, make(map[string]bool))
		if !known || len(resolved.Options) != 0 || !valid(resolved) {
			return ResolvedType{}, false
		}
		if index == 0 {
			result = resolved
		} else {
			result = joinStaticTypes(result, resolved)
		}
	}

	return result, len(options) != 0
}

func removeNullStaticType(typeExpr ResolvedType) (ResolvedType, bool) {
	if len(typeExpr.Options) == 0 {
		return typeExpr, typeExpr.Name == typeNull
	}
	options := make([]ResolvedType, 0, len(typeExpr.Options))
	removed := false
	for _, option := range typeExpr.Options {
		if option.Name == typeNull && len(option.Options) == 0 {
			removed = true

			continue
		}
		options = append(options, option)
	}
	if len(options) == 0 {
		return ResolvedType{Name: typeNull}, removed
	}
	if len(options) == 1 {
		return options[0], removed
	}

	return ResolvedType{Options: options}, removed
}

func inferStaticHigherOrder(function string, analysis TypeAnalysis, inputs []ResolvedType, initial ResolvedType, types map[string]ResolvedType) (ResolvedType, bool, error) {
	signature := analysis.Type
	if signature.Name == typeFunction && signature.SchemaPath != "" {
		return inferStaticBuiltinCallback(function, signature.SchemaPath, inputs, types)
	}
	signature, known := resolveStaticAlias(signature, types, make(map[string]bool))
	if !known || signature.Name != typeFunction {
		return ResolvedType{}, false, nil
	}
	if len(signature.Parameters) != len(inputs) {
		return ResolvedType{}, false, fmt.Errorf("%s function expects %d parameters", function, len(inputs))
	}
	var functionBindings map[string]TypeAnalysis
	typeSubstitutions := make(map[string]ResolvedType)
	functionInference := analysis.Facts.FunctionInference
	if functionInference != nil {
		functionBindings = maps.Clone(functionInference.Bindings)
	}
	if functionBindings == nil {
		functionBindings = make(map[string]TypeAnalysis)
	}
	for index, input := range inputs {
		parameter := signature.Parameters[index]
		if isLambdaTypeVariable(parameter.Type) {
			typeSubstitutions[parameter.Type.Name] = input
			if parameter.Name != "" {
				functionBindings[parameter.Name] = TypeAnalysis{Type: input}
			}

			continue
		}
		if parameter.Type.Name != "" || parameter.Type.Fields != nil || parameter.Type.OpenRecord || parameter.Type.Element != nil || len(parameter.Type.Options) != 0 {
			matches, compatible := staticTypeAssignable(input, parameter.Type, types)
			if compatible && !matches {
				name := parameter.Name
				if name == "" {
					name = strconv.Itoa(index + 1)
				}

				return ResolvedType{}, false, fmt.Errorf("%s function parameter %q expects %s, got %s", function, name, staticTypeName(parameter.Type), staticTypeName(input))
			}
			if !compatible {
				return ResolvedType{}, false, nil
			}
		} else if parameter.Name != "" {
			functionBindings[parameter.Name] = TypeAnalysis{Type: input}
		}
	}
	returnType := signature.ReturnType
	if functionInference != nil && functionInference.TypedBody != nil {
		specialized, err := specializeTypedExpression(*functionInference.TypedBody, typeSubstitutions, types)
		if err != nil {
			return ResolvedType{}, false, fmt.Errorf("%s function body: %w", function, err)
		}
		inferred := specialized.resolvedType
		if returnType == nil || containsLambdaTypeVariableType(*returnType) {
			returnType = &inferred
		}
	}
	if returnType == nil && functionInference != nil && functionInference.Body != nil {
		inferred, known, err := inferStaticExprType(functionInference.Body, functionBindings, types)
		if err != nil {
			return ResolvedType{}, false, fmt.Errorf("%s function body: %w", function, err)
		}
		if known {
			returnType = new(inferred)
		}
	}
	if returnType == nil {
		return ResolvedType{}, false, nil
	}
	if function == fnFilter || function == fnExactlyOne {
		matches, compatible := staticTypeAssignable(*returnType, ResolvedType{Name: typeBool}, types)
		if compatible && !matches {
			return ResolvedType{}, false, fmt.Errorf("%s function must return Bool, got %s", function, staticTypeName(*returnType))
		}
		if !compatible {
			return ResolvedType{}, false, nil
		}
		if function == fnExactlyOne {
			return inputs[0], true, nil
		}

		return initial, true, nil
	}
	if function == fnFold {
		return joinStaticTypes(initial, *returnType), true, nil
	}

	return ResolvedType{Name: typeList, Element: returnType}, true, nil
}

func staticTypeIsKnown(typeExpr ResolvedType) bool {
	return typeExpr.Name != "" || typeExpr.Fields != nil || typeExpr.OpenRecord || typeExpr.Element != nil || len(typeExpr.Options) != 0
}

func staticFunctionName(reference string, bindings map[string]TypeAnalysis, types map[string]ResolvedType) (string, bool) {
	root, path, _ := strings.Cut(reference, ".")
	if binding, exists := bindings[root]; exists {
		current := binding.Type
		if path == "" {
			if current.Name == typeFunction && current.SchemaPath != "" {
				return current.SchemaPath, true
			}

			return "", false
		}
		for path != "" {
			field, tail, _ := strings.Cut(path, ".")
			currentType, known, exists, _ := staticField(current, field, types, make(map[string]bool))
			if !known || !exists {
				return "", false
			}
			current = currentType
			path = tail
		}
		if current.Name == typeFunction && current.SchemaPath != "" {
			return current.SchemaPath, true
		}

		return "", false
	}
	_, exists := standardFunctions[reference]

	return reference, exists
}

func staticListElement(typeExpr ResolvedType, types map[string]ResolvedType) (ResolvedType, bool) {
	resolved, known := resolveStaticAlias(typeExpr, types, make(map[string]bool))
	if !known {
		return ResolvedType{}, false
	}
	typeExpr = resolved
	if len(typeExpr.Options) > 0 {
		var element ResolvedType
		for index, option := range typeExpr.Options {
			optionElement, isList := staticListElement(option, types)
			if !isList {
				return ResolvedType{}, false
			}
			if index == 0 {
				element = optionElement
			} else {
				element = joinStaticTypes(element, optionElement)
			}
		}

		return element, true
	}
	if typeExpr.Name != typeList {
		if typeExpr.Name == typeNode && len(typeExpr.Arguments) == 1 {
			return staticListElement(typeExpr.Arguments[0], types)
		}

		return ResolvedType{}, false
	}
	if typeExpr.Element == nil {
		return ResolvedType{Name: typeAny}, true
	}

	return *typeExpr.Element, true
}

func staticLenType(typeExpr ResolvedType, types map[string]ResolvedType) bool {
	resolved, known := resolveStaticAlias(typeExpr, types, make(map[string]bool))
	if !known {
		return false
	}
	if resolved.Name == typeNode && len(resolved.Arguments) == 1 {
		return staticLenType(resolved.Arguments[0], types)
	}
	if len(resolved.Options) > 0 {
		for _, option := range resolved.Options {
			if !staticLenType(option, types) {
				return false
			}
		}

		return true
	}

	return resolved.Name == typeAny || resolved.Name == typeString || resolved.Name == typeList || resolved.Name == typeMap || resolved.Fields != nil || resolved.OpenRecord
}

func staticContainsContainer(typeExpr ResolvedType, types map[string]ResolvedType) (bool, bool) {
	resolved, known := resolveStaticAlias(typeExpr, types, make(map[string]bool))
	if !known {
		return false, false
	}
	if resolved.Name == typeNode && len(resolved.Arguments) == 1 {
		return staticContainsContainer(resolved.Arguments[0], types)
	}
	if len(resolved.Options) > 0 {
		includesString := false
		for _, option := range resolved.Options {
			valid, optionIncludesString := staticContainsContainer(option, types)
			if !valid {
				return false, false
			}
			includesString = includesString || optionIncludesString
		}

		return true, includesString
	}
	if resolved.Name == typeAny || resolved.Name == typeList {
		return true, false
	}
	if resolved.Name == typeString {
		return true, true
	}

	return false, false
}

func staticMapType(typeExpr ResolvedType, types map[string]ResolvedType) bool {
	resolved, known := resolveStaticAlias(typeExpr, types, make(map[string]bool))
	if !known {
		return false
	}
	if resolved.Name == typeNode && len(resolved.Arguments) == 1 {
		return staticMapType(resolved.Arguments[0], types)
	}
	if resolved.Name == typeMap || resolved.Fields != nil || resolved.OpenRecord {
		return true
	}
	if len(resolved.Options) == 0 {
		return false
	}
	for _, option := range resolved.Options {
		if !staticMapType(option, types) {
			return false
		}
	}

	return true
}

func staticMembershipContainer(typeExpr ResolvedType, types map[string]ResolvedType) (bool, bool) {
	resolved, known := resolveStaticAlias(typeExpr, types, make(map[string]bool))
	if !known {
		return false, false
	}
	if resolved.Name == typeNode && len(resolved.Arguments) == 1 {
		return staticMembershipContainer(resolved.Arguments[0], types)
	}
	if len(resolved.Options) > 0 {
		isMap := false
		for _, option := range resolved.Options {
			isContainer, optionIsMap := staticMembershipContainer(option, types)
			if !isContainer {
				return false, false
			}
			isMap = isMap || optionIsMap
		}

		return true, isMap
	}
	if resolved.Name == typeAny {
		return true, false
	}
	if resolved.Name == typeList {
		return true, false
	}
	if resolved.Name == typeMap || resolved.Fields != nil || resolved.OpenRecord {
		return true, true
	}

	return false, false
}

func staticTypeName(typeExpr ResolvedType) string {
	if typeExpr.Name != "" {
		if typeExpr.Name == typeNode && len(typeExpr.Arguments) == 1 {
			return "Node<" + staticTypeName(typeExpr.Arguments[0]) + ">"
		}

		return typeExpr.Name
	}
	if len(typeExpr.Options) > 0 {
		return "Union"
	}
	if typeExpr.Fields != nil || typeExpr.OpenRecord {
		return typeMap
	}
	if typeExpr.Element != nil {
		return typeList
	}

	return typeAny
}

func joinStaticTypes(left, right ResolvedType) ResolvedType {
	if equalStaticTypes(left, right) {
		return left
	}
	options := make([]ResolvedType, 0, 2)
	if len(left.Options) > 0 {
		options = append(options, left.Options...)
	} else {
		options = append(options, left)
	}
	if len(right.Options) > 0 {
		options = append(options, right.Options...)
	} else {
		options = append(options, right)
	}
	unique := options[:0]
	for _, option := range options {
		found := false
		for _, previous := range unique {
			if equalStaticTypes(option, previous) {
				found = true

				break
			}
		}
		if !found {
			unique = append(unique, option)
		}
	}

	return ResolvedType{Options: unique}
}

func preserveBundleProvenance(inferred TypeAnalysis, declared ResolvedType, types map[string]ResolvedType) TypeAnalysis {
	result := TypeAnalysis{Type: declared, Facts: inferred.Facts}
	resolved, known := resolveStaticAlias(declared, types, make(map[string]bool))
	if !known || resolved.Name != typeList {
		result.Facts.Bundle = false
		result.Facts.BundleKnown = false
	}

	return result
}

func equalStaticTypes(left, right ResolvedType) bool {
	if left.Name != right.Name || left.StringLiteral != right.StringLiteral || left.StringLiteralKnown != right.StringLiteralKnown || left.SchemaPath != right.SchemaPath || left.SchemaID != right.SchemaID || left.ScopeID != right.ScopeID || left.Optional != right.Optional || left.OpenRecord != right.OpenRecord || len(left.TypeParameters) != len(right.TypeParameters) || len(left.Arguments) != len(right.Arguments) || len(left.Fields) != len(right.Fields) || len(left.Options) != len(right.Options) || len(left.Parameters) != len(right.Parameters) || (left.ReturnType == nil) != (right.ReturnType == nil) || (left.AdditionalProperties == nil) != (right.AdditionalProperties == nil) {
		return false
	}
	for index, parameter := range left.TypeParameters {
		if parameter != right.TypeParameters[index] {
			return false
		}
	}
	for index, argument := range left.Arguments {
		if !equalStaticTypes(argument, right.Arguments[index]) {
			return false
		}
	}
	if left.ReturnType != nil && !equalStaticTypes(*left.ReturnType, *right.ReturnType) {
		return false
	}
	if left.AdditionalProperties != nil && !equalStaticTypes(*left.AdditionalProperties, *right.AdditionalProperties) {
		return false
	}
	for index, parameter := range left.Parameters {
		if parameter.Name != right.Parameters[index].Name || !equalStaticTypes(parameter.Type, right.Parameters[index].Type) {
			return false
		}
	}
	if (left.Element == nil) != (right.Element == nil) {
		return false
	}
	if left.Element != nil && !equalStaticTypes(*left.Element, *right.Element) {
		return false
	}
	for index, field := range left.Fields {
		other := right.Fields[index]
		if field.Name != other.Name || field.Optional != other.Optional || !equalStaticTypes(field.Type, other.Type) {
			return false
		}
	}
	for index, option := range left.Options {
		if !equalStaticTypes(option, right.Options[index]) {
			return false
		}
	}

	return true
}

func staticField(typeExpr ResolvedType, name string, types map[string]ResolvedType, resolving map[string]bool) (ResolvedType, bool, bool, bool) {
	if typeExpr.SchemaID != "" && typeExpr.Fields == nil && !typeExpr.OpenRecord {
		inferred, known := staticSchemaType(nil, make(map[*jsonschema.Schema]bool))
		if !known {
			return ResolvedType{}, false, false, false
		}

		return staticField(inferred, name, types, resolving)
	}
	if typeExpr.Name == typeNode && len(typeExpr.Arguments) == 1 {
		return staticField(typeExpr.Arguments[0], name, types, resolving)
	}
	if len(typeExpr.Options) > 0 {
		var fieldType ResolvedType
		foundAny := false
		optional := false
		for _, option := range typeExpr.Options {
			branchType, known, found, branchOptional := staticField(option, name, types, resolving)
			if !known {
				return ResolvedType{}, false, false, false
			}
			if !found {
				optional = true

				continue
			}
			if foundAny {
				fieldType = joinStaticTypes(fieldType, branchType)
			} else {
				fieldType = branchType
				foundAny = true
			}
			optional = optional || branchOptional
		}
		if !foundAny {
			return ResolvedType{}, true, false, false
		}

		return fieldType, true, true, optional
	}
	if typeExpr.Name == typeFunction && typeExpr.SchemaPath != "" {
		return ResolvedType{}, true, false, false
	}
	if typeExpr.Fields != nil || typeExpr.OpenRecord {
		for _, field := range typeExpr.Fields {
			if field.Name == name {
				return field.Type, true, true, field.Optional
			}
		}
		if typeExpr.OpenRecord {
			if typeExpr.AdditionalProperties != nil {
				return *typeExpr.AdditionalProperties, true, true, true
			}

			return ResolvedType{Name: typeAny}, true, true, true
		}

		return ResolvedType{}, false, false, false
	}
	if typeExpr.Name == typeNull || typeExpr.Name == typeBool || typeExpr.Name == typeInt || typeExpr.Name == typeDecimal || typeExpr.Name == typeString {
		return ResolvedType{}, true, false, false
	}
	if typeExpr.Name != "" {
		if typeExpr.Name == typeAny || typeExpr.SchemaID != "" || typeExpr.SchemaPath != "" {
			return ResolvedType{}, false, false, false
		}
		if typeExpr.Name != typeList && typeExpr.Name != typeNull && typeExpr.Name != typeBool && typeExpr.Name != typeInt && typeExpr.Name != typeDecimal && typeExpr.Name != typeString {
			definition, exists := types[typeExpr.Name]
			if !exists || resolving[typeExpr.Name] {
				return ResolvedType{}, false, false, false
			}
			resolving[typeExpr.Name] = true
			instantiated, err := instantiateTypeAlias(typeExpr, definition)
			if err != nil {
				delete(resolving, typeExpr.Name)

				return ResolvedType{}, false, false, false
			}
			field, known, exists, optional := staticField(instantiated, name, types, resolving)
			delete(resolving, typeExpr.Name)

			return field, known, exists, optional
		}
	}
	if typeExpr.Fields == nil {
		return ResolvedType{}, false, false, false
	}
	for _, field := range typeExpr.Fields {
		if field.Name == name {
			return field.Type, true, true, field.Optional
		}
	}

	return ResolvedType{}, true, false, false
}

func staticTypeAssignable(inferred, expected ResolvedType, types map[string]ResolvedType) (bool, bool) {
	if isLambdaTypeVariable(inferred) || isLambdaTypeVariable(expected) {
		return inferred.Name == expected.Name, inferred.Name == expected.Name
	}
	if expected.Name == typeAny {
		return true, true
	}
	if inferred.Name == typeAny {
		return true, false
	}
	if len(inferred.Options) > 0 {
		unknown := false
		for _, option := range inferred.Options {
			matches, known := staticTypeAssignable(option, expected, types)
			if !known {
				unknown = true
			} else if !matches {
				return false, true
			}
		}

		return true, !unknown
	}
	if len(expected.Options) > 0 {
		unknown := false
		for _, option := range expected.Options {
			matches, known := staticTypeAssignable(inferred, option, types)
			if matches && known {
				return true, true
			}
			unknown = unknown || !known
		}

		return false, !unknown
	}
	inferred, inferredKnown := resolveStaticAlias(inferred, types, make(map[string]bool))
	expected, expectedKnown := resolveStaticAlias(expected, types, make(map[string]bool))
	if !inferredKnown || !expectedKnown {
		return false, false
	}
	if len(inferred.Options) > 0 || len(expected.Options) > 0 {
		return staticTypeAssignable(inferred, expected, types)
	}
	if expected.Name == typeAny {
		return true, true
	}
	if inferred.Name == typeAny {
		return true, false
	}
	if expected.Name == typeNode {
		if inferred.Name != typeNode || len(inferred.Arguments) != 1 || len(expected.Arguments) != 1 {
			return false, true
		}

		return staticTypeAssignable(inferred.Arguments[0], expected.Arguments[0], types)
	}
	if inferred.Name == typeNode {
		if len(inferred.Arguments) != 1 {
			return false, true
		}

		return staticTypeAssignable(inferred.Arguments[0], expected, types)
	}
	if inferred.Name == typeMap && inferred.Fields != nil && expected.Name == "" && expected.Fields != nil {
		return staticOpenMapToRecordAssignable(inferred, expected, types)
	}
	if expected.Name == typeMap && expected.Fields != nil {
		return staticMapFieldsAssignable(inferred, expected, types)
	}
	if expected.Name == typeMap && expected.Fields == nil && expected.Element == nil && (inferred.Fields != nil || inferred.OpenRecord) {
		return true, true
	}
	if expected.Name == typeMap && expected.Element != nil && (inferred.Fields != nil || inferred.OpenRecord) {
		unknown := false
		for _, field := range inferred.Fields {
			matches, known := staticTypeAssignable(field.Type, *expected.Element, types)
			if known && !matches {
				return false, true
			}
			if !known {
				unknown = true
			}
		}
		if inferred.OpenRecord {
			additional := ResolvedType{Name: typeAny}
			if inferred.AdditionalProperties != nil {
				additional = *inferred.AdditionalProperties
			}
			matches, known := staticTypeAssignable(additional, *expected.Element, types)
			if known && !matches {
				return false, true
			}
			unknown = unknown || !known
		}
		if inferred.Name == typeMap {
			if inferred.Element != nil {
				return true, !unknown
			}

			return false, false
		}

		return true, !unknown
	}
	if inferred.Name != "" || expected.Name != "" {
		if inferred.Name != expected.Name {
			return false, true
		}
		if inferred.Name == typeList && expected.Element != nil {
			if inferred.Element == nil {
				return true, false
			}
			if inferred.Element.Name == typeAny {
				return true, false
			}

			return staticTypeAssignable(*inferred.Element, *expected.Element, types)
		}
		if inferred.Name == typeMap && expected.Element != nil {
			if inferred.Element == nil {
				return true, false
			}

			return staticTypeAssignable(*inferred.Element, *expected.Element, types)
		}
		if inferred.Name == typeFunction && expected.Name == typeFunction && expected.ReturnType != nil {
			if inferred.ReturnType == nil || len(inferred.Parameters) != len(expected.Parameters) {
				return false, inferred.ReturnType != nil
			}
			for index := range inferred.Parameters {
				matches, known := staticTypeAssignable(expected.Parameters[index].Type, inferred.Parameters[index].Type, types)
				if !known || !matches {
					return matches, known
				}
			}

			return staticTypeAssignable(*inferred.ReturnType, *expected.ReturnType, types)
		}

		return true, true
	}
	if (inferred.Fields == nil && !inferred.OpenRecord) || (expected.Fields == nil && !expected.OpenRecord) {
		return false, false
	}
	knownFields := make(map[string]TypeField, len(inferred.Fields))
	for _, field := range inferred.Fields {
		knownFields[field.Name] = field
	}
	unknown := false
	for _, expectedField := range expected.Fields {
		inferredField, exists := knownFields[expectedField.Name]
		if !exists {
			if expectedField.Optional {
				if inferred.OpenRecord {
					additional := ResolvedType{Name: typeAny}
					if inferred.AdditionalProperties != nil {
						additional = *inferred.AdditionalProperties
					}
					matches, known := staticTypeAssignable(additional, expectedField.Type, types)
					if known && !matches {
						return false, true
					}
					unknown = unknown || !known
				}

				continue
			}

			return false, true
		}
		if inferredField.Optional && !expectedField.Optional {
			return false, true
		}
		matches, known := staticTypeAssignable(inferredField.Type, expectedField.Type, types)
		if !known {
			unknown = true

			continue
		}
		if !matches {
			return false, true
		}
	}
	expectedFields := make(map[string]bool, len(expected.Fields))
	for _, field := range expected.Fields {
		expectedFields[field.Name] = true
	}
	for name := range knownFields {
		if !expectedFields[name] {
			if expected.OpenRecord {
				field := knownFields[name]
				if expected.AdditionalProperties != nil {
					matches, known := staticTypeAssignable(field.Type, *expected.AdditionalProperties, types)
					if known && !matches {
						return false, true
					}
					unknown = unknown || !known
				}

				continue
			}

			return false, true
		}
	}
	if inferred.OpenRecord && !expected.OpenRecord {
		return false, true
	}
	if inferred.OpenRecord && expected.OpenRecord {
		if inferred.AdditionalProperties == nil {
			if expected.AdditionalProperties != nil {
				unknown = true
			}
		} else if expected.AdditionalProperties != nil {
			matches, known := staticTypeAssignable(*inferred.AdditionalProperties, *expected.AdditionalProperties, types)
			if known && !matches {
				return false, true
			}
			unknown = unknown || !known
		}
	}

	return true, !unknown
}

func staticOpenMapToRecordAssignable(inferred, expected ResolvedType, types map[string]ResolvedType) (bool, bool) {
	inferredFields := make(map[string]TypeField, len(inferred.Fields))
	for _, field := range inferred.Fields {
		inferredFields[field.Name] = field
	}
	expectedFields := make(map[string]TypeField, len(expected.Fields))
	for _, field := range expected.Fields {
		expectedFields[field.Name] = field
	}
	for _, expectedField := range expected.Fields {
		inferredField, exists := inferredFields[expectedField.Name]
		if !exists {
			if !expectedField.Optional {
				return false, false
			}

			continue
		}
		if inferredField.Optional && !expectedField.Optional {
			return false, false
		}
		matches, known := staticTypeAssignable(inferredField.Type, expectedField.Type, types)
		if known && !matches {
			return false, true
		}
		if !known {
			return false, false
		}
	}
	for _, inferredField := range inferred.Fields {
		if _, exists := expectedFields[inferredField.Name]; exists {
			continue
		}
		if !inferredField.Optional {
			return false, true
		}
	}

	return false, false
}

func staticMapFieldsAssignable(inferred, expected ResolvedType, types map[string]ResolvedType) (bool, bool) {
	if inferred.Fields == nil {
		if inferred.Name != typeMap {
			return false, false
		}
		if inferred.Element == nil {
			if expected.OpenRecord && expected.AdditionalProperties == nil && len(expected.Fields) == 0 {
				return true, true
			}

			return false, false
		}
		for _, expectedField := range expected.Fields {
			if !expectedField.Optional {
				return false, false
			}
		}
		if !expected.OpenRecord {
			return false, false
		}
		unknown := false
		for _, expectedField := range expected.Fields {
			matches, known := staticTypeAssignable(*inferred.Element, expectedField.Type, types)
			if known && !matches {
				return false, true
			}
			unknown = unknown || !known
		}
		if expected.AdditionalProperties != nil {
			matches, known := staticTypeAssignable(*inferred.Element, *expected.AdditionalProperties, types)
			if known && !matches {
				return false, true
			}
			unknown = unknown || !known
		}

		return true, !unknown
	}
	inferredFields := make(map[string]TypeField, len(inferred.Fields))
	for _, field := range inferred.Fields {
		inferredFields[field.Name] = field
	}
	unknown := false
	for _, expectedField := range expected.Fields {
		inferredField, exists := inferredFields[expectedField.Name]
		if !exists {
			if expectedField.Optional {
				continue
			}

			return false, true
		}
		if inferredField.Optional && !expectedField.Optional {
			return false, true
		}
		matches, known := staticTypeAssignable(inferredField.Type, expectedField.Type, types)
		if known && !matches {
			return false, true
		}
		unknown = unknown || !known
	}

	return true, !unknown
}

func staticSchemaType(schema *jsonschema.Schema, resolving map[*jsonschema.Schema]bool) (ResolvedType, bool) {
	if schema == nil || resolving[schema] || schema.Bool != nil {
		return ResolvedType{}, false
	}
	if schema.Ref != nil {
		if !staticSchemaPureRef(schema) {
			return ResolvedType{}, false
		}
		resolving[schema] = true
		defer delete(resolving, schema)

		return staticSchemaType(schema.Ref, resolving)
	}
	if schema.RecursiveRef != nil || schema.DynamicRef != nil || schema.Not != nil || len(schema.AllOf) > 0 || len(schema.AnyOf) > 0 || len(schema.OneOf) > 0 || schema.If != nil || schema.Then != nil || schema.Else != nil || len(schema.PatternProperties) > 0 || schema.PropertyNames != nil || len(schema.Dependencies) > 0 || len(schema.DependentRequired) > 0 || len(schema.DependentSchemas) > 0 || schema.UnevaluatedProperties != nil || schema.UnevaluatedItems != nil || len(schema.PrefixItems) > 0 || schema.Contains != nil {
		return ResolvedType{}, false
	}
	if schema.Types == nil {
		return ResolvedType{}, false
	}
	resolving[schema] = true
	defer delete(resolving, schema)
	var inferred ResolvedType
	for index, schemaType := range schema.Types.ToStrings() {
		var current ResolvedType
		switch schemaType {
		case schemaTypeNull:
			current = ResolvedType{Name: typeNull}
		case schemaTypeBoolean:
			current = ResolvedType{Name: typeBool}
		case schemaTypeInteger:
			current = ResolvedType{Name: typeInt}
		case schemaTypeNumber:
			current = joinStaticTypes(ResolvedType{Name: typeInt}, ResolvedType{Name: typeDecimal})
		case schemaTypeString:
			current = ResolvedType{Name: typeString}
		case schemaTypeArray:
			element, known := staticSchemaArrayElement(schema, resolving)
			if !known {
				return ResolvedType{}, false
			}
			current = ResolvedType{Name: typeList, Element: &element}
		case schemaTypeObject:
			var known bool
			current, known = staticSchemaObjectType(schema, resolving)
			if !known {
				return ResolvedType{}, false
			}
		default:
			return ResolvedType{}, false
		}
		if index == 0 {
			inferred = current
		} else {
			inferred = joinStaticTypes(inferred, current)
		}
	}

	return inferred, inferred.Name != "" || inferred.Fields != nil || inferred.OpenRecord || len(inferred.Options) > 0
}

func staticSchemaPureRef(schema *jsonschema.Schema) bool {
	return schema.ID == "" && schema.Anchor == "" && !schema.RecursiveAnchor && schema.DynamicAnchor == "" &&
		schema.Types == nil && schema.Enum == nil && schema.Const == nil && schema.Not == nil &&
		len(schema.AllOf) == 0 && len(schema.AnyOf) == 0 && len(schema.OneOf) == 0 && schema.If == nil && schema.Then == nil && schema.Else == nil &&
		schema.Format == nil && schema.MaxProperties == nil && schema.MinProperties == nil && len(schema.Required) == 0 && schema.PropertyNames == nil &&
		len(schema.Properties) == 0 && len(schema.PatternProperties) == 0 && schema.AdditionalProperties == nil && len(schema.Dependencies) == 0 &&
		len(schema.DependentRequired) == 0 && len(schema.DependentSchemas) == 0 && schema.UnevaluatedProperties == nil &&
		schema.MinItems == nil && schema.MaxItems == nil && !schema.UniqueItems && schema.Contains == nil && schema.MinContains == nil && schema.MaxContains == nil &&
		schema.Items == nil && schema.AdditionalItems == nil && len(schema.PrefixItems) == 0 && schema.Items2020 == nil && schema.UnevaluatedItems == nil &&
		schema.MinLength == nil && schema.MaxLength == nil && schema.Pattern == nil && schema.ContentEncoding == nil && schema.ContentMediaType == nil && schema.ContentSchema == nil &&
		schema.Maximum == nil && schema.Minimum == nil && schema.ExclusiveMaximum == nil && schema.ExclusiveMinimum == nil && schema.MultipleOf == nil &&
		len(schema.Extensions) == 0 && schema.Title == "" && schema.Description == "" && schema.Default == nil && schema.Comment == "" &&
		!schema.ReadOnly && !schema.WriteOnly && len(schema.Examples) == 0 && !schema.Deprecated && schema.RecursiveRef == nil && schema.DynamicRef == nil
}

func staticSchemaArrayElement(schema *jsonschema.Schema, resolving map[*jsonschema.Schema]bool) (ResolvedType, bool) {
	if schema.Items == nil && schema.Items2020 == nil {
		return ResolvedType{Name: typeAny}, true
	}
	if schema.Items != nil && schema.Items2020 != nil {
		return ResolvedType{}, false
	}
	var items *jsonschema.Schema
	if schema.Items != nil {
		var ok bool
		items, ok = schema.Items.(*jsonschema.Schema)
		if !ok {
			return ResolvedType{}, false
		}
	} else {
		items = schema.Items2020
	}

	return staticSchemaType(items, resolving)
}

func staticSchemaObjectType(schema *jsonschema.Schema, resolving map[*jsonschema.Schema]bool) (ResolvedType, bool) {
	open := true
	var additionalProperties *ResolvedType
	switch additional := schema.AdditionalProperties.(type) {
	case nil:
	case bool:
		open = additional
	case *jsonschema.Schema:
		inferred, known := staticSchemaType(additional, resolving)
		if !known {
			return ResolvedType{}, false
		}
		additionalProperties = &inferred
	default:
		return ResolvedType{}, false
	}
	propertyTypes := make(map[string]ResolvedType, len(schema.Properties)+len(schema.Required))
	for _, name := range slices.Sorted(maps.Keys(schema.Properties)) {
		propertyType, known := staticSchemaType(schema.Properties[name], resolving)
		if !known {
			return ResolvedType{}, false
		}
		propertyTypes[name] = propertyType
	}
	for _, name := range schema.Required {
		if _, exists := propertyTypes[name]; !exists {
			propertyTypes[name] = ResolvedType{Name: typeAny}
		}
	}
	fields := make([]TypeField, 0, len(propertyTypes))
	for _, name := range slices.Sorted(maps.Keys(propertyTypes)) {
		fields = append(fields, TypeField{
			Name: name, Type: propertyTypes[name], Optional: !slices.Contains(schema.Required, name),
		})
	}
	if open {
		return ResolvedType{Fields: fields, OpenRecord: true, AdditionalProperties: additionalProperties}, true
	}

	return ResolvedType{Fields: fields}, true
}

func resolveStaticAlias(typeExpr ResolvedType, types map[string]ResolvedType, resolving map[string]bool, analyses ...*TypeAnalysisContext) (ResolvedType, bool) {
	if isLambdaTypeVariable(typeExpr) {
		return typeExpr, true
	}
	var analysis *TypeAnalysisContext
	if len(analyses) != 0 {
		analysis = analyses[0]
	}
	if scoped := analysis.scope(typeExpr.ScopeID); scoped != nil {
		types = scoped
	}
	if typeExpr.SchemaID != "" && typeExpr.Fields == nil && !typeExpr.OpenRecord {
		schema := analysis.schema(typeExpr.SchemaID)
		if schema == nil {
			return ResolvedType{}, false
		}
		inferred, known := staticSchemaType(schema, make(map[*jsonschema.Schema]bool))
		inferred.ScopeID = typeExpr.ScopeID
		if typeExpr.StringLiteralKnown {
			inferred.StringLiteral = typeExpr.StringLiteral
			inferred.StringLiteralKnown = true
		}

		return inferred, known
	}
	if typeExpr.SchemaPath != "" {
		return ResolvedType{}, false
	}
	if typeExpr.Name == "" {
		return typeExpr, typeExpr.Fields != nil || typeExpr.OpenRecord || len(typeExpr.Options) > 0
	}
	switch typeExpr.Name {
	case typeAny, typeNull, typeBool, typeInt, typeDecimal, typeString, typeMap, typeFunction, typeNode:
		return typeExpr, true
	case typeList:
		return typeExpr, true
	}
	definition, exists := types[typeExpr.Name]
	if !exists || resolving[typeExpr.Name] {
		return ResolvedType{}, false
	}
	instantiated, err := instantiateTypeAlias(typeExpr, definition)
	if err != nil {
		return ResolvedType{}, false
	}
	resolving[typeExpr.Name] = true
	resolved, known := resolveStaticAlias(instantiated, types, resolving)
	delete(resolving, typeExpr.Name)
	if known && typeExpr.StringLiteralKnown {
		resolved.StringLiteral = typeExpr.StringLiteral
		resolved.StringLiteralKnown = true
	}

	return resolved, known
}

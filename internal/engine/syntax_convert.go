package engine

import "github.com/manifest-configuration-language/mcl/internal/syntax"

func identifier(text string) bool {
	if text == "" {
		return false
	}
	for index, char := range text {
		if index == 0 {
			if !isLetter(char) && char != '_' {
				return false
			}

			continue
		}
		if !isLetter(char) && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}

	return true
}

func isLetter(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}

func semanticTypeFromSyntax(expression syntax.TypeExpr) ResolvedType {
	converted := ResolvedType{Span: expression.Span, Name: expression.Name, StringLiteral: expression.ConstantString, StringLiteralKnown: expression.ConstantStringKnown, SchemaPath: expression.SchemaPath, TypeParameters: append([]string(nil), expression.TypeParameters...)}
	if expression.Arguments != nil {
		converted.Arguments = make([]ResolvedType, len(expression.Arguments))
	}
	if expression.Options != nil {
		converted.Options = make([]ResolvedType, len(expression.Options))
	}
	if expression.Fields != nil {
		converted.Fields = make([]TypeField, len(expression.Fields))
	}
	for index, argument := range expression.Arguments {
		converted.Arguments[index] = semanticTypeFromSyntax(argument)
	}
	for index, option := range expression.Options {
		converted.Options[index] = semanticTypeFromSyntax(option)
	}
	for index, field := range expression.Fields {
		converted.Fields[index] = TypeField{Name: field.Name, NameSpan: field.NameSpan, Type: semanticTypeFromSyntax(field.Type), Optional: field.Optional}
	}
	if expression.Element != nil {
		element := semanticTypeFromSyntax(*expression.Element)
		converted.Element = &element
	}
	converted.Parameters = make([]FunctionTypeParameter, len(expression.Parameters))
	for index, parameter := range expression.Parameters {
		converted.Parameters[index] = FunctionTypeParameter{Name: parameter.Name, Type: semanticTypeFromSyntax(parameter.Type)}
	}
	if expression.ReturnType != nil {
		returnType := semanticTypeFromSyntax(*expression.ReturnType)
		converted.ReturnType = &returnType
	}

	return converted
}

func resolveTypeFromSyntax(expression syntax.TypeExpr, state *EvaluationState) (ResolvedType, error) {
	resolved := semanticTypeFromSyntax(expression)
	if state == nil || state.Module == nil {
		return resolved, nil
	}
	if err := loadSchemaTypes(&resolved, state.Module.ProjectRoot, state.SourcePath, state.Module.Dependencies, state.Module.Analysis); err != nil {
		return ResolvedType{}, err
	}

	return resolved, nil
}

func resolveOptionalTypeFromSyntax(expression *syntax.TypeExpr, state *EvaluationState) (*ResolvedType, error) {
	if expression == nil {
		return nil, nil
	}
	resolved, err := resolveTypeFromSyntax(*expression, state)
	if err != nil {
		return nil, err
	}

	return &resolved, nil
}

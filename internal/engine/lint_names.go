package engine

import "strconv"

func lintUndefinedNames(module ModuleAST) []Diagnostic {
	defined := map[string]bool{
		typeAny: true, typeBool: true, typeDecimal: true, typeInt: true,
		typeString: true, typeNull: true, typeList: true, typeMap: true,
		typeFunction: true, typeNode: true,
	}
	for _, node := range module.Nodes {
		switch declaration := node.(type) {
		case *TypeDeclaration:
			defined[declaration.Name] = true
		case *RuleDeclaration:
			defined[declaration.Name] = true
		case *FunctionDeclaration:
			defined[declaration.Name] = true
		case *LetDeclaration:
			defined[declaration.Name] = true
		case *ImportDeclaration:
			defined[declaration.Name] = true
		}
	}
	var diagnostics []Diagnostic
	seen := make(map[SourceSpan]bool)
	checkName := func(name string, span SourceSpan, line int, locals map[string]bool) {
		root := referenceRoot(name)
		if root == "" || locals[root] || defined[root] {
			return
		}
		if seen[span] {
			return
		}
		seen[span] = true
		diagnostics = append(diagnostics, Diagnostic{
			Code: "MCL-UNDEFINED", Line: line, Severity: 1,
			Message: "undefined name " + strconv.Quote(root), Span: span,
		})
	}
	checkExpression := func(expression Expr, locals map[string]bool, line int) {
		walkLintReferences(expression, locals, func(name string, span SourceSpan) {
			checkName(name, span, line, locals)
		})
	}
	checkType := func(typeExpr TypeExpr, line int) {
		for _, reference := range lintTypeReferences(typeExpr) {
			checkName(reference.name, reference.span, line, nil)
		}
	}
	for _, node := range module.Nodes {
		switch declaration := node.(type) {
		case *LetDeclaration:
			checkExpression(declaration.Expression, nil, declaration.Line)
			if declaration.Annotation != nil {
				checkType(*declaration.Annotation, declaration.Line)
			}
		case *FunctionDeclaration:
			if declaration.Function == nil {
				continue
			}
			locals := make(map[string]bool, len(declaration.Function.Parameters)+len(declaration.Function.Locals))
			for _, parameter := range declaration.Function.Parameters {
				checkType(parameter.Type, declaration.Line)
				checkExpression(parameter.Default, locals, declaration.Line)
				locals[parameter.Name] = true
			}
			for _, local := range declaration.Function.Locals {
				checkExpression(local.Expression, locals, local.Line)
				if local.Annotation != nil {
					checkType(*local.Annotation, local.Line)
				}
				locals[local.Name] = true
			}
			checkExpression(declaration.Function.Expression, locals, declaration.Line)
			if declaration.Function.ReturnType != nil {
				checkType(*declaration.Function.ReturnType, declaration.Line)
			}
		case *RuleDeclaration:
			if declaration.Rule == nil {
				continue
			}
			locals := make(map[string]bool, len(declaration.Rule.Parameters))
			for _, parameter := range declaration.Rule.Parameters {
				locals[parameter.Name] = true
				checkType(parameter.Type, declaration.Line)
			}
			for _, requirement := range declaration.Rule.Requirements {
				checkExpression(requirement.Expression, locals, requirement.Line)
			}
		case *TypeDeclaration:
			if declaration.TypeSpec != nil {
				checkType(*declaration.TypeSpec, declaration.Line)
			}
		case *EmitStatement:
			checkExpression(declaration.Expression, nil, declaration.Line)
		case *ValidateStatement:
			checkExpression(declaration.Expression, nil, declaration.Line)
			checkExpression(declaration.RuleExpr, nil, declaration.Line)
			checkExpression(declaration.QueryWhere, nil, declaration.Line)
			if declaration.QueryType != nil {
				checkType(*declaration.QueryType, declaration.Line)
			}
			if declaration.QuerySchema != nil {
				checkType(*declaration.QuerySchema, declaration.Line)
			}
		}
	}

	return diagnostics
}

func walkLintReferences(expression Expr, locals map[string]bool, visit func(string, SourceSpan)) {
	if isNilExpr(expression) {
		return
	}
	switch node := expression.(type) {
	case *ReferenceExpr:
		if !locals[referenceRoot(node.Name)] {
			visit(node.Name, node.ExprSpan())
		}
	case *OptionalReferenceExpr:
		if !locals[referenceRoot(node.Path)] {
			visit(node.Path, node.ExprSpan())
		}
	case *LambdaExpr:
		nested := cloneLocals(locals)
		for _, parameter := range node.Parameters {
			nested[parameter] = true
		}
		walkLintReferences(node.Body, nested, visit)
	case *ForExpr:
		walkLintReferences(node.Iterable, locals, visit)
		nested := cloneLocals(locals)
		nested[node.Variable] = true
		walkLintReferences(node.Body, nested, visit)
	default:
		walkExprChildren(expression, func(child Expr) { walkLintReferences(child, locals, visit) })
	}
}

type lintTypeReference struct {
	name string
	span SourceSpan
}

func lintTypeReferences(typeExpr TypeExpr) []lintTypeReference {
	var references []lintTypeReference
	var visit func(TypeExpr, map[string]bool)
	visit = func(current TypeExpr, inheritedParameters map[string]bool) {
		typeParameters := cloneLocals(inheritedParameters)
		for _, parameter := range current.TypeParameters {
			typeParameters[parameter] = true
		}
		if current.Name != "" && !typeParameters[current.Name] {
			references = append(references, lintTypeReference{name: current.Name, span: current.Span})
		}
		for _, argument := range current.Arguments {
			visit(argument, typeParameters)
		}
		if current.Element != nil {
			visit(*current.Element, typeParameters)
		}
		for _, option := range current.Options {
			visit(option, typeParameters)
		}
		for _, field := range current.Fields {
			visit(field.Type, typeParameters)
		}
		for _, parameter := range current.Parameters {
			visit(parameter.Type, typeParameters)
		}
		if current.ReturnType != nil {
			visit(*current.ReturnType, typeParameters)
		}
	}
	visit(typeExpr, nil)

	return references
}

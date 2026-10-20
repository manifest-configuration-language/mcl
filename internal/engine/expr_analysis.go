package engine

func expressionReferences(expression Expr) []string {
	var references []string
	var visit func(Expr, map[string]bool)
	visit = func(expression Expr, locals map[string]bool) {
		if isNilExpr(expression) {
			return
		}
		switch node := expression.(type) {
		case *ReferenceExpr:
			root := referenceRoot(node.Name)
			if !locals[root] {
				references = append(references, root)
			}
		case *OptionalReferenceExpr:
			root := referenceRoot(node.Path)
			if !locals[root] {
				references = append(references, root)
			}
		case *LambdaExpr:
			nested := cloneLocals(locals)
			for _, parameter := range node.Parameters {
				nested[parameter] = true
			}
			visit(node.Body, nested)
		case *ForExpr:
			visit(node.Iterable, locals)
			nested := cloneLocals(locals)
			nested[node.Variable] = true
			visit(node.Body, nested)
		case *ConditionalExpr:
			visit(node.Condition, locals)
			if literal, ok := node.Condition.(*BoolExpr); ok {
				if literal.Value {
					visit(node.Then, locals)
				} else {
					visit(node.Else, locals)
				}

				return
			}
			visit(node.Then, locals)
			visit(node.Else, locals)
		default:
			walkExprChildren(expression, func(child Expr) { visit(child, locals) })
		}
	}
	visit(expression, map[string]bool{})

	return references
}

func cloneLocals(locals map[string]bool) map[string]bool {
	cloned := make(map[string]bool, len(locals)+1)
	for name := range locals {
		cloned[name] = true
	}

	return cloned
}

func walkExprChildren(expression Expr, visit func(Expr)) {
	if isNilExpr(expression) || visit == nil {
		return
	}
	switch node := expression.(type) {
	case *ListExpr:
		for _, child := range node.Items {
			visit(child)
		}
	case *MapExpr:
		for _, entry := range node.Entries {
			visit(entry.KeyExpr)
			visit(entry.Value)
		}
	case *CallExpr:
		visit(node.Callee)
		for _, child := range node.Arguments {
			visit(child)
		}
	case *BinaryExpr:
		visit(node.Left)
		visit(node.Right)
	case *UnaryExpr:
		visit(node.Operand)
	case *IndexExpr:
		visit(node.Base)
		visit(node.Key)
	case *DeferredReferenceExpr:
		visit(node.Target)
		visit(node.Child)
	case *NodeExpr:
		visit(node.Body)
	case *OverlayExpr:
		visit(node.Base)
		visit(node.Patch)
	case *RemovalExpr:
		visit(node.Base)
	}
}

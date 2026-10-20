package syntax

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const nullLiteral = "null"

func parseExpressionSource(source expressionSource) (Expr, error) {
	source = source.withChunkStarts()
	start, end := trimExpressionSourceRange(source, 0, source.Len())
	if start == end {
		return nil, errors.New("expected expression")
	}

	parser := expressionParser{source: source, cursor: expressionSourceCursor{source: source, offset: start}, end: end}
	expression, err := parser.parse(1, nil)
	if err != nil {
		return nil, err
	}
	if _, ok := parser.peek(0); ok {
		return nil, fmt.Errorf("unsupported expression %q", source.Text(start, end))
	}

	return expression, nil
}

func parseExpressionSourceRange(source expressionSource, start, end int) (Expr, error) {
	start, end = trimExpressionSourceRange(source, start, end)
	if start == end {
		return nil, errors.New("expected expression")
	}
	source = source.withChunkStarts()
	parser := expressionParser{source: source, cursor: expressionSourceCursor{source: source, offset: start}, end: end}
	expression, err := parser.parse(1, nil)
	if err != nil {
		return nil, err
	}
	if _, ok := parser.peek(0); ok {
		return nil, fmt.Errorf("unsupported expression %q", source.Text(start, end))
	}

	return expression, nil
}

type expressionParser struct {
	source expressionSource
	cursor expressionSourceCursor
	end    int
}

func (parser *expressionParser) peek(ahead int) (sourceLexeme, bool) {
	cursor := parser.cursor
	for index := range ahead + 1 {
		lexeme, ok := cursor.next()
		if !ok || lexeme.start >= parser.end || lexeme.end > parser.end {
			return sourceLexeme{}, false
		}
		if index == ahead {
			return lexeme, true
		}
	}

	return sourceLexeme{}, false
}

func (parser *expressionParser) take() (sourceLexeme, bool) {
	lexeme, ok := parser.peek(0)
	if !ok {
		return sourceLexeme{}, false
	}
	parser.cursor.offset = lexeme.end

	return lexeme, true
}

func (parser *expressionParser) lexemeText(lexeme sourceLexeme) string {
	return parser.source.Text(lexeme.start, lexeme.end)
}

func (parser *expressionParser) parse(minimumPrecedence int, stops map[string]bool) (Expr, error) {
	first, ok := parser.peek(0)
	if !ok || stops != nil && stops[parser.lexemeText(first)] {
		return nil, errors.New("expected expression")
	}
	left, err := parser.parsePrefix(stops)
	if err != nil {
		return nil, err
	}
	left, err = parser.parsePostfix(left, stops, false)
	if err != nil {
		return nil, err
	}
	for {
		operatorToken, ok := parser.peek(0)
		if !ok {
			break
		}
		operator, precedence, isOperator := parser.binaryOperator(operatorToken)
		if !isOperator || precedence < minimumPrecedence {
			break
		}
		parser.take()
		right, parseErr := parser.parse(precedence+1, stops)
		if parseErr != nil {
			return nil, parseErr
		}
		left = &BinaryExpr{ExprBase: ExprBase{Span: SourceSpan{Start: left.ExprSpan().Start, End: right.ExprSpan().End}}, Operator: operator, Left: left, Right: right}
	}

	return left, nil
}

func (parser *expressionParser) parsePrefix(stops map[string]bool) (Expr, error) {
	lexeme, ok := parser.take()
	if !ok {
		return nil, errors.New("expected expression")
	}
	text := parser.lexemeText(lexeme)
	start := lexeme.start
	switch {
	case lexeme.kind == sourceLexemeIdentifier && text == "not" && parser.hasWhitespaceAfter(lexeme):
		right, err := parser.parse(8, stops)
		if err != nil {
			return nil, err
		}

		return &UnaryExpr{ExprBase: ExprBase{Span: SourceSpan{Start: parser.source.PhysicalOffset(start), End: right.ExprSpan().End}}, Operator: "not", Operand: right}, nil
	case lexeme.kind == sourceLexemeSymbol && text == "-":
		number, hasNumber := parser.peek(0)
		if !hasNumber || number.kind != sourceLexemeNumber || number.start != lexeme.end {
			return nil, fmt.Errorf("unsupported expression %q", parser.source.Text(start, lexeme.end))
		}
		parser.take()
		expression, err := parseScalarExpression("-" + parser.lexemeText(number))
		if err != nil {
			return nil, err
		}
		setExprSpan(expression, parser.source.PhysicalSpan(start, number.end))

		return expression, nil
	case lexeme.kind == sourceLexemeSymbol && text == "(":
		expression, err := parser.parse(1, map[string]bool{")": true})
		if err != nil {
			return nil, err
		}
		closing, ok := parser.take()
		if !ok || parser.lexemeText(closing) != ")" {
			return nil, errors.New("unmatched opening parenthesis")
		}
		setExprSpan(expression, parser.source.PhysicalSpan(start, closing.end))

		return parser.parsePostfix(expression, stops, true)
	case lexeme.kind == sourceLexemeSymbol && text == "[":
		closing, ok := parser.take()
		if !ok || parser.lexemeText(closing) != "]" {
			return nil, errors.New("only an empty list can be written inline")
		}

		return &EmptyListExpr{ExprBase: ExprBase{Span: parser.source.PhysicalSpan(start, closing.end)}}, nil
	case lexeme.kind == sourceLexemeSymbol && text == "{":
		closing, ok := parser.take()
		if !ok || parser.lexemeText(closing) != "}" {
			return nil, errors.New("only an empty map can be written inline")
		}

		return &EmptyMapExpr{ExprBase: ExprBase{Span: parser.source.PhysicalSpan(start, closing.end)}}, nil
	case lexeme.kind == sourceLexemeLiteral || lexeme.kind == sourceLexemeNumber:
		expression, err := parseScalarExpression(text)
		if err != nil {
			return nil, err
		}
		setExprSpan(expression, parser.source.PhysicalSpan(start, lexeme.end))
		if value, ok := parser.source.literalValue(start, lexeme.end); ok {
			if literal, ok := expression.(*StringExpr); ok {
				literal.Value = value
			}
		}

		return expression, nil
	case lexeme.kind == sourceLexemeIdentifier:
		switch text {
		case nullLiteral, "true", "false":
			expression, err := parseScalarExpression(text)
			if err != nil {
				return nil, err
			}
			setExprSpan(expression, parser.source.PhysicalSpan(start, lexeme.end))

			return expression, nil
		case "fn":
			if open, hasOpen := parser.peek(0); hasOpen && parser.lexemeText(open) == "(" && open.start == lexeme.end {
				return parser.parseLambda(lexeme, stops)
			}
		}
		if !identifier(text) {
			return nil, fmt.Errorf("invalid identifier %q", text)
		}

		return parser.parseReferencePath(lexeme)
	case lexeme.kind == sourceLexemeSymbol && text == ".":
		return parser.parseRootPath(lexeme)
	default:
		return nil, fmt.Errorf("unsupported expression %q", parser.source.Text(start, lexeme.end))
	}
}

func (parser *expressionParser) parseReferencePath(first sourceLexeme) (Expr, error) {
	optional := false
	last := first
	for {
		next, ok := parser.peek(0)
		if !ok || next.start != last.end {
			break
		}
		text := parser.lexemeText(next)
		optional = text == "?"
		if optional {
			dot, hasDot := parser.peek(1)
			if !hasDot || parser.lexemeText(dot) != "." || dot.start != next.end {
				break
			}
			parser.take()
			next = dot
			optional = true
		}
		if parser.lexemeText(next) != "." {
			break
		}
		field, hasField := parser.peek(1)
		if !hasField || field.kind != sourceLexemeIdentifier || field.start != next.end {
			return nil, errors.New("expected a field name after '.'")
		}
		parser.take()
		last, _ = parser.take()
	}
	span := parser.source.PhysicalSpan(first.start, last.end)
	path := parser.source.Text(first.start, last.end)
	if optional {
		return &OptionalReferenceExpr{ExprBase: ExprBase{Span: span}, Path: path}, nil
	}

	return &ReferenceExpr{ExprBase: ExprBase{Span: span}, Name: path}, nil
}

func (parser *expressionParser) parseRootPath(dot sourceLexeme) (Expr, error) {
	field, ok := parser.take()
	if !ok || field.kind != sourceLexemeIdentifier || field.start != dot.end || !identifier(parser.lexemeText(field)) {
		return nil, errors.New("expected a field name after '.'")
	}
	last := field
	for {
		next, ok := parser.peek(0)
		if !ok || parser.lexemeText(next) != "." || next.start != last.end {
			break
		}
		field, hasField := parser.peek(1)
		if !hasField || field.kind != sourceLexemeIdentifier || field.start != next.end {
			return nil, errors.New("expected a field name after '.'")
		}
		parser.take()
		last, _ = parser.take()
	}

	return &PathExpr{ExprBase: ExprBase{Span: parser.source.PhysicalSpan(dot.start, last.end)}, Path: parser.source.Text(dot.start, last.end)}, nil
}

func (parser *expressionParser) parseLambda(start sourceLexeme, stops map[string]bool) (Expr, error) {
	open, ok := parser.take()
	if !ok || parser.lexemeText(open) != "(" {
		return nil, errors.New("expected fn(parameter, ...) => expression")
	}
	var parameters []string
	var parameterSpans []SourceSpan
	seen := make(map[string]bool)
	for {
		token, hasToken := parser.peek(0)
		if !hasToken {
			return nil, errors.New("expected ')' in function parameters")
		}
		if parser.lexemeText(token) == ")" {
			parser.take()

			break
		}
		parameter, hasParameter := parser.take()
		if !hasParameter || parameter.kind != sourceLexemeIdentifier {
			return nil, errors.New("expected a lambda parameter")
		}
		name := parser.lexemeText(parameter)
		if !identifier(name) {
			return nil, fmt.Errorf("invalid lambda parameter %q", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate lambda parameter %q", name)
		}
		seen[name] = true
		parameters = append(parameters, name)
		parameterSpans = append(parameterSpans, parser.source.PhysicalSpan(parameter.start, parameter.end))
		separator, hasSeparator := parser.take()
		if !hasSeparator {
			return nil, errors.New("expected ')' in function parameters")
		}
		switch parser.lexemeText(separator) {
		case ",":
			if closing, hasClose := parser.peek(0); hasClose && parser.lexemeText(closing) == ")" {
				parser.take()

				goto parametersParsed
			}
		case ")":
			goto parametersParsed
		default:
			return nil, errors.New("expected ',' or ')' in function parameters")
		}
	}

parametersParsed:
	arrow, ok := parser.take()
	if !ok || parser.lexemeText(arrow) != "=>" {
		return nil, errors.New("expected '=>' after lambda parameters")
	}
	body, err := parser.parse(1, stops)
	if err != nil {
		return nil, err
	}

	return &LambdaExpr{ExprBase: ExprBase{Span: SourceSpan{Start: parser.source.PhysicalOffset(start.start), End: body.ExprSpan().End}}, Parameters: parameters, ParameterSpans: parameterSpans, Body: body}, nil
}

func (parser *expressionParser) parsePostfix(base Expr, stops map[string]bool, grouped bool) (Expr, error) {
	postfixStarted := grouped
	for {
		token, ok := parser.peek(0)
		if !ok || stops != nil && stops[parser.lexemeText(token)] {
			return base, nil
		}
		switch parser.lexemeText(token) {
		case "(":
			callee, isReference := base.(*ReferenceExpr)
			if !isReference {
				return nil, fmt.Errorf("invalid function name %q", base)
			}
			open, _ := parser.take()
			arguments, names, closing, err := parser.parseCallArguments(open)
			if err != nil {
				return nil, err
			}
			call := &CallExpr{ExprBase: ExprBase{Span: SourceSpan{Start: callee.ExprSpan().Start, End: parser.source.PhysicalOffset(closing.end)}}, Delimiters: ExprCallDelimiters{Open: parser.source.PhysicalSpan(open.start, open.end), Close: parser.source.PhysicalSpan(closing.start, closing.end)}, Callee: callee, Arguments: arguments, ArgumentNames: names}
			if callee.Name == "ref" {
				for _, name := range names {
					if name != "" {
						return nil, errors.New("ref() does not accept named arguments")
					}
				}
				var target, child Expr
				if len(arguments) > 0 {
					target = arguments[0]
				}
				if len(arguments) > 1 {
					child = arguments[1]
				}
				base = &DeferredReferenceExpr{ExprBase: call.ExprBase, Delimiters: call.Delimiters, Target: target, Child: child}
			} else {
				base = call
			}
			postfixStarted = true
		case "[":
			open, _ := parser.take()
			key, err := parser.parse(1, map[string]bool{"]": true})
			if err != nil {
				return nil, err
			}
			closing, ok := parser.take()
			if !ok || parser.lexemeText(closing) != "]" {
				return nil, errors.New("unmatched opening bracket")
			}
			base = &IndexExpr{ExprBase: ExprBase{Span: SourceSpan{Start: base.ExprSpan().Start, End: parser.source.PhysicalOffset(closing.end)}}, Base: base, Key: key}
			_ = open
			postfixStarted = true
		case ".":
			if !postfixStarted {
				return base, nil
			}
			parser.take()
			field, ok := parser.take()
			if !ok || field.kind != sourceLexemeIdentifier || !identifier(parser.lexemeText(field)) {
				return nil, errors.New("expected a field name after '.'")
			}
			if reference, isDeferred := base.(*DeferredReferenceExpr); isDeferred {
				reference.Path = append(reference.Path, parser.lexemeText(field))
				reference.Span.End = parser.source.PhysicalOffset(field.end)

				continue
			}
			key := &StringExpr{ExprBase: ExprBase{Span: parser.source.PhysicalSpan(field.start, field.end)}, Value: parser.lexemeText(field)}
			base = &IndexExpr{ExprBase: ExprBase{Span: SourceSpan{Start: base.ExprSpan().Start, End: key.ExprSpan().End}}, Base: base, Key: key}
		default:
			return base, nil
		}
	}
}

func (parser *expressionParser) parseCallArguments(open sourceLexeme) ([]Expr, []string, sourceLexeme, error) {
	var arguments []Expr
	var names []string
	seenNames := make(map[string]bool)
	seenNamed := false
	for {
		token, ok := parser.peek(0)
		if !ok {
			return nil, nil, sourceLexeme{}, errors.New("unmatched opening parenthesis")
		}
		if parser.lexemeText(token) == ")" {
			closing, _ := parser.take()

			return arguments, names, closing, nil
		}
		name := ""
		if token.kind == sourceLexemeIdentifier {
			separator, hasSeparator := parser.peek(1)
			if hasSeparator && parser.lexemeText(separator) == "=" && token.end <= separator.start {
				name = parser.lexemeText(token)
				if !identifier(name) {
					return nil, nil, sourceLexeme{}, fmt.Errorf("invalid argument name %q", name)
				}
				if seenNames[name] {
					return nil, nil, sourceLexeme{}, fmt.Errorf("duplicate named argument %q", name)
				}
				seenNames[name] = true
				seenNamed = true
				parser.take()
				parser.take()
			} else if seenNamed {
				return nil, nil, sourceLexeme{}, errors.New("positional arguments must precede named arguments")
			}
		} else if seenNamed {
			return nil, nil, sourceLexeme{}, errors.New("positional arguments must precede named arguments")
		}
		argument, err := parser.parse(1, map[string]bool{",": true, ")": true})
		if err != nil {
			return nil, nil, sourceLexeme{}, err
		}
		arguments = append(arguments, argument)
		names = append(names, name)
		next, ok := parser.take()
		if !ok {
			return nil, nil, sourceLexeme{}, errors.New("unmatched opening parenthesis")
		}
		switch parser.lexemeText(next) {
		case ")":
			if len(arguments) > 0 && strings.Contains(parser.source.Text(open.end, next.start), "\n") {
				return nil, nil, sourceLexeme{}, errors.New("multiline function calls require a trailing comma after every argument")
			}

			return arguments, names, next, nil
		case ",":
			if closing, ok := parser.peek(0); ok && parser.lexemeText(closing) == ")" {
				closing, _ := parser.take()

				return arguments, names, closing, nil
			}
		default:
			return nil, nil, sourceLexeme{}, fmt.Errorf("expected ',' or ')' after argument in call at %d", parser.source.PhysicalOffset(open.start))
		}
	}
}

func (parser *expressionParser) binaryOperator(lexeme sourceLexeme) (string, int, bool) {
	operator := parser.lexemeText(lexeme)
	precedence := 0
	switch operator {
	case "or":
		precedence = 1
	case "??":
		precedence = 2
	case "and":
		precedence = 3
	case "==", "!=":
		precedence = 4
	case "<", "<=", ">", ">=", "in":
		precedence = 5
	case "+", "-":
		precedence = 6
	case "*", "/", "%":
		precedence = 7
	default:
		return "", 0, false
	}
	if lexeme.kind == sourceLexemeIdentifier && !parser.hasWhitespaceAround(lexeme) {
		return "", 0, false
	}

	return operator, precedence, true
}

func (parser *expressionParser) hasWhitespaceAfter(lexeme sourceLexeme) bool {
	next, ok := parser.peek(0)

	return ok && next.start > lexeme.end
}

func (parser *expressionParser) hasWhitespaceAround(lexeme sourceLexeme) bool {
	previousSpace := lexeme.start > 0 && isSpace(rune(parser.source.ByteAt(lexeme.start-1)))
	nextSpace := lexeme.end < parser.source.Len() && isSpace(rune(parser.source.ByteAt(lexeme.end)))

	return previousSpace && nextSpace
}

func parseScalarExpression(text string) (Expr, error) {
	if strings.HasPrefix(text, `"""`) {
		value, err := multilineString(text)
		if err != nil {
			return nil, err
		}

		return &StringExpr{Value: value}, nil
	}
	switch text {
	case nullLiteral:
		return &NullExpr{}, nil
	case "true":
		return &BoolExpr{Value: true}, nil
	case "false":
		return &BoolExpr{}, nil
	case "{}":
		return &EmptyMapExpr{}, nil
	case "[]":
		return &EmptyListExpr{}, nil
	}
	if len(text) > 0 && text[0] == '"' {
		value, err := strconv.Unquote(text)
		if err != nil {
			return nil, fmt.Errorf("invalid string: %w", err)
		}

		return &StringExpr{Value: value}, nil
	}
	if numberPattern.MatchString(text) {
		if containsDecimal(text) {
			if err := validateRuntimeDecimalText(text); err != nil {
				return nil, fmt.Errorf("invalid Decimal literal: %w", err)
			}
		} else if err := validateRuntimeIntegerText(text); err != nil {
			return nil, fmt.Errorf("invalid Integer literal: %w", err)
		}
		if containsDecimal(text) {
			return &DecimalExpr{Value: text}, nil
		}

		return &IntegerExpr{Value: text}, nil
	}

	return nil, fmt.Errorf("unsupported expression %q", text)
}

func trimExpressionSourceRange(source expressionSource, start, end int) (int, int) {
	for start < end && isSpace(rune(source.ByteAt(start))) {
		start++
	}
	for end > start && isSpace(rune(source.ByteAt(end-1))) {
		end--
	}

	return start, end
}

func setExprSpan(expression Expr, span SourceSpan) {
	switch node := expression.(type) {
	case *NullExpr:
		node.Span = span
	case *BoolExpr:
		node.Span = span
	case *IntegerExpr:
		node.Span = span
	case *DecimalExpr:
		node.Span = span
	case *StringExpr:
		node.Span = span
	case *EmptyListExpr:
		node.Span = span
	case *EmptyMapExpr:
		node.Span = span
	case *ListExpr:
		node.Span = span
	case *MapExpr:
		node.Span = span
	case *ReferenceExpr:
		node.Span = span
	case *OptionalReferenceExpr:
		node.Span = span
	case *PathExpr:
		node.Span = span
	case *CallExpr:
		node.Span = span
	case *BinaryExpr:
		node.Span = span
	case *UnaryExpr:
		node.Span = span
	case *IndexExpr:
		node.Span = span
	case *LambdaExpr:
		node.Span = span
	case *DeferredReferenceExpr:
		node.Span = span
	case *ConditionalExpr:
		node.Span = span
	case *ForExpr:
		node.Span = span
	case *NodeExpr:
		node.Span = span
	case *OverlayExpr:
		node.Span = span
	case *RemovalExpr:
		node.Span = span
	}
}

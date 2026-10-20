package syntax

import (
	"errors"
	"fmt"
)

func (p *Parser) blockWithExpression(indent int) (parsedValue, error) {
	lineToken, firstToken, err := p.beginIndentedBlock(indent)
	if err != nil {
		return parsedValue{}, err
	}
	var parsed parsedValue
	if _, isListItem := p.sourceListItemSourceSpan(firstToken); isListItem {
		parsed, err = p.listWithExpression(indent)
	} else {
		parsed, err = p.mappingWithExpression(indent)
	}
	if err != nil {
		return parsedValue{}, err
	}
	if err := p.endIndentedBlock(lineToken); err != nil {
		return parsedValue{}, err
	}

	return parsed, nil
}

func (p *Parser) beginIndentedBlock(indent int) (sourceToken, sourceToken, error) {
	lineToken, hasLine, err := p.peekParserToken()
	if err != nil {
		return sourceToken{}, sourceToken{}, err
	}
	if !hasLine {
		return sourceToken{}, sourceToken{}, errors.New("expected an indented value")
	}
	if lineToken.indent != indent {
		return sourceToken{}, sourceToken{}, fmt.Errorf("line %d: invalid indentation", lineToken.lineNumber)
	}
	if !p.consumeToken(sourceTokenIndent) {
		return sourceToken{}, sourceToken{}, fmt.Errorf("line %d: expected indentation before block", lineToken.lineNumber)
	}
	firstToken, ok, err := p.peekParserToken()
	if err != nil {
		return sourceToken{}, sourceToken{}, err
	}
	if !ok || firstToken.span != lineToken.span {
		return sourceToken{}, sourceToken{}, fmt.Errorf("line %d: expected source line after indentation", lineToken.lineNumber)
	}

	return lineToken, firstToken, nil
}

func (p *Parser) endIndentedBlock(lineToken sourceToken) error {
	if token, ok := p.peekToken(); ok && token.kind == sourceTokenNewline {
		_ = p.consumeToken(sourceTokenNewline)
	}
	if !p.consumeToken(sourceTokenDedent) {
		return fmt.Errorf("line %d: expected dedent after block", lineToken.lineNumber)
	}

	return nil
}

func (p *Parser) mappingWithExpression(indent int) (parsedValue, error) {
	firstToken, hasFirstLine, err := p.peekParserToken()
	if err != nil {
		return parsedValue{}, err
	}
	if !hasFirstLine {
		return parsedValue{}, errors.New("expected an indented value")
	}

	return p.mappingWithExpressionFromLine(indent, firstToken, false, nil)
}

func (p *Parser) mappingWithExpressionFromLine(indent int, firstToken sourceToken, firstLineConsumed bool, firstLineSpan *SourceSpan) (parsedValue, error) {
	allowIndent := firstLineConsumed
	virtualIndentConsumed := false
	expressionEntries := make([]ExprMapEntry, 0)
	keys := make(map[string]bool)
	useFirstLine := firstLineConsumed
	for {
		lineToken := firstToken
		hasLine := useFirstLine
		consumedIndent := false
		lineConsumed := firstLineConsumed && useFirstLine
		if !useFirstLine {
			var boundaryErr error
			lineToken, hasLine, consumedIndent, boundaryErr = p.nextBlockEntry(indent, allowIndent)
			if boundaryErr != nil {
				return parsedValue{}, boundaryErr
			}
		}
		useFirstLine = false
		virtualIndentConsumed = virtualIndentConsumed || consumedIndent
		if !hasLine || p.stopElseIndent == indent && p.isExactElseHeader(lineToken) {
			break
		}
		if !lineConsumed {
			if _, isListItem := p.sourceListItemSourceSpan(lineToken); isListItem {
				return parsedValue{}, fmt.Errorf("line %d: list item is not valid in a map", lineToken.lineNumber)
			}
		}
		lineSpan := lineToken.span
		if lineConsumed && firstLineSpan != nil {
			lineSpan = *firstLineSpan
		}
		header, ok, err := p.collectionEntryHeader(lineSpan)
		if !ok {
			return parsedValue{}, fmt.Errorf("line %d: expected key: value", lineToken.lineNumber)
		}
		if err != nil {
			return parsedValue{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
		}
		if !header.dynamic {
			if keys[header.key] {
				return parsedValue{}, fmt.Errorf("line %d: duplicate key %q", lineToken.lineNumber, header.key)
			}
			keys[header.key] = true
		}
		if !lineConsumed {
			if err := p.consumeLine(lineToken); err != nil {
				return parsedValue{}, err
			}
		}
		var parsed parsedValue
		if header.value.Start == header.value.End {
			childToken, hasChild, childErr := p.peekParserToken()
			if childErr != nil {
				return parsedValue{}, childErr
			}
			if !hasChild || childToken.indent <= indent {
				return parsedValue{}, fmt.Errorf("line %d: expected an indented value", lineToken.lineNumber)
			}
			parsed, err = p.indentedValueWithExpression(lineToken.lineNumber)
		} else {
			continued, continueErr := p.continuedExpressionSourceSpan(lineToken, header.value, indent)
			if continueErr != nil {
				return parsedValue{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, continueErr)
			}
			parsed, err = p.valueExpressionWithContinued(continued, indent, lineToken.lineNumber)
			if err == nil && !parsed.isOverlay() {
				childToken, hasChild, childErr := p.peekParserToken()
				if childErr != nil {
					return parsedValue{}, childErr
				}
				if hasChild && childToken.indent > indent {
					return parsedValue{}, fmt.Errorf("line %d: scalar value cannot have child entries", lineToken.lineNumber)
				}
			}
		}
		if err != nil {
			return parsedValue{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
		}
		expressionEntries = append(expressionEntries, ExprMapEntry{Key: header.key, KeyExpr: header.keyExpr, KeySpan: header.keySpan, Value: parsed.Expression})
	}
	if virtualIndentConsumed {
		if token, ok := p.peekToken(); ok && token.kind == sourceTokenNewline {
			p.tokenIndex++
		}
		if !p.consumeToken(sourceTokenDedent) {
			return parsedValue{}, fmt.Errorf("line %d: expected dedent after inline map", firstToken.lineNumber)
		}
	}
	span := SourceSpan{}
	if len(expressionEntries) > 0 {
		lastToken, ok := p.lastConsumedSourceToken()
		if !ok {
			lastToken = firstToken
		}
		spanStart := firstToken.span.Start
		if firstLineConsumed && firstLineSpan != nil {
			spanStart = firstLineSpan.Start
		}
		span = SourceSpan{Start: spanStart, End: lastToken.span.End}
	}

	return parsedValue{Expression: &MapExpr{ExprBase: ExprBase{Span: span}, Entries: expressionEntries}}, nil
}

func (p *Parser) listWithExpression(indent int) (parsedValue, error) {
	firstToken, hasFirstLine, err := p.peekParserToken()
	if err != nil {
		return parsedValue{}, err
	}
	if !hasFirstLine {
		return parsedValue{}, errors.New("expected an indented value")
	}
	expressionItems := make([]Expr, 0)
	for {
		lineToken, hasLine, _, boundaryErr := p.nextBlockEntry(indent, false)
		if boundaryErr != nil {
			return parsedValue{}, boundaryErr
		}
		if !hasLine || p.stopElseIndent == indent && p.isExactElseHeader(lineToken) {
			break
		}
		itemSpan, isListItem := p.sourceListItemSourceSpan(lineToken)
		if !isListItem {
			return parsedValue{}, fmt.Errorf("line %d: expected - before list item", lineToken.lineNumber)
		}
		if err := p.consumeLine(lineToken); err != nil {
			return parsedValue{}, err
		}
		var parsed parsedValue
		var err error
		if itemSpan.Start == itemSpan.End {
			childToken, hasChild, childErr := p.peekParserToken()
			if childErr != nil {
				return parsedValue{}, childErr
			}
			if !hasChild || childToken.indent <= indent {
				return parsedValue{}, fmt.Errorf("line %d: expected an indented list item", lineToken.lineNumber)
			}
			parsed, err = p.indentedValueWithExpression(lineToken.lineNumber)
		} else if p.hasPairSourceSpan(itemSpan) {
			mapIndent := indent + 2
			parsed, err = p.mappingWithExpressionFromLine(mapIndent, lineToken, true, &itemSpan)
		} else {
			continued, continueErr := p.continuedExpressionSourceSpan(lineToken, itemSpan, indent)
			if continueErr != nil {
				return parsedValue{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, continueErr)
			}
			parsed, err = p.valueExpressionWithContinued(continued, indent, lineToken.lineNumber)
			if err == nil && !parsed.isOverlay() {
				childToken, hasChild, childErr := p.peekParserToken()
				if childErr != nil {
					return parsedValue{}, childErr
				}
				if hasChild && childToken.indent > indent {
					return parsedValue{}, fmt.Errorf("line %d: scalar list item cannot have child entries", lineToken.lineNumber)
				}
			}
		}
		if err != nil {
			return parsedValue{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
		}
		expressionItems = append(expressionItems, parsed.Expression)
	}
	span := SourceSpan{}
	if len(expressionItems) > 0 {
		lastToken, ok := p.lastConsumedSourceToken()
		if !ok {
			lastToken = firstToken
		}
		span = SourceSpan{Start: firstToken.span.Start, End: lastToken.span.End}
	}

	return parsedValue{Expression: &ListExpr{ExprBase: ExprBase{Span: span}, Items: expressionItems}}, nil
}

func (p *Parser) indentedValueWithExpression(line int) (parsedValue, error) {
	currentToken, ok, err := p.peekParserToken()
	if err != nil {
		return parsedValue{}, err
	}
	if !ok {
		return parsedValue{}, fmt.Errorf("line %d: expected an indented value", line)
	}
	if p.isSourceHeader(currentToken, "node") {
		if err := p.consumeLine(currentToken); err != nil {
			return parsedValue{}, err
		}

		return p.nodeWithSourceToken(currentToken, currentToken.indent)
	}
	if p.sourceLineStartsWithKeyword(currentToken, "if") || p.sourceLineStartsWithKeyword(currentToken, "for") {
		if err := p.consumeLine(currentToken); err != nil {
			return parsedValue{}, err
		}
		continued, err := p.continuedExpressionSourceSpan(currentToken, currentToken.span, currentToken.indent)
		if err != nil {
			return parsedValue{}, err
		}

		return p.valueExpressionWithContinued(continued, currentToken.indent, currentToken.lineNumber)
	}
	_, isListItem := p.sourceListItemSourceSpan(currentToken)
	if isListItem {
		return p.blockWithExpression(currentToken.indent)
	}
	if !p.hasPairSourceSpan(currentToken.span) {
		if err := p.consumeLine(currentToken); err != nil {
			return parsedValue{}, err
		}
		continued, err := p.continuedExpressionSourceSpan(currentToken, currentToken.span, currentToken.indent)
		if err != nil {
			return parsedValue{}, err
		}

		return p.valueExpressionWithContinued(continued, currentToken.indent, currentToken.lineNumber)
	}

	return p.blockWithExpression(currentToken.indent)
}

func (p *Parser) valueExpressionWithContinued(source continuedSource, parentIndent, line int) (parsedValue, error) {
	previous := p.continued
	p.continued = &source
	defer func() { p.continued = previous }()
	view := source.expressionSource(p.source, p.literals)
	if view.startsWithKeyword("for") {
		if parsed, handled, err := p.forWithExpressionSource(view, parentIndent, line); handled || err != nil {
			return parsed, err
		}
	}
	if view.startsWithKeyword("if") {
		if parsed, handled, err := p.ifWithExpressionSource(view, parentIndent, line); handled || err != nil {
			return parsed, err
		}
	}
	if expressionSourceHasHeaderSuffix(view, "without") {
		if parsed, handled, err := p.withWithoutExpressionSource(view, "without", parentIndent, line); handled || err != nil {
			return parsed, err
		}
	}
	if expressionSourceHasHeaderSuffix(view, "with") {
		if parsed, handled, err := p.withWithoutExpressionSource(view, "with", parentIndent, line); handled || err != nil {
			return parsed, err
		}
	}
	if start, end, ok := expressionSourceHeader(view, "node"); ok {
		return p.nodeWithSpan(view.PhysicalSpan(start, end), parentIndent, line)
	}
	expression, err := parseExpressionSource(view)
	if err != nil {
		return parsedValue{}, err
	}

	return parsedValue{Expression: expression}, nil
}

func (p *Parser) parseContinuedExpression(source continuedSource) (Expr, error) {
	previous := p.continued
	p.continued = &source
	defer func() { p.continued = previous }()
	expression, err := parseExpressionSource(source.expressionSource(p.source, p.literals))
	if err != nil {
		return nil, err
	}

	return expression, nil
}

func (p *Parser) nodeWithSourceToken(header sourceToken, parentIndent int) (parsedValue, error) {
	defer func() { p.continued = nil }()

	lexemes, _, ok := p.sourceLineLexemes(header)
	if !p.isSourceHeader(header, "node") || !ok || len(lexemes) != 2 {
		return parsedValue{}, fmt.Errorf("line %d: expected node", header.lineNumber)
	}
	keyword, colon := lexemes[0], lexemes[1]
	span := SourceSpan{Start: keyword.span.Start, End: colon.span.End}
	body, err := p.nodeBody(parentIndent, header.lineNumber)
	if err != nil {
		return parsedValue{}, err
	}
	expression := &NodeExpr{ExprBase: ExprBase{Span: span}, Body: body}

	return parsedValue{Expression: expression}, nil
}

func (p *Parser) nodeWithSpan(span SourceSpan, parentIndent, line int) (parsedValue, error) {
	defer func() { p.continued = nil }()

	body, err := p.nodeBody(parentIndent, line)
	if err != nil {
		return parsedValue{}, err
	}
	expression := &NodeExpr{ExprBase: ExprBase{Span: span}, Body: body}

	return parsedValue{Expression: expression}, nil
}

func (p *Parser) nodeBody(parentIndent, line int) (Expr, error) {
	bodyLine, ok, err := p.peekParserToken()
	if err != nil {
		return nil, err
	}
	if !ok || bodyLine.indent <= parentIndent {
		return nil, fmt.Errorf("line %d: expected an indented node value", line)
	}
	body, err := p.branchValueWithExpression(bodyLine.indent, line)
	if err != nil {
		return nil, err
	}

	return body.Expression, nil
}

func (p *Parser) forWithExpressionSource(source expressionSource, parentIndent, line int) (parsedValue, bool, error) {
	source = source.withChunkStarts()
	start, end := trimExpressionSourceRange(source, 0, source.Len())
	cursor := expressionSourceCursor{source: source, offset: start}
	lexemes := make([]sourceLexeme, 0, 8)
	parenDepth, bracketDepth, braceDepth := 0, 0, 0
	colonIndex := -1
	for {
		lexeme, ok := cursor.next()
		if !ok || lexeme.start >= end {
			break
		}
		lexemes = append(lexemes, lexeme)
		if lexeme.kind != sourceLexemeSymbol {
			continue
		}
		switch source.Text(lexeme.start, lexeme.end) {
		case "(":
			parenDepth++
		case ")":
			parenDepth--
		case "[":
			bracketDepth++
		case "]":
			bracketDepth--
		case "{":
			braceDepth++
		case "}":
			braceDepth--
		case ":":
			if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
				colonIndex = len(lexemes) - 1
			}
		}
	}
	if len(lexemes) == 0 || lexemes[0].kind != sourceLexemeIdentifier || source.Text(lexemes[0].start, lexemes[0].end) != "for" {
		return parsedValue{}, false, nil
	}
	if colonIndex < 4 || colonIndex != len(lexemes)-1 {
		return parsedValue{}, true, fmt.Errorf("line %d: expected for name in values", line)
	}
	nameToken, inToken, colonToken := lexemes[1], lexemes[2], lexemes[colonIndex]
	if nameToken.kind != sourceLexemeIdentifier || inToken.kind != sourceLexemeIdentifier || nameToken.end == inToken.start || source.Text(inToken.start, inToken.end) != "in" || inToken.end == lexemes[3].start {
		return parsedValue{}, true, fmt.Errorf("line %d: expected for name in values", line)
	}
	name := source.Text(nameToken.start, nameToken.end)
	if !identifier(name) {
		return parsedValue{}, true, fmt.Errorf("line %d: expected for name in values", line)
	}
	previous := p.continued
	p.continued = nil
	defer func() { p.continued = previous }()
	iterableStart, iterableEnd := trimExpressionSourceRange(source, inToken.end, colonToken.start)
	if iterableStart == iterableEnd {
		return parsedValue{}, true, fmt.Errorf("line %d: expected for name in values", line)
	}
	iterable, err := parseExpressionSourceRange(source, iterableStart, iterableEnd)
	if err != nil {
		return parsedValue{}, true, err
	}
	bodyLine, ok, err := p.peekParserToken()
	if err != nil {
		return parsedValue{}, true, err
	}
	if !ok || bodyLine.indent <= parentIndent {
		return parsedValue{}, true, fmt.Errorf("line %d: expected an indented for body", line)
	}
	body, err := p.branchValueWithExpression(bodyLine.indent, line)
	if err != nil {
		return parsedValue{}, true, err
	}
	expression := &ForExpr{
		ExprBase: ExprBase{Span: source.PhysicalSpan(start, end)},
		Variable: name, VariableSpan: source.PhysicalSpan(nameToken.start, nameToken.end),
		Iterable: iterable, Body: body.Expression,
	}

	return parsedValue{Expression: expression}, true, nil
}

func (p *Parser) ifWithExpressionSource(source expressionSource, parentIndent, line int) (parsedValue, bool, error) {
	source = source.withChunkStarts()
	start, end := trimExpressionSourceRange(source, 0, source.Len())
	cursor := expressionSourceCursor{source: source, offset: start}
	lexemes := make([]sourceLexeme, 0, 8)
	parenDepth, bracketDepth, braceDepth := 0, 0, 0
	colonIndex := -1
	for {
		lexeme, ok := cursor.next()
		if !ok || lexeme.start >= end {
			break
		}
		lexemes = append(lexemes, lexeme)
		if lexeme.kind != sourceLexemeSymbol {
			continue
		}
		switch source.Text(lexeme.start, lexeme.end) {
		case "(":
			parenDepth++
		case ")":
			parenDepth--
		case "[":
			bracketDepth++
		case "]":
			bracketDepth--
		case "{":
			braceDepth++
		case "}":
			braceDepth--
		case ":":
			if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
				colonIndex = len(lexemes) - 1
			}
		}
	}
	if len(lexemes) < 2 || lexemes[0].kind != sourceLexemeIdentifier || source.Text(lexemes[0].start, lexemes[0].end) != "if" || lexemes[0].end == lexemes[1].start {
		return parsedValue{}, false, nil
	}
	if colonIndex < 1 || colonIndex != len(lexemes)-1 {
		return parsedValue{}, true, fmt.Errorf("line %d: expected if condition", line)
	}
	conditionStart, conditionEnd := trimExpressionSourceRange(source, lexemes[1].start, lexemes[colonIndex].start)
	condition, err := parseExpressionSourceRange(source, conditionStart, conditionEnd)
	if err != nil {
		return parsedValue{}, true, err
	}
	p.continued = nil
	bodyToken, ok, err := p.peekParserToken()
	if err != nil {
		return parsedValue{}, true, err
	}
	if !ok || bodyToken.indent <= parentIndent {
		return parsedValue{}, true, fmt.Errorf("line %d: expected an indented if branch", line)
	}
	branchIndent := bodyToken.indent
	p.stopElseIndent = parentIndent
	thenValue, err := p.branchValueWithExpression(branchIndent, line)
	p.stopElseIndent = 0
	if err != nil {
		return parsedValue{}, true, err
	}
	elseToken, ok, err := p.peekParserToken()
	if err == nil && !ok {
		elseToken, ok, err = p.peekParserTokenAfterDedent()
	}
	if err != nil {
		return parsedValue{}, true, err
	}
	if !ok || elseToken.indent != parentIndent || !p.isSourceHeader(elseToken, "else") {
		return parsedValue{}, true, fmt.Errorf("line %d: expected else: branch", line)
	}
	if err := p.consumeLine(elseToken); err != nil {
		return parsedValue{}, true, err
	}
	elseBranchToken, ok, err := p.peekParserToken()
	if err != nil {
		return parsedValue{}, true, err
	}
	if !ok || elseBranchToken.indent <= parentIndent {
		return parsedValue{}, true, fmt.Errorf("line %d: expected an indented else branch", line)
	}
	elseValue, err := p.branchValueWithExpression(elseBranchToken.indent, line)
	if err != nil {
		return parsedValue{}, true, err
	}
	expression := &ConditionalExpr{ExprBase: ExprBase{Span: source.PhysicalSpan(start, end)}, Condition: condition, Then: thenValue.Expression, Else: elseValue.Expression}

	return parsedValue{Expression: expression}, true, nil
}

package syntax

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
)

type pairSourceTokens struct {
	key       []sourceToken
	value     []sourceToken
	separator SourceSpan
	found     bool
}

func (p *Parser) scanPairSourceTokens(bounds SourceSpan) pairSourceTokens {
	cursor := p.sourceSpanLexemeCursor(bounds)
	parenDepth, bracketDepth := 0, 0
	pair := pairSourceTokens{}
	for {
		token, ok := cursor.next()
		if !ok {
			break
		}
		if pair.found {
			pair.value = append(pair.value, token)

			continue
		}
		if token.lexeme.kind != sourceLexemeSymbol {
			pair.key = append(pair.key, token)

			continue
		}
		switch p.sourceTokenLexemeText(token) {
		case "(":
			parenDepth++
		case ")":
			if parenDepth > 0 {
				parenDepth--
			}
		case "[":
			bracketDepth++
		case "]":
			if bracketDepth > 0 {
				bracketDepth--
			}
		case ":":
			if parenDepth == 0 && bracketDepth == 0 {
				pair.separator = token.span
				pair.found = true

				continue
			}
		}
		pair.key = append(pair.key, token)
	}

	return pair
}

func (p *Parser) hasPairSourceSpan(span SourceSpan) bool {
	return p.scanPairSourceTokens(span).found
}

func (p *Parser) collectionEntryHeader(bounds SourceSpan) (collectionEntryHeader, bool, error) {
	keySpan, valueSpan, keyTokens, ok := p.collectionEntryHeaderTokens(bounds)
	if !ok {
		return collectionEntryHeader{}, false, nil
	}
	keyExpr, dynamic, err := p.parseDynamicKeyTokens(keySpan, keyTokens)
	if err != nil {
		return collectionEntryHeader{}, true, err
	}
	header := collectionEntryHeader{
		keyExpr: keyExpr,
		keySpan: keySpan,
		value:   valueSpan,
		dynamic: dynamic,
	}
	if !dynamic {
		header.key, err = p.parseKeySourceTokens(keySpan, keyTokens)
		if err != nil {
			return collectionEntryHeader{}, true, err
		}
	}

	return header, true, nil
}

func (p *Parser) collectionEntryHeaderTokens(bounds SourceSpan) (SourceSpan, SourceSpan, []sourceToken, bool) {
	pair := p.scanPairSourceTokens(bounds)
	if !pair.found {
		return SourceSpan{}, SourceSpan{}, nil, false
	}
	keySpan := SourceSpan{Start: bounds.Start, End: bounds.Start}
	if len(pair.key) > 0 {
		keySpan = SourceSpan{Start: pair.key[0].span.Start, End: pair.key[len(pair.key)-1].span.End}
		keySpan = p.trimSourceSpan(keySpan)
	}
	keyTokens := filterSourceTokensBySpan(pair.key, keySpan)
	valueSpan := SourceSpan{Start: pair.separator.End, End: pair.separator.End}
	if len(pair.value) > 0 {
		valueSpan = SourceSpan{Start: pair.value[0].span.Start, End: pair.value[len(pair.value)-1].span.End}
	}

	return keySpan, valueSpan, keyTokens, true
}

func filterSourceTokensBySpan(tokens []sourceToken, bounds SourceSpan) []sourceToken {
	filtered := tokens[:0]
	for _, token := range tokens {
		if token.span.End <= bounds.Start || token.span.Start < bounds.Start || token.span.End > bounds.End {
			continue
		}
		filtered = append(filtered, token)
	}

	return filtered
}

func (p *Parser) sourceListItemSourceSpan(lineToken sourceToken) (SourceSpan, bool) {
	lineLexemes, lineEnd, ok := p.sourceListItemLineLexemes(lineToken)
	if !ok || len(lineLexemes) == 0 {
		return SourceSpan{}, false
	}
	marker := lineLexemes[0]
	if marker.lexeme.kind != sourceLexemeSymbol || p.sourceTokenLexemeText(marker) != "-" {
		return SourceSpan{}, false
	}
	var itemTokens []sourceToken
	if len(lineLexemes) > 1 {
		next := lineLexemes[1]
		if marker.span.End == next.span.Start || marker.span.End >= len(p.source) || p.source[marker.span.End] != ' ' {
			return SourceSpan{}, false
		}
		itemTokens = lineLexemes[1:]
	}
	itemSpan := SourceSpan{Start: lineEnd, End: lineEnd}
	if len(itemTokens) > 0 {
		itemSpan = SourceSpan{Start: itemTokens[0].span.Start, End: itemTokens[len(itemTokens)-1].span.End}
	}

	return itemSpan, true
}

func (p *Parser) sourceListItemLineLexemes(lineToken sourceToken) ([]sourceToken, int, bool) {
	if lineToken.kind != sourceTokenNewline || lineToken.lineNumber == 0 || lineToken.span.Start < 0 || lineToken.span.End < lineToken.span.Start || lineToken.span.End > len(p.source) {
		return nil, 0, false
	}
	lineEnd := lineToken.span.End
	firstToken := sort.Search(len(p.tokens), func(index int) bool {
		return p.tokens[index].span.Start >= lineToken.span.Start
	})
	lineLexemes := make([]sourceToken, 0)
	for index := firstToken; index < len(p.tokens); index++ {
		token := p.tokens[index]
		if token.span.Start >= lineEnd {
			break
		}
		if token.kind != sourceTokenLexeme || token.lineNumber != lineToken.lineNumber || token.span.End <= lineToken.span.Start || token.span.End > lineEnd {
			continue
		}
		lineLexemes = append(lineLexemes, token)
	}

	return lineLexemes, lineEnd, true
}

func (p *Parser) sourceLineLexemes(lineToken sourceToken) ([]sourceToken, int, bool) {
	if lineToken.kind != sourceTokenNewline || lineToken.lineNumber == 0 || lineToken.span.Start < 0 || lineToken.span.End < lineToken.span.Start || lineToken.span.End > len(p.source) {
		return nil, 0, false
	}
	cursor := p.sourceSpanLexemeCursor(lineToken.span)
	lexemes := make([]sourceToken, 0)
	for {
		lexeme, ok := cursor.next()
		if !ok {
			break
		}
		lexemes = append(lexemes, lexeme)
	}

	return lexemes, lineToken.span.End, true
}

func (p *Parser) parseKeySourceTokens(keySpan SourceSpan, tokens []sourceToken) (string, error) {
	var first sourceToken
	hasFirst := len(tokens) != 0
	if hasFirst {
		first = tokens[0]
	}
	keySource := p.source[keySpan.Start:keySpan.End]
	if hasFirst && first.span.Start == keySpan.Start && keySource[0] == '\'' {
		return "", errors.New("single-quoted strings are not allowed")
	}
	if hasFirst && first.lexeme.kind == sourceLexemeLiteral && first.span.Start == keySpan.Start && keySource[0] == '"' {
		key, err := strconv.Unquote(keySource)
		if err != nil {
			return "", fmt.Errorf("invalid key: %w", err)
		}

		return key, nil
	}
	if !hasFirst || first.lexeme.kind != sourceLexemeIdentifier || first.span.Start != keySpan.Start || first.span.End != keySpan.End || !identifier(keySource) {
		return "", fmt.Errorf("invalid key %q", keySource)
	}

	return keySource, nil
}

func (p *Parser) parseDynamicKeyTokens(keySpan SourceSpan, tokens []sourceToken) (Expr, bool, error) {
	index := 0
	next := func() (sourceToken, bool) {
		if index >= len(tokens) {
			return sourceToken{}, false
		}
		token := tokens[index]
		index++

		return token, true
	}
	keyToken, hasKey := next()
	openToken, hasOpen := next()
	if !hasKey || !hasOpen {
		return nil, false, nil
	}
	if keyToken.lexeme.kind != sourceLexemeIdentifier || openToken.lexeme.kind != sourceLexemeSymbol || p.sourceTokenLexemeText(keyToken) != "key" || p.sourceTokenLexemeText(openToken) != "(" || keyToken.span.Start != keySpan.Start || keyToken.span.End != openToken.span.Start {
		return nil, false, nil
	}
	depth := 1
	var closeToken sourceToken
findClose:
	for {
		token, ok := next()
		if !ok {
			break
		}
		if token.lexeme.kind != sourceLexemeSymbol {
			continue
		}
		switch p.sourceTokenLexemeText(token) {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				closeToken = token

				break findClose
			}
		}
	}
	if closeToken.lexeme.end == 0 || closeToken.span.End != keySpan.End {
		return nil, true, errors.New("dynamic key must use key(expression)")
	}
	expressionSpan := p.trimSourceSpan(SourceSpan{Start: openToken.span.End, End: closeToken.span.Start})
	expression, err := p.parseExpressionSourceSpan(expressionSpan)
	if err != nil {
		return nil, true, err
	}

	return expression, true, nil
}

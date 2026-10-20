package syntax

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type parsedValue struct {
	Expression Expr
}

type collectionEntryHeader struct {
	key     string
	keyExpr Expr
	keySpan SourceSpan
	value   SourceSpan
	dynamic bool
}

type continuedSource struct {
	sourceRange SourceSpan
	maskedSpans []SourceSpan
	lexemes     []sourceLexeme
}

type continuationLineBoundary struct {
	line        sourceToken
	cursorIndex int
	tokenIndex  int
}

func (source continuedSource) expressionSource(original string, literals map[SourceSpan]string) expressionSource {
	return expressionSource{
		chunks:      []expressionSourceChunk{{text: original[source.sourceRange.Start:source.sourceRange.End], offset: source.sourceRange.Start}},
		literals:    literals,
		lexemes:     source.lexemes,
		maskedSpans: source.maskedSpans,
	}.withChunkStarts()
}

type topLevelHeader struct {
	keyword  sourceToken
	index    int
	exported bool
}

func (parsed parsedValue) isMap() bool {
	_, ok := parsed.Expression.(*MapExpr)

	return ok
}

func (parsed parsedValue) isOverlay() bool {
	_, ok := parsed.Expression.(*OverlayExpr)

	return ok
}

func (parsed parsedValue) span() SourceSpan {
	return parsed.Expression.ExprSpan()
}

func (p *Parser) sourceTokenLastLexemeSpan(lineToken sourceToken) SourceSpan {
	lexemes := p.sourceTokenLexemesInSpanFromStart(lineToken.span)
	if len(lexemes) == 0 {
		return lineToken.span
	}

	return lexemes[len(lexemes)-1].span
}

func (p *Parser) statementSpan(lineToken sourceToken, end SourceSpan) SourceSpan {
	lexemes := p.sourceTokenLexemesInSpanFromStart(lineToken.span)
	start := lineToken.span.Start
	if len(lexemes) > 0 {
		start = lexemes[0].span.Start
	}
	if end.End < start {
		end = p.sourceTokenLastLexemeSpan(lineToken)
	}

	return SourceSpan{Start: start, End: end.End}
}

func mergeSourceSpans(left, right SourceSpan) SourceSpan {
	if right.End > left.End {
		left.End = right.End
	}
	if left.Start == 0 || right.Start > 0 && right.Start < left.Start {
		left.Start = right.Start
	}

	return left
}

func (p *Parser) expressionTreeSpan(expression Expr) SourceSpan {
	var span SourceSpan
	WalkExpr(expression, func(expression Expr) {
		span = mergeSourceSpans(span, expression.ExprSpan())
		if mapping, ok := expression.(*MapExpr); ok {
			for _, entry := range mapping.Entries {
				span = mergeSourceSpans(span, entry.KeySpan)
			}
		}
	})

	return span
}

type Parser struct {
	tokens                   []sourceToken
	tokenIndex               int
	diagnosticSpan           SourceSpan
	topLevelLexemeIndex      int
	topLevelLineNumber       int
	lastLineCursorLineNumber int
	lastLineCursorToken      sourceToken
	lastTopLevelLineNumber   int
	stopElseIndent           int
	source                   string
	continued                *continuedSource
	literals                 map[SourceSpan]string
}

func ParseAST(source string) (ModuleAST, error) {
	module, _, err := ParseASTWithTokens(source)

	return module, err
}

func parseASTWithTokens(source string) (ModuleAST, []sourceToken, error) {
	if len(source) > maxMCLSourceBytes {
		return ModuleAST{}, nil, fmt.Errorf("MCL source exceeds the %d-byte limit", maxMCLSourceBytes)
	}
	tokens, err := lexSource(source)
	if err != nil {
		return ModuleAST{}, nil, err
	}
	module, err := parseASTWithLexedTokens(source, tokens)
	if err != nil {
		return ModuleAST{}, nil, err
	}

	return module, tokens, nil
}

func parseASTWithLexedTokens(source string, tokens []sourceToken) (module ModuleAST, resultErr error) {
	parser := Parser{tokens: tokens, source: source, literals: make(map[SourceSpan]string)}
	defer func() {
		resultErr = syntaxDiagnostic(resultErr, parser.diagnosticSpan)
	}()
	for _, token := range tokens {
		if token.kind != sourceTokenNewline {
			continue
		}
		for _, literal := range token.literals {
			parser.literals[literal.span] = literal.value
		}
	}
	module = ModuleAST{}
	for _, token := range tokens {
		if token.kind != sourceTokenTrivia {
			continue
		}
		span := token.span
		if token.triviaOnly && token.triviaOffset < 0 && span.End > span.Start && source[span.End-1] == '\r' {
			span.End--
		}
		module.Trivia = append(module.Trivia, SourceTrivia{
			Span: span,
			Text: source[span.Start:span.End],
		})
	}
	phase := 0
	for {
		lineToken, hasLine, err := parser.nextTopLevelStatement()
		if err != nil {
			return ModuleAST{}, err
		}
		if !hasLine {
			break
		}
		if lineToken.indent != 0 {
			return ModuleAST{}, fmt.Errorf("line %d: unexpected indentation", lineToken.lineNumber)
		}
		header, hasHeader := parser.topLevelHeader(lineToken)
		keyword := ""
		if hasHeader {
			keyword = parser.sourceTokenLexemeText(header.keyword)
		}
		lineTokens := parser.sourceTokenLexemesInSpan(SourceSpan{Start: header.keyword.span.End, End: lineToken.span.End})
		if keyword == "import" && !header.exported {
			if phase != 0 {
				return ModuleAST{}, fmt.Errorf("line %d: imports must precede definitions", lineToken.lineNumber)
			}
			path, alias, aliasSpan, err := parser.parseImportLine(lineToken)
			if err != nil {
				return ModuleAST{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
			}
			module.Nodes = append(module.Nodes, &ImportDeclaration{
				NodeMetadata: NodeMetadata{Line: lineToken.lineNumber, Span: parser.statementSpan(lineToken, aliasSpan)},
				Name:         alias, ModulePath: path, NameSpan: aliasSpan,
			})

			continue
		}
		if keyword == "type" {
			if phase > 1 {
				return ModuleAST{}, fmt.Errorf("line %d: type definitions must precede emit statements", lineToken.lineNumber)
			}
			phase = 1
			typeTokens := parser.sourceTokenLexemesInSpan(SourceSpan{Start: header.keyword.span.End, End: lineToken.span.End})
			if len(typeTokens) == 0 || typeTokens[0].lexeme.kind != sourceLexemeIdentifier {
				return ModuleAST{}, fmt.Errorf("line %d: expected type name = type expression", lineToken.lineNumber)
			}
			parenDepth, bracketDepth, braceDepth, angleDepth := 0, 0, 0, 0
			equalIndex := -1
		findTypeDefinitionEqual:
			for index, token := range typeTokens {
				if token.lexeme.kind != sourceLexemeSymbol {
					continue
				}
				switch parser.sourceTokenLexemeText(token) {
				case "=":
					if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 && angleDepth == 0 {
						equalIndex = index

						break findTypeDefinitionEqual
					}
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
				case "<":
					angleDepth++
				case ">":
					angleDepth--
				}
			}
			if equalIndex < 0 {
				return ModuleAST{}, fmt.Errorf("line %d: expected type name = type expression", lineToken.lineNumber)
			}
			nameToken, equalToken := typeTokens[0], typeTokens[equalIndex]
			nameSpan := parser.trimSourceSpan(SourceSpan{Start: nameToken.span.Start, End: equalToken.span.Start})
			name, parameters, valid := parser.parseTypeDeclarationName(nameSpan)
			if !valid {
				return ModuleAST{}, fmt.Errorf("line %d: expected type name = type expression", lineToken.lineNumber)
			}
			definitionSpan := parser.trimSourceSpan(SourceSpan{Start: equalToken.span.End, End: lineToken.span.End})
			var err error
			var typeSpec TypeExpr
			if definitionSpan.Start == definitionSpan.End {
				typeSpec, err = parser.typeRecord(lineToken.lineNumber)
				if err != nil {
					return ModuleAST{}, err
				}
			} else {
				typeSpec, err = parser.parseTypeExpressionSourceSpan(definitionSpan)
				if err != nil {
					return ModuleAST{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
				}
			}
			typeSpec.TypeParameters = parameters
			endSpan := parser.sourceTokenLastLexemeSpan(lineToken)
			if definitionSpan.Start == definitionSpan.End {
				if lastLine, ok := parser.lastConsumedSourceToken(); ok && lastLine.span.Start > lineToken.span.Start {
					endSpan = parser.sourceTokenLastLexemeSpan(lastLine)
				}
			}
			module.Nodes = append(module.Nodes, &TypeDeclaration{
				NodeMetadata: NodeMetadata{Line: lineToken.lineNumber, Span: parser.statementSpan(lineToken, endSpan)},
				Name:         name, Exported: header.exported, TypeSpec: &typeSpec,
				NameSpan: nameToken.span,
			})

			continue
		}
		if keyword == "rule" {
			if phase > 1 {
				return ModuleAST{}, fmt.Errorf("line %d: rules must precede emit statements", lineToken.lineNumber)
			}
			phase = 1
			name, rule, err := parser.ruleDefinition(lineToken, header)
			if err != nil {
				return ModuleAST{}, err
			}
			endSpan := lineToken.span
			if len(rule.Requirements) > 0 {
				endSpan = rule.Requirements[len(rule.Requirements)-1].Span
			}
			module.Nodes = append(module.Nodes, &RuleDeclaration{
				NodeMetadata: NodeMetadata{Line: lineToken.lineNumber, Span: parser.statementSpan(lineToken, endSpan)},
				Name:         name, Exported: header.exported, Rule: rule,
				NameSpan: lineTokens[0].span,
			})

			continue
		}
		if keyword == "fn" {
			if phase > 1 {
				return ModuleAST{}, fmt.Errorf("line %d: function definitions must precede emit statements", lineToken.lineNumber)
			}
			phase = 1
			name, definition, err := parser.functionDefinition(lineToken, header)
			if err != nil {
				return ModuleAST{}, err
			}
			if header.exported && definition.ReturnType == nil {
				return ModuleAST{}, fmt.Errorf("line %d: exported functions require an explicit return type", lineToken.lineNumber)
			}
			module.Nodes = append(module.Nodes, &FunctionDeclaration{
				NodeMetadata: NodeMetadata{Line: lineToken.lineNumber, Span: parser.statementSpan(lineToken, parser.expressionTreeSpan(definition.Expression))},
				Name:         name, Exported: header.exported, Function: definition,
				NameSpan: lineTokens[0].span,
			})

			continue
		}
		if keyword == "let" {
			if phase > 1 {
				return ModuleAST{}, fmt.Errorf("line %d: definitions must precede emit statements", lineToken.lineNumber)
			}
			phase = 1
			if len(lineTokens) == 0 || lineTokens[0].lexeme.kind != sourceLexemeIdentifier {
				return ModuleAST{}, fmt.Errorf("line %d: expected let name = expression", lineToken.lineNumber)
			}
			nameToken := lineTokens[0]
			name := parser.sourceTokenLexemeText(lineTokens[0])
			if !identifier(name) {
				return ModuleAST{}, fmt.Errorf("line %d: invalid name %q", lineToken.lineNumber, name)
			}
			parenDepth, bracketDepth, braceDepth, angleDepth := 0, 0, 0, 0
			equalIndex, annotationIndex := -1, -1
		findLetDelimiter:
			for index := 1; index < len(lineTokens); index++ {
				lexeme := lineTokens[index].lexeme
				if lexeme.kind != sourceLexemeSymbol {
					continue
				}
				switch parser.sourceTokenLexemeText(lineTokens[index]) {
				case ":":
					if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 && angleDepth == 0 && annotationIndex < 0 {
						annotationIndex = index
					}
				case "=":
					if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 && angleDepth == 0 {
						equalIndex = index

						break findLetDelimiter
					}
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
				case "<":
					angleDepth++
				case ">":
					angleDepth--
				}
			}
			if equalIndex < 0 {
				return ModuleAST{}, fmt.Errorf("line %d: expected let name = expression", lineToken.lineNumber)
			}
			var annotation *TypeExpr
			if annotationIndex >= 0 {
				annotationSpan := parser.trimSourceSpan(SourceSpan{Start: lineTokens[annotationIndex].span.End, End: lineTokens[equalIndex].span.Start})
				typeSpec, err := parser.parseTypeExpressionSourceSpan(annotationSpan)
				if err != nil {
					return ModuleAST{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
				}
				annotation = &typeSpec
			}
			valueSpan := parser.trimSourceSpan(SourceSpan{Start: lineTokens[equalIndex].span.End, End: lineToken.span.End})
			var parsed parsedValue
			var err error
			if valueSpan.Start == valueSpan.End {
				valueToken, hasValue, lineErr := parser.peekParserToken()
				if lineErr != nil {
					return ModuleAST{}, lineErr
				}
				if !hasValue || valueToken.indent == 0 {
					return ModuleAST{}, fmt.Errorf("line %d: expected an indented value", lineToken.lineNumber)
				}
				parsed, err = parser.indentedValueWithExpression(lineToken.lineNumber)
			} else {
				continued, continueErr := parser.continuedExpressionSourceSpan(lineToken, valueSpan, 0)
				err = continueErr
				if err == nil {
					parsed, err = parser.valueExpressionWithContinued(continued, 0, lineToken.lineNumber)
				}
			}
			if err != nil {
				return ModuleAST{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
			}
			module.Nodes = append(module.Nodes, &LetDeclaration{
				NodeMetadata: NodeMetadata{Line: lineToken.lineNumber, Span: parser.statementSpan(lineToken, parser.expressionTreeSpan(parsed.Expression))},
				Name:         name, Exported: header.exported, Expression: parsed.Expression,
				Annotation: annotation, NameSpan: nameToken.span,
			})

			continue
		}
		if keyword == "emit" && !header.exported {
			if phase == 3 {
				return ModuleAST{}, fmt.Errorf("line %d: emit statements must precede validate statements", lineToken.lineNumber)
			}
			phase = 2
			expressionSpan := parser.trimSourceSpan(SourceSpan{Start: header.keyword.span.End, End: lineToken.span.End})
			each := false
			if expressionSpan.End-expressionSpan.Start >= len("each") && parser.source[expressionSpan.Start:expressionSpan.Start+len("each")] == "each" &&
				(expressionSpan.End-expressionSpan.Start == len("each") || parser.source[expressionSpan.Start+len("each")] == ' ' || parser.source[expressionSpan.Start+len("each")] == '\t') {
				each = true
				expressionSpan.Start = parser.trimSourceSpan(SourceSpan{Start: expressionSpan.Start + len("each"), End: expressionSpan.End}).Start
			}
			continued, err := parser.continuedExpressionSourceSpan(lineToken, expressionSpan, 0)
			if err != nil {
				return ModuleAST{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
			}
			parsed, err := parser.valueExpressionWithContinued(continued, 0, lineToken.lineNumber)
			if err != nil {
				return ModuleAST{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
			}
			module.Nodes = append(module.Nodes, &EmitStatement{
				NodeMetadata: NodeMetadata{Line: lineToken.lineNumber, Span: parser.statementSpan(lineToken, parser.expressionTreeSpan(parsed.Expression))},
				Expression:   parsed.Expression, Each: each,
			})

			continue
		}
		if keyword == "validate" && !header.exported {
			lineTokens = parser.sourceTokenLexemesInSpan(lineToken.span)
			if phase < 2 {
				return ModuleAST{}, fmt.Errorf("line %d: validate statements must follow emit statements", lineToken.lineNumber)
			}
			usingIndex := -1
			parenDepth, bracketDepth, braceDepth := 0, 0, 0
			for index, token := range lineTokens {
				lexeme := token.lexeme
				if lexeme.kind == sourceLexemeIdentifier && parser.sourceTokenLexemeText(token) == "using" && parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
					usingIndex = index

					break
				}
				if lexeme.kind != sourceLexemeSymbol {
					continue
				}
				switch parser.sourceTokenLexemeText(token) {
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
				}
			}
			if usingIndex < 0 {
				return ModuleAST{}, fmt.Errorf("line %d: expected validate value using ruleReference", lineToken.lineNumber)
			}
			if usingIndex == 0 || usingIndex+1 >= len(lineTokens) {
				return ModuleAST{}, fmt.Errorf("line %d: expected validate value using ruleReference", lineToken.lineNumber)
			}
			targetSpan := parser.trimSourceSpan(SourceSpan{Start: header.keyword.span.End, End: lineTokens[usingIndex].span.Start})
			ruleSpan := parser.trimSourceSpan(SourceSpan{Start: lineTokens[usingIndex].span.End, End: lineToken.span.End})
			if targetSpan.Start == targetSpan.End || ruleSpan.Start == ruleSpan.End {
				return ModuleAST{}, fmt.Errorf("line %d: expected validate value using ruleReference", lineToken.lineNumber)
			}
			var target Expr
			output := false
			var queryType *TypeExpr
			var querySchema *TypeExpr
			var queryWhere Expr
			var querySpan SourceSpan
			queryMinMatches := 0
			queryMinMatchesSet := false
			var err error
			each := false
			targetTokenIndex := 1
			if targetTokenIndex < usingIndex && lineTokens[targetTokenIndex].lexeme.kind == sourceLexemeIdentifier && parser.sourceTokenLexemeText(lineTokens[targetTokenIndex]) == "each" {
				each = true
			}
			if each {
				querySpan = parser.trimSourceSpan(SourceSpan{Start: lineTokens[targetTokenIndex].span.End, End: targetSpan.End})
				if querySpan.Start == querySpan.End {
					return ModuleAST{}, fmt.Errorf("line %d: expected each descendants(output, Type[, minMatches = count]), descendants(output, where = lambda[, minMatches = count]), or descendants(output, select = bySchema(Type)[, minMatches = count])", lineToken.lineNumber)
				}
				queryExpression, err := parser.parseExpressionSourceSpan(querySpan)
				if err != nil {
					return ModuleAST{}, fmt.Errorf("line %d: descendants query: %w", lineToken.lineNumber, err)
				}
				call, isCall := queryExpression.(*CallExpr)
				callee, isReference := callCalleeReference(call)
				if !isCall || !isReference || callee.Name != "descendants" || len(call.Arguments) < 2 || len(call.Arguments) > 3 || len(call.ArgumentNames) != len(call.Arguments) || call.ArgumentNames[0] != "" || call.ArgumentNames[1] != "" && call.ArgumentNames[1] != "where" && call.ArgumentNames[1] != "select" {
					return ModuleAST{}, fmt.Errorf("line %d: expected descendants query", lineToken.lineNumber)
				}
				querySpan = parser.expressionTreeSpan(queryExpression)
				if len(call.Arguments) == 3 {
					minimum, isInteger := call.Arguments[2].(*IntegerExpr)
					if call.ArgumentNames[2] != "minMatches" || !isInteger {
						return ModuleAST{}, fmt.Errorf("line %d: descendants minMatches expects an integer literal", lineToken.lineNumber)
					}
					queryMinMatches, err = strconv.Atoi(minimum.Value)
					if err != nil || queryMinMatches < 0 || queryMinMatches > maxIterations {
						return ModuleAST{}, fmt.Errorf("line %d: descendants minMatches must be between 0 and %d", lineToken.lineNumber, maxIterations)
					}
					queryMinMatchesSet = true
				}
				firstArgument, isReference := call.Arguments[0].(*ReferenceExpr)
				if !isReference || firstArgument.Name != "output" {
					return ModuleAST{}, fmt.Errorf("line %d: descendants expects output as its first argument", lineToken.lineNumber)
				}
				switch call.ArgumentNames[1] {
				case "where":
					lambda, isLambda := call.Arguments[1].(*LambdaExpr)
					if !isLambda || len(lambda.Parameters) != 1 {
						return ModuleAST{}, fmt.Errorf("line %d: descendants where expects a one-argument lambda", lineToken.lineNumber)
					}
					queryWhere = call.Arguments[1]
				case "select":
					selector, isCall := call.Arguments[1].(*CallExpr)
					selectorName, selectorIsReference := callCalleeReference(selector)
					if !isCall || !selectorIsReference || selectorName.Name != "bySchema" || len(selector.Arguments) != 1 || len(selector.ArgumentNames) != 1 || selector.ArgumentNames[0] != "" {
						return ModuleAST{}, fmt.Errorf("line %d: descendants select expects bySchema(Type)", lineToken.lineNumber)
					}
					typeExpr, err := parser.parseTypeExpressionSourceSpan(selector.Arguments[0].ExprSpan())
					if err != nil {
						return ModuleAST{}, fmt.Errorf("line %d: descendants schema selector: %w", lineToken.lineNumber, err)
					}
					querySchema = &typeExpr
				default:
					typeExpr, err := parser.parseTypeExpressionSourceSpan(call.Arguments[1].ExprSpan())
					if err != nil {
						return ModuleAST{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
					}
					queryType = &typeExpr
				}
			} else {
				expression, expressionErr := parser.parseExpressionSourceSpan(targetSpan)
				err = expressionErr
				if err != nil {
					return ModuleAST{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
				}
				if reference, isReference := expression.(*ReferenceExpr); isReference && reference.Name == "output" {
					output = true
				} else {
					target = expression
				}
			}
			ruleExpr, err := parser.parseExpressionSourceSpan(ruleSpan)
			if err != nil {
				return ModuleAST{}, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
			}
			phase = 3
			ruleName := ""
			ruleNameToken := lineTokens[usingIndex+1].lexeme
			if ruleSpan.Start == lineTokens[usingIndex+1].span.Start && ruleSpan.End == lineTokens[usingIndex+1].span.End && ruleNameToken.kind == sourceLexemeIdentifier {
				candidate := parser.sourceTokenLexemeText(lineTokens[usingIndex+1])
				if identifier(candidate) {
					ruleName = candidate
				}
			}
			endSpan := parser.expressionTreeSpan(ruleExpr)
			endSpan = mergeSourceSpans(endSpan, querySpan)
			if target != nil {
				endSpan = mergeSourceSpans(endSpan, parser.expressionTreeSpan(target))
			}
			module.Nodes = append(module.Nodes, &ValidateStatement{
				NodeMetadata: NodeMetadata{Line: lineToken.lineNumber, Span: parser.statementSpan(lineToken, endSpan)},
				Expression:   target, RuleExpr: ruleExpr, RuleName: ruleName, Each: each, Output: output,
				QueryType: queryType, QuerySchema: querySchema, QueryWhere: queryWhere, QueryMinMatches: queryMinMatches, QueryMinMatchesSet: queryMinMatchesSet,
			})

			continue
		}
		if hasHeader && header.exported {
			return ModuleAST{}, fmt.Errorf("line %d: export can only be used with let, fn, rule, or type", lineToken.lineNumber)
		}

		return ModuleAST{}, fmt.Errorf("line %d: expected let or emit", lineToken.lineNumber)
	}

	return module, nil
}

func callCalleeReference(call *CallExpr) (*ReferenceExpr, bool) {
	if call == nil {
		return nil, false
	}
	reference, ok := call.Callee.(*ReferenceExpr)

	return reference, ok
}

func (p *Parser) nextTopLevelStatement() (sourceToken, bool, error) {
	indentPending := false
	lineLexemeIndex := -1
	lineLexemeNumber := 0
	afterLineNumber := max(p.lastLineCursorLineNumber, p.lastTopLevelLineNumber)
	for p.tokenIndex < len(p.tokens) {
		token := p.tokens[p.tokenIndex]
		switch token.kind {
		case sourceTokenTrivia, sourceTokenDedent:
			p.tokenIndex++
		case sourceTokenLexeme:
			if lineLexemeNumber != token.lineNumber {
				lineLexemeIndex = p.tokenIndex
				lineLexemeNumber = token.lineNumber
			}
			p.tokenIndex++
		case sourceTokenNewline:
			p.tokenIndex++
			if token.lineNumber == 0 || token.lineNumber <= afterLineNumber {
				lineLexemeIndex = -1
				lineLexemeNumber = 0

				continue
			}
			if indentPending {
				return sourceToken{}, false, fmt.Errorf("line %d: unexpected indentation", token.lineNumber)
			}
			line, ok := sourceTokenNewlineView(token)
			if !ok {
				return sourceToken{}, false, nil
			}
			p.diagnosticSpan = line.span
			if lineLexemeIndex < 0 || lineLexemeNumber != line.lineNumber {
				return sourceToken{}, false, errors.New("unexpected token between top-level statements")
			}
			p.advanceLineCursor(line)
			p.lastTopLevelLineNumber = line.lineNumber
			p.topLevelLexemeIndex = lineLexemeIndex
			p.topLevelLineNumber = line.lineNumber

			return line, true, nil
		case sourceTokenIndent:
			indentPending = true
			p.tokenIndex++
		default:
			return sourceToken{}, false, errors.New("unexpected token between top-level statements")
		}
	}

	return sourceToken{}, false, nil
}

func (p *Parser) topLevelHeader(lineToken sourceToken) (topLevelHeader, bool) {
	keyword, ok := p.consumeLineLexeme(lineToken)
	if !ok || keyword.lexeme.kind != sourceLexemeIdentifier {
		return topLevelHeader{}, false
	}
	if p.sourceTokenLexemeText(keyword) != "export" {
		return topLevelHeader{keyword: keyword}, true
	}
	keyword, ok = p.consumeLineLexeme(lineToken)
	if !ok || keyword.lexeme.kind != sourceLexemeIdentifier {
		return topLevelHeader{}, false
	}

	return topLevelHeader{keyword: keyword, index: 1, exported: true}, true
}

func (p *Parser) consumeLine(lineToken sourceToken) error {
	if lineToken.lineNumber <= p.lastLineCursorLineNumber {
		return nil
	}
	line, boundaryIndex, ok := p.nextSourceLineBoundary(p.tokenIndex, p.lastLineCursorLineNumber)
	if !ok {
		return errors.New("unexpected end of source tokens")
	}
	if line.lineNumber != lineToken.lineNumber {
		return errors.New("unexpected end of source tokens")
	}
	p.tokenIndex = boundaryIndex + 1
	p.advanceLineCursor(line)

	return nil
}

func (p *Parser) nextSourceLineBoundary(tokenIndex, afterLineNumber int) (sourceToken, int, bool) {
	for tokenIndex < len(p.tokens) {
		token := p.tokens[tokenIndex]
		if token.kind == sourceTokenNewline && token.lineNumber > afterLineNumber {
			line, ok := sourceTokenNewlineView(token)
			if ok {
				return line, tokenIndex, true
			}
		}
		tokenIndex++
	}

	return sourceToken{}, 0, false
}

func (p *Parser) peekContinuationLine() (continuationLineBoundary, bool) {
	line, tokenIndex, hasLine := p.nextContinuationLineBoundary(p.tokenIndex)
	if !hasLine {
		return continuationLineBoundary{}, false
	}

	return continuationLineBoundary{line: line, cursorIndex: p.tokenIndex, tokenIndex: tokenIndex}, true
}

func (p *Parser) nextContinuationLineBoundary(tokenIndex int) (sourceToken, int, bool) {
	for tokenIndex < len(p.tokens) {
		token := p.tokens[tokenIndex]
		if line, ok := sourceTokenNewlineView(token); ok {
			return line, tokenIndex, true
		}
		tokenIndex++
	}

	return sourceToken{}, 0, false
}

func (p *Parser) consumeContinuationLine(boundary continuationLineBoundary) error {
	if p.tokenIndex != boundary.cursorIndex || boundary.tokenIndex < p.tokenIndex || boundary.tokenIndex >= len(p.tokens) {
		return fmt.Errorf("line %d: continuation token cursor is out of sync", boundary.line.lineNumber)
	}
	token, ok := sourceTokenNewlineView(p.tokens[boundary.tokenIndex])
	if !ok || token.lineNumber != boundary.line.lineNumber || token.span != boundary.line.span {
		return fmt.Errorf("line %d: continuation token cursor is out of sync", boundary.line.lineNumber)
	}
	p.tokenIndex = boundary.tokenIndex + 1
	p.advanceLineCursor(token)

	return nil
}

func (p *Parser) peekParserToken() (sourceToken, bool, error) {
	for tokenIndex := p.tokenIndex; tokenIndex < len(p.tokens); tokenIndex++ {
		token := p.tokens[tokenIndex]
		switch token.kind {
		case sourceTokenNewline:
			if token.lineNumber == 0 || token.lineNumber <= p.lastLineCursorLineNumber {
				continue
			}

			line, ok := sourceTokenNewlineView(token)
			if !ok {
				return sourceToken{}, false, fmt.Errorf("line %d: invalid newline token", token.lineNumber)
			}
			p.diagnosticSpan = line.span

			return line, true, nil
		case sourceTokenIndent, sourceTokenTrivia, sourceTokenLexeme:
			continue
		case sourceTokenDedent:
			return sourceToken{}, false, nil
		}
	}

	return sourceToken{}, false, nil
}

func (p *Parser) peekParserTokenAfterDedent() (sourceToken, bool, error) {
	tokenIndex := p.tokenIndex
	for tokenIndex < len(p.tokens) {
		token := p.tokens[tokenIndex]
		switch token.kind {
		case sourceTokenNewline:
			if token.lineNumber == 0 || token.lineNumber <= p.lastLineCursorLineNumber {
				tokenIndex++

				continue
			}

			line, ok := sourceTokenNewlineView(token)
			if !ok {
				return sourceToken{}, false, fmt.Errorf("line %d: invalid newline token", token.lineNumber)
			}
			p.diagnosticSpan = line.span

			return line, true, nil
		case sourceTokenDedent, sourceTokenTrivia, sourceTokenLexeme:
			tokenIndex++
		default:
			return sourceToken{}, false, nil
		}
	}

	return sourceToken{}, false, nil
}

func sourceTokenNewlineView(boundary sourceToken) (sourceToken, bool) {
	if boundary.kind != sourceTokenNewline || boundary.lineNumber == 0 || boundary.lineSpan.End < boundary.lineSpan.Start {
		return sourceToken{}, false
	}

	boundary.span = boundary.lineSpan

	return boundary, true
}

func (p *Parser) advanceLineCursor(line sourceToken) {
	if line.lineNumber <= p.lastLineCursorLineNumber {
		return
	}
	p.lastLineCursorLineNumber = line.lineNumber
	p.lastLineCursorToken = line
}

func (p *Parser) nextBlockEntry(indent int, allowIndent bool) (sourceToken, bool, bool, error) {
	lineNumber := p.lastLineCursorLineNumber
	crossedLineBoundary := false
	for {
		tokenIndex := p.nextBlockStructuralTokenIndex(p.tokenIndex)
		if tokenIndex >= len(p.tokens) {
			return sourceToken{}, false, false, nil
		}
		token := p.tokens[tokenIndex]
		switch token.kind {
		case sourceTokenDedent:
			return sourceToken{}, false, false, nil
		case sourceTokenIndent:
			line, boundaryIndex, hasLine := p.nextSourceLineBoundary(tokenIndex+1, lineNumber)
			if !hasLine {
				return sourceToken{}, false, false, nil
			}
			if !allowIndent {
				return sourceToken{}, false, false, fmt.Errorf("line %d: unexpected indentation between block entries", line.lineNumber)
			}
			p.tokenIndex = boundaryIndex + 1
			p.advanceLineCursor(line)

			return line, line.indent == indent, true, nil
		case sourceTokenNewline:
			if token.lineNumber > lineNumber {
				lineToken, ok := sourceTokenNewlineView(token)
				if !ok {
					return sourceToken{}, false, false, nil
				}
				if lineToken.indent == indent && (p.stopElseIndent != indent || !p.isExactElseHeader(lineToken)) {
					p.tokenIndex = tokenIndex + 1
					p.advanceLineCursor(lineToken)
				}

				return lineToken, lineToken.indent == indent, false, nil
			}
			if crossedLineBoundary {
				return sourceToken{}, false, false, fmt.Errorf("line %d: expected newline between block entries", lineNumber)
			}
			p.tokenIndex = tokenIndex + 1
			lineNumber = token.lineNumber
			crossedLineBoundary = true
		default:
			return sourceToken{}, false, false, fmt.Errorf("line %d: unexpected token between block entries", lineNumber)
		}
	}
}

func (p *Parser) nextBlockStructuralTokenIndex(tokenIndex int) int {
	for tokenIndex < len(p.tokens) {
		switch p.tokens[tokenIndex].kind {
		case sourceTokenNewline, sourceTokenIndent, sourceTokenDedent:
			return tokenIndex
		case sourceTokenTrivia, sourceTokenLexeme:
			tokenIndex++
		default:
			return tokenIndex
		}
	}

	return tokenIndex
}

func (p *Parser) peekToken() (sourceToken, bool) {
	p.tokenIndex = p.nextSyntaxTokenIndex(p.tokenIndex)
	if p.tokenIndex >= len(p.tokens) {
		return sourceToken{}, false
	}

	return p.tokens[p.tokenIndex], true
}

func (p *Parser) nextSyntaxTokenIndex(tokenIndex int) int {
	for tokenIndex < len(p.tokens) && (p.tokens[tokenIndex].kind == sourceTokenTrivia || p.tokens[tokenIndex].kind == sourceTokenLexeme) {
		tokenIndex++
	}

	return tokenIndex
}

func (p *Parser) consumeToken(kind sourceTokenKind) bool {
	token, ok := p.peekToken()
	if !ok || token.kind != kind {
		return false
	}
	p.tokenIndex++
	if token.kind == sourceTokenNewline && token.lineNumber > p.lastLineCursorLineNumber {
		if line, ok := sourceTokenNewlineView(token); ok {
			p.advanceLineCursor(line)
		}
	}

	return true
}

func (p *Parser) lastConsumedSourceToken() (sourceToken, bool) {
	return p.lastLineCursorToken, p.lastLineCursorToken.kind == sourceTokenNewline
}

func (p *Parser) parseTypeDeclarationName(nameSpan SourceSpan) (string, []string, bool) {
	lexemes := p.sourceTokenLexemesInSpan(nameSpan)
	if len(lexemes) == 0 {
		return "", nil, false
	}
	if lexemes[0].lexeme.kind != sourceLexemeIdentifier {
		return "", nil, false
	}

	openIndex := -1
	for index := 1; index < len(lexemes); index++ {
		if lexemes[index].lexeme.kind == sourceLexemeSymbol && p.sourceTokenLexemeText(lexemes[index]) == "<" {
			openIndex = index

			break
		}
	}
	if openIndex < 0 {
		if len(lexemes) != 1 || lexemes[0].span.Start != nameSpan.Start || lexemes[0].span.End != nameSpan.End {
			return "", nil, false
		}

		return p.sourceTokenLexemeText(lexemes[0]), nil, true
	}

	openToken := lexemes[openIndex]
	baseSpan := p.trimSourceSpan(SourceSpan{Start: nameSpan.Start, End: openToken.span.Start})
	if baseSpan.Start == baseSpan.End || !identifier(p.source[baseSpan.Start:baseSpan.End]) {
		return "", nil, false
	}
	lastToken := lexemes[len(lexemes)-1]
	if lastToken.span.Start <= openToken.span.Start || lastToken.span.End != nameSpan.End || lastToken.lexeme.kind != sourceLexemeSymbol || p.sourceTokenLexemeText(lastToken) != ">" {
		return "", nil, false
	}
	parameterSpan := p.trimSourceSpan(SourceSpan{Start: openToken.span.End, End: lastToken.span.Start})
	parameterSpans, valid := p.splitTypeDeclarationParameterSpans(parameterSpan)
	if !valid || len(parameterSpans) == 0 {
		return "", nil, false
	}
	parameters := make([]string, len(parameterSpans))
	for index, span := range parameterSpans {
		parameter := p.source[span.Start:span.End]
		if !identifier(parameter) {
			return "", nil, false
		}
		parameters[index] = parameter
	}

	return p.source[baseSpan.Start:baseSpan.End], parameters, true
}

func (p *Parser) splitTypeDeclarationParameterSpans(span SourceSpan) ([]SourceSpan, bool) {
	var spans []SourceSpan
	parameterStart := span.Start
	depth := 0
	cursor := p.sourceSpanLexemeCursor(span)
	for {
		token, ok := cursor.next()
		if !ok {
			break
		}
		if token.lexeme.kind == sourceLexemeLiteral {
			if !sourceStringTokenIsClosed(p.sourceTokenLexemeText(token)) {
				return nil, false
			}

			continue
		}
		if token.lexeme.kind != sourceLexemeSymbol {
			continue
		}
		switch p.sourceTokenLexemeText(token) {
		case "(", "<", "{":
			depth++
		case ")", ">", "}":
			depth--
			if depth < 0 {
				return nil, false
			}
		case ",":
			if depth == 0 {
				parameterSpan := p.trimSourceSpan(SourceSpan{Start: parameterStart, End: token.span.Start})
				if parameterSpan.Start == parameterSpan.End {
					return nil, false
				}
				spans = append(spans, parameterSpan)
				parameterStart = token.span.End
			}
		}
	}
	if depth != 0 {
		return nil, false
	}
	parameterSpan := p.trimSourceSpan(SourceSpan{Start: parameterStart, End: span.End})
	if parameterSpan.Start == parameterSpan.End {
		if len(spans) == 0 {
			return nil, false
		}

		return spans, true
	}

	return append(spans, parameterSpan), true
}

func (p *Parser) functionDefinition(lineToken sourceToken, declaration topLevelHeader) (string, *FunctionDefinition, error) {
	lexemes := p.sourceTokenLexemesInSpan(SourceSpan{Start: declaration.keyword.span.End, End: lineToken.span.End})
	if p.sourceTokenLexemeText(declaration.keyword) != "fn" {
		return "", nil, fmt.Errorf("line %d: expected fn name(parameters) -> ReturnType = expression", lineToken.lineNumber)
	}
	if len(lexemes) < 2 || lexemes[0].lexeme.kind != sourceLexemeIdentifier || p.sourceTokenLexemeText(lexemes[1]) != "(" {
		return "", nil, fmt.Errorf("line %d: expected fn name(parameters) -> ReturnType = expression", lineToken.lineNumber)
	}
	nameToken, openToken := lexemes[0], lexemes[1]
	name := p.sourceTokenLexemeText(nameToken)
	parenDepth, bracketDepth, braceDepth, angleDepth := 0, 0, 0, 0
	equalIndex, closeIndex := -1, -1
	for index := 1; index < len(lexemes); index++ {
		token := lexemes[index]
		if token.lexeme.kind != sourceLexemeSymbol {
			continue
		}
		switch p.sourceTokenLexemeText(token) {
		case "=":
			if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 && angleDepth == 0 {
				equalIndex = index
				index = len(lexemes)
			}
		case "(":
			parenDepth++
		case ")":
			parenDepth--
			if parenDepth == 0 && closeIndex < 0 {
				closeIndex = index
			}
		case "[":
			bracketDepth++
		case "]":
			bracketDepth--
		case "{":
			braceDepth++
		case "}":
			braceDepth--
		case "<":
			angleDepth++
		case ">":
			angleDepth--
		}
	}
	if equalIndex < 0 {
		return "", nil, fmt.Errorf("line %d: expected fn name(parameters) -> ReturnType = expression", lineToken.lineNumber)
	}
	if closeIndex < 0 {
		return "", nil, fmt.Errorf("line %d: unterminated function parameters", lineToken.lineNumber)
	}
	if !identifier(name) {
		return "", nil, fmt.Errorf("line %d: invalid function name %q", lineToken.lineNumber, name)
	}
	closeToken := lexemes[closeIndex]
	parametersSpan := p.trimSourceSpan(SourceSpan{Start: openToken.span.End, End: closeToken.span.Start})
	parameterSpans, rangesErr := p.splitTypeArgumentSourceSpans(parametersSpan)
	if rangesErr != nil {
		return "", nil, fmt.Errorf("line %d: %w", lineToken.lineNumber, rangesErr)
	}
	var returnSpan SourceSpan
	arrowIndex := closeIndex + 1
	if arrowIndex < equalIndex && p.sourceTokenLexemeText(lexemes[arrowIndex]) == "->" {
		returnSpan = p.trimSourceSpan(SourceSpan{Start: lexemes[arrowIndex].span.End, End: lexemes[equalIndex].span.Start})
	}
	var returnType *TypeExpr
	if returnSpan.Start != returnSpan.End {
		parsed, err := p.parseTypeExpressionSourceSpan(returnSpan)
		if err != nil {
			return "", nil, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
		}
		returnType = &parsed
	}
	definition := &FunctionDefinition{ReturnType: returnType}
	seen := make(map[string]bool)
	for _, parameterSpan := range parameterSpans {
		parameterName, typeSpan, nameSpan, valid := p.typedParameterSourceSpans(parameterSpan)
		if !valid || !identifier(parameterName) {
			return "", nil, fmt.Errorf("line %d: expected a typed function parameter", lineToken.lineNumber)
		}
		if seen[parameterName] {
			return "", nil, fmt.Errorf("line %d: duplicate function parameter %q", lineToken.lineNumber, parameterName)
		}
		seen[parameterName] = true
		parameterType, err := p.parseTypeExpressionSourceSpan(typeSpan)
		if err != nil {
			return "", nil, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
		}
		definition.Parameters = append(definition.Parameters, RuleParameter{Name: parameterName, Type: parameterType, Span: nameSpan})
	}
	bodySpan := p.trimSourceSpan(SourceSpan{Start: lexemes[equalIndex].span.End, End: lineToken.span.End})
	var parsedBody parsedValue
	var err error
	if bodySpan.Start == bodySpan.End {
		bodyToken, hasBody, lineErr := p.peekParserToken()
		if lineErr != nil {
			return "", nil, lineErr
		}
		if !hasBody || bodyToken.indent == 0 {
			return "", nil, fmt.Errorf("line %d: expected an indented function body", lineToken.lineNumber)
		}
		bodySource, sourceErr := p.expressionSourceForSpan(bodyToken.span)
		if sourceErr != nil {
			return "", nil, sourceErr
		}
		_, _, _, _, hasWithHeader := expressionSourceRangeBeforeHeader(bodySource, "with")
		_, _, _, _, hasWithoutHeader := expressionSourceRangeBeforeHeader(bodySource, "without")
		if hasWithHeader || hasWithoutHeader {
			if err := p.consumeLine(bodyToken); err != nil {
				return "", nil, err
			}
			var continued continuedSource
			continued, err = p.continuedExpressionSourceSpan(bodyToken, bodyToken.span, 0)
			if err == nil {
				parsedBody, err = p.valueExpressionWithContinued(continued, 0, bodyToken.lineNumber)
			}
		} else {
			parsedBody, err = p.indentedValueWithExpression(lineToken.lineNumber)
		}
	} else {
		continued, continueErr := p.continuedExpressionSourceSpan(lineToken, bodySpan, 0)
		err = continueErr
		if err == nil {
			parsedBody, err = p.valueExpressionWithContinued(continued, 0, lineToken.lineNumber)
		}
	}
	if err != nil {
		return "", nil, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
	}
	if parsedBody.Expression == nil {
		return "", nil, fmt.Errorf("line %d: function body has no expression", lineToken.lineNumber)
	}
	definition.Expression = parsedBody.Expression

	return name, definition, nil
}

func (p *Parser) sourceTokenLexemesInSpan(span SourceSpan) []sourceToken {
	cursor := p.sourceSpanLexemeCursor(span)
	var lexemes []sourceToken
	for {
		token, ok := cursor.next()
		if !ok {
			return lexemes
		}
		lexemes = append(lexemes, token)
	}
}

func (p *Parser) sourceSpanLexemeCursor(span SourceSpan) sourceSpanLexemeCursor {
	index := sort.Search(len(p.tokens), func(index int) bool {
		return p.tokens[index].span.Start >= span.Start
	})

	return sourceSpanLexemeCursor{tokens: p.tokens, index: index, bounds: span}
}

func (p *Parser) sourceTokenLexemesInSpanFromStart(span SourceSpan) []sourceToken {
	return p.sourceTokenLexemesInSpan(span)
}

func (p *Parser) trimSourceSpan(span SourceSpan) SourceSpan {
	for span.Start < span.End {
		r, size := utf8.DecodeRuneInString(p.source[span.Start:span.End])
		if !unicode.IsSpace(r) {
			break
		}
		span.Start += size
	}
	for span.Start < span.End {
		r, size := utf8.DecodeLastRuneInString(p.source[span.Start:span.End])
		if !unicode.IsSpace(r) {
			break
		}
		span.End -= size
	}

	return span
}

func (p *Parser) splitTypeArgumentSourceSpans(span SourceSpan) ([]SourceSpan, error) {
	var spans []SourceSpan
	itemStart := span.Start
	depth := 0
	cursor := p.sourceSpanLexemeCursor(span)
	for {
		token, ok := cursor.next()
		if !ok {
			break
		}
		if token.lexeme.kind == sourceLexemeLiteral {
			if !sourceStringTokenIsClosed(p.source[token.span.Start:token.span.End]) {
				return nil, errors.New("unterminated type or string")
			}

			continue
		}
		if token.lexeme.kind != sourceLexemeSymbol {
			continue
		}
		switch p.sourceTokenLexemeText(token) {
		case "(", "<", "{":
			depth++
		case ")", ">", "}":
			depth--
			if depth < 0 {
				return nil, errors.New("unmatched closing delimiter")
			}
		case ",":
			if depth == 0 {
				item := p.trimSourceSpan(SourceSpan{Start: itemStart, End: token.span.Start})
				if item.Start == item.End {
					return nil, errors.New("function arguments cannot be empty")
				}
				spans = append(spans, item)
				itemStart = token.span.End
			}
		}
	}
	if depth != 0 {
		return nil, errors.New("unterminated type or string")
	}
	item := p.trimSourceSpan(SourceSpan{Start: itemStart, End: span.End})
	if item.Start == item.End {
		if len(spans) == 0 {
			return nil, nil
		}

		return nil, errors.New("function arguments cannot be empty")
	}
	spans = append(spans, item)

	return spans, nil
}

func (p *Parser) typedParameterSourceSpans(span SourceSpan) (string, SourceSpan, SourceSpan, bool) {
	cursor := p.sourceSpanLexemeCursor(span)
	name, hasName := cursor.next()
	colon, hasColon := cursor.next()
	if !hasName || !hasColon || name.lexeme.kind != sourceLexemeIdentifier || colon.lexeme.kind != sourceLexemeSymbol || p.sourceTokenLexemeText(colon) != ":" {
		return "", SourceSpan{}, SourceSpan{}, false
	}
	typeSpan := p.trimSourceSpan(SourceSpan{Start: colon.span.End, End: span.End})
	if typeSpan.Start == typeSpan.End {
		return "", SourceSpan{}, SourceSpan{}, false
	}

	return p.sourceTokenLexemeText(name), typeSpan, name.span, true
}

func (p *Parser) parseTypeExpressionSourceSpan(span SourceSpan) (TypeExpr, error) {
	source, err := p.expressionSourceForSpan(span)
	if err != nil {
		return TypeExpr{}, syntaxDiagnostic(err, span)
	}
	typeExpression, err := parseTypeExpressionSource(source, 0, source.Len())
	if err != nil {
		return TypeExpr{}, syntaxDiagnostic(err, span)
	}

	return typeExpression, nil
}

func (p *Parser) expressionSourceForSpan(span SourceSpan) (expressionSource, error) {
	if span.Start < 0 || span.End < span.Start || span.End > len(p.source) {
		return expressionSource{}, errors.New("invalid expression source span")
	}
	lexemes, ok := p.expressionLexemesForPhysicalRange(span)
	if !ok {
		return expressionSource{}, errors.New("unable to select expression tokens for source span")
	}
	source := expressionSource{
		chunks:   []expressionSourceChunk{{text: p.source[span.Start:span.End], offset: span.Start}},
		literals: p.literals,
		lexemes:  lexemes,
	}

	return source.withChunkStarts(), nil
}

func (p *Parser) parseExpressionSourceSpan(span SourceSpan) (Expr, error) {
	source, err := p.expressionSourceForSpan(span)
	if err != nil {
		return nil, syntaxDiagnostic(err, span)
	}
	expression, err := parseExpressionSource(source)
	if err != nil {
		return nil, syntaxDiagnostic(err, span)
	}

	return expression, nil
}

func (p *Parser) continuedExpressionSourceSpan(lineToken sourceToken, span SourceSpan, parentIndent int) (continuedSource, error) {
	if span.Start < 0 || span.End < span.Start || span.End > len(p.source) {
		return continuedSource{}, errors.New("invalid continued expression source span")
	}
	span = p.trimSourceSpan(span)

	return p.continuedExpressionFromSource(lineToken.lineNumber, span, span, parentIndent)
}

func (definition FunctionDefinition) BodySpan() SourceSpan {
	return definition.Expression.ExprSpan()
}

func (p *Parser) ruleDefinition(lineToken sourceToken, declaration topLevelHeader) (string, *RuleDefinition, error) {
	lexemes := p.sourceTokenLexemesInSpan(SourceSpan{Start: declaration.keyword.span.End, End: lineToken.span.End})
	if p.sourceTokenLexemeText(declaration.keyword) != "rule" || len(lexemes) < 2 || lexemes[0].lexeme.kind != sourceLexemeIdentifier || p.sourceTokenLexemeText(lexemes[1]) != "(" {
		return "", nil, fmt.Errorf("line %d: expected rule name(parameter: Type)", lineToken.lineNumber)
	}
	closeIndex := -1
	depth := 0
findClose:
	for index := 1; index < len(lexemes); index++ {
		token := lexemes[index]
		if token.lexeme.kind != sourceLexemeSymbol {
			continue
		}
		switch p.sourceTokenLexemeText(token) {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				closeIndex = index

				break findClose
			}
		}
	}
	if closeIndex < 0 || closeIndex+1 >= len(lexemes) || p.sourceTokenLexemeText(lexemes[closeIndex+1]) != ":" || closeIndex+2 != len(lexemes) {
		return "", nil, fmt.Errorf("line %d: expected rule name(parameter: Type)", lineToken.lineNumber)
	}
	name := p.sourceTokenLexemeText(lexemes[0])
	if !identifier(name) {
		return "", nil, fmt.Errorf("line %d: invalid rule name %q", lineToken.lineNumber, name)
	}
	openToken, closeToken := lexemes[1], lexemes[closeIndex]
	parametersSpan := p.trimSourceSpan(SourceSpan{Start: openToken.span.End, End: closeToken.span.Start})
	if parametersSpan.Start == parametersSpan.End {
		return "", nil, fmt.Errorf("line %d: a rule requires at least one typed parameter", lineToken.lineNumber)
	}
	definition := &RuleDefinition{}
	seen := make(map[string]bool)
	parameterSpans, err := p.splitTypeArgumentSourceSpans(parametersSpan)
	if err != nil {
		return "", nil, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
	}
	for _, parameterSpan := range parameterSpans {
		parameterName, typeSpan, nameSpan, valid := p.typedParameterSourceSpans(parameterSpan)
		if !valid || !identifier(parameterName) {
			return "", nil, fmt.Errorf("line %d: expected a typed rule parameter", lineToken.lineNumber)
		}
		if seen[parameterName] {
			return "", nil, fmt.Errorf("line %d: duplicate rule parameter %q", lineToken.lineNumber, parameterName)
		}
		seen[parameterName] = true
		typeExpr, err := p.parseTypeExpressionSourceSpan(typeSpan)
		if err != nil {
			return "", nil, fmt.Errorf("line %d: %w", lineToken.lineNumber, err)
		}
		definition.Parameters = append(definition.Parameters, RuleParameter{Name: parameterName, Type: typeExpr, Span: nameSpan})
	}
	if len(definition.Parameters) != 1 {
		return "", nil, fmt.Errorf("line %d: rules currently require exactly one parameter", lineToken.lineNumber)
	}
	firstRequirement, hasRequirement, err := p.peekParserToken()
	if err != nil {
		return "", nil, err
	}
	if !hasRequirement || firstRequirement.indent == 0 {
		return "", nil, fmt.Errorf("line %d: expected at least one require statement", lineToken.lineNumber)
	}
	requireIndent := firstRequirement.indent
	for {
		currentToken, hasCurrent, err := p.peekParserToken()
		if err != nil {
			return "", nil, err
		}
		if !hasCurrent {
			break
		}
		if currentToken.indent != requireIndent {
			return "", nil, fmt.Errorf("line %d: invalid indentation in rule body", currentToken.lineNumber)
		}
		lineTokens := p.sourceTokenLexemesInSpan(currentToken.span)
		if !p.sourceLineStartsWithKeyword(currentToken, "require") || len(lineTokens) < 2 || p.source[lineTokens[0].span.End] != ' ' {
			return "", nil, fmt.Errorf("line %d: rule bodies only accept require expressions", currentToken.lineNumber)
		}
		requirementSpan := p.trimSourceSpan(SourceSpan{Start: lineTokens[0].span.End, End: currentToken.span.End})
		message := ""
		if len(lineTokens) >= 3 {
			messageKeyword := lineTokens[len(lineTokens)-2]
			messageLiteral := lineTokens[len(lineTokens)-1]
			if messageKeyword.lexeme.kind == sourceLexemeIdentifier && p.sourceTokenLexemeText(messageKeyword) == "message" {
				messageText := p.source[messageLiteral.span.Start:messageLiteral.span.End]
				if messageKeyword.span.Start == 0 || p.source[messageKeyword.span.Start-1] != ' ' || messageKeyword.span.End >= len(p.source) || p.source[messageKeyword.span.End] != ' ' || messageLiteral.lexeme.kind != sourceLexemeLiteral || !strings.HasPrefix(messageText, `"`) || strings.HasPrefix(messageText, `"""`) || !sourceStringTokenIsClosed(messageText) {
					return "", nil, fmt.Errorf("line %d: require message must be a double-quoted string", currentToken.lineNumber)
				}
				messageExpression, err := p.parseExpressionSourceSpan(messageLiteral.span)
				if err != nil {
					return "", nil, fmt.Errorf("line %d: invalid require message", currentToken.lineNumber)
				}
				literal, isString := messageExpression.(*StringExpr)
				if !isString {
					return "", nil, fmt.Errorf("line %d: invalid require message", currentToken.lineNumber)
				}
				message = literal.Value
				requirementSpan = p.trimSourceSpan(SourceSpan{Start: requirementSpan.Start, End: messageKeyword.span.Start})
			}
		}
		if requirementSpan.Start == requirementSpan.End {
			return "", nil, fmt.Errorf("line %d: rule bodies only accept require expressions", currentToken.lineNumber)
		}
		if err := p.consumeLine(currentToken); err != nil {
			return "", nil, err
		}
		continued, continueErr := p.continuedExpressionSourceSpan(currentToken, requirementSpan, requireIndent)
		err = continueErr
		if err != nil {
			return "", nil, fmt.Errorf("line %d: %w", currentToken.lineNumber, err)
		}
		requirementText := p.source[requirementSpan.Start:requirementSpan.End]
		expression, err := p.parseContinuedExpression(continued)
		if err != nil {
			return "", nil, fmt.Errorf("line %d: %w", currentToken.lineNumber, err)
		}
		span := expression.ExprSpan()
		definition.Requirements = append(definition.Requirements, RuleRequirement{Expression: expression, Source: requirementText, Message: message, Line: currentToken.lineNumber, Span: span})
	}
	if len(definition.Requirements) == 0 {
		return "", nil, fmt.Errorf("line %d: a rule requires at least one require statement", lineToken.lineNumber)
	}

	return name, definition, nil
}

func (p *Parser) parseImportLine(lineToken sourceToken) (string, string, SourceSpan, error) {
	pathToken, ok := p.consumeLineLexeme(lineToken)
	pathText := p.sourceTokenLexemeText(pathToken)
	if !ok || pathToken.lexeme.kind != sourceLexemeLiteral || len(pathText) == 0 || pathText[0] != '"' {
		return "", "", SourceSpan{}, errors.New("import path must be a double-quoted string")
	}
	if !sourceStringTokenIsClosed(pathText) {
		return "", "", SourceSpan{}, errors.New("unterminated import path")
	}
	path, err := strconv.Unquote(pathText)
	if err != nil || path == "" {
		return "", "", SourceSpan{}, errors.New("invalid import path")
	}
	aliasKeyword, hasAliasKeyword := p.consumeLineLexeme(lineToken)
	aliasToken, hasAlias := p.consumeLineLexeme(lineToken)
	if !hasAliasKeyword || !hasAlias || aliasKeyword.lexeme.kind != sourceLexemeIdentifier || aliasToken.lexeme.kind != sourceLexemeIdentifier || p.hasLineLexeme(lineToken) || p.sourceTokenLexemeText(aliasKeyword) != "as" || aliasKeyword.span.End >= aliasToken.span.Start || p.source[aliasKeyword.span.End] != ' ' {
		return "", "", SourceSpan{}, errors.New("expected as and an import alias")
	}
	alias := p.sourceTokenLexemeText(aliasToken)
	if !identifier(alias) {
		return "", "", SourceSpan{}, fmt.Errorf("invalid import alias %q", alias)
	}

	return path, alias, aliasToken.span, nil
}

func (p *Parser) consumeLineLexeme(lineToken sourceToken) (sourceToken, bool) {
	if p.topLevelLineNumber != lineToken.lineNumber || p.topLevelLexemeIndex >= len(p.tokens) {
		return sourceToken{}, false
	}
	_, lineEnd, hasLine := p.sourceLineLexemes(lineToken)
	token := p.tokens[p.topLevelLexemeIndex]
	if !hasLine || token.kind != sourceTokenLexeme || token.span.Start < lineToken.span.Start || token.span.End > lineEnd {
		return sourceToken{}, false
	}
	p.topLevelLexemeIndex++

	return token, true
}

func (p *Parser) hasLineLexeme(lineToken sourceToken) bool {
	if p.topLevelLineNumber != lineToken.lineNumber || p.topLevelLexemeIndex >= len(p.tokens) {
		return false
	}
	_, lineEnd, hasLine := p.sourceLineLexemes(lineToken)
	token := p.tokens[p.topLevelLexemeIndex]

	return hasLine && token.kind == sourceTokenLexeme && token.span.Start >= lineToken.span.Start && token.span.End <= lineEnd
}

func sourceStringTokenIsClosed(text string) bool {
	if len(text) < 2 || text[len(text)-1] != '"' {
		return false
	}
	backslashes := 0
	for index := len(text) - 2; index >= 0 && text[index] == '\\'; index-- {
		backslashes++
	}

	return backslashes%2 == 0
}

func (p *Parser) sourceLineStartsWithKeyword(lineToken sourceToken, keyword string) bool {
	lexemes, _, ok := p.sourceLineLexemes(lineToken)
	if !ok || len(lexemes) < 2 || lexemes[0].lexeme.kind != sourceLexemeIdentifier || lexemes[1].span.Start == lexemes[0].span.End {
		return false
	}

	return p.sourceTokenLexemeText(lexemes[0]) == keyword
}

func (p *Parser) isSourceHeader(lineToken sourceToken, keyword string) bool {
	lexemes, lineEnd, ok := p.sourceLineLexemes(lineToken)
	if !ok || len(lexemes) < 2 {
		return false
	}
	if len(lexemes) != 2 || lexemes[0].lexeme.kind != sourceLexemeIdentifier || lexemes[1].lexeme.kind != sourceLexemeSymbol {
		return false
	}

	first, second := lexemes[0], lexemes[1]

	return first.span.End == second.span.Start && strings.TrimSpace(p.source[second.span.End:lineEnd]) == "" && p.sourceTokenLexemeText(first) == keyword && p.sourceTokenLexemeText(second) == ":"
}

func (p *Parser) sourceTokenLexemeText(token sourceToken) string {
	if token.span.Start < 0 || token.span.End < token.span.Start || token.span.End > len(p.source) {
		return ""
	}

	return p.source[token.span.Start:token.span.End]
}

func (p *Parser) isExactElseHeader(lineToken sourceToken) bool {
	lexemes, lineEnd, ok := p.sourceLineLexemes(lineToken)
	if !ok || len(lexemes) != 2 {
		return false
	}
	keyword, colon := lexemes[0], lexemes[1]
	if keyword.lexeme.kind != sourceLexemeIdentifier || colon.lexeme.kind != sourceLexemeSymbol {
		return false
	}

	return keyword.span.Start == lineToken.span.Start && keyword.span.End == colon.span.Start && colon.span.End == lineEnd && p.sourceTokenLexemeText(keyword) == "else" && p.sourceTokenLexemeText(colon) == ":"
}

func expressionSourceHeader(source expressionSource, keyword string) (int, int, bool) {
	source = source.withChunkStarts()
	start, end := trimExpressionSourceRange(source, 0, source.Len())
	cursor := expressionSourceCursor{source: source, offset: start}
	name, ok := cursor.next()
	if !ok || name.kind != sourceLexemeIdentifier || source.Text(name.start, name.end) != keyword {
		return 0, 0, false
	}
	colon, ok := cursor.next()
	if !ok || colon.kind != sourceLexemeSymbol || name.end != colon.start || source.Text(colon.start, colon.end) != ":" || colon.end > end {
		return 0, 0, false
	}
	if _, ok := cursor.next(); ok {
		return 0, 0, false
	}

	return start, end, true
}

func expressionSourceRangeBeforeHeader(source expressionSource, keyword string) (int, int, int, int, bool) {
	source = source.withChunkStarts()
	start, end := trimExpressionSourceRange(source, 0, source.Len())
	cursor := expressionSourceCursor{source: source, offset: start}
	parenDepth, bracketDepth, braceDepth := 0, 0, 0
	var previous, last sourceLexeme
	for {
		lexeme, ok := cursor.next()
		if !ok || lexeme.start >= end {
			break
		}
		if lexeme.kind == sourceLexemeSymbol {
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
			}
		}
		if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
			previous, last = last, lexeme
		}
	}
	if previous.kind != sourceLexemeIdentifier || last.kind != sourceLexemeSymbol || previous.end != last.start || last.end != end || source.Text(previous.start, previous.end) != keyword || source.Text(last.start, last.end) != ":" || previous.start == start || source.ByteAt(previous.start-1) != ' ' {
		return 0, 0, 0, 0, false
	}
	baseStart, baseEnd := trimExpressionSourceRange(source, start, previous.start-1)
	if baseStart == baseEnd {
		return 0, 0, 0, 0, false
	}

	return baseStart, baseEnd, start, end, true
}

func expressionSourceHasHeaderSuffix(source expressionSource, keyword string) bool {
	source = source.withChunkStarts()
	start, end := trimExpressionSourceRange(source, 0, source.Len())
	if end-start <= len(keyword)+1 || source.ByteAt(end-1) != ':' {
		return false
	}
	keywordStart := end - len(keyword) - 1
	if source.ByteAt(keywordStart-1) != ' ' || source.Text(keywordStart, end-1) != keyword {
		return false
	}

	return true
}

func (p *Parser) withWithoutExpressionSource(source expressionSource, keyword string, parentIndent, line int) (parsedValue, bool, error) {
	baseStart, baseEnd, start, end, ok := expressionSourceRangeBeforeHeader(source, keyword)
	if !ok {
		if source.endsWithHeader(keyword) {
			return parsedValue{}, true, fmt.Errorf("line %d: expected %s: expression", line, keyword)
		}

		return parsedValue{}, false, nil
	}
	base, err := parseExpressionSourceRange(source, baseStart, baseEnd)
	if err != nil {
		return parsedValue{}, true, err
	}
	p.continued = nil
	if keyword == "with" {
		patchToken, hasLine, peekErr := p.peekParserToken()
		if peekErr != nil {
			return parsedValue{}, true, peekErr
		}
		if !hasLine || patchToken.indent <= parentIndent {
			return parsedValue{}, true, fmt.Errorf("line %d: expected an indented map after with", line)
		}
		patch, parseErr := p.blockWithExpression(patchToken.indent)
		if parseErr != nil {
			return parsedValue{}, true, parseErr
		}
		if !patch.isMap() {
			return parsedValue{}, true, fmt.Errorf("line %d: with: requires a map value", line)
		}
		patchSpan := patch.span()
		patchExpression, isMap := patch.Expression.(*MapExpr)
		if !isMap {
			return parsedValue{}, true, fmt.Errorf("line %d: with: requires a map value", line)
		}
		patchExpression.Span = patchSpan
		expression := &OverlayExpr{ExprBase: ExprBase{Span: source.PhysicalSpan(start, end)}, Base: base, Patch: patch.Expression}

		return parsedValue{Expression: expression}, true, nil
	}
	pathsToken, hasLine, peekErr := p.peekParserToken()
	if peekErr != nil {
		return parsedValue{}, true, peekErr
	}
	if !hasLine || pathsToken.indent <= parentIndent {
		return parsedValue{}, true, fmt.Errorf("line %d: expected field paths after without", line)
	}
	paths, parseErr := p.blockWithExpression(pathsToken.indent)
	if parseErr != nil {
		return parsedValue{}, true, parseErr
	}
	pathList, isList := paths.Expression.(*ListExpr)
	if !isList || len(pathList.Items) == 0 {
		return parsedValue{}, true, fmt.Errorf("line %d: without: requires a non-empty list of field paths", line)
	}
	pathNames := make([]string, 0, len(pathList.Items))
	pathSpans := make([]SourceSpan, 0, len(pathList.Items))
	for _, path := range pathList.Items {
		fieldPath, isPath := path.(*PathExpr)
		if !isPath {
			return parsedValue{}, true, fmt.Errorf("line %d: without: entries must be field paths", line)
		}
		pathNames = append(pathNames, fieldPath.Path)
		pathSpans = append(pathSpans, fieldPath.ExprSpan())
	}
	expression := &RemovalExpr{ExprBase: ExprBase{Span: source.PhysicalSpan(start, end)}, Base: base, Paths: pathNames, PathSpans: pathSpans}

	return parsedValue{Expression: expression}, true, nil
}

func (p *Parser) continuedExpressionFromSource(lineNumber int, firstSpan, scannerSpan SourceSpan, parentIndent int) (continuedSource, error) {
	continued := continuedSource{
		sourceRange: firstSpan,
	}
	depthScanner := continuedExpressionDepth{}
	depth, err := depthScanner.scanTokens(p, scannerSpan)
	if err != nil {
		return continuedSource{}, err
	}
	var boundaries []continuationLineBoundary
	for depth > 0 {
		partBoundary, hasPart := p.peekContinuationLine()
		if !hasPart {
			return continuedSource{}, fmt.Errorf("line %d: unterminated function call", lineNumber)
		}
		part := partBoundary.line
		partIndent := part.indent
		if partIndent < parentIndent || partIndent == parentIndent && !p.onlyClosingParentheses(part) {
			return continuedSource{}, fmt.Errorf("line %d: unterminated function call", lineNumber)
		}
		if err := p.consumeContinuationLine(partBoundary); err != nil {
			return continuedSource{}, err
		}
		boundaries = append(boundaries, partBoundary)
		depth, err = depthScanner.scanTokens(p, part.span)
		if err != nil {
			return continuedSource{}, err
		}
	}
	for _, boundary := range boundaries {
		continued.sourceRange.End = boundary.line.span.End
		if boundary.line.triviaOnly && (boundary.line.triviaOffset < 0 || boundary.line.triviaText == "") {
			return continuedSource{}, errors.New("continued expression trivia has no source span")
		}
	}
	if continued.sourceRange.Start < 0 || continued.sourceRange.End < continued.sourceRange.Start || continued.sourceRange.End > len(p.source) {
		return continuedSource{}, errors.New("continued expression source span is outside the source")
	}
	var ok bool
	continued.maskedSpans, ok = p.expressionCommentSpans(continued.sourceRange)
	if !ok {
		return continuedSource{}, errors.New("continued expression comment spans are inconsistent")
	}
	continued.lexemes, ok = p.expressionLexemesForPhysicalRange(continued.sourceRange)
	if !ok {
		return continuedSource{}, errors.New("unable to select expression tokens for continued source span")
	}

	return continued, nil
}

func (p *Parser) expressionCommentSpans(span SourceSpan) ([]SourceSpan, bool) {
	if span.Start < 0 || span.End < span.Start || span.End > len(p.source) {
		return nil, false
	}
	var spans []SourceSpan
	firstToken := sort.Search(len(p.tokens), func(index int) bool {
		return p.tokens[index].span.Start >= span.Start
	})
	if firstToken > 0 {
		firstToken--
	}
	for _, token := range p.tokens[firstToken:] {
		if token.span.Start >= span.End {
			break
		}
		if token.span.End <= span.Start {
			continue
		}
		if token.kind == sourceTokenTrivia && token.triviaOnly {
			if token.span.Start >= span.Start && token.span.End <= span.End && (token.triviaOffset < span.Start || token.triviaText == "") {
				return nil, false
			}
		}
		if token.kind != sourceTokenTrivia || token.triviaText == "" {
			continue
		}
		commentStart := token.triviaOffset
		commentEnd := commentStart + len(token.triviaText)
		start := max(commentStart, span.Start)
		end := min(commentEnd, span.End)
		if start >= end {
			continue
		}
		spans = append(spans, SourceSpan{Start: start, End: end})
	}

	return spans, true
}

func (p *Parser) expressionLexemesForPhysicalRange(span SourceSpan) ([]sourceLexeme, bool) {
	if span.Start < 0 || span.End < span.Start || span.End > len(p.source) {
		return nil, false
	}

	return p.expressionLexemesForSpan(span, 0)
}

func (p *Parser) expressionLexemesForSpan(span SourceSpan, expressionStart int) ([]sourceLexeme, bool) {
	if span.Start < 0 || span.End < span.Start || span.End > len(p.source) {
		return nil, false
	}
	lexemes := make([]sourceLexeme, 0)
	cursor := p.sourceSpanLexemeCursor(span)
	for {
		token, ok := cursor.next()
		if !ok {
			return lexemes, true
		}
		if token.span.Start < span.Start || token.span.End > span.End || token.span.Start < 0 || token.span.End > len(p.source) {
			return nil, false
		}
		if token.lexeme.start < 0 || token.lexeme.end < token.lexeme.start {
			return nil, false
		}
		if token.lexeme.end-token.lexeme.start != token.span.End-token.span.Start {
			return nil, false
		}
		if token.lexeme.kind == sourceLexemeLiteral && !sourceLiteralLexemeClosed(p.source[token.span.Start:token.span.End]) {
			return nil, false
		}
		lexemes = append(lexemes, sourceLexeme{
			kind:  token.lexeme.kind,
			start: expressionStart + token.span.Start - span.Start,
			end:   expressionStart + token.span.End - span.Start,
		})
	}
}

func sourceLiteralLexemeClosed(text string) bool {
	if len(text) < 2 || text[0] != '"' || text[len(text)-1] != '"' {
		return false
	}
	if text == `""` {
		return true
	}
	escapes := 0
	for index := len(text) - 2; index >= 0 && text[index] == '\\'; index-- {
		escapes++
	}

	return escapes%2 == 0
}

func (p *Parser) onlyClosingParentheses(lineToken sourceToken) bool {
	if lineToken.span.Start < 0 || lineToken.span.End <= lineToken.span.Start || lineToken.span.End > len(p.source) {
		return false
	}
	cursor := p.sourceSpanLexemeCursor(lineToken.span)
	position := lineToken.span.Start
	for {
		token, ok := cursor.next()
		if !ok {
			return true
		}
		if token.span.Start < position || token.span.End <= token.span.Start || token.span.End > lineToken.span.End || token.lexeme.kind != sourceLexemeSymbol || p.sourceTokenLexemeText(token) != ")" {
			return false
		}
		position = token.span.End
	}
}

type continuedExpressionDepth struct {
	depth int
}

func (scanner *continuedExpressionDepth) scanTokens(parser *Parser, bounds SourceSpan) (int, error) {
	cursor := sourceSpanLexemeCursor{tokens: parser.tokens, bounds: bounds}
	for {
		token, ok := cursor.next()
		if !ok {
			return scanner.depth, nil
		}
		if token.lexeme.kind != sourceLexemeSymbol {
			continue
		}
		switch parser.sourceTokenLexemeText(token) {
		case "(":
			scanner.depth++
		case ")":
			scanner.depth--
			if scanner.depth < 0 {
				return 0, errors.New("unmatched closing parenthesis")
			}
		}
	}
}

func (p *Parser) branchValueWithExpression(indent, line int) (parsedValue, error) {
	currentToken, ok, err := p.peekParserToken()
	if err != nil {
		return parsedValue{}, err
	}
	if !ok || currentToken.indent != indent {
		return parsedValue{}, fmt.Errorf("line %d: expected branch value", line)
	}
	if _, isListItem := p.sourceListItemSourceSpan(currentToken); isListItem {
		return p.blockWithExpression(indent)
	}
	if p.hasPairSourceSpan(currentToken.span) {
		return p.blockWithExpression(indent)
	}
	if err := p.consumeLine(currentToken); err != nil {
		return parsedValue{}, err
	}
	continued, err := p.continuedExpressionSourceSpan(currentToken, currentToken.span, indent-1)
	if err != nil {
		return parsedValue{}, err
	}

	return p.valueExpressionWithContinued(continued, indent-1, currentToken.lineNumber)
}

func isSpace(char rune) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r'
}

func identifier(text string) bool {
	if text == "" || !isLetter(rune(text[0])) && text[0] != '_' {
		return false
	}
	for _, char := range text[1:] {
		if !isLetter(char) && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}

	return true
}

func isLetter(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}

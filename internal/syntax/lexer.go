package syntax

import (
	"fmt"
	"sort"
	"strings"
)

type sourceTokenKind uint8

const (
	sourceTokenLexeme sourceTokenKind = iota
	sourceTokenNewline
	sourceTokenIndent
	sourceTokenDedent
	sourceTokenTrivia
)

type sourceToken struct {
	kind         sourceTokenKind
	indent       int
	lineNumber   int
	lexeme       sourceLexeme
	span         SourceSpan
	lineSpan     SourceSpan
	literals     []sourceLiteralToken
	triviaText   string
	triviaOffset int
	triviaOnly   bool
}

type sourceSpanLexemeCursor struct {
	tokens []sourceToken
	index  int
	bounds SourceSpan
}

func (cursor *sourceSpanLexemeCursor) next() (sourceToken, bool) {
	for cursor.index < len(cursor.tokens) {
		token := cursor.tokens[cursor.index]
		cursor.index++
		if token.kind == sourceTokenLexeme {
			if token.span.End <= cursor.bounds.Start {
				continue
			}
			if token.span.Start >= cursor.bounds.End {
				return sourceToken{}, false
			}
			if token.span.Start < cursor.bounds.Start || token.span.End > cursor.bounds.End {
				continue
			}

			return token, true
		}
	}

	return sourceToken{}, false
}

type sourceLiteralToken struct {
	value string
	span  SourceSpan
}

type sourceLexemeKind uint8

const (
	sourceLexemeIdentifier sourceLexemeKind = iota
	sourceLexemeLiteral
	sourceLexemeSymbol
	sourceLexemeNumber
)

type sourceLexeme struct {
	kind       sourceLexemeKind
	start, end int
}

type expressionSourceCursor struct {
	source expressionSource
	offset int
}

func (cursor *expressionSourceCursor) next() (sourceLexeme, bool) {
	cursor.source = cursor.source.withChunkStarts()
	index := sort.Search(len(cursor.source.lexemes), func(index int) bool {
		return cursor.source.lexemes[index].end > cursor.offset
	})
	if index == len(cursor.source.lexemes) {
		return sourceLexeme{}, false
	}
	lexeme := cursor.source.lexemes[index]
	cursor.offset = lexeme.end

	return lexeme, true
}

type sourceLexer struct {
	indentStack          []int
	parentheses          int
	source               string
	offset               int
	lineNumber           int
	lineStartNumber      int
	lineStart            int
	lineContentStart     int
	lineCodeEnd          int
	lineIndent           int
	lineParentheses      int
	lineParenthesisReset bool
	lineActive           bool
	lineComment          string
	lineCommentOffset    int
	lineCommentEmitted   bool
	triviaOnly           bool
	lineLiterals         []sourceLiteralToken
	pending              []sourceToken
	eof                  bool
}

func lexSource(source string) ([]sourceToken, error) {
	lexer := sourceLexer{source: source, indentStack: []int{0}, lineNumber: 1, lineCommentOffset: -1}
	tokens := make([]sourceToken, 0)
	for {
		token, ok, err := lexer.nextToken()
		if err != nil {
			offset := min(max(lexer.offset, 0), len(source))
			span := SourceSpan{Start: offset, End: min(offset+1, len(source))}
			if span.Start == span.End && span.Start > 0 {
				span.Start--
			}

			return nil, syntaxDiagnostic(err, span)
		}
		if !ok {
			break
		}
		tokens = append(tokens, token)
	}

	return tokens, nil
}

func (lexer *sourceLexer) nextToken() (sourceToken, bool, error) {
	for {
		if len(lexer.pending) > 0 {
			token := lexer.pending[0]
			lexer.pending = lexer.pending[1:]

			return token, true, nil
		}
		if lexer.lineActive {
			token, ok, err := lexer.nextLineToken()
			if err != nil || ok {
				return token, ok, err
			}

			continue
		}
		if lexer.eof {
			if len(lexer.indentStack) > 1 {
				lexer.indentStack = lexer.indentStack[:len(lexer.indentStack)-1]

				return sourceToken{kind: sourceTokenDedent}, true, nil
			}

			return sourceToken{}, false, nil
		}

		if err := lexer.startLine(); err != nil {
			return sourceToken{}, false, err
		}
	}
}

func (lexer *sourceLexer) nextLineToken() (sourceToken, bool, error) {
	for lexer.offset < len(lexer.source) {
		char := lexer.source[lexer.offset]
		if char == '\n' || char == '\r' {
			break
		}
		if char == ' ' || char == '\t' {
			if char == '\t' {
				return sourceToken{}, false, fmt.Errorf("line %d: tabs are not allowed", lexer.lineNumber)
			}
			lexer.offset++

			continue
		}
		if char == '#' && (lexer.offset == lexer.lineContentStart || lexer.source[lexer.offset-1] == ' ') {
			lexer.lineCommentOffset = lexer.offset
			end := lexer.physicalLineEnd(lexer.offset)
			commentEnd := end
			if commentEnd > lexer.offset && lexer.source[commentEnd-1] == '\r' {
				commentEnd--
			}
			lexer.lineComment = lexer.source[lexer.offset:commentEnd]
			if strings.ContainsRune(lexer.lineComment, '\t') {
				return sourceToken{}, false, fmt.Errorf("line %d: tabs are not allowed", lexer.lineStartNumber)
			}
			lexer.lineCodeEnd = lexer.offset
			for lexer.lineCodeEnd > lexer.lineContentStart && lexer.source[lexer.lineCodeEnd-1] == ' ' {
				lexer.lineCodeEnd--
			}
			lexer.offset = end

			break
		}
		lexemeLine := lexer.lineStartNumber
		lexeme, err := lexer.scanLexeme()
		if err != nil {
			return sourceToken{}, false, err
		}
		if lexeme.kind == sourceLexemeLiteral && strings.HasPrefix(lexer.source[lexeme.start:lexeme.end], `"""`) {
			value, literalEnd, triviaOffset, triviaText, err := lexer.readMultilineLiteral(lexeme.start)
			if err != nil {
				return sourceToken{}, false, err
			}
			lexeme.end = literalEnd
			lexer.offset = literalEnd
			lexer.lineCodeEnd = literalEnd
			lexer.lineLiterals = append(lexer.lineLiterals, sourceLiteralToken{value: value, span: SourceSpan{Start: lexeme.start, End: literalEnd}})
			if triviaOffset >= 0 {
				lexer.lineCommentOffset, lexer.lineComment = triviaOffset, triviaText
				lexer.lineCodeEnd = triviaOffset
				for lexer.lineCodeEnd > lexer.lineContentStart && lexer.source[lexer.lineCodeEnd-1] == ' ' {
					lexer.lineCodeEnd--
				}
			}
		}
		lexer.updateLineParenthesisDepth(lexeme)

		return sourceToken{kind: sourceTokenLexeme, lineNumber: lexemeLine, lexeme: lexeme, span: SourceSpan{Start: lexeme.start, End: lexeme.end}}, true, nil
	}
	if lexer.lineComment != "" && !lexer.lineCommentEmitted {
		lexer.lineCommentEmitted = true

		return sourceToken{kind: sourceTokenTrivia, indent: lexer.lineIndent, lineNumber: lexer.lineStartNumber, span: SourceSpan{Start: lexer.lineCommentOffset, End: lexer.lineCommentOffset + len(lexer.lineComment)}, triviaText: lexer.lineComment, triviaOffset: lexer.lineCommentOffset}, true, nil
	}
	if lexer.lineComment == "" {
		lexer.lineCodeEnd = lexer.physicalLineEnd(lexer.offset)
		if lexer.lineCodeEnd > lexer.lineContentStart && lexer.source[lexer.lineCodeEnd-1] == '\r' {
			lexer.lineCodeEnd--
		}
	}
	lineEnd := lexer.lineCodeEnd
	if lineEnd < lexer.lineContentStart {
		lineEnd = lexer.offset
	}
	lineSpan := SourceSpan{Start: lexer.lineContentStart, End: lineEnd}
	lineNumber := lexer.lineStartNumber
	indent := lexer.lineIndent
	if lexer.triviaOnly {
		lexer.finishLine()

		return sourceToken{}, false, nil
	}
	lexer.finishLine()

	return sourceToken{kind: sourceTokenNewline, span: SourceSpan{Start: lineEnd, End: lineEnd}, lineSpan: lineSpan, indent: indent, lineNumber: lineNumber, literals: lexer.lineLiterals, triviaText: lexer.lineComment}, true, nil
}

func (lexer *sourceLexer) updateLineParenthesisDepth(lexeme sourceLexeme) {
	if lexer.lineParenthesisReset || lexeme.kind != sourceLexemeSymbol {
		return
	}
	lexemeText := lexer.source[lexeme.start:lexeme.end]
	switch lexemeText {
	case "(":
		lexer.lineParentheses++
	case ")":
		lexer.lineParentheses--
		if lexer.lineParentheses < 0 {
			lexer.lineParentheses = 0
			lexer.lineParenthesisReset = true
		}
	}
}

func (lexer *sourceLexer) startLine() error {
	if lexer.offset > len(lexer.source) {
		lexer.eof = true

		return nil
	}
	lexer.lineStart = lexer.offset
	lexer.lineStartNumber = lexer.lineNumber
	lexer.lineIndent = 0
	lexer.lineComment, lexer.lineCommentOffset = "", -1
	lexer.lineCommentEmitted, lexer.triviaOnly = false, false
	lexer.lineLiterals = nil
	lexer.lineParentheses, lexer.lineParenthesisReset = lexer.parentheses, false
	for lexer.offset < len(lexer.source) && lexer.source[lexer.offset] == ' ' {
		lexer.offset++
		lexer.lineIndent++
	}
	if lexer.offset < len(lexer.source) && lexer.source[lexer.offset] == '\t' {
		return fmt.Errorf("line %d: tabs are not allowed", lexer.lineNumber)
	}
	lexer.lineContentStart, lexer.lineCodeEnd = lexer.offset, lexer.offset
	lineEnd := lexer.physicalLineEnd(lexer.offset)
	first := lexer.offset
	if first == lineEnd || lexer.source[first] == '\r' || lexer.source[first] == '#' && (first == lexer.lineStart+lexer.lineIndent || lexer.source[first-1] == ' ') {
		lexer.triviaOnly = true
		spanEnd := lineEnd
		if first < lineEnd && lexer.source[first] == '#' {
			commentEnd := lineEnd
			if commentEnd > first && lexer.source[commentEnd-1] == '\r' {
				commentEnd--
			}
			lexer.lineComment, lexer.lineCommentOffset = lexer.source[first:commentEnd], first
			if strings.ContainsRune(lexer.lineComment, '\t') {
				return fmt.Errorf("line %d: tabs are not allowed", lexer.lineStartNumber)
			}
			spanEnd = commentEnd
		}
		lexer.pending = append(lexer.pending, sourceToken{kind: sourceTokenTrivia, indent: lexer.lineIndent, lineNumber: lexer.lineStartNumber, span: SourceSpan{Start: lexer.lineStart, End: spanEnd}, triviaText: lexer.lineComment, triviaOffset: lexer.lineCommentOffset, triviaOnly: true})
		lexer.offset = lineEnd
		lexer.lineComment, lexer.lineCommentOffset = "", -1
		lexer.lineActive = true

		return nil
	}
	if lexer.parentheses == 0 {
		for lexer.lineIndent < lexer.indentStack[len(lexer.indentStack)-1] {
			lexer.indentStack = lexer.indentStack[:len(lexer.indentStack)-1]
			lexer.pending = append(lexer.pending, sourceToken{kind: sourceTokenDedent, span: SourceSpan{Start: lexer.lineStart, End: lexer.lineStart}})
		}
		if lexer.lineIndent < lexer.indentStack[len(lexer.indentStack)-1] {
			return fmt.Errorf("line %d: inconsistent indentation", lexer.lineNumber)
		}
		if lexer.lineIndent > lexer.indentStack[len(lexer.indentStack)-1] {
			lexer.indentStack = append(lexer.indentStack, lexer.lineIndent)
			lexer.pending = append(lexer.pending, sourceToken{kind: sourceTokenIndent, span: SourceSpan{Start: lexer.lineStart, End: lexer.lineStart}})
		}
	}
	lexer.lineActive = true

	return nil
}

func (lexer *sourceLexer) scanLexeme() (sourceLexeme, error) {
	start := lexer.offset
	if lexer.source[start] >= '0' && lexer.source[start] <= '9' {
		lexer.scanNumber()
		lexer.lineCodeEnd = lexer.offset

		return sourceLexeme{kind: sourceLexemeNumber, start: start, end: lexer.offset}, nil
	}
	if strings.HasPrefix(lexer.source[start:], `"""`) {
		return sourceLexeme{kind: sourceLexemeLiteral, start: start, end: start + 3}, nil
	}
	char := lexer.source[lexer.offset]
	if char == '"' {
		lexer.offset++
		escaped := false
		for lexer.offset < len(lexer.source) && lexer.source[lexer.offset] != '\n' && lexer.source[lexer.offset] != '\r' {
			current := lexer.source[lexer.offset]
			if current == '\t' {
				return sourceLexeme{}, fmt.Errorf("line %d: tabs are not allowed", lexer.lineStartNumber)
			}
			lexer.offset++
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				break
			}
		}
		lexer.lineCodeEnd = lexer.offset

		return sourceLexeme{kind: sourceLexemeLiteral, start: start, end: lexer.offset}, nil
	}
	if isSourceIdentifierStart(char) {
		lexer.offset++
		for lexer.offset < len(lexer.source) && isSourceIdentifierContinue(lexer.source[lexer.offset]) {
			lexer.offset++
		}
		lexer.lineCodeEnd = lexer.offset

		return sourceLexeme{kind: sourceLexemeIdentifier, start: start, end: lexer.offset}, nil
	}
	if lexer.offset+1 < len(lexer.source) {
		switch lexer.source[lexer.offset : lexer.offset+2] {
		case "->", "=>", "==", "!=", "<=", ">=", "??":
			lexer.offset += 2
		default:
			lexer.offset++
		}
	} else {
		lexer.offset++
	}
	lexer.lineCodeEnd = lexer.offset

	return sourceLexeme{kind: sourceLexemeSymbol, start: start, end: lexer.offset}, nil
}

func (lexer *sourceLexer) scanNumber() {
	for lexer.offset < len(lexer.source) && lexer.source[lexer.offset] >= '0' && lexer.source[lexer.offset] <= '9' {
		lexer.offset++
	}
	if lexer.offset+1 < len(lexer.source) && lexer.source[lexer.offset] == '.' && lexer.source[lexer.offset+1] >= '0' && lexer.source[lexer.offset+1] <= '9' {
		lexer.offset++
		for lexer.offset < len(lexer.source) && lexer.source[lexer.offset] >= '0' && lexer.source[lexer.offset] <= '9' {
			lexer.offset++
		}
	}
	exponent := lexer.offset
	if lexer.offset < len(lexer.source) && (lexer.source[lexer.offset] == 'e' || lexer.source[lexer.offset] == 'E') {
		lexer.offset++
		if lexer.offset < len(lexer.source) && (lexer.source[lexer.offset] == '+' || lexer.source[lexer.offset] == '-') {
			lexer.offset++
		}
		digits := lexer.offset
		for lexer.offset < len(lexer.source) && lexer.source[lexer.offset] >= '0' && lexer.source[lexer.offset] <= '9' {
			lexer.offset++
		}
		if digits == lexer.offset {
			lexer.offset = exponent
		}
	}
}

func (lexer *sourceLexer) readMultilineLiteral(start int) (string, int, int, string, error) {
	lineEnd := lexer.physicalLineEnd(start)
	prefix := lexer.source[lexer.lineStart:start]
	if strings.ContainsRune(prefix, '\t') {
		return "", 0, -1, "", fmt.Errorf("line %d: tabs are not allowed", lexer.lineNumber)
	}
	afterOpen := start + 3
	openingEnd := lineEnd
	if openingEnd > start && lexer.source[openingEnd-1] == '\r' {
		openingEnd--
	}
	if closeRel := strings.Index(lexer.source[afterOpen:openingEnd], `"""`); closeRel >= 0 {
		closeStart := afterOpen + closeRel
		closeEnd := closeStart + 3
		lineContentEnd := openingEnd
		after := lexer.source[closeEnd:lineContentEnd]
		if strings.ContainsRune(after, '\t') {
			return "", 0, -1, "", fmt.Errorf("line %d: tabs are not allowed", lexer.lineNumber)
		}
		suffix, trivia, triviaRel := splitSourceComment(after)
		end := closeEnd + len(suffix)
		triviaOffset := -1
		if triviaRel >= 0 {
			triviaOffset = closeEnd + triviaRel
		}

		return multilineStringContent(lexer.source[afterOpen:closeStart]), end, triviaOffset, trivia, nil
	}
	if strings.TrimSpace(lexer.source[afterOpen:openingEnd]) != "" {
		return "", 0, -1, "", fmt.Errorf("line %d: multiline string content must start on the next line", lexer.lineNumber)
	}
	contentStart := lineEnd
	if contentStart < len(lexer.source) && lexer.source[contentStart] == '\n' {
		contentStart++
	}
	if contentStart == lineEnd {
		return "", 0, -1, "", fmt.Errorf("line %d: unterminated multiline string", lexer.lineNumber)
	}
	lineEnding := "\n"
	if lineEnd > start && lexer.source[lineEnd-1] == '\r' {
		lineEnding = "\r\n"
	}
	candidateStart := contentStart
	for candidateStart <= len(lexer.source) {
		candidateEnd := lexer.physicalLineEnd(candidateStart)
		candidateContentEnd := candidateEnd
		if candidateContentEnd > candidateStart && lexer.source[candidateContentEnd-1] == '\r' {
			candidateContentEnd--
		}
		candidate := lexer.source[candidateStart:candidateContentEnd]
		if closeRel := strings.Index(candidate, `"""`); closeRel >= 0 && strings.TrimSpace(candidate[:closeRel]) == "" {
			closeStart := candidateStart + closeRel
			before, after := candidate[:closeRel], candidate[closeRel+3:]
			if strings.ContainsRune(before, '\t') || strings.ContainsRune(after, '\t') {
				return "", 0, -1, "", fmt.Errorf("line %d: tabs are not allowed", lexer.lineNumber+strings.Count(lexer.source[start:candidateStart], "\n"))
			}
			suffix, trivia, triviaRel := splitSourceComment(after)
			end := closeStart + 3 + len(suffix)
			triviaOffset := -1
			if triviaRel >= 0 {
				triviaOffset = closeStart + 3 + triviaRel
			}
			content := strings.TrimSuffix(lexer.source[contentStart:candidateStart], "\n")
			value := multilineStringContent(lineEnding + content + "\n" + before)
			lexer.lineNumber += strings.Count(lexer.source[start:end], "\n")
			lexer.offset = end
			lexer.lineCodeEnd = end

			return value, end, triviaOffset, trivia, nil
		}
		if candidateEnd >= len(lexer.source) {
			break
		}
		candidateStart = candidateEnd + 1
	}

	return "", 0, -1, "", fmt.Errorf("line %d: unterminated multiline string", lexer.lineNumber)
}

func (lexer *sourceLexer) physicalLineEnd(offset int) int {
	if relative := strings.IndexByte(lexer.source[offset:], '\n'); relative >= 0 {
		return offset + relative
	}

	return len(lexer.source)
}

func (lexer *sourceLexer) finishLine() {
	lexer.parentheses = lexer.lineParentheses
	end := lexer.physicalLineEnd(lexer.offset)
	if end < len(lexer.source) {
		lexer.offset = end + 1
		lexer.lineNumber++
	} else {
		lexer.offset = len(lexer.source) + 1
	}
	lexer.lineActive = false
}

func splitSourceComment(line string) (string, string, int) {
	commentOffset := sourceCommentOffset(line, 0, len(line))
	if commentOffset < 0 {
		return line, "", -1
	}

	return strings.TrimRight(line[:commentOffset], " "), line[commentOffset:], commentOffset
}

func sourceCommentOffset(source string, start, end int) int {
	quoted := false
	escaped := false
	for index := start; index < end; index++ {
		char := source[index]
		if quoted {
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				quoted = false
			}

			continue
		}
		if char == '"' {
			quoted = true
		} else if char == '#' && (index == start || source[index-1] == ' ') {
			return index
		}
	}

	return -1
}

func isSourceIdentifierStart(char byte) bool {
	return char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}

func isSourceIdentifierContinue(char byte) bool {
	return isSourceIdentifierStart(char) || char >= '0' && char <= '9'
}

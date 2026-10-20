package syntax

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (p *Parser) typeRecord(line int) (TypeExpr, error) {
	type pendingTypeField struct {
		field    TypeField
		typeSpan SourceSpan
	}
	fields := make([]pendingTypeField, 0)
	seen := make(map[string]bool)
	if !p.consumeToken(sourceTokenIndent) {
		return TypeExpr{}, fmt.Errorf("line %d: expected an indented type definition", line)
	}
	for {
		fieldTokens, fieldLine, hasField, err := p.nextTypeRecordField(line)
		if err != nil {
			return TypeExpr{}, err
		}
		if !hasField {
			break
		}
		header, ok := p.typeRecordFieldHeader(fieldTokens)
		if !ok || header.typeSpan.Start == header.typeSpan.End {
			return TypeExpr{}, fmt.Errorf("line %d: expected type field: Type", fieldLine)
		}
		if !identifier(header.name) {
			return TypeExpr{}, fmt.Errorf("line %d: invalid type field %q", fieldLine, header.name)
		}
		if seen[header.name] {
			return TypeExpr{}, fmt.Errorf("line %d: duplicate type field %q", fieldLine, header.name)
		}
		seen[header.name] = true
		fields = append(fields, pendingTypeField{
			field:    TypeField{Name: header.name, NameSpan: header.nameSpan, Optional: header.optional},
			typeSpan: header.typeSpan,
		})
	}
	if len(fields) == 0 {
		return TypeExpr{}, fmt.Errorf("line %d: type record cannot be empty", line)
	}
	first := fields[0]
	last := fields[len(fields)-1]
	typeExpr := TypeExpr{
		Span:   SourceSpan{Start: first.field.NameSpan.Start, End: last.typeSpan.End},
		Fields: make([]TypeField, 0, len(fields)),
	}
	for _, pending := range fields {
		fieldType, err := p.parseTypeExpressionSourceSpan(pending.typeSpan)
		if err != nil {
			return TypeExpr{}, fmt.Errorf("line %d: %w", line, err)
		}
		pending.field.Type = fieldType
		typeExpr.Fields = append(typeExpr.Fields, pending.field)
	}

	return typeExpr, nil
}

func (p *Parser) nextTypeRecordField(parentLine int) ([]sourceToken, int, bool, error) {
	fieldTokens := make([]sourceToken, 0)
	fieldLine := 0
	for p.tokenIndex < len(p.tokens) {
		token := p.tokens[p.tokenIndex]
		switch token.kind {
		case sourceTokenTrivia:
			p.tokenIndex++
		case sourceTokenLexeme:
			if fieldLine == 0 {
				fieldLine = token.lineNumber
			}
			fieldTokens = append(fieldTokens, token)
			p.tokenIndex++
		case sourceTokenNewline:
			p.tokenIndex++
			if len(fieldTokens) == 0 {
				continue
			}

			return fieldTokens, fieldLine, true, nil
		case sourceTokenDedent:
			p.tokenIndex++

			return nil, 0, false, nil
		default:
			return nil, 0, false, fmt.Errorf("line %d: expected an indented type definition", parentLine)
		}
	}

	return nil, 0, false, nil
}

func (p *Parser) typeRecordFieldHeader(lexemes []sourceToken) (typeRecordFieldHeader, bool) {
	parenDepth, bracketDepth := 0, 0
	var firstKey, lastKey, separator, firstValue, lastValue sourceToken
	foundKey, foundSeparator, foundValue := false, false, false
	for _, token := range lexemes {
		if foundSeparator {
			if !foundValue {
				firstValue = token
				foundValue = true
			}
			lastValue = token

			continue
		}
		if token.lexeme.kind == sourceLexemeSymbol {
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
					separator = token
					foundSeparator = true

					continue
				}
			}
		}
		if !foundKey {
			firstKey = token
			foundKey = true
		}
		lastKey = token
	}
	if !foundSeparator {
		return typeRecordFieldHeader{}, false
	}
	header := typeRecordFieldHeader{
		nameSpan: SourceSpan{Start: separator.span.Start, End: separator.span.Start},
		typeSpan: SourceSpan{Start: separator.span.End, End: separator.span.End},
	}
	if foundKey {
		header.nameSpan = SourceSpan{Start: firstKey.span.Start, End: lastKey.span.End}
		if p.sourceTokenLexemeText(lastKey) == "?" {
			header.optional = true
			header.nameSpan.End = lastKey.span.Start
		}
		if header.nameSpan.Start >= 0 && header.nameSpan.End >= header.nameSpan.Start && header.nameSpan.End <= len(p.source) {
			header.name = p.source[header.nameSpan.Start:header.nameSpan.End]
		}
	}
	if foundValue {
		header.typeSpan = SourceSpan{Start: firstValue.span.Start, End: lastValue.span.End}
	}

	return header, true
}

func parseTypeExpressionSource(source expressionSource, start, end int) (TypeExpr, error) {
	start, end = trimTypeSourceRange(source, start, end)
	if start == end {
		return TypeExpr{}, errors.New("expected type expression")
	}
	parts := splitTypeSourceRanges(source, start, end, '|', false)
	if len(parts) > 1 {
		typeExpr := TypeExpr{Span: source.PhysicalSpan(start, end), Options: make([]TypeExpr, 0, len(parts))}
		for _, part := range parts {
			option, err := parseTypeExpressionSource(source, part[0], part[1])
			if err != nil {
				return TypeExpr{}, err
			}
			typeExpr.Options = append(typeExpr.Options, option)
		}

		return typeExpr, nil
	}
	if expressionSourceHasPrefix(source, start, end, "schema(") && source.ByteAt(end-1) == ')' {
		pathStart, pathEnd := trimTypeSourceRange(source, start+len("schema("), end-1)
		path, err := strconv.Unquote(source.Text(pathStart, pathEnd))
		if err != nil || path == "" {
			return TypeExpr{}, errors.New("schema() expects a non-empty quoted local path or HTTPS URL")
		}

		return TypeExpr{Span: source.PhysicalSpan(start, end), SchemaPath: path}, nil
	}
	if source.ByteAt(start) == '{' && source.ByteAt(end-1) == '}' {
		typeExpr := TypeExpr{Span: source.PhysicalSpan(start, end), Fields: make([]TypeField, 0)}
		for _, fieldRange := range splitTypeSourceRanges(source, start+1, end-1, ',', true) {
			colon := sourceRangeByteIndex(source, fieldRange[0], fieldRange[1], ':')
			nameEndRange := fieldRange[1]
			if colon >= 0 {
				nameEndRange = colon
			}
			nameStart, nameEnd := trimTypeSourceRange(source, fieldRange[0], nameEndRange)
			fieldNameEnd := nameEnd
			optional := fieldNameEnd > nameStart && source.ByteAt(fieldNameEnd-1) == '?'
			if optional {
				fieldNameEnd--
			}
			name := source.Text(nameStart, fieldNameEnd)
			if colon < 0 || !identifier(name) {
				return TypeExpr{}, fmt.Errorf("invalid type field %q", source.Text(nameStart, nameEnd))
			}
			fieldTypeExpr, err := parseTypeExpressionSource(source, colon+1, fieldRange[1])
			if err != nil {
				return TypeExpr{}, err
			}
			typeExpr.Fields = append(typeExpr.Fields, TypeField{
				Name:     name,
				NameSpan: source.PhysicalSpan(nameStart, fieldNameEnd),
				Type:     fieldTypeExpr,
				Optional: optional,
			})
		}

		return typeExpr, nil
	}
	if open := sourceRangeByteIndex(source, start, end, '<'); open > start && source.ByteAt(end-1) == '>' {
		nameStart, nameEnd := trimTypeSourceRange(source, start, open)
		name := source.Text(nameStart, nameEnd)
		if !qualifiedIdentifier(name) {
			return TypeExpr{}, fmt.Errorf("invalid generic type %q", name)
		}
		arguments, err := splitTypeExpressionArgumentRanges(source, open+1, end-1)
		if err != nil {
			return TypeExpr{}, err
		}
		parsedArguments := make([]TypeExpr, 0, len(arguments))
		for _, argument := range arguments {
			parsed, err := parseTypeExpressionSource(source, argument[0], argument[1])
			if err != nil {
				return TypeExpr{}, err
			}
			parsedArguments = append(parsedArguments, parsed)
		}
		if name == typeFunction {
			if len(parsedArguments) < 2 {
				return TypeExpr{}, errors.New("function expects at least 2 type arguments")
			}
			parameters := make([]FunctionTypeParameter, len(parsedArguments)-1)
			for index, argument := range parsedArguments[:len(parsedArguments)-1] {
				parameters[index] = FunctionTypeParameter{Type: argument}
			}

			return TypeExpr{Span: source.PhysicalSpan(start, end), Name: name, Parameters: parameters, ReturnType: new(parsedArguments[len(parsedArguments)-1])}, nil
		}
		if name == typeList || name == typeMap || name == typeNode {
			expected := map[string]int{typeList: 1, typeMap: 2, typeNode: 1}[name]
			if len(parsedArguments) != expected {
				return TypeExpr{}, fmt.Errorf("%s expects %d type arguments", name, expected)
			}
		} else if isBuiltinType(name) {
			return TypeExpr{}, fmt.Errorf("%s does not accept type arguments", name)
		}
		if name == typeList {
			return TypeExpr{Span: source.PhysicalSpan(start, end), Name: name, Element: &parsedArguments[0]}, nil
		}
		if name == typeMap {
			if parsedArguments[0].Name != typeString || len(parsedArguments[0].Arguments) != 0 {
				return TypeExpr{}, errors.New("map keys must have type String")
			}

			return TypeExpr{Span: source.PhysicalSpan(start, end), Name: name, Element: &parsedArguments[1]}, nil
		}
		if name == typeNode {
			return TypeExpr{Span: source.PhysicalSpan(start, end), Name: name, Arguments: parsedArguments}, nil
		}

		return TypeExpr{Span: source.PhysicalSpan(start, end), Name: name, Arguments: parsedArguments}, nil
	}
	text := source.Text(start, end)
	if source.ByteAt(start) == '"' {
		value, err := strconv.Unquote(text)
		if err != nil {
			return TypeExpr{}, fmt.Errorf("invalid string literal type %q", text)
		}

		return TypeExpr{Span: source.PhysicalSpan(start, end), Name: typeString, ConstantString: value, ConstantStringKnown: true}, nil
	}
	if !qualifiedIdentifier(text) {
		return TypeExpr{}, fmt.Errorf("invalid type expression %q", text)
	}

	return TypeExpr{Span: source.PhysicalSpan(start, end), Name: text}, nil
}

func splitTypeSourceRanges(source expressionSource, start, end int, separator byte, includeUnsplit bool) [][2]int {
	depth := 0
	partStart := start
	var parts [][2]int
	cursor := expressionSourceCursor{source: source, offset: start}
	for {
		lexeme, ok := cursor.next()
		if !ok || lexeme.start >= end {
			break
		}
		if lexeme.kind != sourceLexemeSymbol || lexeme.end > end || lexeme.end-lexeme.start != 1 {
			continue
		}
		symbol := source.ByteAt(lexeme.start)
		switch symbol {
		case '<', '{':
			depth++
		case '>', '}':
			depth--
		default:
			if symbol == separator && depth == 0 {
				partStartTrimmed, partEnd := trimTypeSourceRange(source, partStart, lexeme.start)
				parts = append(parts, [2]int{partStartTrimmed, partEnd})
				partStart = lexeme.end
			}
		}
	}
	if len(parts) > 0 || includeUnsplit {
		trimmedStart, trimmedEnd := trimTypeSourceRange(source, partStart, end)
		parts = append(parts, [2]int{trimmedStart, trimmedEnd})
	}

	return parts
}

func splitTypeExpressionArgumentRanges(source expressionSource, start, end int) ([][2]int, error) {
	ranges := make([][2]int, 0, 1)
	argumentStart := start
	depth := 0
	unterminatedLiteral := false
	cursor := expressionSourceCursor{source: source, offset: start}
	for {
		lexeme, ok := cursor.next()
		if !ok || lexeme.start >= end {
			break
		}
		if lexeme.kind == sourceLexemeLiteral {
			unterminatedLiteral = unterminatedLiteral || !typeSourceLiteralClosed(source, lexeme)

			continue
		}
		if lexeme.kind != sourceLexemeSymbol || lexeme.end > end {
			continue
		}
		symbol := source.Text(lexeme.start, lexeme.end)
		switch symbol {
		case "(", "<", "{":
			depth++
		case ")", ">", "}", "=>", "->":
			depth--
			if depth < 0 {
				return nil, errors.New("unmatched closing delimiter")
			}
		case ",":
			if depth == 0 {
				if !appendTypeSourceRange(&ranges, source, argumentStart, lexeme.start) {
					return nil, errors.New("function arguments cannot be empty")
				}
				argumentStart = lexeme.end
			}
		}
	}
	if depth != 0 || unterminatedLiteral {
		return nil, errors.New("unterminated type or string")
	}
	lastStart, lastEnd := trimTypeSourceRange(source, argumentStart, end)
	if lastStart == lastEnd {
		if len(ranges) == 0 {
			return nil, errors.New("function arguments cannot be empty")
		}

		return ranges, nil
	}
	ranges = append(ranges, [2]int{lastStart, lastEnd})

	return ranges, nil
}

func appendTypeSourceRange(ranges *[][2]int, source expressionSource, start, end int) bool {
	trimmedStart, trimmedEnd := trimTypeSourceRange(source, start, end)
	if trimmedStart == trimmedEnd {
		return false
	}
	*ranges = append(*ranges, [2]int{trimmedStart, trimmedEnd})

	return true
}

func typeSourceLiteralClosed(source expressionSource, literal sourceLexeme) bool {
	if literal.start+3 <= literal.end && source.ByteAt(literal.start+1) == '"' && source.ByteAt(literal.start+2) == '"' {
		return literal.end-literal.start >= 6 && source.ByteAt(literal.end-1) == '"' && source.ByteAt(literal.end-2) == '"' && source.ByteAt(literal.end-3) == '"'
	}
	escaped := false
	for offset := literal.start + 1; offset < literal.end; offset++ {
		current := source.ByteAt(offset)
		if escaped {
			escaped = false
		} else if current == '\\' {
			escaped = true
		} else if current == '"' {
			return offset+1 == literal.end
		}
	}

	return false
}

func trimTypeSourceRange(source expressionSource, start, end int) (int, int) {
	for start < end {
		var buffer [utf8.UTFMax]byte
		size := min(len(buffer), end-start)
		for index := range size {
			buffer[index] = source.ByteAt(start + index)
		}
		r, width := utf8.DecodeRune(buffer[:size])
		if !unicode.IsSpace(r) {
			break
		}
		start += width
	}
	for end > start {
		var buffer [utf8.UTFMax]byte
		size := min(len(buffer), end-start)
		bufferStart := end - size
		for index := range size {
			buffer[index] = source.ByteAt(bufferStart + index)
		}
		r, width := utf8.DecodeLastRune(buffer[:size])
		if !unicode.IsSpace(r) {
			break
		}
		end -= width
	}

	return start, end
}

func sourceRangeByteIndex(source expressionSource, start, end int, target byte) int {
	for offset := start; offset < end; offset++ {
		if source.ByteAt(offset) == target {
			return offset
		}
	}

	return -1
}

func expressionSourceHasPrefix(source expressionSource, start, end int, prefix string) bool {
	if end-start < len(prefix) {
		return false
	}
	for index := range prefix {
		if source.ByteAt(start+index) != prefix[index] {
			return false
		}
	}

	return true
}

func qualifiedIdentifier(text string) bool {
	parts := strings.Split(text, ".")

	return !slices.ContainsFunc(parts, func(part string) bool { return !identifier(part) })
}

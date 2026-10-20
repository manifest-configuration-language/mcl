package engine

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shopspring/decimal"
	"gopkg.in/yaml.v3"
)

func sourceLineSpan(source []byte, line int) (SourceSpan, bool) {
	start := 0
	for current := 1; current < line; current++ {
		newline := start
		for newline < len(source) && source[newline] != '\n' {
			newline++
		}
		if newline == len(source) {
			return SourceSpan{}, false
		}
		start = newline + 1
	}
	if start > len(source) {
		return SourceSpan{}, false
	}
	end := start
	for end < len(source) && source[end] != '\n' {
		end++
	}
	if end > start && source[end-1] == '\r' {
		end--
	}
	if end <= start {
		return SourceSpan{}, false
	}

	return SourceSpan{Start: start, End: end}, true
}

func validateYAMLNode(node *yaml.Node, depth int, count *int) error {
	if depth > maxYAMLDepth {
		return fmt.Errorf("YAML nesting exceeds the %d-level limit", maxYAMLDepth)
	}
	*count++
	if *count > maxYAMLNodes {
		return fmt.Errorf("YAML input exceeds the %d-node limit", maxYAMLNodes)
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child, depth+1, count); err != nil {
			return err
		}
	}

	return nil
}

func normalizeYAMLDecimal(text string) (string, error) {
	if len(text) > maxMaterializedNumericDigits {
		return "", fmt.Errorf("YAML Decimal exceeds the %d-character input limit", maxMaterializedNumericDigits)
	}
	numericText := text
	if strings.ContainsRune(text, '_') {
		var numeric strings.Builder
		numeric.Grow(len(text))
		for index := range len(text) {
			if text[index] != '_' {
				numeric.WriteByte(text[index])

				continue
			}
			if index == 0 || index+1 == len(text) || !isASCIIDigit(text[index-1]) || !isASCIIDigit(text[index+1]) {
				return "", errors.New("underscore must separate decimal digits")
			}
		}
		numericText = numeric.String()
	}
	if err := validateRuntimeDecimalText(numericText); err != nil {
		return "", err
	}

	value, err := decimal.NewFromString(numericText)
	if err != nil {
		return "", err
	}
	if value.IsZero() {
		return "0.0", nil
	}

	normalized := value.Coefficient().String() + "e" + strconv.FormatInt(int64(value.Exponent()), 10)
	if !numberPattern.MatchString(normalized) {
		return "", errors.New("value cannot be represented as an MCL Decimal")
	}
	if _, err := decimal.NewFromString(normalized); err != nil {
		return "", fmt.Errorf("value cannot be represented as an MCL Decimal: %w", err)
	}

	return normalized, nil
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func yamlNodeSourceSpan(source []byte, node *yaml.Node) SourceSpan {
	lineSpan, ok := sourceLineSpan(source, node.Line)
	if !ok || node.Kind != yaml.ScalarNode {
		return lineSpan
	}
	start := lineSpan.Start
	for column := 1; column < node.Column && start < lineSpan.End; column++ {
		_, size := utf8.DecodeRune(source[start:lineSpan.End])
		if size == 0 {
			return lineSpan
		}
		start += size
	}
	if node.Style&yaml.DoubleQuotedStyle != 0 {
		if end, ok := yamlQuotedScalarEnd(source, start, lineSpan.End, '"'); ok {
			return SourceSpan{Start: start, End: end}
		}

		return lineSpan
	}
	if node.Style&yaml.SingleQuotedStyle != 0 {
		if end, ok := yamlQuotedScalarEnd(source, start, lineSpan.End, '\''); ok {
			return SourceSpan{Start: start, End: end}
		}

		return lineSpan
	}
	if node.Style != 0 || node.Value == "" || strings.ContainsAny(node.Value, "\r\n") || start+len(node.Value) > lineSpan.End || !bytes.HasPrefix(source[start:lineSpan.End], []byte(node.Value)) {
		return lineSpan
	}

	return SourceSpan{Start: start, End: start + len(node.Value)}
}

func yamlQuotedScalarEnd(source []byte, start, lineEnd int, quote byte) (int, bool) {
	if start >= lineEnd || source[start] != quote {
		return 0, false
	}
	for index := start + 1; index < lineEnd; index++ {
		if quote == '"' && source[index] == '\\' {
			index++

			continue
		}
		if source[index] != quote {
			continue
		}
		if quote == '\'' && index+1 < lineEnd && source[index+1] == '\'' {
			index++

			continue
		}

		return index + 1, true
	}

	return 0, false
}

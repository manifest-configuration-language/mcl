package syntax

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MaxSourceBytes is the maximum size of an MCL source file.
const MaxSourceBytes = 16 << 20

const (
	maxIterations                = 100_000
	maxNumericCoefficientDigits  = 4096
	maxMaterializedNumericDigits = 16384
	maxAbsoluteDecimalExponent   = 10000
)

const (
	typeAny      = "Any"
	typeBool     = "Bool"
	typeDecimal  = "Decimal"
	typeFunction = "Function"
	typeInt      = "Int"
	typeList     = "List"
	typeMap      = "Map"
	typeNode     = "Node"
	typeNull     = "Null"
	typeString   = "String"
)

var numberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

type typeRecordFieldHeader struct {
	name     string
	nameSpan SourceSpan
	typeSpan SourceSpan
	optional bool
}

func isBuiltinType(name string) bool {
	switch name {
	case typeAny, typeNull, typeBool, typeInt, typeDecimal, typeString, typeList, typeMap, typeFunction, typeNode:
		return true
	default:
		return false
	}
}

func multilineStringContent(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	if !strings.HasPrefix(content, "\n") {
		return content
	}
	lines := strings.Split(strings.TrimPrefix(content, "\n"), "\n")
	if len(lines) != 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	commonIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if commonIndent < 0 || indent < commonIndent {
			commonIndent = indent
		}
	}
	if commonIndent <= 0 {
		return strings.Join(lines, "\n")
	}
	for index, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[index] = line[commonIndent:]
		}
	}

	return strings.Join(lines, "\n")
}

func validateRuntimeIntegerText(text string) error {
	digits := strings.TrimPrefix(strings.TrimPrefix(text, "+"), "-")
	if digits == "" || len(digits) > maxNumericCoefficientDigits || !isDecimalDigits(digits) {
		return fmt.Errorf("integer value exceeds the %d-digit limit or is invalid", maxNumericCoefficientDigits)
	}

	return nil
}

func validateRuntimeDecimalText(text string) error {
	if len(text) > maxMaterializedNumericDigits {
		return fmt.Errorf("decimal value exceeds the %d-character input limit", maxMaterializedNumericDigits)
	}
	coefficient, exponent, err := decimalTextParts(text)
	if err != nil {
		return err
	}
	if len(coefficient) > maxNumericCoefficientDigits {
		return fmt.Errorf("decimal coefficient exceeds the %d-digit limit", maxNumericCoefficientDigits)
	}
	if exponent < -maxAbsoluteDecimalExponent || exponent > maxAbsoluteDecimalExponent {
		return fmt.Errorf("decimal exponent exceeds the +/- %d limit", maxAbsoluteDecimalExponent)
	}

	return nil
}

func decimalTextParts(text string) (string, int, error) {
	unsigned := strings.TrimPrefix(strings.TrimPrefix(text, "+"), "-")
	mantissa, exponentText, hasExponent := strings.Cut(strings.ToLower(unsigned), "e")
	if strings.Contains(exponentText, "e") {
		return "", 0, errors.New("invalid decimal value")
	}
	fractionDigits := 0
	if integerPart, fractionPart, found := strings.Cut(mantissa, "."); found {
		if strings.Contains(fractionPart, ".") {
			return "", 0, errors.New("invalid decimal value")
		}
		fractionDigits = len(fractionPart)
		mantissa = integerPart + fractionPart
	}
	if mantissa == "" || !isDecimalDigits(mantissa) {
		return "", 0, errors.New("invalid decimal value")
	}
	exponent := 0
	if hasExponent {
		parsed, err := parseBoundedExponent(exponentText)
		if err != nil {
			return "", 0, err
		}
		exponent = parsed
	}

	return mantissa, exponent - fractionDigits, nil
}

func parseBoundedExponent(text string) (int, error) {
	if text == "" {
		return 0, errors.New("invalid decimal exponent")
	}
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(strings.TrimPrefix(text, "+"), "-")
	text = strings.TrimLeft(text, "0")
	if text == "" {
		return 0, nil
	}
	if len(text) > 5 {
		return 0, fmt.Errorf("decimal exponent exceeds the +/- %d limit", maxAbsoluteDecimalExponent)
	}
	value, err := strconv.Atoi(text)
	if err != nil || value > maxAbsoluteDecimalExponent {
		return 0, fmt.Errorf("decimal exponent exceeds the +/- %d limit", maxAbsoluteDecimalExponent)
	}
	if negative {
		return -value, nil
	}

	return value, nil
}

func isDecimalDigits(text string) bool {
	for _, digit := range text {
		if digit < '0' || digit > '9' {
			return false
		}
	}

	return text != ""
}

func multilineString(text string) (string, error) {
	if len(text) < 6 || !strings.HasSuffix(text, `"""`) {
		return "", errors.New("unterminated multiline string")
	}
	if closing := strings.Index(text[3:], `"""`); closing != len(text)-6 {
		return "", errors.New("unexpected text after multiline string")
	}

	return multilineStringContent(text[3 : len(text)-3]), nil
}

func containsDecimal(text string) bool {
	return strings.ContainsAny(text, ".eE")
}

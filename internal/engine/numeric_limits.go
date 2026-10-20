package engine

import (
	"cmp"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

const (
	maxNumericCoefficientDigits  = 4096
	maxAbsoluteDecimalExponent   = 10000
	maxMaterializedNumericDigits = 16384
	decimalDivisionPrecision     = 16
)

func parseRuntimeInteger(text string) (*big.Int, error) {
	if err := validateRuntimeIntegerText(text); err != nil {
		return nil, err
	}
	value, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return nil, errors.New("invalid integer value")
	}

	return value, nil
}

func validateRuntimeIntegerText(text string) error {
	digits := strings.TrimPrefix(strings.TrimPrefix(text, "+"), "-")
	if digits == "" || len(digits) > maxNumericCoefficientDigits || !isDecimalDigits(digits) {
		return fmt.Errorf("integer value exceeds the %d-digit limit or is invalid", maxNumericCoefficientDigits)
	}

	return nil
}

func validateRuntimeYAMLIntegerText(text string) error {
	if len(text) > maxMaterializedNumericDigits {
		return fmt.Errorf("YAML Integer exceeds the %d-character input limit", maxMaterializedNumericDigits)
	}
	unsigned := strings.TrimPrefix(strings.TrimPrefix(text, "+"), "-")
	base := 10
	if len(unsigned) >= 2 && unsigned[0] == '0' {
		switch unsigned[1] {
		case 'x', 'X':
			base = 16
		case 'b', 'B':
			base = 2
		case 'o', 'O':
			base = 8
		default:
			base = 8
		}
	}
	if len(unsigned) >= 2 && unsigned[0] == '0' && strings.ContainsRune("xXbBoO", rune(unsigned[1])) {
		unsigned = unsigned[2:]
	}
	maximumDigits := maxNumericCoefficientDigits
	switch base {
	case 2:
		maximumDigits = 13613
	case 8:
		maximumDigits = 4536
	case 16:
		maximumDigits = 3401
	}
	digits := 0
	leadingZero := true
	for _, character := range unsigned {
		if character == '_' {
			continue
		}
		if leadingZero && character == '0' {
			continue
		}
		leadingZero = false
		digits++
		if digits > maximumDigits {
			return fmt.Errorf("YAML Integer may exceed the %d-digit decimal limit", maxNumericCoefficientDigits)
		}
	}

	return nil
}

func integerDigitCount(value *big.Int) int {
	digits := value.String()

	return len(strings.TrimPrefix(digits, "-"))
}

func parseRuntimeDecimal(text string) (decimal.Decimal, error) {
	if err := validateRuntimeDecimalText(text); err != nil {
		return decimal.Decimal{}, err
	}
	value, err := decimal.NewFromString(text)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("invalid decimal value: %w", err)
	}

	return value, nil
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
	exponent -= fractionDigits

	return mantissa, exponent, nil
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

func validateDecimalOperationInputs(left, right decimal.Decimal, operator string) error {
	leftDigits := left.NumDigits()
	rightDigits := right.NumDigits()
	leftExponent := int(left.Exponent())
	rightExponent := int(right.Exponent())
	resultExponent := min(leftExponent, rightExponent)
	switch operator {
	case "+", "-":
		leftAligned := leftDigits + max(0, rightExponent-leftExponent)
		rightAligned := rightDigits + max(0, leftExponent-rightExponent)
		if max(leftAligned, rightAligned) > maxNumericCoefficientDigits {
			return fmt.Errorf("decimal %s intermediate exceeds the %d-digit coefficient limit", operator, maxNumericCoefficientDigits)
		}
	case "*":
		resultExponent = leftExponent + rightExponent
		if leftDigits+rightDigits-1 > maxNumericCoefficientDigits {
			return fmt.Errorf("decimal multiplication exceeds the %d-digit coefficient limit", maxNumericCoefficientDigits)
		}
	case "/", "%":
		if operator == "/" {
			resultExponent = -decimalDivisionPrecision
		}
		exponentDelta := leftExponent - rightExponent
		if operator == "/" {
			exponentDelta += decimalDivisionPrecision
		}
		intermediateDigits := leftDigits
		if exponentDelta < 0 {
			intermediateDigits = rightDigits - exponentDelta
		} else {
			intermediateDigits += exponentDelta
		}
		if intermediateDigits > maxNumericCoefficientDigits {
			return fmt.Errorf("decimal %s intermediate exceeds the %d-digit coefficient limit", operator, maxNumericCoefficientDigits)
		}
	}
	if resultExponent < -maxAbsoluteDecimalExponent || resultExponent > maxAbsoluteDecimalExponent {
		return fmt.Errorf("decimal %s result exponent exceeds the +/- %d limit", operator, maxAbsoluteDecimalExponent)
	}

	return nil
}

func validateRuntimeDecimal(value decimal.Decimal) error {
	coefficientDigits := value.NumDigits()
	if coefficientDigits > maxNumericCoefficientDigits {
		return fmt.Errorf("decimal result exceeds the %d-digit coefficient limit", maxNumericCoefficientDigits)
	}
	exponent := int(value.Exponent())
	if exponent < -maxAbsoluteDecimalExponent || exponent > maxAbsoluteDecimalExponent {
		return fmt.Errorf("decimal result exponent exceeds the +/- %d limit", maxAbsoluteDecimalExponent)
	}
	materializedDigits := coefficientDigits
	if exponent >= 0 {
		materializedDigits += exponent
	} else if coefficientDigits+exponent <= 0 {
		materializedDigits = 2 - exponent
	} else {
		materializedDigits++
	}
	if materializedDigits > maxMaterializedNumericDigits {
		return fmt.Errorf("decimal result exceeds the %d-digit materialization limit", maxMaterializedNumericDigits)
	}

	return nil
}

func compareRuntimeDecimals(left, right decimal.Decimal) int {
	leftSign := left.Sign()
	rightSign := right.Sign()
	if leftSign != rightSign {
		return cmp.Compare(leftSign, rightSign)
	}
	if leftSign == 0 {
		return 0
	}
	leftCoefficient := left.Coefficient().String()
	rightCoefficient := right.Coefficient().String()
	leftNegative := leftSign < 0
	leftDigits := strings.TrimPrefix(leftCoefficient, "-")
	rightDigits := strings.TrimPrefix(rightCoefficient, "-")
	leftPosition := len(leftDigits) + int(left.Exponent())
	rightPosition := len(rightDigits) + int(right.Exponent())
	comparison := 0
	switch {
	case leftPosition < rightPosition:
		comparison = -1
	case leftPosition > rightPosition:
		comparison = 1
	default:
		for index := range max(len(leftDigits), len(rightDigits)) {
			leftDigit := byte('0')
			rightDigit := byte('0')
			if index < len(leftDigits) {
				leftDigit = leftDigits[index]
			}
			if index < len(rightDigits) {
				rightDigit = rightDigits[index]
			}
			if leftDigit < rightDigit {
				comparison = -1

				break
			}
			if leftDigit > rightDigit {
				comparison = 1

				break
			}
		}
	}
	if leftNegative {
		return -comparison
	}

	return comparison
}

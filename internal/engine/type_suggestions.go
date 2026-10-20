package engine

import (
	"fmt"
)

type unknownTypeField struct {
	name       string
	suggestion string
}

func (err unknownTypeField) Error() string {
	message := fmt.Sprintf("unknown field %q", err.name)
	if err.suggestion != "" {
		message += fmt.Sprintf("; did you mean %q?", err.suggestion)
	}

	return message
}

func unknownTypeFieldError(name string, fields []TypeField) error {
	result := unknownTypeField{name: name}
	candidate, distance := closestTypeField(name, fields)
	threshold := max(1, len([]rune(name))/3)
	if candidate != "" && distance <= threshold {
		result.suggestion = candidate
	}

	return result
}

func closestTypeField(name string, fields []TypeField) (string, int) {
	closest, closestDistance := "", int(^uint(0)>>1)
	for _, field := range fields {
		distance := stringEditDistance(name, field.Name)
		if distance < closestDistance {
			closest, closestDistance = field.Name, distance
		}
	}

	return closest, closestDistance
}

func stringEditDistance(left, right string) int {
	leftRunes, rightRunes := []rune(left), []rune(right)
	previousPrevious := make([]int, len(rightRunes)+1)
	previous := make([]int, len(rightRunes)+1)
	current := make([]int, len(rightRunes)+1)
	for index := range previous {
		previous[index] = index
	}
	for leftIndex, leftRune := range leftRunes {
		current[0] = leftIndex + 1
		for rightIndex, rightRune := range rightRunes {
			substitution := 0
			if leftRune != rightRune {
				substitution = 1
			}
			current[rightIndex+1] = min(
				previous[rightIndex+1]+1,
				current[rightIndex]+1,
				previous[rightIndex]+substitution,
			)
			if leftIndex > 0 && rightIndex > 0 && leftRune == rightRunes[rightIndex-1] && leftRunes[leftIndex-1] == rightRune {
				current[rightIndex+1] = min(current[rightIndex+1], previousPrevious[rightIndex-1]+1)
			}
		}
		previousPrevious, previous, current = previous, current, previousPrevious
	}

	return previous[len(rightRunes)]
}

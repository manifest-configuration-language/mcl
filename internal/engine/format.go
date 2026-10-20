package engine

import "github.com/manifest-configuration-language/mcl/internal/syntax"

func FormatSource(source string) (string, error) {
	formatted, err := syntax.FormatSource(source)
	if err != nil {
		return "", normalizeSyntaxError(err)
	}

	return formatted, nil
}

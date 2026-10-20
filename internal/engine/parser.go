package engine

import "github.com/manifest-configuration-language/mcl/internal/syntax"

func ParseAST(source string) (ModuleAST, error) {
	module, err := syntax.ParseAST(source)
	if err != nil {
		return ModuleAST{}, normalizeSyntaxError(err)
	}

	return module, nil
}

func normalizeSyntaxError(err error) error {
	diagnostic, ok := syntax.SyntaxDiagnosticFromError(err)
	if !ok {
		return err
	}

	return SourceDiagnosticError{
		Code:    diagnostic.Code,
		Message: diagnostic.Message,
		Span:    diagnostic.Span,
		Cause:   err,
	}
}

package project

import (
	"fmt"

	"github.com/manifest-configuration-language/mcl/internal/source"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

func parseWorkspaceModule(path, sourceText string) (syntax.ModuleAST, error) {
	module, err := syntax.ParseAST(sourceText)
	if err == nil {
		return module, nil
	}
	if diagnostic, ok := syntax.SyntaxDiagnosticFromError(err); ok {
		err = source.DiagnosticError{
			Path: path, Code: diagnostic.Code, Message: diagnostic.Message,
			Span: source.Span{Start: diagnostic.Span.Start, End: diagnostic.Span.End}, Cause: err,
		}
	}

	return syntax.ModuleAST{}, fmt.Errorf("parse workspace module %q: %w", path, err)
}

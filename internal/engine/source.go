package engine

import (
	"errors"

	"github.com/manifest-configuration-language/mcl/internal/source"
)

type SourceDiagnosticLocation = source.DiagnosticLocation

type SourceOriginKind = source.OriginKind

const (
	SourceOriginMCL             = source.OriginMCL
	SourceOriginMCLOverlayBase  = source.OriginMCLOverlayBase
	SourceOriginMCLOverlayPatch = source.OriginMCLOverlayPatch
	SourceOriginKustomizeInput  = source.OriginKustomizeInput
	SourceOriginKustomizeChange = source.OriginKustomizeChange
	SourceOriginHelmExpression  = source.OriginHelmExpression
	SourceOriginHelmBranch      = source.OriginHelmBranch
	SourceOriginHelmFallback    = source.OriginHelmFallback
)

type SourceOriginMultiplicity = source.OriginMultiplicity

const (
	SourceOriginUnique    = source.OriginUnique
	SourceOriginRepeated  = source.OriginRepeated
	SourceOriginAmbiguous = source.OriginAmbiguous
)

type SourceDiagnosticError = source.DiagnosticError

type typeDiagnosticError struct {
	cause error
}

func (err typeDiagnosticError) Error() string {
	return err.cause.Error()
}

func (err typeDiagnosticError) Unwrap() error {
	return err.cause
}

func typeDiagnostic(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[typeDiagnosticError](err); ok {
		return err
	}

	return typeDiagnosticError{cause: err}
}

// SourceDiagnosticFromError はエラー列からソース診断を取得します。
func SourceDiagnosticFromError(err error) (SourceDiagnosticError, bool) {
	return source.FromError(err)
}

func sourceDiagnosticPath(path string, err error) error {
	return source.WithPath(path, err)
}

func sourceDiagnostic(path string, span SourceSpan, err error) error {
	return sourceDiagnosticWithRelated(path, span, err, nil)
}

func sourceDiagnosticWithRelated(path string, span SourceSpan, err error, related []SourceDiagnosticLocation) error {
	if err == nil || span.End <= span.Start {
		return err
	}
	if diagnostic, ok := SourceDiagnosticFromError(err); ok {
		if diagnostic.Path == "" && diagnostic.Span.End <= diagnostic.Span.Start {
			diagnostic.Path = path
			diagnostic.Span = span
		}
		if len(diagnostic.Related) == 0 {
			diagnostic.Related = related
		}

		return diagnostic
	}
	code := "MCL-EVAL"
	if errors.Is(err, errUndefinedName) {
		code = "MCL-UNDEFINED"
	} else if _, ok := errors.AsType[typeDiagnosticError](err); ok {
		code = codeMCLType
	}

	return SourceDiagnosticError{Path: path, Code: code, Message: err.Error(), Span: span, Related: related, Cause: err}
}

func sourceDiagnosticWithCode(path string, span SourceSpan, code string, err error, related []SourceDiagnosticLocation) error {
	if err == nil || span.End <= span.Start {
		return err
	}
	if diagnostic, ok := SourceDiagnosticFromError(err); ok {
		return diagnostic
	}

	return SourceDiagnosticError{Path: path, Code: code, Message: err.Error(), Span: span, Related: related, Cause: err}
}

var errUndefinedName = errors.New("undefined name")

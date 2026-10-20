package engine

import (
	"errors"
)

// SourceDiagnosticLocation は診断に関連するソース範囲です。
type SourceDiagnosticLocation struct {
	Path         string
	Span         SourceSpan
	Message      string
	Origin       SourceOriginKind
	Multiplicity SourceOriginMultiplicity
	Occurrences  int
}

// SourceOriginKindは出力値とソース範囲の関係を示します。
type SourceOriginKind string

const (
	SourceOriginMCL             SourceOriginKind = "mcl"
	SourceOriginMCLOverlayBase  SourceOriginKind = "mcl-overlay-base"
	SourceOriginMCLOverlayPatch SourceOriginKind = "mcl-overlay-patch"
	SourceOriginKustomizeInput  SourceOriginKind = "kustomize-input"
	SourceOriginKustomizeChange SourceOriginKind = "kustomize-transformation"
	SourceOriginHelmExpression  SourceOriginKind = "helm-template-expression"
	SourceOriginHelmBranch      SourceOriginKind = "helm-template-branch"
	SourceOriginHelmFallback    SourceOriginKind = "helm-template-fallback"
)

// SourceOriginMultiplicityはソース範囲と出力範囲の対応の一意性を示します。
type SourceOriginMultiplicity string

const (
	SourceOriginUnique    SourceOriginMultiplicity = "unique"
	SourceOriginRepeated  SourceOriginMultiplicity = "repeated"
	SourceOriginAmbiguous SourceOriginMultiplicity = "ambiguous"
)

// SourceDiagnosticError はソース上の位置を持つ評価診断です。
type SourceDiagnosticError struct {
	Path       string
	Code       string
	Message    string
	TypeName   string
	SchemaPath string
	Span       SourceSpan
	Related    []SourceDiagnosticLocation
	Cause      error
}

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

func (err SourceDiagnosticError) Error() string {
	return err.Message
}

func (err SourceDiagnosticError) Unwrap() error {
	return err.Cause
}

// DisplayMessage は診断の内容と型情報を表示用の文章にまとめます。
func (err SourceDiagnosticError) DisplayMessage() string {
	message := err.Message
	if err.TypeName != "" {
		message += "\nType: " + err.TypeName
	}
	if err.SchemaPath != "" {
		message += "\nSchema: " + err.SchemaPath
	}

	return message
}

// SourceDiagnosticFromError はエラー列からソース診断を取得します。
func SourceDiagnosticFromError(err error) (SourceDiagnosticError, bool) {
	return errors.AsType[SourceDiagnosticError](err)
}

func sourceDiagnosticPath(path string, err error) error {
	diagnostic, ok := SourceDiagnosticFromError(err)
	if !ok || diagnostic.Path != "" {
		return err
	}
	diagnostic.Path = path

	return diagnostic
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

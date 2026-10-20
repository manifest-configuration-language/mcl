package syntax

import "errors"

// DiagnosticCodeは字句解析と構文解析で使う診断コードです。
const DiagnosticCode = "MCL-SYNTAX"

// SyntaxDiagnosticErrorは字句解析または構文解析の診断を表します。
type SyntaxDiagnosticError struct {
	Code    string
	Message string
	Span    SourceSpan
	Cause   error
}

// Errorは診断メッセージを返します。
func (diagnostic SyntaxDiagnosticError) Error() string {
	return diagnostic.Message
}

// Unwrapは診断の原因になったエラーを返します。
func (diagnostic SyntaxDiagnosticError) Unwrap() error {
	return diagnostic.Cause
}

// SyntaxDiagnosticFromErrorはエラー列から構文診断を取得します。
func SyntaxDiagnosticFromError(err error) (SyntaxDiagnosticError, bool) {
	return errors.AsType[SyntaxDiagnosticError](err)
}

func syntaxDiagnostic(err error, span SourceSpan) error {
	if err == nil {
		return nil
	}
	if _, ok := SyntaxDiagnosticFromError(err); ok {
		return err
	}

	return SyntaxDiagnosticError{Code: DiagnosticCode, Message: err.Error(), Span: span, Cause: err}
}

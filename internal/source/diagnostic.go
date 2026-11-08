// Package source owns source locations and diagnostics shared by compiler tools.
package source

import (
	"errors"
)

// OriginKind identifies how an output value relates to a source location.
type OriginKind string

const (
	OriginMCL             OriginKind = "mcl"
	OriginMCLOverlayBase  OriginKind = "mcl-overlay-base"
	OriginMCLOverlayPatch OriginKind = "mcl-overlay-patch"
	OriginKustomizeInput  OriginKind = "kustomize-input"
	OriginKustomizeChange OriginKind = "kustomize-transformation"
	OriginHelmExpression  OriginKind = "helm-template-expression"
	OriginHelmBranch      OriginKind = "helm-template-branch"
	OriginHelmFallback    OriginKind = "helm-template-fallback"
)

// OriginMultiplicity describes how many output locations share one source location.
type OriginMultiplicity string

const (
	OriginUnique    OriginMultiplicity = "unique"
	OriginRepeated  OriginMultiplicity = "repeated"
	OriginAmbiguous OriginMultiplicity = "ambiguous"
)

// DiagnosticLocation is a source range related to a diagnostic or output value.
type DiagnosticLocation struct {
	Path         string
	Span         Span
	Message      string
	Origin       OriginKind
	Multiplicity OriginMultiplicity
	Occurrences  int
}

// DiagnosticError carries an evaluation error and its source locations.
type DiagnosticError struct {
	Path              string
	Code              string
	Message           string
	TypeName          string
	SchemaPath        string
	Span              Span
	Related           []DiagnosticLocation
	OmitSourceSnippet bool
	Cause             error
}

func (err DiagnosticError) Error() string { return err.Message }

func (err DiagnosticError) Unwrap() error { return err.Cause }

// DisplayMessage combines the diagnostic message with optional type and schema context.
func (err DiagnosticError) DisplayMessage() string {
	message := err.Message
	if err.TypeName != "" {
		message += "\nType: " + err.TypeName
	}
	if err.SchemaPath != "" {
		message += "\nSchema: " + err.SchemaPath
	}

	return message
}

// FromError finds a source diagnostic in an error chain.
func FromError(err error) (DiagnosticError, bool) {
	return errors.AsType[DiagnosticError](err)
}

// WithPath adds a module path when a source diagnostic does not already have one.
func WithPath(path string, err error) error {
	diagnostic, ok := FromError(err)
	if !ok || diagnostic.Path != "" {
		return err
	}
	diagnostic.Path = path

	return diagnostic
}

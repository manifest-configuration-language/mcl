package main

import (
	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/wasmprotocol"
)

func diagnostics(module engine.ModuleAST, source, sourcePath string) wasmprotocol.LintResult {
	items := engine.LintAST(module)
	result := make(wasmprotocol.LintResult, 0, len(items))
	for _, item := range items {
		line, column := sourcePosition(source, item.Span.Start)
		endLine, endColumn := sourcePosition(source, item.Span.End)
		actions := make([]wasmprotocol.LintCodeAction, 0, len(item.Actions))
		for _, action := range item.Actions {
			edits := make([]wasmprotocol.RenameEdit, 0, len(action.Edits))
			for _, edit := range action.Edits {
				path := edit.Path
				if path == "" {
					path = sourcePath
				}
				editLine, editColumn := sourcePosition(source, edit.Span.Start)
				editEndLine, editEndColumn := sourcePosition(source, edit.Span.End)
				edits = append(edits, wasmprotocol.RenameEdit{
					Path: wasmprotocol.ProjectPath(path),
					Range: wasmprotocol.SourceRange{
						Start: wasmprotocol.CompletionPosition{Line: uint64(editLine - 1), Character: uint64(editColumn - 1)},
						End:   wasmprotocol.CompletionPosition{Line: uint64(editEndLine - 1), Character: uint64(editEndColumn - 1)},
					},
					NewText: edit.NewText,
				})
			}
			actions = append(actions, wasmprotocol.LintCodeAction{Kind: wasmprotocol.LintCodeActionKind(action.Kind), Edits: edits})
		}
		result = append(result, wasmprotocol.LintDiagnostic{
			Code: item.Code, Message: item.Message, Severity: lintSeverity(item.Severity), Path: sourcePath,
			Line: uint64(line), Column: uint64(column), EndLine: uint64(endLine), EndColumn: uint64(endColumn), Actions: actions,
		})
	}

	return result
}

func lintSeverity(severity int) string {
	switch severity {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "information"
	case 4:
		return "hint"
	default:
		return "unknown"
	}
}

func sourcePosition(source string, offset int) (int, int) {
	return engine.SourcePositionUTF16(source, offset)
}

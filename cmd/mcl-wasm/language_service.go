//go:build js && wasm

package main

import (
	"fmt"
	"io/fs"
	"syscall/js"

	"github.com/manifest-configuration-language/mcl/internal/languageservice"
	"github.com/manifest-configuration-language/mcl/internal/wasmprotocol"
)

// handleLanguageServiceOperation converts Browser SDK language-service requests to protocol results.
func handleLanguageServiceOperation(input request, result *response, binaryArgument js.Value, sessionFiles *projectFilesystem, entrypoint string) bool {
	switch input.Operation {
	case wasmprotocol.RequestOperationCompletion, wasmprotocol.RequestOperationDefinition, wasmprotocol.RequestOperationHover, wasmprotocol.RequestOperationReferences, wasmprotocol.RequestOperationRename, wasmprotocol.RequestOperationRenameFileImports:
	default:
		return false
	}

	resultValue := result
	if input.Operation != wasmprotocol.RequestOperationRenameFileImports && (input.Position == nil || input.Position.Line > uint64(^uint(0)>>1) || input.Position.Character > uint64(^uint(0)>>1)) {
		resultValue.Error = &responseError{Code: "MCL-WASM-INPUT", Message: "operation requires a valid non-negative position"}
		return true
	}
	var projectFiles fs.FS
	var err error
	if sessionFiles != nil {
		projectFiles = projectFilesystemView{filesystem: sessionFiles, excluded: entrypoint}
	} else {
		binaryFiles, binaryErr := copyBinaryFiles(binaryArgument, input.Source, input.Files)
		if binaryErr != nil {
			resultValue.Error = &responseError{Code: "MCL-WASM-INPUT", Message: binaryErr.Error()}
			return true
		}
		projectFiles, err = newProjectFiles(input.Source, input.Files, binaryFiles, entrypoint)
		if err != nil {
			resultValue.Error = &responseError{Code: "MCL-WASM-INPUT", Message: err.Error()}
			return true
		}
	}
	params := languageservice.NavigationParams{
		Entrypoint: entrypoint, Source: input.Source,
	}
	if input.Position != nil {
		params.Line = int(input.Position.Line)
		params.Character = int(input.Position.Character)
	}
	switch input.Operation {
	case wasmprotocol.RequestOperationCompletion:
		items := languageservice.CompletionsForProjectFS(languageservice.CompletionParams{
			ProjectRoot: browserProjectRoot(), Entrypoint: entrypoint, Source: input.Source,
			Line: params.Line, Character: params.Character,
		}, projectFiles)
		completionItems := make([]wasmprotocol.CompletionItem, len(items))
		for index, item := range items {
			completionItems[index] = wasmprotocol.CompletionItem{Label: item.Label, Kind: uint64(item.Kind)}
			if item.InsertText != "" {
				completionItems[index].InsertText = new(item.InsertText)
			}
			if item.Detail != "" {
				completionItems[index].Detail = new(item.Detail)
			}
			if item.Documentation != "" {
				completionItems[index].Documentation = new(item.Documentation)
			}
		}
		resultValue.Result = wasmprotocol.CompletionResult{IsIncomplete: false, Items: completionItems}
	case wasmprotocol.RequestOperationDefinition:
		var location *wasmprotocol.DefinitionResultLocation
		if definition := languageservice.DefinitionForProjectFS(params, projectFiles); definition != nil {
			location = &wasmprotocol.DefinitionResultLocation{
				Path: definition.Path,
				Range: wasmprotocol.SourceRange{
					Start: wasmprotocol.CompletionPosition{Line: uint64(definition.Range.Start.Line), Character: uint64(definition.Range.Start.Character)},
					End:   wasmprotocol.CompletionPosition{Line: uint64(definition.Range.End.Line), Character: uint64(definition.Range.End.Character)},
				},
			}
		}
		resultValue.Result = wasmprotocol.DefinitionResult{Location: location}
	case wasmprotocol.RequestOperationHover:
		var hover *wasmprotocol.HoverResultHover
		if info := languageservice.HoverForProjectFS(params, projectFiles); info != nil {
			var standardLibrary *wasmprotocol.StandardLibraryFunction
			if info.StandardLibrary != nil {
				standardLibrary = &wasmprotocol.StandardLibraryFunction{
					Module: info.StandardLibrary.Module,
					Name:   info.StandardLibrary.Name,
				}
			}
			hover = &wasmprotocol.HoverResultHover{
				Contents:        info.Contents,
				StandardLibrary: standardLibrary,
				Range: wasmprotocol.SourceRange{
					Start: wasmprotocol.CompletionPosition{Line: uint64(info.Range.Start.Line), Character: uint64(info.Range.Start.Character)},
					End:   wasmprotocol.CompletionPosition{Line: uint64(info.Range.End.Line), Character: uint64(info.Range.End.Character)},
				},
			}
		}
		resultValue.Result = wasmprotocol.HoverResult{Hover: hover}
	case wasmprotocol.RequestOperationReferences:
		references := languageservice.ReferencesForProjectFS(params, projectFiles, true)
		locations := make([]wasmprotocol.ReferenceLocation, len(references))
		for index, reference := range references {
			locations[index] = wasmprotocol.ReferenceLocation{
				Path: wasmprotocol.ProjectPath(reference.Path),
				Range: wasmprotocol.SourceRange{
					Start: wasmprotocol.CompletionPosition{Line: uint64(reference.Range.Start.Line), Character: uint64(reference.Range.Start.Character)},
					End:   wasmprotocol.CompletionPosition{Line: uint64(reference.Range.End.Line), Character: uint64(reference.Range.End.Character)},
				},
			}
		}
		resultValue.Result = wasmprotocol.ReferencesResult{References: locations}
	case wasmprotocol.RequestOperationRename:
		if input.NewName == nil || len(*input.NewName) > wasmprotocol.MaxRenameNameLength {
			resultValue.Error = &responseError{Code: "MCL-WASM-INPUT", Message: fmt.Sprintf("rename requires a name no longer than %d bytes", wasmprotocol.MaxRenameNameLength)}
			return true
		}
		rename := languageservice.RenameForProjectFS(params, projectFiles, *input.NewName)
		edits := make([]wasmprotocol.RenameEdit, 0)
		if rename != nil {
			edits = make([]wasmprotocol.RenameEdit, len(rename.Edits))
			for index, edit := range rename.Edits {
				edits[index] = wasmprotocol.RenameEdit{
					Path: wasmprotocol.ProjectPath(edit.Path),
					Range: wasmprotocol.SourceRange{
						Start: wasmprotocol.CompletionPosition{Line: uint64(edit.Range.Start.Line), Character: uint64(edit.Range.Start.Character)},
						End:   wasmprotocol.CompletionPosition{Line: uint64(edit.Range.End.Line), Character: uint64(edit.Range.End.Character)},
					},
					NewText: edit.NewText,
				}
			}
		}
		resultValue.Result = wasmprotocol.RenameResult{Edits: edits}
	case wasmprotocol.RequestOperationRenameFileImports:
		if input.OldPath == nil || input.NewPath == nil {
			resultValue.Error = &responseError{Code: "MCL-WASM-INPUT", Message: "renameFileImports requires oldPath and newPath"}
			return true
		}
		rename, renameErr := languageservice.RenameFileImportsForProjectFS(params, projectFiles, string(*input.OldPath), string(*input.NewPath))
		if renameErr != nil {
			resultValue.Error = &responseError{Code: "MCL-WASM-IMPORT-RENAME", Message: renameErr.Error()}
			return true
		}
		edits := make([]wasmprotocol.RenameEdit, len(rename.Edits))
		for index, edit := range rename.Edits {
			edits[index] = wasmprotocol.RenameEdit{
				Path: wasmprotocol.ProjectPath(edit.Path),
				Range: wasmprotocol.SourceRange{
					Start: wasmprotocol.CompletionPosition{Line: uint64(edit.Range.Start.Line), Character: uint64(edit.Range.Start.Character)},
					End:   wasmprotocol.CompletionPosition{Line: uint64(edit.Range.End.Line), Character: uint64(edit.Range.End.Character)},
				},
				NewText: edit.NewText,
			}
		}
		resultValue.Result = wasmprotocol.RenameFileImportsResult{OldPath: *input.OldPath, NewPath: *input.NewPath, Edits: edits}
	}
	resultValue.Ok = true
	return true
}

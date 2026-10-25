//go:build js && wasm

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"syscall/js"
	"testing/fstest"
	"time"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/languageservice"
	"github.com/manifest-configuration-language/mcl/internal/sourceindex"
	"github.com/manifest-configuration-language/mcl/internal/wasmprotocol"
)

const (
	evaluationTime = time.Duration(wasmprotocol.EvaluationTimeoutMillis) * time.Millisecond
	projectRoot    = "/mcl-project"
)

type request = wasmprotocol.Request
type response = wasmprotocol.Response
type fileInput = wasmprotocol.FileInput
type responseError = wasmprotocol.Error

type projectUpdate struct {
	Reset   bool                 `json:"reset"`
	Files   map[string]fileInput `json:"files"`
	Deleted []string             `json:"deleted"`
}

var projectSessionMu sync.Mutex
var activeProjectID string
var activeProject *projectFilesystem
var updateProject js.Func

func main() {
	api := js.Global().Get("Object").New()
	run := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if (len(args) < 1 || len(args) > 3) || args[0].Type() != js.TypeString {
			return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-REQUEST", Message: "run expects a JSON string, an optional binary file map, and an optional project ID"}})
		}

		binaryFiles := js.Undefined()
		if len(args) == 2 {
			binaryFiles = args[1]
		}
		if len(args) == 3 {
			binaryFiles = args[1]
		}
		projectID := ""
		if len(args) == 3 {
			if args[2].Type() != js.TypeString {
				return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-REQUEST", Message: "project ID must be a string"}})
			}
			projectID = args[2].String()
		}

		return handle(args[0].String(), binaryFiles, projectID)
	})
	updateProject = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) != 3 || args[0].Type() != js.TypeString || args[1].Type() != js.TypeString {
			return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-PROJECT", Message: "updateProject expects a project ID, JSON update, and binary file map"}})
		}
		return applyProjectUpdate(args[0].String(), args[1].String(), args[2])
	})
	api.Set("run", run)
	api.Set("updateProject", updateProject)
	js.Global().Set("mclWasm", api)
	js.Global().Set("mclWasmReady", true)
	select {}
}

func handle(raw string, binaryArgument js.Value, projectID string) string {
	var input request
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-REQUEST", Message: "request must be valid JSON"}})
	}
	var requestID uint64
	if input.RequestID != nil {
		if *input.RequestID < 0 {
			return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-REQUEST", Message: "requestId must be non-negative"}})
		}
		requestID = uint64(*input.RequestID)
	}
	var revision *uint64
	if input.Revision != nil {
		if *input.Revision < 0 {
			return marshal(response{RequestID: requestID, Ok: false, Error: &responseError{Code: "MCL-WASM-REQUEST", Message: "revision must be non-negative"}})
		}
		value := uint64(*input.Revision)
		revision = &value
	}
	result := response{RequestID: requestID, Revision: revision}
	if len(input.Source) > wasmprotocol.MaxSourceBytes {
		result.Error = &responseError{Code: "MCL-WASM-SOURCE-LIMIT", Message: fmt.Sprintf("source exceeds the %d-byte limit", wasmprotocol.MaxSourceBytes)}
		return marshal(result)
	}
	entrypointInput := ""
	if input.Entrypoint != nil {
		entrypointInput = string(*input.Entrypoint)
	}
	entrypoint, err := projectEntrypoint(entrypointInput)
	if err != nil {
		result.Error = &responseError{Code: "MCL-WASM-INPUT", Message: err.Error()}
		return marshal(result)
	}
	var sessionFiles *projectFilesystem
	if projectID != "" {
		sessionFiles, err = projectFilesForSession(projectID, entrypoint, input.Source)
		if err != nil {
			result.Error = &responseError{Code: "MCL-WASM-PROJECT", Message: err.Error()}
			return marshal(result)
		}
	}
	if input.Operation == "format" {
		formatted, err := engine.FormatSource(input.Source)
		if err != nil {
			result.Error = diagnosticError(input.Source, entrypoint, nil, err)
		} else if len(formatted) > wasmprotocol.MaxOutputBytes {
			result.Error = &responseError{Code: "MCL-WASM-OUTPUT-LIMIT", Message: fmt.Sprintf("formatted output exceeds the %d-byte limit", wasmprotocol.MaxOutputBytes)}
		} else {
			result.Ok = true
			result.Result = wasmprotocol.FormatResult{Source: formatted}
		}
		return marshal(result)
	}
	if input.Operation == wasmprotocol.RequestOperationCompletion || input.Operation == wasmprotocol.RequestOperationDefinition || input.Operation == wasmprotocol.RequestOperationHover || input.Operation == wasmprotocol.RequestOperationReferences || input.Operation == wasmprotocol.RequestOperationRename || input.Operation == wasmprotocol.RequestOperationRenameFileImports {
		if input.Operation != wasmprotocol.RequestOperationRenameFileImports && (input.Position == nil || input.Position.Line > uint64(^uint(0)>>1) || input.Position.Character > uint64(^uint(0)>>1)) {
			result.Error = &responseError{Code: "MCL-WASM-INPUT", Message: "operation requires a valid non-negative position"}
			return marshal(result)
		}
		var projectFiles fs.FS
		if sessionFiles != nil {
			projectFiles = projectFilesystemView{filesystem: sessionFiles, excluded: entrypoint}
		} else {
			binaryFiles, binaryErr := copyBinaryFiles(binaryArgument, input.Source, input.Files)
			if binaryErr != nil {
				result.Error = &responseError{Code: "MCL-WASM-INPUT", Message: binaryErr.Error()}
				return marshal(result)
			}
			projectFiles, err = newProjectFiles(input.Source, input.Files, binaryFiles, entrypoint)
			if err != nil {
				result.Error = &responseError{Code: "MCL-WASM-INPUT", Message: err.Error()}
				return marshal(result)
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
			result.Result = wasmprotocol.CompletionResult{IsIncomplete: false, Items: completionItems}
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
			result.Result = wasmprotocol.DefinitionResult{Location: location}
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
			result.Result = wasmprotocol.HoverResult{Hover: hover}
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
			result.Result = wasmprotocol.ReferencesResult{References: locations}
		case wasmprotocol.RequestOperationRename:
			if input.NewName == nil || len(*input.NewName) > wasmprotocol.MaxRenameNameLength {
				result.Error = &responseError{Code: "MCL-WASM-INPUT", Message: fmt.Sprintf("rename requires a name no longer than %d bytes", wasmprotocol.MaxRenameNameLength)}
				return marshal(result)
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
			result.Result = wasmprotocol.RenameResult{Edits: edits}
		case wasmprotocol.RequestOperationRenameFileImports:
			if input.OldPath == nil || input.NewPath == nil {
				result.Error = &responseError{Code: "MCL-WASM-INPUT", Message: "renameFileImports requires oldPath and newPath"}
				return marshal(result)
			}
			rename, renameErr := languageservice.RenameFileImportsForProjectFS(params, projectFiles, string(*input.OldPath), string(*input.NewPath))
			if renameErr != nil {
				result.Error = &responseError{Code: "MCL-WASM-IMPORT-RENAME", Message: renameErr.Error()}
				return marshal(result)
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
			result.Result = wasmprotocol.RenameFileImportsResult{OldPath: *input.OldPath, NewPath: *input.NewPath, Edits: edits}
		}
		result.Ok = true
		return marshal(result)
	}

	module, err := engine.ParseAST(input.Source)
	if err != nil {
		result.Error = diagnosticError(input.Source, entrypoint, nil, err)
		return marshal(result)
	}
	switch input.Operation {
	case "parse":
		result.Ok = true
		result.Result = wasmprotocol.ParseResult{Declarations: uint64(len(module.Nodes))}
	case "lint":
		result.Ok = true
		result.Result = diagnostics(module, input.Source, entrypoint)
	case "check", "evaluate", "render":
		var projectFiles fs.FS
		if sessionFiles != nil {
			projectFiles = projectFilesystemView{filesystem: sessionFiles, excluded: entrypoint}
		} else {
			binaryFiles, binaryErr := copyBinaryFiles(binaryArgument, input.Source, input.Files)
			if binaryErr != nil {
				result.Error = &responseError{Code: "MCL-WASM-INPUT", Message: binaryErr.Error()}
				return marshal(result)
			}
			projectFiles, err = newProjectFiles(input.Source, input.Files, binaryFiles, entrypoint)
			if err != nil {
				result.Error = &responseError{Code: "MCL-WASM-INPUT", Message: err.Error()}
				return marshal(result)
			}
		}
		bundle, evalErr := evaluate(input.Source, projectFiles, entrypoint)
		if evalErr != nil {
			result.Error = diagnosticError(input.Source, entrypoint, projectFiles, evalErr)
			return marshal(result)
		}
		if input.Operation == "check" || input.Operation == "render" {
			if checkErr := engine.ValidateBundleOutput(bundle); checkErr != nil {
				result.Error = diagnosticError(input.Source, entrypoint, projectFiles, checkErr)
				return marshal(result)
			}
		}
		if input.Operation == "check" {
			result.Ok = true
			result.Result = wasmprotocol.CheckResult{Valid: true, Artifacts: uint64(len(bundle.Artifacts))}
			return marshal(result)
		}
		var output []byte
		var manifestSourceMap engine.ManifestSourceMap
		var renderErr error
		if input.Operation == "render" {
			output, manifestSourceMap, renderErr = engine.RenderWithSourceMap(bundle)
		} else {
			output, renderErr = engine.Render(bundle)
		}
		if renderErr != nil {
			result.Error = diagnosticError(input.Source, entrypoint, projectFiles, renderErr)
			return marshal(result)
		}
		if len(output) > wasmprotocol.MaxOutputBytes {
			result.Error = &responseError{Code: "MCL-WASM-OUTPUT-LIMIT", Message: fmt.Sprintf("rendered output exceeds the %d-byte limit", wasmprotocol.MaxOutputBytes)}
			return marshal(result)
		}
		result.Ok = true
		if input.Operation == "render" {
			result.Result = wasmprotocol.RenderResult{
				Yaml: string(output), Artifacts: uint64(len(bundle.Artifacts)),
				SourceMap: wasmSourceMap(manifestSourceMap, output, projectFiles, entrypoint, input.Source),
			}
		} else {
			result.Result = wasmprotocol.EvaluateResult{Artifacts: uint64(len(bundle.Artifacts)), RenderedBytes: uint64(len(output))}
		}
	default:
		result.Error = &responseError{Code: "MCL-WASM-OPERATION", Message: "operation must be parse, lint, format, completion, definition, hover, references, rename, renameFileImports, evaluate, check, or render"}
	}

	return marshal(result)
}

func wasmSourceMap(sourceMap engine.ManifestSourceMap, output []byte, files fs.FS, entrypoint, entrypointSource string) wasmprotocol.SourceMap {
	result := wasmprotocol.SourceMap{Complete: true, Entries: make([]wasmprotocol.SourceMapEntry, 0)}
	outputIndex := sourceindex.New(string(output))
	sourceIndices := make(map[string]*sourceindex.Index)
	sourceLengths := make(map[string]int)
	missingSources := make(map[string]bool)
	usedBytes := 0
	if encoded, err := json.Marshal(result); err == nil {
		usedBytes = len(encoded) - len("[]")
	} else {
		result.Complete = false
		return result
	}

	for _, document := range sourceMap.Documents {
		for _, entry := range document.Entries {
			if !entry.HasSource {
				continue
			}
			if entry.OutputStart < 0 || entry.OutputEnd < entry.OutputStart || entry.OutputEnd > len(output) {
				result.Complete = false
				continue
			}
			origins := make([]wasmprotocol.SourceMapOrigin, 0, len(entry.Origins))
			for _, origin := range entry.Origins {
				if !mclSourceOrigin(origin.Kind) {
					continue
				}
				projectPath, ok := sourceMapProjectPath(origin.Path, entrypoint)
				if !ok {
					result.Complete = false
					continue
				}
				sourceIndex, exists := sourceIndices[projectPath]
				if !exists && !missingSources[projectPath] {
					source, found := sourceMapSource(files, projectPath, entrypoint, entrypointSource)
					if found {
						sourceIndex = sourceindex.New(source)
						sourceIndices[projectPath] = sourceIndex
						sourceLengths[projectPath] = len(source)
					} else {
						missingSources[projectPath] = true
					}
				}
				if sourceIndex == nil {
					result.Complete = false
					continue
				}
				if origin.Span.Start < 0 || origin.Span.End < origin.Span.Start || origin.Span.End > sourceLengths[projectPath] {
					result.Complete = false
					continue
				}
				kind, ok := wasmSourceOriginKind(origin.Kind)
				if !ok {
					result.Complete = false
					continue
				}
				multiplicity, ok := wasmSourceOriginMultiplicity(origin.Multiplicity)
				if !ok {
					result.Complete = false
					continue
				}
				origins = append(origins, wasmprotocol.SourceMapOrigin{
					Path: wasmprotocol.ProjectPath(projectPath), Range: wasmSourceRange(sourceIndex, origin.Span.Start, origin.Span.End),
					Kind: kind, Multiplicity: multiplicity, Occurrences: uint64(origin.Occurrences),
				})
			}
			if len(origins) == 0 {
				continue
			}
			kind := wasmprotocol.SourceMapEntryKindValue
			if entry.Kind == "key" {
				kind = wasmprotocol.SourceMapEntryKindKey
			}
			candidate := wasmprotocol.SourceMapEntry{
				Document: uint64(document.Index), Path: entry.Path, Kind: kind,
				Output: wasmSourceRange(outputIndex, entry.OutputStart, entry.OutputEnd), Sources: origins,
			}
			encoded, err := json.Marshal(candidate)
			entryBytes := len(encoded)
			if len(result.Entries) > 0 {
				entryBytes++
			}
			if err != nil || usedBytes+entryBytes > wasmprotocol.MaxSourceMapBytes {
				result.Complete = false
				return result
			}
			usedBytes += entryBytes
			result.Entries = append(result.Entries, candidate)
		}
	}

	return result
}

func sourceMapProjectPath(sourcePath, entrypoint string) (string, bool) {
	if sourcePath == "" {
		return "", false
	}
	cleaned := path.Clean(sourcePath)
	root := browserProjectRoot()
	if cleaned == root {
		cleaned = entrypoint
	} else if strings.HasPrefix(cleaned, root+"/") {
		cleaned = strings.TrimPrefix(cleaned, root+"/")
	} else if path.IsAbs(cleaned) {
		return "", false
	}
	if !validProjectPath(cleaned) {
		return "", false
	}

	return cleaned, true
}

func sourceMapSource(files fs.FS, sourcePath, entrypoint, entrypointSource string) (string, bool) {
	if sourcePath == entrypoint {
		return entrypointSource, true
	}
	if files == nil {
		return "", false
	}
	source, err := fs.ReadFile(files, sourcePath)
	if err != nil {
		return "", false
	}

	return string(source), true
}

func mclSourceOrigin(kind engine.SourceOriginKind) bool {
	_, ok := wasmSourceOriginKind(kind)
	return ok
}

func wasmSourceOriginKind(kind engine.SourceOriginKind) (wasmprotocol.SourceMapOriginKind, bool) {
	switch kind {
	case engine.SourceOriginMCL:
		return wasmprotocol.SourceMapOriginKindMcl, true
	case engine.SourceOriginMCLOverlayBase:
		return wasmprotocol.SourceMapOriginKindMclOverlayBase, true
	case engine.SourceOriginMCLOverlayPatch:
		return wasmprotocol.SourceMapOriginKindMclOverlayPatch, true
	default:
		return "", false
	}
}

func wasmSourceOriginMultiplicity(multiplicity engine.SourceOriginMultiplicity) (wasmprotocol.SourceMapOriginMultiplicity, bool) {
	switch multiplicity {
	case engine.SourceOriginUnique:
		return wasmprotocol.SourceMapOriginMultiplicityUnique, true
	case engine.SourceOriginRepeated:
		return wasmprotocol.SourceMapOriginMultiplicityRepeated, true
	case engine.SourceOriginAmbiguous:
		return wasmprotocol.SourceMapOriginMultiplicityAmbiguous, true
	default:
		return "", false
	}
}

func wasmSourceRange(index *sourceindex.Index, start, end int) wasmprotocol.SourceRange {
	startPosition := index.PositionAt(start)
	endPosition := index.PositionAt(end)
	return wasmprotocol.SourceRange{
		Start: wasmprotocol.CompletionPosition{Line: uint64(startPosition.Line), Character: uint64(startPosition.Character)},
		End:   wasmprotocol.CompletionPosition{Line: uint64(endPosition.Line), Character: uint64(endPosition.Character)},
	}
}

func applyProjectUpdate(projectID, raw string, binaryArgument js.Value) string {
	var update projectUpdate
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-PROJECT", Message: "project update must be valid JSON"}})
	}
	if projectID == "" {
		return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-PROJECT", Message: "project ID must not be empty"}})
	}
	if update.Files == nil {
		update.Files = map[string]fileInput{}
	}
	binaryFiles, err := copyBinaryFiles(binaryArgument, "", update.Files)
	if err != nil {
		return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-PROJECT", Message: err.Error()}})
	}

	projectSessionMu.Lock()
	defer projectSessionMu.Unlock()
	project := activeProject
	if update.Reset {
		project = &projectFilesystem{files: make(fstest.MapFS)}
	} else if activeProjectID != projectID || project == nil {
		return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-PROJECT", Message: "project session must be reset before applying changes"}})
	}
	if err := project.applyDelta(update.Files, binaryFiles, update.Deleted, update.Reset); err != nil {
		return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-PROJECT", Message: err.Error()}})
	}
	activeProjectID = projectID
	activeProject = project
	return marshal(response{Ok: true})
}

func projectFilesForSession(projectID, entrypoint, source string) (*projectFilesystem, error) {
	projectSessionMu.Lock()
	defer projectSessionMu.Unlock()
	if activeProjectID != projectID || activeProject == nil {
		return nil, errors.New("project session is unavailable; synchronize the project again")
	}
	content, err := fs.ReadFile(activeProject, entrypoint)
	if err != nil {
		return nil, fmt.Errorf("project entrypoint %q is unavailable", entrypoint)
	}
	if string(content) != source {
		return nil, errors.New("project entrypoint source does not match the synchronized project")
	}
	return activeProject, nil
}

func evaluate(source string, files fs.FS, entrypoint string) (engine.Bundle, error) {
	ctx, cancel := context.WithTimeout(context.Background(), evaluationTime)
	defer cancel()

	return engine.EvaluateProjectSourceContext(ctx, browserProjectRoot(), entrypoint, source, files, nil)
}

func copyBinaryFiles(value js.Value, source string, files map[string]fileInput) (map[string][]byte, error) {
	binaryCount := 0
	for _, input := range files {
		if input.Encoding == wasmprotocol.FileInputEncodingBinary {
			binaryCount++
		}
	}
	if value.Type() == js.TypeUndefined || value.Type() == js.TypeNull {
		if binaryCount != 0 {
			return nil, errors.New("binary project files are missing from the binary file map")
		}
		return nil, nil
	}
	if value.Type() != js.TypeObject {
		return nil, errors.New("binary file map must be an object mapping paths to Uint8Array values")
	}

	keys := js.Global().Get("Object").Call("keys", value)
	if keys.Length() != binaryCount {
		return nil, errors.New("binary file map does not match binary file metadata")
	}
	if keys.Length() > wasmprotocol.MaxProjectFiles {
		return nil, fmt.Errorf("project has more than %d files", wasmprotocol.MaxProjectFiles)
	}
	uint8Array := js.Global().Get("Uint8Array")
	projectBytes := len(source)
	result := make(map[string][]byte, keys.Length())
	binaryMap := value
	for index := 0; index < keys.Length(); index++ {
		filePath := keys.Index(index).String()
		metadata, ok := files[filePath]
		if !ok || metadata.Encoding != wasmprotocol.FileInputEncodingBinary {
			return nil, fmt.Errorf("binary file map contains path %q without binary metadata", filePath)
		}
		fileValue := binaryMap.Get(filePath)
		if !fileValue.InstanceOf(uint8Array) {
			return nil, fmt.Errorf("binary project file %q must be a Uint8Array", filePath)
		}
		fileBytes := fileValue.Get("byteLength").Int()
		if fileBytes > wasmprotocol.MaxProjectFileBytes {
			return nil, fmt.Errorf("project file %q exceeds the %d-byte limit", filePath, wasmprotocol.MaxProjectFileBytes)
		}
		projectBytes += fileBytes
		if projectBytes > wasmprotocol.MaxProjectBytes {
			return nil, fmt.Errorf("project files exceed the %d-byte limit", wasmprotocol.MaxProjectBytes)
		}
		content := make([]byte, fileBytes)
		if copied := js.CopyBytesToGo(content, fileValue); copied != fileBytes {
			return nil, fmt.Errorf("copy binary project file %q: copied %d of %d bytes", filePath, copied, fileBytes)
		}
		result[filePath] = content
	}

	return result, nil
}

func browserProjectRoot() string {
	if configuredRoot := os.Getenv("MCL_WASM_PROJECT_ROOT"); path.IsAbs(configuredRoot) {
		return path.Clean(configuredRoot)
	}

	return projectRoot
}

func projectEntrypoint(entrypoint string) (string, error) {
	if entrypoint == "" {
		return "main.mcl", nil
	}
	if !validProjectPath(entrypoint) {
		return "", fmt.Errorf("invalid project entrypoint %q", entrypoint)
	}

	return entrypoint, nil
}

func diagnostics(module engine.ModuleAST, source, sourcePath string) wasmprotocol.LintResult {
	items := engine.LintAST(module)
	result := make(wasmprotocol.LintResult, 0, len(items))
	for _, item := range items {
		line, column := sourcePosition(source, item.Span.Start)
		endLine, endColumn := sourcePosition(source, item.Span.End)
		result = append(result, wasmprotocol.LintDiagnostic{
			Code: item.Code, Message: item.Message, Severity: lintSeverity(item.Severity), Path: sourcePath,
			Line: uint64(line), Column: uint64(column), EndLine: uint64(endLine), EndColumn: uint64(endColumn),
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

func diagnosticError(source, entrypoint string, files fs.FS, err error) *responseError {
	code := "MCL-WASM-EVALUATION"
	line, column := 0, 0
	endLine, endColumn := 0, 0
	locationPath := entrypoint
	message := err.Error()
	var related []wasmprotocol.RelatedLocation
	if diagnostic, ok := engine.SourceDiagnosticFromError(err); ok {
		code = diagnostic.Code
		locationPath = projectRelativeDiagnosticPath(diagnostic.Path, entrypoint)
		locationSource := projectFileSource(files, locationPath, entrypoint, source)
		line, column = sourcePosition(locationSource, diagnostic.Span.Start)
		endLine, endColumn = sourcePosition(locationSource, diagnostic.Span.End)
		for _, location := range diagnostic.Related {
			relatedPath := projectRelativeDiagnosticPath(location.Path, entrypoint)
			relatedSource := projectFileSource(files, relatedPath, entrypoint, source)
			relatedLine, relatedColumn := sourcePosition(relatedSource, location.Span.Start)
			relatedEndLine, relatedEndColumn := sourcePosition(relatedSource, location.Span.End)
			related = append(related, wasmprotocol.RelatedLocation{
				Path: relatedPath, Message: location.Message,
				Line: uint64(relatedLine), Column: uint64(relatedColumn),
				EndLine: uint64(relatedEndLine), EndColumn: uint64(relatedEndColumn),
			})
		}
		message = diagnostic.DisplayMessage()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = "MCL-WASM-TIMEOUT"
		message = "evaluation exceeded the 4-second limit"
	}
	if errors.Is(err, dependency.ErrRemoteDependencyUnavailable) {
		code = "MCL-WASM-NETWORK"
		message = "remote dependency access is unavailable in this browser runtime; include dependencies in the project files"
	}

	return &responseError{
		Code: code, Message: message,
		Path: &locationPath,
		Line: new(uint64(line)), Column: new(uint64(column)),
		EndLine: new(uint64(endLine)), EndColumn: new(uint64(endColumn)),
		Related: related,
	}
}

func projectRelativeDiagnosticPath(sourcePath, entrypoint string) string {
	if sourcePath == "" {
		return entrypoint
	}
	cleaned := path.Clean(sourcePath)
	root := browserProjectRoot()
	if cleaned == root {
		return entrypoint
	}
	if strings.HasPrefix(cleaned, root+"/") {
		return strings.TrimPrefix(cleaned, root+"/")
	}
	return strings.TrimPrefix(cleaned, "/")
}

func projectFileSource(files fs.FS, sourcePath, entrypoint, fallback string) string {
	if sourcePath == "" || sourcePath == entrypoint || files == nil {
		return fallback
	}
	source, err := fs.ReadFile(files, sourcePath)
	if err != nil {
		return fallback
	}
	return string(source)
}

func sourcePosition(source string, offset int) (int, int) {
	return engine.SourcePositionUTF16(source, offset)
}

func marshal(value response) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `{"ok":false,"error":{"code":"MCL-WASM-SERIALIZE","message":"failed to serialize response"}}`
	}

	return string(encoded)
}

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
	if handleLanguageServiceOperation(input, &result, binaryArgument, sessionFiles, entrypoint) {
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
		result.Error = &responseError{
			Code:    "MCL-WASM-OPERATION",
			Message: fmt.Sprintf("unsupported operation %q; supported operations are: %s", input.Operation, wasmprotocol.RequestOperationNames),
		}
	}

	return marshal(result)
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

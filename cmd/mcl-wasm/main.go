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
	"path/filepath"
	"strings"
	"syscall"
	"syscall/js"
	"time"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/wasmprotocol"
)

const (
	maxOutputBytes = 1 << 20
	evaluationTime = 4 * time.Second
	projectRoot    = "/mcl-project"
)

type request = wasmprotocol.Request
type response = wasmprotocol.Response
type fileInput = wasmprotocol.FileInput
type responseError = wasmprotocol.Error

func main() {
	api := js.Global().Get("Object").New()
	run := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if (len(args) != 1 && len(args) != 2) || args[0].Type() != js.TypeString {
			return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-REQUEST", Message: "run expects a JSON string and an optional binary file map"}})
		}

		binaryFiles := js.Undefined()
		if len(args) == 2 {
			binaryFiles = args[1]
		}

		return handle(args[0].String(), binaryFiles)
	})
	api.Set("run", run)
	js.Global().Set("mclWasm", api)
	js.Global().Set("mclWasmReady", true)
	select {}
}

func handle(raw string, binaryArgument js.Value) string {
	var input request
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-REQUEST", Message: "request must be valid JSON"}})
	}
	result := response{RequestID: input.RequestID, Revision: input.Revision}
	if len(input.Source) > maxSourceBytes {
		result.Error = &responseError{Code: "MCL-WASM-SOURCE-LIMIT", Message: fmt.Sprintf("source exceeds the %d-byte limit", maxSourceBytes)}
		return marshal(result)
	}
	entrypointInput := ""
	if input.Entrypoint != nil {
		entrypointInput = *input.Entrypoint
	}
	entrypoint, err := projectEntrypoint(entrypointInput)
	if err != nil {
		result.Error = &responseError{Code: "MCL-WASM-INPUT", Message: err.Error()}
		return marshal(result)
	}
	if input.Operation == "format" {
		formatted, err := engine.FormatSource(input.Source)
		if err != nil {
			result.Error = diagnosticError(input.Source, err)
		} else if len(formatted) > maxOutputBytes {
			result.Error = &responseError{Code: "MCL-WASM-OUTPUT-LIMIT", Message: fmt.Sprintf("formatted output exceeds the %d-byte limit", maxOutputBytes)}
		} else {
			result.Ok = true
			result.Result = map[string]any{"source": formatted}
		}
		return marshal(result)
	}

	module, err := engine.ParseAST(input.Source)
	if err != nil {
		result.Error = diagnosticError(input.Source, err)
		return marshal(result)
	}
	switch input.Operation {
	case "parse":
		result.Ok = true
		result.Result = map[string]any{"declarations": len(module.Nodes)}
	case "lint":
		result.Ok = true
		result.Result = diagnostics(module, input.Source)
	case "check", "evaluate", "render":
		binaryFiles, binaryErr := copyBinaryFiles(binaryArgument, input.Source, input.Files)
		if binaryErr != nil {
			result.Error = &responseError{Code: "MCL-WASM-INPUT", Message: binaryErr.Error()}
			return marshal(result)
		}
		projectFiles, filesErr := newProjectFiles(input.Source, input.Files, binaryFiles, entrypoint)
		if filesErr != nil {
			result.Error = &responseError{Code: "MCL-WASM-INPUT", Message: filesErr.Error()}
			return marshal(result)
		}
		bundle, evalErr := evaluate(input.Source, projectFiles, entrypoint)
		if evalErr != nil {
			result.Error = diagnosticError(input.Source, evalErr)
			return marshal(result)
		}
		if input.Operation == "check" || input.Operation == "render" {
			catalog, catalogErr := engine.LoadKubernetesResourceCatalogForProjectFS(browserProjectRoot(), projectFiles)
			if catalogErr != nil {
				result.Error = diagnosticError(input.Source, catalogErr)
				return marshal(result)
			}
			if checkErr := engine.ValidateBundleOutputWithCatalog(bundle, catalog); checkErr != nil {
				result.Error = diagnosticError(input.Source, checkErr)
				return marshal(result)
			}
		}
		output, renderErr := engine.Render(bundle)
		if renderErr != nil {
			result.Error = diagnosticError(input.Source, renderErr)
			return marshal(result)
		}
		if len(output) > maxOutputBytes {
			result.Error = &responseError{Code: "MCL-WASM-OUTPUT-LIMIT", Message: fmt.Sprintf("rendered output exceeds the %d-byte limit", maxOutputBytes)}
			return marshal(result)
		}
		result.Ok = true
		if input.Operation == "render" {
			result.Result = map[string]any{"yaml": string(output), "artifacts": len(bundle.Artifacts)}
		} else if input.Operation == "check" {
			result.Result = map[string]any{"valid": true, "artifacts": len(bundle.Artifacts)}
		} else {
			result.Result = map[string]any{"artifacts": len(bundle.Artifacts), "renderedBytes": len(output)}
		}
	default:
		result.Error = &responseError{Code: "MCL-WASM-OPERATION", Message: "operation must be parse, lint, format, evaluate, check, or render"}
	}

	return marshal(result)
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
	if keys.Length() > maxProjectFiles-1 {
		return nil, fmt.Errorf("project has more than %d files", maxProjectFiles)
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
		if fileBytes > maxProjectFileBytes {
			return nil, fmt.Errorf("project file %q exceeds the %d-byte limit", filePath, maxProjectFileBytes)
		}
		projectBytes += fileBytes
		if projectBytes > maxProjectBytes {
			return nil, fmt.Errorf("project files exceed the %d-byte limit", maxProjectBytes)
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
	if !filepath.IsLocal(entrypoint) || path.Clean(entrypoint) != entrypoint || entrypoint == "." || strings.Contains(entrypoint, "\\") {
		return "", fmt.Errorf("invalid project entrypoint %q", entrypoint)
	}

	return entrypoint, nil
}

func diagnostics(module engine.ModuleAST, source string) []map[string]any {
	items := engine.LintAST(module)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		line, column := sourcePosition(source, item.Span.Start)
		result = append(result, map[string]any{
			"code": item.Code, "message": item.Message, "severity": item.Severity,
			"line": line, "column": column,
		})
	}

	return result
}

func diagnosticError(source string, err error) *responseError {
	code := "MCL-WASM-EVALUATION"
	line, column := 0, 0
	message := err.Error()
	if diagnostic, ok := engine.SourceDiagnosticFromError(err); ok {
		code = diagnostic.Code
		line, column = sourcePosition(source, diagnostic.Span.Start)
		message = diagnostic.DisplayMessage()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = "MCL-WASM-TIMEOUT"
		message = "evaluation exceeded the 4-second limit"
	}
	if errors.Is(err, syscall.ENOSYS) {
		code = "MCL-WASM-NETWORK"
		message = "remote dependency access is unavailable in this browser runtime; provide dependencies in the project files"
	}

	return &responseError{Code: code, Message: message, Line: new(uint64(line)), Column: new(uint64(column))}
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

//go:build js && wasm

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	maxSourceBytes      = 64 << 10
	maxProjectFiles     = 4096
	maxProjectFileBytes = 16 << 20
	maxProjectBytes     = 64 << 20
	maxOutputBytes      = 1 << 20
	evaluationTime      = 4 * time.Second
	projectRoot         = "/mcl-project"
)

type request = wasmprotocol.Request
type response = wasmprotocol.Response
type fileInput = wasmprotocol.FileInput
type responseError = wasmprotocol.Error

func main() {
	api := js.Global().Get("Object").New()
	run := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) != 1 || args[0].Type() != js.TypeString {
			return marshal(response{Ok: false, Error: &responseError{Code: "MCL-WASM-REQUEST", Message: "run expects one JSON string argument"}})
		}

		return handle(args[0].String())
	})
	api.Set("run", run)
	js.Global().Set("mclWasm", api)
	js.Global().Set("mclWasmReady", true)
	select {}
}

func handle(raw string) string {
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
		bundle, evalErr := evaluate(input.Source, input.Files, entrypoint)
		if evalErr != nil {
			result.Error = diagnosticError(input.Source, evalErr)
			return marshal(result)
		}
		if input.Operation == "check" || input.Operation == "render" {
			catalog, catalogErr := engine.LoadKubernetesResourceCatalogForFile(path.Join(browserProjectRoot(), entrypoint))
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

func evaluate(source string, files map[string]fileInput, entrypoint string) (engine.Bundle, error) {
	if err := validateProject(source, files, entrypoint); err != nil {
		return engine.Bundle{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), evaluationTime)
	defer cancel()

	return engine.EvaluateSourceWithOverlaysContext(ctx, path.Join(browserProjectRoot(), entrypoint), source, nil)
}

func browserProjectRoot() string {
	if configuredRoot := os.Getenv("MCL_WASM_PROJECT_ROOT"); path.IsAbs(configuredRoot) {
		return path.Clean(configuredRoot)
	}

	return projectRoot
}

func validateProject(source string, files map[string]fileInput, entrypoint string) error {
	if len(files)+1 > maxProjectFiles {
		return fmt.Errorf("project has more than %d files", maxProjectFiles)
	}
	totalBytes := len(source)
	for relativePath, input := range files {
		if !filepath.IsLocal(relativePath) || path.Clean(relativePath) != relativePath || relativePath == "." || relativePath == entrypoint || strings.Contains(relativePath, "\\") {
			return fmt.Errorf("invalid project file path %q", relativePath)
		}
		var content []byte
		switch input.Encoding {
		case wasmprotocol.FileInputEncodingUtf8:
			content = []byte(input.Content)
		case wasmprotocol.FileInputEncodingBase64:
			decoded, err := base64.StdEncoding.DecodeString(input.Content)
			if err != nil {
				return fmt.Errorf("decode project file %q: invalid base64 content", relativePath)
			}
			content = decoded
		default:
			return fmt.Errorf("project file %q encoding must be utf8 or base64", relativePath)
		}
		if len(content) > maxProjectFileBytes {
			return fmt.Errorf("project file %q exceeds the %d-byte limit", relativePath, maxProjectFileBytes)
		}
		totalBytes += len(content)
		if totalBytes > maxProjectBytes {
			return fmt.Errorf("project files exceed the %d-byte limit", maxProjectBytes)
		}
	}

	return nil
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

package engine

import (
	"errors"
	"strconv"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/manifest-configuration-language/mcl/internal/manifestadapter"
)

func manifestLoad(arguments []RuntimeValue, state *EvaluationState) (RuntimeValue, error) {
	if len(arguments) != 1 || arguments[0].Kind() != String {
		return RuntimeValue{}, errors.New("manifest.load expects a source String")
	}
	projectRoot, sourcePath, dependencies := adapterModuleInputs(state, arguments...)
	if projectRoot == "" || sourcePath == "" {
		return RuntimeValue{}, errors.New("manifest.load requires file-based evaluation")
	}
	request := manifestadapter.Request{
		Source: arguments[0].TextValue(), ProjectRoot: projectRoot, SourcePath: sourcePath, Dependencies: dependencies,
	}
	if filesystem := state.Module.filesystem; filesystem != nil {
		request.SourceFS = filesystem.files
		if filesystem.osBacked {
			request.SourceRoot = filesystem.root
		}
	}
	loaded, err := manifestadapter.Load(request)
	if err != nil {
		return RuntimeValue{}, err
	}
	value, err := decodeRuntimeYAMLDocuments(loaded.YAML, loaded.LocalSourcePath, runtimeAllocationBudgetOf(state))
	if err != nil {
		if loaded.LocalSourcePath != "" {
			if line, ok := yamlDecodeErrorLine(err); ok {
				if span, ok := sourceLineSpan(loaded.YAML, line); ok {
					return RuntimeValue{}, SourceDiagnosticError{
						Path:    loaded.LocalSourcePath,
						Code:    "MCL-YAML",
						Message: err.Error(),
						Span:    span,
						Cause:   err,
					}
				}
			}
		}

		return RuntimeValue{}, err
	}

	return manifestRuntimeBundle(value, Lifecycle{Events: []string{phaseMain}, Origin: loaded.Origin}, loaded.LocalSourcePath, runtimeAllocationBudgetOf(state))
}

func manifestRuntimeBundle(documents RuntimeValue, lifecycle Lifecycle, sourcePath string, allocations *runtimeAllocationBudget) (RuntimeValue, error) {
	artifacts := make([]RuntimeArtifact, len(documents.ListItems()))
	for index, document := range documents.ListItems() {
		artifacts[index] = RuntimeArtifact{Value: document, Lifecycle: lifecycle}
	}

	return runtimeBundleValueWithBudget(artifacts, sourcePath, allocations)
}

func adapterModuleInputs(state *EvaluationState, arguments ...RuntimeValue) (string, string, *dependency.Lock) {
	if state == nil || state.Module == nil {
		return "", "", nil
	}
	sourcePath := state.SourcePath
	if len(arguments) > 0 {
		if argumentSourcePath, _ := arguments[0].Source(); argumentSourcePath != "" {
			sourcePath = argumentSourcePath
		}
	}

	return state.Module.ProjectRoot, sourcePath, state.Module.Dependencies
}

func yamlDecodeErrorLine(err error) (int, bool) {
	const prefix = "decode YAML: yaml: line "
	message := err.Error()
	if !strings.HasPrefix(message, prefix) {
		return 0, false
	}
	lineText, _, ok := strings.Cut(strings.TrimPrefix(message, prefix), ":")
	if !ok {
		return 0, false
	}
	line, err := strconv.Atoi(lineText)

	return line, err == nil && line > 0
}

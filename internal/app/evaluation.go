package app

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"io/fs"
	"time"

	"github.com/manifest-configuration-language/mcl/pkg/mcl"
)

// EvaluationOperation selects the output needed by an application request.
type EvaluationOperation uint8

const (
	EvaluationOperationCheck EvaluationOperation = iota + 1
	EvaluationOperationEvaluate
	EvaluationOperationRender
	EvaluationOperationBundle
)

// EvaluationInput identifies a saved MCL file or an in-memory project source.
type EvaluationInput interface {
	appEvaluationInput()
}

// FileEvaluationInput evaluates a saved MCL file.
type FileEvaluationInput struct {
	Path string
}

func (FileEvaluationInput) appEvaluationInput() {}

// ProjectSourceInput evaluates source against an injected project filesystem.
type ProjectSourceInput struct {
	ProjectRoot string
	Entrypoint  string
	Source      string
	Files       fs.FS
}

func (ProjectSourceInput) appEvaluationInput() {}

// EvaluationRequest supplies the input and output requirements for one application operation.
type EvaluationRequest struct {
	Input              EvaluationInput
	Operation          EvaluationOperation
	Phase              string
	LifecycleOperation string
	Parameters         map[string]jsontext.Value
	Options            mcl.EvaluationOptions
}

// EvaluationResult contains the evaluated bundle and the requested rendering.
type EvaluationResult struct {
	Bundle    mcl.Bundle
	Output    []byte
	Files     []mcl.RenderedFile
	SourceMap mcl.ManifestSourceMap
}

// EvaluationService evaluates project source and produces operation-specific output.
type EvaluationService struct {
	Timeout time.Duration
}

func (service EvaluationService) Run(ctx context.Context, request EvaluationRequest) (EvaluationResult, error) {
	if ctx == nil {
		return EvaluationResult{}, errors.New("evaluation context is required")
	}
	if request.Operation < EvaluationOperationCheck || request.Operation > EvaluationOperationBundle {
		return EvaluationResult{}, errors.New("unsupported evaluation operation")
	}
	if request.Phase != "" && request.Operation != EvaluationOperationRender {
		return EvaluationResult{}, errors.New("phase requires render operation")
	}
	if request.Phase != "" && request.LifecycleOperation != "" {
		return EvaluationResult{}, errors.New("phase and lifecycle operation cannot be combined")
	}
	if service.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, service.Timeout)
		defer cancel()
	}

	bundle, err := evaluateInput(ctx, request)
	if err != nil {
		return EvaluationResult{}, err
	}
	result := EvaluationResult{Bundle: bundle}
	if request.Operation == EvaluationOperationCheck || request.Operation == EvaluationOperationRender {
		if err := mcl.ValidateBundleOutput(bundle); err != nil {
			return EvaluationResult{}, err
		}
	}
	if request.Operation == EvaluationOperationCheck {
		return result, nil
	}
	if request.Operation == EvaluationOperationBundle {
		return result, nil
	}

	if request.LifecycleOperation != "" {
		result.Files, err = mcl.RenderOperation(bundle, request.LifecycleOperation)
	} else if request.Operation == EvaluationOperationRender && request.Phase != "" {
		result.Output, result.SourceMap, err = mcl.RenderPhaseWithSourceMap(bundle, request.Phase)
	} else if request.Operation == EvaluationOperationRender {
		result.Output, result.SourceMap, err = mcl.RenderWithSourceMap(bundle)
	} else if request.Operation == EvaluationOperationEvaluate {
		result.Output, err = mcl.Render(bundle)
	} else {
		return EvaluationResult{}, errors.New("unsupported evaluation operation")
	}
	if err != nil {
		return EvaluationResult{}, err
	}

	return result, nil
}

func evaluateInput(ctx context.Context, request EvaluationRequest) (mcl.Bundle, error) {
	switch input := request.Input.(type) {
	case FileEvaluationInput:
		if request.LifecycleOperation != "" {
			return mcl.EvaluateFileForOperationWithParametersAndOptionsContext(
				ctx, input.Path, request.LifecycleOperation, request.Parameters, request.Options,
			)
		}
		return mcl.EvaluateFileWithParametersAndOptionsContext(ctx, input.Path, request.Parameters, request.Options)
	case ProjectSourceInput:
		if request.LifecycleOperation != "" || !isEmptyEvaluationOptions(request.Options) {
			return mcl.Bundle{}, errors.New("project source evaluation does not support evaluation options or lifecycle operations")
		}
		return mcl.EvaluateProjectSourceWithParametersContext(
			ctx, input.ProjectRoot, input.Entrypoint, input.Source, input.Files, nil, request.Parameters,
		)
	default:
		return mcl.Bundle{}, errors.New("evaluation input must identify a saved file or project source")
	}
}

func isEmptyEvaluationOptions(options mcl.EvaluationOptions) bool {
	return !options.AllowNondeterministicOutput && options.CacheDirectory == "" && options.KubernetesVersion == "" &&
		len(options.ParameterSources) == 0 && options.GitCredentialProvider == nil
}

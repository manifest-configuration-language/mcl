package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/manifest-configuration-language/mcl/pkg/mcl"
)

const evaluationTestSource = `emit node:
  apiVersion: "v1"
  kind: "ConfigMap"
  metadata:
    name: "sample"
    namespace: "default"
  data:
    key: "value"
`

func TestEvaluationServiceOperationOutputs(t *testing.T) {
	service := EvaluationService{}
	request := EvaluationRequest{
		Input: ProjectSourceInput{
			ProjectRoot: "/mcl-project", Entrypoint: "main.mcl", Source: evaluationTestSource, Files: fstest.MapFS{},
		},
	}

	checkRequest := request
	checkRequest.Operation = EvaluationOperationCheck
	checked, err := service.Run(t.Context(), checkRequest)
	if err != nil {
		t.Fatalf("Run(check) error = %v", err)
	}
	if len(checked.Bundle.Artifacts) != 1 || len(checked.Output) != 0 || len(checked.SourceMap.Documents) != 0 {
		t.Fatalf("Run(check) = (%d artifacts, %d output bytes, %d source-map documents), want (1, 0, 0)", len(checked.Bundle.Artifacts), len(checked.Output), len(checked.SourceMap.Documents))
	}

	evaluateRequest := request
	evaluateRequest.Operation = EvaluationOperationEvaluate
	evaluated, err := service.Run(t.Context(), evaluateRequest)
	if err != nil {
		t.Fatalf("Run(evaluate) error = %v", err)
	}
	wantOutput, err := mcl.Render(evaluated.Bundle)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if string(evaluated.Output) != string(wantOutput) || len(evaluated.SourceMap.Documents) != 0 {
		t.Fatalf("Run(evaluate) output/source map = (%q, %d documents), want (%q, 0 documents)", evaluated.Output, len(evaluated.SourceMap.Documents), wantOutput)
	}

	renderRequest := request
	renderRequest.Operation = EvaluationOperationRender
	rendered, err := service.Run(t.Context(), renderRequest)
	if err != nil {
		t.Fatalf("Run(render) error = %v", err)
	}
	wantRendered, wantSourceMap, err := mcl.RenderWithSourceMap(rendered.Bundle)
	if err != nil {
		t.Fatalf("RenderWithSourceMap() error = %v", err)
	}
	if string(rendered.Output) != string(wantRendered) || len(rendered.SourceMap.Documents) != len(wantSourceMap.Documents) {
		t.Fatalf("Run(render) output/source map = (%q, %d documents), want (%q, %d documents)", rendered.Output, len(rendered.SourceMap.Documents), wantRendered, len(wantSourceMap.Documents))
	}
}

func TestEvaluationServiceHonorsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := (EvaluationService{}).Run(ctx, EvaluationRequest{
		Operation: EvaluationOperationEvaluate,
		Input: ProjectSourceInput{
			ProjectRoot: "/mcl-project", Entrypoint: "main.mcl", Source: evaluationTestSource, Files: fstest.MapFS{},
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestEvaluationServiceSupportsSavedFileInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.mcl")
	if err := os.WriteFile(path, []byte(evaluationTestSource), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (EvaluationService{}).Run(t.Context(), EvaluationRequest{
		Input: FileEvaluationInput{Path: path}, Operation: EvaluationOperationBundle,
	})
	if err != nil {
		t.Fatalf("Run(file input) error = %v", err)
	}
	if len(result.Bundle.Artifacts) != 1 || len(result.Output) != 0 {
		t.Fatalf("Run(file input) = (%d artifacts, %d output bytes), want (1, 0)", len(result.Bundle.Artifacts), len(result.Output))
	}
}

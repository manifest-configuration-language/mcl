package engine

import (
	"encoding/json/v2"
	"path/filepath"
	"testing"
)

func TestRenderPhaseArtifactsPreservesResourceAndLifecycle(t *testing.T) {
	source := `let resource =
  apiVersion: "v1"
  kind: "ConfigMap"
  metadata:
    name: "sample"

let hook = lifecycle.attach(resource, ["before.create"], 4, false, ["on-success"])
emit hook
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := RenderPhaseArtifacts(bundle, phaseBeforeCreate)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("RenderPhaseArtifacts() returned %d artifacts, want 1", len(artifacts))
	}
	var resource map[string]any
	if err := json.Unmarshal(artifacts[0].Resource, &resource); err != nil {
		t.Fatalf("resource is invalid JSON: %v", err)
	}
	if resource["kind"] != "ConfigMap" || artifacts[0].Lifecycle.Priority != 4 || len(artifacts[0].Lifecycle.Cleanup) != 1 || artifacts[0].Lifecycle.Cleanup[0] != cleanupOnSuccess {
		t.Fatalf("unexpected structured artifact: resource=%v lifecycle=%+v", resource, artifacts[0].Lifecycle)
	}
}

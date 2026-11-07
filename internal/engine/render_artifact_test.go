package engine

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"
)

func TestRenderPhaseArtifactsPreservesResourceAndLifecycle(t *testing.T) {
	t.Parallel()

	source := `let resource =
  apiVersion: "coordination.k8s.io/v1"
  kind: "Lease"
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
	if resource["kind"] != "Lease" || artifacts[0].Lifecycle.Priority != 4 || len(artifacts[0].Lifecycle.Cleanup) != 1 || artifacts[0].Lifecycle.Cleanup[0] != cleanupOnSuccess {
		t.Fatalf("unexpected structured artifact: resource=%v lifecycle=%+v", resource, artifacts[0].Lifecycle)
	}
}

func TestRenderPhaseArtifactsMatchPhaseOutputForHelmResources(t *testing.T) {
	t.Parallel()

	root := filepath.Clean(t.TempDir())
	files := fstest.MapFS{
		"chart/Chart.yaml":               &fstest.MapFile{Data: []byte("apiVersion: v2\nname: app\nversion: 1.0.0\n")},
		helmConfigMapPath:                &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}\n")},
		"chart/templates/migration.yaml": &fstest.MapFile{Data: []byte("apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: migration\n  annotations:\n    helm.sh/hook: pre-install\n    helm.sh/hook-weight: \"-2\"\nspec:\n  template:\n    spec:\n      restartPolicy: Never\n      containers:\n        - name: migration\n          image: busybox:1.37.0\n")},
	}
	source := `import "std:helm" as helm
let options =
  releaseName: "sample"
  namespace: "default"
  kubeVersion: "1.36.0"
  apiVersions: ["apps/v1"]

let resources = helm.render("chart", options)
emit resources
`
	bundle, err := EvaluateProjectSourceContext(t.Context(), root, "main.mcl", source, files, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, phase := range []string{phaseMain, phaseBeforeCreate} {
		phaseOutput, err := RenderPhase(bundle, phase)
		if err != nil {
			t.Fatalf("RenderPhase(%q): %v", phase, err)
		}
		artifacts, err := RenderPhaseArtifacts(bundle, phase)
		if err != nil {
			t.Fatalf("RenderPhaseArtifacts(%q): %v", phase, err)
		}
		var outputDocuments []map[string]any
		decoder := yaml.NewDecoder(bytes.NewReader(phaseOutput))
		for {
			var document map[string]any
			if err := decoder.Decode(&document); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatalf("decode RenderPhase(%q): %v", phase, err)
			}
			if document != nil {
				outputDocuments = append(outputDocuments, document)
			}
		}
		artifactDocuments := make([]map[string]any, 0, len(artifacts))
		for _, artifact := range artifacts {
			var document map[string]any
			if err := yaml.Unmarshal(artifact.Resource, &document); err != nil {
				t.Fatalf("decode %q artifact JSON: %v", phase, err)
			}
			artifactDocuments = append(artifactDocuments, document)
		}
		if !reflect.DeepEqual(outputDocuments, artifactDocuments) {
			t.Errorf("RenderPhase(%q) documents differ from structured artifacts:\noutput: %#v\nartifacts: %#v", phase, outputDocuments, artifactDocuments)
		}
	}
}

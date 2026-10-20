package kustomizeadapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
)

func TestBuildIgnoresUnreferencedFilesAndDirectories(t *testing.T) {
	t.Parallel()

	projectRoot := t.TempDir()
	projectRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(projectRoot, "deployment")
	buildDirectory := filepath.Join(sourceRoot, "overlays", "production")
	if err := os.MkdirAll(buildDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(buildDirectory, "kustomization.yaml"): "resources:\n- deployment.yaml\n",
		filepath.Join(buildDirectory, "deployment.yaml"):    "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app\nspec:\n  selector:\n    matchLabels:\n      app: app\n  template:\n    metadata:\n      labels:\n        app: app\n    spec:\n      containers:\n      - name: app\n        image: app:latest\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(name, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(buildDirectory, "unused.bin"), []byte(strings.Repeat("x", maxSourceBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelatedDirectory := filepath.Join(sourceRoot, "overlays", "development", "unrelated")
	if err := os.MkdirAll(unrelatedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unrelatedDirectory, "large.bin"), []byte("unused"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(projectRoot, "missing"), filepath.Join(buildDirectory, "unused-link")); err != nil {
		t.Fatal(err)
	}

	result, err := Build(Request{
		Source:       "./deployment",
		Directory:    "overlays/production",
		ProjectRoot:  projectRoot,
		SourcePath:   filepath.Join(projectRoot, "main.mcl"),
		Dependencies: &dependency.Lock{},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !strings.Contains(string(result.YAML), "name: app") {
		t.Fatalf("Build() output does not include the referenced deployment:\n%s", result.YAML)
	}
}

func TestBuildRejectsReferencedFileOverInputLimit(t *testing.T) {
	t.Parallel()

	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	buildDirectory := filepath.Join(projectRoot, "deployment")
	if err := os.MkdirAll(buildDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDirectory, "kustomization.yaml"), []byte("resources:\n- large.yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	largeFile, err := os.Create(filepath.Join(buildDirectory, "large.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := largeFile.Truncate(maxSourceBytes + 1); err != nil {
		_ = largeFile.Close()
		t.Fatal(err)
	}
	if err := largeFile.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Build(Request{
		Source:       "./deployment",
		Directory:    ".",
		ProjectRoot:  projectRoot,
		SourcePath:   filepath.Join(projectRoot, "main.mcl"),
		Dependencies: &dependency.Lock{},
	})
	if err == nil || !strings.Contains(err.Error(), "inputs exceed") {
		t.Fatalf("Build() error = %v, want referenced input size diagnostic", err)
	}
}

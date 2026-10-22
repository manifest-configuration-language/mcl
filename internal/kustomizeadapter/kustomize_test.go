package kustomizeadapter

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
)

const testDeploymentSource = "./deployment"

func TestLoadGitSourceFSUsesValidatedInMemoryTree(t *testing.T) {
	t.Parallel()

	archive := makeGitArchive(t, []gitArchiveEntry{{name: "base/kustomization.yaml", body: "resources:\n- app.yaml\n"}})
	filesystem, root, err := loadGitSourceFS(archive)
	if err != nil {
		t.Fatalf("loadGitSourceFS() error = %v", err)
	}
	if root != "/mcl-source" {
		t.Fatalf("loadGitSourceFS() root = %q, want /mcl-source", root)
	}
	got, err := filesystem.ReadFile(filepath.Join(root, "base/kustomization.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "resources:\n- app.yaml\n" {
		t.Fatalf("ReadFile() = %q", got)
	}
}

func TestLoadGitSourceFSRejectsUnsafeArchiveEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries []gitArchiveEntry
	}{
		{name: "path traversal", entries: []gitArchiveEntry{{name: "../outside.yaml", body: "unsafe"}}},
		{name: "symlink", entries: []gitArchiveEntry{{name: "link.yaml", typeflag: tar.TypeSymlink, body: "outside.yaml"}}},
		{name: "duplicate path", entries: []gitArchiveEntry{{name: "app.yaml", body: "one"}, {name: "app.yaml", body: "two"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := loadGitSourceFS(makeGitArchive(t, test.entries)); err == nil {
				t.Fatal("loadGitSourceFS() error = nil, want invalid archive error")
			}
		})
	}
}

type gitArchiveEntry struct {
	name     string
	body     string
	typeflag byte
}

func makeGitArchive(t *testing.T, entries []gitArchiveEntry) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.body)), Typeflag: typeflag}
		if typeflag == tar.TypeSymlink {
			header.Linkname = entry.body
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := writer.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return archive.Bytes()
}

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
		Source:       testDeploymentSource,
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

func TestBuildReadsRootRelativeProjectFS(t *testing.T) {
	t.Parallel()

	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := fstest.MapFS{
		"deployment/base/kustomization.yaml": &fstest.MapFile{Data: []byte("resources:\n- deployment.yaml\n")},
		"deployment/base/deployment.yaml":    &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n")},
	}
	result, err := Build(Request{
		Source: testDeploymentSource, Directory: "base", ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"), SourceFS: project,
		Dependencies: &dependency.Lock{},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !strings.Contains(string(result.YAML), "name: app") {
		t.Fatalf("Build() output does not include the root-relative project resource:\n%s", result.YAML)
	}
	if result.SourceOSBacked {
		t.Fatal("Build() marked an in-memory source filesystem as host-backed")
	}
}

func TestBuildPreservesHostPathProvenanceForInjectedFS(t *testing.T) {
	t.Parallel()

	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := fstest.MapFS{
		"deployment/base/kustomization.yaml": &fstest.MapFile{Data: []byte("resources:\n- deployment.yaml\n")},
		"deployment/base/deployment.yaml":    &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n")},
	}
	result, err := Build(Request{
		Source: testDeploymentSource, Directory: "base", ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"), SourceFS: project, SourcePathsOSBacked: true,
		Dependencies: &dependency.Lock{},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !result.SourceOSBacked {
		t.Fatal("Build() did not preserve host-backed source path provenance")
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
		Source:       testDeploymentSource,
		Directory:    ".",
		ProjectRoot:  projectRoot,
		SourcePath:   filepath.Join(projectRoot, "main.mcl"),
		Dependencies: &dependency.Lock{},
	})
	if err == nil || !strings.Contains(err.Error(), "inputs exceed") {
		t.Fatalf("Build() error = %v, want referenced input size diagnostic", err)
	}
}

package manifestadapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadLocalManifestEnforcesProjectBoundary(t *testing.T) {
	t.Parallel()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projectFile := filepath.Join(root, "main.mcl")
	manifestPath := filepath.Join(root, "manifests", "deployment.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("apiVersion: v1\nkind: ConfigMap\n")
	if err := os.WriteFile(manifestPath, want, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Load(Request{Source: "manifests/deployment.yaml", ProjectRoot: root, SourcePath: projectFile})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.YAML) != string(want) || result.LocalSourcePath != manifestPath || result.Origin != "manifests/deployment.yaml" {
		t.Fatalf("Load() = %#v, want local content and normalized origin", result)
	}

	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.yaml")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	_, err = Load(Request{Source: "escape.yaml", ProjectRoot: root, SourcePath: projectFile})
	if err == nil || !strings.Contains(err.Error(), "escapes the project directory") {
		t.Fatalf("Load() error = %v, want project boundary rejection", err)
	}
}

func TestLoadRejectsAbsoluteAndNonRegularManifestSources(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	projectFile := filepath.Join(root, "main.mcl")
	if _, err := Load(Request{Source: filepath.Join(root, "absolute.yaml"), ProjectRoot: root, SourcePath: projectFile}); err == nil {
		t.Fatal("Load() accepted an absolute source")
	}
	if err := os.Mkdir(filepath.Join(root, "directory.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(Request{Source: "directory.yaml", ProjectRoot: root, SourcePath: projectFile}); err == nil {
		t.Fatal("Load() accepted a directory as a manifest")
	}
}

func TestLoadProjectFilesystemRejectsInvalidManifestSources(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	tests := []struct {
		name      string
		source    string
		files     fstest.MapFS
		wantError string
	}{
		{
			name:      "absolute path",
			source:    "/outside.yaml",
			wantError: "local relative sources",
		},
		{
			name:      "path escape",
			source:    "../../outside.yaml",
			wantError: "escapes the project directory",
		},
		{
			name:      "directory",
			source:    "directory",
			files:     fstest.MapFS{"directory/child.yaml": &fstest.MapFile{Data: []byte("apiVersion: v1\n")}},
			wantError: "regular file",
		},
		{
			name:      "oversized file",
			source:    "large.yaml",
			files:     fstest.MapFS{"large.yaml": &fstest.MapFile{Data: []byte(strings.Repeat("x", maxManifestBytes+1))}},
			wantError: "byte limit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(Request{
				Source: test.source, ProjectRoot: root, SourcePath: filepath.Join(root, "main.mcl"), SourceFS: test.files,
			})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Load() error = %v, want error containing %q", err, test.wantError)
			}
		})
	}
}

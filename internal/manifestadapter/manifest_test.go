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

	result, err := Load(Request{
		Source: "manifests/deployment.yaml", ProjectRoot: root, SourcePath: projectFile,
		SourceFS: os.DirFS(root), SourceRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.YAML) != string(want) || result.LocalSourcePath != manifestPath || result.Origin != "manifests/deployment.yaml" {
		t.Fatalf("Load() = %#v, want local content and normalized origin", result)
	}
	if err := os.Symlink(manifestPath, filepath.Join(root, "alias.yaml")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	result, err = Load(Request{
		Source: "alias.yaml", ProjectRoot: root, SourcePath: projectFile,
		SourceFS: os.DirFS(root), SourceRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.LocalSourcePath != manifestPath || result.Origin != "manifests/deployment.yaml" {
		t.Fatalf("Load() through an in-project symlink = %#v, want canonical source path", result)
	}

	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.yaml")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	_, err = Load(Request{
		Source: "escape.yaml", ProjectRoot: root, SourcePath: projectFile,
		SourceFS: os.DirFS(root), SourceRoot: root,
	})
	if err == nil || !strings.Contains(err.Error(), "escapes the project directory") {
		t.Fatalf("Load() error = %v, want project boundary rejection", err)
	}
}

func TestLoadRejectsAbsoluteAndNonRegularManifestSources(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	projectFile := filepath.Join(root, "main.mcl")
	files := os.DirFS(root)
	if _, err := Load(Request{
		Source: filepath.Join(root, "absolute.yaml"), ProjectRoot: root, SourcePath: projectFile,
		SourceFS: files, SourceRoot: root,
	}); err == nil {
		t.Fatal("Load() accepted an absolute source")
	}
	if err := os.Mkdir(filepath.Join(root, "directory.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(Request{
		Source: "directory.yaml", ProjectRoot: root, SourcePath: projectFile,
		SourceFS: files, SourceRoot: root,
	}); err == nil {
		t.Fatal("Load() accepted a directory as a manifest")
	}
}

func TestLoadRequiresExplicitProjectFilesystem(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manifest := filepath.Join(root, "manifest.yaml")
	if err := os.WriteFile(manifest, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(Request{Source: "manifest.yaml", ProjectRoot: root, SourcePath: filepath.Join(root, "main.mcl")})
	if err == nil || !strings.Contains(err.Error(), "requires a project filesystem") {
		t.Fatalf("Load() error = %v, want explicit filesystem requirement", err)
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

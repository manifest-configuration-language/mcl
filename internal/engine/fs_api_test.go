package engine

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEvaluateProjectSourceUsesRootRelativeFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		"child.mcl": &fstest.MapFile{Data: []byte("export type Child = String")},
	}

	bundle, loaded, err := EvaluateProjectSourceWithDependenciesContext(t.Context(), root, "main.mcl", `import "./child.mcl" as child`, files, nil)
	if err != nil {
		t.Fatalf("evaluate project source: %v", err)
	}
	if len(bundle.Artifacts) != 0 {
		t.Fatalf("got %d artifacts, want none", len(bundle.Artifacts))
	}
	if !slices.Contains(loaded, filepath.Join(root, "child.mcl")) {
		t.Fatalf("loaded modules = %v, want child module %q", loaded, filepath.Join(root, "child.mcl"))
	}
}

func TestEvaluateProjectSourceReadsDependencyLockFromFilesystem(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"mcl.lock": &fstest.MapFile{Data: []byte(`{"version":4}`)},
	}

	_, err := EvaluateProjectSourceContext(t.Context(), filepath.Join(string(filepath.Separator), "virtual-project"), "main.mcl", "", files, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported dependency lock version 4") {
		t.Fatalf("evaluate with virtual lock: got %v, want unsupported lock version", err)
	}
}

func TestEvaluateProjectFileUsesRootRelativeFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		"main.mcl":  &fstest.MapFile{Data: []byte("import \"./child.mcl\" as child\nemit child.value\n")},
		"child.mcl": &fstest.MapFile{Data: []byte("export let value = \"project file\"\n")},
	}

	bundle, err := EvaluateProjectFileContext(t.Context(), root, "main.mcl", files)
	if err != nil {
		t.Fatalf("evaluate project file: %v", err)
	}
	if len(bundle.Artifacts) != 1 || bundle.Artifacts[0].Value.Kind != String || bundle.Artifacts[0].Value.Text != "project file" {
		t.Fatalf("project file bundle = %#v, want one imported string artifact", bundle.Artifacts)
	}
}

func TestEvaluateProjectFileRejectsFileSymlinkOutsideRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	externalFile := filepath.Join(t.TempDir(), "external.mcl")
	if err := os.WriteFile(externalFile, []byte("emit \"outside\"\n"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	if err := os.Symlink(externalFile, filepath.Join(root, "main.mcl")); err != nil {
		t.Skipf("create file symlink: %v", err)
	}

	_, err := EvaluateProjectFileContext(t.Context(), root, "main.mcl", os.DirFS(root))
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("evaluate symlinked project file: got %v, want permission error", err)
	}
}

func TestEvaluateProjectFileRejectsDirectorySymlinkOutsideRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	externalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.mcl"), []byte("import \"./linked/child.mcl\" as child\nemit child.value\n"), 0o600); err != nil {
		t.Fatalf("write project entrypoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalDir, "child.mcl"), []byte("export let value = \"outside\"\n"), 0o600); err != nil {
		t.Fatalf("write external module: %v", err)
	}
	if err := os.Symlink(externalDir, filepath.Join(root, "linked")); err != nil {
		t.Skipf("create directory symlink: %v", err)
	}

	_, err := EvaluateProjectFileContext(t.Context(), root, "main.mcl", os.DirFS(root))
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("evaluate import through directory symlink: got %v, want permission error", err)
	}
}

func TestEvaluateProjectFileReadsDependencyLockFromFilesystem(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"main.mcl": &fstest.MapFile{Data: []byte("emit \"ok\"\n")},
		"mcl.lock": &fstest.MapFile{Data: []byte(`{"version":4}`)},
	}

	_, err := EvaluateProjectFileContext(t.Context(), filepath.Join(string(filepath.Separator), "virtual-project"), "main.mcl", files)
	if err == nil || !strings.Contains(err.Error(), "unsupported dependency lock version 4") {
		t.Fatalf("evaluate project file with virtual lock: got %v, want unsupported lock version", err)
	}
}

func TestLoadKubernetesResourceCatalogForProjectFS(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		projectConfigFileName: &fstest.MapFile{Data: []byte(`[kubernetes.resourceCatalog]
revision = "mcl-kubernetes-resource-catalog-v1"

[[kubernetes.resourceCatalog.additionalResources]]
apiGroup = "widgets.example.test"
kind = "Widget"
resourceName = "widgets"
scope = "Namespaced"
`)},
	}

	catalog, err := LoadKubernetesResourceCatalogForProjectFS(filepath.Join(string(filepath.Separator), "virtual-project"), files)
	if err != nil {
		t.Fatalf("load project catalog: %v", err)
	}
	if catalog.Revision != "mcl-kubernetes-resource-catalog-v1" || len(catalog.AdditionalResources) != 1 {
		t.Fatalf("loaded catalog = %#v, want configured revision and resource", catalog)
	}
}

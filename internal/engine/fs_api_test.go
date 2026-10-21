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

const projectFileEntrypoint = "main.mcl"

func TestEvaluateProjectSourceUsesRootRelativeFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		"child.mcl": &fstest.MapFile{Data: []byte("export type Child = String")},
	}

	bundle, loaded, err := EvaluateProjectSourceWithDependenciesContext(t.Context(), root, projectFileEntrypoint, `import "./child.mcl" as child`, files, nil)
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

	_, err := EvaluateProjectSourceContext(t.Context(), filepath.Join(string(filepath.Separator), "virtual-project"), projectFileEntrypoint, "", files, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported dependency lock version 4") {
		t.Fatalf("evaluate with virtual lock: got %v, want unsupported lock version", err)
	}
}

func TestEvaluateProjectFileUsesRootRelativeFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		projectFileEntrypoint: &fstest.MapFile{Data: []byte("import \"./child.mcl\" as child\nemit child.value\n")},
		"child.mcl":           &fstest.MapFile{Data: []byte("export let value = \"project file\"\n")},
	}

	bundle, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, files)
	if err != nil {
		t.Fatalf("evaluate project file: %v", err)
	}
	if len(bundle.Artifacts) != 1 || bundle.Artifacts[0].Value.Kind != String || bundle.Artifacts[0].Value.Text != "project file" {
		t.Fatalf("project file bundle = %#v, want one imported string artifact", bundle.Artifacts)
	}
}

func TestEvaluateProjectFileLoadsManifestFromRootRelativeFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		projectFileEntrypoint:     &fstest.MapFile{Data: []byte("let resources = manifest.load(\"./fixtures/resources.yaml\")\nemit resources\n")},
		"fixtures/resources.yaml": &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: settings\n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: api\n")},
	}

	bundle, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, files)
	if err != nil {
		t.Fatalf("evaluate project file with virtual Manifest: %v", err)
	}
	if len(bundle.Artifacts) != 2 {
		t.Fatalf("project file bundle has %d artifacts, want 2 Manifest documents", len(bundle.Artifacts))
	}
	for _, artifact := range bundle.Artifacts {
		if artifact.Lifecycle.Origin != "fixtures/resources.yaml" {
			t.Fatalf("Manifest origin = %q, want root-relative source path", artifact.Lifecycle.Origin)
		}
	}
}

func TestEvaluateProjectFileMapsKustomizeOriginForRootRelativeFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		projectFileEntrypoint:           &fstest.MapFile{Data: []byte("let resources = kustomize.build(\"fixtures\", \"\", \"base\")\nemit resources\n")},
		"fixtures/base/Kustomization":   &fstest.MapFile{Data: []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - deployment.yaml\n")},
		"fixtures/base/deployment.yaml": &fstest.MapFile{Data: []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: sample\n")},
	}

	bundle, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, files)
	if err != nil {
		t.Fatalf("evaluate project file with virtual Kustomize source: %v", err)
	}
	if len(bundle.Artifacts) != 1 {
		t.Fatalf("project file bundle has %d artifacts, want one Kustomize resource", len(bundle.Artifacts))
	}
	if got, want := bundle.Artifacts[0].Lifecycle.Origin, "kustomize:fixtures/base/deployment.yaml"; got != want {
		t.Fatalf("Kustomize origin = %q, want %q", got, want)
	}
}

func TestEvaluateProjectFileRejectsManifestSymlinkAndPathEscape(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	for _, test := range []struct {
		name   string
		files  fstest.MapFS
		wanted error
	}{
		{
			name: "symlink",
			files: fstest.MapFS{
				projectFileEntrypoint: &fstest.MapFile{Data: []byte("emit manifest.load(\"linked.yaml\")\n")},
				"linked.yaml":         &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("outside.yaml")},
				"outside.yaml":        &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: ConfigMap\n")},
			},
			wanted: fs.ErrPermission,
		},
		{
			name: "path escape",
			files: fstest.MapFS{
				projectFileEntrypoint: &fstest.MapFile{Data: []byte("emit manifest.load(\"../../outside.yaml\")\n")},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, test.files)
			if err == nil {
				t.Fatal("evaluate project file: got no error, want Manifest source rejection")
			}
			if test.wanted != nil && !errors.Is(err, test.wanted) {
				t.Fatalf("evaluate project file: got %v, want %v", err, test.wanted)
			}
		})
	}
}

func TestEvaluateProjectFileRejectsFileSymlinkOutsideRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	externalFile := filepath.Join(t.TempDir(), "external.mcl")
	if err := os.WriteFile(externalFile, []byte("emit \"outside\"\n"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	if err := os.Symlink(externalFile, filepath.Join(root, projectFileEntrypoint)); err != nil {
		t.Skipf("create file symlink: %v", err)
	}

	_, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, os.DirFS(root))
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("evaluate symlinked project file: got %v, want permission error", err)
	}
}

func TestEvaluateProjectFileRejectsDirectorySymlinkOutsideRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	externalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, projectFileEntrypoint), []byte("import \"./linked/child.mcl\" as child\nemit child.value\n"), 0o600); err != nil {
		t.Fatalf("write project entrypoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalDir, "child.mcl"), []byte("export let value = \"outside\"\n"), 0o600); err != nil {
		t.Fatalf("write external module: %v", err)
	}
	if err := os.Symlink(externalDir, filepath.Join(root, "linked")); err != nil {
		t.Skipf("create directory symlink: %v", err)
	}

	_, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, os.DirFS(root))
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("evaluate import through directory symlink: got %v, want permission error", err)
	}
}

func TestEvaluateProjectFileReadsDependencyLockFromFilesystem(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		projectFileEntrypoint: &fstest.MapFile{Data: []byte("emit \"ok\"\n")},
		"mcl.lock":            &fstest.MapFile{Data: []byte(`{"version":4}`)},
	}

	_, err := EvaluateProjectFileContext(t.Context(), filepath.Join(string(filepath.Separator), "virtual-project"), projectFileEntrypoint, files)
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

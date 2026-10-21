package engine

import (
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

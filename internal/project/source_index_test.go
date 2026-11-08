package project

import (
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestDependentSourceIndexExcludesIgnoredDirectories(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "project")
	files := fstest.MapFS{
		"target.mcl":              &fstest.MapFile{Data: []byte("export let value = 1\n")},
		"nested/importer.mcl":     &fstest.MapFile{Data: []byte("import \"../target.mcl\" as target\nemit target.value\n")},
		".mcl/cache/cached.mcl":   &fstest.MapFile{Data: []byte("ignored\n")},
		"node_modules/module.mcl": &fstest.MapFile{Data: []byte("ignored\n")},
	}
	index := &DependentMCLSourceIndex{}
	sources, err := index.SourcesForProjectFS(root, "target.mcl", files, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		filepath.Join(root, "target.mcl"):          "export let value = 1\n",
		filepath.Join(root, "nested/importer.mcl"): "import \"../target.mcl\" as target\nemit target.value\n",
	} {
		if got := sources[name]; got != want {
			t.Errorf("source %q = %q, want %q", name, got, want)
		}
	}
	for _, ignored := range []string{filepath.Join(root, ".mcl", "cache", "cached.mcl"), filepath.Join(root, "node_modules", "module.mcl")} {
		if _, exists := sources[ignored]; exists {
			t.Errorf("source index included ignored module %q", ignored)
		}
	}
}

func TestDependentSourceIndexReusesSnapshotUntilWorkspaceChanges(t *testing.T) {
	t.Parallel()
	const root = "/virtual-project"
	files := fstest.MapFS{
		"target.mcl":   &fstest.MapFile{Data: []byte("export let value = 1\n")},
		"importer.mcl": &fstest.MapFile{Data: []byte("import \"./target.mcl\" as target\nemit target.value\n")},
	}
	index := &DependentMCLSourceIndex{}
	if _, err := index.SourcesForProjectFS(root, "target.mcl", files, nil); err != nil {
		t.Fatal(err)
	}
	first := index.WorkspaceSnapshot()
	if first == nil {
		t.Fatal("workspace snapshot is nil after initialization")
	}
	if _, err := index.SourcesForProjectFS(root, "target.mcl", files, nil); err != nil {
		t.Fatal(err)
	}
	if got := index.WorkspaceSnapshot(); got != first {
		t.Fatalf("unchanged snapshot = %p, want reused snapshot %p", got, first)
	}

	files["importer.mcl"].Data = []byte("let local = 1\n")
	if _, err := index.SourcesForProjectFS(root, "target.mcl", files, nil); err != nil {
		t.Fatal(err)
	}
	updated := index.WorkspaceSnapshot()
	if updated == first || updated.Generation() != first.Generation()+1 {
		t.Fatalf("changed snapshot = %p generation %d, want new snapshot at generation %d", updated, updated.Generation(), first.Generation()+1)
	}
}

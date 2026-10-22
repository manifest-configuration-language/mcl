package engine

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestCollectDependentSourcesWithFilesystem(t *testing.T) {
	t.Parallel()
	const targetFilename = "target.mcl"
	root := filepath.Join(t.TempDir(), "project")
	files := newProjectFilesystem(root, fstest.MapFS{
		projectConfigFileName:     &fstest.MapFile{Data: []byte("")},
		targetFilename:            &fstest.MapFile{Data: []byte("export let value = 1\n")},
		"nested/importer.mcl":     &fstest.MapFile{Data: []byte("import \"../target.mcl\" as target\nemit target.value\n")},
		".mcl/cache/cached.mcl":   &fstest.MapFile{Data: []byte("ignored\n")},
		"node_modules/module.mcl": &fstest.MapFile{Data: []byte("ignored\n")},
	})
	canonical := filepath.Join(root, targetFilename)
	sources, err := collectDependentSourcesWithFilesystem(files, canonical, root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sources[canonical], "export let value = 1\n"; got != want {
		t.Fatalf("target source = %q, want %q", got, want)
	}
	importer := filepath.Join(root, "nested", "importer.mcl")
	if got, want := sources[importer], "import \"../target.mcl\" as target\nemit target.value\n"; got != want {
		t.Fatalf("importer source = %q, want %q", got, want)
	}
	for _, ignored := range []string{filepath.Join(root, ".mcl", "cache", "cached.mcl"), filepath.Join(root, "node_modules", "module.mcl")} {
		if _, exists := sources[ignored]; exists {
			t.Errorf("source index included ignored module %q", ignored)
		}
	}
}

func TestDependentMCLSourcesForProjectFS(t *testing.T) {
	t.Parallel()
	const targetFilename = "target.mcl"
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		targetFilename:             &fstest.MapFile{Data: []byte("export let value = 1\n")},
		"modules/middle.mcl":       &fstest.MapFile{Data: []byte("import \"../target.mcl\" as target\nexport let value = target.value\n")},
		"modules/main.mcl":         &fstest.MapFile{Data: []byte("import \"./middle.mcl\" as middle\nemit middle.value\n")},
		"modules/unrelated.mcl":    &fstest.MapFile{Data: []byte("emit 1\n")},
		".mcl/cache/cached.mcl":    &fstest.MapFile{Data: []byte("import \"../../target.mcl\" as target\n")},
		"node_modules/ignored.mcl": &fstest.MapFile{Data: []byte("import \"../target.mcl\" as target\n")},
	}

	sources, err := DependentMCLSourcesForProjectFS(root, targetFilename, files)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{targetFilename, "modules/middle.mcl", "modules/main.mcl"} {
		if _, exists := sources[filepath.Join(root, filepath.FromSlash(relative))]; !exists {
			t.Errorf("dependent source result omitted %q", relative)
		}
	}
	for _, relative := range []string{"modules/unrelated.mcl", ".mcl/cache/cached.mcl", "node_modules/ignored.mcl"} {
		if _, exists := sources[filepath.Join(root, filepath.FromSlash(relative))]; exists {
			t.Errorf("dependent source result included %q", relative)
		}
	}
}

func TestDependentMCLSourceIndexSupportsProjectFSUpdatesAndOverlays(t *testing.T) {
	t.Parallel()
	const root = "/virtual-project"
	const targetFilename = "target.mcl"
	files := fstest.MapFS{
		targetFilename: &fstest.MapFile{Data: []byte("export let value = 1\n")},
		"importer.mcl": &fstest.MapFile{Data: []byte("let local = 1\n")},
	}
	importer := filepath.Join(root, "importer.mcl")
	index := &DependentMCLSourceIndex{}
	if _, err := index.SourcesForProjectFS(root, targetFilename, files, nil); err != nil {
		t.Fatal(err)
	}

	importSource := "import \"./target.mcl\" as target\nemit target.value\n"
	files["importer.mcl"].Data = []byte(importSource)
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: importer}}); err != nil {
		t.Fatal(err)
	}
	dependent, err := index.SourcesFromProjectFSIndex(root, targetFilename, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := dependent[importer]; got != importSource {
		t.Fatalf("incremental filesystem source = %q, want %q", got, importSource)
	}

	overlay := "let local = 2\n"
	dependent, err = index.SourcesFromProjectFSIndex(root, targetFilename, files, map[string]string{importer: overlay})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[importer]; exists {
		t.Fatal("overlay removed the importer edge, but importer remained in dependent sources")
	}

	updatedImportSource := "import \"./target.mcl\" as target\nlet value = target.value\nemit value\n"
	files["importer.mcl"].Data = []byte(updatedImportSource)
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: importer}}); err != nil {
		t.Fatal(err)
	}
	dependent, err = index.SourcesFromProjectFSIndex(root, targetFilename, files, map[string]string{importer: overlay})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[importer]; exists {
		t.Fatal("filesystem update replaced an open document overlay")
	}
	dependent, err = index.SourcesFromProjectFSIndex(root, targetFilename, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := dependent[importer]; got != updatedImportSource {
		t.Fatalf("closing overlay restored %q, want %q", got, updatedImportSource)
	}

	delete(files, "importer.mcl")
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: importer, Deleted: true}}); err != nil {
		t.Fatal(err)
	}
	dependent, err = index.SourcesFromProjectFSIndex(root, targetFilename, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[importer]; exists {
		t.Fatal("deleted importer remained in dependent sources")
	}
}

func TestDependentMCLSourceIndexAppliesWatchedChangesAndOverlays(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for path, source := range map[string]string{
		projectConfigFileName: "",
		"target.mcl":          "export let value = \"old\"\n",
		"importer.mcl":        "let local = \"old\"\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "target.mcl")
	importer := filepath.Join(root, "importer.mcl")
	importer, err := filepath.EvalSymlinks(importer)
	if err != nil {
		t.Fatal(err)
	}
	index := &DependentMCLSourceIndex{}
	if _, err := index.Sources(target, []string{root}, nil); err != nil {
		t.Fatal(err)
	}

	importSource := "import \"./target.mcl\" as target\nemit target.value\n"
	if err := os.WriteFile(importer, []byte(importSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: importer}}); err != nil {
		t.Fatal(err)
	}
	dependent, err := index.SourcesFromIndex(target, []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := dependent[importer]; got != importSource {
		t.Fatalf("incremental index source = %q, want %q", got, importSource)
	}
	if got, want := index.hashes[importer], sha256.Sum256([]byte(importSource)); got != want {
		t.Fatalf("source hash = %x, want hash of current source %x", got, want)
	}

	overlay := "let local = \"overlay\"\n"
	dependent, err = index.SourcesFromIndex(target, []string{root}, map[string]string{importer: overlay})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[importer]; exists {
		t.Fatal("overlay removed the importer edge, but importer remained in dependent sources")
	}
	diskSource := "import \"./target.mcl\" as target\nlet changed = \"disk\"\nemit target.value\n"
	if err := os.WriteFile(importer, []byte(diskSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: importer}}); err != nil {
		t.Fatal(err)
	}
	dependent, err = index.SourcesFromIndex(target, []string{root}, map[string]string{importer: overlay})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[importer]; exists {
		t.Fatal("disk watcher change replaced an open document overlay")
	}
	dependent, err = index.SourcesFromIndex(target, []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := dependent[importer]; got != diskSource {
		t.Fatalf("closed overlay restored %q, want disk source %q", got, diskSource)
	}
	if got, want := index.hashes[importer], sha256.Sum256([]byte(diskSource)); got != want {
		t.Fatalf("closed overlay source hash = %x, want hash of disk source %x", got, want)
	}
}

func TestDependentMCLSourceIndexInvalidatesOnDeletedImport(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for path, source := range map[string]string{
		projectConfigFileName: "",
		"target.mcl":          "export let value = \"old\"\n",
		"importer.mcl":        "import \"./target.mcl\" as target\nemit target.value\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "target.mcl")
	index := &DependentMCLSourceIndex{}
	if _, err := index.Sources(target, []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: target, Deleted: true}}); err == nil {
		t.Fatal("deleting an imported module left a valid reverse index")
	}
	if index.initialized {
		t.Fatal("failed watcher update kept the index marked complete")
	}
}

func TestDependentMCLSourceIndexIgnoresWatchedFilesInCache(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for path, source := range map[string]string{
		projectConfigFileName: "",
		"target.mcl":          "export let value = \"old\"\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cache := filepath.Join(root, ".mcl", "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target.mcl")
	cachedImporter := filepath.Join(cache, "importer.mcl")
	index := &DependentMCLSourceIndex{}
	if _, err := index.Sources(target, []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachedImporter, []byte("import \"../../target.mcl\" as target\nemit target.value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := index.ApplyFileChanges([]DependentMCLFileChange{{Path: cachedImporter}}); err != nil {
		t.Fatal(err)
	}
	dependent, err := index.SourcesFromIndex(target, []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := dependent[cachedImporter]; exists {
		t.Fatal("incremental index included a module from the ignored cache directory")
	}
}

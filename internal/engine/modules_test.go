package engine

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

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

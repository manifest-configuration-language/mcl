package engine

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/manifest-configuration-language/mcl/internal/safepath"
)

func TestProjectFilesystemReadsRootRelativeFilesAndRejectsTraversal(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	filesystem := newProjectFilesystem(root, fstest.MapFS{
		"modules/child.mcl": &fstest.MapFile{Data: []byte("export type Child = String")},
	})

	data, err := filesystem.readLimitedFile(filepath.Join(root, "modules", "child.mcl"))
	if err != nil || string(data) != "export type Child = String" {
		t.Fatalf("read virtual module: got %q, %v", data, err)
	}
	if _, err := filesystem.readLimitedFile(filepath.Join(root, "..", "outside.mcl")); err == nil {
		t.Fatal("expected a path outside the project root to be rejected")
	}

	graph := moduleGraph{root: root, files: filesystem}
	resolved, err := graph.resolveImport(filepath.Join(root, "main.mcl"), "./modules/child.mcl")
	if err != nil || resolved != filepath.Join(root, "modules", "child.mcl") {
		t.Fatalf("resolve virtual import: got %q, %v", resolved, err)
	}
	if _, err := graph.resolveImport(filepath.Join(root, "main.mcl"), "../../outside.mcl"); err == nil {
		t.Fatal("expected an import outside the project root to be rejected")
	}
}

func TestEvaluateSourceReadsImportedModulesFromInjectedFilesystem(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	entry := filepath.Join(root, "main.mcl")
	dependency := filepath.Join(root, "child.mcl")
	filesystem := newProjectFilesystem(root, fstest.MapFS{
		"child.mcl": &fstest.MapFile{Data: []byte("export type Child = String")},
	})

	_, loaded, err := evaluateSourceWithFilesystem(t.Context(), entry, `import "./child.mcl" as child`, nil, filesystem)
	if err != nil {
		t.Fatalf("evaluate source with injected filesystem: %v", err)
	}
	if !slices.Contains(loaded, dependency) {
		t.Fatalf("loaded modules do not include injected dependency %q: %v", dependency, loaded)
	}
}

func TestProjectFilesystemKeepsOSSymlinkContainment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.mcl")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside.mcl")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := newOSProjectFilesystem(root).readLimitedFile(link)
	if !errors.Is(err, safepath.ErrOutsideRoot) {
		t.Fatalf("expected symlink containment error, got %v", err)
	}
}

func TestCompileSchemaLoadsReferencesFromInjectedFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	filesystem := newProjectFilesystem(root, fstest.MapFS{
		"schema.json":      &fstest.MapFile{Data: []byte(`{"$ref":"definitions.json#/$defs/Name"}`)},
		"definitions.json": &fstest.MapFile{Data: []byte(`{"$defs":{"Name":{"type":"string"}}}`)},
	})

	if _, err := compileSchema("schema.json", root, filepath.Join(root, "main.mcl"), nil, filesystem); err != nil {
		t.Fatalf("compile schema through injected filesystem: %v", err)
	}
}

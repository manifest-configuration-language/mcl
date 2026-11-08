package languageservice

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/manifest-configuration-language/mcl/internal/project"
)

func TestProjectSemanticSnapshotReusesWorkspaceASTAndIndex(t *testing.T) {
	workspace, err := project.NewWorkspaceSnapshot(12, ".", nil, map[string]string{
		"main.mcl": "import \"./lib.mcl\" as lib\nemit lib.value\n",
		"lib.mcl":  "let value = 42\n",
	})
	if err != nil {
		t.Fatalf("NewWorkspaceSnapshot() error = %v", err)
	}
	snapshot := NewProjectSemanticSnapshot(workspace)
	if got := snapshot.Generation(); got != 12 {
		t.Fatalf("Generation() = %d, want 12", got)
	}
	if got, want := snapshot.Paths(), []string{"lib.mcl", "main.mcl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Paths() = %v, want %v", got, want)
	}

	workspaceModule, exists := workspace.Module("lib.mcl")
	if !exists {
		t.Fatal("WorkspaceSnapshot.Module() did not find lib.mcl")
	}
	module, exists := snapshot.Module("./lib.mcl")
	if !exists {
		t.Fatal("Module() did not find ./lib.mcl")
	}
	if len(module.Nodes) == 0 || &module.Nodes[0] != &workspaceModule.Nodes[0] {
		t.Fatal("Module() did not return the workspace AST")
	}
	firstIndex, exists := snapshot.SemanticIndex("lib.mcl")
	if !exists {
		t.Fatal("SemanticIndex() did not find lib.mcl")
	}
	secondIndex, exists := snapshot.SemanticIndex("./lib.mcl")
	if !exists || firstIndex != secondIndex {
		t.Fatal("SemanticIndex() did not return the cached index")
	}
	if len(firstIndex.Module.Nodes) == 0 || &firstIndex.Module.Nodes[0] != &workspaceModule.Nodes[0] {
		t.Fatal("SemanticIndex() does not reference the workspace AST")
	}

	if got, exists := snapshot.Source("./lib.mcl"); !exists || got != "let value = 42\n" {
		t.Fatalf("Source() = (%q, %v), want lib.mcl source", got, exists)
	}
}

func TestProjectSemanticSnapshotResolvesImportsForAbsoluteImporters(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "app", "main.mcl")
	libPath := filepath.Join(root, "shared", "lib.mcl")
	snapshot, err := NewProjectSemanticSnapshotFromSources(map[string]string{
		mainPath: "import \"../shared/lib.mcl\" as lib\n",
		libPath:  "let value = 42\n",
	})
	if err != nil {
		t.Fatalf("NewProjectSemanticSnapshotFromSources() error = %v", err)
	}

	got, ok := snapshot.ResolveImport(mainPath, "../shared/lib.mcl")
	if !ok || got != libPath {
		t.Fatalf("ResolveImport() = (%q, %v), want (%q, true)", got, ok, libPath)
	}
	for _, modulePath := range []string{"/absolute/import.mcl", `..\shared\lib.mcl`} {
		if got, ok := snapshot.ResolveImport(mainPath, modulePath); ok {
			t.Errorf("ResolveImport(%q) = (%q, true), want unresolved", modulePath, got)
		}
	}
}

func TestProjectSemanticSnapshotResolvesOnlySnapshotImports(t *testing.T) {
	workspace, err := project.NewWorkspaceSnapshot(1, ".", nil, map[string]string{
		"app/main.mcl":   "import \"../shared/lib.mcl\" as lib\n",
		"shared/lib.mcl": "let value = 42\n",
	})
	if err != nil {
		t.Fatalf("NewWorkspaceSnapshot() error = %v", err)
	}
	snapshot := NewProjectSemanticSnapshot(workspace)

	if got, ok := snapshot.ResolveImport("app/main.mcl", "../shared/lib.mcl"); !ok || got != "shared/lib.mcl" {
		t.Fatalf("ResolveImport() = (%q, %v), want shared/lib.mcl", got, ok)
	}
	for _, modulePath := range []string{"./missing.mcl", "../../outside.mcl", "/absolute.mcl", `..\shared\lib.mcl`} {
		if got, ok := snapshot.ResolveImport("app/main.mcl", modulePath); ok {
			t.Errorf("ResolveImport(%q) = (%q, true), want unresolved", modulePath, got)
		}
	}
}

func TestProjectSemanticSnapshotSourcesAreIsolatedAndNilSafe(t *testing.T) {
	workspace, err := project.NewWorkspaceSnapshot(3, ".", nil, map[string]string{"main.mcl": "let value = 42\n"})
	if err != nil {
		t.Fatalf("NewWorkspaceSnapshot() error = %v", err)
	}
	snapshot := NewProjectSemanticSnapshot(workspace)
	sources := snapshot.Sources()
	sources["main.mcl"] = "changed"
	delete(sources, "other.mcl")
	if got, exists := snapshot.Source("main.mcl"); !exists || got != "let value = 42\n" {
		t.Fatalf("Source() after mutating Sources() = (%q, %v), want original source", got, exists)
	}

	var nilSnapshot *ProjectSemanticSnapshot
	if nilSnapshot.Generation() != 0 || len(nilSnapshot.Paths()) != 0 || len(nilSnapshot.Sources()) != 0 {
		t.Fatal("nil snapshot accessors should return zero values")
	}
	if _, exists := nilSnapshot.Source("main.mcl"); exists {
		t.Fatal("nil snapshot Source() found a path")
	}
	if _, exists := nilSnapshot.Module("main.mcl"); exists {
		t.Fatal("nil snapshot Module() found a path")
	}
	if _, exists := nilSnapshot.SemanticIndex("main.mcl"); exists {
		t.Fatal("nil snapshot SemanticIndex() found a path")
	}
	if _, exists := nilSnapshot.ResolveImport("main.mcl", "./lib.mcl"); exists {
		t.Fatal("nil snapshot ResolveImport() found a target")
	}
}

func TestProjectSemanticSnapshotFromSourcesParsesModulesOnDemand(t *testing.T) {
	snapshot, err := NewProjectSemanticSnapshotFromSources(map[string]string{
		"broken.mcl": "let =\n",
		"valid.mcl":  "let value = 42\n",
	})
	if err != nil {
		t.Fatalf("NewProjectSemanticSnapshotFromSources() error = %v", err)
	}
	if got, exists := snapshot.Source("broken.mcl"); !exists || got != "let =\n" {
		t.Fatalf("Source(broken.mcl) = (%q, %v), want source without parsing", got, exists)
	}
	if _, exists := snapshot.SemanticIndex("broken.mcl"); exists {
		t.Fatal("SemanticIndex(broken.mcl) succeeded for invalid source")
	}
	firstIndex, exists := snapshot.SemanticIndex("valid.mcl")
	if !exists {
		t.Fatal("SemanticIndex(valid.mcl) failed")
	}
	secondIndex, exists := snapshot.SemanticIndex("./valid.mcl")
	if !exists || firstIndex != secondIndex {
		t.Fatal("SemanticIndex(valid.mcl) did not reuse its cached index")
	}
}

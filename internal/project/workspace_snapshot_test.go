package project

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

func TestWorkspaceSnapshotCopiesInputsAndAccessorSlices(t *testing.T) {
	searchRoots := []string{"./modules", "shared/../shared"}
	sources := map[string]string{
		"./b.mcl": "let b = 2\n",
		"a.mcl":   "let a = 1\n",
	}
	snapshot, err := NewWorkspaceSnapshot(7, "./workspace", searchRoots, sources)
	if err != nil {
		t.Fatalf("NewWorkspaceSnapshot() error = %v", err)
	}

	searchRoots[0] = "changed"
	sources["a.mcl"] = "let a = 9\n"
	delete(sources, "b.mcl")

	gotRoots := snapshot.SearchRoots()
	gotRoots[0] = "also changed"
	gotPaths := snapshot.Paths()
	gotPaths[0] = "also changed"
	gotImports, ok := snapshot.Imports("a.mcl")
	if !ok {
		t.Fatal("Imports() did not find a.mcl")
	}
	if len(gotImports) != 0 {
		t.Fatalf("Imports() = %v, want an empty import list", gotImports)
	}

	if snapshot.Generation() != 7 {
		t.Errorf("Generation() = %d, want 7", snapshot.Generation())
	}
	if snapshot.Root() != "workspace" {
		t.Errorf("Root() = %q, want workspace", snapshot.Root())
	}
	if got := snapshot.SearchRoots(); !reflect.DeepEqual(got, []string{"modules", "shared"}) {
		t.Errorf("SearchRoots() = %v", got)
	}
	if got := snapshot.Paths(); !reflect.DeepEqual(got, []string{"a.mcl", "b.mcl"}) {
		t.Errorf("Paths() = %v", got)
	}
	content, digest, ok := snapshot.Source("./a.mcl")
	if !ok || content != "let a = 1\n" || digest != sha256.Sum256([]byte(content)) {
		t.Errorf("Source() = (%q, %x, %v)", content, digest, ok)
	}
	imports, ok := snapshot.Imports("a.mcl")
	if !ok || len(imports) != 0 {
		t.Errorf("Imports() = (%v, %v), want an empty import list", imports, ok)
	}
}

func TestWorkspaceSnapshotPathsAreSortedAndImportsArePreserved(t *testing.T) {
	content := "import \"./names.mcl\" as names\n\nlet value = names.value\n"
	snapshot, err := NewWorkspaceSnapshot(1, ".", nil, map[string]string{
		"z.mcl":        "let z = 0\n",
		"dir/../a.mcl": content,
	})
	if err != nil {
		t.Fatalf("NewWorkspaceSnapshot() error = %v", err)
	}
	if got, want := snapshot.Paths(), []string{"a.mcl", "z.mcl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Paths() = %v, want %v", got, want)
	}

	ast, ok := snapshot.Module("a.mcl")
	if !ok || len(ast.Nodes) != 2 {
		t.Fatalf("Module() = (%#v, %v), want two AST nodes", ast, ok)
	}
	imports, ok := snapshot.Imports("a.mcl")
	if !ok || len(imports) != 1 {
		t.Fatalf("Imports() = (%v, %v), want one import", imports, ok)
	}
	if imports[0].Name != "names" || imports[0].ModulePath != "./names.mcl" || imports[0].Text {
		t.Errorf("import declaration = %+v", imports[0])
	}
	imports[0].ModulePath = "changed"
	again, _ := snapshot.Imports("a.mcl")
	if again[0].ModulePath != "./names.mcl" {
		t.Errorf("mutating returned import declarations changed snapshot data: %+v", again[0])
	}
	if _, ok := ast.Nodes[0].(*syntax.ImportDeclaration); !ok {
		t.Errorf("first AST node has type %T, want *syntax.ImportDeclaration", ast.Nodes[0])
	}
}

func TestWorkspaceSnapshotReusesModulesWithMatchingPathAndDigest(t *testing.T) {
	previous, err := NewWorkspaceSnapshot(3, ".", nil, map[string]string{
		"unchanged.mcl": "let value = 1\n",
		"changed.mcl":   "let value = 1\n",
	})
	if err != nil {
		t.Fatalf("NewWorkspaceSnapshot() error = %v", err)
	}

	next, err := NewWorkspaceSnapshotWithPrevious(4, ".", nil, map[string]string{
		"./unchanged.mcl": "let value = 1\n",
		"changed.mcl":     "let value = 2\n",
	}, previous)
	if err != nil {
		t.Fatalf("NewWorkspaceSnapshotWithPrevious() error = %v", err)
	}
	if next.Generation() != 4 {
		t.Errorf("Generation() = %d, want caller supplied generation 4", next.Generation())
	}

	oldUnchanged, _ := previous.Module("unchanged.mcl")
	newUnchanged, _ := next.Module("unchanged.mcl")
	if oldUnchanged.Nodes[0] != newUnchanged.Nodes[0] {
		t.Error("unchanged module AST node was not reused")
	}
	oldChanged, _ := previous.Module("changed.mcl")
	newChanged, _ := next.Module("changed.mcl")
	if oldChanged.Nodes[0] == newChanged.Nodes[0] {
		t.Error("changed module AST node was reused")
	}
	content, _, ok := next.Source("changed.mcl")
	if !ok || content != "let value = 2\n" {
		t.Errorf("Source(changed.mcl) = %q, %v", content, ok)
	}
}

func TestWorkspaceSnapshotFromParsedCopiesMapsAndPathLists(t *testing.T) {
	content := "import \"./dependency.mcl\" as dependency\n"
	ast, err := syntax.ParseAST(content)
	if err != nil {
		t.Fatalf("ParseAST() error = %v", err)
	}
	sources := map[string]string{"./main.mcl": content}
	modules := map[string]syntax.ModuleAST{"main.mcl": ast}
	dependencies := map[string][]string{"main.mcl": {"./dependency.mcl"}}
	importers := map[string][]string{"dependency.mcl": {"main.mcl"}}
	snapshot, err := NewWorkspaceSnapshotFromParsed(9, ".", []string{"modules"}, sources, modules, dependencies, importers)
	if err != nil {
		t.Fatalf("NewWorkspaceSnapshotFromParsed() error = %v", err)
	}

	sources["main.mcl"] = "changed"
	delete(modules, "main.mcl")
	dependencies["main.mcl"][0] = "changed"
	importers["dependency.mcl"][0] = "changed"
	gotSources := snapshot.Sources()
	gotSources["main.mcl"] = "changed"
	gotModules := snapshot.Modules()
	delete(gotModules, "main.mcl")
	gotDependencies := snapshot.Dependencies("./main.mcl")
	gotDependencies[0] = "changed"
	gotImporters := snapshot.Importers("./dependency.mcl")
	gotImporters[0] = "changed"

	if got := snapshot.Sources()["main.mcl"]; got != content {
		t.Errorf("Sources()[main.mcl] = %q, want original content", got)
	}
	if _, ok := snapshot.Modules()["main.mcl"]; !ok {
		t.Error("Modules() no longer contains main.mcl")
	}
	if got := snapshot.Dependencies("main.mcl"); !reflect.DeepEqual(got, []string{"./dependency.mcl"}) {
		t.Errorf("Dependencies() = %v", got)
	}
	if got := snapshot.Importers("dependency.mcl"); !reflect.DeepEqual(got, []string{"main.mcl"}) {
		t.Errorf("Importers() = %v", got)
	}
}

func TestWorkspaceSnapshotFromParsedRequiresMatchingPaths(t *testing.T) {
	_, err := NewWorkspaceSnapshotFromParsed(1, ".", nil,
		map[string]string{"source.mcl": "let value = 1\n"},
		map[string]syntax.ModuleAST{"other.mcl": {}}, nil, nil,
	)
	if err == nil || !strings.Contains(err.Error(), `"source.mcl"`) {
		t.Fatalf("NewWorkspaceSnapshotFromParsed() error = %v, want path mismatch error", err)
	}
}

func TestWorkspaceSnapshotDependentSourcesIncludesTransitiveImporters(t *testing.T) {
	sources := map[string]string{
		"target.mcl":    "let target = 1\n",
		"middle.mcl":    "let middle = 2\n",
		"root.mcl":      "let root = 3\n",
		"unrelated.mcl": "let unrelated = 4\n",
	}
	modules := make(map[string]syntax.ModuleAST, len(sources))
	for path := range sources {
		modules[path] = syntax.ModuleAST{}
	}
	snapshot, err := NewWorkspaceSnapshotFromParsed(1, ".", nil, sources, modules, nil, map[string][]string{
		"target.mcl": {"middle.mcl", "missing.mcl"},
		"middle.mcl": {"root.mcl"},
		"root.mcl":   {"target.mcl"},
	})
	if err != nil {
		t.Fatalf("NewWorkspaceSnapshotFromParsed() error = %v", err)
	}

	got, err := snapshot.DependentSources(context.Background(), "./target.mcl")
	if err != nil {
		t.Fatalf("DependentSources() error = %v", err)
	}
	want := map[string]string{
		"target.mcl": "let target = 1\n",
		"middle.mcl": "let middle = 2\n",
		"root.mcl":   "let root = 3\n",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DependentSources() = %v, want %v", got, want)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snapshot.DependentSources(canceled, "target.mcl"); !errors.Is(err, context.Canceled) {
		t.Errorf("DependentSources() error = %v, want context.Canceled", err)
	}
}

func TestWorkspaceSnapshotParseErrorIncludesPath(t *testing.T) {
	_, err := NewWorkspaceSnapshot(1, ".", nil, map[string]string{"broken.mcl": "let =\n"})
	if err == nil || !strings.Contains(err.Error(), `"broken.mcl"`) {
		t.Fatalf("NewWorkspaceSnapshot() error = %v, want path-specific parse error", err)
	}
}

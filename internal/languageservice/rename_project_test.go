package languageservice

import (
	"testing"
	"testing/fstest"
)

const (
	mainMCLPath  = "main.mcl"
	defsMCLPath  = "defs.mcl"
	otherMCLPath = "other.mcl"
)

func TestRenameForProjectFSUpdatesExportedValueReferences(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		mainMCLPath:  {Data: []byte("import \"./defs.mcl\" as defs\nlet value = defs.value\nemit value\n")},
		defsMCLPath:  {Data: []byte("export type Config = { name: String }\nexport let value = \"ok\"\n")},
		otherMCLPath: {Data: []byte("import \"./defs.mcl\" as d\nemit d.value\n")},
	}
	params := NavigationParams{Entrypoint: defsMCLPath, Source: "export type Config = { name: String }\nexport let value = \"ok\"\n", Line: 1, Character: 13}
	result := RenameForProjectFS(params, files, "item")
	if result == nil {
		t.Fatal("RenameForProjectFS() returned nil for an exported declaration")
	}
	if len(result.Edits) != 3 {
		t.Fatalf("RenameForProjectFS() returned %d edits, want declaration and two qualified value references: %#v", len(result.Edits), result.Edits)
	}
	if result.Edits[0].Path != defsMCLPath || result.Edits[0].Range.Start.Line != 1 || result.Edits[0].NewText != "item" {
		t.Errorf("first edit = %#v, want exported declaration in defs.mcl", result.Edits[0])
	}
	if result.Edits[1].Path != mainMCLPath || result.Edits[1].Range.Start.Line != 1 {
		t.Errorf("second edit = %#v, want qualified value reference in main.mcl", result.Edits[1])
	}
	if result.Edits[2].Path != otherMCLPath || result.Edits[2].Range.Start.Line != 1 {
		t.Errorf("third edit = %#v, want qualified value reference in other.mcl", result.Edits[2])
	}
}

func TestRenameForProjectFSUpdatesQualifiedTypeReferences(t *testing.T) {
	t.Parallel()
	const targetSource = "export type Config = { name: String }\n"
	files := fstest.MapFS{
		mainMCLPath: {Data: []byte("import \"./defs.mcl\" as defs\nlet config: defs.Config = {}\nemit config\n")},
		defsMCLPath: {Data: []byte(targetSource)},
	}
	params := NavigationParams{Entrypoint: defsMCLPath, Source: targetSource, Line: 0, Character: 13}
	result := RenameForProjectFS(params, files, "Settings")
	if result == nil || len(result.Edits) != 2 {
		t.Fatalf("RenameForProjectFS() = %#v, want declaration and qualified type reference", result)
	}
	if result.Edits[1].Path != mainMCLPath || result.Edits[1].Range.Start.Line != 1 {
		t.Errorf("qualified type edit = %#v, want main.mcl line 2", result.Edits[1])
	}
}

func TestRenameForProjectFSResolvesExportFromQualifiedReference(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		mainMCLPath:  {Data: []byte("import \"./defs.mcl\" as defs\nemit defs.value\n")},
		defsMCLPath:  {Data: []byte("export let value = 1\n")},
		otherMCLPath: {Data: []byte("import \"./defs.mcl\" as d\nemit d.value\n")},
	}
	params := NavigationParams{Entrypoint: mainMCLPath, Source: "import \"./defs.mcl\" as defs\nemit defs.value\n", Line: 1, Character: 11}
	result := RenameForProjectFS(params, files, "item")
	if result == nil || len(result.Edits) != 3 {
		t.Fatalf("RenameForProjectFS() = %#v, want declaration and both importer references", result)
	}
}

func TestRenameForProjectFSRejectsExportCollision(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		defsMCLPath: {Data: []byte("export let value = 1\nexport let item = 2\n")},
	}
	params := NavigationParams{Entrypoint: defsMCLPath, Source: "export let value = 1\nexport let item = 2\n", Line: 0, Character: 13}
	if result := RenameForProjectFS(params, files, "item"); result != nil {
		t.Fatalf("RenameForProjectFS() = %#v, want collision rejected", result)
	}
}

func TestRenameForProjectFSKeepsLocalRenameBehavior(t *testing.T) {
	t.Parallel()
	const source = "let value = 1\nemit value\n"
	files := fstest.MapFS{mainMCLPath: {Data: []byte(source)}}
	params := NavigationParams{Entrypoint: mainMCLPath, Source: source, Line: 0, Character: 5}
	result := RenameForProjectFS(params, files, "item")
	if result == nil || len(result.Edits) != 2 {
		t.Fatalf("RenameForProjectFS() = %#v, want local declaration and reference", result)
	}
	if result.Edits[0].Path != mainMCLPath || result.Edits[1].Path != mainMCLPath {
		t.Fatalf("RenameForProjectFS() edits = %#v, want edits in main.mcl", result.Edits)
	}
}

func TestRenameForProjectFSRenamesImportAliasAndQualifiedReferences(t *testing.T) {
	t.Parallel()
	const source = "import \"./defs.mcl\" as defs\nlet config: defs.Config = {}\nemit defs.value\n"
	files := fstest.MapFS{
		mainMCLPath: {Data: []byte(source)},
		defsMCLPath: {Data: []byte("export type Config = { name: String }\nexport let value = 1\n")},
	}
	params := NavigationParams{Entrypoint: mainMCLPath, Source: source, Line: 0, Character: 25}
	result := RenameForProjectFS(params, files, "definitions")
	if result == nil || len(result.Edits) != 3 {
		t.Fatalf("RenameForProjectFS() = %#v, want alias declaration and value/type qualifiers", result)
	}
	for _, edit := range result.Edits {
		if edit.Path != mainMCLPath || edit.NewText != "definitions" {
			t.Errorf("edit = %#v, want an edit in main.mcl to definitions", edit)
		}
	}
}

func TestRenameForProjectFSRejectsImportAliasCollision(t *testing.T) {
	t.Parallel()
	const source = "import \"./defs.mcl\" as defs\nlet replacement = 1\nemit defs.value\n"
	files := fstest.MapFS{
		mainMCLPath: {Data: []byte(source)},
		defsMCLPath: {Data: []byte("export let value = 1\n")},
	}
	params := NavigationParams{Entrypoint: mainMCLPath, Source: source, Line: 0, Character: 25}
	if result := RenameForProjectFS(params, files, "replacement"); result != nil {
		t.Fatalf("RenameForProjectFS() = %#v, want alias collision rejected", result)
	}
}

func TestRenameForProjectFSRejectsImportAliasShadowing(t *testing.T) {
	t.Parallel()
	const source = "import \"./defs.mcl\" as defs\nlet apply = fn(replacement) => defs.value\nemit apply\n"
	if _, err := AnalyzeSource(source); err != nil {
		t.Fatalf("AnalyzeSource() error = %v, want valid shadowing fixture", err)
	}
	files := fstest.MapFS{
		mainMCLPath: {Data: []byte(source)},
		defsMCLPath: {Data: []byte("export let value = 1\n")},
	}
	params := NavigationParams{Entrypoint: mainMCLPath, Source: source, Line: 0, Character: 25}
	if result := RenameForProjectFS(params, files, "replacement"); result != nil {
		t.Fatalf("RenameForProjectFS() = %#v, want shadowing rename rejected", result)
	}
}

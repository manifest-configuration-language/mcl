package languageservice

import (
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestReferencesForProjectFindsImportedExport(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	targetPath := filepath.Join(root, "target.mcl")
	importerPath := filepath.Join(root, "main.mcl")
	targetSource := "export let value = \"ok\"\n"
	importerSource := "import \"./target.mcl\" as target\nemit target.value\n"
	sources := map[string]string{targetPath: targetSource, importerPath: importerSource}
	resolve := func(importer, modulePath string) (string, bool) {
		resolved := filepath.Clean(filepath.Join(filepath.Dir(importer), modulePath))
		_, exists := sources[resolved]

		return resolved, exists
	}
	read := func(path string) (string, bool) {
		source, exists := sources[path]

		return source, exists
	}
	target, ok := ReferenceTargetAt(importerPath, importerSource, Position{Line: 1, Character: 12}, resolve, read)
	if !ok {
		t.Fatal("ReferenceTargetAt() did not resolve the imported export")
	}
	if target.Path != targetPath || target.Name != "value" || !target.Exported {
		t.Fatalf("ReferenceTargetAt() = %#v, want exported value in target file", target)
	}
	references := ReferencesForProject(target, sources, resolve, true)
	if len(references) != 2 {
		t.Fatalf("ReferencesForProject() returned %d locations, want declaration and import reference: %#v", len(references), references)
	}
	foundDeclaration, foundImport := false, false
	for _, reference := range references {
		foundDeclaration = foundDeclaration || reference.Path == targetPath && reference.Range.Start.Line == 0
		foundImport = foundImport || reference.Path == importerPath && reference.Range.Start.Line == 1
	}
	if !foundDeclaration || !foundImport {
		t.Errorf("ReferencesForProject() omitted declaration or imported member: %#v", references)
	}
}

func TestReferencesForProjectFSFindsImportedExport(t *testing.T) {
	t.Parallel()
	const targetSource = "export let value = \"ok\"\n"
	const importerSource = "import \"./target.mcl\" as target\nemit target.value\n"
	files := fstest.MapFS{
		mainMCLPath:  {Data: []byte(importerSource)},
		"target.mcl": {Data: []byte(targetSource)},
		otherMCLPath: {Data: []byte("emit value\n")},
	}
	params := NavigationParams{Entrypoint: mainMCLPath, Source: importerSource, Line: 1, Character: 12}
	references := ReferencesForProjectFS(params, files, true)
	if len(references) != 2 {
		t.Fatalf("ReferencesForProjectFS() returned %d locations, want declaration and imported reference: %#v", len(references), references)
	}
	if references[0].Path != mainMCLPath || references[1].Path != "target.mcl" {
		t.Fatalf("ReferencesForProjectFS() = %#v, want importer reference and target declaration", references)
	}
}

func TestReferencesForProjectFSFindsImportAliasReferences(t *testing.T) {
	t.Parallel()
	const source = "import \"./defs.mcl\" as defs\nlet config: defs.Config = {}\nemit defs.value\n"
	files := fstest.MapFS{
		mainMCLPath: {Data: []byte(source)},
		"defs.mcl":  {Data: []byte("export type Config = { name: String }\nexport let value = 1\n")},
	}
	params := NavigationParams{Entrypoint: mainMCLPath, Source: source, Line: 0, Character: 25}
	references := ReferencesForProjectFS(params, files, true)
	if len(references) != 3 {
		t.Fatalf("ReferencesForProjectFS() returned %d locations, want alias declaration and qualified value/type references: %#v", len(references), references)
	}
	for _, reference := range references {
		if reference.Path != mainMCLPath {
			t.Errorf("reference = %#v, want all references in main.mcl", reference)
		}
	}
}

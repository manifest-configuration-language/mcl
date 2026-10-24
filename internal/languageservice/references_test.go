package languageservice

import (
	"path/filepath"
	"testing"
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

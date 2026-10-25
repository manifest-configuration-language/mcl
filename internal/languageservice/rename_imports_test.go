package languageservice

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestRenameFileImportsForProjectFS(t *testing.T) {
	projectFiles := fstest.MapFS{
		"lib/old.mcl":    {Data: []byte("import \"./shared.mcl\" as shared\nlet value = shared.value\nemit value\n")},
		"lib/shared.mcl": {Data: []byte("export let value = 1\n")},
		"other/use.mcl":  {Data: []byte("import \"../lib/old.mcl\" as old\nemit old.value\n")},
	}
	params := NavigationParams{Entrypoint: "main.mcl", Source: "emit 1\n"}

	result, err := RenameFileImportsForProjectFS(params, projectFiles, "lib/old.mcl", "pkg/old.mcl")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Edits) != 2 {
		t.Fatalf("got %d edits, want 2: %#v", len(result.Edits), result.Edits)
	}
	if result.Edits[0].Path != "lib/old.mcl" || result.Edits[0].NewText != "\"../lib/shared.mcl\"" {
		t.Fatalf("unexpected moved-file import edit: %#v", result.Edits[0])
	}
	if result.Edits[1].Path != "other/use.mcl" || result.Edits[1].NewText != "\"../pkg/old.mcl\"" {
		t.Fatalf("unexpected dependent import edit: %#v", result.Edits[1])
	}
}

func TestRenameFileImportsRejectsIncompleteSource(t *testing.T) {
	projectFiles := fstest.MapFS{
		"lib/old.mcl": {Data: []byte("emit 1\n")},
		"broken.mcl":  {Data: []byte("import \"./lib/old.mcl\" as old\nemit old.\n")},
	}
	params := NavigationParams{Entrypoint: "main.mcl", Source: "emit 1\n"}

	_, err := RenameFileImportsForProjectFS(params, projectFiles, "lib/old.mcl", "pkg/old.mcl")
	var parseErr *ImportRenameError
	if !errors.As(err, &parseErr) || parseErr.Path != "broken.mcl" {
		t.Fatalf("got error %v, want ImportRenameError for broken.mcl", err)
	}
}

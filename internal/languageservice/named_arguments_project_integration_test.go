package languageservice

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestProjectNamedArgumentLSPResolvesAndRenamesImportedParameter(t *testing.T) {
	t.Parallel()

	const source = "import \"./helpers.mcl\" as helpers\nemit helpers.service(name = \"api\")\n"
	const targetSource = "export fn service(name: String) -> String = name\nlet local = service(name = \"local\")\n"
	files := fstest.MapFS{
		mainMCLPath:    {Data: []byte(source)},
		helpersMCLPath: {Data: []byte(targetSource)},
	}
	callOffset := strings.LastIndex(source, "name =")
	callParams := NavigationParams{Entrypoint: mainMCLPath, Source: source, Line: 1, Character: callOffset - strings.LastIndex(source[:callOffset], "\n") - 1}
	definition := DefinitionForProjectFS(callParams, files)
	if definition == nil || definition.Path != helpersMCLPath || definition.Range.Start.Character != strings.Index(targetSource, "name:") {
		t.Fatalf("DefinitionForProjectFS() = %#v, want helpers.mcl parameter declaration", definition)
	}

	parameterOffset := strings.Index(targetSource, "name:")
	bodyOffset := strings.LastIndex(targetSource, "= name") + 2
	positions := []struct {
		name   string
		params NavigationParams
	}{
		{name: "imported call", params: callParams},
		{name: "parameter declaration", params: NavigationParams{Entrypoint: helpersMCLPath, Source: targetSource, Character: parameterOffset}},
		{name: "parameter body reference", params: NavigationParams{Entrypoint: helpersMCLPath, Source: targetSource, Character: bodyOffset}},
	}
	for _, test := range positions {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			references := ReferencesForProjectFS(test.params, files, true)
			if len(references) != 4 {
				t.Fatalf("ReferencesForProjectFS() returned %d references, want declaration, body, local call and imported call: %#v", len(references), references)
			}
			paths := make(map[string]int)
			for _, reference := range references {
				paths[reference.Path]++
			}
			if paths[helpersMCLPath] != 3 || paths[mainMCLPath] != 1 {
				t.Fatalf("references by path = %#v, want three in helpers.mcl and one in main.mcl", paths)
			}

			rename := RenameForProjectFS(test.params, files, "serviceName")
			if rename == nil || len(rename.Edits) != 4 {
				t.Fatalf("RenameForProjectFS() returned %#v, want four cross-file parameter edits", rename)
			}
			paths = make(map[string]int)
			for _, edit := range rename.Edits {
				paths[edit.Path]++
				if edit.NewText != "serviceName" {
					t.Errorf("rename edit = %#v, want serviceName replacement", edit)
				}
			}
			if paths[helpersMCLPath] != 3 || paths[mainMCLPath] != 1 {
				t.Fatalf("rename edits by path = %#v, want three in helpers.mcl and one in main.mcl", paths)
			}
		})
	}
}

func TestProjectNamedArgumentResolutionRejectsAmbiguousImportAlias(t *testing.T) {
	t.Parallel()

	const source = "import \"./helpers.mcl\" as helpers\nimport \"./other.mcl\" as helpers\nemit helpers.service(name = \"api\")\n"
	files := fstest.MapFS{
		mainMCLPath:    {Data: []byte(source)},
		helpersMCLPath: {Data: []byte("export fn service(name: String) -> String = name\n")},
		"other.mcl":    {Data: []byte("export fn service(name: String) -> String = name\n")},
	}
	callOffset := strings.LastIndex(source, "name =")
	params := NavigationParams{Entrypoint: mainMCLPath, Source: source, Line: 2, Character: callOffset - strings.LastIndex(source[:callOffset], "\n") - 1}
	if definition := DefinitionForProjectFS(params, files); definition != nil {
		t.Fatalf("DefinitionForProjectFS() = %#v, want ambiguous call to remain unresolved", definition)
	}
	if rename := RenameForProjectFS(params, files, "serviceName"); rename != nil {
		t.Fatalf("RenameForProjectFS() = %#v, want ambiguous call to remain unchanged", rename)
	}
}

func TestProjectNamedArgumentRenameFailsClosedOnInvalidProjectSource(t *testing.T) {
	t.Parallel()

	const source = "import \"./helpers.mcl\" as helpers\nlet result = helpers.service(name = \"api\")\n"
	const targetSource = "export fn service(name: String) -> String = name\n"
	files := fstest.MapFS{
		mainMCLPath:    {Data: []byte(source)},
		helpersMCLPath: {Data: []byte(targetSource)},
		"broken.mcl":   {Data: []byte("let broken =\n")},
	}
	params := NavigationParams{
		Entrypoint: helpersMCLPath, Source: targetSource, Character: strings.Index(targetSource, "name:"),
	}
	if result := RenameForProjectFS(params, files, "serviceName"); result != nil {
		t.Fatalf("RenameForProjectFS() = %#v, want invalid project source to reject a partial rename", result)
	}
}

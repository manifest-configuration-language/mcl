package languageservice

import (
	"strings"
	"testing"
	"testing/fstest"
)

const helpersMCLPath = "helpers.mcl"

func TestNamedArgumentReferencesForProjectFindsImportedFunctionParameters(t *testing.T) {
	t.Parallel()

	const importerPath = "main.mcl"
	const source = "import \"./helpers.mcl\" as helpers\nemit helpers.service(name = \"api\")\n"
	const targetSource = "export fn service(name: String) -> String = name\n"
	files := fstest.MapFS{
		helpersMCLPath: {Data: []byte(targetSource)},
	}

	references := NamedArgumentReferencesForProject(importerPath, source, files)
	if len(references) != 1 {
		t.Fatalf("NamedArgumentReferencesForProject() returned %d references, want 1: %#v", len(references), references)
	}
	reference := references[0]
	if reference.Name != "name" || reference.Path != importerPath || reference.DeclarationPath != helpersMCLPath {
		t.Fatalf("reference = %#v, want named argument linked to helpers.mcl", reference)
	}
	if source[reference.Span.Start:reference.Span.End] != "name" || targetSource[reference.Declaration.Start:reference.Declaration.End] != "name" {
		t.Fatalf("reference spans = %#v, do not select matching parameter names", reference)
	}
	if reference.Span.Start != strings.LastIndex(source, "name =") {
		t.Fatalf("call argument span starts at %d, want %d", reference.Span.Start, strings.LastIndex(source, "name ="))
	}
}

func TestNamedArgumentReferencesForProjectIgnoresAmbiguousOrShadowedImports(t *testing.T) {
	t.Parallel()

	const targetSource = "export fn service(name: String) -> String = name\n"
	tests := []struct {
		name   string
		source string
	}{
		{name: "duplicate aliases", source: "import \"./helpers.mcl\" as helpers\nimport \"./helpers.mcl\" as helpers\nemit helpers.service(name = \"api\")\n"},
		{name: "local shadow", source: "import \"./helpers.mcl\" as helpers\nlet apply = fn(helpers) => helpers.service(name = \"api\")\nemit apply\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			files := fstest.MapFS{helpersMCLPath: {Data: []byte(targetSource)}}
			if references := NamedArgumentReferencesForProject("main.mcl", test.source, files); len(references) != 0 {
				t.Fatalf("NamedArgumentReferencesForProject() = %#v, want no links", references)
			}
		})
	}
}

package languageservice

import (
	"testing"
	"testing/fstest"
)

func TestNamedArgumentCompletionUsesImportedFunctionParameters(t *testing.T) {
	t.Parallel()

	const importerPath = "main.mcl"
	const source = "import \"./helpers.mcl\" as helpers\nlet result = helpers.service(na)\n"
	files := fstest.MapFS{
		helpersMCLPath: {Data: []byte("export fn service(name: String, namespace: String) -> String = name\n")},
	}
	line := "let result = helpers.service(na)"
	items := CompletionsForProjectFS(CompletionParams{
		Entrypoint: importerPath, Source: source, Line: 1, Character: len(line) - 1,
	}, files)
	found := make(map[string]bool, len(items))
	for _, item := range items {
		if item.Detail == "名前付き引数" && item.InsertText == item.Label+" = " {
			found[item.Label] = true
		}
	}
	if !found["name"] || !found["namespace"] {
		t.Fatalf("CompletionsForProjectFS() named parameters = %#v, want name and namespace", items)
	}
}

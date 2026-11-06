package languageservice

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestNamedArgumentNavigationReferencesAndRename(t *testing.T) {
	t.Parallel()

	const source = "fn render(name: String) -> String = name\nlet value = render(name = \"api\")\n"
	files := fstest.MapFS{mainMCLPath: {Data: []byte(source)}}
	callName := strings.Index(source, "name =")
	position := Position{Line: 1, Character: callName - strings.LastIndex(source[:callName], "\n") - 1}
	definition := DefinitionForProjectFS(NavigationParams{
		Entrypoint: mainMCLPath, Source: source, Line: position.Line, Character: position.Character,
	}, files)
	if definition == nil || definition.Range.Start.Character != strings.Index(source, "name:") {
		t.Fatalf("DefinitionForProjectFS() = %#v, want parameter declaration", definition)
	}

	resolve := func(string, string) (string, bool) { return "", false }
	target, found := ReferenceTargetAt(mainMCLPath, source, position, resolve, func(string) (string, bool) { return "", false })
	if !found || !target.Local || target.Declaration.Start != strings.Index(source, "name:") {
		t.Fatalf("ReferenceTargetAt() = %#v, %v, want local function parameter", target, found)
	}
	references := ReferencesForProject(target, map[string]string{mainMCLPath: source}, resolve, true)
	if len(references) != 3 {
		t.Fatalf("ReferencesForProject() returned %d references, want declaration, body reference, and named call argument: %#v", len(references), references)
	}

	result := RenameForSource(mainMCLPath, source, Position{Line: 0, Character: strings.Index(source, "name:")}, "containerName")
	if result == nil || len(result.Edits) != 3 {
		t.Fatalf("RenameForSource() = %#v, want edits for declaration, body, and named call argument", result)
	}
	var renamedCallArgument bool
	for _, edit := range result.Edits {
		renamedCallArgument = renamedCallArgument || edit.Range.Start.Line == 1 && edit.Range.Start.Character == strings.Index(source[strings.Index(source, "\n")+1:], "name =")
	}
	if !renamedCallArgument {
		t.Errorf("RenameForSource() = %#v, omitted named call argument", result.Edits)
	}
	callRename := RenameForSource(mainMCLPath, source, position, "containerName")
	if callRename == nil || len(callRename.Edits) != 3 {
		t.Errorf("RenameForSource() at named argument = %#v, want the same three parameter edits", callRename)
	}
}

func TestCompletionOffersUnspecifiedNamedArguments(t *testing.T) {
	t.Parallel()

	const source = "fn render(name: String, namespace: String) -> String = name\nlet value = render()\n"
	line := source[strings.Index(source, "\n")+1:]
	items := CompletionForSource(source, 1, strings.Index(line, ")"), "")
	found := map[string]CompletionItem{}
	for _, item := range items {
		found[item.Label] = item
	}
	for _, name := range []string{"name", "namespace"} {
		item, ok := found[name]
		if !ok || item.InsertText != name+" = " || item.Detail != "名前付き引数" {
			t.Errorf("completion %q = %#v, %v; want named argument insertion", name, item, ok)
		}
	}
}

func TestNamedArgumentCompletionOmitsArgumentsAlreadyProvided(t *testing.T) {
	t.Parallel()

	for _, call := range []string{`render(name = "api", )`, `render("api", )`} {
		t.Run(call, func(t *testing.T) {
			t.Parallel()

			const declaration = "fn render(name: String, namespace: String) -> String = name\n"
			line := "let value = " + call
			source := declaration + line + "\n"
			items := CompletionForSource(source, 1, strings.Index(line, ")"), "")
			found := make(map[string]bool, len(items))
			for _, item := range items {
				found[item.Label] = true
			}
			if found["name"] || !found["namespace"] {
				t.Fatalf("named argument completions = %#v, want only namespace among function parameters", items)
			}
		})
	}
}

func TestNamedArgumentRenamePrefersArgumentParameterOverShadowingLocal(t *testing.T) {
	t.Parallel()

	const source = "fn outer(name: String) -> String = inner(name = \"api\")\nfn inner(name: String) -> String = name\n"
	callName := strings.Index(source, "name =")
	result := RenameForSource(mainMCLPath, source, Position{Line: 0, Character: callName}, "serviceName")
	if result == nil || len(result.Edits) != 3 {
		t.Fatalf("RenameForSource() = %#v, want the inner parameter, body reference, and call argument", result)
	}
	outerParameter := strings.Index(source, "name:")
	for _, edit := range result.Edits {
		if edit.Range.Start.Line == 0 && edit.Range.Start.Character == outerParameter {
			t.Fatalf("RenameForSource() changed the shadowing outer parameter: %#v", result.Edits)
		}
	}
}

func TestNamedArgumentCompletionHandlesExistingAssignmentAndMultilineCalls(t *testing.T) {
	t.Parallel()

	const declaration = "fn render(name: String, namespace: String) -> String = name\n"
	tests := []struct {
		name       string
		call       string
		line       int
		character  int
		wantInsert string
	}{
		{
			name:       "existing assignment",
			call:       "let result = render(na = \"api\")\n",
			line:       1,
			character:  strings.Index("let result = render(na = \"api\")", "na ="),
			wantInsert: "name",
		},
		{
			name:       "multiline call",
			call:       "let result = render(\n  na\n)\n",
			line:       2,
			character:  len("  na"),
			wantInsert: "name = ",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := declaration + test.call
			items := CompletionForSource(source, test.line, test.character, "")
			for _, item := range items {
				if item.Label == "name" {
					if item.InsertText != test.wantInsert {
						t.Fatalf("name completion InsertText = %q, want %q", item.InsertText, test.wantInsert)
					}

					return
				}
			}
			t.Fatalf("completion omitted named argument name: %#v", items)
		})
	}
}

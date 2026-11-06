package languageservice

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestForwardFunctionReferenceSupportsNavigationReferencesAndRename(t *testing.T) {
	t.Parallel()

	const source = "let result = later(3)\nfn later(value: Int) -> Int = value + 1\nemit result\n"
	files := fstest.MapFS{mainMCLPath: {Data: []byte(source)}}
	callOffset := strings.Index(source, "later(")
	position := Position{Line: 0, Character: callOffset}
	params := NavigationParams{Entrypoint: mainMCLPath, Source: source, Line: position.Line, Character: position.Character}
	definition := DefinitionForProjectFS(params, files)
	declarationOffset := strings.Index(source, "later(value")
	if definition == nil || definition.Path != mainMCLPath || definition.Range.Start.Line != 1 || definition.Range.Start.Character != declarationOffset-strings.LastIndex(source[:declarationOffset], "\n")-1 {
		t.Fatalf("DefinitionForProjectFS() = %#v, want the forward function declaration", definition)
	}

	resolve := func(string, string) (string, bool) { return "", false }
	target, found := ReferenceTargetAt(mainMCLPath, source, position, resolve, func(string) (string, bool) { return "", false })
	if !found || target.Declaration.Start != declarationOffset {
		t.Fatalf("ReferenceTargetAt() = %#v, %v; want the forward function declaration", target, found)
	}
	references := ReferencesForProject(target, map[string]string{mainMCLPath: source}, resolve, true)
	if len(references) != 2 {
		t.Fatalf("ReferencesForProject() returned %d references, want declaration and call: %#v", len(references), references)
	}
	result := RenameForSource(mainMCLPath, source, position, "computeLater")
	if result == nil || len(result.Edits) != 2 {
		t.Fatalf("RenameForSource() = %#v, want declaration and forward call edits", result)
	}
}

func TestForwardFunctionCompletionIncludesLaterDeclaration(t *testing.T) {
	t.Parallel()

	const source = "let result = lat\nfn later(value: Int) -> Int = value + 1\n"
	items := CompletionForSource(source, 0, len("let result = lat"), "")
	for _, item := range items {
		if item.Label == "later" {
			return
		}
	}
	t.Fatalf("CompletionForSource() omitted later function: %#v", items)
}

func TestForwardLetReferenceRemainsUnavailableToNavigationAndCompletion(t *testing.T) {
	t.Parallel()

	const source = "let first = later\nlet later = 1\n"
	files := fstest.MapFS{mainMCLPath: {Data: []byte(source)}}
	definition := DefinitionForProjectFS(NavigationParams{
		Entrypoint: mainMCLPath, Source: source, Line: 0, Character: strings.Index(source, "later"),
	}, files)
	if definition != nil {
		t.Fatalf("DefinitionForProjectFS() = %#v, want forward let reference to remain unresolved", definition)
	}
	items := CompletionForSource(source, 0, len("let first = later"), "")
	for _, item := range items {
		if item.Label == "later" {
			t.Fatalf("CompletionForSource() exposed a later let declaration: %#v", items)
		}
	}
}

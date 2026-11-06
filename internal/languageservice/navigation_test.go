package languageservice

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

func TestDeclarationHoverShowsFunctionParameterDefaults(t *testing.T) {
	t.Parallel()

	const source = "fn service(name: String = \"api\", retries: Int = 3) -> String = name\n"
	index, err := AnalyzeSource(source)
	if err != nil {
		t.Fatalf("AnalyzeSource() error = %v", err)
	}
	declaration, ok := index.Module.Nodes[0].(*syntax.FunctionDeclaration)
	if !ok {
		t.Fatalf("first declaration = %T, want *syntax.FunctionDeclaration", index.Module.Nodes[0])
	}

	got := DeclarationHover(declaration, source)
	const want = "fn service(name: String = \"api\", retries: Int = 3) -> String"
	if got != want {
		t.Errorf("DeclarationHover() = %q, want %q", got, want)
	}
}

func TestEarlierFunctionParameterResolvesInsideDefault(t *testing.T) {
	t.Parallel()

	const source = "fn configure(prefix: String, name: String = prefix) -> String = name\n"
	position := Position{Line: 0, Character: strings.LastIndex(source, "prefix")}
	files := fstest.MapFS{mainMCLPath: {Data: []byte(source)}}
	definition := DefinitionForProjectFS(NavigationParams{Entrypoint: mainMCLPath, Source: source, Line: position.Line, Character: position.Character}, files)
	if definition == nil || definition.Range.Start.Character != strings.Index(source, "prefix") {
		t.Fatalf("DefinitionForProjectFS() = %#v, want earlier parameter declaration", definition)
	}

	result := RenameForSource(mainMCLPath, source, position, "namePrefix")
	if result == nil || len(result.Edits) != 2 {
		t.Fatalf("RenameForSource() = %#v, want parameter declaration and default reference", result)
	}
}

package main

import (
	"testing"
	"unicode/utf16"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/wasmprotocol"
)

func TestDiagnosticsMapsUnreachableDefinitionCodeAction(t *testing.T) {
	t.Parallel()

	source := "let kept = \"🦦\"\nlet unused = \"💩\"\nemit kept\n"
	module, err := engine.ParseAST(source)
	if err != nil {
		t.Fatal(err)
	}

	got := diagnostics(module, source, "main.mcl")
	if len(got) != 1 {
		t.Fatalf("diagnostics() returned %d diagnostics, want 1: %#v", len(got), got)
	}
	diagnostic := got[0]
	if diagnostic.Code != "MCLW001" || diagnostic.Path != "main.mcl" {
		t.Fatalf("diagnostic identity = (%q, %q), want (MCLW001, main.mcl)", diagnostic.Code, diagnostic.Path)
	}
	if len(diagnostic.Actions) != 1 {
		t.Fatalf("diagnostic has %d actions, want 1: %#v", len(diagnostic.Actions), diagnostic.Actions)
	}
	action := diagnostic.Actions[0]
	if action.Kind != wasmprotocol.LintCodeActionKindRemoveUnreachableDefinition {
		t.Fatalf("action kind = %q, want %q", action.Kind, wasmprotocol.LintCodeActionKindRemoveUnreachableDefinition)
	}
	if len(action.Edits) != 1 {
		t.Fatalf("action has %d edits, want 1: %#v", len(action.Edits), action.Edits)
	}
	edit := action.Edits[0]
	if edit.Path != "main.mcl" || edit.NewText != "" {
		t.Fatalf("edit = %#v, want main.mcl and an empty replacement", edit)
	}
	wantEndCharacter := uint64(len(utf16.Encode([]rune(`let unused = "💩"`))))
	wantRange := wasmprotocol.SourceRange{
		Start: wasmprotocol.CompletionPosition{Line: 1, Character: 0},
		End:   wasmprotocol.CompletionPosition{Line: 1, Character: wantEndCharacter},
	}
	if edit.Range != wantRange {
		t.Fatalf("edit range = %#v, want %#v", edit.Range, wantRange)
	}
}

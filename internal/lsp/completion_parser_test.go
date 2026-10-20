package lsp

import (
	"strings"
	"testing"
)

func TestPartialLocalBindingsUseSharedParser(t *testing.T) {
	t.Parallel()
	source := "let text = \"fn(stringValue) => stringValue\"\n# fn(commentValue) => commentValue\nlet result = map(values, fn(value) => value + 1)"
	cursor := strings.Index(source, "value +") + len("value")
	bindings := partialLocalBindings(source, cursor)
	if len(bindings) != 1 || bindings[0].name != "value" {
		t.Fatalf("partial local bindings = %#v, want only value", bindings)
	}
	if got := source[bindings[0].declaration.Start:bindings[0].declaration.End]; got != "value" {
		t.Fatalf("parameter span = %q, want value", got)
	}
}

func TestPartialLocalBindingsWithIncompleteNestedExpression(t *testing.T) {
	t.Parallel()
	source := "let result = map(values, fn(left, right) => call(right,"
	cursor := len(source)
	bindings := partialLocalBindings(source, cursor)
	if len(bindings) != 2 || bindings[0].name != "left" || bindings[1].name != "right" {
		t.Fatalf("partial local bindings = %#v, want left and right", bindings)
	}
}

func TestPartialLocalBindingsForIncompleteForExpression(t *testing.T) {
	t.Parallel()
	source := "let values = for item in items:\n  item +"
	bindings := partialLocalBindings(source, len(source))
	if len(bindings) != 1 || bindings[0].name != "item" {
		t.Fatalf("partial local bindings = %#v, want only item", bindings)
	}
}

func TestPartialLocalBindingsForIncompleteFunctionBody(t *testing.T) {
	t.Parallel()
	source := "fn render(value: String) -> String = value +"
	bindings := partialLocalBindings(source, len(source))
	if len(bindings) != 1 || bindings[0].name != "value" {
		t.Fatalf("partial local bindings = %#v, want only value", bindings)
	}
}

func TestPartialLocalBindingsStopsOnLexicalError(t *testing.T) {
	t.Parallel()
	source := "let result = map(values, fn(value) => value, \"valid\")\n\tinvalid \"unterminated"
	if bindings := partialLocalBindings(source, len(source)); len(bindings) != 0 {
		t.Fatalf("partial local bindings = %#v, want none for a lexically invalid prefix", bindings)
	}
}

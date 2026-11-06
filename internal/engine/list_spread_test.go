package engine

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluateListSpreadInFunctionBody(t *testing.T) {
	t.Parallel()

	const source = `fn extend(items: List<Int>) -> List<Int> = [...items, 3]
let values = extend([1, 2])
emit each values
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v", err)
	}
	want := []string{"1", "2", "3"}
	if len(bundle.Artifacts) != len(want) {
		t.Fatalf("EvaluateSource() emitted %d artifacts, want %d", len(bundle.Artifacts), len(want))
	}
	for index, artifact := range bundle.Artifacts {
		if artifact.Value.Kind != Integer || artifact.Value.Text != want[index] {
			t.Errorf("artifact %d = (%v, %q), want (Integer, %q)", index, artifact.Value.Kind, artifact.Value.Text, want[index])
		}
	}
}

func TestEvaluateListSpreadChecksDynamicValueAtRuntime(t *testing.T) {
	t.Parallel()

	t.Run("list", func(t *testing.T) {
		t.Parallel()

		const source = `let value: Any = [1, 2]
let values = [...value]
emit each values
`
		bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
		if err != nil {
			t.Fatalf("EvaluateSource() error = %v", err)
		}
		if len(bundle.Artifacts) != 2 || bundle.Artifacts[0].Value.Text != "1" || bundle.Artifacts[1].Value.Text != "2" {
			t.Fatalf("EvaluateSource() artifacts = %#v, want [1, 2]", bundle.Artifacts)
		}
	})
	t.Run("scalar", func(t *testing.T) {
		t.Parallel()

		const source = `let value: Any = 1
let values = [...value]
emit each values
`
		_, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
		if err == nil || !strings.Contains(err.Error(), "list spread requires a List value") {
			t.Fatalf("EvaluateSource() error = %v, want a runtime List spread diagnostic", err)
		}
	})
}

func TestEvaluateListSpreadPreservesStaticIndexFacts(t *testing.T) {
	t.Parallel()

	const source = `let keys = ["a", ...["b", "c"], "d"]
let selected = keys[2]
let values =
  c: "correct"
  d: 123
let result: String = values[keys[2]]
emit result
`
	module, err := ParseAST(source)
	if err != nil {
		t.Fatalf("ParseAST() error = %v", err)
	}
	keysExpression := module.Nodes[0].(*LetDeclaration).Expression
	keysAnalysis, known, err := inferStaticExprAnalysis(keysExpression, nil, nil)
	if err != nil || !known {
		t.Fatalf("inferStaticExprAnalysis(keys) = (%#v, %t, %v)", keysAnalysis, known, err)
	}
	selectedExpression := module.Nodes[1].(*LetDeclaration).Expression
	selected, known, err := inferStaticExprAnalysis(selectedExpression, map[string]TypeAnalysis{"keys": keysAnalysis}, nil)
	if err != nil || !known || !selected.Facts.ConstantStringKnown || selected.Facts.ConstantString != "c" {
		t.Fatalf("inferStaticExprAnalysis(keys[2]) = (%#v, %t, %v), want constant String c", selected, known, err)
	}

	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v, want spread-aware index facts: %v", err, err)
	}
	if len(bundle.Artifacts) != 1 || bundle.Artifacts[0].Value.Kind != String || bundle.Artifacts[0].Value.Text != "correct" {
		t.Fatalf("EvaluateSource() artifacts = %#v, want the String value at key c", bundle.Artifacts)
	}

	t.Run("unknown spread length", func(t *testing.T) {
		t.Parallel()

		module, err := ParseAST("let keys = [\"a\", ...parts, \"d\"]\nlet selected = keys[2]\n")
		if err != nil {
			t.Fatalf("ParseAST() error = %v", err)
		}
		bindings := map[string]TypeAnalysis{
			"parts": {
				Type:  ResolvedType{Name: typeList, Element: new(ResolvedType{Name: typeString})},
				Facts: TypeAnalysisFacts{BundleKnown: true},
			},
		}
		keys, known, err := inferStaticExprAnalysis(module.Nodes[0].(*LetDeclaration).Expression, bindings, nil)
		if err != nil || !known || keys.Type.Element == nil || keys.Type.Element.Name != typeString {
			t.Fatalf("inferStaticExprAnalysis(keys) = (%#v, %t, %v), want List<String>", keys, known, err)
		}
		selected, known, err := inferStaticExprAnalysis(module.Nodes[1].(*LetDeclaration).Expression, map[string]TypeAnalysis{"keys": keys}, nil)
		if err != nil || !known || selected.Facts.ConstantStringKnown {
			t.Fatalf("inferStaticExprAnalysis(keys[2]) = (%#v, %t, %v), want unknown positional facts", selected, known, err)
		}
	})
}

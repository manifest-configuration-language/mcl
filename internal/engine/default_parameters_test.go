package engine

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluateFunctionDefaultsParseComparisonsAndNestedCalls(t *testing.T) {
	t.Parallel()

	const source = `fn add(left: Int, right: Int = left + 1) -> Int = left + right
fn result(flag: Bool = 1 < 2, value: Int = add(left = 2, right = 3)) -> Int = value
fn pair(first: Int = 1, second: Int) -> Int = first + second
fn strings(values: List<String> = []) -> List<String> = values

emit result()
emit pair(second = 2)
emit strings()
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v", err)
	}
	if len(bundle.Artifacts) != 3 || bundle.Artifacts[0].Value.Kind != Integer || bundle.Artifacts[0].Value.Text != "5" {
		t.Fatalf("EvaluateSource() artifacts = %#v, want Integer 5, Integer 3, and an empty List", bundle.Artifacts)
	}
	if value := bundle.Artifacts[1].Value; value.Kind != Integer || value.Text != "3" {
		t.Errorf("pair(second = 2) = (%v, %q), want (Integer, 3)", value.Kind, value.Text)
	}
	if value := bundle.Artifacts[2].Value; value.Kind != List || len(value.List) != 0 {
		t.Errorf("strings() = (%v, %#v), want an empty List", value.Kind, value.List)
	}
}

func TestEvaluateFunctionDefaultValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
	}{
		{
			name:   "required parameter after default",
			source: "fn pair(first: Int = 1, second: Int) -> Int = first + second\nemit pair()\n",
		},
		{
			name:   "default type mismatch",
			source: "fn value(result: Int = \"wrong\") -> Int = result\nemit value()\n",
		},
		{
			name:   "later parameter in default",
			source: "fn pair(first: Int = second, second: Int = 2) -> Int = first + second\nemit pair()\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), test.source); err == nil {
				t.Fatal("EvaluateSource() succeeded for invalid function defaults")
			}
		})
	}
}

func TestEvaluateRecursiveFunctionDefaultUsesCallBudget(t *testing.T) {
	t.Parallel()

	const source = `fn recurse(value: Int = recurse()) -> Int = value
emit recurse()
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source); err == nil {
		t.Fatal("EvaluateSource() succeeded for a recursive default expression")
	} else if !strings.Contains(err.Error(), "function call depth exceeds") {
		t.Fatalf("EvaluateSource() error = %v, want a function call depth diagnostic", err)
	}
}

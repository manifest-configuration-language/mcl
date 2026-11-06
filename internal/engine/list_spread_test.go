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

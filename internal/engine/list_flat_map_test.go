package engine

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluateListsFlatMap(t *testing.T) {
	t.Parallel()

	const source = `import "std:list" as lists

let values: List<Int> = [1, 2]
let emptyValues: List<Int> = []
fn expand(value: Int) -> List<Int> = [value, value + 10]
let expanded: List<Int> = lists.flatMap(values = values, transform = expand)
let empty: List<Int> = lists.flatMap(emptyValues, fn(value) => [value])
emit each expanded
emit each empty
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v", err)
	}
	want := []string{"1", "11", "2", "12"}
	if len(bundle.Artifacts) != len(want) {
		t.Fatalf("EvaluateSource() emitted %d artifacts, want %d", len(bundle.Artifacts), len(want))
	}
	for index, artifact := range bundle.Artifacts {
		if artifact.Value.Kind != Integer || artifact.Value.Text != want[index] {
			t.Errorf("artifact %d = (%v, %q), want (Integer, %q)", index, artifact.Value.Kind, artifact.Value.Text, want[index])
		}
	}
}

func TestEvaluateListsFlatMapRejectsNonListCallback(t *testing.T) {
	t.Parallel()

	const source = `import "std:list" as lists

fn scalar(value: Int) -> Int = value
let values: List<Int> = [1]
let expanded = lists.flatMap(values, scalar)
emit each expanded
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source); err == nil || !strings.Contains(err.Error(), "flatMap transform must return a List") {
		t.Fatalf("EvaluateSource() error = %v, want a flatMap List return-type diagnostic", err)
	}
}

func TestEvaluateListsFlatMapRejectsNonListAnyCallbackResult(t *testing.T) {
	t.Parallel()

	const source = `import "std:list" as lists

fn scalar(value: Int) -> Any = value
let values: List<Int> = [1]
let expanded = lists.flatMap(values, scalar)
emit each expanded
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source); err == nil || !strings.Contains(err.Error(), "flatMap transform for item 0 returned Int, expected a List") {
		t.Fatalf("EvaluateSource() error = %v, want a runtime flatMap List diagnostic", err)
	}
}

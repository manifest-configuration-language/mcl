package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluateFunctionCompositionAcrossImports(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	library := `export fn combine(base: Int, amount: Int = base + 1) -> Int = base + amount
`
	if err := os.WriteFile(filepath.Join(root, "functions.mcl"), []byte(library), 0o600); err != nil {
		t.Fatal(err)
	}

	const source = `import "./functions.mcl" as shared

fn first(value: Int) -> Int = later(value)
fn later(value: Int) -> Int = value + 1
fn caller(amount: Int) -> Int = shared.combine(base = 2)

let combine = shared.combine
let importedDefault = shared.combine(base = 2)
let functionValueNamed = combine(amount = 10, base = 4)
let functionValueDefault = combine(base = 5)
let definitionDefault = caller(100)
let forwardFunction = first(6)

emit importedDefault
emit functionValueNamed
emit functionValueDefault
emit definitionDefault
emit forwardFunction
`
	bundle, err := EvaluateSource(filepath.Join(root, "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v", err)
	}
	want := []string{"5", "14", "11", "5", "7"}
	if len(bundle.Artifacts) != len(want) {
		t.Fatalf("EvaluateSource() emitted %d artifacts, want %d", len(bundle.Artifacts), len(want))
	}
	for index, artifact := range bundle.Artifacts {
		if artifact.Value.Kind != Integer || artifact.Value.Text != want[index] {
			t.Errorf("artifact %d = (%v, %q), want (Integer, %q)", index, artifact.Value.Kind, artifact.Value.Text, want[index])
		}
	}
}

func TestEvaluateFunctionCompositionKeepsLetDeclarationsOrdered(t *testing.T) {
	t.Parallel()

	const source = `let result = later
let later = 1
emit result
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source); err == nil {
		t.Fatal("EvaluateSource() succeeded with a forward reference to a top-level let")
	}
}

func TestEvaluateListSpreadAndTypedRecordArgument(t *testing.T) {
	t.Parallel()

	const source = `type ServiceConfig = { name: String, port: Int }

fn service(config: ServiceConfig) -> Map<String, Any> = {
  name: config.name,
  port: config.port,
}

let config: ServiceConfig =
  name: "api"
  port: 8080
let nested = [2, 3]
let values = [1, ...nested, ...[], ...[4, 5]]
let manifest = service(config)

emit each values
emit manifest
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v", err)
	}
	wantKinds := []Kind{Integer, Integer, Integer, Integer, Integer, Map}
	wantValues := []string{"1", "2", "3", "4", "5", ""}
	if len(bundle.Artifacts) != len(wantKinds) {
		t.Fatalf("EvaluateSource() emitted %d artifacts, want %d", len(bundle.Artifacts), len(wantKinds))
	}
	for index, artifact := range bundle.Artifacts {
		if artifact.Value.Kind != wantKinds[index] || artifact.Value.Text != wantValues[index] {
			t.Errorf("artifact %d = (%v, %q), want (%v, %q)", index, artifact.Value.Kind, artifact.Value.Text, wantKinds[index], wantValues[index])
		}
	}
	manifest := bundle.Artifacts[len(bundle.Artifacts)-1].Value
	fields := make(map[string]Value, len(manifest.Map))
	for _, entry := range manifest.Map {
		fields[entry.Key] = entry.Value
	}
	if name := fields["name"]; name.Kind != String || name.Text != "api" {
		t.Errorf("manifest name = (%v, %q), want (String, %q)", name.Kind, name.Text, "api")
	}
	if port := fields["port"]; port.Kind != Integer || port.Text != "8080" {
		t.Errorf("manifest port = (%v, %q), want (Integer, %q)", port.Kind, port.Text, "8080")
	}
}

func TestEvaluateListSpreadRejectsWrongElementType(t *testing.T) {
	t.Parallel()

	const source = `let values: List<Int> = [...["wrong"]]
emit each values
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source); err == nil || !strings.Contains(err.Error(), "List<Int>") {
		t.Fatalf("EvaluateSource() error = %v, want a List<Int> element type diagnostic", err)
	}
}

func TestEvaluateTopLevelLetCanCallLaterFunction(t *testing.T) {
	t.Parallel()

	const source = `let result = later(3)
fn later(value: Int) -> Int = value + 1
emit result
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v", err)
	}
	if len(bundle.Artifacts) != 1 || bundle.Artifacts[0].Value.Kind != Integer || bundle.Artifacts[0].Value.Text != "4" {
		t.Fatalf("EvaluateSource() artifacts = %#v, want one Integer 4", bundle.Artifacts)
	}
}

func TestEvaluateFunctionForwardReferenceCapturesOnlyEarlierLets(t *testing.T) {
	t.Parallel()

	const source = `fn first() -> Int = later()
let base = 1
fn later() -> Int = base
emit first()
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v", err)
	}
	if len(bundle.Artifacts) != 1 || bundle.Artifacts[0].Value.Kind != Integer || bundle.Artifacts[0].Value.Text != "1" {
		t.Fatalf("EvaluateSource() artifacts = %#v, want one Integer 1", bundle.Artifacts)
	}
}

func TestEvaluateInferredForwardFunctionReference(t *testing.T) {
	t.Parallel()

	const source = `fn first(value: Int) = later(value)
fn later(value: Int) = value + 1
let result = first(3)
emit result
`
	bundle, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source)
	if err != nil {
		t.Fatalf("EvaluateSource() error = %v", err)
	}
	if len(bundle.Artifacts) != 1 || bundle.Artifacts[0].Value.Kind != Integer || bundle.Artifacts[0].Value.Text != "4" {
		t.Fatalf("EvaluateSource() artifacts = %#v, want one Integer 4", bundle.Artifacts)
	}
}

func TestEvaluateForwardFunctionCallRequiresCapturedLetsToBeInitialized(t *testing.T) {
	t.Parallel()

	const source = `let result = later()
fn first() -> Int = later()
let base = 1
fn later() -> Int = base
emit result
`
	if _, err := EvaluateSource(filepath.Join(t.TempDir(), "main.mcl"), source); err == nil || !strings.Contains(err.Error(), `requires let "base" to be initialized`) {
		t.Fatalf("EvaluateSource() error = %v, want an uninitialized captured-let diagnostic", err)
	}
}

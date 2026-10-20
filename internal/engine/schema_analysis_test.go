package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
)

func TestSchemaAnalysisLoadsOnlyExportedTypesWithoutEvaluatingValues(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, "main.mcl")
	dependencyPath := filepath.Join(root, "types.mcl")
	dependencySource := "export type Config = { name: String }\ntype Private = { token: String }\nexport let failure = missingFunction()"
	if err := os.WriteFile(dependencyPath, []byte(dependencySource), 0o600); err != nil {
		t.Fatal(err)
	}
	source := `import "./types.mcl" as types`
	dependencies, err := dependency.Open(t.Context(), root, false, dependencyToolchainIdentity())
	if err != nil {
		t.Fatal(err)
	}
	loader := schemaAnalysisLoader{
		root: root, entry: entry, entrySource: source, dependencies: dependencies,
		cache: make(map[string]map[string]ResolvedType), active: make(map[string]bool),
	}
	types, err := loader.analyze(entry, true)
	if err != nil {
		t.Fatalf("type-only analysis evaluated a value or failed to load types: %v", err)
	}
	if _, exists := types["types.Config"]; !exists {
		t.Fatal("imported exported type was not available")
	}
	if _, exists := types["types.Private"]; exists {
		t.Fatal("imported private type was exposed")
	}
}

func TestSchemaAnalysisReportsImportCycles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, "first.mcl")
	second := filepath.Join(root, "second.mcl")
	if err := os.WriteFile(first, []byte(`import "./second.mcl" as second`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(`import "./first.mcl" as first`), 0o600); err != nil {
		t.Fatal(err)
	}
	dependencies, err := dependency.Open(t.Context(), root, false, dependencyToolchainIdentity())
	if err != nil {
		t.Fatal(err)
	}
	loader := schemaAnalysisLoader{
		root: root, entry: first, entrySource: `import "./second.mcl" as second`, dependencies: dependencies,
		cache: make(map[string]map[string]ResolvedType), active: make(map[string]bool),
	}
	_, err = loader.analyze(first, true)
	if err == nil || !strings.Contains(err.Error(), "module import cycle") {
		t.Fatalf("expected module import cycle diagnostic, got %v", err)
	}
}

func TestSchemaFieldCompletionsWithErrorDistinguishesNoCandidatesFromFailure(t *testing.T) {
	t.Parallel()
	fields, err := SchemaFieldCompletionsWithError(filepath.Join(t.TempDir(), "main.mcl"), "let value = 1", "placeholder")
	if err != nil || len(fields) != 0 {
		t.Fatalf("expected no candidates without an error, got %v, %v", fields, err)
	}
	_, err = SchemaFieldCompletionsWithError(filepath.Join(t.TempDir(), "main.mcl"), "let =", "placeholder")
	if err == nil {
		t.Fatal("expected a parse error to be returned")
	}
}

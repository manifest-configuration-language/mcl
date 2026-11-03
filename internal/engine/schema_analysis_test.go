package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

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

func TestModuleTypeAliasCanReferenceImportedExportedType(t *testing.T) {
	t.Parallel()
	const root = "/virtual-type-alias"
	files := fstest.MapFS{
		"shared.mcl": &fstest.MapFile{Data: []byte("export type Config = { name: String }\n")},
	}
	source := `import "./shared.mcl" as shared
type Local = shared.Config
let value: Local =
  name: "ready"
emit value.name
`
	fields, err := SchemaTypeFieldsForProjectFS(root, "main.mcl", source, "Local", nil, files)
	if err != nil || !slices.Contains(fields, "name") {
		t.Fatalf("schema type fields = %v, %v; want imported field name", fields, err)
	}
	bundle, err := EvaluateProjectSourceContext(t.Context(), root, "main.mcl", source, files, nil)
	if err != nil {
		t.Fatalf("EvaluateProjectSourceContext() error = %v, want imported type alias to validate", err)
	}
	output, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"ready"`) {
		t.Fatalf("Render() = %s, want imported type alias value", output)
	}
}

func TestListMapAcceptsImportedUnionThroughLocalTypeAlias(t *testing.T) {
	t.Parallel()

	const root = "/virtual-imported-union"
	files := fstest.MapFS{
		"types.mcl": &fstest.MapFile{Data: []byte(`
export type Config = { enabled: Bool }
export type WakeOnLAN = { backend: "WakeOnLAN", wakeOnLAN: { broadcastAddress: String } }
export type Redfish = { backend: "Redfish", redfish: { address: String } }
export type Power = WakeOnLAN | Redfish
`)},
	}
	const source = `import "std:list" as list
import "./types.mcl" as types

type LocalConfig = types.Config
type Host = { power: types.Power, config: LocalConfig }

fn powerOf(host: Host) -> Any = host.power

let hosts: List<Host> =
  - power:
      backend: "WakeOnLAN"
      wakeOnLAN:
        broadcastAddress: "192.168.0.255:9"
    config:
      enabled: true

emit each list.map(hosts, powerOf)
`
	if _, err := EvaluateProjectSourceContext(t.Context(), root, "main.mcl", source, files, nil); err != nil {
		t.Fatalf("EvaluateProjectSourceContext() error = %v, want imported union and local alias callback types to validate", err)
	}
}

func TestImportedCallbackAliasChecksInlineLambdaSignature(t *testing.T) {
	t.Parallel()

	const root = "/virtual-imported-callback"
	files := fstest.MapFS{
		"callbacks.mcl": &fstest.MapFile{Data: []byte(`
type Payload = { name: String }
export type Callback = Function<Payload, String>
`)},
	}
	const source = `import "./callbacks.mcl" as callbacks

fn consume(callback: callbacks.Callback) -> String = "ok"
let result = consume(fn(payload) => payload.name)
`
	if _, err := EvaluateProjectSourceContext(t.Context(), root, "main.mcl", source, files, nil); err != nil {
		t.Fatalf("EvaluateProjectSourceContext() error = %v, want imported callback alias to accept a compatible lambda", err)
	}

	invalidSource := strings.Replace(source, "payload.name", "1", 1)
	if _, err := EvaluateProjectSourceContext(t.Context(), root, "main.mcl", invalidSource, files, nil); err == nil || !strings.Contains(err.Error(), "lambda returns Int, declared String") {
		t.Fatalf("EvaluateProjectSourceContext() error = %v, want an incompatible callback return type diagnostic", err)
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

func TestSchemaCompletionsUseInjectedProjectFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-schema-completions")
	files := fstest.MapFS{
		"types/types.mcl":                &fstest.MapFile{Data: []byte(`export type Config = schema("schemas/config.json")`)},
		"types/schemas/config.json":      &fstest.MapFile{Data: []byte(`{"$ref":"definitions.json#/$defs/Config"}`)},
		"types/schemas/definitions.json": &fstest.MapFile{Data: []byte(`{"$defs":{"Config":{"type":"object","properties":{"name":{"type":"string"},"nested":{"type":"object","properties":{"child":{"type":"string"}}}}}}}`)},
	}
	source := `import "./types/types.mcl" as types
let value: types.Config =
  nested:
    __mcl_completion__: null
`
	fields, err := SchemaFieldCompletionsWithErrorForProjectFS(root, "main.mcl", source, "__mcl_completion__", files)
	if err != nil || !slices.Contains(fields, "child") {
		t.Fatalf("schema completions = %v, %v; want child from the injected schema", fields, err)
	}
	fields, err = SchemaTypeFieldsForProjectFS(root, "main.mcl", `import "./types/types.mcl" as types`, "types.Config", []string{"nested"}, files)
	if err != nil || !slices.Contains(fields, "child") {
		t.Fatalf("schema type fields = %v, %v; want child from imported schema references", fields, err)
	}
}

func TestHelmSchemaCompletionsUseInjectedProjectFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-helm-schema-completions")
	files := fstest.MapFS{
		"charts/app/values.schema.json": &fstest.MapFile{Data: []byte(`{"$ref":"definitions.json#/$defs/Values"}`)},
		"charts/app/definitions.json":   &fstest.MapFile{Data: []byte(`{"$defs":{"Values":{"type":"object","properties":{"nested":{"type":"object","properties":{"child":{"type":"string"}}}}}}}`)},
	}
	source := `import "std:helm" as helm
let options =
  values:
    nested:
      __mcl_completion__: null
let rendered = helm.render("charts/app", options)`
	fields := HelmSchemaFieldCompletionsForProjectFS(root, "main.mcl", source, "__mcl_completion__", files)
	if !slices.Contains(fields, "child") {
		t.Fatalf("Helm schema completions = %v; want child from injected chart schema", fields)
	}
}

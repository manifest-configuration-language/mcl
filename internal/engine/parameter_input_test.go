package engine

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRuntimeTypePathPreservesUnstructuredCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("underlying failure")
	err := addRuntimeTypePath(cause, "outer", "field %q", false, false)
	if !errors.Is(err, cause) {
		t.Fatal("field context lost its underlying cause")
	}
	if err.Error() != `field "outer": underlying failure` {
		t.Fatalf("unexpected field context: %s", err)
	}
}

const parameterEnvoyMemory = "envoyMemory"

func TestParseParameterFileRetainsNestedJSONLocations(t *testing.T) {
	t.Parallel()
	source := []byte("{\n  \"settings\": {\n    \"racks\": [{\"members\": 64}],\n    \"labels\": {\"example.com/key\": true}\n  },\n  \"odd~name\": null\n}\n")
	parameters, sources, err := ParseParameterFile("deployment.json", source)
	if err != nil {
		t.Fatalf("ParseParameterFile() error = %v", err)
	}
	if got := string(parameters[testSettingsName]); !strings.Contains(got, `"racks"`) || !strings.Contains(got, `"example.com/key"`) {
		t.Fatalf("settings JSON = %s, want nested input fields", got)
	}
	parameterSource := sources[testSettingsName]
	for pointer, token := range map[string]string{
		"/settings/racks/0/members":         "64",
		"/settings/labels/example.com~1key": "true",
	} {
		location, exists := parameterSource.Locations[pointer]
		if !exists {
			t.Fatalf("location %q was not recorded", pointer)
		}
		start := strings.Index(string(source), token)
		if location.Span != (SourceSpan{Start: start, End: start + len(token)}) {
			t.Errorf("location %q span = %+v, want %d-%d", pointer, location.Span, start, start+len(token))
		}
	}
	keyLocation := parameterSource.Locations["/settings/labels/example.com~1key"]
	keyStart := strings.Index(string(source), `"example.com/key"`)
	if keyLocation.KeySpan != (SourceSpan{Start: keyStart, End: keyStart + len(`"example.com/key"`)}) {
		t.Fatalf("map key span = %+v, want %d-%d", keyLocation.KeySpan, keyStart, keyStart+len(`"example.com/key"`))
	}
	if _, exists := sources["odd~name"].Locations["/odd~0name"]; !exists {
		t.Fatal("parameter name was not escaped in its JSON Pointer")
	}
}

func TestParseParameterFileRejectsExcessiveNesting(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("[", maxCallDepth+1) + "0" + strings.Repeat("]", maxCallDepth+1)
	source := []byte("{\"value\":" + value + "}")
	_, _, err := ParseParameterFile("deep.json", source)
	if err == nil || !strings.Contains(err.Error(), "nesting exceeds") {
		t.Fatalf("ParseParameterFile() error = %v, want nesting limit error", err)
	}
}

func TestParameterTypeFailureTracksNestedPaths(t *testing.T) {
	t.Parallel()
	stringType := ResolvedType{Name: typeString}
	intType := ResolvedType{Name: typeInt}
	tests := []struct {
		name     string
		value    RuntimeValue
		typeExpr ResolvedType
		path     []runtimeTypePathSegment
		expected string
		actual   string
	}{
		{
			name:     "record field",
			value:    runtimeMap(RuntimeEntry{Key: testSettingsName, Value: runtimeMap(RuntimeEntry{Key: parameterEnvoyMemory, Value: runtimeScalar(Integer)})}),
			typeExpr: ResolvedType{Fields: []TypeField{{Name: testSettingsName, Type: ResolvedType{Fields: []TypeField{{Name: parameterEnvoyMemory, Type: stringType}}}}}},
			path:     []runtimeTypePathSegment{{Value: testSettingsName}, {Value: parameterEnvoyMemory}}, expected: typeString, actual: "Int",
		},
		{
			name:     "list item",
			value:    runtimeMap(RuntimeEntry{Key: "racks", Value: runtimeList(runtimeScalar(Integer))}),
			typeExpr: ResolvedType{Fields: []TypeField{{Name: "racks", Type: ResolvedType{Name: typeList, Element: &stringType}}}},
			path:     []runtimeTypePathSegment{{Value: "racks"}, {Value: "0", Index: true}}, expected: typeString, actual: "Int",
		},
		{
			name:     "dynamic map key",
			value:    runtimeMap(RuntimeEntry{Key: "example.com/key", Value: runtimeScalar(Integer)}),
			typeExpr: ResolvedType{Name: typeMap, Element: &stringType},
			path:     []runtimeTypePathSegment{{Value: "example.com/key", MapKey: true}}, expected: typeString, actual: "Int",
		},
		{
			name:     "missing field",
			value:    runtimeMap(),
			typeExpr: ResolvedType{Fields: []TypeField{{Name: "storageSize", Type: stringType}}},
			path:     []runtimeTypePathSegment{{Value: "storageSize"}}, expected: typeString, actual: "missing",
		},
		{
			name:     "union",
			value:    runtimeScalar(Bool),
			typeExpr: ResolvedType{Options: []ResolvedType{stringType, intType}},
			expected: typeString + " | Int", actual: "Bool",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := checkRuntimeType(test.value, test.typeExpr, nil, make(map[string]bool))
			if err == nil {
				t.Fatal("checkRuntimeType() succeeded")
			}
			failure, ok := runtimeTypeFailureOf(err)
			if !ok || failure.Expected != test.expected || failure.Actual != test.actual {
				t.Fatalf("runtime type failure = %#v, want expected %q actual %q (error %v)", failure, test.expected, test.actual, err)
			}
			if len(failure.Path) != len(test.path) {
				t.Fatalf("runtime type path = %#v, want %#v", failure.Path, test.path)
			}
			for index := range test.path {
				if failure.Path[index] != test.path[index] {
					t.Fatalf("runtime type path = %#v, want %#v", failure.Path, test.path)
				}
			}
		})
	}

	unknownValue := runtimeMap(RuntimeEntry{Key: parameterEnvoyMemory, Value: runtimeScalar(String)}, RuntimeEntry{Key: "envoyMemroy", Value: runtimeScalar(String)})
	unknownType := ResolvedType{Fields: []TypeField{{Name: parameterEnvoyMemory, Type: stringType}}}
	err := checkRuntimeType(unknownValue, unknownType, nil, make(map[string]bool))
	var unknown unknownTypeField
	var ok bool
	unknown, ok = errors.AsType[unknownTypeField](err)
	if !ok || unknown.name != "envoyMemroy" {
		t.Fatalf("unknown field error = %v, want typo field metadata", err)
	}
}

func TestParameterUnknownFieldTracksNestedListAndMapPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modulePath := filepath.Join(root, "main.mcl")
	module := "type Storage = { capacity: String }\n" +
		"type Rack = { storage: Storage }\n" +
		"type Settings = { racks: List<Rack> }\n" +
		"param settings: Settings\nemit settings\n"
	if err := os.WriteFile(modulePath, []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	parameterPath := filepath.Join(root, "parameters.json")
	parameterJSON := `{"settings":{"racks":[{"storage":{"capacity":"2Gi","typo":"private-value"}}]}}`
	if err := os.WriteFile(parameterPath, []byte(parameterJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	parameters, sources, err := ParseParameterFile(parameterPath, []byte(parameterJSON))
	if err != nil {
		t.Fatalf("ParseParameterFile() error = %v", err)
	}
	_, err = EvaluateFileWithParametersAndOptionsContext(t.Context(), modulePath, parameters, EvaluationOptions{ParameterSources: sources})
	if err == nil {
		t.Fatal("EvaluateFileWithParametersAndOptionsContext() succeeded with an unknown nested field")
	}
	diagnostic, ok := SourceDiagnosticFromError(err)
	if !ok {
		t.Fatalf("SourceDiagnosticFromError(%v) = false", err)
	}
	keyStart := strings.Index(parameterJSON, `"typo"`)
	if diagnostic.Path != parameterPath || diagnostic.Span != (SourceSpan{Start: keyStart, End: keyStart + len(`"typo"`)}) {
		t.Fatalf("unknown field location = (%q, %+v), message %q; want key %q at %d-%d", diagnostic.Path, diagnostic.Span, diagnostic.Message, parameterPath, keyStart, keyStart+len(`"typo"`))
	}
	if !strings.Contains(diagnostic.Message, "pointer: /settings/racks/0/storage/typo") {
		t.Fatalf("unknown field message = %q, want complete nested JSON Pointer", diagnostic.Message)
	}
	if !diagnostic.OmitSourceSnippet {
		t.Fatal("parameter diagnostic should suppress source snippets")
	}
	if !slices.ContainsFunc(diagnostic.Related, func(location SourceDiagnosticLocation) bool {
		return filepath.Base(location.Path) == filepath.Base(modulePath)
	}) {
		t.Fatalf("unknown field related locations = %#v, want parameter declaration", diagnostic.Related)
	}
}

func TestParameterValidationResolvesNestedImportedAliases(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := filepath.Join(root, "config.mcl")
	config := "export type StorageSettings =\n  capacity: String\n" +
		"export type RackSettings =\n  name: String\n  storage: StorageSettings\n" +
		"export type Settings =\n  racks: List<RackSettings>\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	modulePath := filepath.Join(root, "main.mcl")
	module := "import \"./config.mcl\" as config\nparam settings: config.Settings\nemit settings\n"
	if err := os.WriteFile(modulePath, []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	parameterPath := filepath.Join(root, "parameters.json")
	parameterJSON := `{"settings":{"racks":[{"name":"rack-a","storage":{"capacity":"8Gi"}}]}}`
	if err := os.WriteFile(parameterPath, []byte(parameterJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	parameters, sources, err := ParseParameterFile(parameterPath, []byte(parameterJSON))
	if err != nil {
		t.Fatalf("ParseParameterFile() error = %v", err)
	}
	_, err = EvaluateFileWithParametersAndOptionsContext(t.Context(), modulePath, parameters, EvaluationOptions{ParameterSources: sources})
	if err != nil {
		t.Fatalf("EvaluateFileWithParametersAndOptionsContext() error = %v", err)
	}
	invalidJSON := `{"settings":{"racks":[{"name":"rack-a","storage":{"capacity":8}}]}}`
	if err := os.WriteFile(parameterPath, []byte(invalidJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	parameters, sources, err = ParseParameterFile(parameterPath, []byte(invalidJSON))
	if err != nil {
		t.Fatalf("ParseParameterFile() for invalid nested value error = %v", err)
	}
	_, err = EvaluateFileWithParametersAndOptionsContext(t.Context(), modulePath, parameters, EvaluationOptions{ParameterSources: sources})
	if err == nil {
		t.Fatal("EvaluateFileWithParametersAndOptionsContext() succeeded with an invalid nested imported alias value")
	}
	diagnostic, ok := SourceDiagnosticFromError(err)
	if !ok || !diagnostic.OmitSourceSnippet || diagnostic.Path != parameterPath || !strings.Contains(diagnostic.Message, "pointer: /settings/racks/0/storage/capacity") {
		t.Fatalf("nested imported alias diagnostic = %#v, %t, want suppressed parameter source snippet and full JSON Pointer", diagnostic, ok)
	}
}

func TestEvaluateFileReportsParameterFileValueLocation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	entryPath := filepath.Join(root, "main.mcl")
	entry := "type Settings = { envoyMemory: String }\nparam settings: Settings\nemit settings\n"
	if err := os.WriteFile(entryPath, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	parameterPath := filepath.Join(root, "deployment-parameters.json")
	parameterSource := "{\n  \"settings\": {\n    \"envoyMemory\": 64\n  }\n}\n"
	if err := os.WriteFile(parameterPath, []byte(parameterSource), 0o600); err != nil {
		t.Fatal(err)
	}
	parameters, sources, err := ParseParameterFile(parameterPath, []byte(parameterSource))
	if err != nil {
		t.Fatal(err)
	}
	_, err = EvaluateFileWithParametersAndOptionsContext(t.Context(), entryPath, parameters, EvaluationOptions{ParameterSources: sources})
	if err == nil {
		t.Fatal("EvaluateFileWithParametersAndOptionsContext() succeeded with a nested type mismatch")
	}
	diagnostic, ok := SourceDiagnosticFromError(err)
	if !ok {
		t.Fatalf("SourceDiagnosticFromError(%v) = false", err)
	}
	valueStart := strings.Index(parameterSource, "64")
	if diagnostic.Path != parameterPath || diagnostic.Span != (SourceSpan{Start: valueStart, End: valueStart + 2}) {
		t.Fatalf("diagnostic source = (%q, %+v), want (%q, %d-%d)", diagnostic.Path, diagnostic.Span, parameterPath, valueStart, valueStart+2)
	}
	for _, want := range []string{"path: settings.envoyMemory", "pointer: /settings/envoyMemory", "expected: String", "actual: Int", "source: " + parameterPath} {
		if !strings.Contains(diagnostic.Message, want) {
			t.Errorf("diagnostic message %q does not contain %q", diagnostic.Message, want)
		}
	}
	if len(diagnostic.Related) != 1 || filepath.Base(diagnostic.Related[0].Path) != filepath.Base(entryPath) {
		t.Fatalf("diagnostic related locations = %#v, want the MCL parameter declaration", diagnostic.Related)
	}
}

func TestParameterTypeDiagnosticDoesNotExposeLiteralValue(t *testing.T) {
	t.Parallel()
	declaration := &LetDeclaration{Name: "role", SourcePath: "main.mcl", NameSpan: SourceSpan{Start: 5, End: 9}}
	expected := ResolvedType{Name: typeString, StringLiteral: "admin", StringLiteralKnown: true}
	cause := &runtimeTypeFailure{Expected: `string "admin"`, Actual: "String", Message: `expected string "admin", got "private-value"`}
	diagnostic := parameterTypeDiagnostic(declaration, expected, runtimeScalar(String), ParameterSource{}, false, cause)
	if strings.Contains(diagnostic.Error(), "private-value") {
		t.Fatalf("parameter diagnostic leaked input value: %q", diagnostic.Error())
	}
}

func runtimeMap(entries ...RuntimeEntry) RuntimeValue {
	return RuntimeValue{Value: runtimeValueData{Kind: Map}, Map: entries}
}

func runtimeList(values ...RuntimeValue) RuntimeValue {
	return RuntimeValue{Value: runtimeValueData{Kind: List}, List: values}
}

func runtimeScalar(kind Kind) RuntimeValue {
	return RuntimeValue{Value: runtimeValueData{Kind: kind}}
}

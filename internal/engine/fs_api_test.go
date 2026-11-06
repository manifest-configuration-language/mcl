package engine

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/manifest-configuration-language/mcl/internal/content"
)

const projectFileEntrypoint = "main.mcl"

const (
	testLockFilename      = "mcl.lock"
	testLockRevisionField = "revision"
)

func TestEvaluateProjectSourceUsesRootRelativeFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		"child.mcl": &fstest.MapFile{Data: []byte("export type Child = String")},
	}

	bundle, loaded, err := EvaluateProjectSourceWithDependenciesContext(t.Context(), root, projectFileEntrypoint, `import "./child.mcl" as child`, files, nil)
	if err != nil {
		t.Fatalf("evaluate project source: %v", err)
	}
	if len(bundle.Artifacts) != 0 {
		t.Fatalf("got %d artifacts, want none", len(bundle.Artifacts))
	}
	if !slices.Contains(loaded, filepath.Join(root, "child.mcl")) {
		t.Fatalf("loaded modules = %v, want child module %q", loaded, filepath.Join(root, "child.mcl"))
	}
}

func TestEvaluateProjectSourceReadsDependencyLockFromFilesystem(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		testLockFilename: &fstest.MapFile{Data: []byte(`{"version":5}`)},
	}

	_, err := EvaluateProjectSourceContext(t.Context(), filepath.Join(string(filepath.Separator), "virtual-project"), projectFileEntrypoint, "", files, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported dependency lock version 5") {
		t.Fatalf("evaluate with virtual lock: got %v, want unsupported lock version", err)
	}
}

func TestEvaluateProjectSourceLoadsCRDCatalogOnlyForUnrecognizedResources(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := lockedHTTPSCRDFiles(t, []byte("apiVersion: [\n"))
	builtinResource := `let resource =
  apiVersion: "v1"
  kind: "ConfigMap"
  metadata:
    name: "sample"
emit resource
`
	if _, err := EvaluateProjectSourceContext(t.Context(), root, projectFileEntrypoint, builtinResource, files, nil); err != nil {
		t.Fatalf("EvaluateProjectSourceContext() for built-in resource error = %v, want unused remote CRD YAML to remain unread", err)
	}

	customResource := `let resource =
  apiVersion: "example.test/v1"
  kind: "Widget"
  metadata:
    name: "sample"
emit resource
`
	_, err := EvaluateProjectSourceContext(t.Context(), root, projectFileEntrypoint, customResource, files, nil)
	if err == nil || !strings.Contains(err.Error(), "decode CRD schema") {
		t.Fatalf("EvaluateProjectSourceContext() for custom resource error = %v, want malformed configured CRD YAML to be loaded", err)
	}
}

func TestEvaluateProjectSourceValidatesAgainstConfiguredCRDSchema(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	crd := []byte(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.test
spec:
  group: example.test
  scope: Namespaced
  names:
    plural: widgets
    singular: widget
    kind: Widget
    listKind: WidgetList
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            apiVersion:
              type: string
            kind:
              type: string
            metadata:
              type: object
            spec:
              type: object
              properties:
                size:
                  type: integer
`)
	files := lockedHTTPSCRDFiles(t, crd)
	for _, test := range []struct {
		name      string
		size      string
		wantError bool
	}{
		{name: "valid resource", size: "2"},
		{name: "invalid field type", size: `"large"`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := "let widget =\n" +
				"  apiVersion: \"example.test/v1\"\n" +
				"  kind: \"Widget\"\n" +
				"  metadata:\n" +
				"    name: \"sample\"\n" +
				"  spec:\n" +
				"    size: " + test.size + "\n" +
				"emit widget\n"
			_, err := EvaluateProjectSourceContext(t.Context(), root, projectFileEntrypoint, source, files, nil)
			if test.wantError && (err == nil || !strings.Contains(err.Error(), "must be of type integer")) {
				t.Fatalf("EvaluateProjectSourceContext() error = %v, want a CRD schema type error", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("EvaluateProjectSourceContext() error = %v, want configured CRD schema validation to pass", err)
			}
		})
	}
}

func TestEvaluateProjectSourceDoesNotValidateKubernetesObjectMetaForCRD(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	crd := []byte(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.test
spec:
  group: example.test
  scope: Namespaced
  names:
    plural: widgets
    singular: widget
    kind: Widget
    listKind: WidgetList
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            apiVersion:
              type: string
            kind:
              type: string
            metadata:
              type: object
            spec:
              type: object
`)
	files := lockedHTTPSCRDFiles(t, crd)
	validSource := `let widget =
  apiVersion: "example.test/v1"
  kind: "Widget"
  metadata:
    name: "sample"
emit widget
`
	if _, err := EvaluateProjectSourceContext(t.Context(), root, projectFileEntrypoint, validSource, files, nil); err != nil {
		t.Fatalf("EvaluateProjectSourceContext() error = %v, want a valid namespaced CRD resource with omitted namespace to pass", err)
	}

	customMetadataSource := `let widget =
  apiVersion: "example.test/v1"
  kind: "Widget"
  metadata:
    name: "Invalid_Name"
emit widget
`
	customMetadataBundle, err := EvaluateProjectSourceContext(t.Context(), root, projectFileEntrypoint, customMetadataSource, files, nil)
	if err != nil {
		t.Fatalf("EvaluateProjectSourceContext() error = %v, want CRD metadata.name to remain governed by its schema", err)
	}
	if _, err := Render(customMetadataBundle); err != nil {
		t.Fatalf("Render() error = %v, want CRD metadata.name to remain governed by its schema", err)
	}

	unknownFieldSource := `let widget =
  apiVersion: "example.test/v1"
  kind: "Widget"
  metadata:
    name: "sample"
  spec:
    replcias: 3
emit widget
`
	_, err = EvaluateProjectSourceContext(t.Context(), root, projectFileEntrypoint, unknownFieldSource, files, nil)
	if err == nil {
		t.Fatal("EvaluateProjectSourceContext() succeeded, want an unknown CRD field to fail")
	}
	diagnostic, ok := errors.AsType[SourceDiagnosticError](err)
	if !ok {
		t.Fatalf("EvaluateProjectSourceContext() error = %T, want SourceDiagnosticError", err)
	}
	if diagnostic.Code != "MCL-CRD" {
		t.Errorf("diagnostic code = %q, want MCL-CRD", diagnostic.Code)
	}
	if diagnostic.Span.Start < 0 || diagnostic.Span.End > len(unknownFieldSource) || !strings.Contains(unknownFieldSource[diagnostic.Span.Start:diagnostic.Span.End], "3") {
		t.Errorf("diagnostic span = %#v, want the unknown field value", diagnostic.Span)
	}
	if !strings.Contains(diagnostic.Message, "replcias") {
		t.Errorf("diagnostic message = %q, want the unknown spec field", diagnostic.Message)
	}
}

func TestEvaluateProjectSourceRequiresResourceIdentityForConfiguredCRD(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	crd := []byte(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.test
spec:
  group: example.test
  scope: Namespaced
  names:
    plural: widgets
    singular: widget
    kind: Widget
    listKind: WidgetList
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
`)
	files := lockedHTTPSCRDFiles(t, crd)
	resource := `let widget =
  apiVersion: "example.test/v1"
  kind: "Widget"
emit widget
`
	if _, err := EvaluateProjectSourceContext(t.Context(), root, projectFileEntrypoint, resource, files, nil); err == nil || !strings.Contains(err.Error(), "invalid Kubernetes resource identity") {
		t.Fatalf("EvaluateProjectSourceContext() error = %v, want the generic resource identity check to require metadata.name", err)
	}
}

func lockedHTTPSCRDFiles(t *testing.T, crd []byte) fstest.MapFS {
	t.Helper()
	source := "https://schemas.example.test/widget.yaml"
	digest := content.SHA256(crd)
	identity := dependencyToolchainIdentity()
	lockData, err := json.Marshal(map[string]any{
		"version": 3,
		"toolchain": map[string]string{
			"mcl": identity.MCL, testLockRevisionField: identity.Revision, "modified": identity.Modified,
			"go": identity.Go, "helm": identity.Helm, "kustomizeAPI": identity.KustomizeAPI, "kustomizeKYAML": identity.KustomizeKYAML,
		},
		"schemas": map[string]string{source: digest},
	})
	if err != nil {
		t.Fatal(err)
	}

	return fstest.MapFS{
		projectConfigFileName: {Data: []byte("[[crd_schemas]]\nsource = \"" + source + "\"\n")},
		testLockFilename:      {Data: lockData},
		filepath.ToSlash(filepath.Join(".mcl", "cache", "sha256", digest)): {Data: crd},
	}
}

func TestEvaluateProjectFileUsesRootRelativeFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		projectFileEntrypoint: &fstest.MapFile{Data: []byte("import \"./child.mcl\" as child\nemit child.value\n")},
		"child.mcl":           &fstest.MapFile{Data: []byte("export let value = \"project file\"\n")},
	}

	bundle, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, files)
	if err != nil {
		t.Fatalf("evaluate project file: %v", err)
	}
	if len(bundle.Artifacts) != 1 || bundle.Artifacts[0].Value.Kind != String || bundle.Artifacts[0].Value.Text != "project file" {
		t.Fatalf("project file bundle = %#v, want one imported string artifact", bundle.Artifacts)
	}
}

func TestEvaluateProjectFileLoadsManifestFromRootRelativeFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		projectFileEntrypoint:     &fstest.MapFile{Data: []byte("let resources = manifest.load(\"./fixtures/resources.yaml\")\nemit resources\n")},
		"fixtures/resources.yaml": &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: settings\n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: api\n")},
	}

	bundle, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, files)
	if err != nil {
		t.Fatalf("evaluate project file with virtual Manifest: %v", err)
	}
	if len(bundle.Artifacts) != 2 {
		t.Fatalf("project file bundle has %d artifacts, want 2 Manifest documents", len(bundle.Artifacts))
	}
	for _, artifact := range bundle.Artifacts {
		if artifact.Lifecycle.Origin != "fixtures/resources.yaml" {
			t.Fatalf("Manifest origin = %q, want root-relative source path", artifact.Lifecycle.Origin)
		}
	}
}

func TestEvaluateProjectFileLoadsOneManifestDocument(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		projectFileEntrypoint: &fstest.MapFile{Data: []byte("import \"std:manifest\" as manifest\nemit manifest.loadOne(\"./fixtures/one.yaml\")\n")},
		"fixtures/one.yaml":   &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: settings\n")},
	}

	bundle, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, files)
	if err != nil {
		t.Fatalf("evaluate project file with one Manifest: %v", err)
	}
	if len(bundle.Artifacts) != 1 || bundle.Artifacts[0].Value.Kind != Map {
		t.Fatalf("project file bundle = %#v, want one Map artifact", bundle.Artifacts)
	}
	if got, want := bundle.Artifacts[0].Lifecycle.Origin, "fixtures/one.yaml"; got != want {
		t.Fatalf("Manifest origin = %q, want %q", got, want)
	}
}

func TestEvaluateProjectFileLoadOneRequiresExactlyOneDocument(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{name: "zero", data: "", want: "got 0"},
		{name: "multiple", data: "kind: ConfigMap\n---\nkind: Service\n", want: "got 2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			files := fstest.MapFS{
				projectFileEntrypoint: &fstest.MapFile{Data: []byte("import \"std:manifest\" as manifest\nemit manifest.loadOne(\"input.yaml\")\n")},
				"input.yaml":          &fstest.MapFile{Data: []byte(test.data)},
			}
			_, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, files)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("evaluate project file with %s Manifest: error = %v, want %q", test.name, err, test.want)
			}
		})
	}
}

func TestEvaluateProjectFileMapsKustomizeOriginForRootRelativeFilesystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	files := fstest.MapFS{
		projectFileEntrypoint:           &fstest.MapFile{Data: []byte("let resources = kustomize.build(\"fixtures\", \"\", \"base\")\nemit resources\n")},
		"fixtures/base/Kustomization":   &fstest.MapFile{Data: []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - deployment.yaml\n")},
		"fixtures/base/deployment.yaml": &fstest.MapFile{Data: []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: sample\nspec:\n  selector:\n    matchLabels:\n      app: sample\n  template:\n    metadata:\n      labels:\n        app: sample\n    spec:\n      containers:\n        - name: sample\n          image: busybox\n")},
	}

	bundle, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, files)
	if err != nil {
		t.Fatalf("evaluate project file with virtual Kustomize source: %v", err)
	}
	if len(bundle.Artifacts) != 1 {
		t.Fatalf("project file bundle has %d artifacts, want one Kustomize resource", len(bundle.Artifacts))
	}
	if got, want := bundle.Artifacts[0].Lifecycle.Origin, "kustomize:fixtures/base/deployment.yaml"; got != want {
		t.Fatalf("Kustomize origin = %q, want %q", got, want)
	}
}

func TestEvaluateProjectFileRejectsManifestSymlinkAndPathEscape(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "virtual-project")
	for _, test := range []struct {
		name   string
		files  fstest.MapFS
		wanted error
	}{
		{
			name: "symlink",
			files: fstest.MapFS{
				projectFileEntrypoint: &fstest.MapFile{Data: []byte("emit manifest.load(\"linked.yaml\")\n")},
				"linked.yaml":         &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("outside.yaml")},
				"outside.yaml":        &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: ConfigMap\n")},
			},
			wanted: fs.ErrPermission,
		},
		{
			name: "path escape",
			files: fstest.MapFS{
				projectFileEntrypoint: &fstest.MapFile{Data: []byte("emit manifest.load(\"../../outside.yaml\")\n")},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, test.files)
			if err == nil {
				t.Fatal("evaluate project file: got no error, want Manifest source rejection")
			}
			if test.wanted != nil && !errors.Is(err, test.wanted) {
				t.Fatalf("evaluate project file: got %v, want %v", err, test.wanted)
			}
		})
	}
}

func TestEvaluateProjectFileRejectsFileSymlinkOutsideRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	externalFile := filepath.Join(t.TempDir(), "external.mcl")
	if err := os.WriteFile(externalFile, []byte("emit \"outside\"\n"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	if err := os.Symlink(externalFile, filepath.Join(root, projectFileEntrypoint)); err != nil {
		t.Skipf("create file symlink: %v", err)
	}

	_, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, os.DirFS(root))
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("evaluate symlinked project file: got %v, want permission error", err)
	}
}

func TestEvaluateProjectFileRejectsDirectorySymlinkOutsideRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	externalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, projectFileEntrypoint), []byte("import \"./linked/child.mcl\" as child\nemit child.value\n"), 0o600); err != nil {
		t.Fatalf("write project entrypoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalDir, "child.mcl"), []byte("export let value = \"outside\"\n"), 0o600); err != nil {
		t.Fatalf("write external module: %v", err)
	}
	if err := os.Symlink(externalDir, filepath.Join(root, "linked")); err != nil {
		t.Skipf("create directory symlink: %v", err)
	}

	_, err := EvaluateProjectFileContext(t.Context(), root, projectFileEntrypoint, os.DirFS(root))
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("evaluate import through directory symlink: got %v, want permission error", err)
	}
}

func TestEvaluateProjectFileReadsDependencyLockFromFilesystem(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		projectFileEntrypoint: &fstest.MapFile{Data: []byte("emit \"ok\"\n")},
		testLockFilename:      &fstest.MapFile{Data: []byte(`{"version":5}`)},
	}

	_, err := EvaluateProjectFileContext(t.Context(), filepath.Join(string(filepath.Separator), "virtual-project"), projectFileEntrypoint, files)
	if err == nil || !strings.Contains(err.Error(), "unsupported dependency lock version 5") {
		t.Fatalf("evaluate project file with virtual lock: got %v, want unsupported lock version", err)
	}
}

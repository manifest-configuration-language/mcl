package engine

import (
	"encoding/json/v2"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/manifest-configuration-language/mcl/internal/content"
	"github.com/manifest-configuration-language/mcl/internal/dependency"
)

func TestProjectCRDCatalogEnforcesResourceScopeAndIdentity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name              string
		scope             string
		resources         string
		wantError         string
		wantEvaluateError bool
	}{
		{
			name:  "cluster scoped resources reject namespace",
			scope: "Cluster",
			resources: `let resource =
  apiVersion: "example.test/v1"
  kind: "Widget"
  metadata:
    name: "sample"
    namespace: "default"
emit resource
`,
			wantError:         "not allowed on this type",
			wantEvaluateError: true,
		},
		{
			name:  "namespaced identity normalizes default namespace",
			scope: "Namespaced",
			resources: `let implicitNamespace =
  apiVersion: "example.test/v1"
  kind: "Widget"
  metadata:
    name: "sample"
let explicitNamespace =
  apiVersion: "example.test/v1"
  kind: "Widget"
  metadata:
    name: "sample"
    namespace: "default"
emit implicitNamespace
emit explicitNamespace
`,
			wantError: "duplicate Kubernetes resource",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(string(filepath.Separator), "virtual-project")
			source := "https://schemas.example.test/widget.yaml"
			crd := strings.Replace(projectCRD("example.test", "Widget", "widgets"), "scope: Namespaced", "scope: "+test.scope, 1)
			identity := dependencyToolchainIdentity()
			lockData, err := json.Marshal(map[string]any{
				"version": 3,
				"toolchain": map[string]string{
					"mcl": identity.MCL, testLockRevisionField: identity.Revision, "modified": identity.Modified,
					"go": identity.Go, "helm": identity.Helm, "kustomizeAPI": identity.KustomizeAPI, "kustomizeKYAML": identity.KustomizeKYAML,
				},
				"schemas": map[string]string{source: content.SHA256([]byte(crd))},
			})
			if err != nil {
				t.Fatal(err)
			}
			files := fstest.MapFS{
				projectConfigFileName: {Data: []byte(fmt.Sprintf("[[crd_schemas]]\nsource = %q\n", source))},
				testLockFilename:      {Data: lockData},
				filepath.ToSlash(filepath.Join(".mcl", "cache", "sha256", content.SHA256([]byte(crd)))): {Data: []byte(crd)},
			}
			if _, err := dependency.OpenWithFS(t.Context(), root, false, identity, files); err != nil {
				t.Fatalf("open dependency lock: %v", err)
			}
			bundle, err := EvaluateProjectSourceContext(t.Context(), root, projectFileEntrypoint, test.resources, files, nil)
			if test.wantEvaluateError {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("EvaluateProjectSourceContext() error = %v, want %q", err, test.wantError)
				}

				return
			}
			if err != nil {
				t.Fatalf("EvaluateProjectSourceContext() error = %v", err)
			}
			_, err = Render(bundle)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Render() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestRegisterRuntimeCRDRejectsDescriptorConflictsInEitherOrder(t *testing.T) {
	t.Parallel()
	namespaced := projectCRD("example.test", "Widget", "widgets")
	cluster := strings.Replace(namespaced, "scope: Namespaced", "scope: Cluster", 1)
	pluralMismatch := strings.Replace(namespaced, "widgets.example.test", "gadgets.example.test", 1)
	pluralMismatch = strings.Replace(pluralMismatch, "plural: widgets", "plural: gadgets", 1)
	for _, variant := range []string{cluster, pluralMismatch} {
		for _, ordered := range [][]string{{namespaced, variant}, {variant, namespaced}} {
			schemas := make(map[customResourceDefinitionKey]crdVersionSchema)
			for index, document := range ordered {
				documents, err := decodeRuntimeYAMLDocuments([]byte(document), "crd.yaml")
				if err != nil {
					t.Fatal(err)
				}
				err = registerRuntimeCustomResourceDefinition(t.Context(), schemas, documents.List[0])
				if index == 0 && err != nil {
					t.Fatalf("registerRuntimeCustomResourceDefinition() for first CRD: %v", err)
				}
				if index == 1 && (err == nil || !strings.Contains(err.Error(), "conflicting schemas")) {
					t.Fatalf("registerRuntimeCustomResourceDefinition() error = %v, want descriptor conflict", err)
				}
			}
		}
	}
}

func TestRegisterCatalogCRDRejectsDescriptorConflictsWithoutCompilation(t *testing.T) {
	t.Parallel()
	namespaced := projectCRD("example.test", "Widget", "widgets")
	cluster := strings.Replace(namespaced, "scope: Namespaced", "scope: Cluster", 1)
	for _, ordered := range [][]string{{namespaced, cluster}, {cluster, namespaced}} {
		schemas := make(map[customResourceDefinitionKey]crdVersionSchema)
		for index, document := range ordered {
			documents, err := decodeRuntimeYAMLDocuments([]byte(document), "crd.yaml")
			if err != nil {
				t.Fatal(err)
			}
			err = registerCatalogCustomResourceDefinition(schemas, documents.List[0])
			if index == 0 && err != nil {
				t.Fatalf("registerCatalogCustomResourceDefinition() for first CRD: %v", err)
			}
			if index == 1 && (err == nil || !strings.Contains(err.Error(), "conflicting schemas")) {
				t.Fatalf("registerCatalogCustomResourceDefinition() error = %v, want descriptor conflict", err)
			}
		}
		for _, schema := range schemas {
			if schema.validator != nil || schema.structural != nil || schema.cel != nil {
				t.Fatalf("Catalog schema was compiled during conflict detection: %#v", schema)
			}
		}
	}
}

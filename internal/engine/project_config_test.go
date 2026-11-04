package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json/v2"
	"fmt"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/manifest-configuration-language/mcl/internal/content"
	"github.com/manifest-configuration-language/mcl/internal/dependency"
)

func TestLoadCRDSchemaCatalogUsesLockedHTTPSGitAndOCIInputs(t *testing.T) {
	t.Parallel()

	root := filepath.Join(string(filepath.Separator), "virtual-project")
	httpsSource := "https://schemas.example.test/widget.yaml"
	gitSource := "git+https://github.com/example/operator.git"
	gitRevision := "0123456789abcdef0123456789abcdef01234567"
	gitDirectory := "config/crd/bases"
	ociSource := "oci://ghcr.io/example/operator-chart"
	ociVersion := "1.2.3"
	httpsCRD := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ignored\n---\n" + projectCRD("https.example.test", "HTTPSWidget", "httpswidgets")
	gitCRD := projectCRD("git.example.test", "GitWidget", "gitwidgets")
	gitSecondCRD := projectCRD("git.example.test", "GitGadget", "gitgadgets")
	ociCRD := projectCRD("oci.example.test", "OCIWidget", "ociwidgets")
	gitArchive := projectCRDArchive(t, map[string][]byte{"widget.yaml": []byte(gitCRD), "gadget.yaml": []byte(gitSecondCRD)})
	ociArchive := projectHelmChartArchive(t, "operator-chart", ociVersion, map[string][]byte{"widget.yaml": []byte(ociCRD)})
	gitDigest := content.SHA256(gitArchive)
	ociDigest := content.SHA256(ociArchive)
	identity := dependencyToolchainIdentity()
	lockData, err := json.Marshal(map[string]any{
		"version": 3,
		"toolchain": map[string]string{
			"mcl": identity.MCL, testLockRevisionField: identity.Revision, "modified": identity.Modified,
			"go": identity.Go, "helm": identity.Helm, "kustomizeAPI": identity.KustomizeAPI, "kustomizeKYAML": identity.KustomizeKYAML,
		},
		"schemas": map[string]string{httpsSource: content.SHA256([]byte(httpsCRD))},
		"gitSources": map[string]any{
			dependency.GitSourceKey(gitSource, gitRevision, gitDirectory): map[string]string{
				"source": gitSource, "revision": gitRevision, "directory": gitDirectory, "archiveDigest": gitDigest,
			},
		},
		"ociCharts": map[string]any{
			ociSource: map[string]any{
				ociVersion: map[string]any{
					"manifestDigest": "sha256:" + content.SHA256([]byte("manifest")), "manifestSize": int64(len("manifest")),
					"chartLayerDigest": "sha256:" + ociDigest, "chartLayerSize": int64(len(ociArchive)), "archiveDigest": ociDigest,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		projectConfigFileName: {Data: []byte(fmt.Sprintf(`[[crd_schemas]]
source = %q

[[crd_schemas]]
source = %q
revision = %q
directory = %q
files = ["widget.yaml"]

[[crd_schemas]]
source = %q
revision = %q
directory = %q
files = ["gadget.yaml"]

[[crd_schemas]]
source = %q
version = %q
files = ["widget.yaml"]
`, httpsSource, gitSource, gitRevision, gitDirectory, gitSource, gitRevision, gitDirectory, ociSource, ociVersion))},
		testLockFilename: {Data: lockData},
		filepath.ToSlash(filepath.Join(".mcl", "cache", "sha256", content.SHA256([]byte(httpsCRD)))): {Data: []byte(httpsCRD)},
		filepath.ToSlash(filepath.Join(".mcl", "cache", "sha256", gitDigest)):                        {Data: gitArchive},
		filepath.ToSlash(filepath.Join(".mcl", "cache", "sha256", ociDigest)):                        {Data: ociArchive},
	}
	dependencies, err := dependency.OpenWithFS(t.Context(), root, false, identity, files)
	if err != nil {
		t.Fatalf("open dependency lock: %v", err)
	}
	catalog, err := loadCRDSchemaCatalog(t.Context(), newProjectFilesystem(root, files), dependencies)
	if err != nil {
		t.Fatalf("load CRD schema catalog: %v", err)
	}
	for _, key := range []customResourceDefinitionKey{
		{group: "https.example.test", version: "v1", kind: "HTTPSWidget"},
		{group: "git.example.test", version: "v1", kind: "GitWidget"},
		{group: "git.example.test", version: "v1", kind: "GitGadget"},
		{group: "oci.example.test", version: "v1", kind: "OCIWidget"},
	} {
		if _, exists := catalog[key]; !exists {
			t.Errorf("CRD schema catalog is missing %s/%s/%s", key.group, key.version, key.kind)
		}
	}
}

func TestGroupCRDSchemaSourcesRejectsAllAndPartialFileSelection(t *testing.T) {
	t.Parallel()
	source := projectCRDSchemaSource{Source: "oci://example.org/operator", Version: "1.0.0"}
	for _, sources := range [][]indexedCRDSchemaSource{
		{
			{index: 1, source: source},
			{index: 2, source: projectCRDSchemaSource{Source: source.Source, Version: source.Version, Files: []string{"widgets.yaml"}}},
		},
		{
			{index: 1, source: projectCRDSchemaSource{Source: source.Source, Version: source.Version, Files: []string{"widgets.yaml"}}},
			{index: 2, source: source},
		},
	} {
		if _, err := groupCRDSchemaSources(sources); err == nil {
			t.Fatal("groupCRDSchemaSources() succeeded, want mixed all-file and selected-file declarations to fail")
		}
	}
}

func projectCRD(group, kind, plural string) string {
	return fmt.Sprintf(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: %s.%s
spec:
  group: %s
  scope: Namespaced
  names:
    plural: %s
    singular: widget
    kind: %s
    listKind: %sList
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
`, plural, group, group, plural, kind, kind)
}

func projectCRDArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for name, data := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return archive.Bytes()
}

func projectHelmChartArchive(t *testing.T, name, version string, crds map[string][]byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	writer := tar.NewWriter(compressed)
	files := map[string][]byte{
		name + "/Chart.yaml": []byte(fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", name, version)),
	}
	for path, data := range crds {
		files[name+"/crds/"+path] = data
	}
	for path, data := range files {
		if err := writer.WriteHeader(&tar.Header{Name: path, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}

	return archive.Bytes()
}

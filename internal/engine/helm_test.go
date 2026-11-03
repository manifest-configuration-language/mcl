package engine

import (
	"strings"
	"testing"

	"github.com/manifest-configuration-language/mcl/internal/helmrender"
)

func TestHelmLookupResourceDescriptorsUseResourceCatalogAndCRDs(t *testing.T) {
	t.Parallel()
	artifacts := make([]Artifact, 0, 4)
	artifacts = append(artifacts,
		Artifact{Value: testHelmResource("v1", "ConfigMap", map[string]Value{"name": {Kind: String, Text: "settings"}})},
		Artifact{Value: testHelmResource("v1", "Namespace", map[string]Value{"name": {Kind: String, Text: testNamespace}})},
		Artifact{Value: testHelmResource("apiextensions.k8s.io/v1", "CustomResourceDefinition", map[string]Value{"name": {Kind: String, Text: "samples.example.test"}})},
	)
	artifacts[2].Value.Map = append(artifacts[2].Value.Map,
		Entry{Key: "spec", Value: Value{Kind: Map, Map: []Entry{
			{Key: "group", Value: Value{Kind: String, Text: "example.test"}},
			{Key: "scope", Value: Value{Kind: String, Text: "Namespaced"}},
			{Key: "names", Value: Value{Kind: Map, Map: []Entry{
				{Key: "kind", Value: Value{Kind: String, Text: "Sample"}},
				{Key: "plural", Value: Value{Kind: String, Text: "samples"}},
			}}},
		}}},
	)
	customResource := testHelmResource("example.test/v1", "Sample", map[string]Value{
		"name": {Kind: String, Text: "sample"}, namespaceFieldName: {Kind: String, Text: testNamespace},
	})
	artifacts = append(artifacts, Artifact{Value: customResource})

	descriptors, err := helmLookupResourceDescriptors(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]helmrender.LookupResourceScope, len(descriptors))
	for _, descriptor := range descriptors {
		got[descriptor.APIGroup+"/"+descriptor.Kind] = descriptor.Scope
	}
	for key, want := range map[string]helmrender.LookupResourceScope{
		"/ConfigMap":          helmrender.LookupResourceScopeNamespaced,
		"/Namespace":          helmrender.LookupResourceScopeCluster,
		"example.test/Sample": helmrender.LookupResourceScopeNamespaced,
		"apiextensions.k8s.io/CustomResourceDefinition": helmrender.LookupResourceScopeCluster,
	} {
		if got[key] != want {
			t.Errorf("scope for %s = %q, want %q", key, got[key], want)
		}
	}
}

func TestHelmLookupResourceDescriptorsRejectUnknownScopeWithoutNamespace(t *testing.T) {
	t.Parallel()
	artifact := Artifact{Value: testHelmResource("custom.test/v1", "Widget", map[string]Value{"name": {Kind: String, Text: "widget"}})}

	_, err := helmLookupResourceDescriptors([]Artifact{artifact})
	if err == nil || !strings.Contains(err.Error(), "unknown namespace scope") {
		t.Fatalf("helmLookupResourceDescriptors() error = %v, want unknown-scope error", err)
	}
}

func TestHelmLookupResourceDescriptorsRejectDuplicateIdentity(t *testing.T) {
	t.Parallel()
	resource := testHelmResource("v1", "ConfigMap", map[string]Value{
		"name": {Kind: String, Text: "settings"}, namespaceFieldName: {Kind: String, Text: testNamespace},
	})

	_, err := helmLookupResourceDescriptors([]Artifact{{Value: resource}, {Value: resource}})
	if err == nil || !strings.Contains(err.Error(), "duplicate Kubernetes resource") {
		t.Fatalf("helmLookupResourceDescriptors() error = %v, want duplicate identity error", err)
	}
}

func testHelmResource(apiVersion, kind string, metadata map[string]Value) Value {
	entries := make([]Entry, 0, 3)
	entries = append(entries,
		Entry{Key: "apiVersion", Value: Value{Kind: String, Text: apiVersion}},
		Entry{Key: "kind", Value: Value{Kind: String, Text: kind}},
	)
	metadataEntries := make([]Entry, 0, len(metadata))
	for key, value := range metadata {
		metadataEntries = append(metadataEntries, Entry{Key: key, Value: value})
	}
	entries = append(entries, Entry{Key: "metadata", Value: Value{Kind: Map, Map: metadataEntries}})

	return Value{Kind: Map, Map: entries}
}

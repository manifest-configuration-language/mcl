package engine

import (
	"strings"
	"testing"
)

const metadataFieldName = "metadata"

func TestRejectDuplicateKubernetesResourcesPreservesClusterNamespaceInput(t *testing.T) {
	t.Parallel()

	resource := Value{Kind: Map, Map: []Entry{
		{Key: "apiVersion", Value: Value{Kind: String, Text: "v1"}},
		{Key: "kind", Value: Value{Kind: String, Text: "Namespace"}},
		{Key: metadataFieldName, Value: Value{Kind: Map, Map: []Entry{
			{Key: "name", Value: Value{Kind: String, Text: "production"}},
			{Key: "namespace", Value: Value{Kind: String, Text: "incorrect"}},
		}}},
	}}

	_, _, err := validatedKubernetesResourceIdentityOf(resource)
	if err != nil {
		t.Fatalf("validatedKubernetesResourceIdentityOf() error = %v", err)
	}

	err = rejectDuplicateKubernetesResources([]Artifact{{Value: resource}}, builtinKubernetesResourceDescriptors)
	if err == nil || !strings.Contains(err.Error(), "must not set metadata.namespace") {
		t.Fatalf("rejectDuplicateKubernetesResources() error = %v, want cluster scope namespace diagnostic", err)
	}
}

func TestNormalizeKubernetesResourceIdentityAfterValidation(t *testing.T) {
	t.Parallel()

	resource := Value{Kind: Map, Map: []Entry{
		{Key: "apiVersion", Value: Value{Kind: String, Text: "v1"}},
		{Key: "kind", Value: Value{Kind: String, Text: "ConfigMap"}},
		{Key: metadataFieldName, Value: Value{Kind: Map, Map: []Entry{
			{Key: "name", Value: Value{Kind: String, Text: "settings"}},
		}}},
	}}

	identity, ok := kubernetesResourceIdentityOf(resource)
	if !ok || identity.namespace != "default" {
		t.Fatalf("kubernetesResourceIdentityOf() = %#v, %t; want default namespace", identity, ok)
	}
	if identity.rawNamespace != "" {
		t.Fatalf("kubernetesResourceIdentityOf() raw namespace = %q, want cleared canonical identity", identity.rawNamespace)
	}

	explicitDefault := resource
	explicitDefault.Map = append([]Entry(nil), resource.Map...)
	explicitDefault.Map[2].Value.Map = append([]Entry(nil), resource.Map[2].Value.Map...)
	explicitDefault.Map[2].Value.Map = append(explicitDefault.Map[2].Value.Map, Entry{Key: "namespace", Value: Value{Kind: String, Text: defaultKubernetesNamespace}})
	err := rejectDuplicateKubernetesResources([]Artifact{{Value: resource}, {Value: explicitDefault}}, builtinKubernetesResourceDescriptors)
	if err == nil || !strings.Contains(err.Error(), "duplicate Kubernetes resource") {
		t.Fatalf("rejectDuplicateKubernetesResources() error = %v, want duplicate after namespace normalization", err)
	}
}

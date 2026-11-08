package engine

import (
	"fmt"
	"strings"
	"testing"
)

const (
	metadataFieldName  = "metadata"
	namespaceFieldName = "namespace"
	testSettingsName   = "settings"
	testNamespace      = "production"
	testResourceName   = "sample"
	testResourceGroup  = "example.test"
	testConfigMapKind  = "ConfigMap"
	testDeploymentKind = "Deployment"
	testWorkloadNS     = "workloads"
)

func TestRejectDuplicateKubernetesResourcesPreservesClusterNamespaceInput(t *testing.T) {
	t.Parallel()

	resource := Value{Kind: Map, Map: []Entry{
		{Key: "apiVersion", Value: Value{Kind: String, Text: "v1"}},
		{Key: "kind", Value: Value{Kind: String, Text: "Namespace"}},
		{Key: metadataFieldName, Value: Value{Kind: Map, Map: []Entry{
			{Key: "name", Value: Value{Kind: String, Text: testNamespace}},
			{Key: namespaceFieldName, Value: Value{Kind: String, Text: "incorrect"}},
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
		{Key: "kind", Value: Value{Kind: String, Text: testConfigMapKind}},
		{Key: metadataFieldName, Value: Value{Kind: Map, Map: []Entry{
			{Key: "name", Value: Value{Kind: String, Text: testSettingsName}},
		}}},
	}}

	identity, ok := kubernetesResourceIdentityOf(resource)
	if !ok || identity.namespace != defaultKubernetesNamespace {
		t.Fatalf("kubernetesResourceIdentityOf() = %#v, %t; want default namespace", identity, ok)
	}
	if identity.rawNamespace != "" {
		t.Fatalf("kubernetesResourceIdentityOf() raw namespace = %q, want cleared canonical identity", identity.rawNamespace)
	}

	explicitDefault := resource
	explicitDefault.Map = append([]Entry(nil), resource.Map...)
	explicitDefault.Map[2].Value.Map = append([]Entry(nil), resource.Map[2].Value.Map...)
	explicitDefault.Map[2].Value.Map = append(explicitDefault.Map[2].Value.Map, Entry{Key: namespaceFieldName, Value: Value{Kind: String, Text: defaultKubernetesNamespace}})
	err := rejectDuplicateKubernetesResources([]Artifact{{Value: resource}, {Value: explicitDefault}}, builtinKubernetesResourceDescriptors)
	if err == nil || !strings.Contains(err.Error(), "duplicate Kubernetes resource") {
		t.Fatalf("rejectDuplicateKubernetesResources() error = %v, want duplicate after namespace normalization", err)
	}
}

func TestResolveKubernetesResourceIdentity(t *testing.T) {
	t.Parallel()

	bundle := Bundle{resourceDescriptors: []KubernetesResourceDescriptor{
		{APIGroup: testResourceGroup, Kind: "NamespacedWidget", ResourceName: "namespacedwidgets", Scope: KubernetesResourceScopeNamespaced},
		{APIGroup: testResourceGroup, Kind: "ClusterWidget", ResourceName: "clusterwidgets", Scope: KubernetesResourceScopeCluster},
	}}
	tests := []struct {
		name                 string
		apiVersion           string
		kind                 string
		namespace            string
		destinationNamespace string
		want                 KubernetesResourceIdentity
		wantError            bool
	}{
		{
			name:                 "namespace defaults to Argo destination",
			apiVersion:           "v1",
			kind:                 testConfigMapKind,
			destinationNamespace: testWorkloadNS,
			want:                 KubernetesResourceIdentity{Kind: testConfigMapKind, Namespace: testWorkloadNS, Name: testResourceName, Scope: KubernetesResourceScopeNamespaced},
		},
		{
			name:       "namespace defaults to Kubernetes default",
			apiVersion: "v1",
			kind:       testConfigMapKind,
			want:       KubernetesResourceIdentity{Kind: testConfigMapKind, Namespace: defaultKubernetesNamespace, Name: testResourceName, Scope: KubernetesResourceScopeNamespaced},
		},
		{
			name:                 "destination namespace matches explicit namespace",
			apiVersion:           "v1",
			kind:                 testConfigMapKind,
			namespace:            testWorkloadNS,
			destinationNamespace: testWorkloadNS,
			want:                 KubernetesResourceIdentity{Kind: testConfigMapKind, Namespace: testWorkloadNS, Name: testResourceName, Scope: KubernetesResourceScopeNamespaced},
		},
		{
			name:       "core API group remains empty",
			apiVersion: "v1",
			kind:       testConfigMapKind,
			want:       KubernetesResourceIdentity{Kind: testConfigMapKind, Namespace: defaultKubernetesNamespace, Name: testResourceName, Scope: KubernetesResourceScopeNamespaced},
		},
		{
			name:       "named API group is retained",
			apiVersion: "apps/v1",
			kind:       testDeploymentKind,
			want:       KubernetesResourceIdentity{APIGroup: "apps", Kind: testDeploymentKind, Namespace: defaultKubernetesNamespace, Name: testResourceName, Scope: KubernetesResourceScopeNamespaced},
		},
		{
			name:       "API versions share one identity",
			apiVersion: "apps/v1beta1",
			kind:       testDeploymentKind,
			want:       KubernetesResourceIdentity{APIGroup: "apps", Kind: testDeploymentKind, Namespace: defaultKubernetesNamespace, Name: testResourceName, Scope: KubernetesResourceScopeNamespaced},
		},
		{
			name:                 "CRD namespaced scope uses destination namespace",
			apiVersion:           testResourceGroup + "/v1alpha1",
			kind:                 "NamespacedWidget",
			destinationNamespace: "widgets",
			want:                 KubernetesResourceIdentity{APIGroup: testResourceGroup, Kind: "NamespacedWidget", Namespace: "widgets", Name: testResourceName, Scope: KubernetesResourceScopeNamespaced},
		},
		{
			name:       "CRD cluster scope has no namespace",
			apiVersion: testResourceGroup + "/v1alpha1",
			kind:       "ClusterWidget",
			want:       KubernetesResourceIdentity{APIGroup: testResourceGroup, Kind: "ClusterWidget", Name: testResourceName, Scope: KubernetesResourceScopeCluster},
		},
		{
			name:       "cluster resources reject a namespace",
			apiVersion: "v1",
			kind:       "Namespace",
			namespace:  testWorkloadNS,
			wantError:  true,
		},
		{
			name:                 "unknown scope preserves namespace",
			apiVersion:           testResourceGroup + "/v1alpha1",
			kind:                 "ExternalWidget",
			destinationNamespace: testWorkloadNS,
			want:                 KubernetesResourceIdentity{APIGroup: testResourceGroup, Kind: "ExternalWidget", Name: testResourceName, Scope: KubernetesResourceScopeUnknown},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveKubernetesResourceIdentity(bundle, test.apiVersion, test.kind, test.namespace, testResourceName, test.destinationNamespace)
			if (err != nil) != test.wantError {
				t.Fatalf("ResolveKubernetesResourceIdentity() error = %v, wantError = %t", err, test.wantError)
			}
			if err == nil && got != test.want {
				t.Fatalf("ResolveKubernetesResourceIdentity() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestKubernetesResourceIdentityResolverMatchesBundleResolver(t *testing.T) {
	t.Parallel()

	bundle := Bundle{resourceDescriptors: []KubernetesResourceDescriptor{
		{APIGroup: testResourceGroup, Kind: "NamespacedWidget", ResourceName: "namespacedwidgets", Scope: KubernetesResourceScopeNamespaced},
	}}
	resolver, err := NewKubernetesResourceIdentityResolver(bundle)
	if err != nil {
		t.Fatalf("NewKubernetesResourceIdentityResolver() error = %v", err)
	}
	want, err := ResolveKubernetesResourceIdentity(bundle, testResourceGroup+"/v1alpha1", "NamespacedWidget", "", testResourceName, testWorkloadNS)
	if err != nil {
		t.Fatalf("ResolveKubernetesResourceIdentity() error = %v", err)
	}
	got, err := resolver.Resolve(testResourceGroup+"/v1alpha1", "NamespacedWidget", "", testResourceName, testWorkloadNS)
	if err != nil {
		t.Fatalf("resolver.Resolve() error = %v", err)
	}
	if got != want {
		t.Fatalf("resolver.Resolve() = %+v, want %+v", got, want)
	}
}

func TestGeneratedNameRequiresHookLifecycle(t *testing.T) {
	t.Parallel()

	resource := Value{Kind: Map, Map: []Entry{
		{Key: "apiVersion", Value: Value{Kind: String, Text: "v1"}},
		{Key: "kind", Value: Value{Kind: String, Text: testConfigMapKind}},
		{Key: metadataFieldName, Value: Value{Kind: Map, Map: []Entry{
			{Key: "generateName", Value: Value{Kind: String, Text: "migration-"}},
		}}},
	}}
	if _, isResource, err := validatedKubernetesResourceIdentityOf(resource); err == nil || !isResource {
		t.Fatalf("validatedKubernetesResourceIdentityOf() = %t, %v; want missing-name error", isResource, err)
	}

	identity, isResource, err := validatedKubernetesResourceIdentityOfWithGeneratedName(resource, true)
	if err != nil || !isResource || !identity.generatedName || identity.name != "migration-" {
		t.Fatalf("hook generated-name identity = %+v, %t, %v", identity, isResource, err)
	}
	artifact := Artifact{Value: resource, Lifecycle: Lifecycle{Events: []string{phaseBeforeCreate}}}
	if err := rejectDuplicateKubernetesResources([]Artifact{artifact, artifact}, builtinKubernetesResourceDescriptors); err != nil {
		t.Fatalf("rejectDuplicateKubernetesResources() rejected distinct generated Hook names: %v", err)
	}
}

func BenchmarkKubernetesResourceIdentityResolver(b *testing.B) {
	for _, count := range []int{100, 1_000, 5_000} {
		bundle := bundleWithConfigMaps(count)
		b.Run(fmt.Sprintf("resources=%d/rebuild", count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ResolveKubernetesResourceIdentity(bundle, "v1", testConfigMapKind, "", "resource-0", testWorkloadNS); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("resources=%d/reuse", count), func(b *testing.B) {
			resolver, err := NewKubernetesResourceIdentityResolver(bundle)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := resolver.Resolve("v1", testConfigMapKind, "", "resource-0", testWorkloadNS); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func bundleWithConfigMaps(count int) Bundle {
	bundle := Bundle{Artifacts: make([]Artifact, count)}
	for index := range count {
		bundle.Artifacts[index] = Artifact{Value: Value{Kind: Map, Map: []Entry{
			{Key: "apiVersion", Value: Value{Kind: String, Text: "v1"}},
			{Key: "kind", Value: Value{Kind: String, Text: testConfigMapKind}},
			{Key: metadataFieldName, Value: Value{Kind: Map, Map: []Entry{
				{Key: "name", Value: Value{Kind: String, Text: fmt.Sprintf("resource-%d", index)}},
			}}},
		}}}
	}

	return bundle
}

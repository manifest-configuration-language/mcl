package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// KubernetesResourceCatalogRevisionは組み込みKubernetes Resource Catalogのrevisionを識別します。
const KubernetesResourceCatalogRevision = "mcl-kubernetes-resource-catalog-v1"

// KubernetesResourceScopeはKubernetes Resourceのnamespace scopeを表します。
type KubernetesResourceScope string

const (
	KubernetesResourceScopeNamespaced KubernetesResourceScope = "Namespaced"
	KubernetesResourceScopeCluster    KubernetesResourceScope = "Cluster"
)

// KubernetesResourceDescriptorはResourceのAPI identityとnamespace scopeを表します。
type KubernetesResourceDescriptor struct {
	APIGroup     string
	Kind         string
	ResourceName string
	Scope        KubernetesResourceScope
}

// KubernetesResourceCatalogはrevision付き組み込みCatalogに追加Descriptorを設定します。
type KubernetesResourceCatalog struct {
	Revision            string
	AdditionalResources []KubernetesResourceDescriptor
}

// DefaultKubernetesResourceCatalogは現在の組み込みCatalog revisionを返します。
func DefaultKubernetesResourceCatalog() KubernetesResourceCatalog {
	return KubernetesResourceCatalog{Revision: KubernetesResourceCatalogRevision}
}

// NewKubernetesResourceCatalogは現在の組み込みCatalogに追加Descriptorを設定して返します。
func NewKubernetesResourceCatalog(additionalResources ...KubernetesResourceDescriptor) KubernetesResourceCatalog {
	catalog := DefaultKubernetesResourceCatalog()
	catalog.AdditionalResources = slices.Clone(additionalResources)

	return catalog
}

type kubernetesResourceScope uint8

const (
	kubernetesResourceUnknown kubernetesResourceScope = iota
	kubernetesResourceNamespaced
	kubernetesResourceCluster
)

type kubernetesResourceKey struct {
	group string
	kind  string
}

type kubernetesResourceDescriptor struct {
	resourceName string
	scope        kubernetesResourceScope
}

type kubernetesResourceScopeIndex map[kubernetesResourceKey]kubernetesResourceDescriptor

type kubernetesResourceCatalogEntry struct {
	group        string
	version      string
	kind         string
	resourceName string
	scope        kubernetesResourceScope
}

func resourceScopeIndexFromCatalog(entries []kubernetesResourceCatalogEntry) kubernetesResourceScopeIndex {
	index := make(kubernetesResourceScopeIndex, len(entries))
	for _, entry := range entries {
		key := kubernetesResourceKey{group: entry.group, kind: entry.kind}
		value := kubernetesResourceDescriptor{resourceName: entry.resourceName, scope: entry.scope}
		if previous, exists := index[key]; exists && previous != value {
			panic(fmt.Sprintf("inconsistent built-in Kubernetes resource descriptor across API versions for %s/%s", entry.group, entry.kind))
		}
		index[key] = value
	}
	return index
}

//go:generate go run ./cmd/generate-kubernetes-resource-catalog -input ../k8sschema/data/kubernetes-v1.37.1.swagger.json.gz -output kubernetes_resource_catalog_generated.go

func newKubernetesResourceScopeIndex(artifacts []Artifact) (kubernetesResourceScopeIndex, error) {
	return newKubernetesResourceScopeIndexWithCatalog(artifacts, DefaultKubernetesResourceCatalog())
}

func newKubernetesResourceScopeIndexWithCatalog(artifacts []Artifact, catalog KubernetesResourceCatalog) (kubernetesResourceScopeIndex, error) {
	if catalog.Revision != KubernetesResourceCatalogRevision {
		return nil, fmt.Errorf("unsupported Kubernetes Resource catalog revision %q", catalog.Revision)
	}
	index := maps.Clone(builtinKubernetesResourceDescriptors)
	for _, descriptor := range catalog.AdditionalResources {
		key, value, err := kubernetesResourceDescriptorFromPublic(descriptor)
		if err != nil {
			return nil, err
		}
		if _, exists := index[key]; exists {
			return nil, fmt.Errorf("additional Kubernetes Resource descriptor %q conflicts with an existing descriptor", kubernetesResourceKeyName(key))
		}
		index[key] = value
	}
	for _, artifact := range artifacts {
		value := nodeContents(artifact.Value)
		kind, exists := valueStringField(value, "kind")
		if !exists || kind != kindCustomResourceDefinition {
			continue
		}
		key, descriptor, err := kubernetesCustomResourceDescriptor(value)
		if err != nil {
			return nil, fmt.Errorf("CustomResourceDefinition scope: %w", err)
		}
		if existing, exists := index[key]; exists && existing != descriptor {
			return nil, fmt.Errorf("CustomResourceDefinition %q conflicts with an existing Kubernetes API resource", kubernetesResourceKeyName(key))
		}
		index[key] = descriptor
	}

	return index, nil
}

func KubernetesResourceScopeForBundle(bundle Bundle, catalog KubernetesResourceCatalog, apiVersion, kind string) (KubernetesResourceScope, bool, error) {
	index, err := newKubernetesResourceScopeIndexWithCatalog(bundle.Artifacts, catalog)
	if err != nil {
		return "", false, err
	}
	group, _, hasGroup := strings.Cut(apiVersion, "/")
	if !hasGroup {
		group = ""
	}
	descriptor, exists := index[kubernetesResourceKey{group: group, kind: kind}]
	if !exists {
		return "", false, nil
	}
	if descriptor.scope == kubernetesResourceCluster {
		return KubernetesResourceScopeCluster, true, nil
	}

	return KubernetesResourceScopeNamespaced, true, nil
}

func kubernetesResourceKeyName(key kubernetesResourceKey) string {
	if key.group == "" {
		return key.kind
	}

	return key.group + "/" + key.kind
}

func kubernetesResourceDescriptorFromPublic(descriptor KubernetesResourceDescriptor) (kubernetesResourceKey, kubernetesResourceDescriptor, error) {
	if descriptor.APIGroup != "" && len(validation.IsDNS1123Subdomain(descriptor.APIGroup)) != 0 {
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, fmt.Errorf("invalid Kubernetes Resource API group %q", descriptor.APIGroup)
	}
	if len(validation.IsDNS1035Label(strings.ToLower(descriptor.Kind))) != 0 {
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, fmt.Errorf("invalid Kubernetes Resource kind %q", descriptor.Kind)
	}
	if len(validation.IsDNS1123Label(descriptor.ResourceName)) != 0 {
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, fmt.Errorf("invalid Kubernetes Resource name %q", descriptor.ResourceName)
	}
	var scope kubernetesResourceScope
	switch descriptor.Scope {
	case KubernetesResourceScopeNamespaced:
		scope = kubernetesResourceNamespaced
	case KubernetesResourceScopeCluster:
		scope = kubernetesResourceCluster
	default:
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, fmt.Errorf("invalid Kubernetes Resource scope %q", descriptor.Scope)
	}

	return kubernetesResourceKey{group: descriptor.APIGroup, kind: descriptor.Kind}, kubernetesResourceDescriptor{resourceName: descriptor.ResourceName, scope: scope}, nil
}

func kubernetesCustomResourceDescriptor(value Value) (kubernetesResourceKey, kubernetesResourceDescriptor, error) {
	apiVersion, hasAPIVersion := valueStringField(value, "apiVersion")
	if !hasAPIVersion || apiVersion != "apiextensions.k8s.io/v1" {
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, errors.New("apiVersion must be apiextensions.k8s.io/v1")
	}
	spec, ok := mapValue(value, "spec")
	if !ok {
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, errors.New("spec is missing")
	}
	group, hasGroup := valueStringField(spec, "group")
	scopeText, hasScope := valueStringField(spec, "scope")
	names, hasNames := mapValue(nodeContents(spec), "names")
	plural, hasPlural := valueStringField(names, "plural")
	kind, hasKind := valueStringField(names, "kind")
	if !hasGroup || group == "" || !hasNames || !hasKind || kind == "" || !hasScope || !hasPlural || plural == "" {
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, errors.New("spec.group, spec.scope, spec.names.kind, and non-empty spec.names.plural are required")
	}
	var scope kubernetesResourceScope
	switch scopeText {
	case "Namespaced":
		scope = kubernetesResourceNamespaced
	case "Cluster":
		scope = kubernetesResourceCluster
	default:
		return kubernetesResourceKey{}, kubernetesResourceDescriptor{}, fmt.Errorf("spec.scope must be Namespaced or Cluster, got %q", scopeText)
	}

	publicScope := KubernetesResourceScopeNamespaced
	if scope == kubernetesResourceCluster {
		publicScope = KubernetesResourceScopeCluster
	}

	return kubernetesResourceDescriptorFromPublic(KubernetesResourceDescriptor{
		APIGroup: group, Kind: kind, ResourceName: plural, Scope: publicScope,
	})
}

func validKubernetesAPIVersion(version string) bool {
	if !strings.HasPrefix(version, "v") {
		return false
	}
	version = strings.TrimPrefix(version, "v")
	majorEnd := 0
	for majorEnd < len(version) && version[majorEnd] >= '0' && version[majorEnd] <= '9' {
		majorEnd++
	}
	if majorEnd == 0 {
		return false
	}
	if majorEnd == len(version) {
		return true
	}
	for _, suffix := range []string{"alpha", "beta"} {
		if !strings.HasPrefix(version[majorEnd:], suffix) {
			continue
		}
		minor := strings.TrimPrefix(version[majorEnd:], suffix)
		if minor == "" {
			return false
		}
		for _, digit := range minor {
			if digit < '0' || digit > '9' {
				return false
			}
		}

		return true
	}

	return false
}

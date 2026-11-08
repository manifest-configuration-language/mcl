package kubernetes

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/value"
	"k8s.io/apimachinery/pkg/util/validation"
)

const CatalogRevision = "mcl-kubernetes-resource-catalog-v1"

type Scope string

const (
	ScopeUnknown    Scope = "Unknown"
	ScopeNamespaced Scope = "Namespaced"
	ScopeCluster    Scope = "Cluster"
)

type Descriptor struct {
	APIGroup     string
	Kind         string
	ResourceName string
	Scope        Scope
}

type Catalog struct {
	Revision            string
	AdditionalResources []Descriptor
}

func DefaultCatalog() Catalog { return Catalog{Revision: CatalogRevision} }

func NewCatalog(additionalResources ...Descriptor) Catalog {
	catalog := DefaultCatalog()
	catalog.AdditionalResources = slices.Clone(additionalResources)
	return catalog
}

type ResourceKey struct{ Group, Kind string }
type ResourceDescriptor struct {
	ResourceName string
	Scope        Scope
}
type ScopeIndex map[ResourceKey]ResourceDescriptor
type CatalogEntry struct {
	Group, Version, Kind, ResourceName string
	Scope                              Scope
}

func BuiltinScopeIndex() ScopeIndex { return maps.Clone(builtinDescriptors) }

func ScopeIndexFromCatalog(entries []CatalogEntry) ScopeIndex {
	index := make(ScopeIndex, len(entries))
	for _, entry := range entries {
		key := ResourceKey{Group: entry.Group, Kind: entry.Kind}
		value := ResourceDescriptor{ResourceName: entry.ResourceName, Scope: entry.Scope}
		if previous, exists := index[key]; exists && previous != value {
			panic(fmt.Sprintf("inconsistent built-in Kubernetes resource descriptor across API versions for %s/%s", entry.Group, entry.Kind))
		}
		index[key] = value
	}
	return index
}

//go:generate go run ./cmd/generate-kubernetes-resource-catalog -input ../k8sschema/data/kubernetes-v1.37.1.swagger.json.gz -output catalog_generated.go

func NewScopeIndex(artifacts []value.Artifact, catalog Catalog, descriptors []Descriptor) (ScopeIndex, error) {
	if catalog.Revision != CatalogRevision {
		return nil, fmt.Errorf("unsupported Kubernetes Resource catalog revision %q", catalog.Revision)
	}
	index := maps.Clone(builtinDescriptors)
	for _, descriptor := range catalog.AdditionalResources {
		key, value, err := ResourceDescriptorFromPublic(descriptor)
		if err != nil {
			return nil, err
		}
		if _, exists := index[key]; exists {
			return nil, fmt.Errorf("additional Kubernetes Resource descriptor %q conflicts with an existing descriptor", resourceKeyName(key))
		}
		index[key] = value
	}
	for _, artifact := range artifacts {
		contents := nodeContents(artifact.Value)
		kind, exists := stringField(contents, "kind")
		if !exists || kind != "CustomResourceDefinition" {
			continue
		}
		key, descriptor, err := customResourceDescriptor(contents)
		if err != nil {
			return nil, fmt.Errorf("CustomResourceDefinition scope: %w", err)
		}
		if existing, exists := index[key]; exists && existing != descriptor {
			return nil, fmt.Errorf("CustomResourceDefinition %q conflicts with an existing Kubernetes API resource", resourceKeyName(key))
		}
		index[key] = descriptor
	}
	for _, descriptor := range descriptors {
		key, value, err := ResourceDescriptorFromPublic(descriptor)
		if err != nil {
			return nil, err
		}
		if previous, exists := index[key]; exists {
			if previous != value {
				return nil, fmt.Errorf("project CRD descriptor %q conflicts with an existing descriptor", resourceKeyName(key))
			}
			continue
		}
		index[key] = value
	}
	return index, nil
}

func (index ScopeIndex) Descriptor(group, kind string) (ResourceDescriptor, bool) {
	descriptor, exists := index[ResourceKey{Group: group, Kind: kind}]
	return descriptor, exists
}

func (index ScopeIndex) ScopeFor(apiVersion, kind string) (Scope, bool) {
	group, _, hasGroup := strings.Cut(apiVersion, "/")
	if !hasGroup {
		group = ""
	}
	descriptor, exists := index[ResourceKey{Group: group, Kind: kind}]
	if !exists {
		return "", false
	}
	return descriptor.Scope, true
}

func resourceKeyName(key ResourceKey) string {
	if key.Group == "" {
		return key.Kind
	}
	return key.Group + "/" + key.Kind
}

func ResourceDescriptorFromPublic(descriptor Descriptor) (ResourceKey, ResourceDescriptor, error) {
	if descriptor.APIGroup != "" && len(validation.IsDNS1123Subdomain(descriptor.APIGroup)) != 0 {
		return ResourceKey{}, ResourceDescriptor{}, fmt.Errorf("invalid Kubernetes Resource API group %q", descriptor.APIGroup)
	}
	if len(validation.IsDNS1035Label(strings.ToLower(descriptor.Kind))) != 0 {
		return ResourceKey{}, ResourceDescriptor{}, fmt.Errorf("invalid Kubernetes Resource kind %q", descriptor.Kind)
	}
	if len(validation.IsDNS1123Label(descriptor.ResourceName)) != 0 {
		return ResourceKey{}, ResourceDescriptor{}, fmt.Errorf("invalid Kubernetes Resource name %q", descriptor.ResourceName)
	}
	if descriptor.Scope != ScopeNamespaced && descriptor.Scope != ScopeCluster {
		return ResourceKey{}, ResourceDescriptor{}, fmt.Errorf("invalid Kubernetes Resource scope %q", descriptor.Scope)
	}
	return ResourceKey{Group: descriptor.APIGroup, Kind: descriptor.Kind}, ResourceDescriptor{ResourceName: descriptor.ResourceName, Scope: descriptor.Scope}, nil
}

func customResourceDescriptor(resource value.Value) (ResourceKey, ResourceDescriptor, error) {
	apiVersion, hasAPIVersion := stringField(resource, "apiVersion")
	if !hasAPIVersion || apiVersion != "apiextensions.k8s.io/v1" {
		return ResourceKey{}, ResourceDescriptor{}, fmt.Errorf("apiVersion must be apiextensions.k8s.io/v1")
	}
	spec, ok := mapField(resource, "spec")
	if !ok {
		return ResourceKey{}, ResourceDescriptor{}, fmt.Errorf("spec is missing")
	}
	group, hasGroup := stringField(spec, "group")
	scope, hasScope := stringField(spec, "scope")
	names, hasNames := mapField(nodeContents(spec), "names")
	plural, hasPlural := stringField(names, "plural")
	kind, hasKind := stringField(names, "kind")
	if !hasGroup || group == "" || !hasNames || !hasKind || kind == "" || !hasScope || !hasPlural || plural == "" {
		return ResourceKey{}, ResourceDescriptor{}, fmt.Errorf("spec.group, spec.scope, spec.names.kind, and non-empty spec.names.plural are required")
	}
	publicScope := Scope(scope)
	if publicScope != ScopeNamespaced && publicScope != ScopeCluster {
		return ResourceKey{}, ResourceDescriptor{}, fmt.Errorf("spec.scope must be Namespaced or Cluster, got %q", scope)
	}
	return ResourceDescriptorFromPublic(Descriptor{APIGroup: group, Kind: kind, ResourceName: plural, Scope: publicScope})
}

func nodeContents(item value.Value) value.Value {
	for item.Kind == value.Node && item.Base != nil {
		item = *item.Base
	}
	return item
}

func mapField(item value.Value, key string) (value.Value, bool) {
	for _, entry := range item.Map {
		if entry.Key == key {
			return entry.Value, true
		}
	}
	return value.Value{}, false
}

func stringField(item value.Value, key string) (string, bool) {
	field, exists := mapField(nodeContents(item), key)
	return field.Text, exists && field.Kind == value.String
}

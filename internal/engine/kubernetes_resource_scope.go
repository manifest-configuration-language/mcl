package engine

import (
	"github.com/manifest-configuration-language/mcl/internal/value"

	kubernetespkg "github.com/manifest-configuration-language/mcl/internal/kubernetes"
)

const KubernetesResourceCatalogRevision = kubernetespkg.CatalogRevision

type KubernetesResourceScope = kubernetespkg.Scope

const (
	KubernetesResourceScopeUnknown    = kubernetespkg.ScopeUnknown
	KubernetesResourceScopeNamespaced = kubernetespkg.ScopeNamespaced
	KubernetesResourceScopeCluster    = kubernetespkg.ScopeCluster
)

type KubernetesResourceIdentity = kubernetespkg.KubernetesResourceIdentity
type KubernetesResourceIdentityResolver = kubernetespkg.IdentityResolver
type KubernetesResourceDescriptor = kubernetespkg.Descriptor
type KubernetesResourceCatalog = kubernetespkg.Catalog

type kubernetesResourceScopeIndex = kubernetespkg.ScopeIndex
type kubernetesResourceKey = kubernetespkg.ResourceKey
type kubernetesResourceDescriptor = kubernetespkg.ResourceDescriptor
type kubernetesResourceScope = kubernetespkg.Scope

var builtinKubernetesResourceDescriptors = kubernetespkg.BuiltinScopeIndex()

const (
	kubernetesResourceUnknown    = kubernetespkg.ScopeUnknown
	kubernetesResourceNamespaced = kubernetespkg.ScopeNamespaced
	kubernetesResourceCluster    = kubernetespkg.ScopeCluster
	defaultKubernetesNamespace   = kubernetespkg.DefaultNamespace
)

func DefaultKubernetesResourceCatalog() KubernetesResourceCatalog {
	return kubernetespkg.DefaultCatalog()
}

func NewKubernetesResourceCatalog(additionalResources ...KubernetesResourceDescriptor) KubernetesResourceCatalog {
	return kubernetespkg.NewCatalog(additionalResources...)
}

func newKubernetesResourceScopeIndex(artifacts []Artifact) (kubernetesResourceScopeIndex, error) {
	return newKubernetesResourceScopeIndexWithCatalog(artifacts, DefaultKubernetesResourceCatalog())
}

func newKubernetesResourceScopeIndexWithCatalog(artifacts []Artifact, catalog KubernetesResourceCatalog) (kubernetesResourceScopeIndex, error) {
	return kubernetespkg.NewScopeIndex(artifacts, catalog, nil)
}

func newKubernetesResourceScopeIndexForBundle(bundle Bundle, catalog KubernetesResourceCatalog) (kubernetesResourceScopeIndex, error) {
	return kubernetespkg.NewScopeIndex(bundle.Artifacts, catalog, bundle.resourceDescriptors)
}

func KubernetesResourceScopeForBundle(bundle Bundle, catalog KubernetesResourceCatalog, apiVersion, kind string) (KubernetesResourceScope, bool, error) {
	index, err := newKubernetesResourceScopeIndexForBundle(bundle, catalog)
	if err != nil {
		return "", false, err
	}
	scope, ok := index.ScopeFor(apiVersion, kind)
	return scope, ok, nil
}

func NewKubernetesResourceIdentityResolver(bundle Bundle) (*KubernetesResourceIdentityResolver, error) {
	return NewKubernetesResourceIdentityResolverWithCatalog(bundle, DefaultKubernetesResourceCatalog())
}

func NewKubernetesResourceIdentityResolverWithCatalog(bundle Bundle, catalog KubernetesResourceCatalog) (*KubernetesResourceIdentityResolver, error) {
	index, err := newKubernetesResourceScopeIndexForBundle(bundle, catalog)
	if err != nil {
		return nil, err
	}
	return kubernetespkg.NewIdentityResolver(index), nil
}

func ResolveKubernetesResourceIdentity(bundle Bundle, apiVersion, kind, namespace, name, destinationNamespace string) (KubernetesResourceIdentity, error) {
	resolver, err := NewKubernetesResourceIdentityResolver(bundle)
	if err != nil {
		return KubernetesResourceIdentity{}, err
	}
	return resolver.Resolve(apiVersion, kind, namespace, name, destinationNamespace)
}

func kubernetesResourceKeyName(key kubernetesResourceKey) string {
	if key.Group == "" {
		return key.Kind
	}
	return key.Group + "/" + key.Kind
}

func validKubernetesAPIVersion(version string) bool { return kubernetespkg.ValidAPIVersion(version) }

func resourceScopeIndexFromCatalog(entries []kubernetespkg.CatalogEntry) kubernetesResourceScopeIndex {
	return kubernetespkg.ScopeIndexFromCatalog(entries)
}

func parseKubernetesResourceIdentity(value value.Value, allowGeneratedName bool) (kubernetesResourceIdentity, bool, error) {
	identity, isResource, err := kubernetespkg.ParseResourceIdentity(value, allowGeneratedName)
	return kubernetesResourceIdentity{group: identity.APIGroup, kind: identity.Kind, namespace: identity.Namespace, rawNamespace: identity.RawNamespace, name: identity.Name, resourceName: identity.ResourceName, scope: identity.Scope, generatedName: identity.GeneratedName}, isResource, err
}

func normalizeKubernetesResourceIdentity(identity kubernetesResourceIdentity) kubernetesResourceIdentity {
	normalized := kubernetespkg.NormalizeResourceIdentity(kubernetespkg.ResourceIdentity{APIGroup: identity.group, Kind: identity.kind, Namespace: identity.namespace, RawNamespace: identity.rawNamespace, Name: identity.name, ResourceName: identity.resourceName, Scope: identity.scope, GeneratedName: identity.generatedName}, kubernetespkg.BuiltinScopeIndex())
	return kubernetesResourceIdentity{group: normalized.APIGroup, kind: normalized.Kind, namespace: normalized.Namespace, rawNamespace: normalized.RawNamespace, name: normalized.Name, resourceName: normalized.ResourceName, scope: normalized.Scope, generatedName: normalized.GeneratedName}
}

func kubernetesResourceDescriptorFromPublic(descriptor KubernetesResourceDescriptor) (kubernetesResourceKey, kubernetesResourceDescriptor, error) {
	return kubernetespkg.ResourceDescriptorFromPublic(descriptor)
}

package kubernetes

import (
	"errors"
	"fmt"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/value"
	"k8s.io/apimachinery/pkg/util/validation"
)

const DefaultNamespace = "default"

type ResourceIdentity struct {
	APIGroup      string
	Kind          string
	Namespace     string
	RawNamespace  string
	Name          string
	ResourceName  string
	Scope         Scope
	GeneratedName bool
}

type Identity = ResourceIdentity
type KubernetesResourceIdentity = ResourceIdentity

type IdentityResolver struct{ scopes ScopeIndex }

func NewIdentityResolver(index ScopeIndex) *IdentityResolver { return &IdentityResolver{scopes: index} }

func (resolver *IdentityResolver) Resolve(apiVersion, kind, namespace, name, destinationNamespace string) (ResourceIdentity, error) {
	if resolver == nil || resolver.scopes == nil {
		return ResourceIdentity{}, errors.New("Kubernetes Resource identity resolver is nil")
	}
	if kind == "" || name == "" {
		return ResourceIdentity{}, errors.New("kubernetes resource kind and name must not be empty")
	}
	group, version, hasGroup := strings.Cut(apiVersion, "/")
	if !hasGroup {
		group = ""
		version = apiVersion
	} else if group == "" || strings.Contains(version, "/") {
		return ResourceIdentity{}, errors.New("kubernetes resource apiVersion is invalid")
	}
	if !ValidAPIVersion(version) {
		return ResourceIdentity{}, errors.New("kubernetes resource apiVersion is invalid")
	}
	if group != "" && len(validation.IsDNS1123Subdomain(group)) != 0 {
		return ResourceIdentity{}, fmt.Errorf("invalid Kubernetes resource API group %q", group)
	}
	if len(validation.IsDNS1035Label(strings.ToLower(kind))) != 0 {
		return ResourceIdentity{}, fmt.Errorf("invalid Kubernetes resource kind %q", kind)
	}
	descriptor, known := resolver.scopes[ResourceKey{Group: group, Kind: kind}]
	scope := ScopeUnknown
	if known {
		scope = descriptor.Scope
	}
	switch scope {
	case ScopeNamespaced:
		if namespace == "" {
			namespace = destinationNamespace
			if namespace == "" {
				namespace = DefaultNamespace
			}
		}
	case ScopeCluster:
		if namespace != "" {
			return ResourceIdentity{}, fmt.Errorf("cluster-scoped Kubernetes resource %s/%s must not set metadata.namespace", group, kind)
		}
	case ScopeUnknown:
	}
	return ResourceIdentity{APIGroup: group, Kind: kind, Namespace: namespace, Name: name, Scope: scope}, nil
}

func ParseResourceIdentity(item value.Value, allowGeneratedName bool) (ResourceIdentity, bool, error) {
	item = nodeContents(item)
	if item.Kind != value.Map {
		return ResourceIdentity{}, false, nil
	}
	apiVersion, hasAPIVersion := mapField(item, "apiVersion")
	kind, hasKind := mapField(item, "kind")
	if !hasAPIVersion && !hasKind {
		return ResourceIdentity{}, false, nil
	}
	if !hasAPIVersion || apiVersion.Kind != value.String || apiVersion.Text == "" {
		return ResourceIdentity{}, true, errors.New("kubernetes resource apiVersion must be a non-empty string")
	}
	if !hasKind || kind.Kind != value.String || kind.Text == "" {
		return ResourceIdentity{}, true, errors.New("kubernetes resource kind must be a non-empty string")
	}
	metadata, hasMetadata := mapField(item, "metadata")
	if !hasMetadata || nodeContents(metadata).Kind != value.Map {
		return ResourceIdentity{}, true, errors.New("kubernetes resource metadata must be a map containing name")
	}
	metadata = nodeContents(metadata)
	name, hasName := mapField(metadata, "name")
	generatedName := false
	if !hasName || name.Kind != value.String || name.Text == "" {
		prefix, hasPrefix := mapField(metadata, "generateName")
		if !allowGeneratedName || !hasPrefix || prefix.Kind != value.String || prefix.Text == "" {
			return ResourceIdentity{}, true, errors.New("kubernetes resource metadata.name must be a non-empty string")
		}
		name = prefix
		generatedName = true
	}
	group, version, hasGroup := strings.Cut(apiVersion.Text, "/")
	if !hasGroup {
		group = ""
		version = apiVersion.Text
	} else if group == "" || strings.Contains(version, "/") {
		return ResourceIdentity{}, true, errors.New("kubernetes resource apiVersion is invalid")
	}
	if !ValidAPIVersion(version) {
		return ResourceIdentity{}, true, errors.New("kubernetes resource apiVersion is invalid")
	}
	namespace := value.Value{}
	if namespaceValue, exists := mapField(metadata, "namespace"); exists {
		if namespaceValue.Kind != value.String {
			return ResourceIdentity{}, true, errors.New("kubernetes resource metadata.namespace must be a string")
		}
		namespace = namespaceValue
	}
	identity := ResourceIdentity{APIGroup: group, Kind: kind.Text, Namespace: namespace.Text, RawNamespace: namespace.Text, Name: name.Text, GeneratedName: generatedName}
	if descriptor, known := builtinDescriptors[ResourceKey{Group: group, Kind: kind.Text}]; known {
		identity.ResourceName = descriptor.ResourceName
		identity.Scope = descriptor.Scope
	}
	return identity, true, nil
}

func NormalizeResourceIdentity(identity ResourceIdentity, index ScopeIndex) ResourceIdentity {
	descriptor, known := index[ResourceKey{Group: identity.APIGroup, Kind: identity.Kind}]
	if !known {
		return identity
	}
	identity.ResourceName = descriptor.ResourceName
	identity.Scope = descriptor.Scope
	if descriptor.Scope == ScopeCluster {
		identity.Namespace = ""
	} else if descriptor.Scope == ScopeNamespaced && identity.Namespace == "" {
		identity.Namespace = DefaultNamespace
	}
	identity.RawNamespace = ""
	return identity
}

func ValidAPIVersion(version string) bool {
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

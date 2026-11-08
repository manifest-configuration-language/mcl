package kubernetes

import (
	"fmt"

	"github.com/manifest-configuration-language/mcl/internal/value"
)

// ValidateResourceArtifacts rejects invalid or duplicate Kubernetes resource identities in output order.
func ValidateResourceArtifacts(artifacts []value.Artifact, scopes ScopeIndex) error {
	seen := make(map[outputResourceIdentity]int, len(artifacts))
	for index, artifact := range artifacts {
		identity, isResource, err := ParseResourceIdentity(artifact.Value, AllowsGeneratedName(artifact.Lifecycle))
		if err != nil {
			return fmt.Errorf("artifact %d: %w", index+1, err)
		}
		if !isResource {
			continue
		}
		descriptor, known := scopes[ResourceKey{Group: identity.APIGroup, Kind: identity.Kind}]
		if !known {
			descriptor.Scope = ScopeUnknown
		}
		if descriptor.Scope == ScopeCluster && identity.RawNamespace != "" {
			resource := descriptor.ResourceName
			if identity.APIGroup != "" {
				resource = identity.APIGroup + "/" + resource
			}
			return fmt.Errorf("cluster-scoped Kubernetes resource %s with name %q must not set metadata.namespace (artifact %d)", resource, identity.Name, index+1)
		}
		identity.ResourceName = descriptor.ResourceName
		if descriptor.Scope == ScopeUnknown {
			identity.ResourceName = identity.Kind
		}
		identity.Scope = descriptor.Scope
		identity.Kind = ""
		identity.RawNamespace = ""
		switch descriptor.Scope {
		case ScopeNamespaced:
			if identity.Namespace == "" {
				identity.Namespace = DefaultNamespace
			}
		case ScopeCluster:
			identity.Namespace = ""
		case ScopeUnknown:
		}
		if identity.GeneratedName {
			continue
		}
		key := outputResourceIdentity{group: identity.APIGroup, kind: identity.Kind, namespace: identity.Namespace, name: identity.Name, resourceName: identity.ResourceName, scope: identity.Scope}
		if previousIndex, exists := seen[key]; exists {
			resource := identity.ResourceName
			if identity.APIGroup != "" {
				resource = identity.APIGroup + "/" + resource
			}
			if identity.Scope != ScopeCluster {
				return fmt.Errorf("duplicate Kubernetes resource %s with name %q in namespace %q (artifacts %d and %d)", resource, identity.Name, identity.Namespace, previousIndex+1, index+1)
			}
			return fmt.Errorf("duplicate cluster-scoped Kubernetes resource %s with name %q (artifacts %d and %d)", resource, identity.Name, previousIndex+1, index+1)
		}
		seen[key] = index
	}
	return nil
}

type outputResourceIdentity struct {
	group, kind, namespace, name, resourceName string
	scope                                      Scope
}

func AllowsGeneratedName(lifecycle value.Lifecycle) bool {
	if len(lifecycle.Events) == 0 {
		return false
	}
	for _, event := range lifecycle.Events {
		switch event {
		case "before.create", "after.create", "before.update", "after.update", "before.delete", "after.delete", "before.rollback", "after.rollback", "verify":
		default:
			return false
		}
	}
	return true
}

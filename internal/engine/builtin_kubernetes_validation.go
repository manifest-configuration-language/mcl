package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/k8sschema"
)

func validateBuiltinKubernetesResource(value RuntimeValue, validatedCustomResources map[customResourceDefinitionKey]struct{}, catalog KubernetesResourceCatalog) error {
	value = runtimeMapContents(value)
	if value.Kind() != Map {
		return nil
	}
	_, hasAPIVersion := runtimeMapValue(value, "apiVersion")
	_, hasKind := runtimeMapValue(value, "kind")
	if !hasAPIVersion && !hasKind {
		return nil
	}
	converted, err := runtimeValueToValue(value)
	if err != nil {
		return fmt.Errorf("convert Kubernetes resource: %w", err)
	}
	identity, isResource, err := validatedKubernetesResourceIdentityOf(converted)
	if err != nil {
		return fmt.Errorf("invalid Kubernetes resource identity: %w", err)
	}
	if !isResource {
		return nil
	}
	apiVersion, _ := valueStringField(converted, "apiVersion")
	group, _, hasGroup := strings.Cut(apiVersion, "/")
	if !hasGroup {
		group = ""
	}
	if !k8sschema.HasBuiltinSchema(apiVersion, identity.kind) {
		key := customResourceDefinitionKey{group: group, version: apiVersionVersion(apiVersion), kind: identity.kind}
		if _, validated := validatedCustomResources[key]; validated {
			return nil
		}
		if _, known := builtinKubernetesResourceDescriptors[kubernetesResourceKey{group: group, kind: identity.kind}]; known {
			return fmt.Errorf("kubernetes resource %s uses unsupported apiVersion %q; no built-in schema is available and no matching in-bundle CustomResourceDefinition schema exists", identity.kind, apiVersion)
		}
		for _, descriptor := range catalog.AdditionalResources {
			if descriptor.APIGroup == group && descriptor.Kind == identity.kind {
				return fmt.Errorf("registered Kubernetes resource %s (%s) has no schema; add a matching in-bundle CustomResourceDefinition with schema.openAPIV3Schema", identity.kind, apiVersion)
			}
		}

		return fmt.Errorf("unknown kubernetes resource %s (%s) cannot be validated; provide a matching in-bundle CustomResourceDefinition with schema.openAPIV3Schema", identity.kind, apiVersion)
	}
	instance, err := runtimeValueAsKubernetesJSON(value)
	if err != nil {
		return fmt.Errorf("convert Kubernetes resource %s: %w", identity.kind, err)
	}
	validationErrors := k8sschema.ValidateBuiltin(apiVersion, identity.kind, instance)
	if len(validationErrors) == 0 {
		return nil
	}

	return fmt.Errorf("kubernetes resource %s (%s): %w", identity.kind, apiVersion, errors.Join(validationErrors...))
}

func apiVersionVersion(apiVersion string) string {
	_, version, hasGroup := strings.Cut(apiVersion, "/")
	if !hasGroup {
		return ""
	}

	return version
}

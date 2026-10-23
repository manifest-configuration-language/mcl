package engine

import (
	"errors"
	"fmt"

	"github.com/manifest-configuration-language/mcl/internal/k8sschema"
)

func validateBuiltinKubernetesResource(value RuntimeValue) error {
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
	if !k8sschema.HasBuiltinSchema(apiVersion, identity.kind) {
		// scope catalogとは独立してschemaを扱い、ローカルschemaがないリソースの検証を省略する。
		return nil
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

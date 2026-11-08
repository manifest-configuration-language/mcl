package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/k8sschema"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validateBuiltinKubernetesResource(value RuntimeValue, lifecycle Lifecycle, customSchemas map[customResourceDefinitionKey]struct{}, kubernetesVersion string) error {
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
	identity, isResource, err := validatedKubernetesResourceIdentityOfWithGeneratedName(converted, lifecycleAllowsGeneratedName(lifecycle))
	if err != nil {
		return fmt.Errorf("invalid Kubernetes resource identity: %w", err)
	}
	if !isResource {
		return nil
	}
	apiVersion, _ := valueStringField(converted, "apiVersion")
	_, hasBuiltinSchema, err := k8sschema.BuiltinOpenAPIForVersion(kubernetesVersion, apiVersion, identity.kind)
	if err != nil {
		return fmt.Errorf("load Kubernetes resource schema for %s (%s): %w", identity.kind, apiVersion, err)
	}
	if !hasBuiltinSchema {
		group, version, hasGroup := strings.Cut(apiVersion, "/")
		if !hasGroup {
			version = group
			group = ""
		}
		if _, registered := customSchemas[customResourceDefinitionKey{group: group, version: version, kind: identity.kind}]; registered {
			return nil
		}

		return fmt.Errorf("no schema is registered for Kubernetes resource %s (%s)", identity.kind, apiVersion)
	}
	instance, err := runtimeValueAsKubernetesJSON(value)
	if err != nil {
		return fmt.Errorf("convert Kubernetes resource %s: %w", identity.kind, err)
	}
	validationErrors := k8sschema.ValidateBuiltinForVersion(kubernetesVersion, apiVersion, identity.kind, instance)
	if len(validationErrors) == 0 {
		return nil
	}

	return builtinKubernetesValidationDiagnostic(value, identity.kind, apiVersion, validationErrors)
}

func builtinKubernetesValidationDiagnostic(value RuntimeValue, kind, apiVersion string, validationErrors []error) error {
	messages := make([]string, 0, len(validationErrors))
	var primary RuntimeValue
	primaryFound := false
	var related []SourceDiagnosticLocation
	for _, validationError := range validationErrors {
		messages = append(messages, validationError.Error())
		var path []string
		if fieldError, ok := errors.AsType[*field.Error](validationError); ok {
			path = customResourceFieldPath(fieldError.Field)
		} else if schemaError, ok := errors.AsType[*jsonschema.ValidationError](validationError); ok {
			path = builtinJSONSchemaInstancePath(schemaError)
		} else if before, _, ok := strings.Cut(validationError.Error(), ": unknown field"); ok {
			path = customResourceFieldPath(before)
		}
		if len(path) == 0 {
			continue
		}
		current, locations := customResourceRuntimeFieldValueAtPath(value, path)
		related = appendSourceLocations(related, locations, "", SourceSpan{}, "")
		metadata := current.Metadata()
		if !primaryFound && metadata.SourcePath != "" && metadata.Span.End > metadata.Span.Start {
			primary = current
			primaryFound = true
		}
	}
	message := fmt.Sprintf("kubernetes resource %s (%s): %s", kind, apiVersion, strings.Join(messages, "; "))
	if primaryFound {
		metadata := primary.Metadata()
		related = withoutSourceLocation(related, metadata.SourcePath, metadata.Span)

		return SourceDiagnosticError{Path: metadata.SourcePath, Code: "MCL-K8S", Message: message, Span: metadata.Span, Related: related, OmitSourceSnippet: metadata.OmitSourceSnippet, Cause: errors.Join(validationErrors...)}
	}

	return fmt.Errorf("%s: %w", message, errors.Join(validationErrors...))
}

func builtinJSONSchemaInstancePath(validationError *jsonschema.ValidationError) []string {
	path := validationError.InstanceLocation
	for _, cause := range validationError.Causes {
		if causePath := builtinJSONSchemaInstancePath(cause); len(causePath) > len(path) {
			path = causePath
		}
	}

	return path
}

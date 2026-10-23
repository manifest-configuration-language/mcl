package engine

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	crdcel "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/listtype"
	crdvalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
)

type crdVersionSchema struct {
	validator  crdvalidation.SchemaValidator
	structural *structuralschema.Structural
	typeExpr   *ResolvedType
	cel        *crdcel.Validator
	location   SourceDiagnosticLocation
	served     bool
}

type customResourceDefinitionKey struct {
	group   string
	version string
	kind    string
}

// KubernetesのCustomResourceDefinitionのKind名。
const kindCustomResourceDefinition = "CustomResourceDefinition"

func validateRuntimeBundleCustomResources(artifacts []RuntimeArtifact) error {
	schemas := make(map[customResourceDefinitionKey]crdVersionSchema)
	for index := range artifacts {
		artifact := &artifacts[index]
		resource := runtimeNodeContents(artifact.Value)
		kind, _ := runtimeStringField(resource, "kind")
		if kind != kindCustomResourceDefinition {
			continue
		}
		if err := registerRuntimeCustomResourceDefinition(schemas, resource); err != nil {
			metadata := artifact.Value.Metadata()

			return sourceDiagnostic(metadata.SourcePath, metadata.Span, fmt.Errorf("CustomResourceDefinition: %w", err))
		}
	}
	for index := range artifacts {
		artifact := &artifacts[index]
		resource := runtimeNodeContents(artifact.Value)
		apiVersion, hasAPIVersion := runtimeStringField(resource, "apiVersion")
		kind, hasKind := runtimeStringField(resource, "kind")
		if !hasAPIVersion || !hasKind {
			continue
		}
		group, version, namespaced := strings.Cut(apiVersion, "/")
		if !namespaced || strings.Contains(version, "/") {
			continue
		}
		registered, exists := schemas[customResourceDefinitionKey{group: group, version: version, kind: kind}]
		if !exists {
			continue
		}
		metadata := artifact.Value.Metadata()
		if !registered.served {
			return sourceDiagnostic(metadata.SourcePath, metadata.Span, fmt.Errorf("custom resource %s uses unserved version %q", kind, apiVersion))
		}
		if registered.typeExpr != nil {
			annotateRuntimeSchemaProvenance(&artifact.Value, apiVersion+"/"+kind, "", registered.typeExpr)
		}
		instance, err := runtimeValueAsKubernetesJSON(resource)
		if err != nil {
			return fmt.Errorf("custom resource %s: %w", kind, err)
		}
		validationErrors := crdvalidation.ValidateCustomResource(field.NewPath("resource"), instance, registered.validator)
		if instanceMap, ok := instance.(map[string]any); ok {
			validationErrors = append(validationErrors, listtype.ValidateListSetsAndMaps(field.NewPath("resource"), registered.structural, instanceMap)...)
		}
		if registered.cel != nil {
			celErrors, _ := registered.cel.Validate(context.Background(), field.NewPath("resource"), registered.structural, instance, nil, int64(celconfig.PerCallLimit))
			validationErrors = append(validationErrors, celErrors...)
		}
		if len(validationErrors) != 0 {
			return fmt.Errorf("custom resource %s (%s): %w", kind, apiVersion, customResourceRuntimeValidationDiagnostic(artifact.Value, validationErrors, registered.location))
		}
	}

	return nil
}

func registerRuntimeCustomResourceDefinition(schemas map[customResourceDefinitionKey]crdVersionSchema, value RuntimeValue) error {
	apiVersion, hasAPIVersion := runtimeStringField(value, "apiVersion")
	if !hasAPIVersion || apiVersion != "apiextensions.k8s.io/v1" {
		return fmt.Errorf("unsupported apiVersion %q", apiVersion)
	}
	spec, exists := runtimeMapValue(value, "spec")
	if !exists {
		return errors.New("spec is missing")
	}
	group, hasGroup := runtimeStringField(spec, "group")
	names, hasNames := runtimeMapValue(spec, "names")
	kind, hasKind := runtimeStringField(names, "kind")
	if !hasGroup || !hasNames || !hasKind {
		return errors.New("spec.group and spec.names.kind are required")
	}
	versions, exists := runtimeMapValue(spec, "versions")
	if !exists || (versions.Kind() != List && versions.Kind() != AdapterBundle) || len(versions.List) == 0 {
		return errors.New("spec.versions must be a non-empty List")
	}
	for _, versionEntry := range versions.List {
		version, hasVersion := runtimeStringField(versionEntry, "name")
		served, hasServed := runtimeMapValue(versionEntry, "served")
		if !hasVersion || !hasServed || served.Kind() != Bool {
			return errors.New("each spec.versions entry requires name and Bool served fields")
		}
		key := customResourceDefinitionKey{group: group, version: version, kind: kind}
		if _, duplicate := schemas[key]; duplicate {
			return fmt.Errorf("duplicate schema for %s %s/%s", kind, group, version)
		}
		registered := crdVersionSchema{served: served.BoolValue()}
		if served.BoolValue() {
			openAPISchema, location, err := customResourceOpenAPISchemaRuntime(versionEntry)
			if err != nil {
				return fmt.Errorf("schema for %s %s/%s: %w", kind, group, version, err)
			}
			validator, structural, celValidator, err := compileCRDValidatorJSON(openAPISchema)
			if err != nil {
				return fmt.Errorf("compile schema for %s %s/%s: %w", kind, group, version, err)
			}
			registered.validator = validator
			registered.structural = structural
			typeExpr := structuralSchemaType(structural, 0)
			registered.typeExpr = &typeExpr
			registered.cel = celValidator
			registered.location = location
		}
		schemas[key] = registered
	}

	return nil
}

func customResourceOpenAPISchemaRuntime(version RuntimeValue) (any, SourceDiagnosticLocation, error) {
	versionSchema, exists := runtimeMapValue(version, "schema")
	if !exists {
		return nil, SourceDiagnosticLocation{}, errors.New("schema is missing")
	}
	openAPISchema, exists := runtimeMapValue(versionSchema, "openAPIV3Schema")
	if !exists || runtimeNodeContents(openAPISchema).Kind() != Map {
		return nil, SourceDiagnosticLocation{}, errors.New("schema.openAPIV3Schema must be a Map")
	}
	metadata := openAPISchema.Metadata()
	location := SourceDiagnosticLocation{Path: metadata.SourcePath, Span: metadata.Span, Message: "CRD OpenAPI schema"}
	data, err := runtimeValueAsJSON(openAPISchema)
	if err != nil {
		return nil, SourceDiagnosticLocation{}, err
	}

	return data, location, nil
}

func customResourceRuntimeValidationDiagnostic(value RuntimeValue, validationErrors field.ErrorList, schemaLocation SourceDiagnosticLocation) error {
	messages := make([]string, 0, len(validationErrors))
	var primary RuntimeValue
	var primaryFound bool
	var related []SourceDiagnosticLocation
	for _, validationError := range validationErrors {
		messages = append(messages, validationError.Error())
		current, locations := customResourceRuntimeFieldValue(value, strings.TrimPrefix(validationError.Field, "resource"))
		related = appendSourceLocations(related, locations, "", SourceSpan{}, "")
		metadata := current.Metadata()
		if !primaryFound && metadata.SourcePath != "" && metadata.Span.End > metadata.Span.Start {
			primary = current
			primaryFound = true
		}
	}
	related = appendSourceLocations(related, nil, schemaLocation.Path, schemaLocation.Span, schemaLocation.Message)
	message := strings.Join(messages, "; ")
	if primaryFound {
		metadata := primary.Metadata()
		related = withoutSourceLocation(related, metadata.SourcePath, metadata.Span)

		return SourceDiagnosticError{Path: metadata.SourcePath, Code: "MCL-CRD", Message: message, Span: metadata.Span, Related: related, Cause: fmt.Errorf("%s", message)}
	}
	if len(related) != 0 {
		return SourceDiagnosticError{Code: "MCL-CRD", Message: message, Related: related, Cause: fmt.Errorf("%s", message)}
	}

	return fmt.Errorf("%s", message)
}

func customResourceRuntimeFieldValue(value RuntimeValue, path string) (RuntimeValue, []SourceDiagnosticLocation) {
	current := runtimeNodeContents(value)
	var related []SourceDiagnosticLocation
	for _, segment := range customResourceFieldPath(path) {
		current = runtimeNodeContents(current)
		metadata := current.Metadata()
		if current.Kind() == Map || current.Kind() == List || current.Kind() == AdapterBundle {
			related = appendSourceLocations(related, metadata.SourceLocations, metadata.SourcePath, metadata.Span, "Containing value")
		}
		var next RuntimeValue
		var exists bool
		//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
		switch current.Kind() {
		case Map:
			next, exists = runtimeMapValue(current, segment)
		case List, AdapterBundle:
			index, err := strconv.Atoi(segment)
			if err == nil && index >= 0 && index < len(current.List) {
				next, exists = current.List[index], true
			}
		}
		if !exists {
			return current, related
		}
		current = next
	}
	current = runtimeNodeContents(current)
	metadata := current.Metadata()
	related = appendSourceLocations(related, metadata.SourceLocations, "", SourceSpan{}, "")

	return current, related
}

func compileCRDValidatorJSON(data any) (crdvalidation.SchemaValidator, *structuralschema.Structural, *crdcel.Validator, error) {
	encoded, err := jsonv2.Marshal(data)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode openAPIV3Schema: %w", err)
	}
	var versionedSchema apiextensionsv1.JSONSchemaProps
	if err := jsonv2.Unmarshal(encoded, &versionedSchema); err != nil {
		return nil, nil, nil, fmt.Errorf("decode openAPIV3Schema: %w", err)
	}
	internalSchema := new(apiextensions.JSONSchemaProps)
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&versionedSchema, internalSchema, nil); err != nil {
		return nil, nil, nil, fmt.Errorf("convert openAPIV3Schema: %w", err)
	}
	structural, err := structuralschema.NewStructural(internalSchema)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("convert to structural schema: %w", err)
	}
	validator, _, err := crdvalidation.NewSchemaValidator(internalSchema)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create schema validator: %w", err)
	}
	celValidator := crdcel.NewValidator(structural, true, celconfig.PerCallLimit)

	return validator, structural, celValidator, nil
}

func runtimeValueAsKubernetesJSON(value RuntimeValue) (any, error) {
	value = runtimeNodeContents(value)
	switch value.Kind() {
	case Null:
		return nil, nil
	case Bool:
		return value.BoolValue(), nil
	case Integer:
		if _, err := parseRuntimeInteger(value.TextValue()); err != nil {
			return nil, fmt.Errorf("integer %q exceeds the %d-digit limit", value.TextValue(), maxNumericCoefficientDigits)
		}
		converted, err := strconv.ParseInt(value.TextValue(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("integer %q is outside Kubernetes' supported range", value.TextValue())
		}

		return converted, nil
	case Decimal:
		parsed, err := parseRuntimeDecimal(value.TextValue())
		if err != nil {
			return nil, err
		}
		if err := validateRuntimeDecimal(parsed); err != nil {
			return nil, err
		}
		converted, err := strconv.ParseFloat(value.TextValue(), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid Decimal value %q", value.TextValue())
		}

		return converted, nil
	case String:
		return value.TextValue(), nil
	case List, AdapterBundle:
		result := make([]any, len(value.List))
		for index, item := range value.List {
			converted, err := runtimeValueAsKubernetesJSON(item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			result[index] = converted
		}

		return result, nil
	case Map:
		result := make(map[string]any, len(value.Map))
		for _, entry := range value.Map {
			converted, err := runtimeValueAsKubernetesJSON(entry.Value)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", entry.Key, err)
			}
			result[entry.Key] = converted
		}

		return result, nil
	default:
		return nil, fmt.Errorf("cannot validate %s as a Kubernetes resource", kindName(value.Kind()))
	}
}

func customResourceFieldPath(path string) []string {
	var segments []string
	for len(path) > 0 {
		path = strings.TrimPrefix(path, ".")
		fieldEnd := strings.IndexAny(path, ".[ ")
		if fieldEnd < 0 {
			segments = append(segments, path)

			break
		}
		if fieldEnd > 0 {
			segments = append(segments, path[:fieldEnd])
			path = path[fieldEnd:]

			continue
		}
		if path[0] == '[' {
			end := strings.IndexByte(path, ']')
			if end < 0 {
				break
			}
			segments = append(segments, path[1:end])
			path = path[end+1:]

			continue
		}
		path = path[1:]
	}

	return segments
}

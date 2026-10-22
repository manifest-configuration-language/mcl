package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/pkg/mcl"
)

func explainCommand(ctx context.Context, filePath, apiVersion, kind, name, namespace, phase, operation, path string) error {
	if apiVersion == "" || kind == "" || name == "" {
		return errors.New("explain requires --api-version, --kind, and --name")
	}
	segments, err := decodeJSONPointer(path)
	if err != nil {
		return err
	}

	var bundle mcl.Bundle
	if operation != "" {
		bundle, err = mcl.EvaluateFileForOperationContext(ctx, filePath, operation)
	} else {
		bundle, err = mcl.EvaluateFileContext(ctx, filePath)
	}
	if err != nil {
		return err
	}
	catalog, err := mcl.LoadKubernetesResourceCatalogForFile(filePath)
	if err != nil {
		return err
	}
	resourceScope, knownScope, err := engine.KubernetesResourceScopeForBundle(bundle, catalog, apiVersion, kind)
	if err != nil {
		return err
	}
	matches := make([]mcl.Artifact, 0, 1)
	for _, artifact := range bundle.Artifacts {
		if !slices.Contains(artifact.Lifecycle.Events, phase) {
			continue
		}
		if resourceMatches(artifact.Value, apiVersion, kind, name, namespace, knownScope && resourceScope == mcl.KubernetesResourceScopeNamespaced) {
			matches = append(matches, artifact)
		}
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("no resource matches apiVersion %q, kind %q, name %q", apiVersion, kind, name)
	case 1:
	default:
		return fmt.Errorf("resource selection is ambiguous: %d artifacts match apiVersion %q, kind %q, name %q", len(matches), apiVersion, kind, name)
	}

	value, locations, err := selectValue(matches[0].Value, segments)
	if err != nil {
		return fmt.Errorf("select --path %q: %w", path, err)
	}
	output, err := mcl.RenderPhase(mcl.Bundle{Artifacts: []mcl.Artifact{{Value: value, Lifecycle: mcl.Lifecycle{Events: []string{"main"}}}}}, "main")
	if err != nil {
		return fmt.Errorf("render selected value: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Resource: %s %s/%s", apiVersion, kind, name)
	if namespace != "" {
		fmt.Fprintf(os.Stdout, " namespace=%s", namespace)
	}
	fmt.Fprintf(os.Stdout, "\nPath: %s\n\nRendered YAML:\n", path)
	if _, err := os.Stdout.Write(output); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Provenance:")
	if len(locations) == 0 {
		fmt.Fprintln(os.Stdout, "  No source locations are available for this value.")

		return nil
	}
	for _, location := range locations {
		printExplainLocation(location)
	}

	return nil
}

func resourceMatches(value mcl.Value, apiVersion, kind, name, namespace string, defaultNamespaceApplies bool) bool {
	api, hasAPI := mapField(value, "apiVersion")
	resourceKind, hasKind := mapField(value, "kind")
	metadata, hasMetadata := mapField(value, "metadata")
	resourceName, hasName := mapField(metadata, "name")
	api = unwrapNode(api)
	resourceKind = unwrapNode(resourceKind)
	resourceName = unwrapNode(resourceName)
	if !hasAPI || !hasKind || !hasMetadata || !hasName || api.Kind != mcl.String || resourceKind.Kind != mcl.String || resourceName.Kind != mcl.String {
		return false
	}
	if api.Text != apiVersion || resourceKind.Text != kind || resourceName.Text != name {
		return false
	}
	if namespace == "" {
		return true
	}
	resourceNamespace, hasNamespace := mapField(metadata, "namespace")
	resourceNamespace = unwrapNode(resourceNamespace)
	if !hasNamespace {
		return namespace == "default" && defaultNamespaceApplies
	}

	return resourceNamespace.Kind == mcl.String && resourceNamespace.Text == namespace
}

func selectValue(root mcl.Value, segments []string) (mcl.Value, []mcl.SourceDiagnosticLocation, error) {
	current := root
	var locations []mcl.SourceDiagnosticLocation
	for _, segment := range segments {
		switch unwrapNode(current).Kind {
		case mcl.Map:
			entries := unwrapNode(current).Map
			found := false
			for _, entry := range entries {
				if entry.Key != segment {
					continue
				}
				locations = append(locations, entry.KeySourceLocations...)
				if entry.KeySourcePath != "" || entry.KeySpan.End > entry.KeySpan.Start {
					locations = append(locations, mcl.SourceDiagnosticLocation{Path: entry.KeySourcePath, Span: entry.KeySpan, Message: "Map key"})
				}
				current = entry.Value
				found = true

				break
			}
			if !found {
				return mcl.Value{}, nil, fmt.Errorf("map key %q does not exist", segment)
			}
		case mcl.List:
			index, err := parseListIndex(segment)
			if err != nil {
				return mcl.Value{}, nil, err
			}
			list := unwrapNode(current).List
			if index >= len(list) {
				return mcl.Value{}, nil, fmt.Errorf("list index %d is out of range", index)
			}
			current = list[index]
		default:
			return mcl.Value{}, nil, fmt.Errorf("cannot select segment %q from a non-container value", segment)
		}
	}
	locations = append(locations, current.SourceLocations...)
	if current.SourcePath != "" || current.Span.End > current.Span.Start {
		locations = append(locations, mcl.SourceDiagnosticLocation{Path: current.SourcePath, Span: current.Span, Message: "Value definition"})
	}

	return current, uniqueExplainLocations(locations), nil
}

func decodeJSONPointer(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, errors.New("JSON Pointer must be empty or start with '/'")
	}
	encoded := strings.Split(pointer[1:], "/")
	segments := make([]string, 0, len(encoded))
	for _, segment := range encoded {
		var decoded strings.Builder
		for index := 0; index < len(segment); index++ {
			if segment[index] != '~' {
				decoded.WriteByte(segment[index])

				continue
			}
			if index+1 >= len(segment) {
				return nil, fmt.Errorf("invalid JSON Pointer escape in %q", pointer)
			}
			index++
			switch segment[index] {
			case '0':
				decoded.WriteByte('~')
			case '1':
				decoded.WriteByte('/')
			default:
				return nil, fmt.Errorf("invalid JSON Pointer escape in %q", pointer)
			}
		}
		segments = append(segments, decoded.String())
	}

	return segments, nil
}

func parseListIndex(segment string) (int, error) {
	if segment == "" || (len(segment) > 1 && segment[0] == '0') {
		return 0, fmt.Errorf("list index %q must be a non-negative integer without leading zeroes", segment)
	}
	for _, digit := range segment {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("list index %q must be a non-negative integer", segment)
		}
	}
	index, err := strconv.Atoi(segment)
	if err != nil || index < 0 {
		return 0, fmt.Errorf("list index %q must be a non-negative integer", segment)
	}

	return index, nil
}

func mapField(value mcl.Value, key string) (mcl.Value, bool) {
	for _, entry := range unwrapNode(value).Map {
		if entry.Key == key {
			return entry.Value, true
		}
	}

	return mcl.Value{}, false
}

func unwrapNode(value mcl.Value) mcl.Value {
	for value.Kind == mcl.Node && value.Base != nil {
		value = *value.Base
	}

	return value
}

func uniqueExplainLocations(locations []mcl.SourceDiagnosticLocation) []mcl.SourceDiagnosticLocation {
	unique := make([]mcl.SourceDiagnosticLocation, 0, len(locations))
	for _, location := range locations {
		duplicate := false
		for _, existing := range unique {
			if existing.Path == location.Path && existing.Span == location.Span && existing.Message == location.Message {
				duplicate = true

				break
			}
		}
		if !duplicate {
			unique = append(unique, location)
		}
	}

	return unique
}

func printExplainLocation(location mcl.SourceDiagnosticLocation) {
	label := location.Message
	if label == "" {
		label = "Source location"
	}
	path := location.Path
	if path == "" {
		path = unknownPath
	}
	source, err := os.ReadFile(location.Path)
	if err == nil && location.Path != "" && location.Span.Start >= 0 && location.Span.Start <= len(source) {
		line, column, lineText := sourceLineAt(source, location.Span.Start)
		fmt.Fprintf(os.Stdout, "  %s: %s:%d:%d\n", label, path, line, column)
		fmt.Fprintf(os.Stdout, "    %d | %s\n", line, lineText)
		fmt.Fprintf(os.Stdout, "      | %s^\n", strings.Repeat(" ", max(column-1, 0)))

		return
	}
	fmt.Fprintf(os.Stdout, "  %s: %s", label, path)
	if location.Span.End > location.Span.Start {
		fmt.Fprintf(os.Stdout, ": %d-%d", location.Span.Start, location.Span.End)
	}
	fmt.Fprintln(os.Stdout)
}

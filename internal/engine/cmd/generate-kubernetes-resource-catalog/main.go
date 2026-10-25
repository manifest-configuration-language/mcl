// Command generate-kubernetes-resource-catalog creates the static resource
// scope catalog from the checked-in Kubernetes OpenAPI specification.
package main

import (
	"compress/gzip"
	jsonv2 "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
)

type groupVersionKind struct {
	Group   string `json:"group"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
}

type schemaReference struct {
	Ref string `json:"$ref"`
}

type response struct {
	Schema schemaReference `json:"schema"`
}

type operation struct {
	Responses map[string]response `json:"responses"`
}

type pathItem struct {
	Get operation `json:"get"`
}

type definition struct {
	GroupVersionKinds []groupVersionKind `json:"x-kubernetes-group-version-kind"`
}

type specification struct {
	Paths       map[string]pathItem   `json:"paths"`
	Definitions map[string]definition `json:"definitions"`
}

type resourceKey struct {
	group   string
	version string
	kind    string
}

type resource struct {
	name       string
	namespaced bool
}

func main() {
	input := flag.String("input", "", "path to a gzipped Kubernetes OpenAPI v2 specification")
	output := flag.String("output", "", "path to the generated Go source file")
	flag.Parse()
	if *input == "" || *output == "" {
		fatalf("both -input and -output are required")
	}

	if err := generate(*input, *output); err != nil {
		fatalf("generate Kubernetes resource catalog: %v", err)
	}
}

func generate(inputPath, outputPath string) error {
	input, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer input.Close()

	compressed, err := gzip.NewReader(input)
	if err != nil {
		return fmt.Errorf("read input gzip header: %w", err)
	}
	defer compressed.Close()

	var spec specification
	if err := jsonv2.UnmarshalRead(compressed, &spec); err != nil {
		return fmt.Errorf("decode OpenAPI specification: %w", err)
	}

	resources, err := discoverResources(spec)
	if err != nil {
		return err
	}
	content, err := format.Source(render(resources, path.Base(inputPath)))
	if err != nil {
		return fmt.Errorf("format generated source: %w", err)
	}
	if err := os.WriteFile(outputPath, content, 0o600); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

func discoverResources(spec specification) (map[resourceKey]resource, error) {
	resources := make(map[resourceKey]resource)
	for route, item := range spec.Paths {
		group, version, resourceName, namespaced, ok := parseCollectionRoute(route)
		if !ok {
			continue
		}

		result, exists := item.Get.Responses["200"]
		if !exists || result.Schema.Ref == "" {
			continue
		}

		definitionName := path.Base(result.Schema.Ref)
		listDefinition, exists := spec.Definitions[definitionName]
		if !exists {
			return nil, fmt.Errorf("%s references missing definition %s", route, definitionName)
		}
		for _, gvk := range listDefinition.GroupVersionKinds {
			if !strings.HasSuffix(gvk.Kind, "List") {
				continue
			}
			if gvk.Group != group || gvk.Version != version {
				return nil, fmt.Errorf("%s references list GVK %s/%s %s", route, gvk.Group, gvk.Version, gvk.Kind)
			}
			kind := strings.TrimSuffix(gvk.Kind, "List")
			if kind == "" {
				return nil, fmt.Errorf("%s references an empty resource kind", route)
			}
			key := resourceKey{group: group, version: version, kind: kind}
			current := resources[key]
			if current.name != "" && current.name != resourceName {
				return nil, fmt.Errorf("%s %s resolves to resource names %q and %q", group, kind, current.name, resourceName)
			}
			current.name = resourceName
			current.namespaced = current.namespaced || namespaced
			resources[key] = current
		}
	}

	if len(resources) == 0 {
		return nil, errors.New("the OpenAPI specification contains no resource collection routes")
	}
	groupResources := make(map[resourceKey]resource, len(resources))
	for key, item := range resources {
		if item.name == "" {
			return nil, fmt.Errorf("incomplete descriptor for %s/%s", key.group, key.kind)
		}
		groupKey := resourceKey{group: key.group, kind: key.kind}
		previous, exists := groupResources[groupKey]
		if exists && (previous.name != item.name || previous.namespaced != item.namespaced) {
			return nil, fmt.Errorf("inconsistent descriptors across API versions for %s/%s", key.group, key.kind)
		}
		groupResources[groupKey] = item
	}

	return resources, nil
}

func parseCollectionRoute(route string) (group, version, resource string, namespaced, ok bool) {
	parts := strings.Split(strings.Trim(route, "/"), "/")
	var suffix []string
	switch {
	case len(parts) >= 3 && parts[0] == "api":
		version = parts[1]
		suffix = parts[2:]
	case len(parts) >= 4 && parts[0] == "apis":
		group = parts[1]
		version = parts[2]
		suffix = parts[3:]
	default:
		return "", "", "", false, false
	}

	switch {
	case len(suffix) == 1:
		resource = suffix[0]
	case len(suffix) == 3 && suffix[0] == "namespaces" && suffix[1] == "{namespace}":
		resource = suffix[2]
		namespaced = true
	default:
		return "", "", "", false, false
	}
	if resource == "" || strings.ContainsAny(resource, "{}") {
		return "", "", "", false, false
	}

	return group, version, resource, namespaced, true
}

func render(resources map[resourceKey]resource, source string) []byte {
	keys := make([]resourceKey, 0, len(resources))
	for key := range resources {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(left, right resourceKey) int {
		if result := strings.Compare(left.group, right.group); result != 0 {
			return result
		}
		if result := strings.Compare(left.version, right.version); result != 0 {
			return result
		}

		return strings.Compare(left.kind, right.kind)
	})

	var output strings.Builder
	output.WriteString("// Code generated by go generate; DO NOT EDIT.\n")
	fmt.Fprintf(&output, "// Source: checked-in Kubernetes OpenAPI v2 catalog %s.\n\n", source)
	output.WriteString("package engine\n\n")
	output.WriteString("var builtinKubernetesResourceDescriptors = resourceScopeIndexFromCatalog([]kubernetesResourceCatalogEntry{\n")
	for _, key := range keys {
		item := resources[key]
		scope := "kubernetesResourceCluster"
		if item.namespaced {
			scope = "kubernetesResourceNamespaced"
		}
		fmt.Fprintf(&output, "\t{group: %s, version: %s, kind: %s, resourceName: %s, scope: %s},\n",
			strconv.Quote(key.group), strconv.Quote(key.version), strconv.Quote(key.kind), strconv.Quote(item.name), scope)
	}
	output.WriteString("})\n")

	return []byte(output.String())
}

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", arguments...)
	os.Exit(1)
}

package engine

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/kustomize/kyaml/openapi"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

type diffListTopology uint8

const (
	diffListUnknown diffListTopology = iota
	diffListMap
	diffListSet
	diffListAtomic
)

type bundleDiffSchema struct {
	properties  map[string]*bundleDiffSchema
	items       *bundleDiffSchema
	listType    diffListTopology
	listMapKeys []string
}

func diffSchemaResourceKey(value Value) string {
	value = nodeContents(value)
	apiVersion, _ := diffMapString(value, "apiVersion")
	kind, _ := diffMapString(value, "kind")

	return apiVersion + "\x00" + kind
}

func bundleDiffSchemas(bundle Bundle) (map[string]*bundleDiffSchema, error) {
	schemas := make(map[string]*bundleDiffSchema)
	for _, artifact := range bundle.Artifacts {
		value := nodeContents(artifact.Value)
		kind, _ := diffMapString(value, "kind")
		if kind != kindCustomResourceDefinition {
			continue
		}
		if err := addCustomResourceDiffSchemas(schemas, value); err != nil {
			return nil, err
		}
	}
	for _, artifact := range bundle.Artifacts {
		value := nodeContents(artifact.Value)
		apiVersion, hasVersion := diffMapString(value, "apiVersion")
		kind, hasKind := diffMapString(value, "kind")
		if !hasVersion || !hasKind {
			continue
		}
		key := apiVersion + "\x00" + kind
		if _, exists := schemas[key]; exists {
			continue
		}
		resourceSchema := openapi.SchemaForResourceType(yaml.TypeMeta{APIVersion: apiVersion, Kind: kind})
		if resourceSchema == nil || resourceSchema.Schema == nil {
			continue
		}
		definitions := openapi.Schema().Definitions
		schemas[key] = bundleDiffSchemaFromOpenAPI(resourceSchema.Schema, definitions, 0, make(map[string]bool))
	}

	return schemas, nil
}

func addCustomResourceDiffSchemas(schemas map[string]*bundleDiffSchema, crd Value) error {
	spec, exists := diffMapValue(crd, "spec")
	if !exists {
		return nil
	}
	group, hasGroup := diffMapString(spec, "group")
	names, hasNames := diffMapValue(spec, "names")
	kind, hasKind := diffMapString(names, "kind")
	versions, hasVersions := diffMapValue(spec, "versions")
	if !hasGroup || !hasNames || !hasKind || !hasVersions || versions.Kind != List {
		return nil
	}
	for _, versionEntry := range versions.List {
		version, hasVersion := diffMapString(versionEntry, "name")
		served, hasServed := diffMapValue(versionEntry, "served")
		if !hasVersion || !hasServed || served.Kind != Bool || !served.Bool {
			continue
		}
		versionSchema, hasSchema := diffMapValue(versionEntry, "schema")
		openAPISchema, hasOpenAPISchema := diffMapValue(versionSchema, "openAPIV3Schema")
		if !hasSchema || !hasOpenAPISchema {
			continue
		}
		document, err := diffValueAsJSON(openAPISchema)
		if err != nil {
			return fmt.Errorf("encode CustomResourceDefinition schema for %s/%s %s: %w", group, version, kind, err)
		}
		encoded, err := jsonv2.Marshal(document)
		if err != nil {
			return fmt.Errorf("encode CustomResourceDefinition schema for %s/%s %s: %w", group, version, kind, err)
		}
		var versioned apiextensionsv1.JSONSchemaProps
		if err := jsonv2.Unmarshal(encoded, &versioned); err != nil {
			return fmt.Errorf("decode CustomResourceDefinition schema for %s/%s %s: %w", group, version, kind, err)
		}
		internal := new(apiextensions.JSONSchemaProps)
		if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&versioned, internal, nil); err != nil {
			return fmt.Errorf("convert CustomResourceDefinition schema for %s/%s %s: %w", group, version, kind, err)
		}
		structural, err := structuralschema.NewStructural(internal)
		if err != nil {
			return fmt.Errorf("build CustomResourceDefinition schema for %s/%s %s: %w", group, version, kind, err)
		}
		key := group + "/" + version + "\x00" + kind
		if _, duplicate := schemas[key]; duplicate {
			return fmt.Errorf("duplicate CustomResourceDefinition schema for %s/%s %s", group, version, kind)
		}
		schemas[key] = bundleDiffSchemaFromStructural(structural)
	}

	return nil
}

func bundleDiffSchemaFromStructural(schema *structuralschema.Structural) *bundleDiffSchema {
	if schema == nil {
		return nil
	}
	result := &bundleDiffSchema{properties: make(map[string]*bundleDiffSchema), items: bundleDiffSchemaFromStructural(schema.Items), listMapKeys: append([]string(nil), schema.XListMapKeys...)}
	switch {
	case schema.XListType == nil:
		result.listType = diffListUnknown
	case *schema.XListType == "map":
		result.listType = diffListMap
	case *schema.XListType == "set":
		result.listType = diffListSet
	case *schema.XListType == "atomic":
		result.listType = diffListAtomic
	}
	for name, property := range schema.Properties {
		result.properties[name] = bundleDiffSchemaFromStructural(&property)
	}
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Structural != nil {
		result.properties["*"] = bundleDiffSchemaFromStructural(schema.AdditionalProperties.Structural)
	}

	return result
}

func bundleDiffSchemaFromOpenAPI(schema *spec.Schema, definitions spec.Definitions, depth int, active map[string]bool) *bundleDiffSchema {
	if schema == nil || depth > 128 {
		return nil
	}
	var result *bundleDiffSchema
	if schema.Ref.String() != "" {
		name, found := strings.CutPrefix(schema.Ref.String(), "#/definitions/")
		if !found || active[name] {
			return nil
		}
		name, err := url.PathUnescape(name)
		if err != nil {
			return nil
		}
		name = strings.ReplaceAll(strings.ReplaceAll(name, "~1", "/"), "~0", "~")
		definition, exists := definitions[name]
		if !exists {
			return nil
		}
		active[name] = true
		result = bundleDiffSchemaFromOpenAPI(&definition, definitions, depth+1, active)
		delete(active, name)
	}
	if result == nil {
		result = &bundleDiffSchema{properties: make(map[string]*bundleDiffSchema)}
	}
	listType, listKeys := openAPIDiffListTopology(schema)
	if listType != diffListUnknown {
		result.listType = listType
		result.listMapKeys = listKeys
	}
	if schema.Items != nil && schema.Items.Schema != nil {
		result.items = bundleDiffSchemaFromOpenAPI(schema.Items.Schema, definitions, depth+1, active)
	}
	for name, property := range schema.Properties {
		result.properties[name] = bundleDiffSchemaFromOpenAPI(&property, definitions, depth+1, active)
	}
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		result.properties["*"] = bundleDiffSchemaFromOpenAPI(schema.AdditionalProperties.Schema, definitions, depth+1, active)
	}
	for _, alternative := range schema.AllOf {
		result = mergeBundleDiffSchemas(result, bundleDiffSchemaFromOpenAPI(&alternative, definitions, depth+1, active))
	}

	return result
}

func mergeBundleDiffSchemas(left, right *bundleDiffSchema) *bundleDiffSchema {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	if left.properties == nil {
		left.properties = make(map[string]*bundleDiffSchema)
	}
	for name, property := range right.properties {
		left.properties[name] = mergeBundleDiffSchemas(left.properties[name], property)
	}
	if right.items != nil {
		left.items = mergeBundleDiffSchemas(left.items, right.items)
	}
	if left.listType == diffListUnknown && right.listType != diffListUnknown {
		left.listType = right.listType
		left.listMapKeys = slices.Clone(right.listMapKeys)
	}

	return left
}

func diffSchemaProperty(schema *bundleDiffSchema, name string) *bundleDiffSchema {
	if schema == nil {
		return nil
	}
	if property := schema.properties[name]; property != nil {
		return property
	}

	return schema.properties["*"]
}

func diffSchemaItems(schema *bundleDiffSchema) *bundleDiffSchema {
	if schema == nil {
		return nil
	}

	return schema.items
}

func compatibleDiffListTopology(before, after *bundleDiffSchema) (diffListTopology, []string, bool) {
	if before == nil || after == nil || before.listType == diffListUnknown || before.listType != after.listType {
		return diffListUnknown, nil, false
	}
	switch before.listType {
	case diffListMap:
		if len(before.listMapKeys) == 0 || !equalStringSlice(before.listMapKeys, after.listMapKeys) {
			return diffListUnknown, nil, false
		}

		return diffListMap, before.listMapKeys, true
	case diffListSet, diffListAtomic:
		return before.listType, nil, true
	default:
		return diffListUnknown, nil, false
	}
}

func diffMapList(path, resource string, before, after Value, beforeItemSchema, afterItemSchema *bundleDiffSchema, keys []string, changes *[]BundleChange, walk func(string, Value, Value, *bundleDiffSchema, *bundleDiffSchema)) bool {
	oldEntries, ok := indexedDiffMapList(before.List, keys)
	if !ok {
		return false
	}
	newEntries, ok := indexedDiffMapList(after.List, keys)
	if !ok {
		return false
	}
	identities := make([]string, 0, len(oldEntries)+len(newEntries))
	seen := make(map[string]struct{}, len(oldEntries)+len(newEntries))
	for identity := range oldEntries {
		identities = append(identities, identity)
		seen[identity] = struct{}{}
	}
	for identity := range newEntries {
		if _, exists := seen[identity]; !exists {
			identities = append(identities, identity)
		}
	}
	slices.Sort(identities)
	for _, identity := range identities {
		itemPath := path + "/@map/" + escapeDiffPathSegment(identity)
		old, oldExists := oldEntries[identity]
		current, newExists := newEntries[identity]
		switch {
		case !oldExists:
			*changes = append(*changes, BundleChange{Kind: BundleFieldAdded, Resource: resource, Path: itemPath, After: formatDiffValue(current)})
		case !newExists:
			*changes = append(*changes, BundleChange{Kind: BundleFieldRemoved, Resource: resource, Path: itemPath, Before: formatDiffValue(old)})
		default:
			walk(itemPath, old, current, beforeItemSchema, afterItemSchema)
		}
	}

	return true
}

func indexedDiffMapList(items []Value, keys []string) (map[string]Value, bool) {
	entries := make(map[string]Value, len(items))
	for _, item := range items {
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			value, exists := diffMapValue(item, key)
			if !exists || !diffListKeyScalar(value) {
				return nil, false
			}
			parts = append(parts, key+"="+diffIdentityValue(value))
		}
		encoded, err := jsonv2.Marshal(parts)
		if err != nil {
			return nil, false
		}
		identity := string(encoded)
		if _, duplicate := entries[identity]; duplicate {
			return nil, false
		}
		entries[identity] = item
	}

	return entries, true
}

func diffSetList(path, resource string, before, after Value, changes *[]BundleChange) bool {
	oldEntries, newEntries := make(map[string]Value, len(before.List)), make(map[string]Value, len(after.List))
	for _, item := range before.List {
		key := diffIdentityValue(item)
		if _, duplicate := oldEntries[key]; duplicate {
			return false
		}
		oldEntries[key] = item
	}
	for _, item := range after.List {
		key := diffIdentityValue(item)
		if _, duplicate := newEntries[key]; duplicate {
			return false
		}
		newEntries[key] = item
	}
	keys := make([]string, 0, len(oldEntries)+len(newEntries))
	seen := make(map[string]struct{}, len(oldEntries)+len(newEntries))
	for key := range oldEntries {
		keys = append(keys, key)
		seen[key] = struct{}{}
	}
	for key := range newEntries {
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		itemPath := path + "/@set/" + escapeDiffPathSegment(key)
		old, oldExists := oldEntries[key]
		current, newExists := newEntries[key]
		switch {
		case !oldExists:
			*changes = append(*changes, BundleChange{Kind: BundleFieldAdded, Resource: resource, Path: itemPath, After: formatDiffValue(current)})
		case !newExists:
			*changes = append(*changes, BundleChange{Kind: BundleFieldRemoved, Resource: resource, Path: itemPath, Before: formatDiffValue(old)})
		}
	}

	return true
}

func diffListKeyScalar(value Value) bool {
	switch nodeContents(value).Kind {
	case Bool, Integer, Decimal, String:
		return true
	default:
		return false
	}
}

func escapeDiffPathSegment(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func diffIdentityValue(value Value) string {
	value = nodeContents(value)
	switch value.Kind {
	case Integer:
		if integer, err := parseRuntimeInteger(value.Text); err == nil {
			return integer.String()
		}
	case Decimal:
		if decimal, err := parseRuntimeDecimal(value.Text); err == nil {
			return decimal.String()
		}
	case List:
		items := make([]string, len(value.List))
		for index, item := range value.List {
			items[index] = diffIdentityValue(item)
		}
		encoded, err := jsonv2.Marshal(items)
		if err == nil {
			return string(encoded)
		}
	case Map:
		entries := diffMapEntries(value)
		keys := make([]string, 0, len(entries))
		for key := range entries {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		items := make([]string, 0, len(keys))
		for _, key := range keys {
			encodedKey, err := jsonv2.Marshal(key)
			if err != nil {
				return formatDiffValue(value)
			}
			items = append(items, string(encodedKey)+":"+diffIdentityValue(entries[key]))
		}
		encoded, err := jsonv2.Marshal(items)
		if err == nil {
			return string(encoded)
		}
	case Null, Bool, String, AdapterBundle, Replacement, Function, Lambda, Node, Rule:
		return formatDiffValue(value)
	}

	return formatDiffValue(value)
}

func diffValueAsJSON(value Value) (any, error) {
	value = nodeContents(value)
	switch value.Kind {
	case Null:
		return nil, nil
	case Bool:
		return value.Bool, nil
	case Integer:
		converted, err := strconv.ParseInt(value.Text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("integer %q is outside the supported range", value.Text)
		}

		return converted, nil
	case Decimal:
		converted, err := strconv.ParseFloat(value.Text, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid decimal %q", value.Text)
		}

		return converted, nil
	case String:
		return value.Text, nil
	case List:
		result := make([]any, len(value.List))
		for index, item := range value.List {
			converted, err := diffValueAsJSON(item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			result[index] = converted
		}

		return result, nil
	case Map:
		result := make(map[string]any, len(value.Map))
		for _, entry := range value.Map {
			converted, err := diffValueAsJSON(entry.Value)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", entry.Key, err)
			}
			result[entry.Key] = converted
		}

		return result, nil
	default:
		return nil, fmt.Errorf("cannot convert %s to JSON", kindName(value.Kind))
	}
}

func openAPIDiffListTopology(schema *spec.Schema) (diffListTopology, []string) {
	if schema == nil {
		return diffListUnknown, nil
	}
	listType, _ := schema.Extensions["x-kubernetes-list-type"].(string)
	keys, _ := schema.Extensions["x-kubernetes-list-map-keys"].([]any)
	listMapKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		if text, ok := key.(string); ok {
			listMapKeys = append(listMapKeys, text)
		}
	}
	switch listType {
	case "map":
		return diffListMap, listMapKeys
	case "set":
		return diffListSet, nil
	case "atomic":
		return diffListAtomic, nil
	default:
		return diffListUnknown, nil
	}
}

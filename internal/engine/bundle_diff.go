package engine

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// BundleChangeKindは差分の種類を表します。
type BundleChangeKind uint8

const (
	// BundleResourceAddedはBundleにResourceが追加されたことを表します。
	BundleResourceAdded BundleChangeKind = iota + 1
	// BundleResourceRemovedはBundleからResourceが削除されたことを表します。
	BundleResourceRemoved
	// BundleFieldAddedはResourceにフィールドが追加されたことを表します。
	BundleFieldAdded
	// BundleFieldRemovedはResourceからフィールドが削除されたことを表します。
	BundleFieldRemoved
	// BundleFieldChangedはResourceのフィールド値が変更されたことを表します。
	BundleFieldChanged
	// BundleLifecycleChangedはResourceのLifecycleが変更されたことを表します。
	BundleLifecycleChanged
)

// BundleChangeは二つのBundle間にある一つの変更を表します。
type BundleChange struct {
	Kind     BundleChangeKind
	Resource string
	Path     string
	Before   string
	After    string
}

type bundleDiffArtifact struct {
	artifact Artifact
	identity string
	key      string
}

// DiffBundlesはResource、フィールド、Lifecycleの差分をBundle順に返します。
func DiffBundles(before, after Bundle) ([]BundleChange, error) {
	defaultCatalog := DefaultKubernetesResourceCatalog()

	return DiffBundlesWithCatalogs(before, after, defaultCatalog, defaultCatalog)
}

// DiffBundlesWithCatalogsは各BundleのCatalogを使ってResource identityを解決し、差分を返します。
func DiffBundlesWithCatalogs(before, after Bundle, beforeCatalog, afterCatalog KubernetesResourceCatalog) ([]BundleChange, error) {
	beforeScopes, err := newKubernetesResourceScopeIndexWithCatalog(before.Artifacts, beforeCatalog)
	if err != nil {
		return nil, fmt.Errorf("resolve before Resource scopes: %w", err)
	}
	afterScopes, err := newKubernetesResourceScopeIndexWithCatalog(after.Artifacts, afterCatalog)
	if err != nil {
		return nil, fmt.Errorf("resolve after Resource scopes: %w", err)
	}
	beforeArtifacts := indexBundleDiffArtifacts(before, beforeScopes)
	afterArtifacts := indexBundleDiffArtifacts(after, afterScopes)
	beforeSchemas, err := bundleDiffSchemas(before)
	if err != nil {
		return nil, fmt.Errorf("resolve before Resource schemas: %w", err)
	}
	afterSchemas, err := bundleDiffSchemas(after)
	if err != nil {
		return nil, fmt.Errorf("resolve after Resource schemas: %w", err)
	}
	afterIndexes := make(map[string]int, len(afterArtifacts))
	for index, artifact := range afterArtifacts {
		afterIndexes[artifact.key] = index
	}
	matched := make([]bool, len(afterArtifacts))
	changes := make([]BundleChange, 0)

	for _, old := range beforeArtifacts {
		newIndex, ok := afterIndexes[old.key]
		if !ok {
			changes = append(changes, BundleChange{Kind: BundleResourceRemoved, Resource: old.identity})

			continue
		}
		matched[newIndex] = true
		current := afterArtifacts[newIndex]
		changes = append(changes, diffBundleValues(old.identity, old.artifact.Value, current.artifact.Value, beforeSchemas[diffSchemaResourceKey(old.artifact.Value)], afterSchemas[diffSchemaResourceKey(current.artifact.Value)])...)
		if !sameLifecycle(old.artifact.Lifecycle, current.artifact.Lifecycle) {
			changes = append(changes, BundleChange{
				Kind: BundleLifecycleChanged, Resource: old.identity,
				Before: formatLifecycle(old.artifact.Lifecycle), After: formatLifecycle(current.artifact.Lifecycle),
			})
		}
	}
	for index, current := range afterArtifacts {
		if !matched[index] {
			changes = append(changes, BundleChange{Kind: BundleResourceAdded, Resource: current.identity})
		}
	}

	return changes, nil
}

func indexBundleDiffArtifacts(bundle Bundle, scopes kubernetesResourceScopeIndex) []bundleDiffArtifact {
	artifacts := make([]bundleDiffArtifact, len(bundle.Artifacts))
	occurrences := make(map[string]int, len(bundle.Artifacts))
	for index, artifact := range bundle.Artifacts {
		resourceIdentity, matchKey, ok := bundleResourceIdentity(artifact.Value, scopes)
		if !ok {
			identity := fmt.Sprintf("artifact[%d]", index)
			artifacts[index] = bundleDiffArtifact{artifact: artifact, identity: identity, key: fmt.Sprintf("artifact:%d", index)}

			continue
		}
		occurrence := occurrences[matchKey]
		occurrences[matchKey]++
		identity := resourceIdentity
		key := matchKey
		if occurrence > 0 {
			identity = fmt.Sprintf("%s#%d", identity, occurrence+1)
			key = fmt.Sprintf("%s#%d", key, occurrence+1)
		}
		artifacts[index] = bundleDiffArtifact{artifact: artifact, identity: identity, key: key}
	}

	return artifacts
}

func bundleResourceIdentity(value Value, scopes kubernetesResourceScopeIndex) (string, string, bool) {
	value = nodeContents(value)
	resourceIdentity, ok := kubernetesResourceIdentityOf(value)
	if !ok {
		return "", "", false
	}
	apiVersion, _ := diffMapString(value, "apiVersion")
	group := resourceIdentity.group
	if group == "" {
		group = "core"
	}
	namespace := resourceIdentity.namespace
	//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
	switch scopes[kubernetesResourceKey{group: resourceIdentity.group, kind: resourceIdentity.kind}].scope {
	case kubernetesResourceNamespaced:
		if namespace == "" {
			namespace = "default"
		}
	case kubernetesResourceCluster:
		namespace = ""
	}
	display := strings.Join([]string{apiVersion, resourceIdentity.kind, namespace, resourceIdentity.name}, "/")
	key := strings.Join([]string{"resource", group, resourceIdentity.kind, namespace, resourceIdentity.name}, "\x00")

	return display, key, true
}

func diffBundleValues(resource string, before, after Value, beforeSchema, afterSchema *bundleDiffSchema) []BundleChange {
	changes := make([]BundleChange, 0)
	var walk func(string, Value, Value, *bundleDiffSchema, *bundleDiffSchema)
	walk = func(path string, old, current Value, oldSchema, currentSchema *bundleDiffSchema) {
		old = nodeContents(old)
		current = nodeContents(current)
		if equalDiffValue(old, current) {
			return
		}
		if old.Kind == Map && current.Kind == Map {
			oldEntries := diffMapEntries(old)
			newEntries := diffMapEntries(current)
			keys := make([]string, 0, len(oldEntries)+len(newEntries))
			seen := make(map[string]struct{}, len(oldEntries)+len(newEntries))
			for key := range oldEntries {
				keys = append(keys, key)
				seen[key] = struct{}{}
			}
			for key := range newEntries {
				if _, ok := seen[key]; !ok {
					keys = append(keys, key)
				}
			}
			slices.Sort(keys)
			for _, key := range keys {
				oldValue, oldOK := oldEntries[key]
				newValue, newOK := newEntries[key]
				fieldPath := path + "/" + escapeDiffPathSegment(key)
				oldChildSchema := diffSchemaProperty(oldSchema, key)
				newChildSchema := diffSchemaProperty(currentSchema, key)
				switch {
				case !oldOK:
					changes = append(changes, BundleChange{Kind: BundleFieldAdded, Resource: resource, Path: fieldPath, After: formatDiffValue(newValue)})
				case !newOK:
					changes = append(changes, BundleChange{Kind: BundleFieldRemoved, Resource: resource, Path: fieldPath, Before: formatDiffValue(oldValue)})
				default:
					walk(fieldPath, oldValue, newValue, oldChildSchema, newChildSchema)
				}
			}

			return
		}
		if old.Kind == List && current.Kind == List {
			if listType, keys, ok := compatibleDiffListTopology(oldSchema, currentSchema); ok {
				switch listType {
				case diffListUnknown:
				case diffListMap:
					if diffMapList(path, resource, old, current, oldSchema.items, currentSchema.items, keys, &changes, walk) {
						return
					}
				case diffListSet:
					if diffSetList(path, resource, old, current, &changes) {
						return
					}
				case diffListAtomic:
					changes = append(changes, BundleChange{Kind: BundleFieldChanged, Resource: resource, Path: path, Before: formatDiffValue(old), After: formatDiffValue(current)})

					return
				}
			}
			if len(old.List) != len(current.List) {
				changes = append(changes, BundleChange{Kind: BundleFieldChanged, Resource: resource, Path: path, Before: formatDiffValue(old), After: formatDiffValue(current)})

				return
			}
			for index, item := range old.List {
				walk(fmt.Sprintf("%s/%d", path, index), item, current.List[index], diffSchemaItems(oldSchema), diffSchemaItems(currentSchema))
			}

			return
		}
		changes = append(changes, BundleChange{Kind: BundleFieldChanged, Resource: resource, Path: path, Before: formatDiffValue(old), After: formatDiffValue(current)})
	}
	walk("", before, after, beforeSchema, afterSchema)

	return changes
}

func equalDiffValue(left, right Value) bool {
	left = nodeContents(left)
	right = nodeContents(right)
	if left.Kind != right.Kind {
		return false
	}
	switch left.Kind {
	case Null:
		return true
	case Bool:
		return left.Bool == right.Bool
	case Decimal:
		leftDecimal, leftErr := parseRuntimeDecimal(left.Text)
		rightDecimal, rightErr := parseRuntimeDecimal(right.Text)

		return leftErr == nil && rightErr == nil && compareRuntimeDecimals(leftDecimal, rightDecimal) == 0
	case Integer, String:
		return left.Text == right.Text
	case List:
		if len(left.List) != len(right.List) {
			return false
		}
		for index := range left.List {
			if !equalDiffValue(left.List[index], right.List[index]) {
				return false
			}
		}

		return true
	case Map:
		leftEntries := diffMapEntries(left)
		rightEntries := diffMapEntries(right)
		if len(leftEntries) != len(rightEntries) {
			return false
		}
		for key, value := range leftEntries {
			other, ok := rightEntries[key]
			if !ok || !equalDiffValue(value, other) {
				return false
			}
		}

		return true
	default:
		return left.Text == right.Text
	}
}

func diffMapEntries(value Value) map[string]Value {
	entries := make(map[string]Value, len(value.Map))
	for _, entry := range value.Map {
		entries[entry.Key] = entry.Value
	}

	return entries
}

func diffMapValue(value Value, key string) (Value, bool) {
	for _, entry := range value.Map {
		if entry.Key == key {
			return nodeContents(entry.Value), true
		}
	}

	return Value{}, false
}

func diffMapString(value Value, key string) (string, bool) {
	field, ok := diffMapValue(value, key)
	if !ok || field.Kind != String {
		return "", false
	}

	return field.Text, true
}

func sameLifecycle(left, right Lifecycle) bool {
	return equalStringSlice(left.Events, right.Events) && left.Priority == right.Priority && equalStringSlice(left.Cleanup, right.Cleanup) && left.Wait == right.Wait && left.Origin == right.Origin
}

func equalStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}

	return true
}

func formatLifecycle(lifecycle Lifecycle) string {
	return fmt.Sprintf("events=%v priority=%d cleanup=%v wait=%t origin=%q", lifecycle.Events, lifecycle.Priority, lifecycle.Cleanup, lifecycle.Wait, lifecycle.Origin)
}

func formatDiffValue(value Value) string {
	value = nodeContents(value)
	switch value.Kind {
	case Null:
		return "null"
	case Bool:
		return strconv.FormatBool(value.Bool)
	case Integer, Decimal:
		return value.Text
	case String:
		return strconv.Quote(value.Text)
	case List:
		items := make([]string, len(value.List))
		for index, item := range value.List {
			items[index] = formatDiffValue(item)
		}

		return "[" + strings.Join(items, ", ") + "]"
	case Map:
		entries := diffMapEntries(value)
		keys := make([]string, 0, len(entries))
		for key := range entries {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		items := make([]string, 0, len(keys))
		for _, key := range keys {
			items = append(items, strconv.Quote(key)+": "+formatDiffValue(entries[key]))
		}

		return "{" + strings.Join(items, ", ") + "}"
	default:
		return "<unresolved>"
	}
}

package kubernetes

import (
	"fmt"
	"slices"
	"strings"
)

// CanonicalizeCRDDescriptors deduplicates CRD descriptors by API group and kind, rejects conflicting descriptors, and sorts the result.
func CanonicalizeCRDDescriptors(descriptors []Descriptor) ([]Descriptor, error) {
	byKind := make(map[ResourceKey]Descriptor, len(descriptors))
	for _, descriptor := range descriptors {
		key := ResourceKey{Group: descriptor.APIGroup, Kind: descriptor.Kind}
		if existing, exists := byKind[key]; exists {
			if existing != descriptor {
				return nil, fmt.Errorf("CRD versions have conflicting resource descriptors for %s", resourceKeyName(key))
			}
			continue
		}
		byKind[key] = descriptor
	}

	canonical := make([]Descriptor, 0, len(byKind))
	for _, descriptor := range byKind {
		canonical = append(canonical, descriptor)
	}
	slices.SortFunc(canonical, func(left, right Descriptor) int {
		if comparison := strings.Compare(left.APIGroup, right.APIGroup); comparison != 0 {
			return comparison
		}
		return strings.Compare(left.Kind, right.Kind)
	})

	return canonical, nil
}

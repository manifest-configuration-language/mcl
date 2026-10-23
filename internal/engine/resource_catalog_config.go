package engine

// LoadKubernetesResourceCatalogForFile preserves the public API while returning
// the built-in catalog. Project configuration no longer defines resource
// descriptors.
//
// Deprecated: use DefaultKubernetesResourceCatalog.
func LoadKubernetesResourceCatalogForFile(string) (KubernetesResourceCatalog, error) {
	return DefaultKubernetesResourceCatalog(), nil
}

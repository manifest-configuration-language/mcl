package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const maxProjectConfigBytes = 1 << 20

type projectConfig struct {
	Kubernetes struct {
		ResourceCatalog struct {
			Revision            string                      `toml:"revision"`
			AdditionalResources []projectResourceDescriptor `toml:"additionalResources"`
		} `toml:"resourceCatalog"`
	} `toml:"kubernetes"`
}

type projectResourceDescriptor struct {
	APIGroup     string                  `toml:"apiGroup"`
	Kind         string                  `toml:"kind"`
	ResourceName string                  `toml:"resourceName"`
	Scope        KubernetesResourceScope `toml:"scope"`
}

// LoadKubernetesResourceCatalogForFileは入力MCLファイルのProject設定からResource Catalogを読み込みます。
func LoadKubernetesResourceCatalogForFile(entryPath string) (KubernetesResourceCatalog, error) {
	absolute, err := filepath.Abs(entryPath)
	if err != nil {
		return KubernetesResourceCatalog{}, fmt.Errorf("resolve input file path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return KubernetesResourceCatalog{}, fmt.Errorf("resolve input file %q: %w", entryPath, err)
	}
	root, err := discoverProjectRoot(filepath.Dir(canonical))
	if err != nil {
		return KubernetesResourceCatalog{}, err
	}

	return loadKubernetesResourceCatalog(root)
}

func loadKubernetesResourceCatalog(root string) (KubernetesResourceCatalog, error) {
	configPath := filepath.Join(root, "mcl.toml")
	info, err := os.Lstat(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultKubernetesResourceCatalog(), nil
	}
	if err != nil {
		return KubernetesResourceCatalog{}, fmt.Errorf("inspect project configuration %q: %w", configPath, err)
	}
	if !info.Mode().IsRegular() {
		return KubernetesResourceCatalog{}, fmt.Errorf("project configuration %q must be a regular file", configPath)
	}
	data, err := readLimitedFileWithLimit(configPath, maxProjectConfigBytes)
	if err != nil {
		return KubernetesResourceCatalog{}, fmt.Errorf("read project configuration %q: %w", configPath, err)
	}
	var config projectConfig
	metadata, err := toml.Decode(string(data), &config)
	if err != nil {
		return KubernetesResourceCatalog{}, fmt.Errorf("decode project configuration %q: %w", configPath, err)
	}
	for _, key := range metadata.Undecoded() {
		if len(key) != 0 && key[0] == "kubernetes" {
			return KubernetesResourceCatalog{}, fmt.Errorf("unknown Kubernetes Resource Catalog key %q in %s", key.String(), configPath)
		}
	}
	if !metadata.IsDefined("kubernetes", "resourceCatalog") {
		return DefaultKubernetesResourceCatalog(), nil
	}
	catalog := KubernetesResourceCatalog{
		Revision:            config.Kubernetes.ResourceCatalog.Revision,
		AdditionalResources: make([]KubernetesResourceDescriptor, 0, len(config.Kubernetes.ResourceCatalog.AdditionalResources)),
	}
	for _, descriptor := range config.Kubernetes.ResourceCatalog.AdditionalResources {
		catalog.AdditionalResources = append(catalog.AdditionalResources, KubernetesResourceDescriptor(descriptor))
	}
	if _, err := newKubernetesResourceScopeIndexWithCatalog(nil, catalog); err != nil {
		return KubernetesResourceCatalog{}, fmt.Errorf("invalid Kubernetes Resource Catalog in %s: %w", configPath, err)
	}

	return catalog, nil
}

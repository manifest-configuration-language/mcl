package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

type projectConfig struct {
	CRDSchemas []string `toml:"crd_schemas"`
}

func loadCRDSchemaCatalog(files *projectFilesystem) (map[customResourceDefinitionKey]crdVersionSchema, error) {
	configPath := filepath.Join(files.root, projectConfigFileName)
	configData, err := files.readLimitedFile(configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("read project configuration: %w", err)
	}
	var config projectConfig
	metadata, err := toml.Decode(string(configData), &config)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", projectConfigFileName, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		return nil, fmt.Errorf("decode %s: unsupported setting %q", projectConfigFileName, undecoded[0].String())
	}

	paths := slices.Clone(config.CRDSchemas)
	slices.Sort(paths)
	for index, schemaPath := range paths {
		if schemaPath == "" || filepath.IsAbs(schemaPath) || strings.Contains(schemaPath, "\\") {
			return nil, fmt.Errorf("%s: crd_schemas entries must be non-empty project-relative paths", projectConfigFileName)
		}
		if index > 0 && paths[index-1] == schemaPath {
			return nil, fmt.Errorf("%s: duplicate crd_schemas path %q", projectConfigFileName, schemaPath)
		}
	}

	catalog := make(map[customResourceDefinitionKey]crdVersionSchema)
	for _, schemaPath := range paths {
		absolutePath := filepath.Join(files.root, filepath.FromSlash(schemaPath))
		data, err := files.readLimitedFile(absolutePath)
		if err != nil {
			return nil, fmt.Errorf("read CRD schema %q: %w", schemaPath, err)
		}
		documents, err := decodeRuntimeYAMLDocuments(data, absolutePath)
		if err != nil {
			return nil, fmt.Errorf("decode CRD schema %q: %w", schemaPath, err)
		}
		if len(documents.List) == 0 {
			return nil, fmt.Errorf("CRD schema %q contains no documents", schemaPath)
		}
		for documentIndex, document := range documents.List {
			kind, _ := runtimeStringField(document, "kind")
			if kind != kindCustomResourceDefinition {
				return nil, fmt.Errorf("CRD schema %q document %d has kind %q, expected %q", schemaPath, documentIndex+1, kind, kindCustomResourceDefinition)
			}
			if err := registerRuntimeCustomResourceDefinition(catalog, document); err != nil {
				return nil, fmt.Errorf("CRD schema %q document %d: %w", schemaPath, documentIndex+1, err)
			}
		}
	}

	return catalog, nil
}

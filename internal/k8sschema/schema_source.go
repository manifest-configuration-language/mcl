package k8sschema

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"sync"

	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/kustomize/kyaml/openapi"
)

const (
	kubernetesSchemaVersion      = "v1.37.1"
	kubernetesSchemaSourceCommit = "f78e722310e50bcaca9276be22276d9e91d91308"
	kubernetesSchemaSHA256       = "465276aedf437726de5a6ed23a41b86d78bb0d347edf251a755ae26a93cdd8a6"
	maxKubernetesSchemaSize      = 16 << 20
)

// Source: https://raw.githubusercontent.com/kubernetes/kubernetes/f78e722310e50bcaca9276be22276d9e91d91308/api/openapi-spec/swagger.json
//
//go:embed data/kubernetes-v1.37.1.swagger.json.gz
var kubernetesSchemaArtifact []byte

type schemaCatalog struct {
	definitions spec.Definitions
	resources   map[schemaGVK]*spec.Schema
}

type schemaGVK struct {
	apiVersion string
	kind       string
}

// OpenAPISchemaはGVKのSchemaと参照先定義を保持します。
type OpenAPISchema struct {
	Schema      *spec.Schema
	Definitions spec.Definitions
}

// OpenAPIElementsは配列要素を指定するLookupのパス要素です。
const OpenAPIElements = "[]"

var loadSchemaCatalog = sync.OnceValues(buildSchemaCatalog)

// BuiltinOpenAPIは現行Schemaを優先し、未収録GVKではlegacy Schemaを返します。
func BuiltinOpenAPI(apiVersion, kind string) (*OpenAPISchema, bool, error) {
	catalog, err := loadSchemaCatalog()
	if err != nil {
		return nil, false, err
	}
	resource := catalog.resources[schemaGVK{apiVersion: apiVersion, kind: kind}]
	if resource == nil {
		return nil, false, nil
	}

	return &OpenAPISchema{Schema: resource, Definitions: catalog.definitions}, true, nil
}

// LookupはSchemaのフィールドパスを参照先定義まで解決します。
func (schema *OpenAPISchema) Lookup(path ...string) *spec.Schema {
	if schema == nil {
		return nil
	}
	current := resolveOpenAPIReference(schema.Schema, schema.Definitions)
	for _, field := range path {
		if current == nil {
			return nil
		}
		if field == OpenAPIElements {
			if current.Items == nil || current.Items.Schema == nil {
				return nil
			}
			current = current.Items.Schema
		} else if property, exists := current.Properties[field]; exists {
			current = &property
		} else if current.AdditionalProperties != nil && current.AdditionalProperties.Schema != nil {
			current = current.AdditionalProperties.Schema
		} else {
			return nil
		}
		current = resolveOpenAPIReference(current, schema.Definitions)
	}

	return current
}

func resolveOpenAPIReference(schema *spec.Schema, definitions spec.Definitions) *spec.Schema {
	for schema != nil && schema.Ref.String() != "" {
		root := &spec.Schema{SchemaProps: spec.SchemaProps{Definitions: definitions}}
		resolved, err := openapi.Resolve(&schema.Ref, root)
		if err != nil {
			return nil
		}
		schema = resolved
	}

	return schema
}

func buildSchemaCatalog() (*schemaCatalog, error) {
	legacy := openapi.Schema()
	catalog := &schemaCatalog{
		definitions: make(spec.Definitions, len(legacy.Definitions)),
		resources:   make(map[schemaGVK]*spec.Schema),
	}
	maps.Copy(catalog.definitions, legacy.Definitions)
	indexDefinitions(catalog, legacy.Definitions)

	data, err := decompressKubernetesSchema(kubernetesSchemaArtifact)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != kubernetesSchemaSHA256 {
		return nil, fmt.Errorf("kubernetes %s OpenAPI schema from %s has a digest mismatch", kubernetesSchemaVersion, kubernetesSchemaSourceCommit)
	}

	var swagger spec.Swagger
	if err := json.Unmarshal(data, &swagger); err != nil {
		return nil, fmt.Errorf("decode Kubernetes %s OpenAPI schema: %w", kubernetesSchemaVersion, err)
	}
	maps.Copy(catalog.definitions, swagger.Definitions)
	indexDefinitions(catalog, swagger.Definitions)

	return catalog, nil
}

func decompressKubernetesSchema(artifact []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(artifact))
	if err != nil {
		return nil, fmt.Errorf("open compressed Kubernetes %s OpenAPI schema: %w", kubernetesSchemaVersion, err)
	}
	defer reader.Close()

	data, err := io.ReadAll(io.LimitReader(reader, maxKubernetesSchemaSize+1))
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes %s OpenAPI schema: %w", kubernetesSchemaVersion, err)
	}
	if len(data) > maxKubernetesSchemaSize {
		return nil, fmt.Errorf("kubernetes %s OpenAPI schema exceeds %d bytes", kubernetesSchemaVersion, maxKubernetesSchemaSize)
	}

	return data, nil
}

func indexDefinitions(catalog *schemaCatalog, definitions spec.Definitions) {
	for _, definition := range definitions {
		groupVersionKinds, ok := definition.Extensions["x-kubernetes-group-version-kind"].([]any)
		if !ok {
			continue
		}
		for _, groupVersionKind := range groupVersionKinds {
			fields, ok := groupVersionKind.(map[string]any)
			if !ok {
				continue
			}
			apiVersion, ok := fields["version"].(string)
			if !ok {
				continue
			}
			if group, ok := fields["group"].(string); ok && group != "" {
				apiVersion = group + "/" + apiVersion
			}
			kind, ok := fields["kind"].(string)
			if !ok || kind == "" {
				continue
			}
			definitionCopy := definition
			catalog.resources[schemaGVK{apiVersion: apiVersion, kind: kind}] = &definitionCopy
		}
	}
}

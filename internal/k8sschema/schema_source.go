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
	"strings"
	"sync"

	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/kustomize/kyaml/openapi"
)

const (
	kubernetesSchemaVersion1371      = "v1.37.1"
	kubernetesSchemaSourceCommit1371 = "f78e722310e50bcaca9276be22276d9e91d91308"
	kubernetesSchemaSHA2561371       = "465276aedf437726de5a6ed23a41b86d78bb0d347edf251a755ae26a93cdd8a6"
	kubernetesSchemaVersion1364      = "v1.36.4"
	kubernetesSchemaSourceCommit1364 = "bb826b1d48562f110659e64e8ec444327433db95"
	kubernetesSchemaSHA2561364       = "dcede2063da1d7ad62ecb5af8adb6d7fabd0b52385a7fa0048afb491dac90450"
)

const (
	DefaultKubernetesVersion = kubernetesSchemaVersion1371
	maxKubernetesSchemaSize  = 16 << 20
)

// Source: https://raw.githubusercontent.com/kubernetes/kubernetes/f78e722310e50bcaca9276be22276d9e91d91308/api/openapi-spec/swagger.json
//
//go:embed data/kubernetes-v1.37.1.swagger.json.gz
var kubernetesSchemaArtifact []byte

// Source: https://raw.githubusercontent.com/kubernetes/kubernetes/bb826b1d48562f110659e64e8ec444327433db95/api/openapi-spec/swagger.json
//
//go:embed data/kubernetes-v1.36.4.swagger.json.gz
var kubernetesSchemaArtifact1364 []byte

type schemaCatalog struct {
	version     string
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

var loadSchemaCatalog = sync.OnceValues(func() (*schemaCatalog, error) {
	return buildSchemaCatalog(kubernetesSchemaVersion1371, kubernetesSchemaSourceCommit1371, kubernetesSchemaSHA2561371, kubernetesSchemaArtifact)
})

var loadSchemaCatalog1364 = sync.OnceValues(func() (*schemaCatalog, error) {
	return buildSchemaCatalog(kubernetesSchemaVersion1364, kubernetesSchemaSourceCommit1364, kubernetesSchemaSHA2561364, kubernetesSchemaArtifact1364)
})

// NormalizeKubernetesVersionは空の指定を既定の組み込みCatalogへ解決します。
func NormalizeKubernetesVersion(version string) (string, error) {
	if version == "" {
		return DefaultKubernetesVersion, nil
	}
	switch version {
	case kubernetesSchemaVersion1364, kubernetesSchemaVersion1371:
		return version, nil
	default:
		return "", fmt.Errorf("unsupported Kubernetes schema version %q; supported versions are %s", version, strings.Join(SupportedKubernetesVersions(), ", "))
	}
}

// SupportedKubernetesVersionsはこのMCL buildに含まれるversionを返します。
func SupportedKubernetesVersions() []string {
	return []string{kubernetesSchemaVersion1364, kubernetesSchemaVersion1371}
}

// BuiltinOpenAPIは対象KubernetesバージョンのSchemaを返します。
func BuiltinOpenAPI(apiVersion, kind string) (*OpenAPISchema, bool, error) {
	return BuiltinOpenAPIForVersion(DefaultKubernetesVersion, apiVersion, kind)
}

// BuiltinOpenAPIForVersionは指定したKubernetes Catalogからresource Schemaを検索します。
func BuiltinOpenAPIForVersion(version, apiVersion, kind string) (*OpenAPISchema, bool, error) {
	version, err := NormalizeKubernetesVersion(version)
	if err != nil {
		return nil, false, err
	}
	var catalog *schemaCatalog
	switch version {
	case kubernetesSchemaVersion1364:
		catalog, err = loadSchemaCatalog1364()
	default:
		catalog, err = loadSchemaCatalog()
	}
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

func buildSchemaCatalog(version, sourceCommit, expectedDigest string, artifact []byte) (*schemaCatalog, error) {
	catalog := &schemaCatalog{
		version:     version,
		definitions: make(spec.Definitions),
		resources:   make(map[schemaGVK]*spec.Schema),
	}

	data, err := decompressKubernetesSchema(version, artifact)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != expectedDigest {
		return nil, fmt.Errorf("kubernetes %s OpenAPI schema from %s has a digest mismatch", version, sourceCommit)
	}

	var swagger spec.Swagger
	if err := json.Unmarshal(data, &swagger); err != nil {
		return nil, fmt.Errorf("decode Kubernetes %s OpenAPI schema: %w", version, err)
	}
	maps.Copy(catalog.definitions, swagger.Definitions)
	indexDefinitions(catalog, swagger.Definitions)

	return catalog, nil
}

func decompressKubernetesSchema(version string, artifact []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(artifact))
	if err != nil {
		return nil, fmt.Errorf("open compressed Kubernetes %s OpenAPI schema: %w", version, err)
	}
	defer reader.Close()

	data, err := io.ReadAll(io.LimitReader(reader, maxKubernetesSchemaSize+1))
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes %s OpenAPI schema: %w", version, err)
	}
	if len(data) > maxKubernetesSchemaSize {
		return nil, fmt.Errorf("kubernetes %s OpenAPI schema exceeds %d bytes", version, maxKubernetesSchemaSize)
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

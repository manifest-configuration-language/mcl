# Project configuration

MCL reads `mcl.toml` from the project root for project-wide inputs. Paths in the configuration are relative to that root.

## CRD schemas

Add local Kubernetes CustomResourceDefinition files to `crd_schemas` to validate custom resources even when another application installs the CRDs.

```toml
crd_schemas = [
  "schemas/external-secrets.yaml",
  "schemas/gateway-api.yaml",
]
```

Each listed YAML file may contain one or more complete `apiextensions.k8s.io/v1` CustomResourceDefinition documents. MCL compiles their OpenAPI schemas with Kubernetes validation libraries and uses them to validate emitted custom resources. It also checks structural list constraints and CEL rules.

Catalog paths must remain within the project root. Duplicate paths and conflicting schemas for the same group, version, and kind are errors. Keep the CRD files in the same version-controlled project so the project revision identifies the schemas used during evaluation.

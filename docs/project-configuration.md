# Project configuration

MCL reads `mcl.toml` from the project root for project-wide inputs. Paths in the configuration are relative to that root.

## CRD schemas

Declare CRD schemas in `[[crd_schemas]]` entries. A local file can contain one or more complete `apiextensions.k8s.io/v1` CustomResourceDefinition documents.

```toml
[[crd_schemas]]
path = "schemas/local-crds.yaml"

[[crd_schemas]]
source = "https://example.org/operator/crds.yaml"

[[crd_schemas]]
source = "git+https://github.com/example/operator.git"
revision = "0123456789abcdef0123456789abcdef01234567"
directory = "config/crd/bases"

[[crd_schemas]]
source = "oci://ghcr.io/example/operator-chart"
version = "1.2.3"
```

HTTPS sources point to a YAML file and must not include a query or fragment. Git sources require a full lowercase commit SHA-1 and a repository directory; MCL reads YAML files from that directory. Set `file` to select one YAML file from a Git directory. OCI sources identify a versioned Helm chart; MCL reads static CRD files from the chart's `crds/` directory. Set `file` to select one file relative to `crds/`. Helm templates are not rendered to obtain schemas. Set `select_kind = "CustomResourceDefinition"` when a source also contains ordinary resources; MCL ignores other kinds and requires at least one matching CRD.

Run `mcl update` to fetch remote sources and record their content or archive digests in `mcl.lock`. Normal evaluation reads the locked cache and works offline. MCL compiles the schemas with Kubernetes validation libraries and checks emitted custom resources, structural list constraints, and CEL rules. Duplicate source entries and conflicting schemas for the same group, version, and kind are errors.

# Project configuration

MCL reads `mcl.toml` from the project root for project-wide inputs. Paths in the configuration are relative to that root.

## CRD schemas

Declare CRD schemas in `[[crd_schemas]]` entries using locked HTTPS, Git, or OCI sources. Local CRD files are not accepted. A source can provide one or more complete `apiextensions.k8s.io/v1` CustomResourceDefinition documents.

```toml
[[crd_schemas]]
source = "https://example.org/operator/crds.yaml"

[[crd_schemas]]
source = "git+https://github.com/example/operator.git"
revision = "0123456789abcdef0123456789abcdef01234567"
directory = "config/crd/bases"
files = ["operator.example.io_widgets.yaml", "operator.example.io_widgetschedules.yaml"]

[[crd_schemas]]
source = "oci://ghcr.io/example/operator-chart"
version = "1.2.3"
```

HTTPS sources point to a YAML file and must not include a query or fragment. Git sources require a full lowercase commit SHA-1 and a repository directory; MCL reads YAML files from that directory. Use `files` to select multiple YAML files from a Git directory. OCI sources identify a versioned Helm chart; MCL reads static CRD files from the chart's `crds/` directory. Use `files` to select multiple files relative to `crds/`. Entries with the same source identity are loaded once and their file selections are combined. Helm templates are not rendered to obtain schemas. MCL ignores non-CRD YAML documents and requires at least one valid CRD.

Run `mcl update` to fetch remote sources and record their content or archive digests in `mcl.lock`. Normal evaluation reads the locked cache and works offline. MCL compiles the schemas with Kubernetes validation libraries and checks emitted custom resources, structural list constraints, and CEL rules. Repeated file selections and conflicting schemas for the same group, version, and kind are errors.

Run `mcl fetch --locked main.mcl` to populate the cache from dependencies already recorded in `mcl.lock`. This command verifies each fetched artifact against its locked digest and does not update the lock. Normal `render`, `check`, and other evaluation commands do not fetch missing remote dependencies.

## Kubernetes schema version

MCL includes Kubernetes OpenAPI schemas for `v1.36.4` and `v1.37.1`. Evaluation uses `v1.37.1` by default. Pass `--kubernetes-version v1.36.4` to `render`, `check`, `explain`, `diff`, `update`, or `fetch` to validate resources against the cluster version. The library API accepts the same selection through `EvaluationOptions.KubernetesVersion`. Unsupported versions fail instead of silently using a different schema.

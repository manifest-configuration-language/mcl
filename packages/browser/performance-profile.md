# WebAssembly code size profile

Measured on 2026-10-22 at MCL commit `3eeb4806e5e68c335bf5626f550a1b82f365f621`, using Go 1.27.3, `GOOS=js`, `GOARCH=wasm`, and `CGO_ENABLED=0`.

## Overall sizes

| Measurement | Bytes | MiB |
| --- | ---: | ---: |
| Release WebAssembly | 113,123,893 | 107.84 |
| Release WebAssembly compressed with `gzip -9 -n` | 19,599,650 | 18.69 |
| WebAssembly Code section | 66,690,417 | 63.60 |
| Function bodies in the Code section | 66,565,838 | 63.48 |

The release binary is 330 bytes above the checked-in raw baseline and 663 bytes below the gzip baseline. An unstripped twin built with the same Go version and target settings had an identical Code section, so its function names could be used to attribute function body sizes without changing generated code.

## Method

Both binaries were built from `./cmd/mcl-wasm` with `-trimpath`; the unstripped build omitted only the release `-ldflags="-s -w"`. `wasm-tools objdump` confirmed the Code section had the same byte range and function count in both files. The function bodies and names were read from the unstripped Code and name sections. Names were assigned to the longest matching import path from `GOOS=js GOARCH=wasm go list -deps`, normalizing path separators and hyphens to underscores; 1,062,456 bytes could not be assigned. The compressed size was measured with `gzip -9 -n` on the stripped binary.

The builds can be repeated with:

```sh
mise exec -- env GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -trimpath -o /tmp/mcl-wasm-unstripped.wasm ./cmd/mcl-wasm
mise exec -- env GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tmp/mcl-wasm-release.wasm ./cmd/mcl-wasm
wasm-tools objdump /tmp/mcl-wasm-unstripped.wasm
wasm-tools objdump /tmp/mcl-wasm-release.wasm
gzip -9 -n -c /tmp/mcl-wasm-release.wasm | wc -c
```

## Package function body sizes

The table groups function bodies by Go import path. These values omit each function's body-length prefix and do not include data segments, type information, tables, or other WebAssembly sections.

| Package group | Function body bytes | MiB |
| --- | ---: | ---: |
| `k8s.io/api` | 23,557,807 | 22.47 |
| `github.com/google/cel-go` | 3,772,084 | 3.60 |
| `k8s.io/apimachinery` | 1,884,527 | 1.80 |
| `k8s.io/apiextensions-apiserver` | 1,794,004 | 1.71 |
| `sigs.k8s.io/kustomize` | 1,686,250 | 1.61 |
| `github.com/manifest-configuration-language/mcl/internal/engine` | 1,547,751 | 1.48 |
| `k8s.io/kube-openapi` | 1,327,070 | 1.27 |
| `helm.sh/helm/v4` | 306,888 | 0.29 |
| `github.com/manifest-configuration-language/mcl/internal/kustomizeadapter` | 111,130 | 0.11 |
| `github.com/manifest-configuration-language/mcl/internal/helmrender` | 102,185 | 0.10 |

The profiler assigned 65,503,382 bytes, or 98.4% of function body bytes, to Go packages. Unassigned bodies include compiler-generated wrappers and symbols without an import-path prefix. Inline code is attributed to its caller.

## Interpretation

Kubernetes API packages have the largest measured function body footprint. Helm's own package bodies are smaller, while Helm and schema validation pull in Kubernetes, OpenAPI, and CEL dependencies. These measurements do not identify the marginal size of a dependency: link-time dead-code removal, generated data, and compression affect the final binary independently. They do not support splitting or removing an adapter. Any such change needs a feature-specific build variant, output-parity checks, and a comparison of the complete raw and compressed artifacts.

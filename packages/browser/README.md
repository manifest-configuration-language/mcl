# @mcl/browser

`@mcl/browser` runs MCL parse, lint, format, check, evaluate, and render operations in a dedicated Web Worker. Its `prepack` step builds the WASM binary and copies the matching `wasm_exec.js` from the active Go toolchain so both runtime assets ship together.

```js
import { createMclRuntime } from "@mcl/browser";

const runtime = createMclRuntime();
try {
  const project = runtime.openProject({
    "main.mcl": source,
    "values.json": JSON.stringify(values),
  });
  const result = await project.render({ entrypoint: "main.mcl" });
  console.log(result.yaml);
} finally {
  runtime.dispose();
}
```

Pass an `AbortSignal` through the final options argument to cancel an operation. Cancellation or a timeout terminates the current worker; the runtime creates a fresh worker for the next operation. Operations are serialized because the current Go runtime uses one in-memory project root.

Project files can be UTF-8 strings or `Uint8Array` values. UTF-8 files remain inline in the JSON request. For compatibility, JSON callers can continue to send Base64 files inline. The Browser SDK copies each `Uint8Array` and transfers its buffer to the Worker; the Worker sends only `{ "encoding": "binary" }` metadata in the request JSON and passes a path-to-`Uint8Array` map as the second argument to `mclWasm.run(requestJSON, binaryFiles)`. The Go runtime copies those bytes into its project filesystem without Base64 conversion. The SDK-owned and caller-owned arrays remain reusable after each operation.

MCL decodes the request into a root-relative `io/fs` filesystem for project files, module imports, schemas, resource catalogs, and dependency locks. The runtime enforces 64 KiB per entrypoint source, 4,096 files, 16 MiB per file, 64 MiB for all project files including the entrypoint, and 1 MiB output. The worker blocks `fetch` after loading the WASM runtime assets, so MCL project evaluation cannot make external network requests.

The wire format is described in [`protocol.schema.json`](https://github.com/manifest-configuration-language/mcl/blob/main/cmd/mcl-wasm/protocol.schema.json). Go protocol models are generated with `go generate ./cmd/mcl-wasm`, and the browser package declarations are generated with `npm run generate:protocol`. The browser API declarations reuse the generated operation, diagnostic, and result types. Project files are not copied into `memfs`. The worker resets its `memfs` volume for each operation and retains `globalThis.fs`, `globalThis.path`, and `globalThis.process` only for the Go/WASM `os` calls that Helm and Kustomize use while materializing and processing temporary paths. These adapters still rely on disk-path APIs, so their temporary files use the isolated in-memory volume.

The worker depends on browser-compatible Node built-in shims for `memfs`. Vite consumers need `vite-plugin-node-polyfills` with protocol imports enabled; the package smoke test exercises this setup. Removing these remaining shims requires replacing the OS-path APIs used by Helm and Kustomize adapters.

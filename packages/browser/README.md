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

Project files can be UTF-8 strings or `Uint8Array` values. The worker mounts them in a fresh in-memory filesystem for each operation, then sends them to Go as JSON with binary files base64 encoded. The runtime enforces 64 KiB per entrypoint source, 4,096 files, 16 MiB per file, 64 MiB for all project files including the entrypoint, and 1 MiB output. The worker blocks `fetch` after loading the WASM runtime assets, so MCL project evaluation cannot make external network requests.

The wire format is described in [`protocol.schema.json`](https://github.com/manifest-configuration-language/mcl/blob/main/cmd/mcl-wasm/protocol.schema.json). The runtime currently wraps that protocol; schema-driven Go and TypeScript generation is not yet configured. The `globalThis.fs` adapter and `memfs` remain transitional shims for the Go/WASM `os` compatibility surface. The engine's injected filesystem work covers module and schema reads, while catalog and dependency lock access and Helm/Kustomize materialization still use OS filesystem APIs.

The worker currently depends on browser-compatible Node built-in shims through `memfs`. Vite consumers need `vite-plugin-node-polyfills` with protocol imports enabled; the package smoke test exercises this setup. Removing this bundler requirement depends on completing the engine filesystem boundary.

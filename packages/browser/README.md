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

Project files can be UTF-8 strings or `Uint8Array` values. UTF-8 files remain inline in the JSON request. The Browser SDK copies each `Uint8Array` and transfers its buffer to the Worker; the Worker sends only `{ "encoding": "binary" }` metadata in the request JSON and passes a path-to-`Uint8Array` map as the second argument to `mclWasm.run(requestJSON, binaryFiles)`. The Go runtime copies those bytes into its project filesystem. The SDK-owned and caller-owned arrays remain reusable after each operation.

MCL decodes the request into a root-relative `io/fs` filesystem for project files, module imports, schemas, resource catalogs, and dependency locks. The runtime enforces 64 KiB per entrypoint source, 4,096 files, 16 MiB per file, 64 MiB for all project files including the entrypoint, and 1 MiB output. The worker blocks `fetch` after loading the WASM runtime assets, so MCL project evaluation cannot make external network requests.

The wire format is described in [`protocol.schema.json`](https://github.com/manifest-configuration-language/mcl/blob/main/cmd/mcl-wasm/protocol.schema.json). Go protocol models are generated with `go generate ./cmd/mcl-wasm`, and the browser package declarations are generated with `npm run generate:protocol`. The browser API declarations reuse the generated operation, diagnostic, and result types. MCL decodes each request into a root-relative `io/fs` project filesystem. Kustomize project overlays and locked Git archives use kyaml's in-memory filesystem, and Helm source loading accepts an `io/fs` filesystem. The Go `wasm_exec.js` runtime supplies its standard browser fallbacks for process, path, and stdout APIs; the Browser SDK does not provide a Node-style project filesystem or require Node polyfills in Vite.

`npm run test:browser` records WASM transfer size, initialization, render, cancellation, and linear-memory use in Chromium, Firefox, and WebKit. Chromium also loads and renders a 32 MiB project. [The performance baseline](https://github.com/manifest-configuration-language/mcl/blob/main/packages/browser/performance-baseline.json) records one local reference run. [The performance budget](https://github.com/manifest-configuration-language/mcl/blob/main/packages/browser/performance-budget.json) limits WASM size growth, linear memory to 192 MiB, initialization to 10 seconds, single-operation rendering and cancellation to 1 second, and Chromium's 32 MiB project load and render to 2 and 5 seconds.

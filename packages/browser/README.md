# @manifest-configuration-language/browser

`@manifest-configuration-language/browser` runs MCL parse, lint, format, completion, definition, hover, check, evaluate, and render operations in a dedicated Web Worker. Its `prepack` step uses the shared WASM build script, which builds with the active Go toolchain, applies the mise-pinned Binaryen `wasm-opt -Oz`, selects the smaller binary, and copies the matching Go `wasm_exec.js` so both runtime assets ship together. GitHub Release assets use this same output. Update the Go and Binaryen pins in the repository's `mise.toml` when upgrading either tool.

```js
import { createMclRuntime } from "@manifest-configuration-language/browser";

const runtime = createMclRuntime();
try {
  await runtime.warmUp();
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

Call `runtime.warmUp()` to load the WebAssembly runtime before the first project operation. Pass `signal` and `timeoutMs` to warm-up or an operation to cancel it or set an upper bound for that call. Warm-up's timeout and `initializationTimeoutMs` bound worker startup; an operation's `timeoutMs` bounds execution after startup. Cancelling or timing out worker startup terminates that worker; the next queued operation can create a fresh worker. Cancelling or timing out an operation terminates its worker. Operations and warm-up are serialized because the current Go runtime uses one in-memory project root.

Project files can be UTF-8 strings or `Uint8Array` values. UTF-8 files remain inline in the JSON request. The Browser SDK copies each `Uint8Array` and transfers its buffer to the Worker; the Worker sends only `{ "encoding": "binary" }` metadata in the request JSON and passes a path-to-`Uint8Array` map as the second argument to `mclWasm.run(requestJSON, binaryFiles)`. The Go runtime copies those bytes into its project filesystem. The SDK-owned and caller-owned arrays remain reusable after each operation.

`project.complete({ entrypoint, position })` returns the MCL language-service candidates for a zero-based UTF-16 position. Completion uses the same local project files as parsing and rendering, including schemas and Helm values schemas. It does not access a Kubernetes API server or any other network service.

`project.definition({ entrypoint, position })` returns a project-relative declaration path and zero-based UTF-16 range for local and imported MCL declarations. `project.hover({ entrypoint, position })` returns declaration syntax, including explicit type annotations and function or rule signatures. Hover does not infer runtime value types, and both operations use only project files already supplied to the browser runtime.

The package exports the completion, definition, and hover result types, `maxSourceBytes`, `maxProjectFiles`, `maxProjectFileBytes`, and `maxProjectBytes` so browser interfaces can consume the runtime's public types and limits.

MCL decodes the request into a root-relative `io/fs` filesystem for project files, module imports, schemas, and dependency locks. Kubernetes resource scope checks use built-in metadata and CRDs in the project; unknown resource kinds can still be rendered. The runtime limits are defined in the [protocol schema](https://github.com/manifest-configuration-language/mcl/blob/main/cmd/mcl-wasm/protocol.schema.json). The worker blocks `fetch` after loading the WASM runtime assets, so MCL project evaluation cannot make external network requests.

The wire format is described in [`protocol.schema.json`](https://github.com/manifest-configuration-language/mcl/blob/main/cmd/mcl-wasm/protocol.schema.json). Go protocol models are generated with `go generate ./cmd/mcl-wasm`, and the browser package declarations are generated with `npm run generate:protocol`. The browser API declarations reuse the generated operation, diagnostic, and result types. MCL decodes each request into a root-relative `io/fs` project filesystem. Kustomize project overlays and locked Git archives use kyaml's in-memory filesystem, and Helm source loading accepts an `io/fs` filesystem. The Go `wasm_exec.js` runtime supplies its standard browser fallbacks for process, path, and stdout APIs; the Browser SDK does not provide a Node-style project filesystem or require Node polyfills in Vite.

`MclRuntimeError` and lint diagnostics report project-relative paths with UTF-16 start and end positions. Evaluation errors also include related source locations when the Go engine provides them, such as the schema declaration associated with a resource validation error.

The [WebAssembly code size profile](https://github.com/manifest-configuration-language/mcl/blob/main/packages/browser/performance-profile.md) records package-level function body sizes and explains which WebAssembly sections it excludes.

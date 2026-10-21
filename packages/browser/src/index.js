const defaultInitializationTimeoutMs = 30_000;
const defaultOperationTimeoutMs = 10_000;
const maxSourceBytes = 64 * 1024;
const maxProjectFiles = 4096;
const maxProjectFileBytes = 16 * 1024 * 1024;
const maxProjectBytes = 64 * 1024 * 1024;
const wasmURL = new URL("../dist/mcl.wasm", import.meta.url).href;
const wasmExecURL = new URL("../dist/wasm_exec.js", import.meta.url).href;

export class MclRuntimeError extends Error {
  constructor(error) {
    super(error.message);
    this.name = "MclRuntimeError";
    this.code = error.code;
    this.line = error.line;
    this.column = error.column;
  }
}

export function createMclRuntime(options = {}) {
  const workerFactory = options.workerFactory ?? (() => new Worker(new URL("./worker.js", import.meta.url), { type: "module" }));
  const initializationTimeoutMs = options.initializationTimeoutMs ?? defaultInitializationTimeoutMs;
  let worker;
  let ready;
  let disposed = false;
  let queue = Promise.resolve();
  let sequence = 0;
  let stopCurrent;
  let stopInitialization;

  function startWorker() {
    if (disposed) return Promise.reject(new Error("MCL runtime has been disposed"));
    if (ready) return ready;
    const currentWorker = workerFactory();
    worker = currentWorker;
    ready = new Promise((resolve, reject) => {
      const timer = setTimeout(() => fail(new Error("MCL WebAssembly initialization timed out")), initializationTimeoutMs);
      const onMessage = (event) => {
        if (event.data?.type === "ready") {
          clearTimeout(timer);
          currentWorker.removeEventListener("message", onMessage);
          currentWorker.removeEventListener("error", onError);
          if (stopInitialization === fail) stopInitialization = undefined;
          resolve();
        } else if (event.data?.type === "fatal") {
          fail(new Error(event.data.message || "MCL WebAssembly initialization failed"));
        }
      };
      const onError = (event) => fail(new Error(event.message || "MCL worker failed"));
      function fail(error) {
        clearTimeout(timer);
        currentWorker.removeEventListener("message", onMessage);
        currentWorker.removeEventListener("error", onError);
        currentWorker.terminate();
        if (worker === currentWorker) {
          worker = undefined;
          ready = undefined;
        }
        if (stopInitialization === fail) stopInitialization = undefined;
        reject(error);
      }
      stopInitialization = fail;
      currentWorker.addEventListener("message", onMessage);
      currentWorker.addEventListener("error", onError);
      currentWorker.postMessage({ type: "configure", wasmURL, wasmExecURL });
    });
    return ready;
  }

  async function waitUntilReady(signal) {
    if (!signal) return startWorker();
    if (signal.aborted) throw new DOMException("The operation was aborted", "AbortError");
    let onAbort;
    try {
      await Promise.race([
        startWorker(),
        new Promise((_, reject) => {
          onAbort = () => {
            stopInitialization?.(new DOMException("The operation was aborted", "AbortError"));
          };
          signal.addEventListener("abort", onAbort, { once: true });
        }),
      ]);
    } finally {
      signal.removeEventListener("abort", onAbort);
    }
  }

  async function execute(operation, source, files, requestOptions = {}, entrypoint = "main.mcl") {
    const run = async () => {
      if (disposed) throw new Error("MCL runtime has been disposed");
      if (requestOptions.signal?.aborted) throw new DOMException("The operation was aborted", "AbortError");
      await waitUntilReady(requestOptions.signal);
      if (requestOptions.signal?.aborted) throw new DOMException("The operation was aborted", "AbortError");
      const requestId = ++sequence;
      const timeoutMs = requestOptions.timeoutMs ?? options.operationTimeoutMs ?? defaultOperationTimeoutMs;
      return new Promise((resolve, reject) => {
        let settled = false;
        const cleanup = () => {
          clearTimeout(timer);
          worker?.removeEventListener("message", onMessage);
          worker?.removeEventListener("error", onError);
          requestOptions.signal?.removeEventListener("abort", onAbort);
        };
        const finish = (callback, value) => {
          if (settled) return;
          settled = true;
          cleanup();
          if (stopCurrent === stop) stopCurrent = undefined;
          callback(value);
        };
        const onMessage = (event) => {
          const response = event.data;
          if (response?.type !== "response" || response.requestId !== requestId) return;
          if (!response.value.ok) finish(reject, new MclRuntimeError(response.value.error));
          else if (operation === "lint") finish(resolve, { operation, diagnostics: response.value.result });
          else finish(resolve, { operation, ...response.value.result });
        };
        const onError = (event) => stop(new Error(event.message || "MCL worker failed"));
        const onAbort = () => stop(new DOMException("The operation was aborted", "AbortError"));
        const stop = (error) => {
          finish(reject, error);
          worker?.terminate();
          worker = undefined;
          ready = undefined;
        };
        const timer = setTimeout(() => stop(new MclRuntimeError({ code: "MCL-WASM-TIMEOUT", message: "MCL operation timed out" })), timeoutMs);
        worker.addEventListener("message", onMessage);
        worker.addEventListener("error", onError);
        requestOptions.signal?.addEventListener("abort", onAbort, { once: true });
        stopCurrent = stop;
        worker.postMessage({ type: "run", requestId, operation, source, files: files ?? {}, entrypoint });
      });
    };
    const result = queue.then(run, run);
    queue = result.catch(() => {});
    return result;
  }

  function openProject(files) {
    const entries = Object.entries(files ?? {});
    if (entries.length === 0) throw new TypeError("Project must contain at least one MCL source file");
    if (entries.length > maxProjectFiles) throw new RangeError(`Project has more than ${maxProjectFiles} files`);
    let totalBytes = 0;
    const projectFiles = {};
    for (const [filePath, content] of entries) {
      if (!isSafeRelativePath(filePath)) throw new TypeError(`Invalid project file path: ${filePath}`);
      if (typeof content !== "string" && !(content instanceof Uint8Array)) throw new TypeError(`Project file ${filePath} must be a string or Uint8Array`);
      const copy = typeof content === "string" ? content : content.slice();
      const fileBytes = typeof copy === "string" ? new TextEncoder().encode(copy).byteLength : copy.byteLength;
      if (fileBytes > maxProjectFileBytes) throw new RangeError(`Project file ${filePath} exceeds the ${maxProjectFileBytes}-byte limit`);
      totalBytes += fileBytes;
      if (totalBytes > maxProjectBytes) throw new RangeError(`Project files exceed the ${maxProjectBytes}-byte limit`);
      projectFiles[filePath] = copy;
    }

    const sourcePaths = Object.keys(projectFiles).filter((filePath) => filePath.endsWith(".mcl")).sort();
    const defaultEntrypoint = Object.hasOwn(projectFiles, "main.mcl") ? "main.mcl" : sourcePaths.length === 1 ? sourcePaths[0] : undefined;
    const runProjectOperation = async (operation, requestOptions = {}) => {
      const entrypoint = requestOptions.entrypoint ?? defaultEntrypoint;
      if (!entrypoint) throw new TypeError("Set entrypoint when the project has multiple MCL source files");
      if (!isSafeRelativePath(entrypoint) || !Object.hasOwn(projectFiles, entrypoint)) throw new TypeError(`Project entrypoint does not exist: ${entrypoint}`);
      const entry = projectFiles[entrypoint];
      const source = typeof entry === "string" ? entry : new TextDecoder("utf-8", { fatal: true }).decode(entry);
      const sourceBytes = new TextEncoder().encode(source).byteLength;
      if (sourceBytes > maxSourceBytes) throw new RangeError(`Source exceeds the ${maxSourceBytes}-byte limit`);
      const projectBytes = Object.values(projectFiles).reduce((total, file) => total + (typeof file === "string" ? new TextEncoder().encode(file).byteLength : file.byteLength), 0);
      if (projectBytes > maxProjectBytes) throw new RangeError(`Project files exceed the ${maxProjectBytes}-byte limit`);
      const otherFiles = Object.fromEntries(Object.entries(projectFiles).filter(([filePath]) => filePath !== entrypoint));
      return execute(operation, source, otherFiles, requestOptions, entrypoint);
    };
    return Object.freeze({
      parse: (requestOptions) => runProjectOperation("parse", requestOptions),
      lint: (requestOptions) => runProjectOperation("lint", requestOptions),
      format: (requestOptions) => runProjectOperation("format", requestOptions),
      check: (requestOptions) => runProjectOperation("check", requestOptions),
      evaluate: (requestOptions) => runProjectOperation("evaluate", requestOptions),
      render: (requestOptions) => runProjectOperation("render", requestOptions),
    });
  }

  function isSafeRelativePath(filePath) {
    return typeof filePath === "string" && filePath.length > 0 && !filePath.startsWith("/") && !filePath.includes("\\") && filePath.split("/").every((part) => part && part !== "." && part !== "..");
  }

  return {
    openProject,
    parse: (source, requestOptions) => execute("parse", source, undefined, requestOptions),
    lint: (source, requestOptions) => execute("lint", source, undefined, requestOptions),
    format: (source, requestOptions) => execute("format", source, undefined, requestOptions),
    check: (source, files, requestOptions) => execute("check", source, files, requestOptions),
    evaluate: (source, files, requestOptions) => execute("evaluate", source, files, requestOptions),
    render: (source, files, requestOptions) => execute("render", source, files, requestOptions),
    dispose() {
      if (disposed) return;
      disposed = true;
      stopCurrent?.(new Error("MCL runtime has been disposed"));
      stopInitialization?.(new Error("MCL runtime has been disposed"));
      worker?.terminate();
      worker = undefined;
      ready = undefined;
    },
  };
}

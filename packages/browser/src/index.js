import { maxProjectBytes, maxProjectFileBytes, maxProjectFiles, maxSourceBytes } from "./limits.js";

export { maxProjectBytes, maxProjectFileBytes, maxProjectFiles, maxSourceBytes };

const defaultInitializationTimeoutMs = 30_000;
const defaultOperationTimeoutMs = 10_000;
const wasmURL = new URL("../dist/mcl.wasm", import.meta.url).href;
const wasmExecURL = new URL("../dist/wasm_exec.js", import.meta.url).href;

export class MclRuntimeError extends Error {
  constructor(error) {
    super(error.message);
    this.name = "MclRuntimeError";
    this.code = error.code;
    this.path = error.path;
    this.line = error.line;
    this.column = error.column;
    this.endLine = error.endLine;
    this.endColumn = error.endColumn;
    this.related = error.related;
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
  let workerGeneration = 0;
  let stopCurrent;
  let stopInitialization;

  function startWorker() {
    if (disposed) return Promise.reject(new Error("MCL runtime has been disposed"));
    if (ready) return ready;
    const currentWorker = workerFactory();
    worker = currentWorker;
    workerGeneration++;
    let resolveReady;
    let rejectReady;
    const initialization = new Promise((resolve, reject) => {
      resolveReady = resolve;
      rejectReady = reject;
    });
    ready = initialization;
    const timer = setTimeout(() => fail(new Error("MCL WebAssembly initialization timed out")), initializationTimeoutMs);
    const onMessage = (event) => {
      if (event.data?.type === "ready") {
        clearTimeout(timer);
        currentWorker.removeEventListener("message", onMessage);
        currentWorker.removeEventListener("error", onError);
        if (stopInitialization === fail) stopInitialization = undefined;
        resolveReady();
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
      rejectReady(error);
    }
    stopInitialization = fail;
    try {
      currentWorker.addEventListener("message", onMessage);
      currentWorker.addEventListener("error", onError);
      currentWorker.postMessage({ type: "configure", wasmURL, wasmExecURL });
    } catch (error) {
      fail(error);
    }
    return initialization;
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

  async function execute(operation, source, files, requestOptions = {}, entrypoint = "main.mcl", position) {
    const signal = requestOptions.signal;
    if (signal?.aborted) throw new DOMException("The operation was aborted", "AbortError");
    const run = async () => {
      if (disposed) throw new Error("MCL runtime has been disposed");
      if (requestOptions.signal?.aborted) throw new DOMException("The operation was aborted", "AbortError");
      const timeoutMs = requestOptions.timeoutMs ?? options.operationTimeoutMs ?? defaultOperationTimeoutMs;
      const timeoutError = new MclRuntimeError({ code: "MCL-WASM-TIMEOUT", message: "MCL operation timed out" });
      let timer;
      try {
        if (timeoutMs <= 0) throw timeoutError;
        timer = setTimeout(() => {
          (stopCurrent ?? stopInitialization)?.(timeoutError);
        }, timeoutMs);
        await waitUntilReady(requestOptions.signal);
        if (requestOptions.signal?.aborted) throw new DOMException("The operation was aborted", "AbortError");
        const preparedFiles = prepareFilesForTransfer(source, files, entrypoint);
        const requestId = ++sequence;
        const currentWorker = worker;
        const currentGeneration = workerGeneration;
        return await new Promise((resolve, reject) => {
          let settled = false;
          const cleanup = () => {
            clearTimeout(timer);
            currentWorker.removeEventListener("message", onMessage);
            currentWorker.removeEventListener("error", onError);
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
            if (worker !== currentWorker || workerGeneration !== currentGeneration) return;
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
            currentWorker.terminate();
            if (worker === currentWorker) {
              worker = undefined;
              ready = undefined;
            }
          };
          currentWorker.addEventListener("message", onMessage);
          currentWorker.addEventListener("error", onError);
          requestOptions.signal?.addEventListener("abort", onAbort, { once: true });
          stopCurrent = stop;
          try {
            currentWorker.postMessage({ type: "run", requestId, operation, source, files: preparedFiles.files, entrypoint, position }, preparedFiles.transferables);
          } catch (error) {
            stop(error);
          }
        });
      } catch (error) {
        clearTimeout(timer);
        throw error;
      }
    };
    const result = queue.then(run, run);
    queue = result.catch(() => {});
    if (!signal) return result;

    let onAbort;
    const aborted = new Promise((_, reject) => {
      onAbort = () => reject(new DOMException("The operation was aborted", "AbortError"));
      signal.addEventListener("abort", onAbort, { once: true });
      if (signal.aborted) onAbort();
    });
    return Promise.race([result, aborted]).finally(() => signal.removeEventListener("abort", onAbort));
  }

  function warmUp(requestOptions = {}) {
    const signal = requestOptions.signal;
    if (signal?.aborted) return Promise.reject(new DOMException("The operation was aborted", "AbortError"));
    const run = async () => {
      if (disposed) throw new Error("MCL runtime has been disposed");
      if (signal?.aborted) throw new DOMException("The operation was aborted", "AbortError");
      const timeoutMs = requestOptions.timeoutMs ?? initializationTimeoutMs;
      const timeoutError = new MclRuntimeError({ code: "MCL-WASM-TIMEOUT", message: "MCL WebAssembly initialization timed out" });
      let timer;
      try {
        if (timeoutMs <= 0) throw timeoutError;
        timer = setTimeout(() => stopInitialization?.(timeoutError), timeoutMs);
        await waitUntilReady(signal);
      } finally {
        clearTimeout(timer);
      }
    };
    const result = queue.then(run, run);
    queue = result.catch(() => {});
    if (!signal) return result;
    let onAbort;
    const aborted = new Promise((_, reject) => {
      onAbort = () => reject(new DOMException("The operation was aborted", "AbortError"));
      signal.addEventListener("abort", onAbort, { once: true });
      if (signal.aborted) onAbort();
    });
    return Promise.race([result, aborted]).finally(() => signal.removeEventListener("abort", onAbort));
  }

  function openProject(files) {
    const entries = Object.entries(files ?? {});
    if (entries.length === 0) throw new TypeError("Project must contain at least one MCL source file");
    if (entries.length > maxProjectFiles) throw new RangeError(`Project has more than ${maxProjectFiles} files`);
    let totalBytes = 0;
    // Project paths are user controlled; a null prototype keeps names such as
    // `__proto__` as ordinary file paths instead of mutating the file map.
    const projectFiles = Object.create(null);
    for (const [filePath, content] of entries) {
      if (!isSafeRelativePath(filePath)) throw new TypeError(`Invalid project file path: ${filePath}`);
      if (typeof content !== "string" && !(content instanceof Uint8Array)) throw new TypeError(`Project file ${filePath} must be a string or Uint8Array`);
      const fileBytes = typeof content === "string" ? new TextEncoder().encode(content).byteLength : content.byteLength;
      if (fileBytes > maxProjectFileBytes) throw new RangeError(`Project file ${filePath} exceeds the ${maxProjectFileBytes}-byte limit`);
      totalBytes += fileBytes;
      if (totalBytes > maxProjectBytes) throw new RangeError(`Project files exceed the ${maxProjectBytes}-byte limit`);
      projectFiles[filePath] = typeof content === "string" ? content : new Uint8Array(content);
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
      return execute(operation, source, otherFiles, requestOptions, entrypoint, requestOptions.position);
    };
    return Object.freeze({
      parse: (requestOptions) => runProjectOperation("parse", requestOptions),
      lint: (requestOptions) => runProjectOperation("lint", requestOptions),
      complete: (requestOptions) => runProjectOperation("completion", requestOptions),
      format: (requestOptions) => runProjectOperation("format", requestOptions),
      check: (requestOptions) => runProjectOperation("check", requestOptions),
      evaluate: (requestOptions) => runProjectOperation("evaluate", requestOptions),
      render: (requestOptions) => runProjectOperation("render", requestOptions),
    });
  }

  function isSafeRelativePath(filePath) {
    return typeof filePath === "string" && filePath.length > 0 && !/^[a-z]:/i.test(filePath) && !filePath.startsWith("/") && !filePath.includes("\\") && filePath.split("/").every((part) => part && part !== "." && part !== "..");
  }

  function prepareFilesForTransfer(source, files, entrypoint) {
    if (!isSafeRelativePath(entrypoint)) throw new TypeError(`Invalid project entrypoint: ${entrypoint}`);
    const sourceBytes = new TextEncoder().encode(source).byteLength;
    if (sourceBytes > maxSourceBytes) throw new RangeError(`Source exceeds the ${maxSourceBytes}-byte limit`);
    const entries = Object.entries(files ?? {});
    if (entries.length + 1 > maxProjectFiles) throw new RangeError(`Project has more than ${maxProjectFiles} files`);
    let totalBytes = sourceBytes;
    const transferredFiles = Object.create(null);
    const transferables = [];
    for (const [filePath, content] of entries) {
      if (!isSafeRelativePath(filePath) || filePath === entrypoint) throw new TypeError(`Invalid project file path: ${filePath}`);
      if (typeof content === "string") {
        const fileBytes = new TextEncoder().encode(content).byteLength;
        if (fileBytes > maxProjectFileBytes) throw new RangeError(`Project file ${filePath} exceeds the ${maxProjectFileBytes}-byte limit`);
        totalBytes += fileBytes;
        transferredFiles[filePath] = content;
      } else if (content instanceof Uint8Array) {
        if (content.byteLength > maxProjectFileBytes) throw new RangeError(`Project file ${filePath} exceeds the ${maxProjectFileBytes}-byte limit`);
        totalBytes += content.byteLength;
        const copy = new Uint8Array(content);
        transferredFiles[filePath] = copy;
        transferables.push(copy.buffer);
      } else {
        throw new TypeError(`Project file ${filePath} must be a string or Uint8Array`);
      }
      if (totalBytes > maxProjectBytes) throw new RangeError(`Project files exceed the ${maxProjectBytes}-byte limit`);
    }
    return { files: transferredFiles, transferables };
  }

  return {
    openProject,
    warmUp,
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

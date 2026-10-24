import {
  maxProjectBytes,
  maxProjectFileBytes,
  maxProjectFiles,
  maxProjectPathDepth,
  maxProjectPathLength,
  maxSourceBytes,
} from "./limits.js";

export {
  maxProjectBytes,
  maxProjectFileBytes,
  maxProjectFiles,
  maxProjectPathDepth,
  maxProjectPathLength,
  maxSourceBytes,
};

export function isValidProjectPath(value) {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.startsWith("/") ||
    /^[a-z]:/i.test(value) ||
    value.includes("\\") ||
    value.includes("\0")
  ) {
    return false;
  }
  let pathLength = 0;
  let pathDepth = 1;
  for (const character of value) {
    if (++pathLength > maxProjectPathLength) return false;
    if (character === "/" && ++pathDepth > maxProjectPathDepth) return false;
  }
  return value.split("/").every((part) => part !== "" && part !== "." && part !== "..");
}

export function isValidProjectFileSet(paths) {
  if (!Array.isArray(paths)) return false;
  const files = new Set();
  const directories = new Set();
  for (const filePath of paths) {
    if (!isValidProjectPath(filePath) || files.has(filePath) || directories.has(filePath))
      return false;
    let separator = filePath.indexOf("/");
    while (separator !== -1) {
      const directory = filePath.slice(0, separator);
      if (files.has(directory)) return false;
      directories.add(directory);
      separator = filePath.indexOf("/", separator + 1);
    }
    files.add(filePath);
  }
  return true;
}

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
  const workerFactory =
    options.workerFactory ??
    (() => new Worker(new URL("./worker.js", import.meta.url), { type: "module" }));
  const initializationTimeoutMs = options.initializationTimeoutMs ?? defaultInitializationTimeoutMs;
  let worker;
  let ready;
  let disposed = false;
  let queue = Promise.resolve();
  let sequence = 0;
  let workerGeneration = 0;
  let workerProject;
  let stopCurrent;
  let stopInitialization;

  function startWorker() {
    if (disposed) return Promise.reject(new Error("MCL runtime has been disposed"));
    if (ready) return ready;
    const currentWorker = workerFactory();
    worker = currentWorker;
    workerGeneration++;
    workerProject = undefined;
    let resolveReady;
    let rejectReady;
    const initialization = new Promise((resolve, reject) => {
      resolveReady = resolve;
      rejectReady = reject;
    });
    ready = initialization;
    const timer = setTimeout(
      () => fail(new Error("MCL WebAssembly initialization timed out")),
      initializationTimeoutMs,
    );
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

  async function syncProjectToWorker(project, requestOptions, currentWorker, currentGeneration) {
    const previous = workerProject;
    const reset = !previous || previous.id !== project.id;
    const previousFiles = reset ? Object.create(null) : previous.snapshot.files;
    const updates = Object.create(null);
    const binaryFiles = Object.create(null);
    const transferables = [];
    const deleted = [];
    for (const [filePath, content] of Object.entries(project.snapshot.files)) {
      if (
        !reset &&
        Object.hasOwn(previousFiles, filePath) &&
        sameContent(previousFiles[filePath], content)
      )
        continue;
      if (typeof content === "string") {
        updates[filePath] = { encoding: "utf8", content };
      } else {
        const copy = new Uint8Array(content);
        updates[filePath] = { encoding: "binary" };
        binaryFiles[filePath] = copy;
        transferables.push(copy.buffer);
      }
    }
    if (!reset) {
      for (const filePath of Object.keys(previousFiles)) {
        if (!Object.hasOwn(project.snapshot.files, filePath)) deleted.push(filePath);
      }
    }
    if (!reset && Object.keys(updates).length === 0 && deleted.length === 0) return;

    const requestId = ++sequence;
    const update = JSON.stringify({ reset, files: updates, deleted });
    await new Promise((resolve, reject) => {
      let settled = false;
      const cleanup = () => {
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
        if (response?.type !== "project-updated" || response.requestId !== requestId) return;
        if (!response.value.ok) finish(reject, new MclRuntimeError(response.value.error));
        else {
          workerProject = { id: project.id, snapshot: project.snapshot };
          finish(resolve);
        }
      };
      const stop = (error) => {
        finish(reject, error);
        currentWorker.terminate();
        if (worker === currentWorker) {
          worker = undefined;
          ready = undefined;
          workerProject = undefined;
        }
      };
      const onError = (event) => stop(new Error(event.message || "MCL worker failed"));
      const onAbort = () => stop(new DOMException("The operation was aborted", "AbortError"));
      currentWorker.addEventListener("message", onMessage);
      currentWorker.addEventListener("error", onError);
      requestOptions.signal?.addEventListener("abort", onAbort, { once: true });
      stopCurrent = stop;
      if (requestOptions.signal?.aborted) return onAbort();
      try {
        currentWorker.postMessage(
          { type: "project-update", requestId, projectId: project.id, update, binaryFiles },
          transferables,
        );
      } catch (error) {
        stop(error);
      }
    });
  }

  async function execute(
    operation,
    source,
    files,
    requestOptions = {},
    entrypoint = "main.mcl",
    position,
    project,
  ) {
    const signal = requestOptions.signal;
    if (signal?.aborted) throw new DOMException("The operation was aborted", "AbortError");
    const run = async () => {
      if (disposed) throw new Error("MCL runtime has been disposed");
      if (requestOptions.signal?.aborted)
        throw new DOMException("The operation was aborted", "AbortError");
      const timeoutMs =
        requestOptions.timeoutMs ?? options.operationTimeoutMs ?? defaultOperationTimeoutMs;
      const timeoutError = new MclRuntimeError({
        code: "MCL-WASM-TIMEOUT",
        message: "MCL operation timed out",
      });
      let timer;
      try {
        if (timeoutMs <= 0) throw timeoutError;
        timer = setTimeout(() => {
          if (stopCurrent) stopCurrent(timeoutError);
          else stopInitialization?.(timeoutError);
        }, timeoutMs);
        await waitUntilReady(requestOptions.signal);
        if (requestOptions.signal?.aborted)
          throw new DOMException("The operation was aborted", "AbortError");
        const currentWorker = worker;
        const currentGeneration = workerGeneration;
        if (project) {
          await syncProjectToWorker(
            project,
            requestOptions,
            currentWorker,
            currentGeneration,
            timeoutError,
          );
        }
        const preparedFiles = project
          ? undefined
          : prepareFilesForTransfer(source, files, entrypoint);
        const requestId = ++sequence;
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
            else if (operation === "lint")
              finish(resolve, { operation, diagnostics: response.value.result });
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
            const message = { type: "run", requestId, operation, source, entrypoint, position };
            if (project) message.projectId = project.id;
            else message.files = preparedFiles.files;
            currentWorker.postMessage(message, preparedFiles?.transferables ?? []);
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
    return Promise.race([result, aborted]).finally(() =>
      signal.removeEventListener("abort", onAbort),
    );
  }

  function warmUp(requestOptions = {}) {
    const signal = requestOptions.signal;
    if (signal?.aborted)
      return Promise.reject(new DOMException("The operation was aborted", "AbortError"));
    const run = async () => {
      if (disposed) throw new Error("MCL runtime has been disposed");
      if (signal?.aborted) throw new DOMException("The operation was aborted", "AbortError");
      const timeoutMs = requestOptions.timeoutMs ?? initializationTimeoutMs;
      const timeoutError = new MclRuntimeError({
        code: "MCL-WASM-TIMEOUT",
        message: "MCL WebAssembly initialization timed out",
      });
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
    return Promise.race([result, aborted]).finally(() =>
      signal.removeEventListener("abort", onAbort),
    );
  }

  function openProject(files) {
    const snapshot = createProjectSnapshot(files);
    const id = `mcl-project-${++sequence}`;
    return makeProject(id, snapshot);
  }

  function sameContent(left, right) {
    if (left === right) return true;
    if (typeof left !== typeof right) return false;
    if (typeof left === "string") return left === right;
    if (left.byteLength !== right.byteLength) return false;
    for (let index = 0; index < left.byteLength; index++) {
      if (left[index] !== right[index]) return false;
    }
    return true;
  }

  function createProjectSnapshot(files, previous) {
    const entries = Object.entries(files ?? {});
    if (entries.length === 0)
      throw new TypeError("Project must contain at least one MCL source file");
    if (entries.length > maxProjectFiles)
      throw new RangeError(`Project has more than ${maxProjectFiles} files`);
    if (!isValidProjectFileSet(entries.map(([filePath]) => filePath)))
      throw new TypeError(
        "Project file paths must be valid and cannot conflict with a file or directory path",
      );
    let totalBytes = 0;
    const projectFiles = Object.create(null);
    const fileSizes = Object.create(null);
    for (const [filePath, input] of entries) {
      if (typeof input !== "string" && !(input instanceof Uint8Array))
        throw new TypeError(`Project file ${filePath} must be a string or Uint8Array`);
      const previousContent = previous?.files[filePath];
      const unchanged = previousContent !== undefined && sameContent(previousContent, input);
      const content = unchanged
        ? previousContent
        : typeof input === "string"
          ? input
          : new Uint8Array(input);
      const fileBytes = unchanged
        ? previous.sizes[filePath]
        : typeof content === "string"
          ? new TextEncoder().encode(content).byteLength
          : content.byteLength;
      if (!unchanged && fileBytes > maxProjectFileBytes)
        throw new RangeError(
          `Project file ${filePath} exceeds the ${maxProjectFileBytes}-byte limit`,
        );
      totalBytes += fileBytes;
      if (totalBytes > maxProjectBytes)
        throw new RangeError(`Project files exceed the ${maxProjectBytes}-byte limit`);
      projectFiles[filePath] = content;
      fileSizes[filePath] = fileBytes;
    }
    const sourcePaths = Object.keys(projectFiles)
      .filter((filePath) => filePath.endsWith(".mcl"))
      .sort();
    if (sourcePaths.length === 0)
      throw new TypeError("Project must contain at least one MCL source file");
    const defaultEntrypoint = Object.hasOwn(projectFiles, "main.mcl")
      ? "main.mcl"
      : sourcePaths.length === 1
        ? sourcePaths[0]
        : undefined;
    return Object.freeze({
      files: Object.freeze(projectFiles),
      sizes: Object.freeze(fileSizes),
      defaultEntrypoint,
    });
  }

  function makeProject(id, snapshot) {
    const runProjectOperation = async (operation, requestOptions = {}) => {
      const entrypoint = requestOptions.entrypoint ?? snapshot.defaultEntrypoint;
      if (!entrypoint)
        throw new TypeError("Set entrypoint when the project has multiple MCL source files");
      if (!isSafeRelativePath(entrypoint) || !Object.hasOwn(snapshot.files, entrypoint))
        throw new TypeError(`Project entrypoint does not exist: ${entrypoint}`);
      const entry = snapshot.files[entrypoint];
      const source =
        typeof entry === "string" ? entry : new TextDecoder("utf-8", { fatal: true }).decode(entry);
      const sourceBytes = new TextEncoder().encode(source).byteLength;
      if (sourceBytes > maxSourceBytes)
        throw new RangeError(`Source exceeds the ${maxSourceBytes}-byte limit`);
      return execute(
        operation,
        source,
        undefined,
        requestOptions,
        entrypoint,
        requestOptions.position,
        { id, snapshot },
      );
    };
    return Object.freeze({
      update: (nextFiles) => makeProject(id, createProjectSnapshot(nextFiles, snapshot)),
      parse: (requestOptions) => runProjectOperation("parse", requestOptions),
      lint: (requestOptions) => runProjectOperation("lint", requestOptions),
      complete: (requestOptions) => runProjectOperation("completion", requestOptions),
      definition: (requestOptions) => runProjectOperation("definition", requestOptions),
      hover: (requestOptions) => runProjectOperation("hover", requestOptions),
      format: (requestOptions) => runProjectOperation("format", requestOptions),
      check: (requestOptions) => runProjectOperation("check", requestOptions),
      evaluate: (requestOptions) => runProjectOperation("evaluate", requestOptions),
      render: (requestOptions) => runProjectOperation("render", requestOptions),
    });
  }

  function isSafeRelativePath(filePath) {
    return isValidProjectPath(filePath);
  }

  function prepareFilesForTransfer(source, files, entrypoint) {
    if (!isSafeRelativePath(entrypoint))
      throw new TypeError(`Invalid project entrypoint: ${entrypoint}`);
    const sourceBytes = new TextEncoder().encode(source).byteLength;
    if (sourceBytes > maxSourceBytes)
      throw new RangeError(`Source exceeds the ${maxSourceBytes}-byte limit`);
    const entries = Object.entries(files ?? {});
    if (entries.length + 1 > maxProjectFiles)
      throw new RangeError(`Project has more than ${maxProjectFiles} files`);
    let totalBytes = sourceBytes;
    const transferredFiles = Object.create(null);
    const transferables = [];
    for (const [filePath, content] of entries) {
      if (!isSafeRelativePath(filePath) || filePath === entrypoint)
        throw new TypeError(`Invalid project file path: ${filePath}`);
      if (typeof content === "string") {
        const fileBytes = new TextEncoder().encode(content).byteLength;
        if (fileBytes > maxProjectFileBytes)
          throw new RangeError(
            `Project file ${filePath} exceeds the ${maxProjectFileBytes}-byte limit`,
          );
        totalBytes += fileBytes;
        transferredFiles[filePath] = content;
      } else if (content instanceof Uint8Array) {
        if (content.byteLength > maxProjectFileBytes)
          throw new RangeError(
            `Project file ${filePath} exceeds the ${maxProjectFileBytes}-byte limit`,
          );
        totalBytes += content.byteLength;
        const copy = new Uint8Array(content);
        transferredFiles[filePath] = copy;
        transferables.push(copy.buffer);
      } else {
        throw new TypeError(`Project file ${filePath} must be a string or Uint8Array`);
      }
      if (totalBytes > maxProjectBytes)
        throw new RangeError(`Project files exceed the ${maxProjectBytes}-byte limit`);
    }
    return { files: transferredFiles, transferables };
  }

  function runSourceNavigation(operation, source, filesOrOptions, requestOptions) {
    const files = requestOptions === undefined ? undefined : filesOrOptions;
    const options = requestOptions ?? filesOrOptions;
    if (!options?.position) throw new TypeError("A source position is required");
    return execute(
      operation,
      source,
      files,
      options,
      options.entrypoint ?? "main.mcl",
      options.position,
    );
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
    definition: (source, filesOrOptions, requestOptions) =>
      runSourceNavigation("definition", source, filesOrOptions, requestOptions),
    hover: (source, filesOrOptions, requestOptions) =>
      runSourceNavigation("hover", source, filesOrOptions, requestOptions),
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

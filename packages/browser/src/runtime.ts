import { createRuntimeScheduler } from "./runtime-scheduler.js";
import { createWorkerSupervisor } from "./runtime-worker.js";
import { createProjectSynchronizer } from "./runtime-project-sync.js";
import { createWasmRequest, decodeWasmResponse } from "./runtime-protocol.js";
import {
  createProjectSnapshot,
  isValidProjectPath,
  maxSourceBytes,
} from "./project.js";
import type {
  ProjectFiles,
  ProjectSnapshot,
  RequestOptions,
  ProjectOperationOptions,
  ProjectParametersOptions,
  ProjectCompletionOptions,
  ProjectNavigationOptions,
  ProjectRenameOptions,
  ProjectFileRenameOptions,
} from "./project.js";
import type {
  CheckResult,
  CompletionResult as ProtocolCompletionResult,
  DefinitionResult as ProtocolDefinitionResult,
  EvaluateResult,
  FormatResult,
  HoverResult as ProtocolHoverResult,
  LintDiagnostic as ProtocolLintDiagnostic,
  ParseResult,
  ReferencesResult as ProtocolReferencesResult,
  RenderResult,
  RenameResult as ProtocolRenameResult,
  RenameFileImportsResult as ProtocolRenameFileImportsResult,
  Request as WasmRequest,
  Error as ProtocolError,
} from "./protocol.js";

const maxParameterBytes = 1_048_576;
const maxParameterDepth = 64;

function isJSONValue(value: unknown, ancestors = new Set<object>(), depth = 0): boolean {
  if (value === null || typeof value === "string" || typeof value === "boolean") return true;
  if (typeof value === "number") return Number.isFinite(value);
  if (typeof value !== "object" || ancestors.has(value) || depth >= maxParameterDepth) return false;
  if (!Array.isArray(value)) {
    const prototype = Object.getPrototypeOf(value);
    if (prototype !== Object.prototype && prototype !== null) return false;
    if (Object.getOwnPropertySymbols(value).length > 0) return false;
  }
  ancestors.add(value);
  let valid: boolean;
  if (Array.isArray(value)) {
    valid = true;
    for (let index = 0; index < value.length; index += 1) {
      if (!Object.hasOwn(value, index) || !isJSONValue(value[index], ancestors, depth + 1)) {
        valid = false;
        break;
      }
    }
  } else {
    valid = Object.values(value).every((item) => isJSONValue(item, ancestors, depth + 1));
  }
  ancestors.delete(value);
  return valid;
}

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

export type MclOperation = WasmRequest["operation"];

export type MclResult =
  | ({ operation: "parse" } & ParseResult)
  | { operation: "lint"; diagnostics: ProtocolLintDiagnostic[] }
  | ({ operation: "completion" } & ProtocolCompletionResult)
  | ({ operation: "definition" } & ProtocolDefinitionResult)
  | ({ operation: "hover" } & ProtocolHoverResult)
  | ({ operation: "references" } & ProtocolReferencesResult)
  | ({ operation: "rename" } & ProtocolRenameResult)
  | ({ operation: "renameFileImports" } & ProtocolRenameFileImportsResult)
  | ({ operation: "format" } & FormatResult)
  | ({ operation: "check" } & CheckResult)
  | ({ operation: "evaluate" } & EvaluateResult)
  | ({ operation: "render" } & RenderResult);

export type RuntimeStatus = "fetching" | "compiling" | "initializing" | "ready" | "error";

export type RuntimeStatusEvent = {
  status: RuntimeStatus;
  workerGeneration: number;
  initializationDurationMs?: number;
};

export type RuntimeOperationOutcome = "success" | "error" | "aborted";

export type RuntimeOperationEvent = {
  operation: MclOperation;
  durationMs: number;
  queueWaitMs: number;
  executionDurationMs: number;
  wasmDurationMs?: number;
  workerGeneration?: number;
  outcome: RuntimeOperationOutcome;
};

export interface RuntimeOptions {
  initializationTimeoutMs?: number;
  operationTimeoutMs?: number;
  onStatus?: (event: RuntimeStatusEvent) => void;
  onOperation?: (event: RuntimeOperationEvent) => void;
  workerFactory?: () => Worker;
}

export interface MclProject {
  /** Returns a new immutable snapshot handle. Only changed files are sent on the next operation. */
  patch(changes: { set?: ProjectFiles; delete?: string[] }): MclProject;
  run<O extends MclOperation>(
    operation: O,
    ...options: O extends "completion"
      ? [options: ProjectCompletionOptions]
      : O extends "definition" | "hover" | "references"
        ? [options: ProjectNavigationOptions]
        : O extends "rename"
          ? [options: ProjectRenameOptions]
        : O extends "check" | "evaluate" | "render"
          ? [options?: ProjectParametersOptions]
          : O extends "renameFileImports"
            ? [options: ProjectFileRenameOptions]
            : [options?: ProjectOperationOptions]
  ): Promise<Extract<MclResult, { operation: O }>>;
}

export interface MclRuntime {
  openProject(files: ProjectFiles): MclProject;
  warmUp(options?: RequestOptions): Promise<void>;
  dispose(): void;
}

// ---------------------------------------------------------------------------
// Error class
// ---------------------------------------------------------------------------

export class MclRuntimeError extends Error {
  readonly code: string;
  readonly path?: string;
  readonly line?: number;
  readonly column?: number;
  readonly endLine?: number;
  readonly endColumn?: number;
  readonly related?: ProtocolError["related"];

  constructor(error: Partial<ProtocolError> & { code: string; message: string }) {
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

// ---------------------------------------------------------------------------
// Timing helpers
// ---------------------------------------------------------------------------

const defaultInitializationTimeoutMs = 30_000;
const defaultOperationTimeoutMs = 10_000;

// ---------------------------------------------------------------------------
// Runtime factory
// ---------------------------------------------------------------------------

export function createMclRuntime(options: RuntimeOptions = {}): MclRuntime {
  const workerFactory =
    options.workerFactory ??
    (() => new Worker(new URL("./worker.js", import.meta.url), { type: "module" }));
  const initializationTimeoutMs = options.initializationTimeoutMs ?? defaultInitializationTimeoutMs;

  let disposed = false;
  const scheduler = createRuntimeScheduler();
  let sequence = 0;
  let workerGeneration = 0;
  let resetProjectSync = () => {};

  // -------------------------------------------------------------------------
  // Observability
  // -------------------------------------------------------------------------

  function emitStatus(status: RuntimeStatus, extra: Partial<RuntimeStatusEvent> = {}): void {
    try {
      Promise.resolve(options.onStatus?.({ status, workerGeneration, ...extra })).catch(() => {});
    } catch {
      // Observability callbacks must not affect runtime behavior.
    }
  }

  const worker = createWorkerSupervisor(
    workerFactory,
    initializationTimeoutMs,
    () =>
      new MclRuntimeError({
        code: "MCL-WASM-TIMEOUT",
        message: "MCL WebAssembly initialization timed out",
      }),
    {
      emitStatus,
      onGeneration(generation) {
        workerGeneration = generation;
      },
      onReset() {
        resetProjectSync();
      },
    },
  );
  const projectSynchronizer = createProjectSynchronizer(
    () => worker.api,
    (signal, call) => worker.runCall(signal, call),
    (error) => new MclRuntimeError(error),
  );
  resetProjectSync = projectSynchronizer.reset;

  function emitOperation(
    operation: MclOperation,
    startedAt: number,
    outcome: RuntimeOperationOutcome,
    details: {
      executionStartedAt?: number;
      executionDurationMs?: number;
      wasmDurationMs?: number;
      workerGeneration?: number;
    } = {},
  ): void {
    try {
      const durationMs = Math.max(0, performance.now() - startedAt);
      Promise.resolve(
        options.onOperation?.({
          operation,
          durationMs,
          queueWaitMs:
            details.executionStartedAt !== undefined
              ? Math.max(0, details.executionStartedAt - startedAt)
              : durationMs,
          executionDurationMs: details.executionDurationMs ?? 0,
          ...(details.wasmDurationMs === undefined
            ? {}
            : { wasmDurationMs: details.wasmDurationMs }),
          ...(details.workerGeneration === undefined
            ? {}
            : { workerGeneration: details.workerGeneration }),
          outcome,
        }),
      ).catch(() => {});
    } catch {
      // Observability callbacks must not affect runtime behavior.
    }
  }

  // -------------------------------------------------------------------------
  // Core execute function
  // -------------------------------------------------------------------------

  async function execute(
    operation: MclOperation,
    source: string,
    requestOptions: RequestOptions & {
      entrypoint?: string;
      parameters?: Record<string, unknown>;
      position?: { line: number; character: number };
      newName?: string;
      oldPath?: string;
      newPath?: string;
    },
    entrypoint: string,
    project: { id: string; snapshot: ProjectSnapshot },
  ): Promise<MclResult> {
    if (requestOptions.parameters !== undefined) {
      if (!(["check", "evaluate", "render"] as string[]).includes(operation)) {
        throw new TypeError("parameters are only valid for check, evaluate, and render operations");
      }
      const parameters = requestOptions.parameters as unknown;
      if (
        parameters === null ||
        typeof parameters !== "object" ||
        Array.isArray(parameters) ||
        !isJSONValue(parameters)
      ) {
        throw new TypeError("parameters must be an object with JSON values");
      }
      const serializedParameters = JSON.stringify(parameters);
      if (new TextEncoder().encode(serializedParameters).byteLength > maxParameterBytes) {
        throw new RangeError(`parameters exceed the ${maxParameterBytes}-byte limit`);
      }
    }
    // Validate renameFileImports paths eagerly so we never spin up a Worker.
    if (operation === "renameFileImports") {
      const { oldPath, newPath } = requestOptions;
      if (
        !isValidProjectPath(oldPath) ||
        !oldPath!.endsWith(".mcl") ||
        !isValidProjectPath(newPath) ||
        !newPath!.endsWith(".mcl")
      ) {
        throw new TypeError("oldPath and newPath must be valid project-relative MCL paths");
      }
      if (oldPath === newPath) throw new TypeError("oldPath and newPath must be distinct");
    }

    const { signal } = requestOptions;
    if (signal?.aborted) throw new DOMException("The operation was aborted", "AbortError");

    const startedAt = performance.now();
    const timing: {
      executionStartedAt?: number;
      executionDurationMs?: number;
      wasmStartedAt?: number;
      wasmDurationMs?: number;
    } = {};
    let operationWorkerGeneration: number | undefined;

    const run = async (): Promise<MclResult> => {
      if (disposed) throw new Error("MCL runtime has been disposed");
      if (signal?.aborted) throw new DOMException("The operation was aborted", "AbortError");

      const timeoutMs =
        requestOptions.timeoutMs ?? options.operationTimeoutMs ?? defaultOperationTimeoutMs;
      const timeoutError = new MclRuntimeError({
        code: "MCL-WASM-TIMEOUT",
        message: "MCL operation timed out",
      });

      let timer: ReturnType<typeof setTimeout> | undefined;
      try {
        if (timeoutMs <= 0) throw timeoutError;
        await worker.waitUntilReady(signal);
        if (signal?.aborted) throw new DOMException("The operation was aborted", "AbortError");

        timer = setTimeout(() => worker.stopCurrent(timeoutError), timeoutMs);

        const currentApi = worker.api!;
        const currentGeneration = worker.generation;
        operationWorkerGeneration = currentGeneration;

        if (project) {
          await projectSynchronizer.sync(project.id, project.snapshot, requestOptions);
        }

        // Build the WASM request.
        const requestId = ++sequence;
        const request = createWasmRequest(requestId, operation, entrypoint, source, requestOptions);

        timing.wasmStartedAt = performance.now();
        let response: import("./protocol.js").Response;
        try {
          response = await worker.runCall(signal, () =>
            currentApi.run(JSON.stringify(request), project.id),
          );
        } finally {
          timing.wasmDurationMs = Math.max(0, performance.now() - timing.wasmStartedAt!);
          clearTimeout(timer);
        }

        return decodeWasmResponse(operation, response, (error) => new MclRuntimeError(error)) as MclResult;
      } catch (error) {
        clearTimeout(timer);
        throw error;
      }
    };

    try {
      const result = await scheduler.enqueue(
        operation,
        run,
        signal,
        project.id,
        requestOptions.priority,
        timing,
      );
      emitOperation(operation, startedAt, "success", {
        ...timing,
        workerGeneration: operationWorkerGeneration,
      });
      return result;
    } catch (error) {
      emitOperation(
        operation,
        startedAt,
        (error as Error)?.name === "AbortError" ? "aborted" : "error",
        { ...timing, workerGeneration: operationWorkerGeneration },
      );
      throw error;
    }
  }

  // -------------------------------------------------------------------------
  // Project handle factory
  // -------------------------------------------------------------------------

  function makeProject(id: string, snapshot: ReturnType<typeof createProjectSnapshot>): MclProject {
    const runProjectOperation = async (
      operation: MclOperation,
      requestOptions: RequestOptions & {
        entrypoint?: string;
        parameters?: Record<string, unknown>;
        position?: { line: number; character: number };
        newName?: string;
        oldPath?: string;
        newPath?: string;
      } = {},
    ): Promise<MclResult> => {
      const entrypoint = requestOptions.entrypoint ?? snapshot.defaultEntrypoint;
      if (!entrypoint)
        throw new TypeError("Set entrypoint when the project has multiple MCL source files");
      if (!isValidProjectPath(entrypoint) || !Object.hasOwn(snapshot.files, entrypoint))
        throw new TypeError(`Project entrypoint does not exist: ${entrypoint}`);
      const entry = snapshot.files[entrypoint]!;
      const source =
        typeof entry === "string"
          ? entry
          : new TextDecoder("utf-8", { fatal: true }).decode(entry as Uint8Array);
      const sourceBytes = new TextEncoder().encode(source).byteLength;
      if (sourceBytes > maxSourceBytes)
        throw new RangeError(`Source exceeds the ${maxSourceBytes}-byte limit`);
      return execute(operation, source, requestOptions, entrypoint, { id, snapshot });
    };

    const projectApi: MclProject = Object.freeze({
      patch(changes: { set?: ProjectFiles; delete?: string[] }): MclProject {
        // Build new file set from current snapshot + changes.
        const merged: Record<string, string | Uint8Array> = Object.create(null);
        const deleted = new Set(changes.delete);
        for (const [path, content] of Object.entries(snapshot.files)) {
          if (!deleted.has(path)) merged[path] = content;
        }
        if (changes.set) {
          for (const [path, content] of Object.entries(changes.set)) {
            merged[path] = content;
          }
        }
        return makeProject(id, createProjectSnapshot(merged as ProjectFiles, snapshot));
      },
      run: ((operation: MclOperation, opts?: ProjectOperationOptions) =>
        runProjectOperation(operation, opts)) as MclProject["run"],
    });
    return projectApi;
  }

  // -------------------------------------------------------------------------
  // warmUp
  // -------------------------------------------------------------------------

  function warmUp(requestOptions: RequestOptions = {}): Promise<void> {
    const { signal } = requestOptions;
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
      let timer: ReturnType<typeof setTimeout> | undefined;
      try {
        if (timeoutMs <= 0) throw timeoutError;
        timer = setTimeout(() => worker.stopInitialization(timeoutError), timeoutMs);
        await worker.waitUntilReady(signal);
      } finally {
        clearTimeout(timer);
      }
    };
    return scheduler.enqueue("warmUp", run, signal, "runtime", requestOptions.priority);
  }

  // -------------------------------------------------------------------------
  // Public runtime object
  // -------------------------------------------------------------------------

  return {
    openProject(files: ProjectFiles): MclProject {
      const snapshot = createProjectSnapshot(files);
      const id = `mcl-project-${++sequence}`;
      return makeProject(id, snapshot);
    },

    warmUp,

    dispose(): void {
      if (disposed) return;
      disposed = true;
      const error = new Error("MCL runtime has been disposed");
      scheduler.abortAll(error);
      worker.dispose(error);
    },
  };
}

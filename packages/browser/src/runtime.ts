import PQueue from "p-queue";
import * as Comlink from "comlink";
import type { WorkerApi } from "./worker.js";
import {
  createProjectSnapshot,
  computeProjectDelta,
  isValidProjectPath,
  operationPriorities,
  latestOnlyOperations,
  requestPriorities,
  maxSourceBytes,
} from "./project.js";
import type {
  ProjectFiles,
  ProjectFile,
  ProjectSnapshot,
  RequestOptions,
  ProjectOperationOptions,
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

const wasmURL = new URL("../dist/mcl.wasm", import.meta.url).href;
const wasmExecURL = new URL("../dist/wasm_exec.js", import.meta.url).href;

// ---------------------------------------------------------------------------
// Runtime factory
// ---------------------------------------------------------------------------

export function createMclRuntime(options: RuntimeOptions = {}): MclRuntime {
  const workerFactory =
    options.workerFactory ??
    (() => new Worker(new URL("./worker.js", import.meta.url), { type: "module" }));
  const initializationTimeoutMs = options.initializationTimeoutMs ?? defaultInitializationTimeoutMs;

  let rawWorker: Worker | undefined;
  let workerApi: Comlink.Remote<WorkerApi> | undefined;
  let cleanupWorkerListeners: (() => void) | undefined;
  let initializationPromise: Promise<void> | undefined;
  let disposed = false;
  const queue = new PQueue({ concurrency: 1 });
  queue.on("error", () => {});
  const queueSignals = new Set<AbortController>();
  const latestPending = new Map<
    string,
    Map<number, { controller: AbortController; started: boolean }>
  >();
  let sequence = 0;
  let workerGeneration = 0;
  // The snapshot currently synced inside the Worker session.
  let workerProjectId: string | undefined;
  let workerSnapshot: ProjectSnapshot | undefined;

  // Callbacks for aborting the currently-executing step.
  let stopCurrent: ((error: Error) => void) | undefined;
  let stopInitialization: ((error: Error) => void) | undefined;

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
  // Queue management
  // -------------------------------------------------------------------------

  function abortError(): DOMException {
    return new DOMException("The operation was aborted", "AbortError");
  }

  function enqueue<T>(
    operation: string,
    run: () => Promise<T>,
    signal: AbortSignal | undefined,
    scope: string,
    requestedPriority: string | undefined,
    timing: { executionStartedAt?: number; executionDurationMs?: number } = {},
  ): Promise<T> {
    if (signal?.aborted) return Promise.reject(signal.reason ?? abortError());
    const priority =
      (requestPriorities as Record<string, number>)[requestedPriority ?? ""] ??
      operationPriorities[operation] ??
      0;

    const key = (name: string) => JSON.stringify([scope, name]);
    const pendingKey = key(operation);

    const pendingGroup = () => {
      let group = latestPending.get(pendingKey);
      if (!group) {
        group = new Map();
        latestPending.set(pendingKey, group);
      }
      return group;
    };

    const removePending = (
      pendingPriority: number,
      pending: { controller: AbortController; started: boolean },
    ) => {
      const group = latestPending.get(pendingKey);
      if (group?.get(pendingPriority) !== pending) return;
      group.delete(pendingPriority);
      if (group.size === 0) latestPending.delete(pendingKey);
    };

    const supersedePending = (name: string, maximumPriority: number, message: string) => {
      const groupKey = key(name);
      const group = latestPending.get(groupKey);
      if (!group) return;
      for (const [pendingPriority, pending] of group) {
        if (pendingPriority > maximumPriority) continue;
        group.delete(pendingPriority);
        pending.controller.abort(new DOMException(message, "AbortError"));
      }
      if (group.size === 0) latestPending.delete(groupKey);
    };

    if (["render", "evaluate", "check"].includes(operation)) {
      supersedePending("lint", priority, "An explicit run replaced this pending lint");
    } else if (priority > requestPriorities.background && operation !== "lint") {
      supersedePending(
        "lint",
        requestPriorities.background,
        "An interactive operation replaced this pending background lint",
      );
    }

    const queueController = new AbortController();
    const pending = { controller: queueController, started: false };
    if (latestOnlyOperations.has(operation)) {
      supersedePending(operation, priority, "A newer operation replaced this request");
      pendingGroup().set(priority, pending);
    }

    queueSignals.add(queueController);
    const abortWhileQueued = () => {
      if (!pending.started) queueController.abort(signal?.reason ?? abortError());
    };
    signal?.addEventListener("abort", abortWhileQueued, { once: true });

    return queue
      .add(
        async () => {
          pending.started = true;
          if (latestOnlyOperations.has(operation)) removePending(priority, pending);
          timing.executionStartedAt = performance.now();
          try {
            return (await run()) as T;
          } finally {
            timing.executionDurationMs = Math.max(
              0,
              performance.now() - timing.executionStartedAt!,
            );
          }
        },
        { priority, signal: queueController.signal },
      )
      .finally(() => {
        signal?.removeEventListener("abort", abortWhileQueued);
        queueSignals.delete(queueController);
        if (latestOnlyOperations.has(operation)) removePending(priority, pending);
      }) as Promise<T>;
  }

  // -------------------------------------------------------------------------
  // Worker lifecycle
  // -------------------------------------------------------------------------

  function startWorker(): Promise<void> {
    if (disposed) return Promise.reject(new Error("MCL runtime has been disposed"));
    if (initializationPromise) return initializationPromise;

    const initializationStartedAt = performance.now();
    const currentRawWorker = workerFactory();
    rawWorker = currentRawWorker;
    const currentApi = Comlink.wrap<WorkerApi>(currentRawWorker);
    workerApi = currentApi;
    workerGeneration++;
    workerProjectId = undefined;
    workerSnapshot = undefined;

    let lastStatus: string | undefined;
    const reportStatus = (status: RuntimeStatus) => {
      if (lastStatus === status) return;
      lastStatus = status;
      if (status === "ready" || status === "error") {
        emitStatus(status, {
          initializationDurationMs: Math.max(0, performance.now() - initializationStartedAt),
        });
      } else {
        emitStatus(status);
      }
    };

    let resolveReady: () => void;
    let rejectReady: (error: unknown) => void;
    const initialization = new Promise<void>((resolve, reject) => {
      resolveReady = resolve;
      rejectReady = reject;
    });
    initializationPromise = initialization;

    // Status messages from the worker arrive via standard message events.
    // (Comlink forwards non-Comlink messages transparently.)
    const onStatusMessage = (event: MessageEvent<unknown>) => {
      if (rawWorker !== currentRawWorker) return;
      const data = event.data as Record<string, unknown> | null;
      if (data?.type === "status" && typeof data.status === "string") {
        reportStatus(data.status as RuntimeStatus);
      } else if (data?.type === "fatal") {
        const error = new Error(
          typeof data.message === "string" ? data.message : "MCL WebAssembly exited unexpectedly",
        );
        if (initialized) {
          emitStatus("error");
          terminateWorker(error);
        } else fail(error);
      }
    };
    currentRawWorker.addEventListener("message", onStatusMessage);

    // Worker-level error (uncaught exception or load failure).
    const onWorkerError = (event: ErrorEvent) => {
      if (rawWorker !== currentRawWorker) return;
      const error = new Error(event.message || "MCL worker failed");
      if (initialized) {
        emitStatus("error");
        terminateWorker(error);
      } else fail(error);
    };
    currentRawWorker.addEventListener("error", onWorkerError);

    // Malformed message from worker.
    const onMessageError = (event: MessageEvent<unknown>) => {
      if (rawWorker !== currentRawWorker) return;
      const error = new Error("MCL worker sent a malformed message");
      if (initialized) {
        emitStatus("error");
        terminateWorker(error);
      } else fail(error);
    };
    currentRawWorker.addEventListener("messageerror", onMessageError);
    cleanupWorkerListeners = () => {
      currentRawWorker.removeEventListener("message", onStatusMessage);
      currentRawWorker.removeEventListener("error", onWorkerError);
      currentRawWorker.removeEventListener("messageerror", onMessageError);
    };

    let initialized = false;

    const timer = setTimeout(
      () =>
        fail(
          new MclRuntimeError({
            code: "MCL-WASM-TIMEOUT",
            message: "MCL WebAssembly initialization timed out",
          }),
        ),
      initializationTimeoutMs,
    );

    function fail(error: Error): void {
      if (rawWorker !== currentRawWorker) return;
      if (!initialized) reportStatus("error");
      clearTimeout(timer);
      if (stopInitialization === fail) stopInitialization = undefined;
      terminateWorker(error);
      rejectReady(error);
    }

    stopInitialization = fail;

    // Kick off WASM initialization via Comlink.
    currentApi
      .initialize({ wasmURL, wasmExecURL })
      .then(() => {
        if (rawWorker !== currentRawWorker) return;
        initialized = true;
        reportStatus("ready");
        clearTimeout(timer);

        if (stopInitialization === fail) stopInitialization = undefined;
        resolveReady();
      })
      .catch((error: unknown) => {
        fail(error instanceof Error ? error : new Error(String(error)));
      });

    return initialization;
  }

  function terminateWorker(error: Error): void {
    const currentWorker = rawWorker;
    const currentApi = workerApi;
    const currentStop = stopCurrent;
    const cleanup = cleanupWorkerListeners;
    cleanupWorkerListeners = undefined;
    rawWorker = undefined;
    workerApi = undefined;
    initializationPromise = undefined;
    workerProjectId = undefined;
    workerSnapshot = undefined;
    stopCurrent = undefined;
    cleanup?.();
    currentWorker?.terminate();
    currentApi?.[Comlink.releaseProxy]();
    currentStop?.(error);
  }

  function runWorkerCall<T>(signal: AbortSignal | undefined, call: () => Promise<T>): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      let settled = false;
      const finish = (error?: Error, value?: T) => {
        if (settled) return;
        settled = true;
        signal?.removeEventListener("abort", onAbort);
        if (stopCurrent === stop) stopCurrent = undefined;
        if (error) reject(error);
        else resolve(value as T);
      };
      const stop = (error: Error) => {
        finish(error);
        terminateWorker(error);
      };
      const onAbort = () => stop(new DOMException("The operation was aborted", "AbortError"));
      stopCurrent = stop;
      signal?.addEventListener("abort", onAbort, { once: true });
      if (signal?.aborted) {
        onAbort();
        return;
      }
      call().then(
        (value) => finish(undefined, value),
        (error: unknown) => {
          stop(error instanceof Error ? error : new Error(String(error)));
        },
      );
    });
  }

  async function waitUntilReady(signal: AbortSignal | undefined): Promise<void> {
    if (!signal) return startWorker();
    if (signal.aborted) throw new DOMException("The operation was aborted", "AbortError");
    let onAbort: (() => void) | undefined;
    try {
      await Promise.race([
        startWorker(),
        new Promise<void>((_, reject) => {
          onAbort = () => {
            stopInitialization?.(
              new DOMException("The operation was aborted", "AbortError") as unknown as Error,
            );
            reject(new DOMException("The operation was aborted", "AbortError"));
          };
          signal.addEventListener("abort", onAbort, { once: true });
        }),
      ]);
    } finally {
      if (onAbort) signal.removeEventListener("abort", onAbort);
    }
  }

  // -------------------------------------------------------------------------
  // Project sync
  // -------------------------------------------------------------------------

  async function syncProjectToWorker(
    projectId: string,
    snapshot: ProjectSnapshot,
    requestOptions: RequestOptions,
  ): Promise<void> {
    if (workerProjectId === projectId && workerSnapshot === snapshot) return;
    const api = workerApi!;
    const previousSnapshot = workerProjectId === projectId ? workerSnapshot : undefined;
    const reset = workerProjectId !== projectId;
    const result = computeProjectDelta(snapshot, previousSnapshot, reset);
    if (result === null) return; // nothing changed

    const { delta, binaryFiles, transferables } = result;
    const updateJSON = JSON.stringify(delta);

    // Transfer binary buffers to avoid copying.
    const transferableBinaries: Record<string, Uint8Array> = Object.create(null);
    for (const [path, bytes] of Object.entries(binaryFiles)) {
      transferableBinaries[path] = bytes;
    }

    const response = await runWorkerCall(requestOptions.signal, () =>
      api.updateProject(
        projectId,
        updateJSON,
        Comlink.transfer(transferableBinaries, transferables),
      ),
    );
    if (!response.ok)
      throw new MclRuntimeError(
        response.error ?? { code: "MCL-WASM-PROJECT", message: "Project update failed" },
      );
    workerProjectId = projectId;
    workerSnapshot = snapshot;
  }

  // -------------------------------------------------------------------------
  // Core execute function
  // -------------------------------------------------------------------------

  async function execute(
    operation: MclOperation,
    source: string,
    requestOptions: RequestOptions & {
      entrypoint?: string;
      position?: { line: number; character: number };
      newName?: string;
      oldPath?: string;
      newPath?: string;
    },
    entrypoint: string,
    project: { id: string; snapshot: ProjectSnapshot },
  ): Promise<MclResult> {
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
        timer = setTimeout(() => {
          if (stopCurrent) stopCurrent(timeoutError);
          else stopInitialization?.(timeoutError);
        }, timeoutMs);

        await waitUntilReady(signal);
        if (signal?.aborted) throw new DOMException("The operation was aborted", "AbortError");

        const currentApi = workerApi!;
        const currentGeneration = workerGeneration;
        operationWorkerGeneration = currentGeneration;

        if (project) {
          await syncProjectToWorker(project.id, project.snapshot, requestOptions);
        }

        // Build the WASM request.
        const requestId = ++sequence;
        const request: WasmRequest = {
          requestId,
          revision: requestId,
          operation,
          entrypoint,
          source,
          ...(requestOptions.position ? { position: requestOptions.position } : {}),
          ...(requestOptions.newName ? { newName: requestOptions.newName } : {}),
          ...(requestOptions.oldPath ? { oldPath: requestOptions.oldPath } : {}),
          ...(requestOptions.newPath ? { newPath: requestOptions.newPath } : {}),
        };

        timing.wasmStartedAt = performance.now();
        let response: import("./protocol.js").Response;
        try {
          response = await runWorkerCall(signal, () =>
            currentApi.run(JSON.stringify(request), Comlink.transfer({}, []), project.id),
          );
        } finally {
          timing.wasmDurationMs = Math.max(0, performance.now() - timing.wasmStartedAt!);
          clearTimeout(timer);
        }

        if (!response.ok) {
          throw new MclRuntimeError(
            response.error ?? { code: "MCL-WASM-RESPONSE", message: "Operation failed" },
          );
        }
        if (operation === "lint") {
          return { operation, diagnostics: (response.result ?? []) as ProtocolLintDiagnostic[] };
        }
        return { operation, ...(response.result as object) } as MclResult;
      } catch (error) {
        clearTimeout(timer);
        throw error;
      }
    };

    try {
      const result = await enqueue(
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
        timer = setTimeout(() => stopInitialization?.(timeoutError), timeoutMs);
        await waitUntilReady(signal);
      } finally {
        clearTimeout(timer);
      }
    };
    return enqueue("warmUp", run, signal, "runtime", requestOptions.priority);
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
      for (const controller of queueSignals) controller.abort(error);
      stopCurrent?.(error);
      stopInitialization?.(error);
      terminateWorker(error);
    },
  };
}

import * as Comlink from "comlink";
import type { WorkerApi } from "./worker.js";

export type WorkerStatus = "fetching" | "compiling" | "initializing" | "ready" | "error";

export type WorkerStatusDetails = { initializationDurationMs?: number };

export type WorkerSupervisorHooks = {
  emitStatus: (status: WorkerStatus, details?: WorkerStatusDetails) => void;
  onGeneration: (generation: number) => void;
  onReset: () => void;
};

const wasmURL = new URL("../dist/mcl.wasm", import.meta.url).href;
const wasmExecURL = new URL("../dist/wasm_exec.js", import.meta.url).href;

export function createWorkerSupervisor(
  workerFactory: () => Worker,
  initializationTimeoutMs: number,
  makeInitializationTimeoutError: () => Error,
  hooks: WorkerSupervisorHooks,
) {
  let rawWorker: Worker | undefined;
  let workerApi: Comlink.Remote<WorkerApi> | undefined;
  let cleanupWorkerListeners: (() => void) | undefined;
  let initializationPromise: Promise<void> | undefined;
  let workerGeneration = 0;
  let stopCurrent: ((error: Error) => void) | undefined;
  let stopInitialization: ((error: Error) => void) | undefined;
  let disposed = false;

  function start(): Promise<void> {
    if (disposed) return Promise.reject(new Error("MCL runtime has been disposed"));
    if (initializationPromise) return initializationPromise;

    const initializationStartedAt = performance.now();
    const currentRawWorker = workerFactory();
    rawWorker = currentRawWorker;
    const currentApi = Comlink.wrap<WorkerApi>(currentRawWorker);
    workerApi = currentApi;
    workerGeneration++;
    hooks.onGeneration(workerGeneration);
    hooks.onReset();

    let lastStatus: string | undefined;
    const reportStatus = (status: WorkerStatus) => {
      if (lastStatus === status) return;
      lastStatus = status;
      if (status === "ready" || status === "error") {
        hooks.emitStatus(status, {
          initializationDurationMs: Math.max(0, performance.now() - initializationStartedAt),
        });
      } else {
        hooks.emitStatus(status);
      }
    };

    let resolveReady: () => void;
    let rejectReady: (error: unknown) => void;
    const initialization = new Promise<void>((resolve, reject) => {
      resolveReady = resolve;
      rejectReady = reject;
    });
    initializationPromise = initialization;

    const onStatusMessage = (event: MessageEvent<unknown>) => {
      if (rawWorker !== currentRawWorker) return;
      const data = event.data as Record<string, unknown> | null;
      if (data?.type === "status" && typeof data.status === "string") {
        reportStatus(data.status as WorkerStatus);
      } else if (data?.type === "fatal") {
        const error = new Error(
          typeof data.message === "string" ? data.message : "MCL WebAssembly exited unexpectedly",
        );
        if (initialized) terminate(error);
        else fail(error);
      }
    };
    currentRawWorker.addEventListener("message", onStatusMessage);

    const onWorkerError = (event: ErrorEvent) => {
      if (rawWorker !== currentRawWorker) return;
      const error = new Error(event.message || "MCL worker failed");
      if (initialized) terminate(error);
      else fail(error);
    };
    currentRawWorker.addEventListener("error", onWorkerError);

    const onMessageError = (_event: MessageEvent<unknown>) => {
      if (rawWorker !== currentRawWorker) return;
      const error = new Error("MCL worker sent a malformed message");
      if (initialized) terminate(error);
      else fail(error);
    };
    currentRawWorker.addEventListener("messageerror", onMessageError);
    cleanupWorkerListeners = () => {
      currentRawWorker.removeEventListener("message", onStatusMessage);
      currentRawWorker.removeEventListener("error", onWorkerError);
      currentRawWorker.removeEventListener("messageerror", onMessageError);
    };

    let initialized = false;
    const timer = setTimeout(() => fail(makeInitializationTimeoutError()), initializationTimeoutMs);

    function fail(error: Error): void {
      if (rawWorker !== currentRawWorker) return;
      clearTimeout(timer);
      if (stopInitialization === fail) stopInitialization = undefined;
      terminate(error);
      rejectReady(error);
    }

    stopInitialization = fail;
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

  function terminate(error: Error): void {
    const currentWorker = rawWorker;
    if (!currentWorker) return;
    const currentApi = workerApi;
    const currentStop = stopCurrent;
    const cleanup = cleanupWorkerListeners;
    cleanupWorkerListeners = undefined;
    rawWorker = undefined;
    workerApi = undefined;
    initializationPromise = undefined;
    stopCurrent = undefined;
    hooks.onReset();
    if (!disposed && error.name !== "AbortError") hooks.emitStatus("error");
    cleanup?.();
    currentWorker.terminate();
    currentApi?.[Comlink.releaseProxy]();
    currentStop?.(error);
  }

  function runCall<T>(signal: AbortSignal | undefined, call: () => Promise<T>): Promise<T> {
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
        terminate(error);
      };
      const onAbort = () => stop(new DOMException("The operation was aborted", "AbortError"));
      stopCurrent = stop;
      signal?.addEventListener("abort", onAbort, { once: true });
      if (signal?.aborted) {
        onAbort();
        return;
      }
      try {
        call().then(
          (value) => finish(undefined, value),
          (error: unknown) => stop(error instanceof Error ? error : new Error(String(error))),
        );
      } catch (error) {
        stop(error instanceof Error ? error : new Error(String(error)));
      }
    });
  }

  async function waitUntilReady(signal: AbortSignal | undefined): Promise<void> {
    if (!signal) return start();
    if (signal.aborted) throw new DOMException("The operation was aborted", "AbortError");
    let onAbort: (() => void) | undefined;
    try {
      await Promise.race([
        start(),
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

  function dispose(error: Error): void {
    disposed = true;
    stopCurrent?.(error);
    stopInitialization?.(error);
    terminate(error);
  }

  return {
    get api(): Comlink.Remote<WorkerApi> | undefined {
      return workerApi;
    },
    get generation(): number {
      return workerGeneration;
    },
    start,
    waitUntilReady,
    runCall,
    stopCurrent(error: Error): void {
      stopCurrent?.(error);
    },
    stopInitialization(error: Error): void {
      stopInitialization?.(error);
    },
    terminate,
    dispose,
  };
}

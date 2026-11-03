import { expose } from "comlink";
import type { Response } from "./protocol.js";

// ---------------------------------------------------------------------------
// Types exposed from this worker module
// ---------------------------------------------------------------------------

export type RuntimeStatus = "fetching" | "compiling" | "initializing" | "ready" | "error";

export interface RuntimeAssets {
  wasmURL: string;
  wasmExecURL: string;
}

/** Encoded file sent from the main thread. Binary files omit `content`. */
export interface EncodedFile {
  encoding: "utf8" | "binary";
  content?: string;
}

/** A project update delta serialised as JSON. */
export type ProjectUpdateJSON = string;

export interface WorkerApi {
  /**
   * Load the WASM binary and initialise the Go runtime.
   * Must be called once before any other method.
   */
  initialize(assets: RuntimeAssets): Promise<void>;

  /**
   * Apply a project snapshot delta to the in-WASM session.
   * Returns the WASM JSON response directly.
   */
  updateProject(
    projectId: string,
    updateJSON: ProjectUpdateJSON,
    binaryFiles: Record<string, Uint8Array>,
  ): Response;

  /**
   * Execute an MCL operation and return the WASM JSON response.
   * Pass `projectId` to run inside a project session.
   */
  run(requestJSON: string, projectId?: string): Response;
}

// ---------------------------------------------------------------------------
// Globals set by the Go/WASM runtime
// ---------------------------------------------------------------------------

declare global {
  // Set to `true` by the WASM module once it has finished initialising.
  var mclWasmReady: boolean | undefined;
  // The public API exposed by the WASM module.
  var mclWasm: {
    updateProject(projectId: string, updateJSON: string, binaryFiles: Record<string, Uint8Array>): string;
    run(requestJSON: string, binaryFiles: Record<string, Uint8Array>, projectId?: string): string;
  };
  // Go global injected by wasm_exec.js.
  var Go: new () => {
    importObject: WebAssembly.Imports;
    run(instance: WebAssembly.Instance): Promise<void>;
  };
}

// ---------------------------------------------------------------------------
// Module-level state
// ---------------------------------------------------------------------------

// Saved fetch so network lockdown does not affect WASM loading.
const runtimeFetch = globalThis.fetch.bind(globalThis);

const networkAPINames = [
  "Worker",
  "SharedWorker",
  "XMLHttpRequest",
  "WebSocket",
  "EventSource",
  "WebTransport",
  "WebSocketStream",
  "RTCPeerConnection",
  "importScripts",
] as const;

const originalNetworkAPIs = new Map(
  networkAPINames
    .filter((name) => typeof (globalThis as Record<string, unknown>)[name] === "function")
    .map((name) => [name, (globalThis as Record<string, unknown>)[name]]),
);

let initializationPromise: Promise<void> | undefined;
let wasmExitPromise: Promise<void> | undefined;

// ---------------------------------------------------------------------------
// Network lockdown (defence-in-depth; Go code must not reach the network)
// ---------------------------------------------------------------------------

function denyNetworkAccess(): void {
  globalThis.fetch = () =>
    Promise.reject(new TypeError("Network access is disabled in the MCL runtime"));
  for (const [name] of originalNetworkAPIs) {
    Object.defineProperty(globalThis, name, {
      configurable: true,
      writable: true,
      value: function MclNetworkAccessDenied() {
        throw new TypeError("Network access is disabled in the MCL runtime");
      },
    });
  }
}

// ---------------------------------------------------------------------------
// WASM initialisation
// ---------------------------------------------------------------------------

function waitForWasmReady(): Promise<void> {
  return new Promise((resolve) => {
    Object.defineProperty(globalThis, "mclWasmReady", {
      configurable: true,
      set(value: unknown) {
        Object.defineProperty(globalThis, "mclWasmReady", {
          configurable: true,
          writable: true,
          value,
        });
        if (value === true) resolve();
      },
    });
  });
}

// ---------------------------------------------------------------------------
// WorkerApi implementation
// ---------------------------------------------------------------------------

const api: WorkerApi = {
  async initialize({ wasmURL, wasmExecURL }: RuntimeAssets): Promise<void> {
    if (initializationPromise) return initializationPromise;

    initializationPromise = (async () => {
      // Dynamic import of wasm_exec.js so Go is available in this scope.
      self.postMessage({ type: "status", status: "fetching" });
      await import(wasmExecURL);
      const go = new globalThis.Go();

      const response = await runtimeFetch(wasmURL);
      if (!response.ok) throw new Error(`Unable to load MCL WebAssembly: ${response.status}`);

      // Lock down network access before the Go runtime starts.
      denyNetworkAccess();

      let instance: WebAssembly.Instance;
      try {
        self.postMessage({ type: "status", status: "compiling" });
        ({ instance } = await WebAssembly.instantiateStreaming(response.clone(), go.importObject));
      } catch {
        ({ instance } = await WebAssembly.instantiate(await response.arrayBuffer(), go.importObject));
      }

      const wasmReady = waitForWasmReady();
      self.postMessage({ type: "status", status: "initializing" });
      wasmExitPromise = go.run(instance).then(() => {
        throw new Error("MCL WebAssembly exited unexpectedly");
      });
      wasmExitPromise.catch((error: unknown) => {
        self.postMessage({
          type: "fatal",
          message: error instanceof Error ? error.message : String(error),
        });
      });
      await Promise.race([
        wasmReady,
        wasmExitPromise.then(() => {
          throw new Error("MCL WebAssembly exited before initialization completed");
        }),
      ]);
      self.postMessage({ type: "status", status: "ready" });
    })().catch((error: unknown) => {
      // Reset so a retry attempt can call initialize() again.
      initializationPromise = undefined;
      throw error;
    });

    return initializationPromise;
  },

  updateProject(
    projectId: string,
    updateJSON: ProjectUpdateJSON,
    binaryFiles: Record<string, Uint8Array>,
  ): Response {
    return JSON.parse(globalThis.mclWasm.updateProject(projectId, updateJSON, binaryFiles)) as Response;
  },

  run(requestJSON: string, projectId?: string): Response {
    return JSON.parse(globalThis.mclWasm.run(requestJSON, {}, projectId)) as Response;
  },
};

expose(api);

// (no re-exports needed here)

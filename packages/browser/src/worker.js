let initialized;
let runtimeAssets;
let networkAccessDenied = false;
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
];
const originalFetch = globalThis.fetch;
const originalNetworkAPIs = new Map(
  networkAPINames
    .filter((name) => typeof globalThis[name] === "function")
    .map((name) => [name, globalThis[name]]),
);

function denyNetworkAccess() {
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
  networkAccessDenied = true;
}

async function probeNetworkAccess(url) {
  let fetchBlocked = false;
  try {
    await globalThis.fetch(url);
  } catch {
    fetchBlocked = true;
  }

  const availableAPIs = ["fetch", ...originalNetworkAPIs.keys()];
  const blockedAPIs = availableAPIs.filter((name) =>
    name === "fetch"
      ? globalThis.fetch !== originalFetch
      : globalThis[name] !== originalNetworkAPIs.get(name),
  );
  return { fetchBlocked, availableAPIs, blockedAPIs };
}

function waitForWasmReady() {
  return new Promise((resolve) => {
    Object.defineProperty(globalThis, "mclWasmReady", {
      configurable: true,
      set(value) {
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

async function initialize() {
  if (initialized) return initialized;
  initialized = (async () => {
    if (!runtimeAssets?.wasmURL || !runtimeAssets?.wasmExecURL)
      throw new Error("MCL runtime assets were not configured");
    await import(runtimeAssets.wasmExecURL);
    const go = new Go();
    const response = await runtimeFetch(runtimeAssets.wasmURL);
    if (!response.ok) throw new Error(`Unable to load MCL WebAssembly: ${response.status}`);
    // Load the runtime before denying network APIs, then lock them before Go starts.
    denyNetworkAccess();
    let instance;
    try {
      ({ instance } = await WebAssembly.instantiateStreaming(response.clone(), go.importObject));
    } catch {
      ({ instance } = await WebAssembly.instantiate(await response.arrayBuffer(), go.importObject));
    }
    const wasmReady = waitForWasmReady();
    await Promise.race([
      wasmReady,
      go.run(instance).then(() => {
        throw new Error("MCL WebAssembly exited before initialization completed");
      }),
    ]);
    postMessage({ type: "ready" });
  })().catch((error) => {
    postMessage({ type: "fatal", message: error instanceof Error ? error.message : String(error) });
    throw error;
  });
  return initialized;
}

function encodeFiles(files) {
  // Paths come from project input and may legally contain object property
  // names such as `__proto__`; keep them as data when serializing the request.
  const encoded = Object.create(null);
  const binaryFiles = Object.create(null);
  for (const [filePath, content] of Object.entries(files ?? {})) {
    if (typeof content === "string") {
      encoded[filePath] = { encoding: "utf8", content };
    } else if (content instanceof Uint8Array) {
      encoded[filePath] = { encoding: "binary" };
      binaryFiles[filePath] = content;
    } else {
      throw new TypeError(`Project file ${filePath} must be a string or Uint8Array`);
    }
  }
  return { files: encoded, binaryFiles };
}

self.addEventListener("message", async (event) => {
  const message = event.data;
  if (message?.type === "network-probe" && networkAccessDenied) {
    const result = await probeNetworkAccess(message.url);
    postMessage({ type: "network-probe-result", requestId: message.requestId, ...result });
    return;
  }
  if (message?.type === "configure") {
    runtimeAssets = message;
    initialize().catch(() => {});
    return;
  }
  if (message?.type === "project-update") {
    try {
      await initialize();
      const binaryFiles = message.binaryFiles ?? Object.create(null);
      const result = JSON.parse(
        globalThis.mclWasm.updateProject(message.projectId, message.update, binaryFiles),
      );
      postMessage({ type: "project-updated", requestId: message.requestId, value: result });
    } catch (error) {
      postMessage({
        type: "project-updated",
        requestId: message.requestId,
        value: {
          ok: false,
          error: {
            code: "MCL-WASM-PROJECT",
            message: error instanceof Error ? error.message : String(error),
          },
        },
      });
    }
    return;
  }
  if (message?.type !== "run") return;
  try {
    await initialize();
    const encodedFiles = encodeFiles(message.files);
    const request = {
      requestId: message.requestId,
      revision: message.requestId,
      operation: message.operation,
      entrypoint: message.entrypoint,
      position: message.position,
      source: message.source,
      files: encodedFiles.files,
    };
    const value = JSON.parse(
      message.projectId === undefined
        ? globalThis.mclWasm.run(JSON.stringify(request), encodedFiles.binaryFiles)
        : globalThis.mclWasm.run(
            JSON.stringify(request),
            encodedFiles.binaryFiles,
            message.projectId,
          ),
    );
    postMessage({
      type: "response",
      requestId: message.requestId,
      value,
    });
  } catch (error) {
    postMessage({
      type: "response",
      requestId: message.requestId,
      value: {
        ok: false,
        error: {
          code: "MCL-WASM-INPUT",
          message: error instanceof Error ? error.message : String(error),
        },
      },
    });
  }
});

import { Volume, constants } from "memfs";
import path from "path-browserify";

const projectRoot = "/mcl-project";
let initialized;
let runtimeAssets;
let activeVolume = new Volume();
let workingDirectory = projectRoot;
let networkAccessDenied = false;
const runtimeFetch = globalThis.fetch.bind(globalThis);

globalThis.path = path.posix;
globalThis.process = {
  argv: [],
  env: {},
  pid: 1,
  ppid: 1,
  platform: "browser",
  cwd: () => workingDirectory,
  chdir: (directory) => { workingDirectory = path.posix.resolve(workingDirectory, directory); },
  getuid: () => 1000,
  getgid: () => 1000,
  geteuid: () => 1000,
  getegid: () => 1000,
  getgroups: () => [1000],
  umask: () => 0o022,
};

function createFsAdapter() {
  return new Proxy({
    constants,
    writeSync(fd, bytes) {
      const text = new TextDecoder().decode(bytes);
      (fd === 2 ? console.error : console.log)(text);
      return bytes.length;
    },
  }, {
    get(target, key) {
      if (key in target) return target[key];
      if (typeof key !== "string") return undefined;
      return (...args) => {
        const callback = args.pop();
        try {
          const operation = activeVolume[`${key}Sync`];
          if (typeof operation !== "function") throw Object.assign(new Error(`Unsupported filesystem operation: ${key}`), { code: "ENOSYS" });
          const result = operation.apply(activeVolume, args);
          if (key === "read" || key === "write") callback(null, result, args[1]);
          else callback(null, result);
        } catch (error) {
          callback(error);
        }
      };
    },
  });
}

function resetFilesystem() {
  // Helm and Kustomize adapters still use os paths; keep their temporary files isolated per request.
  activeVolume = new Volume();
  activeVolume.mkdirSync(projectRoot, { recursive: true });
  activeVolume.mkdirSync("/tmp", { recursive: true });
  workingDirectory = projectRoot;
}

async function initialize() {
  if (initialized) return initialized;
  initialized = (async () => {
    if (!runtimeAssets?.wasmURL || !runtimeAssets?.wasmExecURL) throw new Error("MCL runtime assets were not configured");
    globalThis.fs = createFsAdapter();
    await import(runtimeAssets.wasmExecURL);
    const go = new Go();
    const response = await runtimeFetch(runtimeAssets.wasmURL);
    if (!response.ok) throw new Error(`Unable to load MCL WebAssembly: ${response.status}`);
    let instance;
    try {
      ({ instance } = await WebAssembly.instantiateStreaming(response.clone(), go.importObject));
    } catch {
      ({ instance } = await WebAssembly.instantiate(await response.arrayBuffer(), go.importObject));
    }
    void go.run(instance);
    while (!globalThis.mclWasmReady) await new Promise((resolve) => setTimeout(resolve, 0));
    globalThis.fetch = () => Promise.reject(new TypeError("Network access is disabled in the MCL runtime"));
    networkAccessDenied = true;
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
    try {
      await fetch(message.url);
      postMessage({ type: "network-probe-result", requestId: message.requestId, blocked: false });
    } catch {
      postMessage({ type: "network-probe-result", requestId: message.requestId, blocked: true });
    }
    return;
  }
  if (message?.type === "configure") {
    runtimeAssets = message;
    initialize().catch(() => {});
    return;
  }
  if (message?.type !== "run") return;
  try {
    await initialize();
    resetFilesystem();
    const encodedFiles = encodeFiles(message.files);
    const request = {
      requestId: message.requestId,
      revision: message.requestId,
      operation: message.operation,
      entrypoint: message.entrypoint,
      source: message.source,
      files: encodedFiles.files,
    };
    const value = JSON.parse(globalThis.mclWasm.run(JSON.stringify(request), encodedFiles.binaryFiles));
    postMessage({ type: "response", requestId: message.requestId, value });
  } catch (error) {
    postMessage({
      type: "response",
      requestId: message.requestId,
      value: {
        ok: false,
        error: { code: "MCL-WASM-INPUT", message: error instanceof Error ? error.message : String(error) },
      },
    });
  }
});

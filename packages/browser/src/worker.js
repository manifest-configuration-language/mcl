import { Buffer } from "buffer";
import { Volume, constants } from "memfs";
import path from "path-browserify";

const projectRoot = "/mcl-project";
const maxSourceBytes = 64 * 1024;
const maxProjectFiles = 4096;
const maxProjectFileBytes = 16 * 1024 * 1024;
const maxProjectBytes = 64 * 1024 * 1024;
let initialized;
let runtimeAssets;
let activeVolume;
let workingDirectory = projectRoot;
let networkAccessDenied = false;
const runtimeFetch = globalThis.fetch.bind(globalThis);

globalThis.Buffer = Buffer;
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

function isSafeRelativePath(filePath) {
  return typeof filePath === "string" && filePath.length > 0 && !filePath.startsWith("/") && !filePath.includes("\\") && filePath.split("/").every((part) => part && part !== "." && part !== "..");
}

function mountProject(source, files, entrypoint) {
  if (!isSafeRelativePath(entrypoint)) throw new Error(`Invalid project entrypoint: ${entrypoint}`);
  const sourceBytes = new TextEncoder().encode(source).byteLength;
  if (sourceBytes > maxSourceBytes) throw new Error(`Source exceeds the ${maxSourceBytes}-byte limit`);
  const entries = Object.entries(files ?? {});
  if (entries.length + 1 > maxProjectFiles) throw new Error(`Project has more than ${maxProjectFiles} files`);
  activeVolume = new Volume();
  activeVolume.mkdirSync(projectRoot, { recursive: true });
  const entryPath = path.posix.join(projectRoot, entrypoint);
  activeVolume.mkdirSync(path.posix.dirname(entryPath), { recursive: true });
  activeVolume.writeFileSync(entryPath, source);
  if (activeVolume.readFileSync(entryPath, "utf8") !== source) throw new Error(`Unable to mount project entrypoint: ${entrypoint}`);
  let totalBytes = sourceBytes;
  for (const [relativePath, content] of entries) {
    if (!isSafeRelativePath(relativePath) || relativePath === entrypoint) {
      throw new Error(`Invalid project file path: ${relativePath}`);
    }
    if (typeof content !== "string" && !(content instanceof Uint8Array)) {
      throw new TypeError(`Project file ${relativePath} must be a string or Uint8Array`);
    }
    const fileBytes = typeof content === "string" ? new TextEncoder().encode(content).byteLength : content.byteLength;
    if (fileBytes > maxProjectFileBytes) throw new RangeError(`Project file ${relativePath} exceeds the ${maxProjectFileBytes}-byte limit`);
    totalBytes += fileBytes;
    if (totalBytes > maxProjectBytes) throw new Error(`Project files exceed the ${maxProjectBytes}-byte limit`);
    const absolutePath = path.posix.join(projectRoot, relativePath);
    activeVolume.mkdirSync(path.posix.dirname(absolutePath), { recursive: true });
    activeVolume.writeFileSync(absolutePath, content);
  }
  workingDirectory = projectRoot;
  globalThis.fs = createFsAdapter();
}

async function initialize() {
  if (initialized) return initialized;
  initialized = (async () => {
    if (!runtimeAssets?.wasmURL || !runtimeAssets?.wasmExecURL) throw new Error("MCL runtime assets were not configured");
    activeVolume = new Volume();
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
  const encoded = {};
  for (const [filePath, content] of Object.entries(files ?? {})) {
    if (typeof content === "string") {
      encoded[filePath] = { encoding: "utf8", content };
    } else if (content instanceof Uint8Array) {
      let binary = "";
      for (let offset = 0; offset < content.length; offset += 0x8000) {
        binary += String.fromCharCode(...content.subarray(offset, offset + 0x8000));
      }
      encoded[filePath] = { encoding: "base64", content: btoa(binary) };
    } else {
      throw new TypeError(`Project file ${filePath} must be a string or Uint8Array`);
    }
  }
  return encoded;
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
    mountProject(message.source, message.files, message.entrypoint || "main.mcl");
    const request = {
      requestId: message.requestId,
      revision: message.requestId,
      operation: message.operation,
      entrypoint: message.entrypoint,
      source: message.source,
      files: encodeFiles(message.files),
    };
    const value = JSON.parse(globalThis.mclWasm.run(JSON.stringify(request)));
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

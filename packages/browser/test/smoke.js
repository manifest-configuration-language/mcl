import { createMclRuntime } from "../src/index.js";
import source from "./app/main.mcl?raw";
import resourceModule from "./app/modules/resource.mcl?raw";
import schema from "./app/schema.json?raw";

const output = document.querySelector("#result");
async function run() {
  let worker;
  let workerStartedAt;
  let wasmMemoryBytes;
  const initializationTimes = [];
  const runtime = createMclRuntime({
    initializationTimeoutMs: 60_000,
    operationTimeoutMs: 60_000,
    workerFactory: () => {
      worker = new Worker(new URL("../src/worker.js", import.meta.url), { type: "module" });
      workerStartedAt = performance.now();
      worker.addEventListener("message", (event) => {
        if (event.data?.type === "ready") initializationTimes.push(performance.now() - workerStartedAt);
        if (Number.isInteger(event.data?.wasmMemoryBytes)) wasmMemoryBytes = event.data.wasmMemoryBytes;
      });
      return worker;
    },
  });
  const project = runtime.openProject({ "app/main.mcl": source, "app/modules/resource.mcl": resourceModule });
  const controller = new AbortController();
  const cancellationStartedAt = performance.now();
  const cancelled = project.render({ entrypoint: "app/main.mcl", signal: controller.signal });
  setTimeout(() => controller.abort(), 1);
  try {
    await cancelled;
    throw new Error("Expected the first render to be cancelled during WebAssembly initialization");
  } catch (error) {
    if (error.name !== "AbortError") throw error;
  }
  const cancellationMs = performance.now() - cancellationStartedAt;

  const parsed = await project.parse({ entrypoint: "app/main.mcl", timeoutMs: 60_000 });
  if (!Number.isInteger(parsed.declarations) || parsed.declarations < 1) {
    throw new Error(`Unexpected parse result: ${JSON.stringify(parsed)}`);
  }
  const linted = await project.lint({ entrypoint: "app/main.mcl", timeoutMs: 60_000 });
  if (!Array.isArray(linted.diagnostics)) {
    throw new Error(`Unexpected lint result: ${JSON.stringify(linted)}`);
  }
  const formatted = await project.format({ entrypoint: "app/main.mcl", timeoutMs: 60_000 });
  if (!formatted.source.includes("emit resources.resource")) {
    throw new Error(`Unexpected format result: ${formatted.source}`);
  }
  const checked = await project.check({ entrypoint: "app/main.mcl", timeoutMs: 60_000 });
  if (!checked.valid || checked.artifacts !== 1) {
    throw new Error(`Unexpected check result: ${JSON.stringify(checked)}`);
  }
  const evaluated = await project.evaluate({ entrypoint: "app/main.mcl", timeoutMs: 60_000 });
  if (evaluated.artifacts !== 1 || evaluated.renderedBytes < 1) {
    throw new Error(`Unexpected evaluate result: ${JSON.stringify(evaluated)}`);
  }

  const firstRenderStartedAt = performance.now();
  const result = await project.render({ entrypoint: "app/main.mcl", timeoutMs: 60_000 });
  const firstRenderMs = performance.now() - firstRenderStartedAt;
  if (!result.yaml.includes("mcl-browser-smoke") || !result.yaml.includes('key: "value"')) {
    throw new Error(`Unexpected render output: ${result.yaml}`);
  }
  const repeatRenderStartedAt = performance.now();
  await project.render({ entrypoint: "app/main.mcl", timeoutMs: 60_000 });
  const repeatRenderMs = performance.now() - repeatRenderStartedAt;

  let largeProjectMetrics;
  if (new URLSearchParams(location.search).has("large-project")) {
    const largeFile = new Uint8Array(16 * 1024 * 1024);
    const largeProjectStartedAt = performance.now();
    const largeProject = runtime.openProject({
      "main.mcl": 'emit "large-project-smoke"\n',
      "fixtures/payload-1.bin": largeFile,
      "fixtures/payload-2.bin": largeFile,
    });
    const largeProjectLoadMs = performance.now() - largeProjectStartedAt;
    const largeProjectRenderStartedAt = performance.now();
    const largeProjectResult = await largeProject.render({ timeoutMs: 60_000 });
    const largeProjectRenderMs = performance.now() - largeProjectRenderStartedAt;
    if (!largeProjectResult.yaml.includes('"large-project-smoke"')) {
      throw new Error(`Unexpected large project output: ${largeProjectResult.yaml}`);
    }
    largeProjectMetrics = { largeProjectLoadMs, largeProjectRenderMs };
  }
  const binarySource = `type BinaryConfig = schema("./schemas/config.json")

let config: BinaryConfig =
  name: "mcl-browser-binary-smoke"

emit config`;
  const binaryFiles = { "schemas/config.json": new TextEncoder().encode(schema) };
  for (let attempt = 0; attempt < 2; attempt++) {
    const binaryResult = await runtime.evaluate(binarySource, binaryFiles, { timeoutMs: 60_000 });
    if (binaryResult.artifacts !== 1 || binaryFiles["schemas/config.json"].byteLength === 0) {
      throw new Error("Binary project files were not reusable after transfer");
    }
  }
  const networkBlocked = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("Network isolation probe timed out")), 5000);
    const onMessage = (event) => {
      if (event.data?.type !== "network-probe-result" || event.data.requestId !== "network-probe") return;
      clearTimeout(timer);
      worker.removeEventListener("message", onMessage);
      resolve(event.data.blocked);
    };
    worker.addEventListener("message", onMessage);
    worker.postMessage({ type: "network-probe", requestId: "network-probe", url: "https://example.com/" });
  });
  if (!await networkBlocked) throw new Error("MCL worker allowed an external network request");
  runtime.dispose();
  try {
    await runtime.parse(source);
    throw new Error("Expected a disposed runtime to reject new work");
  } catch (error) {
    if (!error.message.includes("disposed")) throw error;
  }
  output.dataset.status = "passed";
  output.dataset.metrics = JSON.stringify({
    initializationMs: initializationTimes.at(-1),
    firstRenderMs,
    repeatRenderMs,
    cancellationMs,
    wasmMemoryBytes,
    ...largeProjectMetrics,
  });
  output.textContent = output.dataset.metrics;
}

run().catch((error) => {
  output.dataset.status = "failed";
  output.textContent = error instanceof Error ? error.stack : String(error);
});

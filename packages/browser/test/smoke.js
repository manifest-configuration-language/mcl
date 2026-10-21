import { createMclRuntime } from "../src/index.js";
import source from "./app/main.mcl?raw";
import resourceModule from "./app/modules/resource.mcl?raw";

const output = document.querySelector("#result");
async function run() {
  let worker;
  const runtime = createMclRuntime({
    initializationTimeoutMs: 60_000,
    operationTimeoutMs: 60_000,
    workerFactory: () => {
      worker = new Worker(new URL("../src/worker.js", import.meta.url), { type: "module" });
      return worker;
    },
  });
  const project = runtime.openProject({ "app/main.mcl": source, "app/modules/resource.mcl": resourceModule });
  const controller = new AbortController();
  const cancelled = project.render({ entrypoint: "app/main.mcl", signal: controller.signal });
  setTimeout(() => controller.abort(), 1);
  try {
    await cancelled;
    throw new Error("Expected the first render to be cancelled during WebAssembly initialization");
  } catch (error) {
    if (error.name !== "AbortError") throw error;
  }

  const result = await project.render({ entrypoint: "app/main.mcl", timeoutMs: 60_000 });
  if (!result.yaml.includes("mcl-browser-smoke") || !result.yaml.includes('key: "value"')) {
    throw new Error(`Unexpected render output: ${result.yaml}`);
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
  output.textContent = "passed";
}

run().catch((error) => {
  output.dataset.status = "failed";
  output.textContent = error instanceof Error ? error.stack : String(error);
});

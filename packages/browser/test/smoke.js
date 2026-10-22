import { createMclRuntime, MclRuntimeError } from "@mcl/browser";
import source from "./app/main.mcl?raw";
import resourceModule from "./app/modules/resource.mcl?raw";
import schema from "./app/schema.json?raw";
import manifestMain from "../../../cmd/mcl-wasm/testdata/examples/adapters/manifest/main.mcl?raw";
import manifestResources from "../../../cmd/mcl-wasm/testdata/examples/adapters/manifest/fixtures/resources.yaml?raw";
import helmMain from "../../../cmd/mcl-wasm/testdata/examples/adapters/helm/main.mcl?raw";
import helmChart from "../../../cmd/mcl-wasm/testdata/examples/adapters/helm/chart/Chart.yaml?raw";
import helmValues from "../../../cmd/mcl-wasm/testdata/examples/adapters/helm/chart/values.yaml?raw";
import helmDeployment from "../../../cmd/mcl-wasm/testdata/examples/adapters/helm/chart/templates/deployment.yaml?raw";
import kustomizeMain from "../../../cmd/mcl-wasm/testdata/examples/adapters/kustomize/main.mcl?raw";
import kustomizeConfig from "../../../cmd/mcl-wasm/testdata/examples/adapters/kustomize/fixtures/base/Kustomization?raw";
import kustomizeDeployment from "../../../cmd/mcl-wasm/testdata/examples/adapters/kustomize/fixtures/base/deployment.yaml?raw";

const output = document.querySelector("#result");
async function run() {
  const packageSmoke = new URLSearchParams(location.search).has("package-smoke");
  let worker;
  let workerStartedAt;
  let wasmMemoryBytes;
  const initializationTimes = [];
  const runtimeOptions = {
    initializationTimeoutMs: 60_000,
    operationTimeoutMs: 60_000,
  };
  if (!packageSmoke) {
    runtimeOptions.workerFactory = () => {
      worker = new Worker(new URL("../src/worker.js", import.meta.url), { type: "module" });
      workerStartedAt = performance.now();
      worker.addEventListener("message", (event) => {
        if (event.data?.type === "ready") initializationTimes.push(performance.now() - workerStartedAt);
        if (Number.isInteger(event.data?.wasmMemoryBytes)) wasmMemoryBytes = event.data.wasmMemoryBytes;
      });
      return worker;
    };
  }
  const runtime = createMclRuntime(runtimeOptions);
  const project = runtime.openProject({ "app/main.mcl": source, "app/modules/resource.mcl": resourceModule });
  let cancellationMs;
  if (!packageSmoke) {
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
    cancellationMs = performance.now() - cancellationStartedAt;
  }

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

  const timeoutProject = runtime.openProject({ "main.mcl": 'emit "timeout-recovery"\n' });
  try {
    await timeoutProject.render({ timeoutMs: 0 });
    throw new Error("Expected the Browser operation to time out");
  } catch (error) {
    if (!(error instanceof MclRuntimeError) || error.code !== "MCL-WASM-TIMEOUT") throw error;
  }
  const recoveredResult = await timeoutProject.render({ timeoutMs: 60_000 });
  if (!recoveredResult.yaml.includes('"timeout-recovery"')) {
    throw new Error(`The Browser runtime did not recover after a timed-out operation: ${recoveredResult.yaml}`);
  }

  const workerBeforeCancellation = worker;
  const cancellationPayload = new Uint8Array(16 * 1024 * 1024);
  const activeEvaluationProject = runtime.openProject({
    "main.mcl": 'emit "cancelled-binary-project"\n',
    "fixtures/payload-1.bin": cancellationPayload,
    "fixtures/payload-2.bin": cancellationPayload,
  });
  const cancellationController = new AbortController();
  const activeEvaluation = activeEvaluationProject.render({ signal: cancellationController.signal, timeoutMs: 60_000 });
  setTimeout(() => cancellationController.abort(), 1);
  try {
    await activeEvaluation;
    throw new Error("Expected the active Browser evaluation to be cancelled");
  } catch (error) {
    if (error.name !== "AbortError") throw error;
  }
  const postCancellationResult = await project.render({ entrypoint: "app/main.mcl", timeoutMs: 60_000 });
  if (!packageSmoke && worker === workerBeforeCancellation) {
    throw new Error("The Browser runtime reused the Worker after cancelling an active evaluation");
  }
  if (!postCancellationResult.yaml.includes("mcl-browser-smoke")) {
    throw new Error(`The Browser runtime did not recover after cancelling an active evaluation: ${postCancellationResult.yaml}`);
  }

  try {
    runtime.openProject({ "../outside.mcl": 'emit "unsafe"\n' });
    throw new Error("The Browser runtime accepted a project path outside the project root");
  } catch (error) {
    if (!(error instanceof TypeError) || !error.message.includes("Invalid project file path")) throw error;
  }
  try {
    runtime.openProject({ "main.mcl": 'emit "oversized"\n', "payload.bin": new Uint8Array(16 * 1024 * 1024 + 1) });
    throw new Error("The Browser runtime accepted a project file above the per-file size limit");
  } catch (error) {
    if (!(error instanceof RangeError) || !error.message.includes("16777216")) throw error;
  }

  const adapterProjects = [
    {
      name: "Manifest",
      files: { "main.mcl": manifestMain, "fixtures/resources.yaml": manifestResources },
      expected: ['name: "api-settings"', 'name: "api"'],
    },
    {
      name: "Helm",
      files: {
        "main.mcl": helmMain,
        "chart/Chart.yaml": helmChart,
        "chart/values.yaml": helmValues,
        "chart/templates/deployment.yaml": helmDeployment,
      },
      expected: ['name: "sample-api"', "replicas: 2", 'image: "example.test.walnuts.dev/api:v1"'],
    },
    {
      name: "Kustomize",
      files: {
        "main.mcl": kustomizeMain,
        "fixtures/base/Kustomization": kustomizeConfig,
        "fixtures/base/deployment.yaml": kustomizeDeployment,
      },
      expected: ['name: "sample-api"', "replicas: 2"],
    },
  ];
  for (const adapter of adapterProjects) {
    const adapterProject = runtime.openProject(adapter.files);
    const adapterResult = await adapterProject.render({ timeoutMs: 60_000 });
    for (const expected of adapter.expected) {
      if (!adapterResult.yaml.includes(expected)) {
        throw new Error(`${adapter.name} adapter output is missing ${JSON.stringify(expected)}: ${adapterResult.yaml}`);
      }
    }
  }

  const remoteDependencyProject = runtime.openProject({
    "main.mcl": 'emit manifest.load("https://example.com/resources.yaml")\n',
  });
  try {
    await remoteDependencyProject.render({ timeoutMs: 60_000 });
    throw new Error("Expected remote dependency access to fail in the Browser runtime");
  } catch (error) {
    if (!(error instanceof MclRuntimeError) || error.code !== "MCL-WASM-NETWORK") throw error;
    if (!error.message.includes("include dependencies in the project files")) {
      throw new Error(`Remote dependency error did not explain the Browser limitation: ${error.message}`);
    }
  }

  const sharedEntrypoint = 'import "modules/shared.mcl" as shared\nemit shared.value\n';
  const firstIsolatedProject = runtime.openProject({
    "main.mcl": sharedEntrypoint,
    "modules/shared.mcl": 'export let value = "first-project"\n',
    "modules/private.mcl": 'export let value = "private-project-file"\n',
  });
  const firstIsolatedResult = await firstIsolatedProject.render({ timeoutMs: 60_000 });
  if (!firstIsolatedResult.yaml.includes('"first-project"')) {
    throw new Error(`Unexpected first project output: ${firstIsolatedResult.yaml}`);
  }
  const secondIsolatedProject = runtime.openProject({
    "main.mcl": sharedEntrypoint,
    "modules/shared.mcl": 'export let value = "second-project"\n',
  });
  const secondIsolatedResult = await secondIsolatedProject.render({ timeoutMs: 60_000 });
  if (!secondIsolatedResult.yaml.includes('"second-project"')) {
    throw new Error(`Project files leaked between projects: ${secondIsolatedResult.yaml}`);
  }
  const missingProjectFile = runtime.openProject({
    "main.mcl": 'import "modules/private.mcl" as leftover\nemit leftover.value\n',
  });
  try {
    await missingProjectFile.render({ timeoutMs: 60_000 });
    throw new Error("A file from a previous project remained in the Browser filesystem");
  } catch (error) {
    if (!(error instanceof MclRuntimeError)) throw error;
  }

  const importedFailureProject = runtime.openProject({
    "main.mcl": 'import "modules/broken.mcl" as broken\nemit broken.value\n',
    "modules/broken.mcl": "# imported module\nlet value = 1\n\nlet broken = @\n",
  });
  try {
    await importedFailureProject.render({ timeoutMs: 60_000 });
    throw new Error("Expected the imported module syntax error to fail rendering");
  } catch (error) {
    if (!(error instanceof MclRuntimeError)) throw error;
    if (error.path !== "modules/broken.mcl" || error.line !== 4) {
      throw new Error(`Imported module error location was ${error.path}:${error.line}, expected modules/broken.mcl:4`);
    }
  }

  const relatedLocationProject = runtime.openProject({
    "main.mcl": 'import "modules/rules.mcl" as checks\n\nlet count: Int = -1\n\nemit count\nvalidate count using checks.positive\n',
    "modules/rules.mcl": 'export rule positive(value: Int):\n  require value > 0 message "must be positive"\n',
  });
  try {
    await relatedLocationProject.render({ timeoutMs: 60_000 });
    throw new Error("Expected the imported validation rule to fail rendering");
  } catch (error) {
    if (!(error instanceof MclRuntimeError)) throw error;
    const related = error.related?.find((location) => location.path === "modules/rules.mcl");
    if (error.path !== "main.mcl" || !related || related.line !== 2) {
      throw new Error(`Related diagnostic locations were not preserved: ${JSON.stringify({ path: error.path, line: error.line, related: error.related })}`);
    }
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
    const largeProjectBytes = largeFile.byteLength * 2;
    const largeProjectLoadMs = performance.now() - largeProjectStartedAt;
    const largeProjectRenderStartedAt = performance.now();
    const largeProjectResult = await largeProject.render({ timeoutMs: 60_000 });
    const largeProjectRenderMs = performance.now() - largeProjectRenderStartedAt;
    if (!largeProjectResult.yaml.includes('"large-project-smoke"')) {
      throw new Error(`Unexpected large project output: ${largeProjectResult.yaml}`);
    }
    largeProjectMetrics = { largeProjectBytes, largeProjectLoadMs, largeProjectRenderMs };
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
  if (!packageSmoke) {
    const networkProbe = new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error("Network isolation probe timed out")), 5000);
      const onMessage = (event) => {
        if (event.data?.type !== "network-probe-result" || event.data.requestId !== "network-probe") return;
        clearTimeout(timer);
        worker.removeEventListener("message", onMessage);
        resolve(event.data);
      };
      worker.addEventListener("message", onMessage);
      worker.postMessage({ type: "network-probe", requestId: "network-probe", url: "https://example.com/" });
    });
    const networkResult = await networkProbe;
    if (!networkResult.fetchBlocked) throw new Error("MCL worker allowed fetch network access");
    const nestedWorkerAPIs = ["Worker", "SharedWorker", "importScripts"];
    const missingNestedWorkerBlocks = networkResult.availableAPIs.filter(
      (name) => nestedWorkerAPIs.includes(name) && !networkResult.blockedAPIs.includes(name),
    );
    if (missingNestedWorkerBlocks.length > 0) {
      throw new Error(`MCL worker left nested worker APIs enabled: ${missingNestedWorkerBlocks.join(", ")}`);
    }
    const missingNetworkBlocks = networkResult.availableAPIs.filter(
      (name) => !nestedWorkerAPIs.includes(name) && !networkResult.blockedAPIs.includes(name),
    );
    if (missingNetworkBlocks.length > 0) {
      throw new Error(`MCL worker did not disable network APIs: ${missingNetworkBlocks.join(", ")}`);
    }
  }
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

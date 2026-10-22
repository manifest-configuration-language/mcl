import { chromium, firefox, webkit } from "playwright";
import { spawn } from "node:child_process";
import { readFile } from "node:fs/promises";
import { setTimeout as delay } from "node:timers/promises";
import { testRuntimeLifecycle } from "../test/runtime-lifecycle.mjs";

const performanceBudget = JSON.parse(
  await readFile(new URL("../performance-budget.json", import.meta.url), "utf8"),
);
const timingBudgets = {
  initializationMs: performanceBudget.maxInitializationMs,
  firstRenderMs: performanceBudget.maxFirstRenderMs,
  repeatRenderMs: performanceBudget.maxRepeatRenderMs,
  cancellationMs: performanceBudget.maxCancellationMs,
  largeProjectLoadMs: performanceBudget.maxLargeProjectLoadMs,
  largeProjectRenderMs: performanceBudget.maxLargeProjectRenderMs,
};
for (const [metric, limit] of Object.entries(timingBudgets)) {
  if (!Number.isSafeInteger(limit) || limit <= 0)
    throw new Error(`The ${metric} performance budget is invalid.`);
}
await testRuntimeLifecycle();

const server = spawn("./node_modules/.bin/vite", ["--config", "test/vite.config.js", "--host", "127.0.0.1", "--port", "4173", "--strictPort"], {
  stdio: ["ignore", "pipe", "pipe"],
});
const serverOutput = [];
server.stdout.on("data", (chunk) => serverOutput.push(chunk.toString()));
server.stderr.on("data", (chunk) => serverOutput.push(chunk.toString()));

try {
  const deadline = Date.now() + 30_000;
  let ready = false;
  while (Date.now() < deadline) {
    if (server.exitCode !== null) throw new Error("Vite smoke server exited before it became ready");
    try {
      const response = await fetch("http://127.0.0.1:4173/");
      if (response.ok) {
        ready = true;
        break;
      }
    } catch {}
    await delay(100);
  }
  if (!ready) throw new Error("Vite smoke server did not become ready");

  for (const browserType of [chromium, firefox, webkit]) {
    const browser = await browserType.launch({ headless: true });
    try {
      const page = await browser.newPage();
      const pageErrors = [];
      let wasmBytes;
      page.on("response", async (response) => {
        if (!response.url().endsWith("/mcl.wasm")) return;
        wasmBytes = Number(await response.headerValue("content-length"));
      });
      page.on("pageerror", (error) => pageErrors.push(error.message));
      page.on("console", (message) => { if (message.type() === "error") pageErrors.push(message.text()); });
      const benchmark = browserType.name() === "chromium" ? "?large-project" : "";
      await page.goto(`http://127.0.0.1:4173/${benchmark}`);
      try {
        await page.locator("#result[data-status='passed']").waitFor({ timeout: 60_000 });
      } catch (error) {
        const body = await page.locator("body").textContent();
        throw new Error(`${error.message}\nBrowser: ${browserType.name()}\nPage: ${body}\nBrowser errors: ${pageErrors.join("\n")}\nVite: ${serverOutput.join("")}`);
      }
      if (pageErrors.length) throw new Error(`${browserType.name()}: ${pageErrors.join("\n")}`);
      const metrics = await page.locator("#result").evaluate((output) => JSON.parse(output.dataset.metrics));
      if (!Number.isInteger(wasmBytes)) throw new Error(`${browserType.name()}: WebAssembly response size was not captured`);
      const metricNames = ["initializationMs", "firstRenderMs", "repeatRenderMs", "cancellationMs", "wasmMemoryBytes"];
      if (browserType.name() === "chromium") {
        metricNames.push("largeProjectLoadMs", "largeProjectRenderMs");
      }
      for (const name of metricNames) {
        if (!Number.isFinite(metrics[name])) throw new Error(`${browserType.name()}: ${name} was not captured`);
        const limit = timingBudgets[name];
        if (limit !== undefined && metrics[name] > limit)
          throw new Error(`${browserType.name()}: ${name} ${metrics[name]} ms exceeds the ${limit}-ms budget`);
      }
      if (metrics.wasmMemoryBytes > performanceBudget.maxWasmMemoryBytes) {
        throw new Error(
          `${browserType.name()}: WebAssembly memory ${metrics.wasmMemoryBytes} bytes exceeds the ${performanceBudget.maxWasmMemoryBytes}-byte budget`,
        );
      }
      console.log(`${browserType.name()} ${JSON.stringify({ wasmBytes, ...metrics })}`);
    } finally {
      await browser.close();
    }
  }
} finally {
  server.kill("SIGTERM");
}

import { chromium, firefox, webkit } from "playwright";
import { spawn } from "node:child_process";
import { createReadStream } from "node:fs";
import { readFile, stat } from "node:fs/promises";
import { Writable } from "node:stream";
import { pipeline } from "node:stream/promises";
import { setTimeout as delay } from "node:timers/promises";
import { createGzip } from "node:zlib";
import { testRuntimeLifecycle } from "../test/runtime-lifecycle.mjs";

const performanceBudget = JSON.parse(
  await readFile(new URL("../performance-budget.json", import.meta.url), "utf8"),
);
const packageSmoke = process.argv.includes("--package-smoke");
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
const wasmPath = new URL("../dist/mcl.wasm", import.meta.url);
const wasmSizeBudget = performanceBudget.baselineBytes;
if (
  !Number.isSafeInteger(wasmSizeBudget?.raw) ||
  wasmSizeBudget.raw <= 0 ||
  !Number.isSafeInteger(wasmSizeBudget?.gzip) ||
  wasmSizeBudget.gzip <= 0 ||
  !Number.isFinite(performanceBudget.maxGrowthPercent) ||
  performanceBudget.maxGrowthPercent < 0
) {
  throw new Error("The WebAssembly size budget is invalid.");
}
const wasmRawBytes = (await stat(wasmPath)).size;
let wasmGzipBytes = 0;
await pipeline(
  createReadStream(wasmPath),
  createGzip({ level: 9, mtime: 0 }),
  new Writable({ write(chunk, _encoding, callback) { wasmGzipBytes += chunk.length; callback(); } }),
);
for (const [format, actual, baseline] of [
  ["raw", wasmRawBytes, wasmSizeBudget.raw],
  ["gzip", wasmGzipBytes, wasmSizeBudget.gzip],
]) {
  const limit = baseline * (1 + performanceBudget.maxGrowthPercent / 100);
  if (actual > limit) {
    throw new Error(
      `WebAssembly ${format} size ${actual} bytes exceeds the ${limit}-byte budget (${performanceBudget.maxGrowthPercent}% growth).`,
    );
  }
}
console.log(`WebAssembly size ${JSON.stringify({ raw: wasmRawBytes, gzip: wasmGzipBytes })}`);
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

  const browserTypes = packageSmoke ? [chromium] : [chromium, firefox, webkit];
  for (const browserType of browserTypes) {
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
      const benchmark = packageSmoke ? "?package-smoke" : "?large-project";
      await page.goto(`http://127.0.0.1:4173/${benchmark}`);
      try {
        await page.locator("#result[data-status='passed']").waitFor({ timeout: 60_000 });
      } catch (error) {
        const body = await page.locator("body").textContent();
        throw new Error(`${error.message}\nBrowser: ${browserType.name()}\nPage: ${body}\nBrowser errors: ${pageErrors.join("\n")}\nVite: ${serverOutput.join("")}`);
      }
      if (pageErrors.length) throw new Error(`${browserType.name()}: ${pageErrors.join("\n")}`);
      if (!Number.isInteger(wasmBytes)) throw new Error(`${browserType.name()}: WebAssembly response size was not captured`);
      const metrics = await page.locator("#result").evaluate((output) => JSON.parse(output.dataset.metrics));
      if (!packageSmoke) {
        const metricNames = [
          "initializationMs",
          "firstRenderMs",
          "repeatRenderMs",
          "cancellationMs",
          "wasmMemoryBytes",
          "largeProjectBytes",
          "largeProjectLoadMs",
          "largeProjectRenderMs",
        ];
        for (const name of metricNames) {
          if (!Number.isFinite(metrics[name])) throw new Error(`${browserType.name()}: ${name} was not captured`);
          const limit = timingBudgets[name];
          if (limit !== undefined && metrics[name] > limit)
            throw new Error(`${browserType.name()}: ${name} ${metrics[name]} ms exceeds the ${limit}-ms budget`);
        }
        if (metrics.largeProjectBytes !== 32 * 1024 * 1024) {
          throw new Error(`${browserType.name()}: large project measured ${metrics.largeProjectBytes} bytes instead of 33554432`);
        }
        if (metrics.wasmMemoryBytes > performanceBudget.maxWasmMemoryBytes) {
          throw new Error(
            `${browserType.name()}: WebAssembly memory ${metrics.wasmMemoryBytes} bytes exceeds the ${performanceBudget.maxWasmMemoryBytes}-byte budget`,
          );
        }
      }
      console.log(`${browserType.name()} ${JSON.stringify({ wasmBytes, ...metrics })}`);
    } finally {
      await browser.close();
    }
  }
} finally {
  server.kill("SIGTERM");
}

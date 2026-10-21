import { chromium } from "playwright";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import { testRuntimeLifecycle } from "../test/runtime-lifecycle.mjs";

await testRuntimeLifecycle();

const server = spawn("./node_modules/.bin/vite", ["--config", "test/vite.config.js", "--host", "127.0.0.1", "--port", "4173", "--strictPort"], {
  stdio: ["ignore", "pipe", "pipe"],
});
let browser;
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

  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage();
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  page.on("console", (message) => { if (message.type() === "error") pageErrors.push(message.text()); });
  await page.goto("http://127.0.0.1:4173/");
  try {
    await page.locator("#result[data-status='passed']").waitFor({ timeout: 60_000 });
  } catch (error) {
    const body = await page.locator("body").textContent();
    throw new Error(`${error.message}\nPage: ${body}\nBrowser: ${pageErrors.join("\n")}\nVite: ${serverOutput.join("")}`);
  }
  if (pageErrors.length) throw new Error(pageErrors.join("\n"));
} finally {
  await browser?.close();
  server.kill("SIGTERM");
}

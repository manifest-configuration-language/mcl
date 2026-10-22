import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { setTimeout as delay } from "node:timers/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const [projectDirectoryInput, browserPackageDirectoryInput] = process.argv.slice(2);
if (!projectDirectoryInput || !browserPackageDirectoryInput) {
  throw new Error("Pass the installed consumer project and Browser package directories.");
}

const projectDirectory = resolve(projectDirectoryInput);
const browserPackageDirectory = resolve(browserPackageDirectoryInput);
const vite = resolve(browserPackageDirectory, "node_modules/.bin/vite");
const require = createRequire(pathToFileURL(resolve(browserPackageDirectory, "package.json")));
const { chromium, firefox, webkit } = require("playwright");
const viteConfig = resolve(projectDirectory, "vite.config.js");
const build = spawnSync(vite, ["build", "--config", viteConfig], {
  cwd: projectDirectory,
  encoding: "utf8",
});
process.stdout.write(build.stdout);
process.stderr.write(build.stderr);
if (build.error) throw build.error;
assert.equal(build.status, 0, "The installed package consumer did not build with Vite");

const server = spawn(vite, ["preview", "--config", viteConfig, "--host", "127.0.0.1", "--port", "4179", "--strictPort"], {
  cwd: projectDirectory,
  stdio: ["ignore", "pipe", "pipe"],
});
const serverOutput = [];
server.stdout.on("data", (chunk) => serverOutput.push(chunk.toString()));
server.stderr.on("data", (chunk) => serverOutput.push(chunk.toString()));

try {
  const deadline = Date.now() + 30_000;
  let ready = false;
  while (Date.now() < deadline) {
    if (server.exitCode !== null) throw new Error("Vite preview exited before it became ready");
    try {
      const response = await fetch("http://127.0.0.1:4179/");
      if (response.ok) {
        ready = true;
        break;
      }
    } catch {}
    await delay(100);
  }
  if (!ready) throw new Error(`Vite preview did not become ready.\n${serverOutput.join("")}`);

  for (const browserType of [chromium, firefox, webkit]) {
    const browser = await browserType.launch({ headless: true });
    try {
      const page = await browser.newPage();
      const pageErrors = [];
      page.on("pageerror", (error) => pageErrors.push(error.message));
      page.on("console", (message) => {
        if (message.type() === "error") pageErrors.push(message.text());
      });
      await page.goto("http://127.0.0.1:4179/");
      try {
        await page.locator("#result[data-status='passed']").waitFor({ timeout: 60_000 });
      } catch (error) {
        const body = await page.locator("body").textContent();
        throw new Error(`${error.message}\nBrowser: ${browserType.name()}\nPage: ${body}\nBrowser errors: ${pageErrors.join("\n")}\nVite: ${serverOutput.join("")}`);
      }
      assert.deepEqual(pageErrors, [], `${browserType.name()} emitted browser errors`);
      const result = await page.locator("#result").evaluate((element) => JSON.parse(element.textContent));
      assert.equal(result.grammarScope, "source.mcl");
      assert.match(result.editorDocument, /emit/);
      assert.match(result.yaml, /mcl-package-consumer-smoke/);
      console.log(`Verified installed Browser and Language packages in a production Vite build with ${browserType.name()}.`);
    } finally {
      await browser.close();
    }
  }
} finally {
  server.kill("SIGTERM");
}

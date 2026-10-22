import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { readFile, stat } from "node:fs/promises";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { createMclRuntime, MclRuntimeError } from "@manifest-configuration-language/browser";
import { mclLanguageSupport, parser } from "@manifest-configuration-language/language";
import grammar from "@manifest-configuration-language/language/textmate" with { type: "json" };

const projectRoot = process.cwd();
const browserPackageRoot = join(projectRoot, "node_modules/@manifest-configuration-language/browser");
const languagePackageRoot = join(projectRoot, "node_modules/@manifest-configuration-language/language");
const browserManifest = JSON.parse(
  await readFile(join(browserPackageRoot, "package.json"), "utf8"),
);
const languageManifest = JSON.parse(
  await readFile(join(languagePackageRoot, "package.json"), "utf8"),
);

assert.equal(browserManifest.version, languageManifest.version);
if (process.env.MCL_VERSION) {
  assert.equal(browserManifest.version, process.env.MCL_VERSION);
}

assert.equal(typeof createMclRuntime, "function");
assert.equal(typeof MclRuntimeError, "function");
assert.ok(mclLanguageSupport());
assert.ok(parser.parse('let value = "installed"').length > 1);
assert.equal(grammar.scopeName, "source.mcl");
assert.equal(import.meta.resolve("@manifest-configuration-language/browser"), pathToFileURL(join(browserPackageRoot, "src/index.js")).href);
assert.ok((await stat(join(browserPackageRoot, "src/worker.js"))).size > 0);
assert.ok((await stat(join(browserPackageRoot, "dist/mcl.wasm"))).size > 0);
assert.ok((await stat(join(browserPackageRoot, "dist/wasm_exec.js"))).size > 0);

class SmokeWorker {
  listeners = new Map();
  requests = [];

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) ?? new Set();
    listeners.add(listener);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type, listener) {
    this.listeners.get(type)?.delete(listener);
  }

  postMessage(message, transferables = []) {
    this.requests.push({ message, transferables });
    queueMicrotask(() => {
      const event = message.type === "configure"
        ? { data: { type: "ready" } }
        : {
            data: {
              type: "response",
              requestId: message.requestId,
              value: { ok: true, result: { yaml: 'name: "tarball-smoke"\n' } },
            },
          };
      for (const listener of this.listeners.get("message") ?? []) listener(event);
    });
  }

  terminate() {}
}

let worker;
const runtime = createMclRuntime({
  workerFactory: () => {
    worker = new SmokeWorker();
    return worker;
  },
});
const project = runtime.openProject({ "main.mcl": 'emit "tarball-smoke"\n' });
const renderResult = await project.render();
assert.deepEqual(renderResult, { operation: "render", yaml: 'name: "tarball-smoke"\n' });
const configure = worker.requests.find(({ message }) => message.type === "configure")?.message;
assert.ok(configure?.wasmURL.endsWith("/dist/mcl.wasm"));
assert.ok(configure?.wasmExecURL.endsWith("/dist/wasm_exec.js"));
const renderRequest = worker.requests.find(({ message }) => message.type === "run")?.message;
assert.equal(renderRequest?.operation, "render");
assert.equal(renderRequest?.source, 'emit "tarball-smoke"\n');
runtime.dispose();

const browserTestPackage = process.env.MCL_BROWSER_TEST_PACKAGE;
if (browserTestPackage) {
  const result = spawnSync("npm", ["run", "--prefix", browserTestPackage, "test:browser:installed"], {
    cwd: projectRoot,
    env: { ...process.env, MCL_BROWSER_PACKAGE: browserPackageRoot },
    encoding: "utf8",
  });
  process.stdout.write(result.stdout);
  process.stderr.write(result.stderr);
  if (result.error) throw result.error;
  assert.equal(result.status, 0, "Installed Browser package failed its real Worker and WebAssembly browser smoke test");
}

console.log(`Verified installed MCL packages ${browserManifest.version}.`);

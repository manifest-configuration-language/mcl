import assert from "node:assert/strict";
import { readFile, stat } from "node:fs/promises";
import { join } from "node:path";
import { createMclRuntime, MclRuntimeError } from "@mcl/browser";
import { mclLanguageSupport, parser } from "@mcl/language";

const projectRoot = process.cwd();
const browserPackageRoot = join(projectRoot, "node_modules/@mcl/browser");
const languagePackageRoot = join(projectRoot, "node_modules/@mcl/language");
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
assert.ok((await stat(join(browserPackageRoot, "dist/mcl.wasm"))).size > 0);
assert.ok((await stat(join(browserPackageRoot, "dist/wasm_exec.js"))).size > 0);

console.log(`Verified installed MCL packages ${browserManifest.version}.`);

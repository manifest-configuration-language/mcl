import { spawnSync } from 'node:child_process';
import { readFile, stat } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const workspace = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const browserPackage = path.join(workspace, 'packages/browser');
const wasmPath = path.join(browserPackage, 'dist/mcl.wasm');
const wasmExecPath = path.join(browserPackage, 'dist/wasm_exec.js');

const build = spawnSync('node', ['./packages/browser/scripts/build-wasm.mjs'], {
  cwd: workspace,
  encoding: 'utf8',
});

if (build.status !== 0) {
  process.stderr.write(build.stderr || build.stdout || 'Browser WebAssembly build failed.\n');
  process.exit(build.status ?? 1);
}

const [wasm, wasmStats, wasmExecStats] = await Promise.all([
  readFile(wasmPath),
  stat(wasmPath),
  stat(wasmExecPath),
]);

if (wasmStats.size === 0 || wasmExecStats.size === 0) {
  throw new Error('The Browser WebAssembly build produced an empty runtime artifact.');
}

if (!wasm.subarray(0, 4).equals(Buffer.from([0, 0x61, 0x73, 0x6d]))) {
  throw new Error('The Browser WebAssembly artifact has an invalid module header.');
}

await WebAssembly.compile(wasm);
console.log(`Verified WebAssembly artifacts (${wasmStats.size} byte module, ${wasmExecStats.size} byte Go runtime).`);

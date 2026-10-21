import { cp, mkdir } from "node:fs/promises";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const execFileAsync = promisify(execFile);
const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repositoryRoot = resolve(packageRoot, "../..");
const outputDirectory = resolve(packageRoot, "dist");
const { stdout } = await execFileAsync("go", ["env", "GOROOT"], { cwd: repositoryRoot });
const goRoot = stdout.trim();
const wasmExec = resolve(goRoot, "lib/wasm/wasm_exec.js");

await mkdir(outputDirectory, { recursive: true });
await execFileAsync("go", ["build", "-trimpath", "-ldflags=-s -w", "-o", resolve(outputDirectory, "mcl.wasm"), "./cmd/mcl-wasm"], {
  cwd: repositoryRoot,
  env: { ...process.env, GOOS: "js", GOARCH: "wasm", CGO_ENABLED: "0" },
  maxBuffer: 16 * 1024 * 1024,
});
await cp(wasmExec, resolve(outputDirectory, "wasm_exec.js"));

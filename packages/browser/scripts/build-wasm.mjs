import { cp, mkdir, rename, rm, stat } from "node:fs/promises";
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
const rawWasm = resolve(outputDirectory, ".mcl-raw.wasm");
const optimizedWasm = resolve(outputDirectory, ".mcl-optimized.wasm");

await mkdir(outputDirectory, { recursive: true });
try {
  await execFileAsync("go", ["build", "-trimpath", "-ldflags=-s -w", "-o", rawWasm, "./cmd/mcl-wasm"], {
    cwd: repositoryRoot,
    env: { ...process.env, GOOS: "js", GOARCH: "wasm", CGO_ENABLED: "0" },
    maxBuffer: 16 * 1024 * 1024,
  });
  await execFileAsync(
    "wasm-opt",
    [rawWasm, "-Oz", "--enable-bulk-memory", "--enable-nontrapping-float-to-int", "-o", optimizedWasm],
    {
      cwd: repositoryRoot,
      maxBuffer: 16 * 1024 * 1024,
    },
  );

  const [rawSize, optimizedSize] = await Promise.all([stat(rawWasm), stat(optimizedWasm)]);
  const selected = optimizedSize.size < rawSize.size ? optimizedWasm : rawWasm;
  await rm(resolve(outputDirectory, "mcl.wasm"), { force: true });
  await rename(selected, resolve(outputDirectory, "mcl.wasm"));
  await cp(wasmExec, resolve(outputDirectory, "wasm_exec.js"));
  console.log(`mcl.wasm raw=${rawSize.size} optimized=${optimizedSize.size} selected=${Math.min(rawSize.size, optimizedSize.size)} bytes`);
} finally {
  await Promise.all([rm(rawWasm, { force: true }), rm(optimizedWasm, { force: true })]);
}

import assert from "node:assert/strict";
import { readFile, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { execFileSync } from "node:child_process";

const packages = [
  {
    name: "@manifest-configuration-language/browser",
    entrypoints: [
      "dist/index.js",
      "dist/index.d.ts",
      "dist/worker.js",
      "dist/protocol.d.ts",
      "dist/limits.js",
      "dist/mcl.wasm",
      "dist/wasm_exec.js",
    ],
  },
  {
    name: "@manifest-configuration-language/language",
    entrypoints: [
      "src/index.js",
      "src/index.d.ts",
      "src/parser.js",
      "src/mcl.tmLanguage.json",
      "reference/cli/mcl.md",
      "reference/stdlib-functions.md",
    ],
  },
];

const manifests = await Promise.all(
  packages.map(async ({ name, entrypoints }) => {
    const packageRoot = join(process.cwd(), "node_modules", name);
    const manifest = JSON.parse(await readFile(join(packageRoot, "package.json"), "utf8"));

    assert.equal(manifest.name, name);
    for (const entrypoint of entrypoints) {
      const artifact = await stat(join(packageRoot, entrypoint));
      assert.ok(artifact.isFile() && artifact.size > 0, `${name} is missing ${entrypoint}`);
    }

    return manifest;
  }),
);

assert.equal(manifests[0].version, manifests[1].version);
if (process.env.MCL_VERSION) assert.equal(manifests[0].version, process.env.MCL_VERSION);

await writeFile(
  join(process.cwd(), "browser-consumer.ts"),
  `
import { createMclRuntime } from '@manifest-configuration-language/browser';

const runtime = createMclRuntime();
const project = runtime.openProject({ 'main.mcl': 'emit 1' });
const rendered = await project.run('render');
rendered.yaml satisfies string;
const completion = await project.run('completion', { position: { line: 0, character: 0 } });
completion.items satisfies unknown[];
// @ts-expect-error Completion requires a source position.
project.run('completion');
runtime.dispose();
`,
);
execFileSync(
  process.execPath,
  [
    join(process.cwd(), "node_modules/typescript/bin/tsc"),
    "--noEmit",
    "--strict",
    "--skipLibCheck",
    "--target",
    "ES2022",
    "--module",
    "NodeNext",
    "--moduleResolution",
    "NodeNext",
    "browser-consumer.ts",
  ],
  { stdio: "inherit" },
);

console.log(`Verified installed MCL packages ${manifests[0].version}.`);

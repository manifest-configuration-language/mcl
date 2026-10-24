import assert from 'node:assert/strict';
import { readFile, stat } from 'node:fs/promises';
import { join } from 'node:path';

const packages = [
  {
    name: '@manifest-configuration-language/browser',
    entrypoints: ['src/index.js', 'src/index.d.ts', 'dist/mcl.wasm', 'dist/wasm_exec.js'],
  },
  {
    name: '@manifest-configuration-language/language',
    entrypoints: ['src/index.js', 'src/index.d.ts', 'src/parser.js', 'src/mcl.tmLanguage.json'],
  },
  {
    name: '@manifest-configuration-language/reference',
    entrypoints: ['reference/cli/mcl.md', 'reference/stdlib-functions.md'],
  },
];

const manifests = await Promise.all(packages.map(async ({ name, entrypoints }) => {
  const packageRoot = join(process.cwd(), 'node_modules', name);
  const manifest = JSON.parse(await readFile(join(packageRoot, 'package.json'), 'utf8'));

  assert.equal(manifest.name, name);
  for (const entrypoint of entrypoints) {
    const artifact = await stat(join(packageRoot, entrypoint));
    assert.ok(artifact.isFile() && artifact.size > 0, `${name} is missing ${entrypoint}`);
  }

  return manifest;
}));

assert.equal(manifests[0].version, manifests[1].version);
assert.equal(manifests[0].version, manifests[2].version);
if (process.env.MCL_VERSION) assert.equal(manifests[0].version, process.env.MCL_VERSION);

console.log(`Verified installed MCL packages ${manifests[0].version}.`);

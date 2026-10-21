import { readFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { parser } from "../src/parser.js";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repositoryRoot = resolve(packageRoot, "../..");
const grammar = await readFile(resolve(packageRoot, "src/mcl.grammar"), "utf8");
const textMate = JSON.parse(await readFile(resolve(packageRoot, "src/mcl.tmLanguage.json"), "utf8"));
const lezerKeywords = new Set([...grammar.matchAll(/kw<"([^\"]+)">/g)].map((match) => match[1]));
const textMateKeywords = new Set();
for (const group of ["keywords", "constants", "types"]) {
  for (const pattern of textMate.repository[group].patterns) {
    const alternatives = pattern.match.match(/\(\?:([^)]*)\)/)?.[1] ?? "";
    for (const word of alternatives.split("|")) if (word) textMateKeywords.add(word);
  }
}
const missingInTextMate = [...lezerKeywords].filter((word) => !textMateKeywords.has(word));
const missingInLezer = [...textMateKeywords].filter((word) => !lezerKeywords.has(word));
if (missingInTextMate.length || missingInLezer.length) {
  throw new Error(`Keyword mismatch: missing in TextMate [${missingInTextMate}], missing in Lezer [${missingInLezer}]`);
}

function hasSyntaxError(tree) {
  let hasError = false;
  tree.iterate({ enter(node) { if (node.type.isError) hasError = true; } });
  return hasError;
}

const validFixtures = [
  resolve(packageRoot, "parity-fixtures/basic.mcl"),
  resolve(packageRoot, "parity-fixtures/types.mcl"),
  resolve(packageRoot, "parity-fixtures/blocks.mcl"),
  resolve(packageRoot, "parity-fixtures/without.mcl"),
  resolve(packageRoot, "parity-fixtures/descendants-validation.mcl"),
  resolve(repositoryRoot, "cmd/mcl-wasm/testdata/examples/validation/rules/main.mcl"),
  resolve(repositoryRoot, "cmd/mcl-wasm/testdata/examples/references/clone-node/main.mcl"),
];

for (const fixturePath of validFixtures) {
  const fixture = fixturePath.slice(repositoryRoot.length + 1);
  const source = await readFile(fixturePath, "utf8");
  const tree = parser.parse(source);
  if (hasSyntaxError(tree)) throw new Error(`Lezer parser rejected valid fixture ${fixture}`);
  const go = spawnSync("go", ["run", "./cmd/mcl", "fmt", fixturePath], { cwd: repositoryRoot, encoding: "utf8" });
  if (go.status !== 0) throw new Error(`Go parser rejected fixture ${fixture}: ${go.stderr}`);
}

const invalidFixture = "invalid.mcl";
const invalidPath = resolve(packageRoot, "parity-fixtures", invalidFixture);
const invalidSource = await readFile(invalidPath, "utf8");
if (!hasSyntaxError(parser.parse(invalidSource))) {
  throw new Error(`Lezer parser accepted invalid fixture ${invalidFixture}`);
}
const go = spawnSync("go", ["run", "./cmd/mcl", "fmt", invalidPath], { cwd: repositoryRoot, encoding: "utf8" });
if (go.error || go.status === null) throw new Error(`Could not run Go parser for ${invalidFixture}: ${go.error}`);
if (go.status === 0) throw new Error(`Go parser accepted invalid fixture ${invalidFixture}`);

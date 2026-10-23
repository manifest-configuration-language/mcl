import { mkdtemp, readdir, readFile, rm } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
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
  resolve(packageRoot, "parity-fixtures/advanced-constructs.mcl"),
  resolve(packageRoot, "parity-fixtures/multiline-string-escaped-delimiter.mcl"),
  resolve(packageRoot, "parity-fixtures/block-expressions.mcl"),
  resolve(packageRoot, "parity-fixtures/keyword-identifier.mcl"),
  resolve(packageRoot, "parity-fixtures/numbers.mcl"),
];

async function findMclFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const nested = await Promise.all(entries.map((entry) => {
    const path = resolve(directory, entry.name);
    return entry.isDirectory() ? findMclFiles(path) : entry.name.endsWith(".mcl") ? [path] : [];
  }));
  return nested.flat();
}

validFixtures.push(...await findMclFiles(resolve(repositoryRoot, "cmd/mcl-wasm/testdata/examples")));

const temporaryDirectory = await mkdtemp(join(tmpdir(), "mcl-language-parity-"));
const mclBinary = join(temporaryDirectory, process.platform === "win32" ? "mcl.exe" : "mcl");
try {
  const build = spawnSync("go", ["build", "-o", mclBinary, "./cmd/mcl"], { cwd: repositoryRoot, encoding: "utf8" });
  if (build.error || build.status !== 0) {
    throw new Error(`Could not build the Go parser: ${build.error ?? build.stderr}`);
  }
  for (const fixturePath of validFixtures) {
    const fixture = fixturePath.slice(repositoryRoot.length + 1);
    const source = await readFile(fixturePath, "utf8");
    const tree = parser.parse(source);
    if (hasSyntaxError(tree)) throw new Error(`Lezer parser rejected valid fixture ${fixture}`);
    const go = spawnSync(mclBinary, ["fmt", fixturePath], { cwd: repositoryRoot, encoding: "utf8" });
    if (go.error || go.status !== 0) throw new Error(`Go parser rejected fixture ${fixture}: ${go.error ?? go.stderr}`);
  }

  for (const invalidFixture of ["invalid.mcl", "invalid-unsupported-operator.mcl"]) {
    const invalidPath = resolve(packageRoot, "parity-fixtures", invalidFixture);
    const invalidSource = await readFile(invalidPath, "utf8");
    if (!hasSyntaxError(parser.parse(invalidSource))) {
      throw new Error(`Lezer parser accepted invalid fixture ${invalidFixture}`);
    }
    const go = spawnSync(mclBinary, ["fmt", invalidPath], { cwd: repositoryRoot, encoding: "utf8" });
    if (go.error || go.status === null) throw new Error(`Could not run Go parser for ${invalidFixture}: ${go.error}`);
    if (go.status === 0) throw new Error(`Go parser accepted invalid fixture ${invalidFixture}`);
  }
} finally {
  await rm(temporaryDirectory, { recursive: true, force: true });
}

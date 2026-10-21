import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import textmate from "vscode-textmate";
import oniguruma from "vscode-oniguruma";

const { Registry } = textmate;
const { loadWASM, OnigScanner, OnigString } = oniguruma;

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const wasmBuffer = await readFile(resolve(packageRoot, "node_modules/vscode-oniguruma/release/onig.wasm"));
await loadWASM(wasmBuffer.buffer.slice(wasmBuffer.byteOffset, wasmBuffer.byteOffset + wasmBuffer.byteLength));

const grammarSource = await readFile(resolve(packageRoot, "src/mcl.tmLanguage.json"), "utf8");
const grammar = JSON.parse(grammarSource);
const registry = new Registry({
  onigLib: Promise.resolve({
    createOnigScanner: (patterns) => new OnigScanner(patterns),
    createOnigString: (value) => new OnigString(value),
  }),
  loadGrammar: async (scopeName) => scopeName === "source.mcl" ? grammar : null,
});
const textmateGrammar = await registry.loadGrammar("source.mcl");
assert.ok(textmateGrammar, "TextMate grammar must load");

function tokenize(lines) {
  let ruleStack = null;
  return lines.map((line) => {
    const result = textmateGrammar.tokenizeLine(line, ruleStack);
    ruleStack = result.ruleStack;
    return result.tokens.map((token) => ({
      text: line.slice(token.startIndex, token.endIndex),
      scopes: token.scopes,
    }));
  });
}

function assertToken(tokens, text, scope) {
  const token = tokens.find((candidate) => candidate.text === text && candidate.scopes.includes(scope));
  assert.ok(token, `Expected ${JSON.stringify(text)} to have TextMate scope ${scope}; got ${JSON.stringify(tokens)}`);
}

const lines = tokenize([
  "# Keep this comment",
  'let greeting = "hello \\\"MCL\\\""',
  "if greeting != \"\" and key(greeting) { emit output }",
  "filter(greeting)",
  'let message = """',
  "multiline text",
  '"""; emit message',
]);

assertToken(lines[0], "# Keep this comment", "comment.line.number-sign.mcl");
assertToken(lines[1], "let", "keyword.control.mcl");
assertToken(lines[1], "greeting", "variable.other.mcl");
assertToken(lines[1], "=", "keyword.operator.mcl");
assertToken(lines[1], "hello ", "string.quoted.double.mcl");
assertToken(lines[1], '\\"', "constant.character.escape.mcl");
assertToken(lines[2], "if", "keyword.control.mcl");
assertToken(lines[2], "greeting", "variable.other.mcl");
assertToken(lines[2], "!=", "keyword.operator.mcl");
assertToken(lines[2], "and", "keyword.operator.mcl");
assertToken(lines[2], "key", "keyword.control.mcl");
assertToken(lines[2], "emit", "keyword.control.mcl");
assertToken(lines[2], "output", "constant.language.mcl");
assertToken(lines[3], "filter", "entity.name.function.mcl");
assertToken(lines[5], "multiline text", "string.quoted.multi.double.mcl");
assertToken(lines[6], "emit", "keyword.control.mcl");

registry.dispose();

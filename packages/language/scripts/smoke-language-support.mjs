import assert from "node:assert/strict";
import { EditorState } from "@codemirror/state";
import { defaultHighlightStyle, foldable, getIndentation, syntaxTree } from "@codemirror/language";
import { highlightTree } from "@lezer/highlight";
import { mclLanguageSupport } from "../src/index.js";

const document = `# block expressions remain part of the language tree
let value =
  if true:
    name: "value"
  else:
    name: "fallback"
emit value
`;
const state = EditorState.create({ doc: document, extensions: [mclLanguageSupport()] });
const tree = syntaxTree(state);
const nodeNames = new Set();
tree.iterate({
  enter(node) {
    assert.equal(node.type.isError, false, `unexpected syntax error at ${node.from}`);
    nodeNames.add(node.name);
  },
});

assert.ok(nodeNames.has("IfExpression"), "the parser should recognize block-level if/else expressions");
assert.equal(state.languageDataAt("commentTokens", 0)[0]?.line, "#");

const highlightedText = new Set();
highlightTree(tree, defaultHighlightStyle, (from, to) => highlightedText.add(document.slice(from, to)));
for (const token of ["let", "if", "true", '"value"']) {
  assert.ok(highlightedText.has(token), `CodeMirror should highlight ${token}`);
}

const expectedIndentation = [0, 0, 2, 4, 2, 4, 0];
for (let number = 1; number <= expectedIndentation.length; number++) {
  const line = state.doc.line(number);
  assert.equal(getIndentation(state, line.from), expectedIndentation[number - 1], `indentation for line ${number}`);
}

for (const number of [3, 5]) {
  const line = state.doc.line(number);
  const range = foldable(state, line.from, line.to);
  assert.ok(range, `line ${number} should fold its indented block`);
  assert.ok(range.to > line.to, `line ${number} folding range should include its block`);
}

const scalarLine = state.doc.line(4);
assert.equal(foldable(state, scalarLine.from, scalarLine.to), null, "scalar map entries should not fold");

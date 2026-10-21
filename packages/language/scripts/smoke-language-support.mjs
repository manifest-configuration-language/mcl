import { EditorState } from "@codemirror/state";
import { mclLanguageSupport } from "../src/index.js";

const support = mclLanguageSupport();
const state = EditorState.create({
  doc: 'let config = {name: "mcl"}',
  extensions: [support],
});

if (state.doc.length === 0) {
  throw new Error("CodeMirror did not create an editor state with MCL language support");
}

import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { createMclRuntime } from "@mcl/browser";
import { mclLanguageSupport, parser } from "@mcl/language";
import grammar from "@mcl/language/textmate" with { type: "json" };

const output = document.querySelector("#result");
const source = `let configMap =
  apiVersion: "v1"
  kind: "ConfigMap"
  metadata:
    name: "mcl-package-consumer-smoke"

emit configMap
`;

async function run() {
  if (grammar.scopeName !== "source.mcl" || !grammar.patterns?.length) {
    throw new Error("The installed TextMate grammar export is invalid.");
  }
  if (parser.parse(source).length < 2) {
    throw new Error("The installed Lezer parser did not parse the MCL source.");
  }

  const state = EditorState.create({ doc: source, extensions: [mclLanguageSupport()] });
  new EditorView({ state, parent: document.querySelector("#editor") });

  const runtime = createMclRuntime({
    initializationTimeoutMs: 60_000,
    operationTimeoutMs: 60_000,
  });
  try {
    const project = runtime.openProject({ "main.mcl": source });
    const result = await project.render();
    if (!result.yaml.includes('name: "mcl-package-consumer-smoke"')) {
      throw new Error(`Unexpected Browser SDK result: ${result.yaml}`);
    }
    output.dataset.status = "passed";
    output.textContent = JSON.stringify({
      yaml: result.yaml,
      grammarScope: grammar.scopeName,
      editorDocument: state.doc.toString(),
    });
  } finally {
    runtime.dispose();
  }
}

run().catch((error) => {
  output.dataset.status = "failed";
  output.textContent = error instanceof Error ? error.stack ?? error.message : String(error);
});

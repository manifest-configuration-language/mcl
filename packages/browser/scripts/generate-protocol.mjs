import { readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { compileFromFile } from "json-schema-to-typescript";

const schema = fileURLToPath(new URL("../../../cmd/mcl-wasm/protocol.schema.json", import.meta.url));
const output = fileURLToPath(new URL("../src/protocol.d.ts", import.meta.url));
const goLimitsOutput = fileURLToPath(new URL("../../../internal/wasmprotocol/limits.go", import.meta.url));
const jsLimitsOutput = fileURLToPath(new URL("../src/limits.js", import.meta.url));
const schemaDocument = JSON.parse(await readFile(schema, "utf8"));
const limits = schemaDocument["x-mcl-limits"];
const requestFileLimit = schemaDocument.$defs.request.properties.files.maxProperties;
if (requestFileLimit !== limits.maxProjectFiles - 1) {
  throw new Error("The request file count must exclude the entrypoint from maxProjectFiles.");
}

for (const [name, value] of Object.entries(limits)) {
  if (!Number.isSafeInteger(value) || value <= 0) {
    throw new Error(`Protocol limit ${name} must be a positive safe integer.`);
  }
}

const declarations = await compileFromFile(schema, {
  bannerComment: "/* Generated from cmd/mcl-wasm/protocol.schema.json. Do not edit. */",
  style: { singleQuote: false },
});
const goLimitNames = [
  ["MaxSourceBytes", limits.maxSourceBytes],
  ["MaxProjectFiles", limits.maxProjectFiles],
  ["MaxProjectFileBytes", limits.maxProjectFileBytes],
  ["MaxProjectBytes", limits.maxProjectBytes],
  ["MaxOutputBytes", limits.maxOutputBytes],
  ["EvaluationTimeoutMillis", limits.evaluationTimeoutMillis],
];
const goLimitWidth = Math.max(...goLimitNames.map(([name]) => name.length));
const goLimits = goLimitNames.map(([name, value]) => `\t${name.padEnd(goLimitWidth)} = ${value}`).join("\n");

await writeFile(output, declarations);
await writeFile(
  goLimitsOutput,
  `// Code generated from cmd/mcl-wasm/protocol.schema.json. DO NOT EDIT.\n\npackage wasmprotocol\n\nconst (\n${goLimits}\n)\n`,
);
await writeFile(
  jsLimitsOutput,
  `// Generated from cmd/mcl-wasm/protocol.schema.json. Do not edit.\nexport const maxSourceBytes = ${limits.maxSourceBytes};\nexport const maxProjectFiles = ${limits.maxProjectFiles};\nexport const maxProjectFileBytes = ${limits.maxProjectFileBytes};\nexport const maxProjectBytes = ${limits.maxProjectBytes};\nexport const maxOutputBytes = ${limits.maxOutputBytes};\nexport const evaluationTimeoutMillis = ${limits.evaluationTimeoutMillis};\n`,
);

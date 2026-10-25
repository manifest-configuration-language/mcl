import { readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { compileFromFile } from "json-schema-to-typescript";

const schema = fileURLToPath(new URL("../../../cmd/mcl-wasm/protocol.schema.json", import.meta.url));
const output = fileURLToPath(new URL("../src/protocol.d.ts", import.meta.url));
const goLimitsOutput = fileURLToPath(new URL("../../../internal/wasmprotocol/limits.go", import.meta.url));
const jsLimitsOutput = fileURLToPath(new URL("../src/limits.js", import.meta.url));
const schemaDocument = JSON.parse(await readFile(schema, "utf8"));
const limits = schemaDocument["x-mcl-limits"];
const requestOperations = schemaDocument.$defs.request.properties.operation.enum;
const requestFileLimit = schemaDocument.$defs.request.properties.files.maxProperties;
const renameNameLength = schemaDocument.$defs.request.properties.newName.maxLength;
const projectPathConstraints = schemaDocument.$defs.projectPath.allOf;
const projectPathLengthConstraint = projectPathConstraints.find((constraint) => constraint.maxLength !== undefined);
const projectPathDepthConstraint = projectPathConstraints.find((constraint) => constraint.pattern !== undefined);
if (requestFileLimit !== limits.maxProjectFiles - 1) {
  throw new Error("The request file count must exclude the entrypoint from maxProjectFiles.");
}
if (
  !Array.isArray(requestOperations) ||
  requestOperations.length === 0 ||
  requestOperations.some((operation) => typeof operation !== "string" || operation.length === 0)
) {
  throw new Error("The request operation enum must contain non-empty strings.");
}
if (renameNameLength !== limits.maxRenameNameLength) {
  throw new Error("The rename name maxLength must match maxRenameNameLength.");
}
if (projectPathLengthConstraint?.maxLength !== limits.maxProjectPathLength) {
  throw new Error("The project path maxLength must match maxProjectPathLength.");
}
if (projectPathDepthConstraint?.pattern !== `^([^/]+/){0,${limits.maxProjectPathDepth - 1}}[^/]+$`) {
  throw new Error("The project path depth pattern must match maxProjectPathDepth.");
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
  ["MaxSourceMapBytes", limits.maxSourceMapBytes],
  ["MaxRenameNameLength", limits.maxRenameNameLength],
  ["EvaluationTimeoutMillis", limits.evaluationTimeoutMillis],
  ["MaxProjectPathLength", limits.maxProjectPathLength],
  ["MaxProjectPathDepth", limits.maxProjectPathDepth],
];
const goOperationNames = requestOperations
  .map((operation, index) => {
    const separator = index < requestOperations.length - 1 ? ", " : "";
    const indentation = index === 0 ? "" : "\t";
    return `${indentation}${JSON.stringify(operation + separator)}`;
  })
  .join(" +\n");
const goLimitWidth = Math.max(...goLimitNames.map(([name]) => name.length));
const goLimits = goLimitNames.map(([name, value]) => `\t${name.padEnd(goLimitWidth)} = ${value}`).join("\n");

await writeFile(output, declarations);
await writeFile(
  goLimitsOutput,
  `// Code generated from cmd/mcl-wasm/protocol.schema.json. DO NOT EDIT.\n\npackage wasmprotocol\n\nconst RequestOperationNames = ${goOperationNames}\n\nconst (\n${goLimits}\n)\n`,
);
await writeFile(
  jsLimitsOutput,
  `// Generated from cmd/mcl-wasm/protocol.schema.json. Do not edit.\nexport const maxSourceBytes = ${limits.maxSourceBytes};\nexport const maxProjectFiles = ${limits.maxProjectFiles};\nexport const maxProjectFileBytes = ${limits.maxProjectFileBytes};\nexport const maxProjectBytes = ${limits.maxProjectBytes};\nexport const maxOutputBytes = ${limits.maxOutputBytes};\nexport const maxSourceMapBytes = ${limits.maxSourceMapBytes};\nexport const maxRenameNameLength = ${limits.maxRenameNameLength};\nexport const evaluationTimeoutMillis = ${limits.evaluationTimeoutMillis};\nexport const maxProjectPathLength = ${limits.maxProjectPathLength};\nexport const maxProjectPathDepth = ${limits.maxProjectPathDepth};\n`,
);

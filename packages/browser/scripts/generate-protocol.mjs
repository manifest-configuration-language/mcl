import { writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { compileFromFile } from "json-schema-to-typescript";

const schema = fileURLToPath(new URL("../../../cmd/mcl-wasm/protocol.schema.json", import.meta.url));
const output = fileURLToPath(new URL("../src/protocol.d.ts", import.meta.url));
const declarations = await compileFromFile(schema, {
  bannerComment: "/* Generated from cmd/mcl-wasm/protocol.schema.json. Do not edit. */",
  style: { singleQuote: false },
});

await writeFile(output, declarations);

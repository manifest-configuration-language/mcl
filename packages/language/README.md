# @manifest-configuration-language/language

`@manifest-configuration-language/language` contains the MCL TextMate grammar and a Lezer-backed CodeMirror `LanguageSupport` export with standard bracket completion.

The matching MCL release's CLI and standard-library reference Markdown is included under `reference/`.

```js
import { mclLanguageSupport } from "@manifest-configuration-language/language";
import mclTextMateGrammar from "@manifest-configuration-language/language/textmate" with { type: "json" };

const support = mclLanguageSupport();
```

The Lezer parser provides incremental syntax trees and CodeMirror highlighting for declarations, generic and schema-backed types, expressions, indentation-sensitive map/list blocks, block-level `if`/`else` and `for` expressions, dynamic `key(...)` map keys, node declarations, `with`/`without` overlays, typed functions, and validation rules. Descendant validation supports a type selector, `where = fn(...) => ...`, and `select = bySchema(Type)`; each selector may include the optional integer `minMatches` argument. The grammar recognizes source structure and does not validate evaluation semantics.

`npm run check:parity` checks that the generated TextMate keyword and operator patterns match the Lezer grammar, tokenizes representative MCL constructs with the VS Code TextMate tokenizer and checks their scopes, parses package-owned fixtures and every `.mcl` example under `cmd/mcl-wasm/testdata/examples` with both Lezer and the Go formatter, and verifies one malformed declaration is rejected by both parsers. The tokenization check covers comments, strings, keywords, identifiers, function calls, and operators. The fixtures cover descendant selector variants, dynamic map keys, schema-backed types, qualified rule references, nested application manifests, and final `if`/`else` expressions. The parity check uses the Go CLI as the syntax reference. Run `pnpm run generate:textmate` after changing Lezer keyword or operator declarations; `src/mcl.tmLanguage.json` is committed generated output.

Build generation runs during `prepack` with the pinned `@lezer/generator` dependency. CodeMirror packages, including `@codemirror/autocomplete` for bracket completion, are peers so applications can share their existing editor runtime. The parity check also constructs the support in a Node `EditorState` to catch package integration errors.

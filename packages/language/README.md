# @mcl/language

`@mcl/language` contains the MCL TextMate grammar and a Lezer-backed CodeMirror `LanguageSupport` export.

```js
import { mclLanguageSupport } from "@mcl/language";
import mclTextMateGrammar from "@mcl/language/textmate" with { type: "json" };

const support = mclLanguageSupport();
```

The Lezer parser supplies incremental token parsing and highlighting. It does not replace the Go parser or validate MCL syntax or evaluation semantics. Indentation-sensitive blocks, declaration and expression structure, and advanced rule/query constructs still need a full grammar and parity checks against the Go parser. `npm run check:parity` currently checks keyword coverage in Lezer and TextMate and parses two valid fixtures with both Lezer and the Go formatter. It does not check invalid-input diagnostics. `src/mcl.tmLanguage.json` is the package-owned TextMate grammar source; sibling VS Code and Website copies must be migrated to this package before the ecosystem has a single grammar source.

Build generation runs during `prepack` with the pinned `@lezer/generator` dependency. CodeMirror packages are peers so applications can share their existing editor runtime.

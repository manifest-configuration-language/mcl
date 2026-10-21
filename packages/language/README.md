# @mcl/language

`@mcl/language` contains the MCL TextMate grammar and a Lezer-backed CodeMirror `LanguageSupport` export.

```js
import { mclLanguageSupport } from "@mcl/language";
import mclTextMateGrammar from "@mcl/language/textmate" with { type: "json" };

const support = mclLanguageSupport();
```

The Lezer parser provides incremental syntax trees and CodeMirror highlighting for declarations, types, expressions, indentation-sensitive map/list blocks, node declarations, clone overlays, and validation rules. It is an editor grammar and does not validate evaluation semantics. Query selectors and several advanced constructs still need grammar coverage and parity checks. `npm run check:parity` checks that TextMate and Lezer expose the same keywords, parses representative valid files with Lezer and the Go formatter, and verifies one malformed declaration is rejected by both parsers. The parity check uses the Go CLI as the syntax reference. `src/mcl.tmLanguage.json` is the package-owned TextMate grammar source; sibling VS Code and Website copies still need migration to this package.

Build generation runs during `prepack` with the pinned `@lezer/generator` dependency. CodeMirror packages are peers so applications can share their existing editor runtime.

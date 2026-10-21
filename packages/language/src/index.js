import { LanguageSupport, LRLanguage, continuedIndent, foldInside, foldNodeProp, indentNodeProp } from "@codemirror/language";
import { closeBrackets } from "@codemirror/autocomplete";
import { styleTags, tags } from "@lezer/highlight";
import { parser as generatedParser } from "./parser.js";

export const parser = generatedParser.configure({
  props: [
    styleTags({
      "import as export type let fn rule emit validate require message using for in if else each descendants where select bySchema minMatches requireEach requireDescendants and or not is with without output schema key": tags.keyword,
      "Any Null Bool Int Decimal String List Map Function Node Schema": tags.typeName,
      "true false null": tags.bool,
      Identifier: tags.variableName,
      QuotedString: tags.string,
      Number: tags.number,
      Comment: tags.comment,
      Operator: tags.operator,
    }),
    indentNodeProp.add({ MCL: continuedIndent() }),
    foldNodeProp.add({ MCL: foldInside }),
  ],
});

const language = LRLanguage.define({
  name: "mcl",
  parser,
  languageData: { commentTokens: { line: "#" } },
});

export function mclLanguageSupport() {
  return new LanguageSupport(language, closeBrackets());
}

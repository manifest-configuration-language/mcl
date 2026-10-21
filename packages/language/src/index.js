import { LanguageSupport, LRLanguage, continuedIndent, foldInside, foldNodeProp, indentNodeProp } from "@codemirror/language";
import { styleTags, tags } from "@lezer/highlight";
import { parser as generatedParser } from "./parser.js";

export const parser = generatedParser.configure({
  props: [
    styleTags({
      Keyword: tags.keyword,
      "Any Null Bool Int Decimal String List Map Function Node Schema": tags.typeName,
      "true false null": tags.bool,
      Identifier: tags.variableName,
      QuotedString: tags.string,
      Number: tags.number,
      Comment: tags.comment,
      Operator: tags.operator,
      Punctuation: tags.punctuation,
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
  return new LanguageSupport(language);
}

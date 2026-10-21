import { LanguageSupport, LRLanguage, continuedIndent, foldService, indentNodeProp, syntaxTree } from "@codemirror/language";
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
    indentNodeProp.add({
      LetBlockDeclaration: continuedIndent(),
      Block: continuedIndent({ except: /^\s*else\b/ }),
      IfExpression: continuedIndent({ except: /^\s*else\b/ }),
      ForExpression: continuedIndent(),
      TypeRecordBlock: continuedIndent(),
      RuleBlock: continuedIndent(),
    }),
  ],
});

const language = LRLanguage.define({
  name: "mcl",
  parser,
  languageData: { commentTokens: { line: "#" } },
});

export function mclLanguageSupport() {
  return new LanguageSupport(language, [closeBrackets(), foldService.of(foldMclBlock)]);
}

function foldMclBlock(state, lineStart, lineEnd) {
  const tree = syntaxTree(state);
  const text = state.doc.sliceString(lineStart, lineEnd).trim();
  const isElseHeader = /^else\s*:/.test(text);
  for (let node = tree.resolveInner(lineEnd, -1); node; node = node.parent) {
    if (isElseHeader ? node.name !== "IfExpression" : node.from < lineStart || node.from > lineEnd) continue;

    let candidate;
    for (let child = node.firstChild; child; child = child.nextSibling) {
      if (child.name === "Block" && child.from >= lineEnd) {
        candidate = child;
        if (!isElseHeader) break;
      }
    }
    if (candidate && candidate.to > lineEnd) return { from: lineEnd, to: candidate.to };
    if (isElseHeader) return null;
  }
  return null;
}

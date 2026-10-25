import { readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

// TextMate keyword scopes follow Lezer terminals; parser acceptance is compared with Go separately.
const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const grammar = await readFile(resolve(packageRoot, "src/mcl.grammar"), "utf8");
const textMatePath = resolve(packageRoot, "src/mcl.tmLanguage.json");
const textMate = JSON.parse(await readFile(textMatePath, "utf8"));

function ruleBody(name) {
  const match = grammar.match(new RegExp(`(?:^|\\n)${name}\\s*\\{([^}]*)\\}`));
  if (!match) throw new Error(`Could not find Lezer rule ${name}`);
  return match[1];
}

function keywordsIn(value) {
  return [...value.matchAll(/kw<"([^"]+)">/g)].map((match) => match[1]);
}

function uniqueSorted(values) {
  return [...new Set(values)].sort();
}

function wordPattern(words) {
  return `\\b(?:${uniqueSorted(words).join("|")})\\b`;
}

const allKeywords = uniqueSorted(keywordsIn(grammar));
const typeKeywords = uniqueSorted(keywordsIn(ruleBody("PrimitiveType")));
const constantKeywords = uniqueSorted([
  ...keywordsIn(ruleBody("PrimaryExpression")),
  ...keywordsIn(ruleBody("DescendantsQuery")).filter((keyword) => keyword === "output"),
]);
const operatorKeywords = uniqueSorted([
  ...keywordsIn(ruleBody("UnaryExpression")),
  ...keywordsIn(ruleBody("BinaryOperator")),
]);
const excluded = new Set([...typeKeywords, ...constantKeywords, ...operatorKeywords]);
const controlKeywords = allKeywords.filter((keyword) => !excluded.has(keyword));

function tokenValues(name) {
  const tokens = grammar.match(/@tokens\s*\{([\s\S]*?)^\}/m)?.[1];
  if (!tokens) throw new Error("Could not find Lezer token declarations");
  const declaration = tokens.match(new RegExp(`(?:^|\\n)\\s*${name}\\s*\\{([^}]*)\\}`));
  if (!declaration) throw new Error(`Could not find Lezer token ${name}`);
  return [...declaration[1].matchAll(/("(?:\\.|[^"\\])*")/g)].map((match) => JSON.parse(match[1]));
}

function escapeRegex(value) {
  return [...value].map((character) =>
    "*+?^$|\\()[]{}.".includes(character) ? "\\" + character : character
  ).join("");
}

// Assignment uses the literal "=" token instead of the Lezer Operator token.
const operators = uniqueSorted([...tokenValues("Operator"), ...tokenValues("Minus"), "="])
  .sort((left, right) => right.length - left.length || left.localeCompare(right));
const operatorPattern = `(?:${operators.map(escapeRegex).join("|")})`;

textMate.repository.keywords.patterns[0].match = wordPattern(controlKeywords);
textMate.repository.keywords.patterns[1].match = wordPattern(operatorKeywords);
textMate.repository.constants.patterns[0].match = wordPattern(constantKeywords);
textMate.repository.types.patterns[0].match = wordPattern(typeKeywords);
textMate.repository.operators.patterns[0].match = operatorPattern;

const generated = `${JSON.stringify(textMate, null, 2)}\n`;
if (process.argv.includes("--check")) {
  const current = await readFile(textMatePath, "utf8");
  if (current !== generated) throw new Error("TextMate grammar is stale; run pnpm run generate:textmate");
} else {
  await writeFile(textMatePath, generated);
}


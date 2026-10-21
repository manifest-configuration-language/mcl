import { ContextTracker, ExternalTokenizer } from "@lezer/lr";
import { blankLineStart, dedent, indent } from "./parser.terms.js";

class IndentLevel {
  constructor(parent, depth) {
    this.parent = parent;
    this.depth = depth;
    this.hash = (parent ? (parent.hash + parent.hash) << 8 : 0) + depth + (depth << 4);
  }
}

export const trackIndent = new ContextTracker({
  start: new IndentLevel(null, 0),
  shift(context, term, stack, input) {
    if (term === indent) return new IndentLevel(context, stack.pos - input.pos);
    if (term === dedent) return context.parent;
    return context;
  },
  hash: (context) => context.hash,
});

const newline = 10;
const carriageReturn = 13;
const space = 32;
const tab = 9;
const hash = 35;

export const indentation = new ExternalTokenizer((input, stack) => {
  const previous = input.peek(-1);
  if (previous !== -1 && previous !== newline && previous !== carriageReturn) return;

  let spaces = 0;
  while (input.next === space || input.next === tab) {
    input.advance();
    spaces += 1;
  }

  if ((input.next === newline || input.next === carriageReturn || input.next === hash) && stack.canShift(blankLineStart)) {
    input.acceptToken(blankLineStart, -spaces);
  } else if (spaces > stack.context.depth) {
    input.acceptToken(indent);
  } else if (spaces < stack.context.depth) {
    input.acceptToken(dedent, -spaces);
  }
}, { contextual: true });

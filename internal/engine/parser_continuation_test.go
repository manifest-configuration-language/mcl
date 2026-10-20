package engine

import (
	"strings"
	"testing"

	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

func TestParseMultilineCallUsesAbsoluteSourceSpans(t *testing.T) {
	t.Parallel()

	source := "let value = combine(\n  \"first\",\n  nested(\"second\"),\n)\n"
	module, err := ParseAST(source)
	if err != nil {
		t.Fatal(err)
	}
	declaration, ok := module.Nodes[0].(*LetDeclaration)
	if !ok || declaration.Expression == nil {
		t.Fatalf("first node = %#v, want a let declaration with an expression", module.Nodes[0])
	}
	expression, ok := declaration.Expression.(*CallExpr)
	if !ok {
		t.Fatalf("expression = %#v, want a call", declaration.Expression)
	}
	start := strings.Index(source, "combine(")
	end := strings.LastIndex(source, ")\n") + 1
	if expression.ExprSpan() != (SourceSpan{Start: start, End: end}) {
		t.Fatalf("call expression = %#v, want source span [%d, %d)", expression, start, end)
	}
	if span := expression.ExprSpan(); source[span.Start:span.End] != "combine(\n  \"first\",\n  nested(\"second\"),\n)" {
		t.Fatalf("call source = %q, want the complete multiline call", source[span.Start:span.End])
	}
}

func TestParseMultilineCallKeepsOpeningLineForUnclosedDiagnostic(t *testing.T) {
	t.Parallel()

	_, err := ParseAST("let value = combine(\n  \"first\", # ignored\n")
	if err == nil || !strings.Contains(err.Error(), "line 1: unterminated function call") {
		t.Fatalf("ParseAST() error = %v, want an unterminated call diagnostic on line 1", err)
	}
}

func TestParseMultilineCallKeepsCommentOutsideArgumentSpans(t *testing.T) {
	t.Parallel()

	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(map[string]string{"\n": "LF", "\r\n": "CRLF"}[newline], func(t *testing.T) {
			t.Parallel()

			source := "let value = combine(" + newline + "  \"first\", # ignored" + newline + "  nested(\"second\",), # trailing comment" + newline + ")" + newline
			module, err := ParseAST(source)
			if err != nil {
				t.Fatal(err)
			}
			declaration, ok := module.Nodes[0].(*LetDeclaration)
			if !ok || declaration.Expression == nil {
				t.Fatalf("expression = %#v, want a call with two arguments", module.Nodes[0])
			}
			expression, isCall := declaration.Expression.(*CallExpr)
			if !isCall || len(expression.Arguments) != 2 {
				t.Fatalf("expression = %#v, want a call with two arguments", declaration.Expression)
			}
			callStart := strings.Index(source, "combine(")
			callEnd := strings.LastIndex(source, ")"+newline) + 1
			if expression.ExprSpan() != (SourceSpan{Start: callStart, End: callEnd}) {
				t.Fatalf("call span = %#v, want [%d, %d)", expression.ExprSpan(), callStart, callEnd)
			}
			for index, want := range []string{"first", "second"} {
				argument := expression.Arguments[index]
				if index == 0 {
					literal, isString := argument.(*StringExpr)
					span := argument.ExprSpan()
					if !isString || literal.Value != want || source[span.Start:span.End] != `"`+want+`"` {
						t.Errorf("argument %d = %#v, want source literal %q", index, argument, want)
					}

					continue
				}
				nestedStart := strings.Index(source, "nested(")
				nestedEnd := strings.Index(source[nestedStart:], "),") + nestedStart + 1
				nested, isCall := argument.(*CallExpr)
				if !isCall || argument.ExprSpan() != (SourceSpan{Start: nestedStart, End: nestedEnd}) || len(nested.Arguments) != 1 {
					t.Fatalf("argument %d = %#v, want nested call span [%d, %d)", index, argument, nestedStart, nestedEnd)
				}
				nestedArgument := nested.Arguments[0]
				nestedLiteral, isString := nestedArgument.(*StringExpr)
				nestedSpan := nestedArgument.ExprSpan()
				if !isString || nestedLiteral.Value != want || source[nestedSpan.Start:nestedSpan.End] != `"`+want+`"` {
					t.Errorf("nested argument = %#v, want source literal %q", nestedArgument, want)
				}
			}

			lexemes, ok := syntax.LexemesInSpan(source, expression.ExprSpan())
			if !ok {
				t.Fatal("expression lexemes are unavailable for the absolute call span")
			}
			for _, lexeme := range lexemes {
				lexemeSource := source[lexeme.Start:lexeme.End]
				if strings.Contains(lexemeSource, "#") {
					t.Errorf("comment text was included in expression lexeme %q", lexemeSource)
				}
			}
		})
	}
}

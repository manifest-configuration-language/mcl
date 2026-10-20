package syntax

import (
	"reflect"
	"strings"
	"testing"
)

func TestFormatSourcePreservesNestedListConditionalBranches(t *testing.T) {
	t.Parallel()

	source := "let choice =\n  if true:\n    -\n      - 4\n  else:\n    -\n      - 5\n"
	formatted, err := FormatSource(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"    -\n      - 4", "    -\n      - 5"} {
		if !strings.Contains(formatted, expected) {
			t.Errorf("formatted source does not preserve nested list indentation %q:\n%s", expected, formatted)
		}
	}

	module, err := ParseAST(formatted)
	if err != nil {
		t.Fatal(err)
	}
	declaration, ok := module.Nodes[0].(*LetDeclaration)
	if !ok || declaration.Expression == nil {
		t.Fatalf("formatted expression = %#v, want a conditional", module.Nodes[0])
	}
	conditional, isConditional := declaration.Expression.(*ConditionalExpr)
	if !isConditional {
		t.Fatalf("formatted expression = %#v, want a conditional", declaration)
	}
	for _, branch := range []Expr{conditional.Then, conditional.Else} {
		outer, isList := branch.(*ListExpr)
		if !isList || len(outer.Items) != 1 {
			t.Fatalf("formatted branch = %#v, want a nested List containing an integer", branch)
		}
		inner, isList := outer.Items[0].(*ListExpr)
		if !isList || len(inner.Items) != 1 {
			t.Fatalf("formatted branch = %#v, want a nested List containing an integer", branch)
		}
		if _, isInteger := inner.Items[0].(*IntegerExpr); !isInteger {
			t.Fatalf("formatted branch = %#v, want a nested List containing an integer", branch)
		}
	}
}

func TestFormatSourcePreservesTriviaAttachmentsAndCRLF(t *testing.T) {
	t.Parallel()

	source := "# top first\n# top second\n\nlet value =\n  # map first\n  # map second\n  key:\n    # list first\n    # list second\n    - \"item\"\n\nlet count = length(\n  # call first\n  # call second\n  \"sample\",\n)\nemit count\n"
	module, tokens, err := ParseASTWithTokens(source)
	if err != nil {
		t.Fatal(err)
	}
	cst := parseFormatterCST(source, tokens)
	cst.addSyntaxLabels(module)
	if len(cst.triviaAttachments) != 8 {
		t.Fatalf("trivia attachments = %d, want 8 standalone comments", len(cst.triviaAttachments))
	}
	ownerCounts := make(map[SourceSpan]int)
	for _, trivia := range module.Trivia {
		if strings.TrimSpace(trivia.Text) == "" {
			continue
		}
		attachment, ok := cst.triviaAttachments[trivia.Span]
		if !ok || attachment.trivia != trivia {
			t.Fatalf("comment trivia %#v has no attachment keyed by its span", trivia)
		}
		if attachment.depth < 0 {
			t.Fatalf("comment trivia %#v has invalid attachment depth %d", trivia, attachment.depth)
		}
		if attachment.owner != (SourceSpan{}) {
			ownerFound := false
			for _, line := range cst.lines {
				ownerFound = ownerFound || line.kind == formatterCode && line.bodySpan == attachment.owner
			}
			if !ownerFound {
				t.Fatalf("comment trivia %#v has unknown owner span %#v", trivia, attachment.owner)
			}
		}
		ownerCounts[attachment.owner]++
	}
	sharedOwner := false
	for _, count := range ownerCounts {
		sharedOwner = sharedOwner || count > 1
	}
	if !sharedOwner {
		t.Fatal("multiple comments were not attached to the same owner")
	}

	formatted, err := FormatSource(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{"# top first", "# top second", "# map first", "# map second", "# list first", "# list second", "# call first", "# call second"} {
		if !strings.Contains(formatted, comment) {
			t.Errorf("formatted source lost %q:\n%s", comment, formatted)
		}
	}
	if !strings.Contains(formatted, "# top second\n\nlet value") {
		t.Errorf("formatted source did not preserve the blank line after top-level comments:\n%s", formatted)
	}
	for _, placement := range []string{
		"let value =\n  # map first\n  # map second\n  key:",
		"  key:\n    # list first\n    # list second\n    - \"item\"",
		"length(\n  # call first\n  # call second\n  \"sample\"",
	} {
		if !strings.Contains(formatted, placement) {
			t.Errorf("formatted source changed comment placement %q:\n%s", placement, formatted)
		}
	}
	for _, pair := range [][2]string{{"# top first", "# top second"}, {"# map first", "# map second"}, {"# list first", "# list second"}, {"# call first", "# call second"}} {
		if strings.Index(formatted, pair[0]) >= strings.Index(formatted, pair[1]) {
			t.Errorf("formatted comments changed order %q then %q:\n%s", pair[0], pair[1], formatted)
		}
	}

	crlfSource := strings.ReplaceAll(source, "\n", "\r\n")
	formattedCRLF, err := FormatSource(crlfSource)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.ReplaceAll(formatted, "\n", "\r\n"); formattedCRLF != want {
		t.Fatalf("CRLF formatted output differs from LF output with converted line endings:\n got %q\nwant %q", formattedCRLF, want)
	}
}

func TestFormatSourcePreservesASTSemanticsAndOutputContract(t *testing.T) {
	t.Parallel()

	source := "# package comment\n" +
		"type Workload = {kind: String, replicas?: Int}\n" +
		"fn replicas(value: Workload) -> Int = value.replicas ?? 1\n" +
		"let workloads =\n" +
		"  -\n" +
		"    kind: \"Deployment\"\n" +
		"    replicas: 2\n" +
		"  -\n" +
		"    kind: \"Job\"\n" +
		"let selected = exactlyOne(\n" +
		"  workloads,\n" +
		"  fn(value) => value.kind == \"Deployment\",\n" +
		")\n" +
		"let changed = selected with:\n" +
		"  replicas: replicas(selected) + 1\n" +
		"emit changed\n"

	before, err := ParseAST(source)
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := FormatSource(source)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ParseAST(formatted)
	if err != nil {
		t.Fatalf("formatted source does not parse: %v", err)
	}
	if !reflect.DeepEqual(normalizeModuleAST(before), normalizeModuleAST(after)) {
		t.Fatalf("formatting changed AST semantics:\nbefore: %#v\nafter:  %#v\nformatted source:\n%s", before, after, formatted)
	}
	second, err := FormatSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if second != formatted {
		t.Fatalf("formatting the same source produced different bytes:\nfirst:  %q\nsecond: %q", formatted, second)
	}
	if strings.HasSuffix(source, "\n") != strings.HasSuffix(formatted, "\n") {
		t.Fatalf("formatter changed the final newline contract: source=%q formatted=%q", source, formatted)
	}
	sourceWithoutFinalNewline := strings.TrimSuffix(source, "\n")
	formattedWithoutFinalNewline, err := FormatSource(sourceWithoutFinalNewline)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(formattedWithoutFinalNewline, "\n") {
		t.Fatalf("formatter added a final newline: %q", formattedWithoutFinalNewline)
	}
}

func normalizeModuleAST(module ModuleAST) ModuleAST {
	module.Trivia = nil
	normalizeASTValue(reflect.ValueOf(&module).Elem())

	return module
}

func normalizeASTValue(value reflect.Value) {
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			normalizeASTValue(value.Elem())
		}
	case reflect.Slice, reflect.Array:
		for index := range value.Len() {
			normalizeASTValue(value.Index(index))
		}
	case reflect.Struct:
		if value.Type() == reflect.TypeFor[SourceSpan]() {
			value.SetZero()

			return
		}
		for index := range value.NumField() {
			field := value.Field(index)
			if !field.CanSet() {
				continue
			}
			if name := value.Type().Field(index).Name; name == "Line" || name == "SourcePath" || name == "Source" {
				field.SetZero()

				continue
			}
			normalizeASTValue(field)
		}
	default:
		return
	}
}

func FuzzFormatSourceIdempotent(f *testing.F) {
	for _, seed := range []string{
		"emit {name: \"demo\"}\n",
		"let values =\n  - 1\n  - 2\nemit values\n",
		"# unicode λ\nlet enabled = true\nemit enabled\n",
		"type Item = {name: String, count?: Int}\nlet item: Item =\n  name: \"sample\"\nlet updated = item with:\n  count: (item.count ?? 0) + 1\nemit updated\n",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 64<<10 {
			t.Skip()
		}
		first, err := FormatSource(source)
		if err != nil {
			return
		}
		before, err := ParseAST(source)
		if err != nil {
			t.Fatalf("FormatSource accepted source that ParseAST rejected: %v", err)
		}
		after, err := ParseAST(first)
		if err != nil {
			t.Fatalf("formatted source no longer parses: %v", err)
		}
		if !reflect.DeepEqual(normalizeModuleAST(before), normalizeModuleAST(after)) {
			t.Fatalf("formatting changed AST semantics:\nsource: %q\nformatted: %q", source, first)
		}
		second, err := FormatSource(first)
		if err != nil {
			t.Fatalf("formatted source no longer parses: %v", err)
		}
		if second != first {
			t.Fatalf("formatting is not idempotent:\nfirst: %q\nsecond: %q", first, second)
		}
	})
}

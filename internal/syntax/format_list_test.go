package syntax

import (
	"strings"
	"testing"
)

func TestFormatSourceCompactsMapListItemStart(t *testing.T) {
	t.Parallel()

	const source = `let ports =
  -
    name: "http"
    containerPort: 8080
  -
    name: "metrics"
    containerPort: 9090
`
	formatted, err := FormatSource(source)
	if err != nil {
		t.Fatalf("FormatSource() error = %v", err)
	}
	for _, want := range []string{"  - name: \"http\"\n    containerPort: 8080", "  - name: \"metrics\"\n    containerPort: 9090"} {
		if !strings.Contains(formatted, want) {
			t.Errorf("formatted source does not contain %q:\n%s", want, formatted)
		}
	}
	second, err := FormatSource(formatted)
	if err != nil {
		t.Fatalf("FormatSource(formatted) error = %v", err)
	}
	if second != formatted {
		t.Fatalf("FormatSource() is not idempotent:\nfirst: %q\nsecond: %q", formatted, second)
	}
}

func TestFormatSourcePreservesMapLikeMultilineString(t *testing.T) {
	t.Parallel()

	const source = "let text = \"\"\"\n-\n  name: stays in the string\n\"\"\"\nlet ports =\n  -\n    name: http\n"
	formatted, err := FormatSource(source)
	if err != nil {
		t.Fatalf("FormatSource() error = %v", err)
	}
	if !strings.Contains(formatted, "-\n  name: stays in the string\n") {
		t.Fatalf("formatter changed multiline string contents:\n%s", formatted)
	}
	if !strings.Contains(formatted, "  - name: http\n") {
		t.Fatalf("formatter did not compact the Map list item:\n%s", formatted)
	}
}

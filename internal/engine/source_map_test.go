package engine

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBlockScalarOutputEndExcludesTrailingBlankLines(t *testing.T) {
	output := []byte("value: |\n  content\n\n")
	lineStarts := yamlLineStarts(output)
	lineEnds := yamlLineEnds(output, lineStarts)
	lineIndex := newYAMLLineIndex(output, lineStarts, lineEnds)
	contentEnd := strings.Index(string(output), "\n\n")
	node := &yaml.Node{Kind: yaml.ScalarNode, Line: 1, Column: 1}

	got := blockScalarOutputEnd(output, lineStarts, lineEnds, lineIndex, node, 0)
	if got != contentEnd {
		t.Fatalf("blockScalarOutputEnd() = %d, want %d", got, contentEnd)
	}
}

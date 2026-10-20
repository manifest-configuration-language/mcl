package engine

import "testing"

func TestSourcePositionUTF16CountsNonBMPCharactersAsTwoColumns(t *testing.T) {
	t.Parallel()

	source := "a😀b\nnext"
	line, column := SourcePositionUTF16(source, len("a😀"))
	if line != 1 || column != 4 {
		t.Fatalf("SourcePositionUTF16() = (%d, %d), want (1, 4)", line, column)
	}
}

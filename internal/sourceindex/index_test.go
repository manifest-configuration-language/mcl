package sourceindex

import "testing"

func TestPositionAtAndOffsetAtUseUTF16Columns(t *testing.T) {
	t.Parallel()

	index := New("a😀é\r\n二\n")
	for _, test := range []struct {
		offset   int
		position Position
	}{
		{offset: 0, position: Position{Line: 0, Character: 0}},
		{offset: len("a"), position: Position{Line: 0, Character: 1}},
		{offset: len("a😀"), position: Position{Line: 0, Character: 3}},
		{offset: len("a😀é"), position: Position{Line: 0, Character: 5}},
		{offset: len("a😀é\r\n"), position: Position{Line: 1, Character: 0}},
		{offset: len("a😀é\r\n二\n"), position: Position{Line: 2, Character: 0}},
	} {
		if got := index.PositionAt(test.offset); got != test.position {
			t.Errorf("PositionAt(%d) = %#v, want %#v", test.offset, got, test.position)
		}
		if got, err := index.OffsetAt(test.position); err != nil || got != test.offset {
			t.Errorf("OffsetAt(%#v) = %d, %v; want %d", test.position, got, err, test.offset)
		}
	}
	for _, offset := range []int{len("a😀é\r"), len("a😀é\r\n") - 1} {
		if got := index.PositionAt(offset); got != (Position{Line: 0, Character: 5}) {
			t.Errorf("PositionAt(%d) = %#v, want the end of the first line", offset, got)
		}
	}
	if _, err := index.OffsetAt(Position{Line: 0, Character: 2}); err == nil {
		t.Fatal("OffsetAt accepted a UTF-16 position inside a surrogate pair")
	}
}

func TestLineAtReturnsOneBasedLineAndByteBounds(t *testing.T) {
	t.Parallel()

	index := New("first\r\nsecond\n")
	for _, test := range []struct {
		offset int
		line   int
		start  int
		end    int
	}{
		{offset: 1, line: 1, start: 0, end: len("first")},
		{offset: len("first\r\n"), line: 2, start: len("first\r\n"), end: len("first\r\nsecond")},
		{offset: len("first\r\nsecond\n"), line: 3, start: len("first\r\nsecond\n"), end: len("first\r\nsecond\n")},
	} {
		line, start, end := index.LineAt(test.offset)
		if line != test.line || start != test.start || end != test.end {
			t.Errorf("LineAt(%d) = (%d, %d, %d), want (%d, %d, %d)", test.offset, line, start, end, test.line, test.start, test.end)
		}
	}
}

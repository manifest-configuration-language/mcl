package sourceindex

import (
	"errors"
	"sort"
)

// Indexはsource内の行開始byte offsetを保持します。
type Index struct {
	source     string
	lineStarts []int
}

// Positionは0始まりのlineとUTF-16 characterを表します。
type Position struct {
	Line      int
	Character int
}

// Newはsourceを走査してline indexを構築します。
func New(source string) *Index {
	lineStarts := []int{0}
	for offset := range len(source) {
		if source[offset] == '\n' {
			lineStarts = append(lineStarts, offset+1)
		}
	}

	return &Index{source: source, lineStarts: lineStarts}
}

// PositionAtはbyte offsetを0始まりのLSP positionへ変換します。
func (index *Index) PositionAt(offset int) Position {
	offset = min(max(offset, 0), len(index.source))
	line := sort.Search(len(index.lineStarts), func(line int) bool {
		return index.lineStarts[line] > offset
	}) - 1
	lineStart := index.lineStarts[line]
	_, _, lineEnd := index.LineAt(offset)
	offset = min(offset, lineEnd)
	character := 0
	for _, value := range index.source[lineStart:offset] {
		if value > 0xffff {
			character += 2
		} else {
			character++
		}
	}

	return Position{Line: line, Character: character}
}

// LineAtは1始まりの行番号と改行を除くbyte範囲を返します。
func (index *Index) LineAt(offset int) (line, start, end int) {
	offset = min(max(offset, 0), len(index.source))
	line = sort.Search(len(index.lineStarts), func(line int) bool {
		return index.lineStarts[line] > offset
	}) - 1
	start = index.lineStarts[line]
	end = len(index.source)
	if line+1 < len(index.lineStarts) {
		end = index.lineStarts[line+1] - 1
	}
	if end > start && index.source[end-1] == '\r' {
		end--
	}

	return line + 1, start, end
}

// OffsetAtは0始まりのLSP positionをbyte offsetへ変換します。
func (index *Index) OffsetAt(position Position) (int, error) {
	if position.Line < 0 || position.Character < 0 {
		return 0, errors.New("source position is negative")
	}
	if position.Line >= len(index.lineStarts) {
		return 0, errors.New("source position line is out of range")
	}
	lineStart := index.lineStarts[position.Line]
	_, _, lineEnd := index.LineAt(lineStart)
	character := 0
	for offset, value := range index.source[lineStart:lineEnd] {
		if character == position.Character {
			return lineStart + offset, nil
		}
		if value > 0xffff {
			character += 2
		} else {
			character++
		}
		if character > position.Character {
			break
		}
	}
	if character == position.Character {
		return lineEnd, nil
	}

	return 0, errors.New("source position character is out of range")
}

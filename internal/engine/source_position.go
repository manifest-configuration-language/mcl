package engine

import "unicode/utf16"

// SourcePositionUTF16はバイトオフセットを1始まりの行番号とUTF-16列番号に変換します。
func SourcePositionUTF16(source string, offset int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(source) {
		offset = len(source)
	}
	line, column := 1, 1
	for _, character := range source[:offset] {
		if character == '\n' {
			line++
			column = 1
		} else {
			column += utf16.RuneLen(character)
		}
	}

	return line, column
}

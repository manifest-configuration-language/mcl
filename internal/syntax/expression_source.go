package syntax

import (
	"sort"
	"strings"
)

type expressionSourceChunk struct {
	text   string
	offset int
}

type expressionSource struct {
	chunks      []expressionSourceChunk
	chunkStarts []int
	literals    map[SourceSpan]string
	lexemes     []sourceLexeme
	maskedSpans []SourceSpan
}

func (source expressionSource) Len() int {
	source = source.withChunkStarts()
	if len(source.chunks) == 0 {
		return 0
	}
	lastIndex := len(source.chunks) - 1

	return source.chunkStarts[lastIndex] + len(source.chunks[lastIndex].text)
}

func (source expressionSource) ByteAt(offset int) byte {
	source = source.withChunkStarts()

	return source.byteAt(offset)
}

func (source expressionSource) byteAt(offset int) byte {
	if offset < 0 || len(source.chunks) == 0 {
		return 0
	}
	index := sort.Search(len(source.chunkStarts), func(index int) bool { return source.chunkStarts[index] > offset }) - 1
	if index < 0 {
		return 0
	}
	chunkOffset := offset - source.chunkStarts[index]
	chunk := source.chunks[index]
	if chunkOffset < len(chunk.text) {
		if source.masked(chunk.offset + chunkOffset) {
			return ' '
		}

		return chunk.text[chunkOffset]
	}
	if index+1 < len(source.chunks) && chunkOffset == len(chunk.text) {
		return ' '
	}

	return 0
}

func (source expressionSource) Text(start, end int) string {
	if start < 0 || end < start || end > source.Len() {
		return ""
	}
	source = source.withChunkStarts()
	if len(source.maskedSpans) != 0 {
		var builder strings.Builder
		builder.Grow(end - start)
		for offset := start; offset < end; offset++ {
			builder.WriteByte(source.byteAt(offset))
		}

		return builder.String()
	}
	var builder strings.Builder
	builder.Grow(end - start)
	for offset := start; offset < end; {
		index := sort.Search(len(source.chunkStarts), func(index int) bool { return source.chunkStarts[index] > offset }) - 1
		if index < 0 {
			break
		}
		chunk := source.chunks[index]
		chunkOffset := offset - source.chunkStarts[index]
		if chunkOffset < len(chunk.text) {
			chunkEnd := min(end-offset, len(chunk.text)-chunkOffset)
			builder.WriteString(chunk.text[chunkOffset : chunkOffset+chunkEnd])
			offset += chunkEnd

			continue
		}
		if index+1 >= len(source.chunks) || chunkOffset != len(chunk.text) {
			break
		}
		builder.WriteByte(' ')
		offset++
	}

	return builder.String()
}

func (source expressionSource) masked(physicalOffset int) bool {
	index := sort.Search(len(source.maskedSpans), func(index int) bool {
		return source.maskedSpans[index].End > physicalOffset
	})

	return index < len(source.maskedSpans) && source.maskedSpans[index].Start <= physicalOffset
}

func (source expressionSource) PhysicalOffset(offset int) int {
	source = source.withChunkStarts()
	if len(source.chunks) == 0 {
		return 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset > source.Len() {
		offset = source.Len()
	}
	index := max(sort.Search(len(source.chunkStarts), func(index int) bool { return source.chunkStarts[index] > offset })-1, 0)
	chunkOffset := min(offset-source.chunkStarts[index], len(source.chunks[index].text))

	return source.chunks[index].offset + chunkOffset
}

func (source expressionSource) withChunkStarts() expressionSource {
	if len(source.chunkStarts) == len(source.chunks) {
		return source
	}
	source.chunkStarts = make([]int, len(source.chunks))
	for index := 1; index < len(source.chunks); index++ {
		source.chunkStarts[index] = source.chunkStarts[index-1] + len(source.chunks[index-1].text) + 1
	}

	return source
}

func (source expressionSource) PhysicalSpan(start, end int) SourceSpan {
	return SourceSpan{Start: source.PhysicalOffset(start), End: source.PhysicalOffset(end)}
}

func (source expressionSource) literalValue(start, end int) (string, bool) {
	value, ok := source.literals[source.PhysicalSpan(start, end)]

	return value, ok
}

func (source expressionSource) startsWithKeyword(keyword string) bool {
	source = source.withChunkStarts()
	cursor := expressionSourceCursor{source: source}
	lexeme, ok := cursor.next()

	return ok && lexeme.kind == sourceLexemeIdentifier && source.Text(lexeme.start, lexeme.end) == keyword
}

func (source expressionSource) endsWithHeader(keyword string) bool {
	source = source.withChunkStarts()
	cursor := expressionSourceCursor{source: source}
	parenDepth, bracketDepth, braceDepth := 0, 0, 0
	var previous, last sourceLexeme
	for {
		lexeme, ok := cursor.next()
		if !ok {
			break
		}
		if lexeme.kind == sourceLexemeSymbol {
			switch source.Text(lexeme.start, lexeme.end) {
			case "(":
				parenDepth++
			case ")":
				parenDepth--
			case "[":
				bracketDepth++
			case "]":
				bracketDepth--
			case "{":
				braceDepth++
			case "}":
				braceDepth--
			}
		}
		if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
			previous, last = last, lexeme
		}
	}

	return previous.kind == sourceLexemeIdentifier && last.kind == sourceLexemeSymbol && previous.end == last.start && last.end == source.Len() && source.Text(previous.start, previous.end) == keyword && previous.start > 0 && source.ByteAt(previous.start-1) == ' ' && source.Text(last.start, last.end) == ":"
}

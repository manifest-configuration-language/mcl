package output

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/manifest-configuration-language/mcl/internal/source"
	values "github.com/manifest-configuration-language/mcl/internal/value"
	"gopkg.in/yaml.v3"
)

// ManifestSourceMapはYAML出力のdocumentごとに値とソース位置を対応付けます。
type ManifestSourceMap struct {
	Documents []ManifestDocumentSourceMap
}

// ManifestDocumentSourceMapは1つのYAML document内の出力範囲とsource map項目を保持します。
type ManifestDocumentSourceMap struct {
	Index   int
	Entries []ManifestSourceMapEntry
}

// ManifestSourceMapEntryは出力YAML内の値またはmapping keyとその出典を保持します。
type ManifestSourceMapEntry struct {
	// PathはJSON Pointer形式の値位置です。ルート値は空文字列です。
	Path string
	// Kindはvalueまたはkeyです。
	Kind string
	// OutputStartとOutputEndはYAML全体に対する半開byte offsetです。
	OutputStart int
	OutputEnd   int
	// HasSourceはSourcePath、SourceSpan、SourceLocationsのいずれかに有効な出典があることを示します。
	HasSource       bool
	SourcePath      string
	SourceSpan      source.Span
	SourceLocations []source.DiagnosticLocation
	Origins         []ManifestSourceOrigin
}

// ManifestSourceOriginは出力値から追跡できる構造化された由来です。
type ManifestSourceOrigin struct {
	Path         string
	Span         source.Span
	Kind         source.OriginKind
	Multiplicity source.OriginMultiplicity
	Occurrences  int
}

// ManifestSourceMapForArtifacts matches rendered YAML documents to their values and source locations.
func ManifestSourceMapForArtifacts(output []byte, artifacts []values.Artifact) (ManifestSourceMap, error) {
	result := ManifestSourceMap{Documents: make([]ManifestDocumentSourceMap, 0, len(artifacts))}
	if len(artifacts) == 0 {
		return result, nil
	}

	lineStarts := yamlLineStarts(output)
	lineEnds := yamlLineEnds(output, lineStarts)
	lineIndex := newYAMLLineIndex(output, lineStarts, lineEnds)
	decoder := yaml.NewDecoder(bytes.NewReader(output))
	for index, artifact := range artifacts {
		var document yaml.Node
		if err := decoder.Decode(&document); err != nil {
			return ManifestSourceMap{}, fmt.Errorf("decode rendered YAML document %d: %w", index+1, err)
		}
		if len(document.Content) != 1 {
			return ManifestSourceMap{}, fmt.Errorf("rendered YAML document %d has no root node", index+1)
		}
		documentMap := ManifestDocumentSourceMap{Index: index + 1}
		outputRanges := yamlNodeOutputRanges(output, lineStarts, lineEnds, lineIndex, document.Content[0])
		appendManifestValueEntries(&documentMap.Entries, output, lineEnds, outputRanges, document.Content[0], artifact.Value, "", nil)
		result.Documents = append(result.Documents, documentMap)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return ManifestSourceMap{}, fmt.Errorf("decode rendered YAML after document %d: %w", len(artifacts), err)
		}

		return ManifestSourceMap{}, errors.New("rendered YAML contains more documents than artifacts")
	}

	return result, nil
}

func appendManifestValueEntries(entries *[]ManifestSourceMapEntry, output []byte, lineEnds []int, outputRanges yamlOutputRanges, node *yaml.Node, value values.Value, path string, inherited []source.DiagnosticLocation) {
	sourceValue := value
	value = nodeContents(value)
	locations := source.AppendLocations(slices.Clone(inherited), sourceValue.SourceLocations, "", source.Span{}, "")
	appendManifestEntry(entries, outputRanges.values[node], path, "value", sourceValue.SourcePath, sourceValue.Span, locations)
	childLocations := source.AppendLocations(slices.Clone(locations), nil, sourceValue.SourcePath, sourceValue.Span, "Containing value")

	//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
	switch value.Kind {
	case values.List, values.AdapterBundle:
		for index, item := range value.List {
			if index >= len(node.Content) {
				break
			}
			appendManifestValueEntries(entries, output, lineEnds, outputRanges, node.Content[index], item, path+"/"+strconv.Itoa(index), childLocations)
		}
	case values.Map:
		for index, entry := range value.Map {
			contentIndex := index * 2
			if contentIndex+1 >= len(node.Content) {
				break
			}
			keyNode := node.Content[contentIndex]
			childNode := node.Content[contentIndex+1]
			childPath := path + "/" + escapeJSONPointer(entry.Key)
			keyLocations := source.AppendLocations(slices.Clone(childLocations), entry.KeySourceLocations, entry.KeySourcePath, entry.KeySpan, "Map key")
			appendManifestEntry(entries, mappingKeyOutputRange(output, lineEnds, outputRanges.direct[keyNode], keyNode), childPath, "key", entry.KeySourcePath, entry.KeySpan, keyLocations)
			appendManifestValueEntries(entries, output, lineEnds, outputRanges, childNode, entry.Value, childPath, childLocations)
		}
	}
}

func appendManifestEntry(entries *[]ManifestSourceMapEntry, outputRange source.Span, path, kind, sourcePath string, sourceSpan source.Span, locations []source.DiagnosticLocation) {
	origins := make([]ManifestSourceOrigin, 0, len(locations)+1)
	if sourcePath != "" && sourceSpan.End > sourceSpan.Start {
		primary := ManifestSourceOrigin{Path: sourcePath, Span: sourceSpan, Kind: source.OriginMCL, Multiplicity: source.OriginUnique, Occurrences: 1}
		for _, location := range locations {
			if location.Path == sourcePath && location.Span == sourceSpan {
				primary = manifestSourceOrigin(location)

				break
			}
		}
		origins = append(origins, primary)
	}
	for _, location := range locations {
		if location.Path == "" || location.Span.End <= location.Span.Start {
			continue
		}
		origin := manifestSourceOrigin(location)
		if slices.Contains(origins, origin) {
			continue
		}
		origins = append(origins, origin)
	}
	hasSource := outputRange.End > outputRange.Start && sourcePath != "" && sourceSpan.End > sourceSpan.Start
	if !hasSource {
		for _, location := range locations {
			if outputRange.End > outputRange.Start && location.Path != "" && location.Span.End > location.Span.Start {
				hasSource = true

				break
			}
		}
	}
	*entries = append(*entries, ManifestSourceMapEntry{
		Path: path, Kind: kind, OutputStart: outputRange.Start, OutputEnd: outputRange.End,
		HasSource: hasSource, SourcePath: sourcePath, SourceSpan: sourceSpan,
		SourceLocations: slices.Clone(locations), Origins: origins,
	})
}

func manifestSourceOrigin(location source.DiagnosticLocation) ManifestSourceOrigin {
	origin := location.Origin
	if origin == "" {
		origin = sourceOriginFromMessage(location.Message)
	}
	multiplicity := location.Multiplicity
	if multiplicity == "" {
		multiplicity = source.OriginUnique
	}
	occurrences := location.Occurrences
	if occurrences == 0 {
		occurrences = 1
	}

	return ManifestSourceOrigin{Path: location.Path, Span: location.Span, Kind: origin, Multiplicity: multiplicity, Occurrences: occurrences}
}

func sourceOriginFromMessage(message string) source.OriginKind {
	switch message {
	case "Base value", "Base map", "Base map key":
		return source.OriginMCLOverlayBase
	case "Overlay value", "Overlay map", "Overlay map key":
		return source.OriginMCLOverlayPatch
	case "Original Kustomize resource":
		return source.OriginKustomizeInput
	case "Rendered from Helm template":
		return source.OriginHelmExpression
	default:
		return source.OriginMCL
	}
}

type yamlOutputRanges struct {
	values map[*yaml.Node]source.Span
	direct map[*yaml.Node]source.Span
}

func yamlNodeOutputRanges(output []byte, lineStarts, lineEnds []int, lineIndex yamlLineIndex, root *yaml.Node) yamlOutputRanges {
	ranges := yamlOutputRanges{
		values: make(map[*yaml.Node]source.Span),
		direct: make(map[*yaml.Node]source.Span),
	}
	order := []*yaml.Node{root}
	for index := 0; index < len(order); index++ {
		node := order[index]
		span := source.Span{
			Start: yamlNodeStart(output, lineStarts, lineEnds, node),
			End:   yamlNodeOutputEnd(output, lineStarts, lineEnds, lineIndex, node),
		}
		ranges.values[node] = span
		ranges.direct[node] = span
		order = append(order, node.Content...)
	}
	for index := len(order) - 1; index >= 0; index-- {
		node := order[index]
		span := ranges.values[node]
		for _, child := range node.Content {
			if childSpan := ranges.values[child]; childSpan.End > span.End {
				span.End = childSpan.End
			}
		}
		ranges.values[node] = span
	}

	return ranges
}

func mappingKeyOutputRange(output []byte, lineEnds []int, outputRange source.Span, node *yaml.Node) source.Span {
	start := outputRange.Start
	end := outputRange.End
	if start < 0 || start > end || end > len(output) {
		return source.Span{Start: start, End: start}
	}
	if node.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return source.Span{Start: start, End: end}
	}
	lineEnd := yamlNodeLineEnd(lineEnds, node.Line)
	if start > lineEnd {
		return source.Span{Start: start, End: start}
	}
	line := output[start:lineEnd]
	var quote byte
	for index := 0; index < len(line); index++ {
		if quote == '"' && line[index] == '\\' {
			index++

			continue
		}
		if quote == '\'' && line[index] == '\'' && index+1 < len(line) && line[index+1] == '\'' {
			index++

			continue
		}
		if quote != 0 {
			if line[index] == quote {
				quote = 0
			}

			continue
		}
		if line[index] == '"' || line[index] == '\'' {
			quote = line[index]

			continue
		}
		if line[index] == ':' && (index+1 == len(line) || line[index+1] == ' ' || line[index+1] == '\t') {
			return source.Span{Start: start, End: start + index}
		}
	}

	return source.Span{Start: start, End: start + len(line)}
}

func nodeContents(value values.Value) values.Value {
	for value.Kind == values.Node && value.Base != nil {
		value = *value.Base
	}

	return value
}

func yamlNodeOutputEnd(output []byte, lineStarts, lineEnds []int, lineIndex yamlLineIndex, node *yaml.Node) int {
	if node.Kind != yaml.ScalarNode {
		return yamlNodeLineEnd(lineEnds, node.Line)
	}
	start := yamlNodeStart(output, lineStarts, lineEnds, node)
	if start < 0 || start >= len(output) {
		return start
	}
	switch {
	case node.Style&yaml.DoubleQuotedStyle != 0:
		return quotedScalarOutputEnd(output, start, '"')
	case node.Style&yaml.SingleQuotedStyle != 0:
		return quotedScalarOutputEnd(output, start, '\'')
	case node.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return blockScalarOutputEnd(output, lineStarts, lineEnds, lineIndex, node, start)
	default:
		return yamlNodeLineEnd(lineEnds, node.Line)
	}
}

func quotedScalarOutputEnd(output []byte, start int, quote byte) int {
	for index := start + 1; index < len(output); index++ {
		if quote == '"' && output[index] == '\\' {
			index++

			continue
		}
		if output[index] != quote {
			continue
		}
		if quote == '\'' && index+1 < len(output) && output[index+1] == '\'' {
			index++

			continue
		}

		return index + 1
	}

	return start
}

func blockScalarOutputEnd(output []byte, lineStarts []int, lineEnds []int, lineIndex yamlLineIndex, node *yaml.Node, start int) int {
	baseIndent := 0
	for position := lineStarts[node.Line-1]; position < start && output[position] == ' '; position++ {
		baseIndent++
	}
	end := yamlNodeLineEnd(lineEnds, node.Line)
	if line := lineIndex.firstNonBlankAtMost(node.Line, baseIndent); line >= 0 {
		return lineStarts[line-1]
	}
	if line := lineIndex.lastNonBlankAbove(node.Line, baseIndent); line >= 0 {
		end = lineEnds[line]
	}
	return end
}

// yamlLineIndex finds the next nonblank line at or below a block scalar's indentation in logarithmic time.
type yamlLineIndex struct {
	base      int
	minIndent []int
	maxIndent []int
	lineCount int
}

func newYAMLLineIndex(output []byte, lineStarts, lineEnds []int) yamlLineIndex {
	base := 1
	for base < len(lineStarts) {
		base <<= 1
	}
	index := yamlLineIndex{base: base, minIndent: make([]int, base*2), maxIndent: make([]int, base*2), lineCount: len(lineStarts)}
	for i := range index.minIndent {
		index.minIndent[i] = int(^uint(0) >> 1)
	}
	for i := range index.maxIndent {
		index.maxIndent[i] = -1
	}
	for line, start := range lineStarts {
		end := lineEnds[line]
		indent := 0
		for position := start; position < end && output[position] == ' '; position++ {
			indent++
		}
		if indent == end-start {
			continue
		}
		index.minIndent[base+line] = indent
		index.maxIndent[base+line] = indent
	}
	for i := base - 1; i > 0; i-- {
		if index.minIndent[i*2] < index.minIndent[i*2+1] {
			index.minIndent[i] = index.minIndent[i*2]
		} else {
			index.minIndent[i] = index.minIndent[i*2+1]
		}
		if index.maxIndent[i*2] > index.maxIndent[i*2+1] {
			index.maxIndent[i] = index.maxIndent[i*2]
		} else {
			index.maxIndent[i] = index.maxIndent[i*2+1]
		}
	}
	return index
}

func (index yamlLineIndex) firstNonBlankAtMost(afterLine, indent int) int {
	return index.find(1, 0, index.base, afterLine, indent)
}

func (index yamlLineIndex) find(node, left, right, afterLine, indent int) int {
	if right <= afterLine || left >= index.lineCount || index.minIndent[node] > indent {
		return -1
	}
	if right-left == 1 {
		return left + 1
	}
	middle := left + (right-left)/2
	if line := index.find(node*2, left, middle, afterLine, indent); line >= 0 {
		return line
	}
	return index.find(node*2+1, middle, right, afterLine, indent)
}

func (index yamlLineIndex) lastNonBlankAbove(afterLine, indent int) int {
	return index.findLast(1, 0, index.base, afterLine, indent)
}

func (index yamlLineIndex) findLast(node, left, right, afterLine, indent int) int {
	if right <= afterLine || left >= index.lineCount || index.maxIndent[node] <= indent {
		return -1
	}
	if right-left == 1 {
		if index.maxIndent[node] > indent {
			return left
		}
		return -1
	}
	middle := left + (right-left)/2
	if line := index.findLast(node*2+1, middle, right, afterLine, indent); line >= 0 {
		return line
	}
	return index.findLast(node*2, left, middle, afterLine, indent)
}

func yamlNodeStart(output []byte, lineStarts, lineEnds []int, node *yaml.Node) int {
	if node.Line < 1 || node.Line > len(lineStarts) || node.Column < 1 {
		return 0
	}
	start := lineStarts[node.Line-1]
	lineEnd := yamlNodeLineEnd(lineEnds, node.Line)
	line := output[start:lineEnd]
	column := node.Column - 1
	for column > 0 && len(line) > 0 {
		_, size := utf8.DecodeRune(line)
		if size == 0 {
			break
		}
		line = line[size:]
		start += size
		column--
	}

	return start
}

func yamlNodeLineEnd(lineEnds []int, line int) int {
	if line < 1 || line > len(lineEnds) {
		return 0
	}

	return lineEnds[line-1]
}

func yamlLineStarts(output []byte) []int {
	starts := []int{0}
	for index, value := range output {
		if value == '\n' && index+1 < len(output) {
			starts = append(starts, index+1)
		}
	}

	return starts
}

func yamlLineEnds(output []byte, lineStarts []int) []int {
	ends := make([]int, len(lineStarts))
	line := 0
	for index, value := range output {
		if value != '\n' {
			continue
		}
		end := index
		if end > lineStarts[line] && output[end-1] == '\r' {
			end--
		}
		ends[line] = end
		line++
		if line == len(lineStarts) {
			return ends
		}
	}
	if line < len(ends) {
		ends[line] = len(output)
	}

	return ends
}

func escapeJSONPointer(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")

	return strings.ReplaceAll(value, "/", "~1")
}

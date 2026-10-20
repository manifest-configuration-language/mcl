package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

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
	SourceSpan      SourceSpan
	SourceLocations []SourceDiagnosticLocation
	Origins         []ManifestSourceOrigin
}

// ManifestSourceOriginは出力値から追跡できる構造化された由来です。
type ManifestSourceOrigin struct {
	Path         string
	Span         SourceSpan
	Kind         SourceOriginKind
	Multiplicity SourceOriginMultiplicity
	Occurrences  int
}

func manifestSourceMap(output []byte, artifacts []indexedArtifact) (ManifestSourceMap, error) {
	result := ManifestSourceMap{Documents: make([]ManifestDocumentSourceMap, 0, len(artifacts))}
	if len(artifacts) == 0 {
		return result, nil
	}

	lineStarts := yamlLineStarts(output)
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
		appendManifestValueEntries(&documentMap.Entries, output, lineStarts, document.Content[0], artifact.artifact.Value, "", nil)
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

func appendManifestValueEntries(entries *[]ManifestSourceMapEntry, output []byte, lineStarts []int, node *yaml.Node, value Value, path string, inherited []SourceDiagnosticLocation) {
	sourceValue := value
	value = nodeContents(value)
	locations := appendSourceLocations(slices.Clone(inherited), sourceValue.SourceLocations, "", SourceSpan{}, "")
	appendManifestEntry(entries, outputRange(output, lineStarts, node), path, "value", sourceValue.SourcePath, sourceValue.Span, locations)
	childLocations := appendSourceLocations(slices.Clone(locations), nil, sourceValue.SourcePath, sourceValue.Span, "Containing value")

	//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
	switch value.Kind {
	case List, AdapterBundle:
		for index, item := range value.List {
			if index >= len(node.Content) {
				break
			}
			appendManifestValueEntries(entries, output, lineStarts, node.Content[index], item, path+"/"+strconv.Itoa(index), childLocations)
		}
	case Map:
		for index, entry := range value.Map {
			contentIndex := index * 2
			if contentIndex+1 >= len(node.Content) {
				break
			}
			keyNode := node.Content[contentIndex]
			childNode := node.Content[contentIndex+1]
			childPath := path + "/" + escapeJSONPointer(entry.Key)
			keyLocations := appendSourceLocations(slices.Clone(childLocations), entry.KeySourceLocations, entry.KeySourcePath, entry.KeySpan, "Map key")
			appendManifestEntry(entries, mappingKeyOutputRange(output, lineStarts, keyNode), childPath, "key", entry.KeySourcePath, entry.KeySpan, keyLocations)
			appendManifestValueEntries(entries, output, lineStarts, childNode, entry.Value, childPath, childLocations)
		}
	}
}

func appendManifestEntry(entries *[]ManifestSourceMapEntry, outputRange SourceSpan, path, kind, sourcePath string, sourceSpan SourceSpan, locations []SourceDiagnosticLocation) {
	origins := make([]ManifestSourceOrigin, 0, len(locations)+1)
	if sourcePath != "" && sourceSpan.End > sourceSpan.Start {
		primary := ManifestSourceOrigin{Path: sourcePath, Span: sourceSpan, Kind: SourceOriginMCL, Multiplicity: SourceOriginUnique, Occurrences: 1}
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

func manifestSourceOrigin(location SourceDiagnosticLocation) ManifestSourceOrigin {
	origin := location.Origin
	if origin == "" {
		origin = sourceOriginFromMessage(location.Message)
	}
	multiplicity := location.Multiplicity
	if multiplicity == "" {
		multiplicity = SourceOriginUnique
	}
	occurrences := location.Occurrences
	if occurrences == 0 {
		occurrences = 1
	}

	return ManifestSourceOrigin{Path: location.Path, Span: location.Span, Kind: origin, Multiplicity: multiplicity, Occurrences: occurrences}
}

func sourceOriginFromMessage(message string) SourceOriginKind {
	switch message {
	case "Base value", "Base map", "Base map key":
		return SourceOriginMCLOverlayBase
	case "Overlay value", "Overlay map", "Overlay map key":
		return SourceOriginMCLOverlayPatch
	case kustomizeSourceLocationLabel:
		return SourceOriginKustomizeInput
	case helmRenderedTemplateSourceLabel:
		return SourceOriginHelmExpression
	default:
		return SourceOriginMCL
	}
}

func outputRange(output []byte, lineStarts []int, node *yaml.Node) SourceSpan {
	start := yamlNodeStart(output, lineStarts, node)
	end := yamlNodeOutputEnd(output, lineStarts, node)
	for _, child := range node.Content {
		childRange := outputRange(output, lineStarts, child)
		if childRange.End > end {
			end = childRange.End
		}
	}

	return SourceSpan{Start: start, End: end}
}

func mappingKeyOutputRange(output []byte, lineStarts []int, node *yaml.Node) SourceSpan {
	start := yamlNodeStart(output, lineStarts, node)
	end := yamlNodeOutputEnd(output, lineStarts, node)
	if start < 0 || start > end || end > len(output) {
		return SourceSpan{Start: start, End: start}
	}
	if node.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return SourceSpan{Start: start, End: end}
	}
	lineEnd := yamlNodeLineEnd(output, lineStarts, node.Line)
	if start > lineEnd {
		return SourceSpan{Start: start, End: start}
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
			return SourceSpan{Start: start, End: start + index}
		}
	}

	return SourceSpan{Start: start, End: start + len(line)}
}

func yamlNodeOutputEnd(output []byte, lineStarts []int, node *yaml.Node) int {
	if node.Kind != yaml.ScalarNode {
		return yamlNodeLineEnd(output, lineStarts, node.Line)
	}
	start := yamlNodeStart(output, lineStarts, node)
	if start < 0 || start >= len(output) {
		return start
	}
	switch {
	case node.Style&yaml.DoubleQuotedStyle != 0:
		return quotedScalarOutputEnd(output, start, '"')
	case node.Style&yaml.SingleQuotedStyle != 0:
		return quotedScalarOutputEnd(output, start, '\'')
	case node.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return blockScalarOutputEnd(output, lineStarts, node, start)
	default:
		return yamlNodeLineEnd(output, lineStarts, node.Line)
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

func blockScalarOutputEnd(output []byte, lineStarts []int, node *yaml.Node, start int) int {
	baseIndent := 0
	for position := lineStarts[node.Line-1]; position < start && output[position] == ' '; position++ {
		baseIndent++
	}
	end := yamlNodeLineEnd(output, lineStarts, node.Line)
	for line := node.Line + 1; line <= len(lineStarts); line++ {
		lineStart := lineStarts[line-1]
		lineEnd := yamlNodeLineEnd(output, lineStarts, line)
		indent := 0
		for position := lineStart; position < lineEnd && output[position] == ' '; position++ {
			indent++
		}
		if indent == lineEnd-lineStart {
			continue
		}
		if indent <= baseIndent {
			return lineStart
		}
		end = lineEnd
	}

	return end
}

func yamlNodeStart(output []byte, lineStarts []int, node *yaml.Node) int {
	if node.Line < 1 || node.Line > len(lineStarts) || node.Column < 1 {
		return 0
	}
	start := lineStarts[node.Line-1]
	lineEnd := yamlNodeLineEnd(output, lineStarts, node.Line)
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

func yamlNodeLineEnd(output []byte, lineStarts []int, line int) int {
	if line < 1 || line > len(lineStarts) {
		return 0
	}
	start := lineStarts[line-1]
	if newline := bytes.IndexByte(output[start:], '\n'); newline >= 0 {
		end := start + newline
		if end > start && output[end-1] == '\r' {
			end--
		}

		return end
	}

	return len(output)
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

func escapeJSONPointer(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")

	return strings.ReplaceAll(value, "/", "~1")
}

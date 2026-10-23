package helmrender

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"helm.sh/helm/v4/pkg/chart/common"
	v2 "helm.sh/helm/v4/pkg/chart/v2"
)

// SpanはHelm template source file内のbyte範囲を表します。
type Span struct {
	Start int
	End   int
}

// FieldLocationはtemplate markerを含むrender後のYAML fieldを示します。
type FieldLocation struct {
	Path     []int
	Document int
	Key      bool
}

// FieldMappingはrender後のfieldと生成元のHelm template actionを対応付けます。
type FieldMapping struct {
	Marker    string
	Span      Span
	Source    []byte
	Locations []FieldLocation
	Fallback  bool
	Branch    bool
}

type fieldMapping struct {
	marker    string
	span      Span
	source    []byte
	locations []fieldLocation
	branch    bool
}

type fieldLocation struct {
	path     []int
	document int
	key      bool
}

type helmTemplateAction struct {
	start     int
	end       int
	leftTrim  bool
	rightTrim bool
	branch    bool
}

// DocumentはMCL RuntimeValueに依存しない順序付きYAML documentです。
type Document struct {
	Value Value
}

// Valueはrender後のtemplate markerを探す順序付きYAML valueです。
type Value struct {
	Kind    ValueKind
	Text    string
	Bool    bool
	Entries []Entry
	Items   []Value
}

// ValueKindはinstrumentしたoutputの比較に使うYAML value種別です。
type ValueKind uint8

const (
	ValueOther ValueKind = iota
	ValueMap
	ValueList
	ValueString
	ValueInteger
	ValueDecimal
	ValueBool
	ValueNull
)

// Entryは順序付きmappingの要素です。
type Entry struct {
	Key   string
	Value Value
}

// ErrMappingResourceLimitはmapping解析が呼び出し元のresource上限に達したことを示します。
var ErrMappingResourceLimit = errors.New("helm field mapping resource limit exceeded")

// DocumentParserはrender後のYAMLをadapterに依存しない順序付きdocumentへ変換します。
type DocumentParser func(string) ([]Document, error)

func instrumentChart(source *v2.Chart) (*v2.Chart, map[string][]fieldMapping) {
	markerIndex := 0

	return instrumentChartWithMarkerIndex(source, &markerIndex)
}

func instrumentChartWithMarkerIndex(source *v2.Chart, markerIndex *int) (*v2.Chart, map[string][]fieldMapping) {
	clone := *source
	clone.Templates = make([]*common.File, len(source.Templates))
	mappings := make(map[string][]fieldMapping)
	for index, sourceFile := range source.Templates {
		if sourceFile == nil {
			continue
		}
		file := *sourceFile
		file.Data = bytes.Clone(sourceFile.Data)
		actions := helmTemplateOutputActions(file.Data)
		if len(actions) > 0 {
			var output bytes.Buffer
			cursor := 0
			for _, action := range actions {
				marker := fmt.Sprintf("MCLHELMFIELD%08d", *markerIndex)
				*markerIndex = *markerIndex + 1
				output.Write(file.Data[cursor:action.start])
				output.WriteString("{{")
				if action.leftTrim {
					output.WriteString("- ")
				}
				output.WriteString(`printf "%s" "`)
				output.WriteString(marker)
				output.WriteByte('"')
				if suffix := helmTemplateIndentPipeline(file.Data[action.start:action.end], action); len(suffix) != 0 {
					output.WriteByte(' ')
					output.Write(suffix)
				}
				if action.rightTrim {
					output.WriteString(" -")
				}
				output.WriteString("}}")
				mappings[path.Join(source.ChartFullPath(), file.Name)] = append(mappings[path.Join(source.ChartFullPath(), file.Name)], fieldMapping{marker: marker, span: Span{Start: action.start, End: action.end}, source: bytes.Clone(sourceFile.Data), branch: action.branch})
				cursor = action.end
			}
			output.Write(file.Data[cursor:])
			file.Data = output.Bytes()
		}
		clone.Templates[index] = &file
	}
	dependencies := source.Dependencies()
	clonedDependencies := make([]*v2.Chart, len(dependencies))
	for index, dependency := range dependencies {
		clonedDependency, dependencyMappings := instrumentChartWithMarkerIndex(dependency, markerIndex)
		clonedDependencies[index] = clonedDependency
		maps.Copy(mappings, dependencyMappings)
	}
	clone.SetDependencies(clonedDependencies...)

	return &clone, mappings
}

func helmTemplateIndentPipeline(actionSource []byte, action helmTemplateAction) []byte {
	bodyStart := 2
	bodyEnd := len(actionSource) - 2
	if action.leftTrim {
		bodyStart++
	}
	if action.rightTrim {
		bodyEnd--
	}
	if bodyStart >= bodyEnd {
		return nil
	}
	body := actionSource[bodyStart:bodyEnd]
	var quote byte
	escaped := false
	depth := 0
	for index := range body {
		character := body[index]
		if quote != 0 {
			if quote != '`' && escaped {
				escaped = false
			} else if quote != '`' && character == '\\' {
				escaped = true
			} else if character == quote {
				quote = 0
			}

			continue
		}
		if character == '"' || character == '\'' || character == '`' {
			quote = character

			continue
		}
		switch character {
		case '(':
			depth++
		case ')':
			depth--
		case '|':
			if depth != 0 {
				continue
			}
			functionStart := index + 1
			for functionStart < len(body) && (body[functionStart] == ' ' || body[functionStart] == '\t' || body[functionStart] == '\r' || body[functionStart] == '\n') {
				functionStart++
			}
			functionEnd := functionStart
			for functionEnd < len(body) && (body[functionEnd] == '_' || body[functionEnd] >= 'a' && body[functionEnd] <= 'z' || body[functionEnd] >= 'A' && body[functionEnd] <= 'Z') {
				functionEnd++
			}
			if string(body[functionStart:functionEnd]) == "indent" || string(body[functionStart:functionEnd]) == "nindent" {
				return body[index:]
			}
		}
	}

	return nil
}

func helmTemplateOutputActions(source []byte) []helmTemplateAction {
	var actions []helmTemplateAction
	var controlStack []struct{}
	for cursor := 0; cursor+1 < len(source); {
		start := bytes.Index(source[cursor:], []byte("{{"))
		if start < 0 {
			break
		}
		start += cursor
		end := helmTemplateActionEnd(source, start+2)
		if end < 0 {
			break
		}
		action := helmTemplateAction{
			start: start, end: end,
			leftTrim:  start+2 < end && source[start+2] == '-',
			rightTrim: end >= 3 && source[end-3] == '-',
		}
		bodyStart := start + 2
		bodyEnd := end - 2
		if action.leftTrim {
			bodyStart++
		}
		if action.rightTrim {
			bodyEnd--
		}
		body := strings.TrimSpace(string(source[bodyStart:bodyEnd]))
		fields := strings.Fields(body)
		command := ""
		if len(fields) > 0 {
			command = fields[0]
		}
		if helmTemplateActionProducesOutput(source[bodyStart:bodyEnd]) {
			action.branch = len(controlStack) > 0
			actions = append(actions, action)
		}
		switch command {
		case "if", "range", "with", "block":
			controlStack = append(controlStack, struct{}{})
		case "end":
			if len(controlStack) > 0 {
				controlStack = controlStack[:len(controlStack)-1]
			}
		}
		cursor = end
	}

	return actions
}

func helmTemplateActionEnd(source []byte, cursor int) int {
	var quote byte
	escaped := false
	for cursor+1 < len(source) {
		character := source[cursor]
		if quote != 0 {
			if quote != '`' && escaped {
				escaped = false
			} else if quote != '`' && character == '\\' {
				escaped = true
			} else if character == quote {
				quote = 0
			}
			cursor++

			continue
		}
		if character == '"' || character == '\'' || character == '`' {
			quote = character
			cursor++

			continue
		}
		if source[cursor] == '/' && source[cursor+1] == '*' {
			commentEnd := bytes.Index(source[cursor+2:], []byte("*/"))
			if commentEnd < 0 {
				return -1
			}
			cursor += commentEnd + 4

			continue
		}
		if source[cursor] == '}' && source[cursor+1] == '}' {
			return cursor + 2
		}
		cursor++
	}

	return -1
}

func helmTemplateActionProducesOutput(body []byte) bool {
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(body)), "-")))
	if len(fields) == 0 || strings.HasPrefix(fields[0], "/*") {
		return false
	}
	switch fields[0] {
	case "if", "range", "else", "end", "with", "define", "block", "break", "continue":
		return false
	}

	return !strings.HasPrefix(fields[0], "$") || len(fields) < 2 || fields[1] != ":=" && fields[1] != "="
}

// RenderedFieldMappingsはChartをinstrumentし、render後のYAMLからtemplate actionの位置を特定します。
func RenderedFieldMappings(ctx context.Context, source Chart, values map[string]any, original map[string]string, parse DocumentParser) (map[string][]FieldMapping, error) {
	root, ok := source.source.(*v2.Chart)
	if !ok {
		return nil, nil
	}
	instrumented, mappings := instrumentChart(root)
	if len(mappings) == 0 {
		return nil, nil
	}
	rendered, err := renderSource(ctx, instrumented, values)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		return nil, nil
	}
	result := make(map[string][]FieldMapping)
	for filename, sourceMappings := range mappings {
		originalYAML, exists := original[filename]
		instrumentedYAML, renderedExists := rendered[filename]
		if !exists || !renderedExists || parse == nil {
			continue
		}
		originalDocuments, originalErr := parse(originalYAML)
		instrumentedDocuments, instrumentedErr := parse(instrumentedYAML)
		if errors.Is(originalErr, ErrMappingResourceLimit) || errors.Is(instrumentedErr, ErrMappingResourceLimit) {
			return nil, ErrMappingResourceLimit
		}
		if originalErr != nil || instrumentedErr != nil || len(originalDocuments) != len(instrumentedDocuments) {
			continue
		}
		valid := true
		for index := range originalDocuments {
			if !collectFieldMappingValue(originalDocuments[index].Value, instrumentedDocuments[index].Value, sourceMappings, nil, index) {
				valid = false

				break
			}
		}
		for _, mapping := range sourceMappings {
			if len(mapping.locations) == 0 && valid {
				continue
			}
			result[filename] = append(result[filename], FieldMapping{
				Marker: mapping.marker, Span: mapping.span, Source: bytes.Clone(mapping.source),
				Locations: exportFieldLocations(mapping.locations), Fallback: !valid, Branch: mapping.branch,
			})
		}
	}

	return result, nil
}

func exportFieldLocations(locations []fieldLocation) []FieldLocation {
	result := make([]FieldLocation, len(locations))
	for index, location := range locations {
		result[index] = FieldLocation{Path: slices.Clone(location.path), Document: location.document, Key: location.key}
	}

	return result
}

func collectFieldMappingValue(original, instrumented Value, mappings []fieldMapping, valuePath []int, documentIndex int) bool {
	if instrumented.Kind == ValueString && recordTemplateMarkers(instrumented.Text, mappings, valuePath, documentIndex, false) {
		return true
	}
	if original.Kind != instrumented.Kind {
		return false
	}
	switch original.Kind {
	case ValueMap:
		if len(original.Entries) != len(instrumented.Entries) {
			return false
		}
		for index := range original.Entries {
			childPath := append(slices.Clone(valuePath), index)
			if original.Entries[index].Key != instrumented.Entries[index].Key && !recordTemplateMarkers(instrumented.Entries[index].Key, mappings, childPath, documentIndex, true) {
				return false
			}
			if !collectFieldMappingValue(original.Entries[index].Value, instrumented.Entries[index].Value, mappings, childPath, documentIndex) {
				return false
			}
		}
	case ValueList:
		if len(original.Items) != len(instrumented.Items) {
			return false
		}
		for index := range original.Items {
			childPath := append(slices.Clone(valuePath), index)
			if !collectFieldMappingValue(original.Items[index], instrumented.Items[index], mappings, childPath, documentIndex) {
				return false
			}
		}
	case ValueString, ValueInteger, ValueDecimal:
		return original.Text == instrumented.Text
	case ValueBool:
		return original.Bool == instrumented.Bool
	case ValueNull:
		return true
	default:
		return false
	}

	return true
}

func recordTemplateMarkers(text string, mappings []fieldMapping, valuePath []int, documentIndex int, key bool) bool {
	matched := false
	for index := range mappings {
		if !strings.Contains(text, mappings[index].marker) {
			continue
		}
		mapping := &mappings[index]
		mapping.locations = append(mapping.locations, fieldLocation{path: slices.Clone(valuePath), document: documentIndex, key: key})
		matched = true
	}

	return matched
}

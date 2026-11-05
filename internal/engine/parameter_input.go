package engine

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"
)

// ParameterSource describes where one runtime parameter came from.
type ParameterSource struct {
	Label      string
	SourcePath string
	Locations  map[string]ParameterLocation
}

// ParameterLocation stores byte spans for a JSON value and its object key.
type ParameterLocation struct {
	Span    SourceSpan
	KeySpan SourceSpan
}

func cloneParameterSources(sources map[string]ParameterSource) map[string]ParameterSource {
	if sources == nil {
		return nil
	}
	cloned := make(map[string]ParameterSource, len(sources))
	for name, source := range sources {
		source.Locations = maps.Clone(source.Locations)
		cloned[name] = source
	}

	return cloned
}

// ParseParameterFile parses a top-level parameter object and records each JSON node's byte span.
func ParseParameterFile(path string, source []byte) (map[string]jsontext.Value, map[string]ParameterSource, error) {
	if len(source) > maxRuntimeParameterBytes {
		return nil, nil, fmt.Errorf("parameter file exceeds %d bytes", maxRuntimeParameterBytes)
	}
	decoder := jsontext.NewDecoder(bytes.NewReader(source))
	if decoder.PeekKind() != jsontext.KindBeginObject {
		return nil, nil, errors.New("expected a top-level JSON object")
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, nil, fmt.Errorf("read parameter object: %w", err)
	}
	parameters := make(map[string]jsontext.Value)
	sources := make(map[string]ParameterSource)
	for decoder.PeekKind() != jsontext.KindEndObject {
		keyStart := nextParameterJSONOffset(source, int(decoder.InputOffset()))
		key, err := decoder.ReadToken()
		if err != nil {
			return nil, nil, fmt.Errorf("read parameter name: %w", err)
		}
		if key.Kind() != jsontext.KindString {
			return nil, nil, errors.New("parameter object key must be a string")
		}
		name := key.String()
		if _, exists := parameters[name]; exists {
			return nil, nil, fmt.Errorf("parameter object contains duplicate key %q", name)
		}
		keySpan := SourceSpan{Start: keyStart, End: int(decoder.InputOffset())}
		pointer := parameterJSONPointer(name)
		locations := make(map[string]ParameterLocation)
		location, err := readParameterJSONValue(decoder, source, pointer, keySpan, locations, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("read parameter %q: %w", name, err)
		}
		parameters[name] = jsontext.Value(source[location.Span.Start:location.Span.End]).Clone()
		sources[name] = ParameterSource{Label: path, SourcePath: path, Locations: locations}
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, nil, fmt.Errorf("read parameter object end: %w", err)
	}
	if _, err := decoder.ReadValue(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, nil, errors.New("unexpected data after parameter object")
		}

		return nil, nil, fmt.Errorf("read parameter file end: %w", err)
	}

	return parameters, sources, nil
}

func readParameterJSONValue(decoder *jsontext.Decoder, source []byte, pointer string, keySpan SourceSpan, locations map[string]ParameterLocation, depth int) (ParameterLocation, error) {
	if depth > maxCallDepth {
		return ParameterLocation{}, fmt.Errorf("parameter value nesting exceeds %d levels", maxCallDepth)
	}
	start := nextParameterJSONOffset(source, int(decoder.InputOffset()))
	var end int
	switch decoder.PeekKind() {
	case jsontext.KindBeginObject:
		if _, err := decoder.ReadToken(); err != nil {
			return ParameterLocation{}, err
		}
		seen := make(map[string]struct{})
		for decoder.PeekKind() != jsontext.KindEndObject {
			childKeyStart := nextParameterJSONOffset(source, int(decoder.InputOffset()))
			key, err := decoder.ReadToken()
			if err != nil {
				return ParameterLocation{}, err
			}
			if key.Kind() != jsontext.KindString {
				return ParameterLocation{}, errors.New("parameter object key must be a string")
			}
			name := key.String()
			if _, exists := seen[name]; exists {
				return ParameterLocation{}, fmt.Errorf("parameter object contains duplicate key %q", name)
			}
			seen[name] = struct{}{}
			childPointer := appendParameterJSONPointer(pointer, name)
			childKeySpan := SourceSpan{Start: childKeyStart, End: int(decoder.InputOffset())}
			if _, err := readParameterJSONValue(decoder, source, childPointer, childKeySpan, locations, depth+1); err != nil {
				return ParameterLocation{}, fmt.Errorf("field %q: %w", name, err)
			}
		}
		if _, err := decoder.ReadToken(); err != nil {
			return ParameterLocation{}, err
		}
		end = int(decoder.InputOffset())
	case jsontext.KindBeginArray:
		if _, err := decoder.ReadToken(); err != nil {
			return ParameterLocation{}, err
		}
		index := 0
		for decoder.PeekKind() != jsontext.KindEndArray {
			if _, err := readParameterJSONValue(decoder, source, appendParameterJSONPointer(pointer, strconv.Itoa(index)), SourceSpan{}, locations, depth+1); err != nil {
				return ParameterLocation{}, fmt.Errorf("item %d: %w", index, err)
			}
			index++
		}
		if _, err := decoder.ReadToken(); err != nil {
			return ParameterLocation{}, err
		}
		end = int(decoder.InputOffset())
	default:
		if _, err := decoder.ReadValue(); err != nil {
			return ParameterLocation{}, err
		}
		end = int(decoder.InputOffset())
	}
	location := ParameterLocation{Span: SourceSpan{Start: start, End: end}, KeySpan: keySpan}
	if locations != nil {
		locations[pointer] = location
	}

	return location, nil
}

func nextParameterJSONOffset(source []byte, offset int) int {
	for offset < len(source) {
		switch source[offset] {
		case ' ', '\t', '\r', '\n', ',', ':':
			offset++
		default:
			return offset
		}
	}

	return offset
}

func parameterJSONPointer(name string) string {
	return appendParameterJSONPointer("", name)
}

func appendParameterJSONPointer(pointer, part string) string {
	part = strings.ReplaceAll(strings.ReplaceAll(part, "~", "~0"), "/", "~1")

	return pointer + "/" + part
}

func parameterJSONDisplayPath(name string, relative []runtimeTypePathSegment) string {
	var path strings.Builder
	path.WriteString(name)
	for _, part := range relative {
		if part.Index {
			path.WriteByte('[')
			path.WriteString(part.Value)
			path.WriteByte(']')
		} else if part.MapKey || strings.ContainsAny(part.Value, ".[]/\"") {
			path.WriteByte('[')
			path.WriteString(strconv.Quote(part.Value))
			path.WriteByte(']')
		} else {
			path.WriteByte('.')
			path.WriteString(part.Value)
		}
	}

	return path.String()
}

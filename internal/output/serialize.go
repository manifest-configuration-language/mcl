package output

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	values "github.com/manifest-configuration-language/mcl/internal/value"
	"gopkg.in/yaml.v3"
)

var jsonNumberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// RenderArtifacts encodes finalized artifacts as separate YAML documents.
func RenderArtifacts(artifacts []values.Artifact) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if len(artifacts) == 0 {
		return []byte{}, nil
	}
	for _, artifact := range artifacts {
		node, err := YAMLNode(artifact.Value)
		if err != nil {
			return nil, err
		}
		if err := encoder.Encode(node); err != nil {
			return nil, err
		}
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("encode YAML: %w", err)
	}

	return output.Bytes(), nil
}

// RenderArtifactsJSON encodes finalized artifacts as a formatted JSON array.
func RenderArtifactsJSON(artifacts []values.Artifact) ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('[')
	for index, artifact := range artifacts {
		if index > 0 {
			output.WriteByte(',')
		}
		output.WriteByte('\n')
		output.WriteString("  ")
		if err := AppendJSONValue(&output, artifact.Value); err != nil {
			return nil, err
		}
	}
	if len(artifacts) > 0 {
		output.WriteByte('\n')
	}
	output.WriteByte(']')
	formatted := jsontext.Value(output.Bytes())
	if err := formatted.Indent(jsontext.WithIndent("  ")); err != nil {
		return nil, fmt.Errorf("format JSON output: %w", err)
	}

	return append(formatted, '\n'), nil
}

// AppendJSONValue appends the JSON encoding of one finalized value to output.
func AppendJSONValue(output *bytes.Buffer, value values.Value) error {
	value = nodeContents(value)
	switch value.Kind {
	case values.Null:
		output.WriteString("null")
	case values.Bool:
		output.WriteString(strconv.FormatBool(value.Bool))
	case values.Integer, values.Decimal:
		if !jsonNumberPattern.MatchString(value.Text) {
			return fmt.Errorf("cannot render invalid JSON number %q", value.Text)
		}
		output.WriteString(value.Text)
	case values.String:
		encoded, err := json.Marshal(value.Text)
		if err != nil {
			return fmt.Errorf("encode JSON string: %w", err)
		}
		output.Write(encoded)
	case values.List, values.AdapterBundle:
		output.WriteByte('[')
		for index, item := range value.List {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := AppendJSONValue(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case values.Map:
		output.WriteByte('{')
		for index, entry := range value.Map {
			if index > 0 {
				output.WriteByte(',')
			}
			key, err := json.Marshal(entry.Key)
			if err != nil {
				return fmt.Errorf("encode JSON object key: %w", err)
			}
			output.Write(key)
			output.WriteByte(':')
			if err := AppendJSONValue(output, entry.Value); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	case values.Rule:
		return errors.New("rule values cannot be emitted")
	default:
		return fmt.Errorf("cannot render unresolved reference %q", value.Text)
	}

	return nil
}

// YAMLNode converts one finalized value into the YAML node used by the encoder.
func YAMLNode(value values.Value) (*yaml.Node, error) {
	value = nodeContents(value)
	switch value.Kind {
	case values.Null:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	case values.Bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(value.Bool)}, nil
	case values.Integer:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: value.Text}, nil
	case values.Decimal:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: value.Text}, nil
	case values.String:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value.Text, Style: yaml.DoubleQuotedStyle}, nil
	case values.List, values.AdapterBundle:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range value.List {
			child, err := YAMLNode(item)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, child)
		}

		return node, nil
	case values.Map:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, entry := range value.Map {
			key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: entry.Key}
			child, err := YAMLNode(entry.Value)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, key, child)
		}

		return node, nil
	case values.Rule:
		return nil, errors.New("rule values cannot be emitted")
	default:
		return nil, fmt.Errorf("cannot render unresolved reference %q", value.Text)
	}
}

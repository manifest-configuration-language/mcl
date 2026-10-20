package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func decodeRuntimeYAMLDocuments(source []byte, sourcePath string, allocationBudgets ...*runtimeAllocationBudget) (RuntimeValue, error) {
	if len(source) > maxYAMLBytes {
		return RuntimeValue{}, fmt.Errorf("YAML input exceeds the %d-byte limit", maxYAMLBytes)
	}
	if len(allocationBudgets) != 0 {
		allocationBudget := allocationBudgets[0]
		if err := allocationBudget.consumeBytes(len(source)); err != nil {
			return RuntimeValue{}, err
		}
		if err := allocationBudget.consume(1); err != nil {
			return RuntimeValue{}, err
		}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	result := RuntimeValue{Value: runtimeValueData{Kind: AdapterBundle, Span: SourceSpan{End: len(source)}, SourcePath: sourcePath}, List: make([]RuntimeValue, 0)}
	var nodeCount int
	for {
		var document yaml.Node
		if err := decoder.Decode(&document); err != nil {
			if errors.Is(err, io.EOF) {
				return result, nil
			}

			return RuntimeValue{}, fmt.Errorf("decode YAML: %w", err)
		}
		if len(document.Content) == 0 {
			continue
		}
		if err := validateYAMLNode(document.Content[0], 0, &nodeCount); err != nil {
			return RuntimeValue{}, err
		}
		var allocationBudget *runtimeAllocationBudget
		if len(allocationBudgets) != 0 {
			allocationBudget = allocationBudgets[0]
		}
		value, err := runtimeYAMLNodeValue(document.Content[0], source, sourcePath, allocationBudget)
		if err != nil {
			return RuntimeValue{}, err
		}
		result.List = append(result.List, value)
	}
}

func runtimeYAMLNodeValue(node *yaml.Node, source []byte, sourcePath string, allocationBudget *runtimeAllocationBudget) (RuntimeValue, error) {
	if err := allocationBudget.consume(1); err != nil {
		return RuntimeValue{}, err
	}
	span := yamlNodeSourceSpan(source, node)
	if err := validateYAMLNodeTag(node); err != nil {
		return RuntimeValue{}, err
	}
	metadata := runtimeValueData{Span: span, SourcePath: sourcePath}
	switch node.Kind {
	case yaml.ScalarNode:
		switch node.Tag {
		case yamlTagNull:
			metadata.Kind = Null
		case yamlTagBool:
			value, err := strconv.ParseBool(strings.ToLower(node.Value))
			if err != nil {
				return RuntimeValue{}, fmt.Errorf("invalid YAML Bool %q: %w", node.Value, err)
			}
			metadata.Kind = Bool
			metadata.Bool = value
		case yamlTagInt:
			if err := validateRuntimeYAMLIntegerText(node.Value); err != nil {
				return RuntimeValue{}, fmt.Errorf("invalid YAML Integer %q: %w", node.Value, err)
			}
			value, ok := new(big.Int).SetString(node.Value, 0)
			if !ok {
				return RuntimeValue{}, fmt.Errorf("invalid YAML Integer %q", node.Value)
			}
			metadata.Kind = Integer
			metadata.Text = value.String()
		case yamlTagFloat:
			value, err := normalizeYAMLDecimal(node.Value)
			if err != nil {
				return RuntimeValue{}, fmt.Errorf("invalid YAML Decimal %q: %w", node.Value, err)
			}
			metadata.Kind = Decimal
			metadata.Text = value
		case yamlTagString:
			metadata.Kind = String
			metadata.Text = node.Value
		default:
			return RuntimeValue{}, unsupportedYAMLTagError(node)
		}

		return RuntimeValue{Value: metadata}, nil
	case yaml.SequenceNode:
		metadata.Kind = List
		value := RuntimeValue{Value: metadata, List: make([]RuntimeValue, 0, len(node.Content))}
		for _, child := range node.Content {
			item, err := runtimeYAMLNodeValue(child, source, sourcePath, allocationBudget)
			if err != nil {
				return RuntimeValue{}, err
			}
			value.List = append(value.List, item)
		}

		return value, nil
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return RuntimeValue{}, errors.New("invalid YAML mapping")
		}
		metadata.Kind = Map
		value := RuntimeValue{Value: metadata, Map: make([]RuntimeEntry, 0, len(node.Content)/2)}
		seen := make(map[string]bool)
		for index := 0; index < len(node.Content); index += 2 {
			keyNode := node.Content[index]
			if err := validateYAMLNodeTag(keyNode); err != nil {
				return RuntimeValue{}, err
			}
			if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != yamlTagString {
				return RuntimeValue{}, errors.New("YAML mapping keys must be Strings")
			}
			if seen[keyNode.Value] {
				return RuntimeValue{}, fmt.Errorf("duplicate YAML key %q", keyNode.Value)
			}
			seen[keyNode.Value] = true
			child, err := runtimeYAMLNodeValue(node.Content[index+1], source, sourcePath, allocationBudget)
			if err != nil {
				return RuntimeValue{}, err
			}
			value.Map = append(value.Map, RuntimeEntry{
				Key: keyNode.Value, KeySpan: yamlNodeSourceSpan(source, keyNode), KeySourcePath: sourcePath, Value: child,
			})
		}

		return value, nil
	default:
		return RuntimeValue{}, fmt.Errorf("unsupported YAML node kind %d", node.Kind)
	}
}

func unsupportedYAMLTagError(node *yaml.Node) error {
	return fmt.Errorf("YAML line %d, column %d: unsupported tag %q", node.Line, node.Column, node.Tag)
}

func validateYAMLNodeTag(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		switch node.Tag {
		case yamlTagNull, yamlTagBool, yamlTagInt, yamlTagFloat, yamlTagString:
			return nil
		}
	case yaml.SequenceNode:
		if node.Tag == "!!seq" {
			return nil
		}
	case yaml.MappingNode:
		if node.Tag == "!!map" {
			return nil
		}
	default:
		return nil
	}

	return unsupportedYAMLTagError(node)
}

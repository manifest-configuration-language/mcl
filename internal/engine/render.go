package engine

import (
	"errors"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

func runtimeValueToYAMLNode(value RuntimeValue) (*yaml.Node, error) {
	if value.IsRuleResult() {
		return nil, errors.New("rule values cannot be emitted")
	}
	value = runtimeNodeContents(value)
	switch value.Kind() {
	case Null:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagNull, Value: schemaTypeNull}, nil
	case Bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagBool, Value: strconv.FormatBool(value.BoolValue())}, nil
	case Integer:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagInt, Value: value.TextValue()}, nil
	case Decimal:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagFloat, Value: value.TextValue()}, nil
	case String:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagString, Value: value.TextValue(), Style: yaml.DoubleQuotedStyle}, nil
	case List, AdapterBundle:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range value.List {
			child, err := runtimeValueToYAMLNode(item)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, child)
		}
		return node, nil
	case Map:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, entry := range value.Map {
			key := &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagString, Value: entry.Key}
			child, err := runtimeValueToYAMLNode(entry.Value)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, key, child)
		}
		return node, nil
	case Rule:
		return nil, errors.New("rule values cannot be emitted")
	default:
		return nil, fmt.Errorf("cannot render unresolved reference %q", value.TextValue())
	}
}

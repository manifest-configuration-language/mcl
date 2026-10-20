package lsp

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/manifest-configuration-language/mcl/internal/engine"
)

type completionParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	} `json:"position"`
}

type completionItem struct {
	Label      string `json:"label"`
	Kind       int    `json:"kind"`
	InsertText string `json:"insertText,omitzero"`
}

func (s *server) completions(params completionParams) []completionItem {
	items := []completionItem{
		{Label: "let", Kind: 14},
		{Label: "emit", Kind: 14},
		{Label: "true", Kind: 14},
		{Label: "false", Kind: 14},
		{Label: "null", Kind: 14},
		{Label: "len", Kind: 3},
		{Label: "contains", Kind: 3},
		{Label: "concat", Kind: 3},
		{Label: "union", Kind: 3},
		{Label: "keyOf", Kind: 3},
		{Label: "replace", Kind: 3},
		{Label: "replaceAt", Kind: 3},
		{Label: "mergeByKey", Kind: 3},
	}
	state, exists := s.documents[params.TextDocument.URI]
	if !exists {
		return items
	}
	lines := strings.Split(state.Text, "\n")
	if params.Position.Line < 0 || params.Position.Line >= len(lines) {
		return items
	}
	runes := []rune(lines[params.Position.Line])
	end := runeIndexAtUTF16(runes, params.Position.Character)
	start := end
	for start > 0 && isIdentifierRune(runes[start-1]) {
		start--
	}
	prefix := string(runes[start:end])
	index, parseErr := analyzeSource(state.Text)
	if parseErr == nil {
		for _, name := range index.visibleDeclarationNames(params.Position.Line + 1) {
			items = append(items, completionItem{Label: name, Kind: 6})
		}
	} else {
		for lineNumber, line := range lines {
			if lineNumber >= params.Position.Line {
				break
			}
			if name, _, ok := declaredName(line); ok {
				items = append(items, completionItem{Label: name, Kind: 6})
			}
		}
	}
	if offset, err := offsetAtUTF16(state.Text, params.Position); err == nil {
		var bindings []localBinding
		if parseErr != nil {
			bindings = partialLocalBindings(state.Text, offset)
		} else {
			bindings = index.visibleLocalBindings(offset)
		}
		for _, binding := range bindings {
			items = append(items, completionItem{Label: binding.name, Kind: 6})
		}
	}
	if fields := schemaFieldCompletions(params, lines, runes, end); len(fields) != 0 {
		items = appendFieldCompletions(items, fields)
	}
	if fields := helmSchemaFieldCompletions(params, lines, runes, end); len(fields) != 0 {
		items = appendFieldCompletions(items, fields)
	}
	if fields := kubernetesSchemaFieldCompletions(params, lines, runes, end); len(fields) != 0 {
		items = appendFieldCompletions(items, fields)
	}
	filtered := make([]completionItem, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.Label, prefix) && !seen[item.Label] {
			filtered = append(filtered, item)
			seen[item.Label] = true
		}
	}

	return filtered
}

func schemaFieldCompletions(params completionParams, lines []string, runes []rune, cursor int) []string {
	line := lines[params.Position.Line]
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent == 0 || strings.Contains(string(runes[:cursor]), ":") {
		return nil
	}
	path := documentPath(params.TextDocument.URI)
	if path == "" {
		return nil
	}
	const placeholder = "__mcl_completion__"
	candidateLines := slices.Clone(lines[:params.Position.Line+1])
	candidateLines[len(candidateLines)-1] = strings.Repeat(" ", indent) + placeholder + ": null"

	return engine.SchemaFieldCompletions(path, strings.Join(candidateLines, "\n"), placeholder)
}

func helmSchemaFieldCompletions(params completionParams, lines []string, runes []rune, cursor int) []string {
	line := lines[params.Position.Line]
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent == 0 || strings.Contains(string(runes[:cursor]), ":") {
		return nil
	}
	path := documentPath(params.TextDocument.URI)
	if path == "" {
		return nil
	}
	const placeholder = "__mcl_helm_completion__"
	candidateLines := slices.Clone(lines)
	candidateLines[params.Position.Line] = strings.Repeat(" ", indent) + placeholder + ": null"

	return engine.HelmSchemaFieldCompletions(path, strings.Join(candidateLines, "\n"), placeholder)
}

func kubernetesSchemaFieldCompletions(params completionParams, lines []string, runes []rune, cursor int) []string {
	line := lines[params.Position.Line]
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent == 0 || strings.Contains(string(runes[:cursor]), ":") {
		return nil
	}
	const placeholder = "__mcl_kubernetes_completion__"
	candidateLines := slices.Clone(lines[:params.Position.Line+1])
	candidateLines[len(candidateLines)-1] = strings.Repeat(" ", indent) + placeholder + ": null"

	return engine.KubernetesSchemaFieldCompletions(strings.Join(candidateLines, "\n"), placeholder)
}

func appendFieldCompletions(items []completionItem, fields []string) []completionItem {
	existing := make(map[string]struct{}, len(items))
	for _, item := range items {
		existing[item.Label] = struct{}{}
	}
	for _, field := range fields {
		if _, ok := existing[field]; ok {
			continue
		}
		item := completionItem{Label: field, Kind: 10}
		if !isMCLIdentifier(field) {
			item.InsertText = strconv.Quote(field)
		}
		items = append(items, item)
		existing[field] = struct{}{}
	}

	return items
}

func isMCLIdentifier(text string) bool {
	if text == "" || !isIdentifierRune([]rune(text)[0]) || text[0] >= '0' && text[0] <= '9' {
		return false
	}
	for _, char := range text {
		if !isIdentifierRune(char) {
			return false
		}
	}

	return true
}

func isQualifiedMCLIdentifier(text string) bool {
	for part := range strings.SplitSeq(text, ".") {
		if !isMCLIdentifier(part) {
			return false
		}
	}

	return text != ""
}

func runeIndexAtUTF16(runes []rune, character int) int {
	units := 0
	for index, char := range runes {
		if units+utf16.RuneLen(char) > character {
			return index
		}
		units += utf16.RuneLen(char)
		if units == character {
			return index + 1
		}
	}

	return len(runes)
}

func isIdentifierRune(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_'
}

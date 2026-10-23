package lsp

import (
	"io/fs"
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

type completionItem = CompletionItem

// CompletionItem is a completion candidate returned by the reusable completion API.
type CompletionItem struct {
	Label      string `json:"label"`
	Kind       int    `json:"kind"`
	InsertText string `json:"insertText,omitzero"`
}

// CompletionParams identifies a source position using zero-based UTF-16 coordinates.
type CompletionParams struct {
	ProjectRoot string
	Entrypoint  string
	Source      string
	Line        int
	Character   int
}

// CompletionsForProjectFS returns MCL completion candidates using files from projectFiles.
func CompletionsForProjectFS(params CompletionParams, projectFiles fs.FS) []CompletionItem {
	return complete(params.Source, location{Line: params.Line, Character: params.Character}, params.ProjectRoot, params.Entrypoint, projectFiles, "")
}

func (s *server) completions(params completionParams) []completionItem {
	state, exists := s.documents[params.TextDocument.URI]
	if !exists {
		return complete("", location{Line: params.Position.Line, Character: params.Position.Character}, "", "", nil, "")
	}
	return complete(state.Text, location{Line: params.Position.Line, Character: params.Position.Character}, "", "", nil, documentPath(params.TextDocument.URI))
}

func complete(source string, position location, projectRoot, entrypoint string, projectFiles fs.FS, sourcePath string) []CompletionItem {
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
	lines := strings.Split(source, "\n")
	if position.Line < 0 || position.Line >= len(lines) {
		return items
	}
	runes := []rune(lines[position.Line])
	end := runeIndexAtUTF16(runes, position.Character)
	start := end
	for start > 0 && isIdentifierRune(runes[start-1]) {
		start--
	}
	prefix := string(runes[start:end])
	index, parseErr := analyzeSource(source)
	if parseErr == nil {
		for _, name := range index.visibleDeclarationNames(position.Line + 1) {
			items = append(items, completionItem{Label: name, Kind: 6})
		}
	} else {
		for lineNumber, line := range lines {
			if lineNumber >= position.Line {
				break
			}
			if name, _, ok := declaredName(line); ok {
				items = append(items, completionItem{Label: name, Kind: 6})
			}
		}
	}
	if offset, err := offsetAtUTF16(source, position); err == nil {
		var bindings []localBinding
		if parseErr != nil {
			bindings = partialLocalBindings(source, offset)
		} else {
			bindings = index.visibleLocalBindings(offset)
		}
		for _, binding := range bindings {
			items = append(items, completionItem{Label: binding.name, Kind: 6})
		}
	}
	if fields := schemaFieldCompletions(position, lines, runes, end, projectRoot, entrypoint, projectFiles, sourcePath); len(fields) != 0 {
		items = appendFieldCompletions(items, fields)
	}
	if fields := helmSchemaFieldCompletions(position, lines, runes, end, projectRoot, entrypoint, projectFiles, sourcePath); len(fields) != 0 {
		items = appendFieldCompletions(items, fields)
	}
	if fields := kubernetesSchemaFieldCompletions(position, lines, runes, end); len(fields) != 0 {
		items = appendFieldCompletions(items, fields)
	}
	filtered := make([]CompletionItem, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.Label, prefix) && !seen[item.Label] {
			filtered = append(filtered, CompletionItem(item))
			seen[item.Label] = true
		}
	}

	return filtered
}

func schemaFieldCompletions(position location, lines []string, runes []rune, cursor int, projectRoot, entrypoint string, projectFiles fs.FS, sourcePath string) []string {
	line := lines[position.Line]
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent == 0 || strings.Contains(string(runes[:cursor]), ":") {
		return nil
	}
	const placeholder = "__mcl_completion__"
	candidateLines := slices.Clone(lines[:position.Line+1])
	candidateLines[len(candidateLines)-1] = strings.Repeat(" ", indent) + placeholder + ": null"
	if projectFiles != nil {
		fields, _ := engine.SchemaFieldCompletionsWithErrorForProjectFS(projectRoot, entrypoint, strings.Join(candidateLines, "\n"), placeholder, projectFiles)
		return fields
	}
	if sourcePath != "" {
		return engine.SchemaFieldCompletions(sourcePath, strings.Join(candidateLines, "\n"), placeholder)
	}
	return nil
}

func helmSchemaFieldCompletions(position location, lines []string, runes []rune, cursor int, projectRoot, entrypoint string, projectFiles fs.FS, sourcePath string) []string {
	line := lines[position.Line]
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent == 0 || strings.Contains(string(runes[:cursor]), ":") {
		return nil
	}
	const placeholder = "__mcl_helm_completion__"
	candidateLines := slices.Clone(lines)
	candidateLines[position.Line] = strings.Repeat(" ", indent) + placeholder + ": null"

	if projectFiles != nil {
		return engine.HelmSchemaFieldCompletionsForProjectFS(projectRoot, entrypoint, strings.Join(candidateLines, "\n"), placeholder, projectFiles)
	}
	if sourcePath != "" {
		return engine.HelmSchemaFieldCompletions(sourcePath, strings.Join(candidateLines, "\n"), placeholder)
	}
	return nil
}

func kubernetesSchemaFieldCompletions(position location, lines []string, runes []rune, cursor int) []string {
	line := lines[position.Line]
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent == 0 || strings.Contains(string(runes[:cursor]), ":") {
		return nil
	}
	const placeholder = "__mcl_kubernetes_completion__"
	candidateLines := slices.Clone(lines[:position.Line+1])
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

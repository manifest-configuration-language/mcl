package languageservice

import (
	"context"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/sourceindex"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

// CompletionItem is a completion candidate returned by the reusable completion API.
type CompletionItem struct {
	Label         string `json:"label"`
	Kind          int    `json:"kind"`
	InsertText    string `json:"insertText,omitzero"`
	Detail        string `json:"detail,omitzero"`
	Documentation string `json:"documentation,omitzero"`
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
	return CompletionsForProjectFSWithContext(context.Background(), params, projectFiles)
}

// CompletionsForProjectFSWithContext returns completion candidates using the supplied context.
func CompletionsForProjectFSWithContext(ctx context.Context, params CompletionParams, projectFiles fs.FS) []CompletionItem {
	return complete(ctx, params.Source, Position{Line: params.Line, Character: params.Character}, params.ProjectRoot, params.Entrypoint, projectFiles, "")
}

// CompletionForSource returns completion candidates for a document backed by the host filesystem.
func CompletionForSource(source string, line, character int, sourcePath string) []CompletionItem {
	return CompletionForSourceWithContext(context.Background(), source, line, character, sourcePath)
}

// CompletionForSourceWithContext returns completion candidates using the supplied context.
func CompletionForSourceWithContext(ctx context.Context, source string, line, character int, sourcePath string) []CompletionItem {
	return complete(ctx, source, Position{Line: line, Character: character}, "", "", nil, sourcePath)
}

func complete(ctx context.Context, source string, position Position, projectRoot, entrypoint string, projectFiles fs.FS, sourcePath string) []CompletionItem {
	items := []CompletionItem{
		{Label: "let", Kind: 14, Detail: "キーワード", Documentation: "現在のモジュールで値を宣言します。"},
		{Label: "emit", Kind: 14, Detail: "キーワード", Documentation: "値をレンダリング結果へ追加します。"},
		{Label: "true", Kind: 14, Detail: "真偽値"},
		{Label: "false", Kind: 14, Detail: "真偽値"},
		{Label: "null", Kind: 14, Detail: "null値"},
	}
	for _, function := range engine.StandardLibraryFunctions() {
		if function.Module != "std:core" && function.Module != "std:list" && function.Module != "std:map" && function.Module != "std:string" {
			continue
		}
		items = append(items, CompletionItem{
			Label: function.Name, Kind: 3, Detail: "標準関数", Documentation: function.Documentation,
		})
	}
	lines := strings.Split(source, "\n")
	if position.Line < 0 || position.Line >= len(lines) {
		return items
	}
	runes := []rune(lines[position.Line])
	end := RuneIndexAtUTF16(runes, position.Character)
	start := end
	for start > 0 && IsIdentifierRune(runes[start-1]) {
		start--
	}
	prefix := string(runes[start:end])
	index, parseErr := AnalyzeSource(source)
	if parseErr == nil {
		for _, name := range index.VisibleDeclarationNames(position.Line + 1) {
			items = append(items, CompletionItem{Label: name, Kind: 6, Detail: "モジュール宣言"})
		}
	} else {
		for lineNumber, line := range lines {
			if lineNumber >= position.Line {
				break
			}
			if name, _, ok := DeclaredName(line); ok {
				items = append(items, CompletionItem{Label: name, Kind: 6, Detail: "モジュール宣言"})
			}
		}
	}
	if namedArguments := namedArgumentCompletions(source, position, start); len(namedArguments) != 0 {
		items = append(namedArguments, items...)
	}
	if offset, err := offsetAtUTF16(source, position); err == nil {
		var bindings []LocalBinding
		if parseErr != nil {
			bindings = PartialLocalBindings(source, offset)
		} else {
			bindings = index.VisibleLocalBindings(offset)
		}
		for _, binding := range bindings {
			items = append(items, CompletionItem{Label: binding.Name, Kind: 6, Detail: "ローカル変数"})
		}
	}
	if fields := schemaFieldCompletions(ctx, position, lines, runes, end, projectRoot, entrypoint, projectFiles, sourcePath); len(fields) != 0 {
		items = appendFieldCompletions(items, fields)
	}
	if fields := helmSchemaFieldCompletions(ctx, position, lines, runes, end, projectRoot, entrypoint, projectFiles, sourcePath); len(fields) != 0 {
		items = appendFieldCompletions(items, fields)
	}
	if fields := kubernetesSchemaFieldCompletions(position, lines, runes, end); len(fields) != 0 {
		items = appendFieldCompletions(items, fields)
	}
	filtered := make([]CompletionItem, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.Label, prefix) && !seen[item.Label] {
			filtered = append(filtered, item)
			seen[item.Label] = true
		}
	}

	return filtered
}

func namedArgumentCompletions(source string, position Position, prefixStart int) []CompletionItem {
	lines := strings.Split(source, "\n")
	if position.Line < 0 || position.Line >= len(lines) {
		return nil
	}
	runes := []rune(lines[position.Line])
	startCharacter := len(utf16.Encode(runes[:prefixStart]))
	startOffset, err := offsetAtUTF16(source, Position{Line: position.Line, Character: startCharacter})
	if err != nil {
		return nil
	}
	cursor := RuneIndexAtUTF16(runes, position.Character)
	tokenEnd := cursor
	for tokenEnd < len(runes) && IsIdentifierRune(runes[tokenEnd]) {
		tokenEnd++
	}
	endCharacter := len(utf16.Encode(runes[:tokenEnd]))
	endOffset, err := offsetAtUTF16(source, Position{Line: position.Line, Character: endCharacter})
	if err != nil || startOffset > endOffset {
		return nil
	}
	following := strings.TrimLeft(source[endOffset:], " \t\r\n")
	nameContext := strings.HasPrefix(following, "=")
	if !nameContext {
		before := strings.TrimRight(source[:startOffset], " \t\r\n")
		after := strings.TrimLeft(source[endOffset:], " \t\r\n")
		nameContext = len(before) > 0 && (before[len(before)-1] == '(' || before[len(before)-1] == ',') && len(after) > 0 && (after[0] == ')' || after[0] == ',')
	}
	if !nameContext {
		return nil
	}
	const placeholder = "__mcl_named_argument_completion__"
	placeholderArgument := placeholder
	if !strings.HasPrefix(following, "=") {
		placeholderArgument += " = null"
	}
	candidate := source[:startOffset] + placeholderArgument + source[endOffset:]
	index, err := AnalyzeSource(candidate)
	if err != nil {
		return nil
	}
	cursorOffset := startOffset + len(placeholder)
	var selected *syntax.CallExpr
	VisitModuleExpressions(index.Module, func(expression syntax.Expr) {
		call, ok := expression.(*syntax.CallExpr)
		if !ok || !spanContains(call.ExprSpan(), cursorOffset) {
			return
		}
		if selected == nil || call.ExprSpan().End-call.ExprSpan().Start < selected.ExprSpan().End-selected.ExprSpan().Start {
			selected = call
		}
	})
	if selected == nil {
		return nil
	}
	callee, ok := selected.Callee.(*syntax.ReferenceExpr)
	if !ok || index.LocalAt(callee.Name, callee.ExprSpan().Start).Found {
		return nil
	}
	var function *syntax.FunctionDeclaration
	for _, node := range index.Module.Nodes {
		declaration, ok := node.(*syntax.FunctionDeclaration)
		if !ok || declaration.Function == nil || declaration.Name != callee.Name {
			continue
		}
		if function != nil {
			return nil
		}
		function = declaration
	}
	if function == nil {
		return nil
	}
	currentArgument := -1
	for argumentIndex, argument := range selected.Arguments {
		if argumentIndex < len(selected.ArgumentNameSpans) && spanContains(selected.ArgumentNameSpans[argumentIndex], cursorOffset) || spanContains(argument.ExprSpan(), cursorOffset) {
			currentArgument = argumentIndex

			break
		}
	}
	used := make(map[string]bool)
	positionals := 0
	for argumentIndex, name := range selected.ArgumentNames {
		if argumentIndex == currentArgument {
			if name == "" {
				positionals++
			}

			continue
		}
		if name == "" {
			positionals++
		} else {
			used[name] = true
		}
	}
	for parameterIndex := 0; parameterIndex < positionals && parameterIndex < len(function.Function.Parameters); parameterIndex++ {
		used[function.Function.Parameters[parameterIndex].Name] = true
	}
	var items []CompletionItem
	for _, parameter := range function.Function.Parameters {
		if used[parameter.Name] {
			continue
		}
		items = append(items, CompletionItem{Label: parameter.Name, Kind: 5, InsertText: parameter.Name + " = ", Detail: "名前付き引数"})
	}

	return items
}

func schemaFieldCompletions(ctx context.Context, position Position, lines []string, runes []rune, cursor int, projectRoot, entrypoint string, projectFiles fs.FS, sourcePath string) []string {
	line := lines[position.Line]
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent == 0 || strings.Contains(string(runes[:cursor]), ":") {
		return nil
	}
	const placeholder = "__mcl_completion__"
	candidateLines := slices.Clone(lines[:position.Line+1])
	candidateLines[len(candidateLines)-1] = strings.Repeat(" ", indent) + placeholder + ": null"
	if projectFiles != nil {
		fields, _ := engine.SchemaFieldCompletionsWithErrorForProjectFSWithContext(ctx, projectRoot, entrypoint, strings.Join(candidateLines, "\n"), placeholder, projectFiles)

		return fields
	}
	if sourcePath != "" {
		fields, _ := engine.SchemaFieldCompletionsWithContext(ctx, sourcePath, strings.Join(candidateLines, "\n"), placeholder)

		return fields
	}

	return nil
}

func helmSchemaFieldCompletions(ctx context.Context, position Position, lines []string, runes []rune, cursor int, projectRoot, entrypoint string, projectFiles fs.FS, sourcePath string) []string {
	line := lines[position.Line]
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent == 0 || strings.Contains(string(runes[:cursor]), ":") {
		return nil
	}
	const placeholder = "__mcl_helm_completion__"
	candidateLines := slices.Clone(lines)
	candidateLines[position.Line] = strings.Repeat(" ", indent) + placeholder + ": null"

	if projectFiles != nil {
		return engine.HelmSchemaFieldCompletionsForProjectFSWithContext(ctx, projectRoot, entrypoint, strings.Join(candidateLines, "\n"), placeholder, projectFiles)
	}
	if sourcePath != "" {
		return engine.HelmSchemaFieldCompletionsWithContext(ctx, sourcePath, strings.Join(candidateLines, "\n"), placeholder)
	}

	return nil
}

func kubernetesSchemaFieldCompletions(position Position, lines []string, runes []rune, cursor int) []string {
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

func appendFieldCompletions(items []CompletionItem, fields []string) []CompletionItem {
	existing := make(map[string]struct{}, len(items))
	for _, item := range items {
		existing[item.Label] = struct{}{}
	}
	for _, field := range fields {
		if _, ok := existing[field]; ok {
			continue
		}
		item := CompletionItem{Label: field, Kind: 10, Detail: "スキーマフィールド"}
		if !IsMCLIdentifier(field) {
			item.InsertText = strconv.Quote(field)
		}
		items = append(items, item)
		existing[field] = struct{}{}
	}

	return items
}

func offsetAtUTF16(source string, position Position) (int, error) {
	return sourceindex.New(source).OffsetAt(sourceindex.Position{Line: position.Line, Character: position.Character})
}

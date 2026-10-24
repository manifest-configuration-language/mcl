package languageservice

import (
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

func QualifiedTypeReferences(module syntax.ModuleAST) []QualifiedReference {
	var result []QualifiedReference
	var visit func(syntax.TypeExpr)
	visit = func(typeExpr syntax.TypeExpr) {
		if alias, member, ok := strings.CutLast(typeExpr.Name, "."); ok && alias != "" && member != "" && typeExpr.Span.End-typeExpr.Span.Start >= len(typeExpr.Name) {
			memberOffset := typeExpr.Span.Start + len(alias) + 1
			result = append(result, QualifiedReference{
				Alias:  alias,
				Member: member,
				Span:   syntax.SourceSpan{Start: memberOffset, End: memberOffset + len(member)},
			})
		}
		for _, child := range typeExpr.Arguments {
			visit(child)
		}
		for _, child := range typeExpr.Options {
			visit(child)
		}
		for _, field := range typeExpr.Fields {
			visit(field.Type)
		}
		for _, parameter := range typeExpr.Parameters {
			visit(parameter.Type)
		}
		if typeExpr.Element != nil {
			visit(*typeExpr.Element)
		}
		if typeExpr.ReturnType != nil {
			visit(*typeExpr.ReturnType)
		}
	}
	visitPointer := func(typeExpr *syntax.TypeExpr) {
		if typeExpr != nil {
			visit(*typeExpr)
		}
	}
	for _, node := range module.Nodes {
		switch declaration := node.(type) {
		case *syntax.TypeDeclaration:
			if declaration.TypeSpec != nil {
				visit(*declaration.TypeSpec)
			}
		case *syntax.LetDeclaration:
			visitPointer(declaration.Annotation)
		case *syntax.FunctionDeclaration:
			if declaration.Function == nil {
				continue
			}
			for _, parameter := range declaration.Function.Parameters {
				visit(parameter.Type)
			}
			visitPointer(declaration.Function.ReturnType)
		case *syntax.RuleDeclaration:
			if declaration.Rule == nil {
				continue
			}
			for _, parameter := range declaration.Rule.Parameters {
				visit(parameter.Type)
			}
		case *syntax.ValidateStatement:
			visitPointer(declaration.QueryType)
		}
	}

	return result
}

type SourceReference struct {
	Name     string
	Span     syntax.SourceSpan
	TypeName bool
}

type QualifiedReference struct {
	Alias  string
	Member string
	Span   syntax.SourceSpan
}

func QualifiedSourceReferences(module syntax.ModuleAST) []QualifiedReference {
	var result []QualifiedReference
	VisitModuleExpressions(module, func(expression syntax.Expr) {
		var text string
		var span syntax.SourceSpan
		switch expression := expression.(type) {
		case *syntax.ReferenceExpr:
			text, span = expression.Name, expression.ExprSpan()
		case *syntax.OptionalReferenceExpr:
			text, span = expression.Path, expression.ExprSpan()
		default:
			return
		}
		parts := strings.FieldsFunc(text, func(r rune) bool { return r == '.' || r == '?' })
		if len(parts) < 2 || span.End-span.Start < len(text) {
			return
		}
		separator := strings.Index(text, "?.")
		separatorSize := 2
		if separator < 0 {
			separator = strings.IndexByte(text, '.')
			separatorSize = 1
		}
		memberOffset := separator + separatorSize
		if separator >= 0 && strings.HasPrefix(text[memberOffset:], parts[1]) {
			result = append(result, QualifiedReference{Alias: parts[0], Member: parts[1], Span: syntax.SourceSpan{Start: span.Start + memberOffset, End: span.Start + memberOffset + len(parts[1])}})
		}
	})

	return result
}

func SourceReferences(module syntax.ModuleAST) []SourceReference {
	var references []SourceReference
	VisitModuleExpressions(module, func(expression syntax.Expr) {
		CollectExprSourceReferences(expression, &references)
	})
	var collectType func(syntax.TypeExpr)
	collectType = func(typeExpr syntax.TypeExpr) {
		if IsQualifiedMCLIdentifier(typeExpr.Name) && typeExpr.Span.End-typeExpr.Span.Start >= len(typeExpr.Name) {
			references = append(references, SourceReference{Name: typeExpr.Name, Span: syntax.SourceSpan{Start: typeExpr.Span.Start, End: typeExpr.Span.Start + len(typeExpr.Name)}, TypeName: true})
		}
		for _, argument := range typeExpr.Arguments {
			collectType(argument)
		}
		for _, option := range typeExpr.Options {
			collectType(option)
		}
		for _, field := range typeExpr.Fields {
			collectType(field.Type)
		}
		if typeExpr.Element != nil {
			collectType(*typeExpr.Element)
		}
		for _, parameter := range typeExpr.Parameters {
			collectType(parameter.Type)
		}
		if typeExpr.ReturnType != nil {
			collectType(*typeExpr.ReturnType)
		}
	}
	for _, node := range module.Nodes {
		switch declaration := node.(type) {
		case *syntax.TypeDeclaration:
			if declaration.TypeSpec != nil {
				collectType(*declaration.TypeSpec)
			}
		case *syntax.LetDeclaration:
			if declaration.Annotation != nil {
				collectType(*declaration.Annotation)
			}
		case *syntax.FunctionDeclaration:
			if declaration.Function != nil {
				for _, parameter := range declaration.Function.Parameters {
					collectType(parameter.Type)
				}
				if declaration.Function.ReturnType != nil {
					collectType(*declaration.Function.ReturnType)
				}
			}
		case *syntax.RuleDeclaration:
			if declaration.Rule != nil {
				for _, parameter := range declaration.Rule.Parameters {
					collectType(parameter.Type)
				}
			}
		case *syntax.ValidateStatement:
			if declaration.QueryType != nil {
				collectType(*declaration.QueryType)
			}
			if declaration.QuerySchema != nil {
				collectType(*declaration.QuerySchema)
			}
		}
	}

	return references
}

func VisitModuleExpressions(module syntax.ModuleAST, visit func(syntax.Expr)) {
	visitRoot := func(expression syntax.Expr) {
		if expression != nil {
			syntax.WalkExpr(expression, visit)
		}
	}
	for _, node := range module.Nodes {
		switch node := node.(type) {
		case *syntax.LetDeclaration:
			visitRoot(node.Expression)
		case *syntax.FunctionDeclaration:
			if node.Function != nil {
				visitRoot(node.Function.Expression)
			}
		case *syntax.RuleDeclaration:
			if node.Rule != nil {
				for _, requirement := range node.Rule.Requirements {
					visitRoot(requirement.Expression)
				}
			}
		case *syntax.EmitStatement:
			visitRoot(node.Expression)
		case *syntax.ValidateStatement:
			visitRoot(node.Expression)
			visitRoot(node.RuleExpr)
			visitRoot(node.QueryWhere)
		}
	}
}

func CollectExprSourceReferences(expression syntax.Expr, references *[]SourceReference) {
	switch expression := expression.(type) {
	case *syntax.ReferenceExpr:
		name := expression.Name
		if index := strings.Index(name, "?."); index >= 0 {
			name = name[:index]
		} else if index := strings.IndexByte(name, '.'); index >= 0 {
			name = name[:index]
		}
		span := expression.ExprSpan()
		if IsMCLIdentifier(name) && span.End-span.Start >= len(name) {
			*references = append(*references, SourceReference{Name: name, Span: span})
		}
	case *syntax.OptionalReferenceExpr:
		name := expression.Path
		if index := strings.Index(name, "?."); index >= 0 {
			name = name[:index]
		} else if index := strings.IndexByte(name, '.'); index >= 0 {
			name = name[:index]
		}
		span := expression.ExprSpan()
		if IsMCLIdentifier(name) && span.End-span.Start >= len(name) {
			*references = append(*references, SourceReference{Name: name, Span: span})
		}
	}
}

func TopLevelTypeDefinition(module syntax.ModuleAST, name string) int {
	definition := -1
	for index, node := range module.Nodes {
		declaration, ok := node.(*syntax.TypeDeclaration)
		if !ok || declaration.Name != name {
			continue
		}
		if definition >= 0 {
			return -1
		}
		definition = index
	}

	return definition
}

func IsTypeDeclaration(node syntax.ASTNode) bool {
	_, ok := node.(*syntax.TypeDeclaration)

	return ok
}

func IsRenameableASTDeclaration(node syntax.ASTNode) bool {
	switch node.(type) {
	case *syntax.LetDeclaration, *syntax.FunctionDeclaration, *syntax.RuleDeclaration, *syntax.TypeDeclaration:
		return true
	default:
		return false
	}
}

func ASTDeclarationExported(node syntax.ASTNode) bool {
	switch declaration := node.(type) {
	case *syntax.LetDeclaration:
		return declaration.Exported
	case *syntax.FunctionDeclaration:
		return declaration.Exported
	case *syntax.RuleDeclaration:
		return declaration.Exported
	case *syntax.TypeDeclaration:
		return declaration.Exported
	default:
		return false
	}
}

type ScopedLocalBinding struct {
	Name        string
	Declaration syntax.SourceSpan
	Scope       syntax.SourceSpan
}

type LocalBindingMatch struct {
	Binding ScopedLocalBinding
	Found   bool
}

func ScopedLocalBindings(module syntax.ModuleAST) []ScopedLocalBinding {
	var bindings []ScopedLocalBinding
	add := func(name string, declaration, scope syntax.SourceSpan) {
		if name == "" || declaration.End <= declaration.Start || scope.End <= scope.Start {
			return
		}
		bindings = append(bindings, ScopedLocalBinding{Name: name, Declaration: declaration, Scope: scope})
	}
	for _, node := range module.Nodes {
		switch node := node.(type) {
		case *syntax.FunctionDeclaration:
			if node.Function != nil {
				for _, parameter := range node.Function.Parameters {
					add(parameter.Name, parameter.Span, node.Function.BodySpan())
				}
				CollectLocalExprScopes(node.Function.Expression, add)
			}
		case *syntax.LetDeclaration:
			if node.Expression != nil {
				CollectLocalExprScopes(node.Expression, add)
			}
		case *syntax.EmitStatement:
			if node.Expression != nil {
				CollectLocalExprScopes(node.Expression, add)
			}
		case *syntax.ValidateStatement:
			if node.Expression != nil {
				CollectLocalExprScopes(node.Expression, add)
			}
			CollectLocalExprScopes(node.RuleExpr, add)
			if node.QueryWhere != nil {
				CollectLocalExprScopes(node.QueryWhere, add)
			}
		case *syntax.RuleDeclaration:
			if node.Rule == nil {
				continue
			}
			for _, requirement := range node.Rule.Requirements {
				for _, parameter := range node.Rule.Parameters {
					add(parameter.Name, parameter.Span, requirement.Span)
				}
				CollectLocalExprScopes(requirement.Expression, add)
			}
		}
	}

	return bindings
}

func LocalBindingAt(bindings []ScopedLocalBinding, name string, offset int) LocalBindingMatch {
	var match LocalBindingMatch
	for _, binding := range bindings {
		inDeclaration := offset >= binding.Declaration.Start && offset <= binding.Declaration.End
		inScope := offset >= binding.Scope.Start && offset <= binding.Scope.End
		if binding.Name != name || !inDeclaration && !inScope {
			continue
		}
		if !match.Found || binding.Scope.End-binding.Scope.Start < match.Binding.Scope.End-match.Binding.Scope.Start {
			match = LocalBindingMatch{Binding: binding, Found: true}
		}
	}

	return match
}

func CollectLocalExprScopes(expression syntax.Expr, add func(string, syntax.SourceSpan, syntax.SourceSpan)) {
	syntax.WalkExpr(expression, func(expression syntax.Expr) {
		switch expression := expression.(type) {
		case *syntax.ForExpr:
			add(expression.Variable, expression.VariableSpan, expression.Body.ExprSpan())
		case *syntax.LambdaExpr:
			for index, parameter := range expression.Parameters {
				if index < len(expression.ParameterSpans) {
					add(parameter, expression.ParameterSpans[index], expression.Body.ExprSpan())
				}
			}
		}
	})
}

type LocalBinding struct {
	Name        string
	Declaration syntax.SourceSpan
}

func ASTDeclarationInfo(node syntax.ASTNode) (string, int, syntax.SourceSpan, bool) {
	switch declaration := node.(type) {
	case *syntax.ImportDeclaration:
		return declaration.Name, declaration.Line, declaration.NameSpan, true
	case *syntax.TypeDeclaration:
		return declaration.Name, declaration.Line, declaration.NameSpan, true
	case *syntax.RuleDeclaration:
		return declaration.Name, declaration.Line, declaration.NameSpan, true
	case *syntax.FunctionDeclaration:
		return declaration.Name, declaration.Line, declaration.NameSpan, true
	case *syntax.LetDeclaration:
		return declaration.Name, declaration.Line, declaration.NameSpan, true
	default:
		return "", 0, syntax.SourceSpan{}, false
	}
}

func PartialLocalBindings(source string, cursorOffset int) []LocalBinding {
	if cursorOffset < 0 || cursorOffset > len(source) {
		return nil
	}
	prefix := source[:cursorOffset]
	if index, err := AnalyzeSource(prefix); err == nil {
		return index.VisibleLocalBindings(cursorOffset)
	}
	tokens, err := syntax.Lex(prefix)
	if err != nil {
		return nil
	}
	if bindings := PartialLambdaLocalBindings(prefix, tokens); len(bindings) != 0 {
		return bindings
	}
	closers, ok := MissingDelimiterClosers(prefix, tokens)
	if !ok {
		return nil
	}
	separator := " "
	if prefix == "" || strings.HasSuffix(prefix, "\n") || strings.HasSuffix(prefix, "\r") || strings.HasSuffix(prefix, "\t") || strings.HasSuffix(prefix, " ") {
		separator = ""
	}
	for _, ending := range []string{separator + "null", "\nnull"} {
		candidate := prefix + ending + closers
		if index, parseErr := AnalyzeSource(candidate); parseErr == nil {
			return index.VisibleLocalBindings(cursorOffset)
		}
	}

	return nil
}

func PartialLambdaLocalBindings(prefix string, tokens []syntax.Token) []LocalBinding {
	for _, token := range tokens {
		if token.Kind != syntax.TokenLexeme || prefix[token.Lexeme.Start:token.Lexeme.End] != "fn" {
			continue
		}
		lambdaStart := token.Lexeme.Start
		fragment := prefix[lambdaStart:]
		fragmentTokens, err := syntax.Lex(fragment)
		if err != nil {
			continue
		}
		closers, ok := MissingDelimiterClosers(fragment, fragmentTokens)
		if !ok {
			continue
		}
		separator := " "
		if strings.HasSuffix(fragment, "\n") || strings.HasSuffix(fragment, "\r") || strings.HasSuffix(fragment, "\t") || strings.HasSuffix(fragment, " ") {
			separator = ""
		}
		for _, ending := range []string{"", separator + "null", "\nnull"} {
			declaration := "let __mcl_partial = "
			candidate := declaration + fragment + ending + closers
			module, parseErr := syntax.ParseAST(candidate)
			if parseErr != nil {
				continue
			}
			index := AnalyzeModule(module)
			syntheticCursor := len(declaration) + len(fragment)
			bindings := index.VisibleLocalBindings(syntheticCursor)
			for bindingIndex := range bindings {
				bindings[bindingIndex].Declaration.Start += lambdaStart - len(declaration)
				bindings[bindingIndex].Declaration.End += lambdaStart - len(declaration)
			}
			if len(bindings) != 0 {
				return bindings
			}
		}
	}

	return nil
}

func MissingDelimiterClosers(source string, tokens []syntax.Token) (string, bool) {
	openers := make([]string, 0)
	for _, token := range tokens {
		if token.Kind != syntax.TokenLexeme {
			continue
		}
		lexeme := source[token.Lexeme.Start:token.Lexeme.End]
		switch lexeme {
		case "(", "[", "{":
			openers = append(openers, lexeme)
		case ")", "]", "}":
			if len(openers) == 0 {
				return "", false
			}
			match := openers[len(openers)-1] == "(" && lexeme == ")" || openers[len(openers)-1] == "[" && lexeme == "]" || openers[len(openers)-1] == "{" && lexeme == "}"
			if !match {
				return "", false
			}
			openers = openers[:len(openers)-1]
		}
	}
	closers := make([]byte, 0, len(openers))
	for _, opener := range slices.Backward(openers) {
		switch opener {
		case "(":
			closers = append(closers, ')')
		case "[":
			closers = append(closers, ']')
		case "{":
			closers = append(closers, '}')
		}
	}

	return string(closers), true
}

func DeclaredName(line string) (string, int, bool) {
	tokens, err := syntax.Lex(line)
	if err != nil {
		return "", 0, false
	}
	lexemes := make([]syntax.Token, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind == syntax.TokenLexeme {
			lexemes = append(lexemes, token)
		}
	}
	text := func(index int) string {
		return line[lexemes[index].Lexeme.Start:lexemes[index].Lexeme.End]
	}
	if len(lexemes) == 0 {
		return "", 0, false
	}
	keywordIndex := 0
	if text(0) == "export" {
		keywordIndex++
	}
	if keywordIndex >= len(lexemes) {
		return "", 0, false
	}
	keyword := text(keywordIndex)
	nameIndex := keywordIndex + 1
	if keyword == "import" {
		nameIndex = -1
		for index := keywordIndex + 1; index+1 < len(lexemes); index++ {
			if text(index) == "as" {
				nameIndex = index + 1

				break
			}
		}
	} else if keyword != "let" && keyword != "type" && keyword != "rule" && keyword != "fn" {
		return "", 0, false
	}
	if nameIndex < 0 || nameIndex >= len(lexemes) {
		return "", 0, false
	}
	name := text(nameIndex)
	if !IsMCLIdentifier(name) {
		return "", 0, false
	}
	start := lexemes[nameIndex].Lexeme.Start

	return name, UTF16Length([]rune(line[:start])), true
}

func UTF16Length(runes []rune) int {
	length := 0
	for _, char := range runes {
		length += utf16.RuneLen(char)
	}

	return length
}

func IsMCLIdentifier(text string) bool {
	if text == "" || !IsIdentifierRune([]rune(text)[0]) || text[0] >= '0' && text[0] <= '9' {
		return false
	}
	for _, char := range text {
		if !IsIdentifierRune(char) {
			return false
		}
	}

	return true
}

func IsQualifiedMCLIdentifier(text string) bool {
	for part := range strings.SplitSeq(text, ".") {
		if !IsMCLIdentifier(part) {
			return false
		}
	}

	return text != ""
}

func RuneIndexAtUTF16(runes []rune, character int) int {
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

func IsIdentifierRune(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_'
}

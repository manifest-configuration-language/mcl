package syntax

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"
)

func FormatSource(source string) (string, error) {
	module, tokens, err := ParseASTWithTokens(source)
	if err != nil {
		return "", err
	}
	cst := parseFormatterCST(source, tokens)
	cst.addSyntaxLabels(module)
	formatted := cst.format()
	if _, err := ParseAST(formatted); err != nil {
		return "", fmt.Errorf("formatter produced invalid MCL: %w", err)
	}

	return formatted, nil
}

type formatterNodeKind uint8

const (
	formatterCode formatterNodeKind = iota
	formatterComment
	formatterBlank
	formatterStringContent
)

type formatterNode struct {
	kind        formatterNodeKind
	syntax      []formatterSyntaxLabel
	indent      int
	depth       int
	layoutDepth int
	literals    []LiteralToken
	transitions []TokenKind
	bodySpan    SourceSpan
	lineEnding  SourceSpan
	hasNewline  bool
	children    []*formatterNode
}

type formatterCST struct {
	source            string
	lexemes           []Token
	roots             []*formatterNode
	lines             []*formatterNode
	triviaAttachments map[SourceSpan]formatterTriviaAttachment
}

type tokenSpanLexemeCursor struct {
	tokens []Token
	index  int
	bounds SourceSpan
}

func (cursor *tokenSpanLexemeCursor) next() (Token, bool) {
	for cursor.index < len(cursor.tokens) {
		token := cursor.tokens[cursor.index]
		cursor.index++
		if token.Kind != TokenLexeme || token.Span.End <= cursor.bounds.Start {
			continue
		}
		if token.Span.Start >= cursor.bounds.End {
			return Token{}, false
		}
		if token.Span.Start < cursor.bounds.Start || token.Span.End > cursor.bounds.End {
			continue
		}

		return token, true
	}

	return Token{}, false
}

type formatterTriviaAttachment struct {
	trivia     SourceTrivia
	owner      SourceSpan
	depth      int
	lineEnding SourceSpan
	hasNewline bool
}

type formatterSourceLine struct {
	bodySpan   SourceSpan
	lineEnding SourceSpan
	hasNewline bool
}

type formatterSyntaxCategory uint8

const (
	formatterStatementSyntax formatterSyntaxCategory = iota
	formatterExpressionSyntax
	formatterMapItemSyntax
	formatterListItemSyntax
)

type formatterSyntaxLabel struct {
	category       formatterSyntaxCategory
	kind           string
	span           SourceSpan
	callDelimiters ExprCallDelimiters
	layoutDepth    int
	startsHere     bool
	layoutEligible bool
}

func parseFormatterCST(source string, tokens []Token) formatterCST {
	type lexedLineMetadata struct {
		indent   int
		literals []LiteralToken
	}
	lexedLines := make(map[int]lexedLineMetadata, len(tokens))
	triviaLines := make(map[int]formatterNodeKind)
	layoutTransitions := make(map[int][]TokenKind)
	var pendingTransitions []TokenKind
	for _, token := range tokens {
		switch token.Kind {
		case TokenNewline:
			lineStart := max(token.LineSpan.Start-token.Indent, 0)
			lexedLines[lineStart] = lexedLineMetadata{indent: token.Indent, literals: token.Literals}
			if len(pendingTransitions) > 0 {
				layoutTransitions[lineStart] = pendingTransitions
				pendingTransitions = nil
			}
		case TokenTrivia:
			if token.TriviaOnly {
				lineStart := token.Span.Start
				lexedLines[lineStart] = lexedLineMetadata{indent: formatterTriviaIndent(source, token), literals: token.Literals}
				if strings.TrimSpace(token.TriviaText) == "" {
					triviaLines[lineStart] = formatterBlank
				} else {
					triviaLines[lineStart] = formatterComment
				}
			}
		case TokenIndent, TokenDedent:
			pendingTransitions = append(pendingTransitions, token.Kind)
		case TokenLexeme:
			// 字句は行情報の構築後に別途収集する。
		}
	}
	lexemes := make([]Token, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind == TokenLexeme {
			lexemes = append(lexemes, token)
		}
	}
	lines := formatterSourceLines(source)
	cst := formatterCST{source: source, lexemes: lexemes, roots: make([]*formatterNode, 0, len(lines))}
	for _, line := range lines {
		cst.lines = append(cst.lines, &formatterNode{
			bodySpan:   line.bodySpan,
			lineEnding: line.lineEnding,
			hasNewline: line.hasNewline,
		})
	}
	var codeStack []*formatterNode
	var triviaStack []*formatterNode
	codeDepth := 0
	stringContentThrough := 0
	for index, node := range cst.lines {
		line := cst.nodeText(node)
		trimmed := strings.TrimSpace(line)
		lineNumber := index + 1
		lineStart := node.bodySpan.Start
		if lineNumber <= stringContentThrough {
			node.kind = formatterStringContent
		} else if triviaKind, isTrivia := triviaLines[lineStart]; isTrivia {
			node.kind = triviaKind
			if lexed, exists := lexedLines[lineStart]; exists {
				node.indent = lexed.indent
			}
		} else if lexed, exists := lexedLines[lineStart]; exists {
			node.indent = lexed.indent
			node.literals = lexed.literals
			node.transitions = layoutTransitions[lineStart]
			switch {
			case len(node.literals) > 0 && node.literals[0].Span.Start >= 0 && node.literals[0].Span.End <= len(source) && strings.Contains(source[node.literals[0].Span.Start:node.literals[0].Span.End], "\n"):
				node.kind = formatterCode
				stringContentThrough = lineNumber + strings.Count(source[node.literals[0].Span.Start:node.literals[0].Span.End], "\n")
			default:
				node.kind = formatterCode
			}
		} else if trimmed == "" {
			node.kind = formatterBlank
		} else {
			node.kind = formatterCode
		}
		switch node.kind {
		case formatterCode:
			if len(node.transitions) == 0 {
				for len(codeStack) > 0 && node.indent <= codeStack[len(codeStack)-1].indent {
					codeStack = codeStack[:len(codeStack)-1]
				}
			} else {
				for _, transition := range node.transitions {
					switch transition {
					case TokenIndent:
						codeDepth++
					case TokenDedent:
						if codeDepth > 0 {
							codeDepth--
						}
					case TokenLexeme, TokenNewline, TokenTrivia:
						// レイアウト遷移にはインデントとデデントだけが含まれる。
					}
				}
				if len(codeStack) > codeDepth+1 {
					codeStack = codeStack[:codeDepth+1]
				}
				if codeDepth > len(codeStack) {
					codeDepth = len(codeStack)
				}
			}
		case formatterComment:
			for len(triviaStack) > 0 && node.indent <= triviaStack[len(triviaStack)-1].indent {
				triviaStack = triviaStack[:len(triviaStack)-1]
			}
		default:
			// コードとコメント以外の行はブロックスタックに影響しない。
		}
		if node.kind == formatterCode {
			if len(node.transitions) == 0 {
				node.depth = len(codeStack)
				appendFormatterNode(&cst, codeStack, node)
				codeStack = append(codeStack, node)
			} else {
				node.depth = codeDepth
				appendFormatterNode(&cst, codeStack[:codeDepth], node)
				codeStack = append(codeStack[:codeDepth], node)
			}
			node.layoutDepth = codeDepth
			triviaStack = append(triviaStack[:0], codeStack...)
		} else {
			node.depth = len(triviaStack)
			appendFormatterNode(&cst, triviaStack, node)
		}
	}

	return cst
}

func formatterTriviaIndent(source string, token Token) int {
	start, end := token.Span.Start, token.TriviaOffset
	if end < start {
		end = token.Span.End
	}
	if start < 0 || end < start || end > len(source) {
		return 0
	}

	prefix := source[start:end]

	return len(prefix) - len(strings.TrimLeft(prefix, " "))
}

func formatterSourceLines(source string) []formatterSourceLine {
	lines := make([]formatterSourceLine, 0, strings.Count(source, "\n")+1)
	for start := 0; start <= len(source); {
		lineEnd := len(source)
		endingEnd := len(source)
		hasNewline := false
		if relativeEnd := strings.IndexByte(source[start:], '\n'); relativeEnd >= 0 {
			lineEnd = start + relativeEnd
			endingEnd = lineEnd + 1
			hasNewline = true
		}
		bodyEnd := lineEnd
		if bodyEnd > start && source[bodyEnd-1] == '\r' {
			bodyEnd--
		}
		lines = append(lines, formatterSourceLine{
			bodySpan:   SourceSpan{Start: start, End: bodyEnd},
			lineEnding: SourceSpan{Start: bodyEnd, End: endingEnd},
			hasNewline: hasNewline,
		})
		if !hasNewline {
			break
		}
		start = endingEnd
	}

	return lines
}

func (cst *formatterCST) addSyntaxLabels(module ModuleAST) {
	for _, node := range module.Nodes {
		switch node := node.(type) {
		case *ImportDeclaration:
			cst.addStatementSyntaxLabel(node.Span, "import")
		case *TypeDeclaration:
			cst.addStatementSyntaxLabel(node.Span, "type")
		case *RuleDeclaration:
			cst.addStatementSyntaxLabel(node.Span, "rule")
			if node.Rule != nil {
				for _, requirement := range node.Rule.Requirements {
					cst.addExpressionLabels(requirement.Expression, 0)
				}
			}
		case *FunctionDeclaration:
			cst.addStatementSyntaxLabel(node.Span, "fn")
			if node.Function != nil {
				cst.addExpressionLabels(node.Function.Expression, 0)
			}
		case *LetDeclaration:
			cst.addStatementSyntaxLabel(node.Span, "let")
			cst.addExpressionLabels(node.Expression, 0)
		case *EmitStatement:
			cst.addStatementSyntaxLabel(node.Span, "emit")
			cst.addExpressionLabels(node.Expression, 0)
		case *ValidateStatement:
			cst.addStatementSyntaxLabel(node.Span, "validate")
			cst.addExpressionLabels(node.Expression, 0)
			cst.addExpressionLabels(node.RuleExpr, 0)
			cst.addExpressionLabels(node.QueryWhere, 0)
		}
	}
	cst.assignTriviaOwnershipFromSyntax(module.Trivia)
}

func (cst *formatterCST) assignTriviaOwnershipFromSyntax(sourceTrivia []SourceTrivia) {
	parents := make(map[*formatterNode]*formatterNode, len(cst.lines))
	var rememberParents func(*formatterNode, *formatterNode)
	rememberParents = func(node, parent *formatterNode) {
		parents[node] = parent
		for _, child := range node.children {
			rememberParents(child, node)
		}
	}
	for _, root := range cst.roots {
		rememberParents(root, nil)
	}

	var removeComments func([]*formatterNode) []*formatterNode
	removeComments = func(nodes []*formatterNode) []*formatterNode {
		kept := nodes[:0]
		for _, node := range nodes {
			if node.kind == formatterComment {
				continue
			}
			node.children = removeComments(node.children)
			kept = append(kept, node)
		}

		return kept
	}
	cst.roots = removeComments(cst.roots)
	cst.rebuildCodeHierarchy(parents)
	triviaBySpan := make(map[SourceSpan]SourceTrivia, len(sourceTrivia))
	for _, trivia := range sourceTrivia {
		triviaBySpan[trivia.Span] = trivia
	}
	cst.triviaAttachments = make(map[SourceSpan]formatterTriviaAttachment)

	for _, comment := range cst.lines {
		if comment.kind != formatterComment {
			continue
		}
		trivia, exists := triviaBySpan[comment.bodySpan]
		if !exists {
			trivia = SourceTrivia{Span: comment.bodySpan, Text: cst.nodeText(comment)}
		}
		owner, depth := cst.syntaxTriviaOwner(comment)
		if owner != nil {
			comment.depth = depth
		} else {
			owner = parents[comment]
		}
		ownerSpan := SourceSpan{}
		if owner != nil {
			ownerSpan = owner.bodySpan
		}
		cst.triviaAttachments[trivia.Span] = formatterTriviaAttachment{
			trivia: trivia, owner: ownerSpan, depth: comment.depth,
			lineEnding: comment.lineEnding, hasNewline: comment.hasNewline,
		}
	}
}

func (cst *formatterCST) rebuildCodeHierarchy(previousParents map[*formatterNode]*formatterNode) {
	for _, node := range cst.lines {
		node.children = nil
	}
	cst.roots = nil

	layoutStack := make([]*formatterNode, 0)
	for _, node := range cst.lines {
		if node.kind != formatterCode {
			continue
		}

		parent := cst.syntaxCodeParent(node)
		if parent == nil && node.layoutDepth > 0 && node.layoutDepth-1 < len(layoutStack) {
			parent = layoutStack[node.layoutDepth-1]
		}
		if parent == nil {
			cst.roots = append(cst.roots, node)
		} else {
			parent.children = append(parent.children, node)
		}

		if node.layoutDepth < len(layoutStack) {
			layoutStack = layoutStack[:node.layoutDepth]
		}
		if node.layoutDepth == len(layoutStack) {
			layoutStack = append(layoutStack, node)
		} else if node.layoutDepth < len(layoutStack) {
			layoutStack[node.layoutDepth] = node
		}
	}

	for _, node := range cst.lines {
		if node.kind == formatterCode || node.kind == formatterComment {
			continue
		}
		parent := previousParents[node]
		for parent != nil && parent.kind != formatterCode {
			parent = previousParents[parent]
		}
		if parent == nil {
			cst.roots = append(cst.roots, node)
		} else {
			parent.children = append(parent.children, node)
		}
	}
}

func (cst formatterCST) syntaxCodeParent(node *formatterNode) *formatterNode {
	position := node.bodySpan.Start
	for _, lexeme := range cst.lexemes {
		if lexeme.Span.Start >= node.bodySpan.Start && lexeme.Span.End <= node.bodySpan.End {
			position = lexeme.Span.Start

			break
		}
	}
	var parent *formatterNode
	parentSpanSize := len(cst.source) + 1
	for _, candidate := range cst.lines {
		if candidate.kind != formatterCode || candidate.bodySpan.Start >= node.bodySpan.Start {
			continue
		}
		for _, label := range candidate.syntax {
			if label.span.Start >= position || position >= label.span.End || label.span.Start < 0 || label.span.End > len(cst.source) {
				continue
			}
			if candidate.layoutDepth > node.layoutDepth {
				continue
			}
			spanSize := label.span.End - label.span.Start
			if parent == nil || spanSize < parentSpanSize {
				parent, parentSpanSize = candidate, spanSize
			}
		}
	}

	return parent
}

func (cst formatterCST) syntaxTriviaOwner(comment *formatterNode) (*formatterNode, int) {
	var owner *formatterNode
	ownerDepth, ownerSpanSize := 0, len(cst.source)+1
	for _, node := range cst.lines {
		if node.kind != formatterCode || node.bodySpan.Start >= comment.bodySpan.Start {
			continue
		}
		for _, label := range node.syntax {
			if label.span.Start > comment.bodySpan.Start || comment.bodySpan.Start >= label.span.End {
				continue
			}
			candidateDepth := 0
			switch label.category {
			case formatterMapItemSyntax, formatterListItemSyntax:
				candidateDepth = label.layoutDepth + 1
			case formatterStatementSyntax:
				candidateDepth = node.depth + 1
			default:
				continue
			}
			spanSize := label.span.End - label.span.Start
			if owner == nil || candidateDepth > ownerDepth || (candidateDepth == ownerDepth && spanSize < ownerSpanSize) {
				owner, ownerDepth, ownerSpanSize = node, candidateDepth, spanSize
			}
		}
	}
	if owner == nil {
		return nil, 0
	}

	return owner, ownerDepth
}

func (cst *formatterCST) addStatementSyntaxLabel(span SourceSpan, kind string) {
	if span.End <= span.Start || span.Start < 0 || len(cst.lines) == 0 {
		return
	}
	line := sort.Search(len(cst.lines), func(index int) bool {
		return cst.lines[index].bodySpan.Start > span.Start
	}) - 1
	if line < 0 || line >= len(cst.lines) {
		return
	}
	cst.lines[line].syntax = append(cst.lines[line].syntax, formatterSyntaxLabel{
		category: formatterStatementSyntax,
		kind:     kind,
		span:     span,
	})
}

func (cst *formatterCST) addExpressionLabels(expression Expr, layoutDepth int) {
	if isNilExpr(expression) {
		return
	}
	span := expression.ExprSpan()
	callDelimiters := ExprCallDelimiters{}
	switch call := expression.(type) {
	case *CallExpr:
		callDelimiters = call.Delimiters
	case *DeferredReferenceExpr:
		callDelimiters = call.Delimiters
	}
	cst.addSpanLabel(span, formatterExpressionSyntax, expressionKindName(expression), callDelimiters, layoutDepth)
	addListItems := func(items []Expr) {
		for _, item := range items {
			itemSpan := item.ExprSpan()
			itemDepth := layoutDepth + 1
			cst.addSpanLabel(itemSpan, formatterListItemSyntax, "item", ExprCallDelimiters{}, itemDepth)
			cst.addExpressionLabels(item, itemDepth)
		}
	}
	switch node := expression.(type) {
	case *ListExpr:
		addListItems(node.Items)
	case *MapExpr:
		for _, entry := range node.Entries {
			valueSpan := entry.Value.ExprSpan()
			start := valueSpan.Start
			if entry.KeySpan.End > entry.KeySpan.Start {
				start = entry.KeySpan.Start
			}
			entryDepth := layoutDepth + 1
			cst.addSpanLabel(SourceSpan{Start: start, End: valueSpan.End}, formatterMapItemSyntax, "entry", ExprCallDelimiters{}, entryDepth)
			cst.addExpressionLabels(entry.KeyExpr, layoutDepth)
			cst.addExpressionLabels(entry.Value, entryDepth)
		}
	case *CallExpr:
		cst.addExpressionLabels(node.Callee, layoutDepth)
		for _, argument := range node.Arguments {
			cst.addExpressionLabels(argument, layoutDepth)
		}
	case *BinaryExpr:
		cst.addExpressionLabels(node.Left, layoutDepth)
		cst.addExpressionLabels(node.Right, layoutDepth)
	case *UnaryExpr:
		cst.addExpressionLabels(node.Operand, layoutDepth)
	case *IndexExpr:
		cst.addExpressionLabels(node.Base, layoutDepth)
		cst.addExpressionLabels(node.Key, layoutDepth)
	case *LambdaExpr:
		cst.addExpressionLabels(node.Body, layoutDepth)
	case *DeferredReferenceExpr:
		cst.addExpressionLabels(node.Target, layoutDepth)
		cst.addExpressionLabels(node.Child, layoutDepth)
	case *ConditionalExpr:
		cst.addExpressionLabels(node.Condition, layoutDepth)
		cst.addExpressionLabels(node.Then, layoutDepth+1)
		cst.addExpressionLabels(node.Else, layoutDepth+1)
	case *ForExpr:
		cst.addExpressionLabels(node.Iterable, layoutDepth)
		cst.addExpressionLabels(node.Body, layoutDepth)
	case *NodeExpr:
		cst.addExpressionLabels(node.Body, layoutDepth)
	case *OverlayExpr:
		cst.addExpressionLabels(node.Base, layoutDepth)
		cst.addExpressionLabels(node.Patch, layoutDepth)
	case *RemovalExpr:
		for _, pathSpan := range node.PathSpans {
			cst.addSpanLabel(pathSpan, formatterListItemSyntax, "path", ExprCallDelimiters{}, layoutDepth+1)
		}
		cst.addExpressionLabels(node.Base, layoutDepth)
	}
}

func (cst *formatterCST) addSpanLabel(span SourceSpan, category formatterSyntaxCategory, kind string, callDelimiters ExprCallDelimiters, layoutDepth int) {
	if span.End <= span.Start || span.Start < 0 || len(cst.lines) == 0 || span.Start >= cst.lines[len(cst.lines)-1].bodySpan.End {
		return
	}
	lineAt := func(offset int) int {
		return sort.Search(len(cst.lines), func(index int) bool { return cst.lines[index].bodySpan.Start > offset }) - 1
	}
	firstLine := lineAt(span.Start)
	lastLine := lineAt(span.End - 1)
	for line := max(firstLine, 0); line <= lastLine && line < len(cst.lines); line++ {
		startsHere := line == firstLine
		layoutEligible := false
		if startsHere && (category == formatterMapItemSyntax || category == formatterListItemSyntax) {
			lineNode := cst.lines[line]
			prefixBounds := SourceSpan{Start: lineNode.bodySpan.Start, End: span.Start}
			prefixLexemes, complete := cst.lexemesInSpan(prefixBounds)
			if !complete {
				continue
			}
			switch category {
			case formatterMapItemSyntax:
				layoutEligible = len(prefixLexemes) == 0
			case formatterListItemSyntax:
				layoutEligible = len(prefixLexemes) == 1 && prefixLexemes[0].Lexeme.Kind == LexemeSymbol && cst.source[prefixLexemes[0].Span.Start:prefixLexemes[0].Span.End] == "-"
			case formatterExpressionSyntax, formatterStatementSyntax:
				// レイアウトブロックを開始できるのはマップとリストの項目だけ。
			}
		}
		cst.lines[line].syntax = append(cst.lines[line].syntax, formatterSyntaxLabel{
			category: category, kind: kind, span: span, callDelimiters: callDelimiters,
			layoutDepth: layoutDepth, startsHere: startsHere, layoutEligible: layoutEligible,
		})
	}
}

func (cst formatterCST) nodeText(node *formatterNode) string {
	return cst.source[node.bodySpan.Start:node.bodySpan.End]
}

func (cst *formatterCST) lexemesInSpan(bounds SourceSpan) ([]Token, bool) {
	if bounds.Start < 0 || bounds.End < bounds.Start || bounds.End > len(cst.source) {
		return nil, false
	}
	start := sort.Search(len(cst.lexemes), func(index int) bool {
		return cst.lexemes[index].Span.Start >= bounds.Start
	})
	if start > 0 && cst.lexemes[start-1].Span.End > bounds.Start {
		start--
	}
	for index := start; index < len(cst.lexemes) && cst.lexemes[index].Span.Start < bounds.End; index++ {
		token := cst.lexemes[index]
		if token.Span.End <= bounds.Start {
			continue
		}
		if token.Span.Start < bounds.Start || token.Span.End > bounds.End {
			return nil, false
		}
	}

	cursor := tokenSpanLexemeCursor{tokens: cst.lexemes, index: start, bounds: bounds}
	var lexemes []Token
	for {
		token, ok := cursor.next()
		if !ok {
			return lexemes, true
		}
		lexemes = append(lexemes, token)
	}
}

func (node *formatterNode) syntaxLayoutDepth() (int, bool) {
	depth := 0
	category := formatterSyntaxCategory(0)
	found := false
	mapItemStarts, listItemStarts := false, false
	for _, label := range node.syntax {
		if !label.startsHere {
			continue
		}
		mapItemStarts = mapItemStarts || label.category == formatterMapItemSyntax
		listItemStarts = listItemStarts || label.category == formatterListItemSyntax
		if !label.layoutEligible {
			continue
		}
		if found && category != label.category {
			return 0, false
		}
		category = label.category
		depth = max(depth, label.layoutDepth)
		found = true
	}
	if mapItemStarts && listItemStarts {
		return 0, false
	}

	return depth, found
}

// for式の種別名。
const expressionKindFor = "for"

func expressionKindName(expression Expr) string {
	switch expression.(type) {
	case *NullExpr:
		return nullLiteral
	case *BoolExpr:
		return "bool"
	case *IntegerExpr:
		return "integer"
	case *DecimalExpr:
		return "decimal"
	case *StringExpr:
		return "string"
	case *EmptyListExpr:
		return "empty-list"
	case *EmptyMapExpr:
		return "empty-map"
	case *ListExpr:
		return "list"
	case *MapExpr:
		return "map"
	case *ReferenceExpr:
		return "reference"
	case *OptionalReferenceExpr:
		return "optional-reference"
	case *PathExpr:
		return "path"
	case *CallExpr:
		return "call"
	case *BinaryExpr:
		return "binary"
	case *UnaryExpr:
		return "unary"
	case *IndexExpr:
		return "index-access"
	case *LambdaExpr:
		return "lambda"
	case *DeferredReferenceExpr:
		return "deferred-reference"
	case *ConditionalExpr:
		return "conditional"
	case *ForExpr:
		return expressionKindFor
	case *NodeExpr:
		return "node"
	case *OverlayExpr:
		return "overlay"
	case *RemovalExpr:
		return "removal"
	default:
		return "unknown"
	}
}

func appendFormatterNode(cst *formatterCST, parents []*formatterNode, node *formatterNode) {
	if len(parents) == 0 {
		cst.roots = append(cst.roots, node)

		return
	}
	parent := parents[len(parents)-1]
	parent.children = append(parent.children, node)
}

func (cst formatterCST) format() string {
	var output strings.Builder
	type outputEvent struct {
		start      int
		node       *formatterNode
		attachment *formatterTriviaAttachment
	}
	attachmentsByOwner := make(map[SourceSpan][]formatterTriviaAttachment, len(cst.triviaAttachments))
	for _, attachment := range cst.triviaAttachments {
		attachmentsByOwner[attachment.owner] = append(attachmentsByOwner[attachment.owner], attachment)
	}
	for owner := range attachmentsByOwner {
		slices.SortStableFunc(attachmentsByOwner[owner], func(left, right formatterTriviaAttachment) int {
			return cmp.Compare(left.trivia.Span.Start, right.trivia.Span.Start)
		})
	}
	writeAttachment := func(attachment formatterTriviaAttachment) {
		output.WriteString(strings.Repeat(" ", attachment.depth*2))
		output.WriteString(strings.TrimRight(strings.TrimSpace(attachment.trivia.Text), " "))
		if attachment.hasNewline {
			output.WriteString(cst.source[attachment.lineEnding.Start:attachment.lineEnding.End])
		}
	}
	var writeNode func(*formatterNode)
	writeNode = func(node *formatterNode) {
		switch node.kind {
		case formatterBlank:
		case formatterStringContent:
			output.WriteString(cst.nodeText(node))
		case formatterComment:
			output.WriteString(strings.Repeat(" ", node.depth*2))
			output.WriteString(strings.TrimRight(strings.TrimSpace(cst.nodeText(node)), " "))
		case formatterCode:
			trimmed := strings.TrimSpace(cst.nodeText(node))
			depth := node.depth
			if syntaxDepth, ok := node.syntaxLayoutDepth(); ok {
				depth = syntaxDepth
			}
			output.WriteString(strings.Repeat(" ", depth*2))
			output.WriteString(strings.TrimRight(trimmed, " "))
		}
		if node.hasNewline {
			output.WriteString(cst.source[node.lineEnding.Start:node.lineEnding.End])
		}
		events := make([]outputEvent, 0, len(node.children))
		for _, child := range node.children {
			events = append(events, outputEvent{start: child.bodySpan.Start, node: child})
		}
		if node.bodySpan != (SourceSpan{}) {
			for index := range attachmentsByOwner[node.bodySpan] {
				attachment := &attachmentsByOwner[node.bodySpan][index]
				events = append(events, outputEvent{start: attachment.trivia.Span.Start, attachment: attachment})
			}
		}
		slices.SortStableFunc(events, func(left, right outputEvent) int {
			return cmp.Compare(left.start, right.start)
		})
		for _, event := range events {
			if event.node != nil {
				writeNode(event.node)
			} else {
				writeAttachment(*event.attachment)
			}
		}
	}
	rootEvents := make([]outputEvent, 0, len(cst.roots)+len(attachmentsByOwner[SourceSpan{}]))
	for _, root := range cst.roots {
		rootEvents = append(rootEvents, outputEvent{start: root.bodySpan.Start, node: root})
	}
	for index := range attachmentsByOwner[SourceSpan{}] {
		attachment := &attachmentsByOwner[SourceSpan{}][index]
		rootEvents = append(rootEvents, outputEvent{start: attachment.trivia.Span.Start, attachment: attachment})
	}
	slices.SortStableFunc(rootEvents, func(left, right outputEvent) int {
		return cmp.Compare(left.start, right.start)
	})
	for _, event := range rootEvents {
		if event.node != nil {
			writeNode(event.node)
		} else {
			writeAttachment(*event.attachment)
		}
	}

	return output.String()
}

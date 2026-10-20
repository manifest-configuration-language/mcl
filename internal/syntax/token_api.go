package syntax

type TokenKind uint8

const (
	TokenLexeme TokenKind = iota
	TokenNewline
	TokenIndent
	TokenDedent
	TokenTrivia
)

type LexemeKind uint8

const (
	LexemeIdentifier LexemeKind = iota
	LexemeLiteral
	LexemeSymbol
	LexemeNumber
)

type LiteralToken struct {
	Value string
	Span  SourceSpan
}

type Lexeme struct {
	Kind  LexemeKind
	Start int
	End   int
}

type Token struct {
	Kind         TokenKind
	Indent       int
	LineNumber   int
	Lexeme       Lexeme
	Span         SourceSpan
	LineSpan     SourceSpan
	Literals     []LiteralToken
	TriviaText   string
	TriviaOffset int
	TriviaOnly   bool
}

func Lex(source string) ([]Token, error) {
	tokens, err := lexSource(source)
	if err != nil {
		return nil, err
	}

	return tokensFromLexed(tokens), nil
}

func ParseASTWithTokens(source string) (ModuleAST, []Token, error) {
	module, tokens, err := parseASTWithTokens(source)
	if err != nil {
		return ModuleAST{}, nil, err
	}

	return module, tokensFromLexed(tokens), nil
}

func tokensFromLexed(tokens []sourceToken) []Token {
	converted := make([]Token, len(tokens))
	for index, token := range tokens {
		converted[index] = Token{
			Kind:         TokenKind(token.kind),
			Indent:       token.indent,
			LineNumber:   token.lineNumber,
			Lexeme:       Lexeme{Kind: LexemeKind(token.lexeme.kind), Start: token.lexeme.start, End: token.lexeme.end},
			Span:         token.span,
			LineSpan:     token.lineSpan,
			TriviaText:   token.triviaText,
			TriviaOffset: token.triviaOffset,
			TriviaOnly:   token.triviaOnly,
		}
		if token.literals != nil {
			converted[index].Literals = make([]LiteralToken, len(token.literals))
			for literalIndex, literal := range token.literals {
				converted[index].Literals[literalIndex] = LiteralToken{Value: literal.value, Span: literal.span}
			}
		}
	}

	return converted
}

func LexemesInSpan(source string, span SourceSpan) ([]Lexeme, bool) {
	if span.Start < 0 || span.End < span.Start || span.End > len(source) {
		return nil, false
	}
	tokens, err := Lex(source)
	if err != nil {
		return nil, false
	}
	lexemes := make([]Lexeme, 0)
	for _, token := range tokens {
		if token.Kind != TokenLexeme || token.Span.Start < span.Start || token.Span.End > span.End {
			continue
		}
		lexemes = append(lexemes, token.Lexeme)
	}

	return lexemes, true
}

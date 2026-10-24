package languageservice

// Position is a zero-based UTF-16 position in source text.
type Position struct {
	Line      int
	Character int
}

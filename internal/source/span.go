package source

// Span is a half-open byte range in one source file.
type Span struct {
	Start int
	End   int
}

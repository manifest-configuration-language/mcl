package lsp

import "github.com/manifest-configuration-language/mcl/internal/languageservice"

type completionItem = languageservice.CompletionItem

type completionParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	} `json:"position"`
}

func (s *server) completions(params completionParams) []languageservice.CompletionItem {
	state, exists := s.documents[params.TextDocument.URI]
	if !exists {
		return languageservice.CompletionForSource("", params.Position.Line, params.Position.Character, "")
	}
	return languageservice.CompletionForSource(state.Text, params.Position.Line, params.Position.Character, documentPath(params.TextDocument.URI))
}

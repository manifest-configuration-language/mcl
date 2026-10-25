package lsp

import (
	"context"

	"github.com/manifest-configuration-language/mcl/internal/languageservice"
)

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

func (s *server) completions(ctx context.Context, params completionParams) []languageservice.CompletionItem {
	state, exists := s.documents[params.TextDocument.URI]
	if !exists {
		return languageservice.CompletionForSourceWithContext(ctx, "", params.Position.Line, params.Position.Character, "")
	}

	return languageservice.CompletionForSourceWithContext(ctx, state.Text, params.Position.Line, params.Position.Character, documentPath(params.TextDocument.URI))
}

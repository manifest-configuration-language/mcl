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
	path, ok := canonicalSourcePath(documentPath(params.TextDocument.URI))
	if !ok {
		return languageservice.CompletionForSourceWithContext(ctx, state.Text, params.Position.Line, params.Position.Character, "")
	}
	resolver, readSource := projectReferenceResolvers(s)
	semantic, err := s.semanticSnapshotForDocument(ctx, path, state.Text)
	if err != nil {
		return nil
	}

	return languageservice.CompletionsForSnapshotWithContext(ctx, semantic, languageservice.CompletionParams{
		Entrypoint: path, Source: state.Text, Line: params.Position.Line, Character: params.Position.Character,
	}, resolver, readSource)
}

package lsp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

func TestReferencesFindsWorkspaceImporters(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "mcl.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(root, "target.mcl")
	if err := os.WriteFile(targetPath, []byte("export let value = \"ok\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	importerPath := filepath.Join(root, "main.mcl")
	const importerSource = "import \"./target.mcl\" as target\nemit target.value\n"
	if err := os.WriteFile(importerPath, []byte(importerSource), 0o600); err != nil {
		t.Fatal(err)
	}
	importerURI := filePathToURI(importerPath)
	s := server{
		documents:      map[string]documentState{importerURI: {Text: importerSource}},
		workspaceRoots: []string{root},
	}
	params := &protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri.URI(importerURI)},
			Position:     protocol.Position{Line: 1, Character: 12},
		},
		Context: protocol.ReferenceContext{IncludeDeclaration: true},
	}
	locations, err := s.References(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) != 2 {
		t.Fatalf("References() returned %d locations, want declaration and importer: %#v", len(locations), locations)
	}
	canonicalTarget, _ := canonicalSourcePath(targetPath)
	foundTarget, foundImporter := false, false
	for _, location := range locations {
		foundTarget = foundTarget || location.URI == uri.URI(filePathToURI(canonicalTarget)) && location.Range.Start.Line == 0
		foundImporter = foundImporter || location.URI == uri.URI(importerURI) && location.Range.Start.Line == 1
	}
	if !foundTarget || !foundImporter {
		t.Errorf("References() omitted declaration or importer location: %#v", locations)
	}
}

func TestReferencesReturnsRequestErrorWhenWorkspaceSourceIsInvalid(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	targetPath := filepath.Join(root, "target.mcl")
	if err := os.WriteFile(targetPath, []byte("export let value = \"ok\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	importerPath := filepath.Join(root, "main.mcl")
	const importerSource = "import \"./target.mcl\" as target\nemit target.value\n"
	if err := os.WriteFile(importerPath, []byte(importerSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken.mcl"), []byte("let broken =\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	importerURI := filePathToURI(importerPath)
	s := server{
		documents:      map[string]documentState{importerURI: {Text: importerSource}},
		workspaceRoots: []string{root},
	}
	params := &protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri.URI(importerURI)},
			Position:     protocol.Position{Line: 1, Character: 12},
		},
		Context: protocol.ReferenceContext{IncludeDeclaration: true},
	}
	_, err := s.References(t.Context(), params)
	if _, ok := errors.AsType[*jsonrpc2.Error](err); !ok {
		t.Fatalf("References() error = %v, want a JSON-RPC request error", err)
	}
}

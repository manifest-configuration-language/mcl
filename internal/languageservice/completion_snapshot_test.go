package languageservice

import "testing"

func TestCompletionsForSnapshotUsesCachedSemanticIndex(t *testing.T) {
	snapshot, err := NewProjectSemanticSnapshotFromSources(map[string]string{
		"main.mcl": "let alpha = 1\nemit al\n",
	})
	if err != nil {
		t.Fatalf("NewProjectSemanticSnapshotFromSources() error = %v", err)
	}
	items := CompletionsForSnapshotWithContext(t.Context(), snapshot, CompletionParams{
		Entrypoint: "main.mcl",
		Line:       1,
		Character:  7,
	}, nil, nil)
	for _, item := range items {
		if item.Label == "alpha" {
			return
		}
	}
	t.Fatalf("CompletionsForSnapshotWithContext() = %+v, want alpha", items)
}

package lsp

import (
	"testing"

	"github.com/manifest-configuration-language/mcl/internal/languageservice"
	"github.com/manifest-configuration-language/mcl/internal/project"
)

func TestProjectSemanticSnapshotCacheReusesOnlyMatchingRequestState(t *testing.T) {
	cache := &projectSemanticSnapshotCache{}
	resolveImport := languageservice.ReferenceImportResolver(func(importer, modulePath string) (string, bool) {
		return "", false
	})
	sources := map[string]string{"main.mcl": "let value = 1\n"}
	first, err := cache.fromDocument(3, "main.mcl", sources, resolveImport)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.fromDocument(3, "main.mcl", sources, resolveImport)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("matching source generation did not reuse its semantic snapshot")
	}
	documentIndex, exists := first.SemanticIndex("main.mcl")
	if !exists {
		t.Fatal("document snapshot did not produce its semantic index")
	}
	workspace, err := project.NewWorkspaceSnapshot(1, ".", nil, sources)
	if err != nil {
		t.Fatal(err)
	}
	workspaceSnapshot := cache.fromWorkspace(workspace, resolveImport)
	workspaceIndex, exists := workspaceSnapshot.SemanticIndex("main.mcl")
	if !exists || workspaceIndex != documentIndex {
		t.Fatal("workspace snapshot did not reuse the unchanged document semantic index")
	}

	changed, err := cache.fromDocument(3, "main.mcl", map[string]string{"main.mcl": "let value = 2\n"}, resolveImport)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("changed source reused the prior semantic snapshot")
	}

	nextGeneration, err := cache.fromDocument(4, "main.mcl", map[string]string{"main.mcl": "let value = 2\n"}, resolveImport)
	if err != nil {
		t.Fatal(err)
	}
	if nextGeneration == changed {
		t.Fatal("new document generation reused a prior semantic snapshot")
	}
}

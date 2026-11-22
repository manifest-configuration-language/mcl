package project

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const (
	linkModuleName   = "link.mcl"
	targetModuleName = "target.mcl"
)

func TestResolveModuleImportTracksSymlinkAndTargetPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(root, targetModuleName)
	if err := os.WriteFile(targetPath, []byte("export let value = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, linkModuleName)
	if err := os.Symlink(targetModuleName, linkPath); err != nil {
		t.Fatal(err)
	}

	filesystem := NewOSFilesystem(root)
	resolved, err := filesystem.ResolveModuleImport(filepath.Join(root, "main.mcl"), "./link.mcl")
	if err != nil {
		t.Fatalf("ResolveModuleImport() error = %v", err)
	}
	if _, err := filesystem.ReadLimitedFile(resolved, MaxSourceBytes); err != nil {
		t.Fatalf("ReadLimitedFile() error = %v", err)
	}
	if got, want := filesystem.ReadPaths(), []string{linkModuleName, targetModuleName}; !slices.Equal(got, want) {
		t.Fatalf("ReadPaths() = %q, want %q", got, want)
	}
}

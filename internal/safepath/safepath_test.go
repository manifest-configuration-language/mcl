package safepath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWithinUsesPathBoundaries(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project", "app")
	if !Within(root, filepath.Join(root, "modules", "base.mcl")) {
		t.Fatal("Within() rejected a descendant path")
	}
	if Within(root, filepath.Join(filepath.Dir(root), "application", "main.mcl")) {
		t.Fatal("Within() accepted a sibling with a shared path prefix")
	}
}

func TestResolveWithinRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "outside.mcl")
	if err := os.WriteFile(target, []byte("export let value = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside.mcl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if _, err := ResolveWithin(root, link); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("ResolveWithin() error = %v, want ErrOutsideRoot", err)
	}
}

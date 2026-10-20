package dependency

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contentutil "github.com/manifest-configuration-language/mcl/internal/content"
)

func TestLoadManifestRejectsTamperedOfflineCache(t *testing.T) {
	t.Parallel()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity := ToolchainIdentity{
		MCL: "1.0.0", Go: "go1.27.3", Helm: "4.0.0",
		KustomizeAPI: "api-1", KustomizeKYAML: "kyaml-1",
	}
	lock, err := Open(t.Context(), root, true, identity)
	if err != nil {
		t.Fatal(err)
	}
	source := "https://example.test/deployment.yaml"
	digest := contentutil.SHA256([]byte("expected manifest"))
	lock.manifests[source] = digest
	if err := lock.Save(); err != nil {
		t.Fatal(err)
	}

	cachePath := filepath.Join(root, ".mcl", "cache", "sha256", digest)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("tampered manifest"), 0o600); err != nil {
		t.Fatal(err)
	}

	lock, err = Open(t.Context(), root, false, identity)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lock.LoadManifest(source)
	if err == nil || !strings.Contains(err.Error(), "does not match its locked SHA-256 digest") {
		t.Fatalf("LoadManifest() error = %v, want cache digest mismatch", err)
	}
}

func TestFailedUpdateKeepsExistingLock(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	identity := ToolchainIdentity{MCL: "1.0.0", Go: "go1.27.3", Helm: "4.0.0", KustomizeAPI: "api-1", KustomizeKYAML: "kyaml-1"}
	initial, err := Open(t.Context(), root, true, identity)
	if err != nil {
		t.Fatal(err)
	}
	initial.manifests["https://example.test/previous.yaml"] = contentutil.SHA256([]byte("previous"))
	if err := initial.Save(); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, "mcl.lock")
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	update, err := Open(ctx, root, true, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := update.LoadManifest("https://example.test/next.yaml"); err == nil {
		t.Fatal("LoadManifest() succeeded with a canceled update context")
	}
	after, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed dependency update changed the existing lock file")
	}
}

package dependency

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	contentutil "github.com/manifest-configuration-language/mcl/internal/content"
)

func testToolchainIdentity() ToolchainIdentity {
	return ToolchainIdentity{
		MCL: "1.0.0", Go: "go1.27.3", Helm: "4.0.0",
		KustomizeAPI: "api-1", KustomizeKYAML: "kyaml-1",
	}
}

func TestLoadManifestRejectsTamperedOfflineCache(t *testing.T) {
	t.Parallel()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity := testToolchainIdentity()
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

func TestLoadManifestReadsLockAndCacheFromInjectedFilesystem(t *testing.T) {
	t.Parallel()
	identity := testToolchainIdentity()
	source := "https://example.test/deployment.yaml"
	content := []byte("apiVersion: v1\nkind: ConfigMap\n")
	digest := contentutil.SHA256(content)
	lockData, err := json.Marshal(lockFile{
		Version: 3, Toolchain: identity.lock(),
		Manifests: map[string]string{source: digest},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		"mcl.lock":                    &fstest.MapFile{Data: lockData},
		".mcl/cache/sha256/" + digest: &fstest.MapFile{Data: content},
	}
	lock, err := OpenWithFS(t.Context(), "/virtual/project", false, identity, files)
	if err != nil {
		t.Fatalf("open injected lock filesystem: %v", err)
	}
	loaded, err := lock.LoadManifest(source)
	if err != nil {
		t.Fatalf("load manifest from injected filesystem: %v", err)
	}
	if string(loaded) != string(content) {
		t.Fatalf("loaded manifest = %q, want %q", loaded, content)
	}
}

func TestInjectedFilesystemDoesNotFallBackToOrWriteOSCache(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	content := []byte("cached content")
	digest := contentutil.SHA256(content)
	cachePath := filepath.Join(root, ".mcl", "cache", "sha256", digest)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	zeroValueLock := &Lock{root: root}
	if _, err := zeroValueLock.readCache(digest, maxRemoteManifestSize); err == nil || !strings.Contains(err.Error(), "dependency filesystem is required") {
		t.Fatalf("readCache() error = %v, want missing filesystem error", err)
	}

	lock, err := OpenWithFS(t.Context(), root, false, testToolchainIdentity(), fstest.MapFS{})
	if err != nil {
		t.Fatalf("open injected lock filesystem: %v", err)
	}
	if err := lock.Save(); err == nil || !strings.Contains(err.Error(), "atomic writes") {
		t.Fatalf("Save() error = %v, want atomic writes error", err)
	}
	if err := lock.writeCache(digest, content); err == nil || !strings.Contains(err.Error(), "atomic writes") {
		t.Fatalf("writeCache() error = %v, want atomic writes error", err)
	}
	if _, err := os.Stat(filepath.Join(root, "mcl.lock")); !os.IsNotExist(err) {
		t.Fatalf("mcl.lock stat error = %v, want file not to be created", err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("existing OS cache entry was unexpectedly changed or removed: %v", err)
	}
}

type atomicMapFS struct {
	files fstest.MapFS
}

func (filesystem *atomicMapFS) Open(name string) (fs.File, error) {
	return filesystem.files.Open(name)
}

func (filesystem *atomicMapFS) WriteFileAtomic(name string, data []byte) error {
	if !fs.ValidPath(name) || name == "." {
		return fs.ErrInvalid
	}
	filesystem.files[name] = &fstest.MapFile{Data: append([]byte(nil), data...)}

	return nil
}

func TestOpenWithAtomicFilesystemWritesLockAndCacheWithinRoot(t *testing.T) {
	t.Parallel()
	files := &atomicMapFS{files: make(fstest.MapFS)}
	lock, err := OpenWithFS(t.Context(), "/virtual/project", true, testToolchainIdentity(), files)
	if err != nil {
		t.Fatalf("open writable filesystem: %v", err)
	}
	content := []byte("apiVersion: v1\nkind: ConfigMap\n")
	digest := contentutil.SHA256(content)
	lock.manifests["https://example.test/deployment.yaml"] = digest
	if err := lock.writeCache(digest, content); err != nil {
		t.Fatalf("write dependency cache: %v", err)
	}
	if err := lock.Save(); err != nil {
		t.Fatalf("save dependency lock: %v", err)
	}
	if _, exists := files.files["mcl.lock"]; !exists {
		t.Fatal("atomic filesystem did not receive mcl.lock")
	}
	if _, exists := files.files[".mcl/cache/sha256/"+digest]; !exists {
		t.Fatal("atomic filesystem did not receive dependency cache content")
	}
	loaded, err := OpenWithFS(t.Context(), "/another/project", false, testToolchainIdentity(), files)
	if err != nil {
		t.Fatalf("reopen writable filesystem: %v", err)
	}
	read, err := loaded.LoadManifest("https://example.test/deployment.yaml")
	if err != nil {
		t.Fatalf("load dependency cache: %v", err)
	}
	if string(read) != string(content) {
		t.Fatalf("loaded content = %q, want %q", read, content)
	}
}

func TestOpenWithFSUpdateRequiresAtomicWriter(t *testing.T) {
	t.Parallel()
	if _, err := OpenWithFS(t.Context(), "/virtual/project", true, testToolchainIdentity(), fstest.MapFS{}); err == nil || !strings.Contains(err.Error(), "atomic writable filesystem") {
		t.Fatalf("OpenWithFS() error = %v, want atomic writable filesystem error", err)
	}
}

func TestRootFilesystemRejectsInvalidAtomicWritePath(t *testing.T) {
	t.Parallel()
	files := &atomicMapFS{files: make(fstest.MapFS)}
	filesystem := newRootFilesystem("/virtual/project", files)
	if err := filesystem.writeFileAtomic("../outside", []byte("data")); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("writeFileAtomic() error = %v, want fs.ErrInvalid", err)
	}
	if len(files.files) != 0 {
		t.Fatalf("invalid path was passed to writer: %#v", files.files)
	}
}

func TestLoadManifestRejectsCacheDirectorySymlinkOutsideProject(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	identity := testToolchainIdentity()
	source := "https://example.test/deployment.yaml"
	content := []byte("apiVersion: v1\nkind: ConfigMap\n")
	digest := contentutil.SHA256(content)
	lock, err := Open(t.Context(), root, true, identity)
	if err != nil {
		t.Fatal(err)
	}
	lock.manifests[source] = digest
	if err := lock.Save(); err != nil {
		t.Fatal(err)
	}
	outsideCache := filepath.Join(outside, "cache", "sha256")
	if err := os.MkdirAll(outsideCache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideCache, digest), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".mcl")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	lock, err = Open(t.Context(), root, false, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.LoadManifest(source); err == nil || !strings.Contains(err.Error(), "escapes the project directory") {
		t.Fatalf("LoadManifest() error = %v, want cache containment error", err)
	}
}

func TestFailedUpdateKeepsExistingLock(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	identity := testToolchainIdentity()
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

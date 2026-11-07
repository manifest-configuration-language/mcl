package dependency

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	contentutil "github.com/manifest-configuration-language/mcl/internal/content"
)

const (
	testLockFilename   = "mcl.lock"
	testManifestSource = "https://example.test/deployment.yaml"
)

func TestCheckRemoteRedirect(t *testing.T) {
	t.Parallel()

	request := func(scheme string) *http.Request {
		return &http.Request{URL: &url.URL{Scheme: scheme}}
	}
	tests := []struct {
		name    string
		request *http.Request
		via     []*http.Request
		wantErr string
	}{
		{name: "HTTPS", request: request(schemeHTTPS)},
		{name: "reject HTTP downgrade", request: request("http"), wantErr: "must use HTTPS"},
		{name: "reject too many redirects", request: request(schemeHTTPS), via: make([]*http.Request, maxRemoteRedirects), wantErr: "exceeds the redirect limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := checkRemoteRedirect(test.request, test.via)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("checkRemoteRedirect() error = %v, want nil", err)
				}

				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("checkRemoteRedirect() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestChartArchiveURLUsesExactLockedVersionAndHTTPS(t *testing.T) {
	t.Parallel()

	index := []byte("entries:\n  memcached:\n    - version: 6.14.0\n      urls:\n        - memcached-6.14.0.tgz\n    - version: 6.15.0\n      urls:\n        - memcached-6.15.0.tgz\n")
	archiveURL, err := chartArchiveURL(index, "https://charts.example.test/bitnami", "memcached", "6.14.0")
	if err != nil {
		t.Fatalf("chartArchiveURL() error = %v", err)
	}
	if archiveURL != "https://charts.example.test/bitnami/memcached-6.14.0.tgz" {
		t.Fatalf("chartArchiveURL() = %q, want exact-version archive URL", archiveURL)
	}

	index = []byte("entries:\n  memcached:\n    - version: 6.14.0\n      urls:\n        - http://charts.example.test/memcached-6.14.0.tgz\n")
	if _, err := chartArchiveURL(index, "https://charts.example.test/bitnami", "memcached", "6.14.0"); err == nil {
		t.Fatal("chartArchiveURL() accepted an HTTP archive URL")
	}
}

func TestChartRepositoryIndexUsesBoundedLargerResponseLimit(t *testing.T) {
	t.Parallel()

	if got := remoteResponseLimit("chart-index"); got != 64<<20 {
		t.Fatalf("Helm repository index limit = %d, want 64 MiB", got)
	}
	if _, err := readRemoteResponse(strings.NewReader("index"), "chart-index", 4); err == nil || !strings.Contains(err.Error(), "chart-index response exceeds 4 bytes") {
		t.Fatalf("readRemoteResponse() error = %v, want bounded chart index error", err)
	}
}

func testToolchainIdentity() ToolchainIdentity {
	return ToolchainIdentity{
		MCL: "1.0.0", Go: "go1.27.3", Helm: "4.0.0",
		KustomizeAPI: "api-1", KustomizeKYAML: "kyaml-1",
	}
}

func TestOpenRejectsMismatchedToolchainForCurrentLockVersions(t *testing.T) {
	t.Parallel()

	for _, version := range []int{3, 4} {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			t.Parallel()
			lockData, err := json.Marshal(lockFile{Version: version, Toolchain: toolchainLock{}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = OpenWithFS(t.Context(), "/virtual/project", false, testToolchainIdentity(), fstest.MapFS{
				testLockFilename: {Data: lockData},
			})
			if err == nil || !strings.Contains(err.Error(), "toolchain does not match") {
				t.Fatalf("OpenWithFS() error = %v, want toolchain mismatch", err)
			}
		})
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
	source := testManifestSource
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
	source := testManifestSource
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
		testLockFilename:              &fstest.MapFile{Data: lockData},
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

func TestLockedFetchUsesVerifiedCacheWithoutChangingLock(t *testing.T) {
	t.Parallel()
	identity := testToolchainIdentity()
	source := testManifestSource
	content := []byte("apiVersion: v1\nkind: ConfigMap\n")
	digest := contentutil.SHA256(content)
	lockData, err := json.Marshal(lockFile{
		Version: 3, Toolchain: identity.lock(),
		Manifests: map[string]string{source: digest},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := &atomicMapFS{files: fstest.MapFS{
		testLockFilename:              &fstest.MapFile{Data: lockData},
		".mcl/cache/sha256/" + digest: &fstest.MapFile{Data: content},
	}}
	lock, err := OpenLockedFetchWithFS(t.Context(), "/virtual/project", identity, files)
	if err != nil {
		t.Fatalf("open locked fetch filesystem: %v", err)
	}
	if lock.IsUpdate() || !lock.IsLockedFetch() {
		t.Fatalf("lock modes = update:%t fetch-locked:%t, want false/true", lock.IsUpdate(), lock.IsLockedFetch())
	}
	loaded, err := lock.LoadManifest(source)
	if err != nil {
		t.Fatalf("load manifest from cache: %v", err)
	}
	if string(loaded) != string(content) {
		t.Fatalf("loaded manifest = %q, want %q", loaded, content)
	}
	if got := files.files[testLockFilename].Data; string(got) != string(lockData) {
		t.Fatalf("locked fetch changed mcl.lock: got %s, want %s", got, lockData)
	}
}

func TestLockedFetchFillsCacheFromTheLockedContentWithoutChangingLock(t *testing.T) {
	t.Parallel()
	identity := testToolchainIdentity()
	source := testManifestSource
	content := []byte("apiVersion: v1\nkind: ConfigMap\n")
	digest := contentutil.SHA256(content)
	lockData, err := json.Marshal(lockFile{Version: 3, Toolchain: identity.lock(), Manifests: map[string]string{source: digest}})
	if err != nil {
		t.Fatal(err)
	}
	files := &atomicMapFS{files: fstest.MapFS{testLockFilename: &fstest.MapFile{Data: lockData}}}
	lock, err := OpenLockedFetchWithFS(t.Context(), "/virtual/project", identity, files)
	if err != nil {
		t.Fatalf("open locked fetch filesystem: %v", err)
	}
	fetchCount := 0
	lock.remoteFetcher = func(_ context.Context, gotSource, category string) ([]byte, error) {
		fetchCount++
		if gotSource != source || category != "manifest" {
			t.Fatalf("fetch arguments = (%q, %q), want (%q, manifest)", gotSource, category, source)
		}

		return append([]byte(nil), content...), nil
	}
	for range 2 {
		loaded, err := lock.LoadManifest(source)
		if err != nil {
			t.Fatalf("load manifest on pass %d: %v", fetchCount+1, err)
		}
		if string(loaded) != string(content) {
			t.Fatalf("loaded manifest = %q, want %q", loaded, content)
		}
	}
	if fetchCount != 1 {
		t.Fatalf("remote fetch count = %d, want one fetch followed by a cache hit", fetchCount)
	}
	if got := files.files[".mcl/cache/sha256/"+digest].Data; string(got) != string(content) {
		t.Fatalf("cached content = %q, want %q", got, content)
	}
	if got := files.files[testLockFilename].Data; string(got) != string(lockData) {
		t.Fatalf("locked fetch changed mcl.lock: got %s, want %s", got, lockData)
	}
}

func TestLockedFetchLoadsPinnedGitSourceIntoCache(t *testing.T) {
	t.Parallel()

	identity := testToolchainIdentity()
	source := "git+https://github.com/example/operator.git"
	revision := "0123456789abcdef0123456789abcdef01234567"
	directory := "config/crd/bases"
	archive := []byte("locked Git tree archive")
	digest := contentutil.SHA256(archive)
	key := GitSourceKey(source, revision, directory)
	lockData, err := json.Marshal(lockFile{
		Version: 3, Toolchain: identity.lock(),
		GitSources: map[string]gitSourceLock{key: {Source: source, Revision: revision, Directory: directory, ArchiveDigest: digest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := &atomicMapFS{files: fstest.MapFS{testLockFilename: &fstest.MapFile{Data: lockData}}}
	lock, err := OpenLockedFetchWithFS(t.Context(), "/virtual/project", identity, files)
	if err != nil {
		t.Fatalf("open locked fetch filesystem: %v", err)
	}
	fetchCount := 0
	lock.gitFetcher = func(_ context.Context, gotSource, gotRevision, gotDirectory string) ([]byte, error) {
		fetchCount++
		if gotSource != source || gotRevision != revision || gotDirectory != directory {
			t.Fatalf("Git fetch arguments = (%q, %q, %q), want locked source, revision, and directory", gotSource, gotRevision, gotDirectory)
		}

		return append([]byte(nil), archive...), nil
	}
	for range 2 {
		loaded, err := lock.LoadGitSource(source, revision, directory)
		if err != nil {
			t.Fatalf("load locked Git source: %v", err)
		}
		if string(loaded) != string(archive) {
			t.Fatalf("loaded Git archive = %q, want %q", loaded, archive)
		}
	}
	if fetchCount != 1 {
		t.Fatalf("Git fetch count = %d, want one fetch followed by a cache hit", fetchCount)
	}
	if got := files.files[testLockFilename].Data; string(got) != string(lockData) {
		t.Fatalf("locked fetch changed mcl.lock: got %s, want %s", got, lockData)
	}
}

func TestLockedFetchReusesConfiguredCacheAcrossLocks(t *testing.T) {
	t.Parallel()

	projectRoot := t.TempDir()
	cacheDirectory := filepath.Join(t.TempDir(), "shared-cache")
	identity := testToolchainIdentity()
	source := "git+https://github.com/example/operator.git"
	revision := "0123456789abcdef0123456789abcdef01234567"
	directory := "."
	archive := []byte("locked Git tree archive")
	digest := contentutil.SHA256(archive)
	key := GitSourceKey(source, revision, directory)
	lockData, err := json.Marshal(lockFile{
		Version: 3, Toolchain: identity.lock(),
		GitSources: map[string]gitSourceLock{key: {Source: source, Revision: revision, Directory: directory, ArchiveDigest: digest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, testLockFilename), lockData, 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := OpenLockedFetch(t.Context(), projectRoot, identity)
	if err != nil {
		t.Fatalf("open first locked fetch: %v", err)
	}
	if err := first.SetCacheDirectory(cacheDirectory); err != nil {
		t.Fatalf("configure first shared cache: %v", err)
	}
	first.gitFetcher = func(context.Context, string, string, string) ([]byte, error) {
		return append([]byte(nil), archive...), nil
	}
	if _, err := first.LoadGitSource(source, revision, directory); err != nil {
		t.Fatalf("fetch and cache locked Git source: %v", err)
	}

	second, err := OpenLockedFetch(t.Context(), projectRoot, identity)
	if err != nil {
		t.Fatalf("open second locked fetch: %v", err)
	}
	if err := second.SetCacheDirectory(cacheDirectory); err != nil {
		t.Fatalf("configure second shared cache: %v", err)
	}
	second.gitFetcher = func(context.Context, string, string, string) ([]byte, error) {
		t.Fatal("second lock fetched a dependency already present in the shared cache")

		return nil, nil
	}
	loaded, err := second.LoadGitSource(source, revision, directory)
	if err != nil {
		t.Fatalf("load locked Git source from shared cache: %v", err)
	}
	if string(loaded) != string(archive) {
		t.Fatalf("cached Git archive = %q, want %q", loaded, archive)
	}
	if got, err := os.ReadFile(filepath.Join(cacheDirectory, "sha256", digest)); err != nil || string(got) != string(archive) {
		t.Fatalf("shared cache content = %q, %v; want %q", got, err, archive)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, ".mcl", "cache")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project-local cache stat error = %v, want no project-local cache", err)
	}
	if got, err := os.ReadFile(filepath.Join(projectRoot, testLockFilename)); err != nil || string(got) != string(lockData) {
		t.Fatalf("mcl.lock = %s, %v; want unchanged lock data", got, err)
	}
}

func TestSetCacheDirectoryRequiresAbsoluteOSBackedDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	lock, err := Open(t.Context(), root, false, testToolchainIdentity())
	if err != nil {
		t.Fatalf("open OS-backed lock: %v", err)
	}
	if err := lock.SetCacheDirectory("relative-cache"); err == nil {
		t.Fatal("SetCacheDirectory() accepted a relative path")
	}
	files := &atomicMapFS{files: make(fstest.MapFS)}
	virtualLock, err := OpenWithFS(t.Context(), "/virtual/project", false, testToolchainIdentity(), files)
	if err != nil {
		t.Fatalf("open injected-filesystem lock: %v", err)
	}
	if err := virtualLock.SetCacheDirectory(filepath.Join(t.TempDir(), "cache")); err == nil {
		t.Fatal("SetCacheDirectory() accepted an external cache for an injected filesystem")
	}
}

func TestLockedFetchLoadsPinnedOCIChartIntoCache(t *testing.T) {
	t.Parallel()

	identity := testToolchainIdentity()
	source := "oci://ghcr.io/example/operator"
	version := "1.2.3"
	archive := []byte("locked OCI chart archive")
	manifest := []byte("locked OCI manifest")
	entry := ociChartLock{
		ManifestDigest:   "sha256:" + contentutil.SHA256(manifest),
		ManifestSize:     int64(len(manifest)),
		ChartLayerDigest: "sha256:" + contentutil.SHA256(archive),
		ChartLayerSize:   int64(len(archive)),
		ArchiveDigest:    contentutil.SHA256(archive),
	}
	lockData, err := json.Marshal(lockFile{
		Version: 3, Toolchain: identity.lock(),
		OCICharts: map[string]map[string]ociChartLock{source: {version: entry}},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := &atomicMapFS{files: fstest.MapFS{testLockFilename: &fstest.MapFile{Data: lockData}}}
	lock, err := OpenLockedFetchWithFS(t.Context(), "/virtual/project", identity, files)
	if err != nil {
		t.Fatalf("open locked fetch filesystem: %v", err)
	}
	fetchCount := 0
	lock.ociChartFetcher = func(_ context.Context, gotSource, gotVersion string) ([]byte, ociChartLock, error) {
		fetchCount++
		if gotSource != source || gotVersion != version {
			t.Fatalf("OCI fetch arguments = (%q, %q), want locked source and version", gotSource, gotVersion)
		}

		return append([]byte(nil), archive...), entry, nil
	}
	for range 2 {
		loaded, err := lock.LoadOCIChart(source, version)
		if err != nil {
			t.Fatalf("load locked OCI chart: %v", err)
		}
		if string(loaded) != string(archive) {
			t.Fatalf("loaded OCI archive = %q, want %q", loaded, archive)
		}
	}
	if fetchCount != 1 {
		t.Fatalf("OCI fetch count = %d, want one fetch followed by a cache hit", fetchCount)
	}
	if got := files.files[testLockFilename].Data; string(got) != string(lockData) {
		t.Fatalf("locked fetch changed mcl.lock: got %s, want %s", got, lockData)
	}
}

func TestLockedFetchRejectsUnavailableOrMismatchedContent(t *testing.T) {
	t.Parallel()
	identity := testToolchainIdentity()
	source := testManifestSource
	lockedContent := []byte("locked content")
	digest := contentutil.SHA256(lockedContent)
	lockData, err := json.Marshal(lockFile{Version: 3, Toolchain: identity.lock(), Manifests: map[string]string{source: digest}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		content     []byte
		fetchErr    error
		wantErrText string
	}{
		{name: "network unavailable", fetchErr: errors.New("network unavailable"), wantErrText: "network unavailable"},
		{name: "digest mismatch", content: []byte("different content"), wantErrText: "does not match its locked SHA-256 digest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			files := &atomicMapFS{files: fstest.MapFS{testLockFilename: &fstest.MapFile{Data: lockData}}}
			lock, err := OpenLockedFetchWithFS(t.Context(), "/virtual/project", identity, files)
			if err != nil {
				t.Fatalf("open locked fetch filesystem: %v", err)
			}
			lock.remoteFetcher = func(context.Context, string, string) ([]byte, error) {
				return append([]byte(nil), test.content...), test.fetchErr
			}
			if _, err := lock.LoadManifest(source); err == nil || !strings.Contains(err.Error(), test.wantErrText) {
				t.Fatalf("LoadManifest() error = %v, want text %q", err, test.wantErrText)
			}
			if _, exists := files.files[".mcl/cache/sha256/"+digest]; exists {
				t.Fatal("failed locked fetch wrote content to the cache")
			}
			if got := files.files[testLockFilename].Data; string(got) != string(lockData) {
				t.Fatalf("failed locked fetch changed mcl.lock: got %s, want %s", got, lockData)
			}
		})
	}
}

func TestLockedFetchRejectsUnlockedDependency(t *testing.T) {
	t.Parallel()
	identity := testToolchainIdentity()
	lockData, err := json.Marshal(lockFile{Version: 3, Toolchain: identity.lock(), Manifests: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	files := &atomicMapFS{files: fstest.MapFS{testLockFilename: &fstest.MapFile{Data: lockData}}}
	lock, err := OpenLockedFetchWithFS(t.Context(), "/virtual/project", identity, files)
	if err != nil {
		t.Fatalf("open locked fetch filesystem: %v", err)
	}
	if _, err := lock.LoadManifest("https://example.test/unlocked.yaml"); err == nil || !strings.Contains(err.Error(), "is not locked") {
		t.Fatalf("LoadManifest() error = %v, want unlocked dependency error", err)
	}
	if got := files.files[testLockFilename].Data; string(got) != string(lockData) {
		t.Fatalf("locked fetch changed mcl.lock: got %s, want %s", got, lockData)
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
	if _, err := os.Stat(filepath.Join(root, testLockFilename)); !os.IsNotExist(err) {
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
	lock.manifests[testManifestSource] = digest
	if err := lock.writeCache(digest, content); err != nil {
		t.Fatalf("write dependency cache: %v", err)
	}
	if err := lock.Save(); err != nil {
		t.Fatalf("save dependency lock: %v", err)
	}
	if _, exists := files.files[testLockFilename]; !exists {
		t.Fatal("atomic filesystem did not receive mcl.lock")
	}
	if _, exists := files.files[".mcl/cache/sha256/"+digest]; !exists {
		t.Fatal("atomic filesystem did not receive dependency cache content")
	}
	loaded, err := OpenWithFS(t.Context(), "/another/project", false, testToolchainIdentity(), files)
	if err != nil {
		t.Fatalf("reopen writable filesystem: %v", err)
	}
	read, err := loaded.LoadManifest(testManifestSource)
	if err != nil {
		t.Fatalf("load dependency cache: %v", err)
	}
	if string(read) != string(content) {
		t.Fatalf("loaded content = %q, want %q", read, content)
	}
}

func TestUpdatePreservesDependenciesForOtherEntryPoints(t *testing.T) {
	t.Parallel()

	files := &atomicMapFS{files: make(fstest.MapFS)}
	initial, err := OpenWithFS(t.Context(), "/virtual/project", true, testToolchainIdentity(), files)
	if err != nil {
		t.Fatalf("open initial update: %v", err)
	}
	previousURL := "https://example.test/previous-chart.tgz"
	previousDigest := contentutil.SHA256([]byte("previous chart"))
	initial.charts[previousURL] = previousDigest
	if err := initial.Save(); err != nil {
		t.Fatalf("save initial lock: %v", err)
	}

	updated, err := OpenWithFS(t.Context(), "/virtual/project", true, testToolchainIdentity(), files)
	if err != nil {
		t.Fatalf("open update for another entrypoint: %v", err)
	}
	updated.manifests["https://example.test/next-manifest.yaml"] = contentutil.SHA256([]byte("next manifest"))
	if err := updated.Save(); err != nil {
		t.Fatalf("save updated lock: %v", err)
	}

	loaded, err := OpenWithFS(t.Context(), "/virtual/project", false, testToolchainIdentity(), files)
	if err != nil {
		t.Fatalf("reopen updated lock: %v", err)
	}
	if loaded.charts[previousURL] != previousDigest {
		t.Fatal("update removed the previous entrypoint's locked chart")
	}
	if _, exists := loaded.manifests["https://example.test/next-manifest.yaml"]; !exists {
		t.Fatal("update removed the new entrypoint's locked manifest")
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
	source := testManifestSource
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
	lockPath := filepath.Join(root, testLockFilename)
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

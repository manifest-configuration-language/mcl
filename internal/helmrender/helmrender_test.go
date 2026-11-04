package helmrender

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/manifest-configuration-language/mcl/internal/content"
	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"helm.sh/helm/v4/pkg/chart/common"
	v2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/provenance"
)

func TestLoadUsesLockedHelmDependenciesForAliasesOffline(t *testing.T) {
	t.Parallel()

	const (
		parentURL        = "https://example.test/cortex.tgz"
		childURL         = "https://example.test/memcached-6.14.0.tgz"
		memcachedName    = "memcached"
		memcachedVersion = "6.14.0"
	)
	child := &v2.Chart{Metadata: &v2.Metadata{Name: memcachedName, Version: memcachedVersion, APIVersion: v2.APIVersionV2}}
	childArchive := testChartArchive(t, child)
	aliases := []string{"memcached-frontend", "memcached-blocks-index", "memcached-blocks", "memcached-blocks-metadata", "memcached-parquet-labels"}
	requirements := make([]*v2.Dependency, 0, len(aliases))
	lockedRequirements := make([]*v2.Dependency, 0, len(aliases))
	for _, alias := range aliases {
		requirements = append(requirements, &v2.Dependency{
			Name: memcachedName, Alias: alias, Version: memcachedVersion, Repository: "https://charts.example.test/bitnami",
		})
		lockedRequirements = append(lockedRequirements, &v2.Dependency{
			Name: memcachedName, Version: memcachedVersion, Repository: "https://charts.example.test/bitnami",
		})
	}
	parent := &v2.Chart{
		Metadata: &v2.Metadata{Name: "cortex", Version: "3.3.8", APIVersion: v2.APIVersionV2, Dependencies: requirements},
		Lock:     &v2.Lock{Digest: chartLockDigest(t, requirements, lockedRequirements), Dependencies: lockedRequirements},
	}
	parent.SetDependencies(child)
	parentArchive := testChartArchive(t, parent)
	parentDigest := content.SHA256(parentArchive)
	childDigest := content.SHA256(childArchive)
	chartDependencies := make(map[string]map[string]testChartDependencyLock, 1)
	chartDependencies[parentDigest] = make(map[string]testChartDependencyLock, len(aliases))
	for _, alias := range aliases {
		chartDependencies[parentDigest][alias] = testChartDependencyLock{Source: childURL, Version: memcachedVersion}
	}
	lockData, err := json.Marshal(testDependencyLock{
		Version:           4,
		Toolchain:         testDependencyToolchainLock{},
		Charts:            map[string]string{parentURL: parentDigest, childURL: childDigest},
		ChartDependencies: chartDependencies,
	})
	if err != nil {
		t.Fatal(err)
	}
	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		testDependencyLockFileName:          {Data: lockData},
		".mcl/cache/sha256/" + parentDigest: {Data: parentArchive},
		".mcl/cache/sha256/" + childDigest:  {Data: childArchive},
	}
	lock, err := dependency.OpenWithFS(t.Context(), projectRoot, false, dependency.ToolchainIdentity{}, files)
	if err != nil {
		t.Fatalf("open dependency lock: %v", err)
	}
	loaded, err := Load(LoadRequest{
		Source: parentURL, Version: "3.3.8", ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"), Lock: lock,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	loadedChart := loaded.source.(*v2.Chart)
	if loaded.ContentDigest == loaded.RemoteDigest {
		t.Fatal("Load() content digest does not include the locked dependencies")
	}
	if got := len(loadedChart.Dependencies()); got != len(aliases) {
		t.Fatalf("Load() dependencies = %d, want %d aliases", got, len(aliases))
	}
	for _, child := range loadedChart.Dependencies() {
		if child.Metadata.Name != memcachedName || child.Metadata.Version != memcachedVersion {
			t.Fatalf("loaded dependency metadata = %#v", child.Metadata)
		}
	}
}

func TestLoadPreservesLockedGrandchildFromBundledChart(t *testing.T) {
	t.Parallel()

	const (
		parentURL = "https://example.test/bundled-parent.tgz"
		grandURL  = "https://example.test/grandchild-1.2.0.tgz"
	)
	grandchild := &v2.Chart{Metadata: &v2.Metadata{Name: testGrandchildName, Version: testChildVersion, APIVersion: v2.APIVersionV2}}
	grandchildArchive := testChartArchive(t, grandchild)
	grandRequirement := &v2.Dependency{Name: testGrandchildName, Version: "^1.0.0", Repository: testChartRepository}
	lockedGrandchild := &v2.Dependency{Name: testGrandchildName, Version: testChildVersion, Repository: testChartRepository}
	child := &v2.Chart{
		Metadata: &v2.Metadata{
			Name: testChildChartName, Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2,
			Dependencies: []*v2.Dependency{grandRequirement},
		},
		Lock: &v2.Lock{
			Digest:       chartLockDigest(t, []*v2.Dependency{grandRequirement}, []*v2.Dependency{lockedGrandchild}),
			Dependencies: []*v2.Dependency{lockedGrandchild},
		},
	}
	parentRequirement := &v2.Dependency{Name: testChildChartName, Version: testExactDependencyVersion}
	parent := &v2.Chart{
		Metadata: &v2.Metadata{
			Name: testParentChartName, Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2,
			Dependencies: []*v2.Dependency{parentRequirement},
		},
	}
	parent.SetDependencies(child)
	parentArchive := testChartArchive(t, parent)
	parentDigest := content.SHA256(parentArchive)
	loadedParent, err := loader.LoadArchive(bytes.NewReader(parentArchive))
	if err != nil {
		t.Fatalf("load fixture parent archive: %v", err)
	}
	bundledChildDigest := chartRawDigest(loadedParent.Dependencies()[0])
	grandchildDigest := content.SHA256(grandchildArchive)
	lockData, err := json.Marshal(testDependencyLock{
		Version: 4, Toolchain: testDependencyToolchainLock{},
		Charts: map[string]string{parentURL: parentDigest, grandURL: grandchildDigest},
		ChartDependencies: map[string]map[string]testChartDependencyLock{
			bundledChildDigest: {testGrandchildName: {Source: grandURL, Version: testChildVersion}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		testDependencyLockFileName:              {Data: lockData},
		".mcl/cache/sha256/" + parentDigest:     {Data: parentArchive},
		".mcl/cache/sha256/" + grandchildDigest: {Data: grandchildArchive},
	}
	lock, err := dependency.OpenWithFS(t.Context(), projectRoot, false, dependency.ToolchainIdentity{}, files)
	if err != nil {
		t.Fatalf("open dependency lock: %v", err)
	}
	loaded, err := Load(LoadRequest{
		Source: parentURL, Version: testExactDependencyVersion, ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"), Lock: lock,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	children := loaded.source.(*v2.Chart).Dependencies()
	if len(children) != 1 || len(children[0].Dependencies()) != 1 || children[0].Dependencies()[0].Metadata.Name != testGrandchildName {
		t.Fatalf("loaded dependency tree = %#v, want bundled child with locked grandchild", children)
	}
}

func TestLoadRejectsInconsistentHelmChartLock(t *testing.T) {
	t.Parallel()

	requirement := &v2.Dependency{Name: testChildChartName, Version: testExactDependencyVersion, Repository: testChartRepository}
	chart := &v2.Chart{
		Metadata: &v2.Metadata{Name: testParentChartName, Version: testExactDependencyVersion, Dependencies: []*v2.Dependency{requirement}},
		Lock:     &v2.Lock{Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000", Dependencies: []*v2.Dependency{{Name: testChildChartName, Version: testExactDependencyVersion, Repository: testChartRepository}}},
	}
	_, err := loadLockedDependencies(chart, content.SHA256([]byte(testParentChartName)), &dependency.Lock{})
	if err == nil || !strings.Contains(err.Error(), "chart.lock digest does not match") {
		t.Fatalf("loadLockedDependencies() error = %v, want inconsistent Chart.lock error", err)
	}
}

func TestLoadUsesCompleteBundleWhenChartLockDigestIsStale(t *testing.T) {
	t.Parallel()

	const (
		parentURL  = "https://example.test/stale-lock-parent.tgz"
		firstName  = "first-child"
		secondName = "second-child"
	)
	firstRequirement := &v2.Dependency{Name: firstName, Version: "^1.0.0", Repository: "https://charts.example.test/first"}
	secondRequirement := &v2.Dependency{Name: secondName, Version: "^2.0.0", Repository: "https://charts.example.test/second"}
	parent := &v2.Chart{
		Metadata: &v2.Metadata{
			Name: testParentChartName, Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2,
			Dependencies: []*v2.Dependency{firstRequirement, secondRequirement},
		},
		Lock: &v2.Lock{
			Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Dependencies: []*v2.Dependency{
				{Name: firstName, Version: testChildVersion, Repository: firstRequirement.Repository},
				{Name: secondName, Version: "2.3.0", Repository: secondRequirement.Repository},
			},
		},
	}
	parent.SetDependencies(
		&v2.Chart{Metadata: &v2.Metadata{Name: firstName, Version: testChildVersion, APIVersion: v2.APIVersionV2}},
		&v2.Chart{Metadata: &v2.Metadata{Name: secondName, Version: "2.3.0", APIVersion: v2.APIVersionV2}},
	)
	parentArchive := testChartArchive(t, parent)
	parentDigest := content.SHA256(parentArchive)
	lockData, err := json.Marshal(testDependencyLock{
		Version:   4,
		Toolchain: testDependencyToolchainLock{},
		Charts:    map[string]string{parentURL: parentDigest},
	})
	if err != nil {
		t.Fatal(err)
	}
	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		testDependencyLockFileName:          {Data: lockData},
		".mcl/cache/sha256/" + parentDigest: {Data: parentArchive},
	}
	lock, err := dependency.OpenWithFS(t.Context(), projectRoot, false, dependency.ToolchainIdentity{}, files)
	if err != nil {
		t.Fatalf("open dependency lock: %v", err)
	}
	loaded, err := Load(LoadRequest{
		Source: parentURL, Version: testExactDependencyVersion, ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"), Lock: lock,
	})
	if err != nil {
		t.Fatalf("Load() rejected complete archive with stale Chart.lock: %v", err)
	}
	if len(loaded.source.(*v2.Chart).Dependencies()) != 2 || loaded.ContentDigest != loaded.RemoteDigest {
		t.Fatal("Load() did not preserve the archive-pinned bundled dependencies")
	}
}

func TestValidateHelmChartLockAcceptsLegacyV1DigestOnly(t *testing.T) {
	t.Parallel()

	requirements := []*v2.Dependency{{Name: testChildChartName, Version: testExactDependencyVersion, Repository: testChartRepository}}
	legacyDigest := legacyChartLockDigest(t, requirements)
	chartLock := &v2.Lock{Digest: legacyDigest, Dependencies: []*v2.Dependency{{Name: testChildChartName, Version: testExactDependencyVersion, Repository: testChartRepository}}}
	if err := validateHelmChartLock(&v2.Metadata{APIVersion: v2.APIVersionV1, Dependencies: requirements}, requirements, chartLock); err != nil {
		t.Fatalf("validateHelmChartLock() rejected legacy v1 digest: %v", err)
	}
	if err := validateHelmChartLock(&v2.Metadata{APIVersion: v2.APIVersionV2, Dependencies: requirements}, requirements, chartLock); err == nil {
		t.Fatal("validateHelmChartLock() accepted legacy digest for apiVersion v2")
	}
}

func TestLoadAllowsOnlyFullyBundledDependenciesWithoutChartLock(t *testing.T) {
	t.Parallel()

	const (
		parentURL = "https://example.test/parent.tgz"
		childName = testChildChartName
	)
	child := &v2.Chart{Metadata: &v2.Metadata{Name: childName, Version: testBundledChildVersion, APIVersion: v2.APIVersionV2}}
	dependencyRequirement := &v2.Dependency{Name: childName, Version: testExactDependencyVersion, Repository: testChartRepository}
	parent := &v2.Chart{
		Metadata: &v2.Metadata{Name: testParentChartName, Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2, Dependencies: []*v2.Dependency{dependencyRequirement}},
	}
	parent.SetDependencies(child)
	parentArchive := testChartArchive(t, parent)
	parentDigest := content.SHA256(parentArchive)
	lockData, err := json.Marshal(testDependencyLock{
		Version:   4,
		Toolchain: testDependencyToolchainLock{},
		Charts:    map[string]string{parentURL: parentDigest},
	})
	if err != nil {
		t.Fatal(err)
	}
	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		testDependencyLockFileName:          {Data: lockData},
		".mcl/cache/sha256/" + parentDigest: {Data: parentArchive},
	}
	lock, err := dependency.OpenWithFS(t.Context(), projectRoot, false, dependency.ToolchainIdentity{}, files)
	if err != nil {
		t.Fatalf("open dependency lock: %v", err)
	}
	loaded, err := Load(LoadRequest{
		Source: parentURL, Version: testExactDependencyVersion, ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"), Lock: lock,
	})
	if err != nil {
		t.Fatalf("Load() rejected fully bundled dependency without Chart.lock: %v", err)
	}
	if len(loaded.source.(*v2.Chart).Dependencies()) != 1 || loaded.ContentDigest != loaded.RemoteDigest {
		t.Fatal("Load() did not preserve the archive-pinned bundled dependency tree")
	}

	parent.SetDependencies()
	unresolvedArchive := testChartArchive(t, parent)
	unresolvedDigest := content.SHA256(unresolvedArchive)
	files[".mcl/cache/sha256/"+unresolvedDigest] = &fstest.MapFile{Data: unresolvedArchive}
	lockData, err = json.Marshal(testDependencyLock{
		Version:   4,
		Toolchain: testDependencyToolchainLock{},
		Charts:    map[string]string{parentURL: unresolvedDigest},
	})
	if err != nil {
		t.Fatal(err)
	}
	files[testDependencyLockFileName] = &fstest.MapFile{Data: lockData}
	lock, err = dependency.OpenWithFS(t.Context(), projectRoot, false, dependency.ToolchainIdentity{}, files)
	if err != nil {
		t.Fatalf("open lock for unresolved dependency: %v", err)
	}
	if _, err := Load(LoadRequest{
		Source: parentURL, Version: testExactDependencyVersion, ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"), Lock: lock,
	}); err == nil || !strings.Contains(err.Error(), "no matching bundled subchart and no Chart.lock") {
		t.Fatalf("Load() error = %v, want unresolved dependency error", err)
	}
}

func TestLoadKeepsBundledFileDependencyWithVersionMismatch(t *testing.T) {
	t.Parallel()

	const (
		parentURL = "https://example.test/scylla-manager.tgz"
		childName = "scylla"
	)
	child := &v2.Chart{Metadata: &v2.Metadata{Name: childName, Version: testBundledChildVersion, APIVersion: v2.APIVersionV2}}
	requirement := &v2.Dependency{Name: childName, Version: testExactDependencyVersion, Repository: "file://../scylla"}
	locked := &v2.Dependency{Name: childName, Version: testExactDependencyVersion, Repository: "file://../scylla"}
	parent := &v2.Chart{
		Metadata: &v2.Metadata{Name: "scylla-manager", Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2, Dependencies: []*v2.Dependency{requirement}},
		Lock:     &v2.Lock{Digest: chartLockDigest(t, []*v2.Dependency{requirement}, []*v2.Dependency{locked}), Dependencies: []*v2.Dependency{locked}},
	}
	parent.SetDependencies(child)
	parentArchive := testChartArchive(t, parent)
	parentDigest := content.SHA256(parentArchive)
	lockData, err := json.Marshal(testDependencyLock{
		Version:   4,
		Toolchain: testDependencyToolchainLock{},
		Charts:    map[string]string{parentURL: parentDigest},
	})
	if err != nil {
		t.Fatal(err)
	}
	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		testDependencyLockFileName:          {Data: lockData},
		".mcl/cache/sha256/" + parentDigest: {Data: parentArchive},
	}
	lock, err := dependency.OpenWithFS(t.Context(), projectRoot, false, dependency.ToolchainIdentity{}, files)
	if err != nil {
		t.Fatalf("open dependency lock: %v", err)
	}
	loaded, err := Load(LoadRequest{
		Source: parentURL, Version: testExactDependencyVersion, ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"), Lock: lock,
	})
	if err != nil {
		t.Fatalf("Load() rejected archive-pinned file dependency: %v", err)
	}
	if children := loaded.source.(*v2.Chart).Dependencies(); len(children) != 1 || children[0].Metadata.Version != testBundledChildVersion {
		t.Fatalf("loaded file dependency = %#v, want the archive-bundled chart", children)
	}
}

func TestLoadKeepsBundledDependencyWithEmptyRepository(t *testing.T) {
	t.Parallel()

	const (
		parentURL = "https://example.test/empty-repository-parent.tgz"
		childName = "scylla"
	)
	child := &v2.Chart{Metadata: &v2.Metadata{Name: childName, Version: testBundledChildVersion, APIVersion: v2.APIVersionV2}}
	requirement := &v2.Dependency{Name: childName, Version: testExactDependencyVersion}
	locked := &v2.Dependency{Name: childName, Version: testExactDependencyVersion}
	parent := &v2.Chart{
		Metadata: &v2.Metadata{Name: testParentChartName, Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2, Dependencies: []*v2.Dependency{requirement}},
		Lock:     &v2.Lock{Digest: chartLockDigest(t, []*v2.Dependency{requirement}, []*v2.Dependency{locked}), Dependencies: []*v2.Dependency{locked}},
	}
	parent.SetDependencies(child)
	parentArchive := testChartArchive(t, parent)
	parentDigest := content.SHA256(parentArchive)
	lockData, err := json.Marshal(testDependencyLock{
		Version:   4,
		Toolchain: testDependencyToolchainLock{},
		Charts:    map[string]string{parentURL: parentDigest},
	})
	if err != nil {
		t.Fatal(err)
	}
	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		testDependencyLockFileName:          {Data: lockData},
		".mcl/cache/sha256/" + parentDigest: {Data: parentArchive},
	}
	lock, err := dependency.OpenWithFS(t.Context(), projectRoot, false, dependency.ToolchainIdentity{}, files)
	if err != nil {
		t.Fatalf("open dependency lock: %v", err)
	}
	loaded, err := Load(LoadRequest{
		Source: parentURL, Version: testExactDependencyVersion, ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"), Lock: lock,
	})
	if err != nil {
		t.Fatalf("Load() rejected archive-bundled empty-repository dependency: %v", err)
	}
	if children := loaded.source.(*v2.Chart).Dependencies(); len(children) != 1 || children[0].Metadata.Name != childName {
		t.Fatalf("loaded dependency = %#v, want the local bundled subchart", children)
	}
}

func TestLoadResolvesHelmRepositoryAliasFromChartLock(t *testing.T) {
	t.Parallel()

	const (
		parentURL = "https://example.test/alias-parent.tgz"
		childURL  = "https://example.test/child-1.2.0.tgz"
	)
	requirement := &v2.Dependency{Name: testChildChartName, Alias: "childAlias", Version: "^1.0.0", Repository: "@bitnami"}
	locked := &v2.Dependency{Name: testChildChartName, Version: testChildVersion, Repository: testChartRepository}
	resolvedRequirement := *requirement
	resolvedRequirement.Repository = testChartRepository
	parent := &v2.Chart{
		Metadata: &v2.Metadata{Name: testParentChartName, Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2, Dependencies: []*v2.Dependency{requirement}},
		Lock:     &v2.Lock{Digest: chartLockDigest(t, []*v2.Dependency{&resolvedRequirement}, []*v2.Dependency{locked}), Dependencies: []*v2.Dependency{locked}},
	}
	parentArchive := testChartArchive(t, parent)
	childArchive := testChartArchive(t, &v2.Chart{Metadata: &v2.Metadata{Name: testChildChartName, Version: testChildVersion, APIVersion: v2.APIVersionV2}})
	parentDigest := content.SHA256(parentArchive)
	childDigest := content.SHA256(childArchive)
	lockData, err := json.Marshal(testDependencyLock{
		Version:   4,
		Toolchain: testDependencyToolchainLock{},
		Charts:    map[string]string{parentURL: parentDigest, childURL: childDigest},
		ChartDependencies: map[string]map[string]testChartDependencyLock{
			parentDigest: {"childAlias": {Source: childURL, Version: testChildVersion}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		testDependencyLockFileName:          {Data: lockData},
		".mcl/cache/sha256/" + parentDigest: {Data: parentArchive},
		".mcl/cache/sha256/" + childDigest:  {Data: childArchive},
	}
	lock, err := dependency.OpenWithFS(t.Context(), projectRoot, false, dependency.ToolchainIdentity{}, files)
	if err != nil {
		t.Fatalf("open dependency lock: %v", err)
	}
	loaded, err := Load(LoadRequest{
		Source: parentURL, Version: testExactDependencyVersion, ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"), Lock: lock,
	})
	if err != nil {
		t.Fatalf("Load() rejected Helm repository alias resolved by Chart.lock: %v", err)
	}
	if children := loaded.source.(*v2.Chart).Dependencies(); len(children) != 1 || children[0].Metadata.Version != testChildVersion {
		t.Fatalf("loaded alias dependency = %#v, want locked package version", children)
	}
}

type testChartDependencyLock struct {
	Source  string `json:"source"`
	Version string `json:"version"`
}

type testDependencyToolchainLock struct {
	MCL            string `json:"mcl"`
	Revision       string `json:"revision"`
	Modified       string `json:"modified"`
	Go             string `json:"go"`
	Helm           string `json:"helm"`
	KustomizeAPI   string `json:"kustomizeAPI"`
	KustomizeKYAML string `json:"kustomizeKYAML"`
}

type testDependencyLock struct {
	Version           int                                           `json:"version"`
	Toolchain         testDependencyToolchainLock                   `json:"toolchain"`
	Charts            map[string]string                             `json:"charts"`
	ChartDependencies map[string]map[string]testChartDependencyLock `json:"chartDependencies"`
}

func testChartArchive(t *testing.T, chart *v2.Chart) []byte {
	t.Helper()
	archivePath, err := chartutil.Save(chart, t.TempDir())
	if err != nil {
		t.Fatalf("save chart archive: %v", err)
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read chart archive: %v", err)
	}

	return archive
}

func chartLockDigest(t *testing.T, requirements, locked []*v2.Dependency) string {
	t.Helper()
	data, err := json.Marshal([2][]*v2.Dependency{requirements, locked})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := provenance.Digest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	return "sha256:" + digest
}

func legacyChartLockDigest(t *testing.T, requirements []*v2.Dependency) string {
	t.Helper()
	data, err := json.Marshal(map[string][]*v2.Dependency{"dependencies": requirements})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := provenance.Digest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	return "sha256:" + digest
}

const (
	localTestTemplatePath      = "templates/configmap.yaml"
	testDependencyLockFileName = "mcl.lock"
	testParentChartName        = "parent"
	testBundledChildVersion    = "0.0.0"
	testChildVersion           = "1.2.0"
	testChartName              = "example"
	testChildChartName         = "child"
	testGrandchildName         = "grandchild"
	testChartRepository        = "https://charts.example.test"
	testChartVersion           = "0.1.0"
	testExactDependencyVersion = "1.0.0"
	testIgnoredValue           = "ignored"
	testKubeVersion            = "1.36.0"
)

func TestLoadReadsRootRelativeProjectFS(t *testing.T) {
	t.Parallel()
	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := fstest.MapFS{
		"charts/app/Chart.yaml":          &fstest.MapFile{Data: []byte("apiVersion: v2\nname: app\nversion: 0.1.0\n")},
		"charts/app/templates/test.yaml": &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\n")},
	}
	loaded, err := Load(LoadRequest{
		Source: "./charts/app", ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"), SourceFS: project,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Origin != "helm:charts/app" || loaded.LocalPath != filepath.Join(projectRoot, "charts", "app") {
		t.Fatalf("Load() source identity = (%q, %q)", loaded.Origin, loaded.LocalPath)
	}
	loadedChart, ok := loaded.source.(*v2.Chart)
	if !ok || len(loadedChart.Templates) != 1 || !strings.Contains(string(loadedChart.Templates[0].Data), "name: test") {
		t.Fatalf("Load() did not preserve chart templates: %#v", loadedChart)
	}
}

func TestLoadReadsLocalChartIntoMemory(t *testing.T) {
	t.Parallel()
	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chartRoot := filepath.Join(projectRoot, "charts", "app")
	if err := os.MkdirAll(filepath.Join(chartRoot, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"Chart.yaml":          "apiVersion: v2\nname: app\nversion: 0.1.0\n",
		localTestTemplatePath: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n",
	} {
		if err := os.WriteFile(filepath.Join(chartRoot, filepath.FromSlash(name)), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	loaded, err := Load(LoadRequest{
		Source: "./charts/app", ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	loadedChart := loaded.source.(*v2.Chart)
	if len(loadedChart.Templates) != 1 || loadedChart.Templates[0].Name != localTestTemplatePath {
		t.Fatalf("Load() templates = %#v", loadedChart.Templates)
	}
	if loaded.ContentDigest == "" || loaded.LocalPath != chartRoot {
		t.Fatalf("Load() chart identity = (digest %q, path %q)", loaded.ContentDigest, loaded.LocalPath)
	}
}

func TestLoadReadsProjectFSWithoutMaterializingIgnoredChartFiles(t *testing.T) {
	t.Parallel()

	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := fstest.MapFS{
		"charts/app/.helmignore":              &fstest.MapFile{Data: []byte("ignored.txt\n")},
		"charts/app/Chart.yaml":               &fstest.MapFile{Data: []byte("apiVersion: v2\nname: app\nversion: 0.1.0\n")},
		"charts/app/ignored.txt":              &fstest.MapFile{Data: []byte("ignored")},
		"charts/app/templates/.hidden.yaml":   &fstest.MapFile{Data: []byte("invalid template data")},
		"charts/app/templates/configmap.yaml": &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n")},
	}
	request := LoadRequest{
		Source: "./charts/app", ProjectRoot: projectRoot,
		SourcePath: filepath.Join(projectRoot, "main.mcl"), SourceFS: project,
	}

	loaded, err := Load(request)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	loadedChart := loaded.source.(*v2.Chart)
	if len(loadedChart.Templates) != 1 || loadedChart.Templates[0].Name != localTestTemplatePath {
		t.Fatalf("Load() templates = %#v, want only the non-ignored template", loadedChart.Templates)
	}
	for _, file := range loadedChart.Files {
		if file.Name == "ignored.txt" {
			t.Fatal("Load() included a file excluded by .helmignore")
		}
	}

	project["charts/app/ignored.txt"].Data = []byte("changed ignored input")
	changed, err := Load(request)
	if err != nil {
		t.Fatalf("Load() after ignored input change: %v", err)
	}
	if loaded.ContentDigest != changed.ContentDigest {
		t.Fatal("Load() content digest changed after an ignored chart input changed")
	}
}

func TestRenderedFieldMappingsWithBlockTemplate(t *testing.T) {
	t.Parallel()
	templateSource := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: example\ndata:\n  value: {{ block \"config-value\" . }}{{ .Values.value }}{{ end }}\n")
	chartSource := Chart{source: &v2.Chart{
		Metadata:  &v2.Metadata{Name: testChartName, Version: testChartVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{Name: localTestTemplatePath, Data: templateSource}},
	}}
	values := map[string]any{"Values": map[string]any{"value": "rendered"}}

	rendered, err := Render(t.Context(), chartSource, values)
	if err != nil {
		t.Fatalf("render valid block template: %v", err)
	}
	if !strings.Contains(rendered["example/templates/configmap.yaml"], "value: rendered") {
		t.Fatalf("rendered output did not contain the block value: %q", rendered)
	}

	if _, err := RenderedFieldMappings(t.Context(), chartSource, values, rendered, func(string) ([]Document, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("instrumentation failed for valid block template: %v", err)
	}
}

func TestCloneChartWithoutNotesDoesNotMutateSource(t *testing.T) {
	t.Parallel()
	notes := &common.File{Name: "templates/NOTES.txt", Data: []byte("notes")}
	manifest := &common.File{Name: localTestTemplatePath, Data: []byte("{{ .message }}")}
	chart := &v2.Chart{
		Metadata:  &v2.Metadata{Name: "test", Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{notes, manifest},
	}
	source := Chart{source: chart}

	first, err := Render(t.Context(), source, map[string]any{"message": "stable"})
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	second, err := Render(t.Context(), source, map[string]any{"message": "stable"})
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if !maps.Equal(first, second) {
		t.Fatalf("consecutive renders differ: first=%v second=%v", first, second)
	}
	if len(chart.Templates) != 2 || chart.Templates[0] != notes || chart.Templates[1] != manifest {
		t.Fatalf("source templates changed after render: %#v", chart.Templates)
	}
}

func TestRenderVerifiedTracksNondeterministicFunctions(t *testing.T) {
	t.Parallel()
	chart := Chart{source: &v2.Chart{
		Metadata:  &v2.Metadata{Name: "test", Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{Name: localTestTemplatePath, Data: []byte("{{- $ca := genCA \"test\" 365 -}}\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: stable\n")}},
	}}
	verified, err := RenderVerified(t.Context(), chart, map[string]any{}, false)
	if err != nil {
		t.Fatalf("render verified chart with unused genCA result: %v", err)
	}
	if !verified.UsedNondeterminism {
		t.Fatal("RenderVerified() did not report genCA call")
	}
	if verified.Templates["test/"+localTestTemplatePath] == "" {
		t.Fatal("RenderVerified() did not return rendered template")
	}
}

func TestRenderVerifiedRejectsNondeterministicManifestAndCanAllowIt(t *testing.T) {
	t.Parallel()
	chart := Chart{source: &v2.Chart{
		Metadata:  &v2.Metadata{Name: "test", Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{Name: "templates/secret.yaml", Data: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: generated\ndata:\n  ca.crt: {{ (genCA \"test\" 365).Cert | b64enc | quote }}\n")}},
	}}
	if _, err := RenderVerified(t.Context(), chart, map[string]any{}, false); err == nil || !strings.Contains(err.Error(), "MCL-HELM-NONDETERMINISTIC") || !strings.Contains(err.Error(), "test/templates/secret.yaml") {
		t.Fatalf("RenderVerified() error = %v, want safe nondeterminism diagnostic with template path", err)
	}
	allowed, err := RenderVerified(t.Context(), chart, map[string]any{}, true)
	if err != nil {
		t.Fatalf("RenderVerified() with nondeterministic output allowed: %v", err)
	}
	if !allowed.UsedNondeterminism || !strings.Contains(allowed.Templates["test/templates/secret.yaml"], "ca.crt:") {
		t.Fatal("allowed render did not return generated Secret")
	}
}

func TestRenderVerifiedSupportsNondeterministicNonStringResults(t *testing.T) {
	t.Parallel()
	chart := Chart{source: &v2.Chart{
		Metadata:  &v2.Metadata{Name: "test", Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{Name: localTestTemplatePath, Data: []byte("{{- define \"random\" }}{{ randAlpha 4 }}{{ end }}\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: generated\ndata:\n  number: {{ randInt 1 10 | quote }}\n  uuid: {{ uuidv4 | quote }}\n  shuffled: {{ shuffle \"abcd\" | quote }}\n  included: {{ include \"random\" . | quote }}\n  templated: {{ tpl \"{{ randAlphaNum 4 }}\" . | quote }}\n")}},
	}}
	result, err := RenderVerified(t.Context(), chart, map[string]any{}, true)
	if err != nil {
		t.Fatalf("RenderVerified() with non-string Sprig results: %v", err)
	}
	if !result.UsedNondeterminism {
		t.Fatal("RenderVerified() did not observe random functions")
	}
}

func TestRenderUsesHermeticDateFunctions(t *testing.T) {
	t.Parallel()
	chart := Chart{source: &v2.Chart{
		Metadata:  &v2.Metadata{Name: "test", Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{Name: localTestTemplatePath, Data: []byte("date: {{ date \"2006-01-02 15:04 MST\" 86399 | quote }}\ndateInZone: {{ dateInZone \"2006-01-02 15:04 MST\" 86399 \"Asia/Tokyo\" | quote }}\nhtmlDate: {{ htmlDate 86399 | quote }}\nhtmlDateInZone: {{ htmlDateInZone 86399 \"Asia/Tokyo\" | quote }}\nparsed: {{ date \"2006-01-02 15:04 MST\" (toDate \"2006-01-02 15:04\" \"1970-01-01 12:34\") | quote }}\n")}},
	}}
	rendered, err := Render(t.Context(), chart, map[string]any{})
	if err != nil {
		t.Fatalf("render deterministic date functions: %v", err)
	}
	output := rendered["test/"+localTestTemplatePath]
	for _, expected := range []string{
		`date: "1970-01-01 23:59 UTC"`,
		`dateInZone: "1970-01-02 08:59 JST"`,
		`htmlDate: "1970-01-01"`,
		`htmlDateInZone: "1970-01-02"`,
		`parsed: "1970-01-01 12:34 UTC"`,
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("rendered date output %q does not contain %q", output, expected)
		}
	}

	chart.source.(*v2.Chart).Templates[0].Data = []byte(`{{ date "2006-01-02" "invalid" }}`)
	if _, err := Render(t.Context(), chart, map[string]any{}); err == nil || !strings.Contains(err.Error(), "date does not accept string") {
		t.Fatalf("Render() error = %v, want invalid date argument error", err)
	}
	chart.source.(*v2.Chart).Templates[0].Data = []byte(`{{ date_in_zone "2006-01-02" "invalid" "UTC" }}`)
	if _, err := Render(t.Context(), chart, map[string]any{}); err == nil || !strings.Contains(err.Error(), "date does not accept string") {
		t.Fatalf("Render() error = %v, want date_in_zone to reject implicit current time", err)
	}
	for _, zone := range []string{"Local", "not/a-zone"} {
		chart.source.(*v2.Chart).Templates[0].Data = []byte(`{{ date_in_zone "2006-01-02" 0 ` + fmt.Sprintf("%q", zone) + ` }}`)
		if _, err := Render(t.Context(), chart, map[string]any{}); err == nil {
			t.Errorf("Render() with time zone %q succeeded, want an error", zone)
		}
	}
	chart.source.(*v2.Chart).Templates[0].Data = []byte(`{{ durationRound .Values.timestamp }}`)
	if _, err := Render(t.Context(), chart, map[string]any{"Values": map[string]any{"timestamp": time.Unix(0, 0)}}); err == nil || !strings.Contains(err.Error(), "durationRound with time.Time is disabled") {
		t.Fatalf("Render() error = %v, want hermetic durationRound error", err)
	}
}

func TestRenderPreservesHelmJSONFunctions(t *testing.T) {
	t.Parallel()
	chart := Chart{source: &v2.Chart{
		Metadata:  &v2.Metadata{Name: "test", Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{Name: localTestTemplatePath, Data: []byte("array: {{ toJson (fromJson \"[1,2]\") | quote }}\\ninvalid: {{ (fromJson \"{\").Error | quote }}\\n")}},
	}}
	rendered, err := Render(t.Context(), chart, map[string]any{})
	if err != nil {
		t.Fatalf("render Helm JSON functions: %v", err)
	}
	output := rendered["test/"+localTestTemplatePath]
	if !strings.Contains(output, `array: "{\"Error\":\"json: cannot unmarshal array`) || !strings.Contains(output, `invalid: "unexpected end of JSON input"`) {
		t.Fatalf("Helm JSON function output = %q, want Helm's map result and tolerant parse error", output)
	}
}

func TestRenderSortsSprigKeysAndValues(t *testing.T) {
	t.Parallel()
	chart := Chart{source: &v2.Chart{
		Metadata:  &v2.Metadata{Name: "test", Version: testExactDependencyVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{Name: localTestTemplatePath, Data: []byte("keys: {{ keys .Values.first .Values.second | join \",\" | quote }}\nvalues: {{ values .Values.values | join \",\" | quote }}\n")}},
	}}
	values := map[string]any{"Values": map[string]any{
		"first":  map[string]any{"b": testIgnoredValue, "a": testIgnoredValue},
		"second": map[string]any{"c": testIgnoredValue, "b": testIgnoredValue},
		"values": map[string]any{"z": "last", "a": "first"},
	}}
	rendered, err := Render(t.Context(), chart, values)
	if err != nil {
		t.Fatalf("render sorted Sprig map functions: %v", err)
	}
	output := rendered["test/"+localTestTemplatePath]
	for _, expected := range []string{`keys: "a,b,b,c"`, `values: "first,last"`} {
		if !strings.Contains(output, expected) {
			t.Errorf("rendered output %q does not contain %q", output, expected)
		}
	}
}

func TestTrustManagerV0240HelmCertNondeterminism(t *testing.T) {
	t.Parallel()
	archive, err := os.ReadFile("testdata/trust-manager-v0.24.0.tgz")
	if err != nil {
		t.Fatal(err)
	}
	const trustManagerChartDigest = "0de4bbef2d1a013bd4a91c6f59a9cb0273dda9a785e018a9a16e8f2b096af823"
	if got := content.SHA256(archive); got != trustManagerChartDigest {
		t.Fatalf("trust-manager v0.24.0 chart digest = %s, want %s", got, trustManagerChartDigest)
	}
	loaded, err := loader.LoadArchive(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("load unmodified trust-manager chart: %v", err)
	}
	if loaded.Metadata == nil || loaded.Metadata.Version != "v0.24.0" {
		t.Fatalf("trust-manager chart version = %#v, want v0.24.0", loaded.Metadata)
	}
	for _, test := range []struct {
		name    string
		enabled bool
		allow   bool
		wantErr bool
	}{
		{name: "helmCert disabled", enabled: false},
		{name: "helmCert enabled rejects different outputs", enabled: true, wantErr: true},
		{name: "helmCert enabled allows nondeterministic output", enabled: true, allow: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			prepared, err := Prepare(Chart{source: loaded}, RenderOptions{
				KubeVersion: "1.36.0", APIVersions: []string{}, Values: map[string]any{
					"app": map[string]any{"webhook": map[string]any{"tls": map[string]any{"helmCert": map[string]any{"enabled": test.enabled}}}},
				},
			}, t.TempDir())
			if err != nil {
				t.Fatalf("prepare trust-manager values: %v", err)
			}
			result, err := RenderVerified(t.Context(), prepared.Chart, prepared.Values, test.allow)
			if (err != nil) != test.wantErr {
				t.Fatalf("RenderVerified() error = %v, want error %v", err, test.wantErr)
			}
			if test.wantErr {
				if !strings.Contains(err.Error(), "MCL-HELM-NONDETERMINISTIC") || !strings.Contains(err.Error(), "trust-manager/templates/") {
					t.Fatalf("nondeterministic error = %v, want stable code and template path", err)
				}

				return
			}
			if len(result.Templates) == 0 {
				t.Fatal("RenderVerified() returned no rendered templates")
			}
		})
	}
}

func BenchmarkRenderVerified(b *testing.B) {
	for _, benchmark := range []struct {
		name     string
		template string
		allow    bool
	}{
		{name: "stable", template: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: stable\n"},
		{name: "genCA-unused", template: "{{- $ca := genCA \"test\" 365 -}}\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: stable\n"},
		{name: "nondeterministic-output-allowed", template: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: generated\ndata:\n  value: {{ randAlphaNum 24 | quote }}\n", allow: true},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			chart := Chart{source: &v2.Chart{
				Metadata:  &v2.Metadata{Name: "bench", Version: "1.0.0", APIVersion: v2.APIVersionV2},
				Templates: []*common.File{{Name: "templates/configmap.yaml", Data: []byte(benchmark.template)}},
			}}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := RenderVerified(b.Context(), chart, map[string]any{}, benchmark.allow); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestHelmLookupMatchesNamespaceAndScope(t *testing.T) {
	t.Parallel()
	resources := []map[string]any{
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"namespace": "team-a", "name": "settings"}},
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"namespace": "team-b", "name": "settings"}},
		{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "team-a"}},
	}
	lookupContext := WithLookupResources(t.Context(), resources,
		LookupResourceDescriptor{Kind: "ConfigMap", Scope: LookupResourceScopeNamespaced},
		LookupResourceDescriptor{Kind: "Namespace", Scope: LookupResourceScopeCluster},
	)
	functions, err := hermeticFunctions(lookupContext, func() {})
	if err != nil {
		t.Fatal(err)
	}
	lookup := functions["lookup"].(func(string, string, string, string) (map[string]any, error))

	teamA, err := lookup("v1", "ConfigMap", "team-a", "")
	if err != nil {
		t.Fatal(err)
	}
	items := teamA["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("team-a list returned %d items, want one duplicate-free match", len(items))
	}

	allNamespaces, err := lookup("v1", "ConfigMap", "", "")
	if err != nil {
		t.Fatal(err)
	}
	items = allNamespaces["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("all-namespaces list returned %d items, want two distinct ConfigMaps", len(items))
	}

	cluster, err := lookup("v1", "Namespace", "team-a", "team-a")
	if err != nil {
		t.Fatal(err)
	}
	if helmLookupName(cluster) != "team-a" {
		t.Fatalf("cluster-scoped lookup returned %#v, want the matching Namespace", cluster)
	}
}

func TestPrepareRequiresExplicitKubernetesCapabilities(t *testing.T) {
	t.Parallel()
	loaded := Chart{source: &v2.Chart{Metadata: &v2.Metadata{Name: testChartName, Version: testChartVersion, APIVersion: v2.APIVersionV2}}}
	if _, err := Prepare(loaded, RenderOptions{}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "requires kubeVersion") {
		t.Fatalf("Prepare() error = %v, want missing kubeVersion error", err)
	}
	options := RenderOptions{KubeVersion: testKubeVersion}
	if _, err := Prepare(loaded, options, t.TempDir()); err == nil || !strings.Contains(err.Error(), "requires apiVersions") {
		t.Fatalf("Prepare() error = %v, want missing apiVersions error", err)
	}
	options.APIVersions = []string{}
	if _, err := Prepare(loaded, options, t.TempDir()); err != nil {
		t.Fatalf("Prepare() with explicit empty apiVersions: %v", err)
	}
}

func TestPrepareCacheKeyIncludesNormalizedNow(t *testing.T) {
	t.Parallel()

	loaded := Chart{
		source:        &v2.Chart{Metadata: &v2.Metadata{Name: testChartName, Version: testChartVersion, APIVersion: v2.APIVersionV2}},
		ContentDigest: content.SHA256([]byte("chart")),
	}
	baseOptions := RenderOptions{KubeVersion: testKubeVersion, APIVersions: []string{}}
	withOffset := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.FixedZone("plus-nine", 9*60*60))
	equivalentUTC := withOffset.UTC()
	differentUTC := equivalentUTC.Add(time.Second)
	prepare := func(now *time.Time) Prepared {
		t.Helper()
		options := baseOptions
		options.Now = now
		prepared, err := Prepare(loaded, options, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if !prepared.CacheKeyAvailable {
			t.Fatal("Prepare() did not create a cache key")
		}

		return prepared
	}
	first := prepare(&withOffset)
	second := prepare(&equivalentUTC)
	different := prepare(&differentUTC)
	if first.CacheKey != second.CacheKey {
		t.Fatal("Prepare() cache key differs for equivalent timestamps with different offsets")
	}
	if first.CacheKey == different.CacheKey {
		t.Fatal("Prepare() cache key does not include the fixed render time")
	}
	withoutNow := prepare(nil)
	if first.CacheKey == withoutNow.CacheKey {
		t.Fatal("Prepare() cache key does not distinguish an omitted fixed render time")
	}
}

func TestPrepareKeepsRemoteChartSourcesInMemory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	templateSource := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: example\n")
	chart := &v2.Chart{
		Metadata:  &v2.Metadata{Name: testChartName, Version: testChartVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{Name: localTestTemplatePath, Data: templateSource}},
	}
	loaded := Chart{source: chart, RemoteDigest: content.SHA256([]byte("archive"))}
	prepared, err := Prepare(loaded, RenderOptions{
		KubeVersion: testKubeVersion, APIVersions: []string{},
	}, root)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if got := string(prepared.ChartSourceFiles[localTestTemplatePath]); got != string(templateSource) {
		t.Fatalf("Prepare() chart source = %q, want original template", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".mcl")); !os.IsNotExist(err) {
		t.Fatalf("Prepare() created a project cache path, stat error = %v", err)
	}
}

func TestPrepareProcessesDependencyConditionsWithoutMutatingLoadedChart(t *testing.T) {
	t.Parallel()

	const (
		childChartName   = testChildChartName
		grafanaChartName = "grafana"
		enabledField     = "enabled"
	)

	child := &v2.Chart{
		Metadata: &v2.Metadata{Name: childChartName, Version: testChartVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{
			Name: "templates/invalid.yaml",
			Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: child\n  name: duplicate\n"),
		}},
	}
	grafana := &v2.Chart{
		Metadata: &v2.Metadata{Name: grafanaChartName, Version: testChartVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{
			Name: "templates/invalid.yaml",
			Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: grafana\n  name: duplicate\n"),
		}},
	}
	loadedChart := &v2.Chart{
		Metadata: &v2.Metadata{
			Name:       testParentChartName,
			Version:    testChartVersion,
			APIVersion: v2.APIVersionV2,
			Dependencies: []*v2.Dependency{
				{Name: childChartName, Version: testChartVersion, Condition: childChartName + "." + enabledField, Enabled: true},
				{Name: grafanaChartName, Version: testChartVersion, Condition: grafanaChartName + "." + enabledField, Enabled: true},
			},
		},
		Values: map[string]any{
			childChartName:   map[string]any{enabledField: true},
			grafanaChartName: map[string]any{enabledField: true},
		},
	}
	loadedChart.SetDependencies(child, grafana)

	prepared, err := Prepare(Chart{source: loadedChart}, RenderOptions{
		Values: map[string]any{
			childChartName:   map[string]any{enabledField: false},
			grafanaChartName: map[string]any{enabledField: false},
		},
		KubeVersion: testKubeVersion, APIVersions: []string{},
	}, t.TempDir())
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	rendered, err := Render(t.Context(), prepared.Chart, prepared.Values)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(rendered) != 0 {
		t.Fatalf("Render() returned %d templates, want disabled child templates to be omitted", len(rendered))
	}
	if len(loadedChart.Dependencies()) != 2 || loadedChart.Dependencies()[0] != child || loadedChart.Dependencies()[1] != grafana {
		t.Fatal("Prepare() mutated the loaded chart dependencies")
	}
	if !loadedChart.Metadata.Dependencies[0].Enabled || !loadedChart.Metadata.Dependencies[1].Enabled {
		t.Fatal("Prepare() mutated the loaded chart dependency condition")
	}
}

func TestChartSourceFilesRejectsPathsOutsideChart(t *testing.T) {
	t.Parallel()

	chart := &v2.Chart{
		Metadata:  &v2.Metadata{Name: testChartName, Version: testChartVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{Name: "../outside.yaml", Data: []byte("kind: ConfigMap\n")}},
	}
	if _, err := chartSourceFiles(chart); err == nil || !strings.Contains(err.Error(), "invalid Helm chart source path") {
		t.Fatalf("chartSourceFiles() error = %v, want invalid source path", err)
	}
}

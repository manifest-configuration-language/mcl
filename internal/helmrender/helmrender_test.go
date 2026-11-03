package helmrender

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/manifest-configuration-language/mcl/internal/content"
	"helm.sh/helm/v4/pkg/chart/common"
	v2 "helm.sh/helm/v4/pkg/chart/v2"
)

const (
	localTestTemplatePath = "templates/configmap.yaml"
	testChartName         = "example"
	testChartVersion      = "0.1.0"
	testKubeVersion       = "1.36.0"
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
	if len(loadedChart.Templates) != 1 || loadedChart.Templates[0].Name != "templates/configmap.yaml" {
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
	if loaded.ContentDigest == changed.ContentDigest {
		t.Fatal("Load() content digest did not include ignored chart inputs")
	}
}

func TestRenderedFieldMappingsWithBlockTemplate(t *testing.T) {
	t.Parallel()
	templateSource := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: example\ndata:\n  value: {{ block \"config-value\" . }}{{ .Values.value }}{{ end }}\n")
	chartSource := Chart{source: &v2.Chart{
		Metadata:  &v2.Metadata{Name: testChartName, Version: testChartVersion, APIVersion: v2.APIVersionV2},
		Templates: []*common.File{{Name: "templates/configmap.yaml", Data: templateSource}},
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
		childChartName   = "child"
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
			Name:       "parent",
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

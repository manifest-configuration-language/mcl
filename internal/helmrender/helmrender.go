package helmrender

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/manifest-configuration-language/mcl/internal/content"
	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/manifest-configuration-language/mcl/internal/safepath"
	"helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/loader"
	v2 "helm.sh/helm/v4/pkg/chart/v2"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/engine"
)

const maxChartBytes = 16 << 20

const maxSchemaBytes = 16 << 20

// Chartは読み込み済みHelm Chartと診断・cacheに使うsource識別情報を保持します。
type Chart struct {
	source        chart.Charter
	LocalPath     string
	Origin        string
	RemoteDigest  string
	ContentDigest string
}

// SpanはHelm template source file内のbyte範囲を表します。
type Span struct {
	Start int
	End   int
}

// FieldLocationはtemplate markerを含むrender後のYAML fieldを示します。
type FieldLocation struct {
	Path     []int
	Document int
	Key      bool
}

// FieldMappingはrender後のfieldと生成元のHelm template actionを対応付けます。
type FieldMapping struct {
	Marker    string
	Span      Span
	Source    []byte
	Locations []FieldLocation
	Fallback  bool
	Branch    bool
}

type fieldMapping struct {
	marker    string
	span      Span
	source    []byte
	locations []fieldLocation
	branch    bool
}

type fieldLocation struct {
	path     []int
	document int
	key      bool
}

type helmTemplateAction struct {
	start     int
	end       int
	leftTrim  bool
	rightTrim bool
	branch    bool
}

// DocumentはMCL RuntimeValueに依存しない順序付きYAML documentです。
type Document struct {
	Value Value
}

// Valueはrender後のtemplate markerを探す順序付きYAML valueです。
type Value struct {
	Kind    ValueKind
	Text    string
	Bool    bool
	Entries []Entry
	Items   []Value
}

// ValueKindはinstrumentしたoutputの比較に使うYAML value種別です。
type ValueKind uint8

const (
	ValueOther ValueKind = iota
	ValueMap
	ValueList
	ValueString
	ValueInteger
	ValueDecimal
	ValueBool
	ValueNull
)

// Entryは順序付きmappingの要素です。
type Entry struct {
	Key   string
	Value Value
}

// ErrMappingResourceLimitはmapping解析が呼び出し元のresource上限に達したことを示します。
var ErrMappingResourceLimit = errors.New("helm field mapping resource limit exceeded")

// DocumentParserはrender後のYAMLをadapterに依存しない順序付きdocumentへ変換します。
type DocumentParser func(string) ([]Document, error)

// LoadRequestはlocalまたはlock済みremote Helm Chartの読み込み条件を表します。
type LoadRequest struct {
	Source      string
	Version     string
	ProjectRoot string
	SourcePath  string
	Lock        *dependency.Lock
}

// RenderOptionsはHelm template評価へ渡す値を保持します。
type RenderOptions struct {
	Values      map[string]any
	ReleaseName string
	Namespace   string
	Operation   string
	Revision    int
	KubeVersion string
	APIVersions []string
}

// Preparedは描画用に統合した値とsource pathを保持します。
type Prepared struct {
	Chart             Chart
	Values            map[string]any
	ChartRoot         string
	ChartSourcePath   string
	CacheKey          string
	CacheKeyAvailable bool
}

// SchemaArchiveはHelm Chart values Schemaとoffline参照に必要なarchive情報を保持します。
type SchemaArchive struct {
	Schema []byte
	Root   string
	Files  map[string][]byte
	Digest string
}

// ValuesErrorはChartとReleaseの値を統合するときに発生したerrorを表します。
type ValuesError struct {
	Cause error
}

func (err ValuesError) Error() string {
	return fmt.Sprintf("prepare Helm values: %v", err.Cause)
}

func (err ValuesError) Unwrap() error {
	return err.Cause
}

// IsValuesErrorはerrがHelmの値準備で発生したかを返します。
func IsValuesError(err error) bool {
	_, ok := errors.AsType[ValuesError](err)

	return ok
}

// Loadはlocalまたはlock済みremote Helm Chartを解決して検証します。
func Load(request LoadRequest) (Chart, error) {
	loaded := Chart{}
	if strings.HasPrefix(request.Source, "oci://") {
		if request.Version == "" {
			return Chart{}, errors.New("helm.render requires version for OCI Chart references")
		}
		if request.Lock == nil {
			return Chart{}, errors.New("helm.render requires dependency locking")
		}
		archive, err := request.Lock.LoadOCIChart(request.Source, request.Version)
		if err != nil {
			return Chart{}, fmt.Errorf("load locked OCI Helm Chart: %w", err)
		}
		loaded.RemoteDigest = content.SHA256(archive)
		loaded.ContentDigest = loaded.RemoteDigest
		loaded.Origin = "helm:" + request.Source + "@sha256:" + loaded.RemoteDigest
		loaded.source, err = loader.LoadArchive(bytes.NewReader(archive))
		if err != nil {
			return Chart{}, fmt.Errorf("load OCI Helm Chart archive: %w", err)
		}
		loadedChart, ok := loaded.source.(*v2.Chart)
		if !ok || loadedChart.Metadata == nil || loadedChart.Metadata.Version != request.Version {
			return Chart{}, fmt.Errorf("OCI Helm Chart archive version does not match requested version %q", request.Version)
		}
	} else if remote, err := dependency.ValidatedManifestURL(request.Source); err == nil && remote != nil {
		if request.Lock == nil {
			return Chart{}, errors.New("helm.render requires dependency locking")
		}
		archive, err := request.Lock.LoadChart(request.Source)
		if err != nil {
			return Chart{}, fmt.Errorf("load locked Helm Chart: %w", err)
		}
		loaded.RemoteDigest = content.SHA256(archive)
		loaded.ContentDigest = loaded.RemoteDigest
		loaded.Origin = "helm:" + request.Source + "@sha256:" + loaded.RemoteDigest
		loaded.source, err = loader.LoadArchive(bytes.NewReader(archive))
		if err != nil {
			return Chart{}, fmt.Errorf("load Helm Chart archive: %w", err)
		}
		if request.Version == "" {
			return Chart{}, errors.New("helm.render requires version for HTTPS Chart archives")
		}
		loadedChart, ok := loaded.source.(*v2.Chart)
		if !ok || loadedChart.Metadata == nil || loadedChart.Metadata.Version != request.Version {
			return Chart{}, fmt.Errorf("helm Chart archive version does not match requested version %q", request.Version)
		}
	} else {
		if strings.Contains(request.Source, "://") || filepath.IsAbs(request.Source) {
			return Chart{}, errors.New("helm.render source must be a local relative path, an absolute HTTPS Chart archive URL, or an OCI Chart reference")
		}
		if request.Version != "" {
			return Chart{}, errors.New("helm.render option version is only valid for remote Chart sources")
		}
		chartPath, err := safepath.ResolveWithin(request.ProjectRoot, filepath.Join(filepath.Dir(request.SourcePath), filepath.FromSlash(request.Source)))
		if err != nil {
			return Chart{}, fmt.Errorf("resolve Helm chart: %w", err)
		}
		if err := validateTree(chartPath); err != nil {
			return Chart{}, err
		}
		loaded.ContentDigest, err = digestTree(chartPath)
		if err != nil {
			return Chart{}, err
		}
		loaded.LocalPath = chartPath
		relativeChartPath, err := filepath.Rel(request.ProjectRoot, chartPath)
		if err != nil {
			return Chart{}, fmt.Errorf("relativize Helm chart origin: %w", err)
		}
		loaded.Origin = "helm:" + filepath.ToSlash(relativeChartPath)
		loaded.source, err = loader.Load(chartPath)
		if err != nil {
			return Chart{}, fmt.Errorf("load Helm chart: %w", err)
		}
		loadedDigest, digestErr := digestTree(chartPath)
		if digestErr != nil || loadedDigest != loaded.ContentDigest {
			loaded.ContentDigest = ""
		}
	}

	loadedChart, ok := loaded.source.(*v2.Chart)
	if !ok {
		return Chart{}, errors.New("loaded Helm Chart has an unsupported type")
	}
	if err := validateDependencies(loadedChart); err != nil {
		return Chart{}, fmt.Errorf("validate Helm Chart dependencies: %w", err)
	}

	return loaded, nil
}

func validateDependencies(root *v2.Chart) error {
	if root == nil {
		return errors.New("helm chart is empty")
	}
	var validate func(chart.Charter, string) error
	validate = func(current chart.Charter, chartPath string) error {
		currentChart, ok := current.(*v2.Chart)
		if !ok || currentChart == nil || currentChart.Metadata == nil {
			return fmt.Errorf("helm chart %q has no metadata", chartPath)
		}
		accessor, err := chart.NewAccessor(current)
		if err != nil {
			return fmt.Errorf("chart %q: %w", chartPath, err)
		}
		if accessor.Name() == "" {
			return fmt.Errorf("helm chart %q has no metadata", chartPath)
		}
		children := accessor.Dependencies()
		requirements := accessor.MetaDependencies()
		childCharts := make([]*v2.Chart, len(children))
		for index, child := range children {
			childChart, ok := child.(*v2.Chart)
			if !ok || childChart == nil || childChart.Metadata == nil {
				return fmt.Errorf("chart %q has an invalid bundled subchart", chartPath)
			}
			childCharts[index] = childChart
		}
		matchedRequirements := make([]int, len(childCharts))
		for index := range matchedRequirements {
			matchedRequirements[index] = -1
		}
		var match func(int, []bool) bool
		match = func(requirementIndex int, visited []bool) bool {
			requirement, ok := requirements[requirementIndex].(*v2.Dependency)
			if !ok || requirement == nil {
				return false
			}
			for childIndex, child := range childCharts {
				if visited[childIndex] || child.Metadata.Name != requirement.Name || !chartutil.IsCompatibleRange(requirement.Version, child.Metadata.Version) {
					continue
				}
				visited[childIndex] = true
				if matchedRequirements[childIndex] == -1 || match(matchedRequirements[childIndex], visited) {
					matchedRequirements[childIndex] = requirementIndex

					return true
				}
			}

			return false
		}
		for requirementIndex, rawRequirement := range requirements {
			requirement, ok := rawRequirement.(*v2.Dependency)
			if !ok || requirement == nil {
				return fmt.Errorf("chart %q has an invalid dependency declaration", chartPath)
			}
			if !match(requirementIndex, make([]bool, len(childCharts))) {
				return fmt.Errorf("chart %q declares dependency %q with version constraint %q but no matching subchart is bundled", chartPath, requirement.Name, requirement.Version)
			}
		}
		for _, childChart := range childCharts {
			childPath := filepath.ToSlash(filepath.Join(chartPath, childChart.Metadata.Name))
			if err := validate(childChart, childPath); err != nil {
				return err
			}
		}

		return nil
	}

	return validate(root, "root")
}

// LoadSchemaArchiveはlock済みremote Helm ChartからSchema参照用DTOを読み込みます。
func LoadSchemaArchive(lock *dependency.Lock, source, version string) (SchemaArchive, error) {
	if lock == nil || lock.IsUpdate() {
		return SchemaArchive{}, errors.New("locked Helm Chart cache is unavailable")
	}
	var archive []byte
	var err error
	switch {
	case strings.HasPrefix(source, "oci://"):
		archive, err = lock.LoadOCIChart(source, version)
	case strings.HasPrefix(source, "https://"):
		archive, err = lock.LoadChart(source)
	default:
		return SchemaArchive{}, errors.New("helm Chart source is not a remote archive")
	}
	if err != nil {
		return SchemaArchive{}, err
	}
	loaded, err := loader.LoadArchive(bytes.NewReader(archive))
	if err != nil {
		return SchemaArchive{}, fmt.Errorf("load locked Helm Chart archive: %w", err)
	}
	loadedChart, ok := loaded.(*v2.Chart)
	if !ok || loadedChart.Metadata == nil || loadedChart.Metadata.Version != version {
		return SchemaArchive{}, fmt.Errorf("locked Helm Chart archive version does not match requested version %q", version)
	}
	if len(loadedChart.Schema) > maxSchemaBytes {
		return SchemaArchive{}, fmt.Errorf("helm values Schema exceeds the %d-byte limit", maxSchemaBytes)
	}
	result := SchemaArchive{Schema: bytes.Clone(loadedChart.Schema), Digest: content.SHA256(archive)}
	if len(loadedChart.Schema) == 0 {
		return result, nil
	}
	root := loadedChart.ChartFullPath()
	if root == "" || path.IsAbs(root) || path.Clean(root) != root || root == "." || root == ".." || strings.HasPrefix(root, "../") {
		return SchemaArchive{}, errors.New("locked Helm Chart has an invalid archive root")
	}
	files, err := archiveFiles(loadedChart)
	if err != nil {
		return SchemaArchive{}, err
	}
	result.Root = root
	result.Files = files

	return result, nil
}

// PrepareはvaluesとRelease capabilitiesを統合し、可能な場合はcache keyを設定します。
func Prepare(loaded Chart, options RenderOptions, projectRoot string) (Prepared, error) {
	prepared := Prepared{Chart: loaded}
	if options.KubeVersion == "" {
		return Prepared{}, errors.New("helm.render requires kubeVersion")
	}
	if options.APIVersions == nil {
		return Prepared{}, errors.New("helm.render requires apiVersions; use an empty List when the target has no additional APIs")
	}
	capabilities := common.Capabilities{
		APIVersions: common.VersionSet{},
		HelmVersion: common.DefaultCapabilities.HelmVersion,
	}
	if options.KubeVersion != "" {
		version, err := common.ParseKubeVersion(options.KubeVersion)
		if err != nil {
			return Prepared{}, fmt.Errorf("invalid Helm kubeVersion: %w", err)
		}
		capabilities.KubeVersion = *version
	}
	capabilities.APIVersions = common.VersionSet(options.APIVersions)
	releaseOptions := common.ReleaseOptions{
		Name: options.ReleaseName, Namespace: options.Namespace, Revision: options.Revision,
	}
	switch options.Operation {
	case "create":
		releaseOptions.IsInstall = true
	case "update":
		releaseOptions.IsUpgrade = true
	case "delete", "rollback", "verify":
	}
	values, err := util.ToRenderValues(loaded.source, options.Values, releaseOptions, &capabilities)
	if err != nil {
		return Prepared{}, ValuesError{Cause: err}
	}
	if accessor, ok := loaded.source.(interface{ ChartFullPath() string }); ok {
		prepared.ChartRoot = accessor.ChartFullPath()
	}
	prepared.ChartSourcePath = loaded.LocalPath
	if loaded.RemoteDigest != "" && prepared.ChartRoot != "" {
		if loadedChart, ok := loaded.source.(*v2.Chart); ok {
			if cachedRoot, cacheErr := cacheSources(projectRoot, loaded.RemoteDigest, loadedChart); cacheErr == nil {
				prepared.ChartSourcePath = cachedRoot
			}
		}
	}
	prepared.Values = values
	if loaded.ContentDigest != "" {
		key, err := renderCacheKey(struct {
			Format       string
			ChartDigest  string
			Operation    string
			ReleaseName  string
			Namespace    string
			Revision     int
			KubeVersion  string
			APIVersions  []string
			RenderValues map[string]any
		}{
			Format: "helm-render-v1", ChartDigest: loaded.ContentDigest, Operation: options.Operation,
			ReleaseName: options.ReleaseName, Namespace: options.Namespace, Revision: options.Revision,
			KubeVersion: options.KubeVersion, APIVersions: options.APIVersions, RenderValues: values,
		})
		if err == nil {
			prepared.CacheKey = key
			prepared.CacheKeyAvailable = true
		}
	}

	return prepared, nil
}

func renderCacheKey(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode render cache key: %w", err)
	}

	return content.SHA256(data), nil
}

// Renderは外部アクセスを無効にしたHelm template engineを実行します。
func Render(ctx context.Context, source Chart, values map[string]any) (map[string]string, error) {
	return renderSource(ctx, source.source, values)
}

func renderSource(ctx context.Context, source chart.Charter, values map[string]any) (map[string]string, error) {
	return (engine.Engine{
		Strict: false, EnableDNS: false, CustomTemplateFuncs: hermeticFunctions(),
	}).RenderWithContext(ctx, source, values)
}

func hermeticFunctions() template.FuncMap {
	blocked := func() (string, error) {
		return "", errors.New("template function is disabled in hermetic Helm rendering")
	}

	return template.FuncMap{
		"lookup": func(string, string, string, string) (map[string]any, error) {
			return nil, errors.New("helm lookup is disabled in hermetic rendering")
		},
		"getHostByName": func(string) (string, error) { return blocked() },
		"now": func() (time.Time, error) {
			return time.Time{}, errors.New("helm now is disabled in hermetic rendering")
		},
		"uuidv4": blocked, "randAlphaNum": func(int) (string, error) { return blocked() },
		"randAlpha": func(int) (string, error) { return blocked() }, "randNumeric": func(int) (string, error) { return blocked() },
		"randAscii": func(int) (string, error) { return blocked() }, "randBytes": func(int) (string, error) { return blocked() },
		"shuffle":       func(string) (string, error) { return blocked() },
		"genPrivateKey": func(string) (string, error) { return blocked() },
		"randInt":       func(int, int) (int, error) { return 0, errors.New("helm randInt is disabled in hermetic rendering") },
		"encryptAES":    func(...any) (string, error) { return blocked() },
		"genCA":         func(...any) (string, error) { return blocked() }, "genCAWithKey": func(...any) (string, error) { return blocked() },
		"genSelfSignedCert":        func(...any) (string, error) { return blocked() },
		"genSelfSignedCertWithKey": func(...any) (string, error) { return blocked() },
		"genSignedCert":            func(...any) (string, error) { return blocked() }, "genSignedCertWithKey": func(...any) (string, error) { return blocked() },
		"bcrypt": func(string) (string, error) { return blocked() }, "htpasswd": func(string, string) (string, error) { return blocked() },
	}
}

func validateTree(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("stat Helm chart: %w", err)
	}
	if !info.IsDir() {
		return errors.New("helm chart source must be a directory")
	}
	var totalBytes int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("helm chart contains unsupported symlink %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		file, err := entry.Info()
		if err != nil {
			return err
		}
		if !file.Mode().IsRegular() {
			return fmt.Errorf("helm chart contains unsupported file %q", path)
		}
		totalBytes += file.Size()
		if totalBytes > maxChartBytes {
			return fmt.Errorf("helm chart exceeds the %d-byte limit", maxChartBytes)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("inspect Helm chart: %w", err)
	}

	return nil
}

func digestTree(root string) (string, error) {
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source tree contains unsupported symlink %q", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source tree contains unsupported file %q", path)
		}
		paths = append(paths, path)

		return nil
	}); err != nil {
		return "", fmt.Errorf("read source tree for render cache: %w", err)
	}
	slices.Sort(paths)
	var key strings.Builder
	for _, filePath := range paths {
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return "", fmt.Errorf("relativize source tree path: %w", err)
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("read source tree file: %w", err)
		}
		key.WriteString(filepath.ToSlash(relative))
		key.WriteByte(0)
		key.WriteString(content.SHA256(data))
		key.WriteByte(0)
	}

	return content.SHA256([]byte(key.String())), nil
}

func cacheSources(projectRoot, digest string, root *v2.Chart) (string, error) {
	if !content.IsSHA256(digest) || root == nil {
		return "", errors.New("invalid Helm source cache identity")
	}
	chartRoot := filepath.ToSlash(root.ChartFullPath())
	if chartRoot == "" || path.IsAbs(chartRoot) || path.Clean(chartRoot) != chartRoot || chartRoot == "." || chartRoot == ".." || strings.HasPrefix(chartRoot, "../") {
		return "", fmt.Errorf("invalid Helm chart root %q", chartRoot)
	}
	files := make(map[string][]byte)
	var collect func(*v2.Chart)
	collect = func(current *v2.Chart) {
		for _, file := range current.Templates {
			files[path.Join(current.ChartFullPath(), file.Name)] = file.Data
		}
		for _, crd := range current.CRDObjects() {
			files[path.Join(current.ChartFullPath(), crd.Name)] = crd.File.Data
		}
		for _, child := range current.Dependencies() {
			collect(child)
		}
	}
	collect(root)
	cacheRoot := filepath.Join(projectRoot, ".mcl", "cache", "helm-sources", digest)
	for _, filename := range slices.Sorted(maps.Keys(files)) {
		data := files[filename]
		relativePath, ok := strings.CutPrefix(filename, chartRoot+"/")
		if !ok || relativePath == "" || path.Clean(relativePath) != relativePath || path.IsAbs(relativePath) || relativePath == ".." || strings.HasPrefix(relativePath, "../") {
			return "", fmt.Errorf("invalid Helm chart source path %q", filename)
		}
		target := filepath.Join(cacheRoot, filepath.FromSlash(relativePath))
		if !safepath.Within(cacheRoot, target) {
			return "", errors.New("helm chart source escapes its cache directory")
		}
		if err := ensureDirectoryWithin(projectRoot, filepath.Dir(target)); err != nil {
			return "", fmt.Errorf("create Helm source cache: %w", err)
		}
		if err := content.WriteFileAtomic(target, data); err != nil {
			return "", fmt.Errorf("cache Helm source file: %w", err)
		}
	}

	return cacheRoot, nil
}

func archiveFiles(root *v2.Chart) (map[string][]byte, error) {
	files := make(map[string][]byte)
	var collect func(*v2.Chart, string) error
	collect = func(current *v2.Chart, prefix string) error {
		for _, file := range current.Raw {
			if file == nil {
				continue
			}
			name := strings.ReplaceAll(file.Name, "\\", "/")
			if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
				return errors.New("locked Helm Chart has an invalid archive file path")
			}
			files[path.Join(prefix, name)] = file.Data
		}
		for _, child := range current.Dependencies() {
			if err := collect(child, path.Join(prefix, "charts", child.Name())); err != nil {
				return err
			}
		}

		return nil
	}
	if err := collect(root, ""); err != nil {
		return nil, err
	}

	return files, nil
}

func ensureDirectoryWithin(root, target string) error {
	if !safepath.Within(root, target) {
		return errors.New("directory escapes the project directory")
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return errors.New("directory escapes the project directory")
	}
	current := root
	for part := range strings.SplitSeq(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("cache path contains a non-directory or symbolic link")
		}
	}
	if _, err := safepath.ResolveWithin(root, target); err != nil {
		return errors.New("cache directory escapes the project directory")
	}

	return nil
}

func instrumentChart(source *v2.Chart) (*v2.Chart, map[string][]fieldMapping) {
	markerIndex := 0

	return instrumentChartWithMarkerIndex(source, &markerIndex)
}

func instrumentChartWithMarkerIndex(source *v2.Chart, markerIndex *int) (*v2.Chart, map[string][]fieldMapping) {
	clone := *source
	clone.Templates = make([]*common.File, len(source.Templates))
	mappings := make(map[string][]fieldMapping)
	for index, sourceFile := range source.Templates {
		if sourceFile == nil {
			continue
		}
		file := *sourceFile
		file.Data = bytes.Clone(sourceFile.Data)
		actions := helmTemplateOutputActions(file.Data)
		if len(actions) > 0 {
			var output bytes.Buffer
			cursor := 0
			for _, action := range actions {
				marker := fmt.Sprintf("MCLHELMFIELD%08d", *markerIndex)
				*markerIndex = *markerIndex + 1
				output.Write(file.Data[cursor:action.start])
				output.WriteString("{{")
				if action.leftTrim {
					output.WriteString("- ")
				}
				output.WriteString(`printf "%s" "`)
				output.WriteString(marker)
				output.WriteByte('"')
				if suffix := helmTemplateIndentPipeline(file.Data[action.start:action.end], action); len(suffix) != 0 {
					output.WriteByte(' ')
					output.Write(suffix)
				}
				if action.rightTrim {
					output.WriteString(" -")
				}
				output.WriteString("}}")
				mappings[path.Join(source.ChartFullPath(), file.Name)] = append(mappings[path.Join(source.ChartFullPath(), file.Name)], fieldMapping{marker: marker, span: Span{Start: action.start, End: action.end}, source: bytes.Clone(sourceFile.Data), branch: action.branch})
				cursor = action.end
			}
			output.Write(file.Data[cursor:])
			file.Data = output.Bytes()
		}
		clone.Templates[index] = &file
	}
	dependencies := source.Dependencies()
	clonedDependencies := make([]*v2.Chart, len(dependencies))
	for index, dependency := range dependencies {
		clonedDependency, dependencyMappings := instrumentChartWithMarkerIndex(dependency, markerIndex)
		clonedDependencies[index] = clonedDependency
		maps.Copy(mappings, dependencyMappings)
	}
	clone.SetDependencies(clonedDependencies...)

	return &clone, mappings
}

func helmTemplateIndentPipeline(actionSource []byte, action helmTemplateAction) []byte {
	bodyStart := 2
	bodyEnd := len(actionSource) - 2
	if action.leftTrim {
		bodyStart++
	}
	if action.rightTrim {
		bodyEnd--
	}
	if bodyStart >= bodyEnd {
		return nil
	}
	body := actionSource[bodyStart:bodyEnd]
	var quote byte
	escaped := false
	depth := 0
	for index := range body {
		character := body[index]
		if quote != 0 {
			if quote != '`' && escaped {
				escaped = false
			} else if quote != '`' && character == '\\' {
				escaped = true
			} else if character == quote {
				quote = 0
			}

			continue
		}
		if character == '"' || character == '\'' || character == '`' {
			quote = character

			continue
		}
		switch character {
		case '(':
			depth++
		case ')':
			depth--
		case '|':
			if depth != 0 {
				continue
			}
			functionStart := index + 1
			for functionStart < len(body) && (body[functionStart] == ' ' || body[functionStart] == '\t' || body[functionStart] == '\r' || body[functionStart] == '\n') {
				functionStart++
			}
			functionEnd := functionStart
			for functionEnd < len(body) && (body[functionEnd] == '_' || body[functionEnd] >= 'a' && body[functionEnd] <= 'z' || body[functionEnd] >= 'A' && body[functionEnd] <= 'Z') {
				functionEnd++
			}
			if string(body[functionStart:functionEnd]) == "indent" || string(body[functionStart:functionEnd]) == "nindent" {
				return body[index:]
			}
		}
	}

	return nil
}

func helmTemplateOutputActions(source []byte) []helmTemplateAction {
	var actions []helmTemplateAction
	var controlStack []struct{}
	for cursor := 0; cursor+1 < len(source); {
		start := bytes.Index(source[cursor:], []byte("{{"))
		if start < 0 {
			break
		}
		start += cursor
		end := helmTemplateActionEnd(source, start+2)
		if end < 0 {
			break
		}
		action := helmTemplateAction{
			start: start, end: end,
			leftTrim:  start+2 < end && source[start+2] == '-',
			rightTrim: end >= 3 && source[end-3] == '-',
		}
		bodyStart := start + 2
		bodyEnd := end - 2
		if action.leftTrim {
			bodyStart++
		}
		if action.rightTrim {
			bodyEnd--
		}
		body := strings.TrimSpace(string(source[bodyStart:bodyEnd]))
		fields := strings.Fields(body)
		command := ""
		if len(fields) > 0 {
			command = fields[0]
		}
		if helmTemplateActionProducesOutput(source[bodyStart:bodyEnd]) {
			action.branch = len(controlStack) > 0
			actions = append(actions, action)
		}
		switch command {
		case "if", "range", "with", "block":
			controlStack = append(controlStack, struct{}{})
		case "end":
			if len(controlStack) > 0 {
				controlStack = controlStack[:len(controlStack)-1]
			}
		}
		cursor = end
	}

	return actions
}

func helmTemplateActionEnd(source []byte, cursor int) int {
	var quote byte
	escaped := false
	for cursor+1 < len(source) {
		character := source[cursor]
		if quote != 0 {
			if quote != '`' && escaped {
				escaped = false
			} else if quote != '`' && character == '\\' {
				escaped = true
			} else if character == quote {
				quote = 0
			}
			cursor++

			continue
		}
		if character == '"' || character == '\'' || character == '`' {
			quote = character
			cursor++

			continue
		}
		if source[cursor] == '/' && source[cursor+1] == '*' {
			commentEnd := bytes.Index(source[cursor+2:], []byte("*/"))
			if commentEnd < 0 {
				return -1
			}
			cursor += commentEnd + 4

			continue
		}
		if source[cursor] == '}' && source[cursor+1] == '}' {
			return cursor + 2
		}
		cursor++
	}

	return -1
}

func helmTemplateActionProducesOutput(body []byte) bool {
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(body)), "-")))
	if len(fields) == 0 || strings.HasPrefix(fields[0], "/*") {
		return false
	}
	switch fields[0] {
	case "if", "range", "else", "end", "with", "define", "block", "break", "continue":
		return false
	}

	return !strings.HasPrefix(fields[0], "$") || len(fields) < 2 || fields[1] != ":=" && fields[1] != "="
}

// RenderedFieldMappingsはChartをinstrumentし、render後のYAMLからtemplate actionの位置を特定します。
func RenderedFieldMappings(ctx context.Context, source Chart, values map[string]any, original map[string]string, parse DocumentParser) (map[string][]FieldMapping, error) {
	root, ok := source.source.(*v2.Chart)
	if !ok {
		return nil, nil
	}
	instrumented, mappings := instrumentChart(root)
	if len(mappings) == 0 {
		return nil, nil
	}
	rendered, err := renderSource(ctx, instrumented, values)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		return nil, nil
	}
	result := make(map[string][]FieldMapping)
	for filename, sourceMappings := range mappings {
		originalYAML, exists := original[filename]
		instrumentedYAML, renderedExists := rendered[filename]
		if !exists || !renderedExists || parse == nil {
			continue
		}
		originalDocuments, originalErr := parse(originalYAML)
		instrumentedDocuments, instrumentedErr := parse(instrumentedYAML)
		if errors.Is(originalErr, ErrMappingResourceLimit) || errors.Is(instrumentedErr, ErrMappingResourceLimit) {
			return nil, ErrMappingResourceLimit
		}
		if originalErr != nil || instrumentedErr != nil || len(originalDocuments) != len(instrumentedDocuments) {
			continue
		}
		valid := true
		for index := range originalDocuments {
			if !collectFieldMappingValue(originalDocuments[index].Value, instrumentedDocuments[index].Value, sourceMappings, nil, index) {
				valid = false

				break
			}
		}
		for _, mapping := range sourceMappings {
			if len(mapping.locations) == 0 && valid {
				continue
			}
			result[filename] = append(result[filename], FieldMapping{
				Marker: mapping.marker, Span: mapping.span, Source: bytes.Clone(mapping.source),
				Locations: exportFieldLocations(mapping.locations), Fallback: !valid, Branch: mapping.branch,
			})
		}
	}

	return result, nil
}

// CRDObjectsはHelm chart型を公開せずにChart内のCRD fileを返します。
func CRDObjects(source Chart) ([]CRD, error) {
	crdChart, ok := source.source.(interface{ CRDObjects() []v2.CRD })
	if !ok {
		return nil, errors.New("helm chart loader returned a chart without CRD support")
	}
	crds := crdChart.CRDObjects()
	slices.SortStableFunc(crds, func(left, right v2.CRD) int {
		return cmp.Compare(left.Filename, right.Filename)
	})
	result := make([]CRD, 0, len(crds))
	for _, crd := range crds {
		result = append(result, CRD{Filename: crd.Filename, Data: crd.File.Data})
	}

	return result, nil
}

// CRDはChartに含まれるcustom resource definition fileを表します。
type CRD struct {
	Filename string
	Data     []byte
}

func exportFieldLocations(locations []fieldLocation) []FieldLocation {
	result := make([]FieldLocation, len(locations))
	for index, location := range locations {
		result[index] = FieldLocation{Path: slices.Clone(location.path), Document: location.document, Key: location.key}
	}

	return result
}

func collectFieldMappingValue(original, instrumented Value, mappings []fieldMapping, valuePath []int, documentIndex int) bool {
	if instrumented.Kind == ValueString && recordTemplateMarkers(instrumented.Text, mappings, valuePath, documentIndex, false) {
		return true
	}
	if original.Kind != instrumented.Kind {
		return false
	}
	switch original.Kind {
	case ValueMap:
		if len(original.Entries) != len(instrumented.Entries) {
			return false
		}
		for index := range original.Entries {
			childPath := append(slices.Clone(valuePath), index)
			if original.Entries[index].Key != instrumented.Entries[index].Key && !recordTemplateMarkers(instrumented.Entries[index].Key, mappings, childPath, documentIndex, true) {
				return false
			}
			if !collectFieldMappingValue(original.Entries[index].Value, instrumented.Entries[index].Value, mappings, childPath, documentIndex) {
				return false
			}
		}
	case ValueList:
		if len(original.Items) != len(instrumented.Items) {
			return false
		}
		for index := range original.Items {
			childPath := append(slices.Clone(valuePath), index)
			if !collectFieldMappingValue(original.Items[index], instrumented.Items[index], mappings, childPath, documentIndex) {
				return false
			}
		}
	case ValueString, ValueInteger, ValueDecimal:
		return original.Text == instrumented.Text
	case ValueBool:
		return original.Bool == instrumented.Bool
	case ValueNull:
		return true
	default:
		return false
	}

	return true
}

func recordTemplateMarkers(text string, mappings []fieldMapping, valuePath []int, documentIndex int, key bool) bool {
	matched := false
	for index := range mappings {
		if !strings.Contains(text, mappings[index].marker) {
			continue
		}
		mapping := &mappings[index]
		mapping.locations = append(mapping.locations, fieldLocation{path: slices.Clone(valuePath), document: documentIndex, key: key})
		matched = true
	}

	return matched
}

package helmrender

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/manifest-configuration-language/mcl/internal/content"
	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/manifest-configuration-language/mcl/internal/safepath"
	"helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/chart/loader/archive"
	v2 "helm.sh/helm/v4/pkg/chart/v2"
	v2loader "helm.sh/helm/v4/pkg/chart/v2/loader"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/engine"
	"helm.sh/helm/v4/pkg/ignore"
	"helm.sh/helm/v4/pkg/provenance"
)

const maxChartBytes = 16 << 20

const maxSchemaBytes = 16 << 20

const maxChartDependencyDepth = 32

// Chartは読み込み済みHelm Chartと診断・cacheに使うsource識別情報を保持します。
type Chart struct {
	source        chart.Charter
	LocalPath     string
	Origin        string
	RemoteDigest  string
	ContentDigest string
}

// LoadRequestはlocalまたはlock済みremote Helm Chartの読み込み条件を表します。
type LoadRequest struct {
	Source      string
	Version     string
	ProjectRoot string
	SourcePath  string
	Lock        *dependency.Lock
	// SourceFS optionally supplies project files using root-relative io/fs paths.
	SourceFS fs.FS
}

// RenderOptionsはHelm template評価へ渡す値を保持します。
type RenderOptions struct {
	Values          map[string]any
	LookupResources []map[string]any
	ReleaseName     string
	Namespace       string
	Operation       string
	Revision        int
	KubeVersion     string
	APIVersions     []string
	Now             *time.Time
}

// Preparedは描画用に統合した値とsource pathを保持します。
type Prepared struct {
	Chart             Chart
	Values            map[string]any
	ChartRoot         string
	ChartSourcePath   string
	ChartSourceFiles  map[string][]byte
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

type chartDependencyDigest struct {
	Alias  string
	Digest string
}

type chartRenderDigest struct {
	Archive      string
	Dependencies []chartDependencyDigest
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
		loaded.ContentDigest, err = loadLockedDependencies(loadedChart, loaded.RemoteDigest, request.Lock)
		if err != nil {
			return Chart{}, fmt.Errorf("load OCI Helm Chart dependencies: %w", err)
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
		loaded.ContentDigest, err = loadLockedDependencies(loadedChart, loaded.RemoteDigest, request.Lock)
		if err != nil {
			return Chart{}, fmt.Errorf("load Helm Chart dependencies: %w", err)
		}
	} else {
		if strings.Contains(request.Source, "://") || filepath.IsAbs(request.Source) {
			return Chart{}, errors.New("helm.render source must be a local relative path, an absolute HTTPS Chart archive URL, or an OCI Chart reference")
		}
		if request.Version != "" {
			return Chart{}, errors.New("helm.render option version is only valid for remote Chart sources")
		}
		if request.SourceFS != nil {
			projectPath, err := filepath.Rel(request.ProjectRoot, filepath.Clean(request.SourcePath))
			if err != nil || projectPath == ".." || filepath.IsAbs(projectPath) || strings.HasPrefix(projectPath, ".."+string(filepath.Separator)) {
				return Chart{}, fmt.Errorf("resolve Helm chart source path %q: outside project root", request.SourcePath)
			}
			relativeChartPath := path.Join(path.Dir(filepath.ToSlash(projectPath)), request.Source)
			if relativeChartPath != "." && !fs.ValidPath(relativeChartPath) {
				return Chart{}, fmt.Errorf("resolve Helm chart: invalid project path %q", relativeChartPath)
			}
			chartFS, err := fs.Sub(request.SourceFS, relativeChartPath)
			if err != nil {
				return Chart{}, fmt.Errorf("resolve Helm chart: %w", err)
			}
			loadedChart, digest, err := loadChartFS(chartFS)
			if err != nil {
				return Chart{}, fmt.Errorf("load Helm chart: %w", err)
			}
			loaded.ContentDigest = digest
			loaded.LocalPath = filepath.Join(request.ProjectRoot, filepath.FromSlash(relativeChartPath))
			loaded.Origin = "helm:" + relativeChartPath
			loaded.source = loadedChart

			return validateLoadedChart(loaded)
		}
		chartPath, err := safepath.ResolveWithin(request.ProjectRoot, filepath.Join(filepath.Dir(request.SourcePath), filepath.FromSlash(request.Source)))
		if err != nil {
			return Chart{}, fmt.Errorf("resolve Helm chart: %w", err)
		}
		loadedChart, digest, err := loadChartFS(os.DirFS(chartPath))
		if err != nil {
			return Chart{}, fmt.Errorf("load Helm chart: %w", err)
		}
		loaded.ContentDigest = digest
		loaded.LocalPath = chartPath
		relativeChartPath, err := filepath.Rel(request.ProjectRoot, chartPath)
		if err != nil {
			return Chart{}, fmt.Errorf("relativize Helm chart origin: %w", err)
		}
		loaded.Origin = "helm:" + filepath.ToSlash(relativeChartPath)
		loaded.source = loadedChart
		loadedDigest, digestErr := digestSourceFS(os.DirFS(chartPath))
		if digestErr != nil || loadedDigest != loaded.ContentDigest {
			loaded.ContentDigest = ""
		}
	}

	return validateLoadedChart(loaded)
}

func loadLockedDependencies(current *v2.Chart, parentDigest string, lock *dependency.Lock) (string, error) {
	return loadLockedDependenciesRecursive(current, parentDigest, lock, make(map[string]bool), 0)
}

func loadLockedDependenciesRecursive(current *v2.Chart, parentDigest string, lock *dependency.Lock, ancestry map[string]bool, depth int) (string, error) {
	if current == nil || current.Metadata == nil {
		return "", errors.New("chart metadata is missing")
	}
	if depth >= maxChartDependencyDepth {
		return "", fmt.Errorf("chart dependency nesting exceeds %d levels", maxChartDependencyDepth)
	}
	if ancestry[parentDigest] {
		return "", fmt.Errorf("chart dependency cycle detected at %q", current.Metadata.Name)
	}
	ancestry[parentDigest] = true
	defer delete(ancestry, parentDigest)
	requirements := current.Metadata.Dependencies
	if len(requirements) == 0 {
		if current.Lock != nil && len(current.Lock.Dependencies) != 0 {
			return "", fmt.Errorf("chart %q Chart.lock contains dependencies missing from Chart.yaml", current.Metadata.Name)
		}
		if len(current.Dependencies()) == 0 {
			return parentDigest, nil
		}

		return loadBundledChildTree(current, parentDigest, lock, ancestry, depth)
	}
	if err := validateBundledRequirements(current, current.Dependencies()); err == nil {
		return loadBundledChildTree(current, parentDigest, lock, ancestry, depth)
	}
	if current.Lock == nil {
		if err := validateBundledRequirements(current, current.Dependencies()); err != nil {
			return "", err
		}

		return loadBundledChildTree(current, parentDigest, lock, ancestry, depth)
	}
	if len(current.Lock.Dependencies) != len(requirements) {
		return "", fmt.Errorf("chart %q has dependencies without a complete Chart.lock; run helm dependency update", current.Metadata.Name)
	}
	if lock == nil {
		return "", errors.New("dependency lock is required")
	}
	lockedRequirements, err := resolveLockedRequirements(current.Metadata, current.Lock)
	if err != nil {
		return "", err
	}
	children := make([]*v2.Chart, 0, len(requirements))
	childDigests := make([]chartDependencyDigest, 0, len(requirements))
	for index, requirement := range requirements {
		alias := requirement.Alias
		if alias == "" {
			alias = requirement.Name
		}
		locked := lockedRequirements[index]
		if locked.Repository == "" || strings.HasPrefix(locked.Repository, "file://") {
			child, found := bundledDependency(current, requirement.Name)
			if !found {
				return "", fmt.Errorf("chart %q dependency %q uses a local repository but has no bundled subchart", current.Metadata.Name, requirement.Name)
			}
			child = cloneChart(child)
			childDigest := chartRawDigest(child)
			childDigest, err := loadLockedDependenciesRecursive(child, childDigest, lock, ancestry, depth+1)
			if err != nil {
				return "", fmt.Errorf("dependency %q: %w", alias, err)
			}
			children = append(children, child)
			childDigests = append(childDigests, chartDependencyDigest{Alias: alias, Digest: childDigest})

			continue
		}
		var source string
		var archive []byte
		var err error
		if lock.IsUpdate() {
			source, archive, err = lock.ResolveChartDependency(locked.Repository, locked.Name, locked.Version)
			if err == nil {
				err = lock.SaveChartDependency(parentDigest, alias, source, locked.Version)
			}
		} else {
			var version string
			source, version, _ = lock.LoadChartDependency(parentDigest, alias)
			if source == "" || version != locked.Version {
				return "", fmt.Errorf("dependency %q version %q is not locked by mcl; run mcl update", alias, locked.Version)
			}
			if strings.HasPrefix(source, "oci://") {
				archive, err = lock.LoadOCIChart(source, version)
			} else {
				archive, err = lock.LoadChart(source)
			}
		}
		if err != nil {
			return "", fmt.Errorf("load dependency %q version %q: %w", alias, locked.Version, err)
		}
		loaded, err := loader.LoadArchive(bytes.NewReader(archive))
		if err != nil {
			return "", fmt.Errorf("load dependency %q archive: %w", alias, err)
		}
		child, ok := loaded.(*v2.Chart)
		if !ok || child.Metadata == nil || child.Metadata.Name != requirement.Name || child.Metadata.Version != locked.Version {
			return "", fmt.Errorf("dependency %q archive does not match locked chart %q version %q", alias, requirement.Name, locked.Version)
		}
		child = cloneChart(child)
		childDigest, err := loadLockedDependenciesRecursive(child, content.SHA256(archive), lock, ancestry, depth+1)
		if err != nil {
			return "", fmt.Errorf("dependency %q: %w", alias, err)
		}
		children = append(children, child)
		childDigests = append(childDigests, chartDependencyDigest{Alias: alias, Digest: childDigest})
	}
	current.SetDependencies(children...)

	return compositeChartDigest(parentDigest, childDigests)
}

func loadBundledChildTree(current *v2.Chart, parentDigest string, lock *dependency.Lock, ancestry map[string]bool, depth int) (string, error) {
	children := current.Dependencies()
	childDigests := make([]chartDependencyDigest, 0, len(children))
	dependencyChanged := false
	for index, child := range children {
		if child == nil || child.Metadata == nil {
			return "", errors.New("chart contains an invalid bundled subchart")
		}
		child = cloneChart(child)
		childBaseDigest := chartRawDigest(child)
		childDigest, err := loadLockedDependenciesRecursive(child, childBaseDigest, lock, ancestry, depth+1)
		if err != nil {
			return "", fmt.Errorf("bundled dependency %q: %w", child.Metadata.Name, err)
		}
		childDigests = append(childDigests, chartDependencyDigest{Alias: child.Metadata.Name, Digest: childDigest})
		dependencyChanged = dependencyChanged || childDigest != childBaseDigest
		children[index] = child
	}
	current.SetDependencies(children...)
	if dependencyChanged {
		return compositeChartDigest(parentDigest, childDigests)
	}

	return parentDigest, nil
}

func bundledDependency(parent *v2.Chart, name string) (*v2.Chart, bool) {
	for _, child := range parent.Dependencies() {
		if child != nil && child.Metadata != nil && child.Metadata.Name == name {
			return child, true
		}
	}

	return nil, false
}

func chartRawDigest(chart *v2.Chart) string {
	files := make([]*common.File, 0, len(chart.Raw))
	for _, file := range chart.Raw {
		if file != nil {
			files = append(files, file)
		}
	}
	slices.SortFunc(files, func(left, right *common.File) int {
		return strings.Compare(left.Name, right.Name)
	})
	type rawChartFile struct {
		Name string
		Data []byte
	}
	inputs := make([]rawChartFile, 0, len(files))
	for _, file := range files {
		inputs = append(inputs, rawChartFile{Name: file.Name, Data: file.Data})
	}
	data, err := json.Marshal(inputs)
	if err != nil {
		return ""
	}

	return content.SHA256(data)
}

func validateBundledRequirements(current *v2.Chart, children []*v2.Chart) error {
	used := make([]bool, len(children))
	for _, requirement := range current.Metadata.Dependencies {
		matched := false
		for index, child := range children {
			if used[index] || child == nil || child.Metadata == nil || child.Metadata.Name != requirement.Name {
				continue
			}
			used[index] = true
			matched = true

			break
		}
		if !matched {
			return fmt.Errorf("chart %q dependency %q has no matching bundled subchart and no Chart.lock", current.Metadata.Name, requirement.Name)
		}
	}

	return nil
}

func compositeChartDigest(parentDigest string, dependencies []chartDependencyDigest) (string, error) {
	data, err := json.Marshal(chartRenderDigest{Archive: parentDigest, Dependencies: dependencies})
	if err != nil {
		return "", fmt.Errorf("encode Helm Chart dependency digest: %w", err)
	}

	return content.SHA256(data), nil
}

func resolveLockedRequirements(metadata *v2.Metadata, chartLock *v2.Lock) ([]*v2.Dependency, error) {
	resolvedRequirements := make([]*v2.Dependency, len(metadata.Dependencies))
	lockedRequirements := make([]*v2.Dependency, len(metadata.Dependencies))
	used := make([]bool, len(chartLock.Dependencies))
	for index, requirement := range metadata.Dependencies {
		locked, err := matchLockedRequirement(requirement, chartLock.Dependencies, used)
		if err != nil {
			return nil, fmt.Errorf("chart %q dependency %q: %w", metadata.Name, requirement.Name, err)
		}
		resolved := *requirement
		if isHelmRepositoryAlias(requirement.Repository) {
			resolved.Repository = locked.Repository
		}
		resolvedRequirements[index] = &resolved
		lockedRequirements[index] = locked
	}
	if err := validateHelmChartLock(metadata, resolvedRequirements, chartLock); err != nil {
		return nil, err
	}

	return lockedRequirements, nil
}

func validateHelmChartLock(metadata *v2.Metadata, resolvedRequirements []*v2.Dependency, chartLock *v2.Lock) error {
	data, err := json.Marshal([2][]*v2.Dependency{resolvedRequirements, chartLock.Dependencies})
	if err != nil {
		return fmt.Errorf("encode Chart.lock requirements: %w", err)
	}
	digest, err := provenance.Digest(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("hash Chart.lock requirements: %w", err)
	}
	if chartLock.Digest == "sha256:"+digest {
		return nil
	}
	if metadata.APIVersion == v2.APIVersionV1 {
		legacyData, err := json.Marshal(map[string][]*v2.Dependency{"dependencies": metadata.Dependencies})
		if err != nil {
			return fmt.Errorf("encode legacy Chart.lock requirements: %w", err)
		}
		legacyDigest, err := provenance.Digest(bytes.NewReader(legacyData))
		if err != nil {
			return fmt.Errorf("hash legacy Chart.lock requirements: %w", err)
		}
		if chartLock.Digest == "sha256:"+legacyDigest {
			return nil
		}
	}

	return errors.New("chart.lock digest does not match Chart.yaml dependencies")
}

func isHelmRepositoryAlias(repository string) bool {
	return (strings.HasPrefix(repository, "@") && len(repository) > 1) ||
		(strings.HasPrefix(repository, "alias:") && len(strings.TrimPrefix(repository, "alias:")) > 0)
}

func isLockedRemoteRepository(repository string) bool {
	if strings.HasPrefix(repository, "oci://") {
		reference, err := url.JoinPath(repository, "mcl-validation")

		return err == nil && dependency.ValidateOCIChartSource(reference) == nil
	}
	parsed, err := dependency.ValidatedManifestURL(repository)

	return err == nil && parsed.RawQuery == ""
}

func matchLockedRequirement(requirement *v2.Dependency, lockedRequirements []*v2.Dependency, used []bool) (*v2.Dependency, error) {
	constraint, err := semver.NewConstraint(requirement.Version)
	if err != nil {
		return nil, fmt.Errorf("invalid version constraint %q: %w", requirement.Version, err)
	}
	for index, locked := range lockedRequirements {
		if used[index] || locked == nil || locked.Name != requirement.Name {
			continue
		}
		if isHelmRepositoryAlias(requirement.Repository) {
			if !isLockedRemoteRepository(locked.Repository) {
				continue
			}
		} else if locked.Repository != requirement.Repository {
			continue
		}
		version, err := semver.NewVersion(locked.Version)
		if err != nil || !constraint.Check(version) {
			continue
		}
		used[index] = true

		return locked, nil
	}

	return nil, errors.New("chart.lock has no matching pinned dependency")
}

func validateLoadedChart(loaded Chart) (Chart, error) {
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
		// Helm applies dependency version constraints during ProcessDependencies, so
		// this validation only checks that the archive includes the dependency.
		match = func(requirementIndex int, visited []bool) bool {
			requirement, ok := requirements[requirementIndex].(*v2.Dependency)
			if !ok || requirement == nil {
				return false
			}
			for childIndex, child := range childCharts {
				if visited[childIndex] || child.Metadata.Name != requirement.Name {
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
	if options.Now != nil {
		options.Now = new(options.Now.UTC())
	}
	if options.KubeVersion == "" {
		return Prepared{}, errors.New("helm.render requires kubeVersion")
	}
	if options.APIVersions == nil {
		return Prepared{}, errors.New("helm.render requires apiVersions; use an empty List when the target has no additional APIs")
	}
	loadedChart, ok := loaded.source.(*v2.Chart)
	if !ok {
		return Prepared{}, errors.New("loaded Helm Chart has an unsupported type")
	}
	loadedChart = cloneChart(loadedChart)
	loaded.source = loadedChart
	if err := chartutil.ProcessDependencies(loadedChart, options.Values); err != nil {
		return Prepared{}, ValuesError{Cause: err}
	}
	prepared.Chart = loaded
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
		if !content.IsSHA256(loaded.RemoteDigest) {
			return Prepared{}, errors.New("invalid remote Helm Chart digest")
		}
		loadedChart, ok := loaded.source.(*v2.Chart)
		if !ok {
			return Prepared{}, errors.New("loaded remote Helm Chart has an unsupported type")
		}
		files, err := chartSourceFiles(loadedChart)
		if err != nil {
			return Prepared{}, fmt.Errorf("read remote Helm Chart sources: %w", err)
		}
		prepared.ChartSourcePath = filepath.Join(projectRoot, ".mcl", "cache", "helm-sources", loaded.RemoteDigest)
		prepared.ChartSourceFiles = files
	}
	prepared.Values = values
	if loaded.ContentDigest != "" {
		key, err := renderCacheKey(struct {
			Format          string
			ChartDigest     string
			Operation       string
			ReleaseName     string
			Namespace       string
			Revision        int
			KubeVersion     string
			APIVersions     []string
			LookupResources []map[string]any
			Now             *time.Time
			RenderValues    map[string]any
		}{
			Format: "helm-render-v2", ChartDigest: loaded.ContentDigest, Operation: options.Operation,
			ReleaseName: options.ReleaseName, Namespace: options.Namespace, Revision: options.Revision,
			KubeVersion: options.KubeVersion, APIVersions: options.APIVersions, LookupResources: options.LookupResources,
			Now: options.Now, RenderValues: values,
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
	if helmChart, ok := source.(*v2.Chart); ok {
		source = cloneChartWithoutNotes(helmChart)
	}

	return (engine.Engine{
		Strict: false, EnableDNS: false, CustomTemplateFuncs: hermeticFunctions(ctx),
	}).RenderWithContext(ctx, source, values)
}

type deterministicNowContextKey struct{}

type lookupResourcesContextKey struct{}

type lookupResourcesContext struct {
	resources []map[string]any
}

// WithNow supplies the fixed time used by Helm templates during a hermetic render.
func WithNow(ctx context.Context, now time.Time) context.Context {
	return context.WithValue(ctx, deterministicNowContextKey{}, now.UTC())
}

// WithLookupResources supplies the deterministic Kubernetes object snapshot used by Helm lookup.
func WithLookupResources(ctx context.Context, resources []map[string]any) context.Context {
	return context.WithValue(ctx, lookupResourcesContextKey{}, lookupResourcesContext{resources: resources})
}

func hermeticFunctions(ctx context.Context) template.FuncMap {
	blocked := func() (string, error) {
		return "", errors.New("template function is disabled in hermetic Helm rendering")
	}
	now := func() (time.Time, error) {
		if fixed, ok := ctx.Value(deterministicNowContextKey{}).(time.Time); ok {
			return fixed, nil
		}

		return time.Time{}, errors.New("helm now is disabled in hermetic rendering")
	}

	lookup := func(apiVersion, kind, namespace, name string) (map[string]any, error) {
		snapshot, ok := ctx.Value(lookupResourcesContextKey{}).(lookupResourcesContext)
		if !ok {
			return nil, errors.New("helm lookup requires an explicit lookupResources snapshot")
		}
		if name == "" {
			items := make([]any, 0)
			for _, resource := range snapshot.resources {
				if helmLookupMatches(resource, apiVersion, kind, namespace, true) {
					items = append(items, cloneLookupResource(resource))
				}
			}

			return map[string]any{"apiVersion": apiVersion, "kind": kind + "List", "items": items}, nil
		}
		for _, resource := range snapshot.resources {
			if helmLookupMatches(resource, apiVersion, kind, namespace, false) && helmLookupName(resource) == name {
				return cloneLookupResource(resource), nil
			}
		}

		return make(map[string]any), nil
	}

	return template.FuncMap{
		"lookup":        lookup,
		"getHostByName": func(string) (string, error) { return blocked() },
		"now":           now,
		"uuidv4":        blocked, "randAlphaNum": func(int) (string, error) { return blocked() },
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

func helmLookupMatches(resource map[string]any, apiVersion, kind, namespace string, list bool) bool {
	if resource["apiVersion"] != apiVersion || resource["kind"] != kind {
		return false
	}
	metadata, ok := resource["metadata"].(map[string]any)
	if !ok {
		return namespace == ""
	}
	resourceNamespace, _ := metadata["namespace"].(string)
	if list && namespace == "" {
		return true
	}

	return resourceNamespace == namespace
}

func helmLookupName(resource map[string]any) string {
	metadata, ok := resource["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := metadata["name"].(string)

	return name
}

func cloneLookupResource(resource map[string]any) map[string]any {
	cloned := make(map[string]any, len(resource))
	for key, value := range resource {
		cloned[key] = cloneLookupValue(value)
	}

	return cloned
}

func cloneLookupValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneLookupResource(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneLookupValue(item)
		}

		return cloned
	default:
		return value
	}
}

func loadChartFS(source fs.FS) (*v2.Chart, string, error) {
	info, err := fs.Stat(source, ".")
	if err != nil {
		return nil, "", fmt.Errorf("stat Helm chart source: %w", err)
	}
	if !info.IsDir() {
		return nil, "", errors.New("helm chart source must be a directory")
	}
	rules := ignore.Empty()
	if data, err := fs.ReadFile(source, ignore.HelmIgnore); err == nil {
		rules, err = ignore.Parse(bytes.NewReader(data))
		if err != nil {
			return nil, "", fmt.Errorf("parse Helm ignore file: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("read Helm ignore file: %w", err)
	}
	rules.AddDefaults()

	files := make([]*archive.BufferedFile, 0)
	ignoredDirectories := make(map[string]bool)
	var digestKey strings.Builder
	var totalBytes int64
	err = fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if fileInfo.IsDir() {
			parent := path.Dir(name)
			ignoredDirectories[name] = ignoredDirectories[parent] || rules.Ignore(filepath.ToSlash(name), fileInfo)
			if ignoredDirectories[name] {
				return fs.SkipDir
			}

			return nil
		}
		parent := path.Dir(name)
		if ignoredDirectories[parent] || rules.Ignore(filepath.ToSlash(name), fileInfo) {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("helm chart contains unsupported symlink %q", name)
		}
		if !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("helm chart contains unsupported file %q", name)
		}
		if fileInfo.Size() > maxChartBytes-totalBytes {
			return fmt.Errorf("helm chart exceeds the %d-byte limit", maxChartBytes)
		}
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		totalBytes += int64(len(data))
		if totalBytes > maxChartBytes {
			return fmt.Errorf("helm chart exceeds the %d-byte limit", maxChartBytes)
		}
		digestKey.WriteString(filepath.ToSlash(name))
		digestKey.WriteByte(0)
		digestKey.WriteString(content.SHA256(data))
		digestKey.WriteByte(0)
		files = append(files, &archive.BufferedFile{Name: filepath.ToSlash(name), ModTime: fileInfo.ModTime(), Data: bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})})

		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("inspect Helm chart: %w", err)
	}
	chart, err := v2loader.LoadFiles(files)
	if err != nil {
		return nil, "", err
	}
	if chart.Metadata.APIVersion == "v3" {
		return nil, "", errors.New("loaded Helm Chart has an unsupported type")
	}

	return chart, content.SHA256([]byte(digestKey.String())), nil
}

func digestSourceFS(source fs.FS) (string, error) {
	var paths []string
	var totalBytes int64
	if err := fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." || entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("source tree contains unsupported symlink %q", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source tree contains unsupported file %q", name)
		}
		if info.Size() > maxChartBytes-totalBytes {
			return fmt.Errorf("helm chart exceeds the %d-byte limit", maxChartBytes)
		}
		totalBytes += info.Size()
		paths = append(paths, name)

		return nil
	}); err != nil {
		return "", fmt.Errorf("read source tree for render cache: %w", err)
	}
	slices.Sort(paths)
	var key strings.Builder
	var readBytes int64
	for _, filePath := range paths {
		data, err := fs.ReadFile(source, filePath)
		if err != nil {
			return "", fmt.Errorf("read source tree file: %w", err)
		}
		if int64(len(data)) > maxChartBytes-readBytes {
			return "", fmt.Errorf("helm chart exceeds the %d-byte limit", maxChartBytes)
		}
		readBytes += int64(len(data))
		key.WriteString(filepath.ToSlash(filePath))
		key.WriteByte(0)
		key.WriteString(content.SHA256(data))
		key.WriteByte(0)
	}

	return content.SHA256([]byte(key.String())), nil
}

func chartSourceFiles(root *v2.Chart) (map[string][]byte, error) {
	if root == nil {
		return nil, errors.New("invalid Helm chart source")
	}
	chartRoot := filepath.ToSlash(root.ChartFullPath())
	if chartRoot == "" || path.IsAbs(chartRoot) || path.Clean(chartRoot) != chartRoot || chartRoot == "." || chartRoot == ".." || strings.HasPrefix(chartRoot, "../") {
		return nil, fmt.Errorf("invalid Helm chart root %q", chartRoot)
	}
	fullPaths := make(map[string][]byte)
	var collect func(*v2.Chart) error
	collect = func(current *v2.Chart) error {
		add := func(filename string, data []byte) error {
			filename = strings.ReplaceAll(filename, "\\", "/")
			filePath := filepath.FromSlash(filename)
			if !fs.ValidPath(filename) || path.IsAbs(filename) || filepath.IsAbs(filePath) || filepath.VolumeName(filePath) != "" || path.Clean(filename) != filename {
				return fmt.Errorf("invalid Helm chart source path %q", filename)
			}
			fullPath := path.Join(current.ChartFullPath(), filename)
			if _, ok := fullPaths[fullPath]; ok {
				return fmt.Errorf("duplicate Helm chart source path %q", filename)
			}
			fullPaths[fullPath] = bytes.Clone(data)

			return nil
		}
		for _, file := range current.Templates {
			if file == nil {
				continue
			}
			if err := add(file.Name, file.Data); err != nil {
				return err
			}
		}
		for _, crd := range current.CRDObjects() {
			if crd.File == nil {
				continue
			}
			if err := add(crd.Name, crd.File.Data); err != nil {
				return err
			}
		}
		for _, child := range current.Dependencies() {
			if err := collect(child); err != nil {
				return err
			}
		}

		return nil
	}
	if err := collect(root); err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(fullPaths))
	for _, filename := range slices.Sorted(maps.Keys(fullPaths)) {
		data := fullPaths[filename]
		relativePath, ok := strings.CutPrefix(filename, chartRoot+"/")
		if !ok || relativePath == "" || path.Clean(relativePath) != relativePath || path.IsAbs(relativePath) || relativePath == ".." || strings.HasPrefix(relativePath, "../") {
			return nil, fmt.Errorf("invalid Helm chart source path %q", filename)
		}
		files[relativePath] = data
	}

	return files, nil
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

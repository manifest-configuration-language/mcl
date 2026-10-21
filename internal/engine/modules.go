package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/manifest-configuration-language/mcl/internal/safepath"
)

const maxMCLSourceBytes = 16 << 20

// resolveModuleLinkはモジュールファイルのシンボリックリンクを解決する。解決できない場合はfalseを返す。
func resolveModuleLink(path string) (string, bool) {
	modulePath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}

	return modulePath, true
}

const (
	maxProjectMCLFiles = 4096
	maxProjectMCLBytes = 64 << 20
)

type moduleLoader struct {
	//nolint:containedctx // Contextはモジュール評価とAdapter呼び出しの間で共有します。
	ctx           context.Context
	root          string
	files         *projectFilesystem
	graph         *moduleGraph
	entry         string
	entryText     string
	runtimeCache  map[string][]RuntimeEntry
	runtimeTypes  map[string]map[string]ResolvedType
	dependencies  *dependency.Lock
	operation     string
	overlays      map[string]string
	loadedModules map[string]struct{}
	budget        *functionBudget
	allocations   *runtimeAllocationBudget
	analysis      *TypeAnalysisContext
	catalog       KubernetesResourceCatalog
}

func runtimeBundleToBundle(runtimeBundle RuntimeBundle) (Bundle, error) {
	bundle := Bundle{Artifacts: make([]Artifact, len(runtimeBundle.Artifacts))}
	for index, artifact := range runtimeBundle.Artifacts {
		value, err := runtimeValueToValue(artifact.Value)
		if err != nil {
			return Bundle{}, fmt.Errorf("emitted value %d: %w", index+1, err)
		}
		bundle.Artifacts[index] = Artifact{Value: value, Lifecycle: artifact.Lifecycle}
	}

	return bundle, nil
}

func EvaluateFile(path string) (Bundle, error) {
	return EvaluateFileContext(context.Background(), path)
}

// EvaluateFileContextは呼び出し元のContextを使ってMCLファイルを評価します。
func EvaluateFileContext(ctx context.Context, path string) (Bundle, error) {
	return evaluateFile(ctx, path, false, "")
}

// EvaluateFileForOperation は指定した操作向けにMCLファイルを評価します。
func EvaluateFileForOperation(path, operation string) (Bundle, error) {
	return EvaluateFileForOperationContext(context.Background(), path, operation)
}

// EvaluateFileForOperationContextは呼び出し元のContextを使って指定した操作向けにMCLファイルを評価します。
func EvaluateFileForOperationContext(ctx context.Context, path, operation string) (Bundle, error) {
	if !validOperation(operation) {
		return Bundle{}, fmt.Errorf("unknown operation %q", operation)
	}

	return evaluateFile(ctx, path, false, operation)
}

func UpdateFile(path string) error {
	return UpdateFileContext(context.Background(), path)
}

// UpdateFileContextは呼び出し元のContextを使って依存ロックを更新します。
func UpdateFileContext(ctx context.Context, path string) error {
	_, err := evaluateFile(ctx, path, true, "")

	return err
}

// ResolveImportPathはimport文の参照先をプロジェクトルート内で解決します。
func ResolveImportPath(importer, modulePath string) (string, error) {
	absolute, err := filepath.Abs(importer)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve importer %q: %w", importer, err)
	}
	root, err := discoverProjectRoot(filepath.Dir(canonical))
	if err != nil {
		return "", err
	}

	return (&moduleGraph{root: root}).resolveImport(canonical, modulePath)
}

// DependentMCLSourceIndexはworkspaceのimport逆参照indexを保持します。
type DependentMCLSourceIndex struct {
	root         string
	searchRoots  []string
	hashes       map[string][sha256.Size]byte
	modules      map[string]ModuleAST
	dependencies map[string][]string
	importers    map[string][]string
	diskSources  map[string]string
	sources      map[string]string
	overlayPaths map[string]struct{}
	initialized  bool
}

// Sourcesはworkspaceを完全snapshotし、targetを直接または間接的にimportするMCLソースを返します。
func (index *DependentMCLSourceIndex) Sources(target string, workspaceRoots []string, overlays map[string]string) (map[string]string, error) {
	canonical, root, searchRoots, err := dependentSourceScope(target, workspaceRoots)
	if err != nil {
		return nil, err
	}
	diskSources, err := collectDependentSources(canonical, root, searchRoots)
	if err != nil {
		return nil, err
	}
	allSources, overlayPaths, err := applyDependentOverlays(diskSources, root, searchRoots, overlays)
	if err != nil {
		return nil, err
	}
	hashes := make(map[string][sha256.Size]byte, len(allSources))
	for path, source := range allSources {
		hashes[path] = sha256.Sum256([]byte(source))
	}

	modules := make(map[string]ModuleAST, len(allSources))
	if index.root == root && slices.Equal(index.searchRoots, searchRoots) {
		for path, module := range index.modules {
			if hash, exists := hashes[path]; exists && index.hashes[path] == hash {
				modules[path] = module
			}
		}
	}
	for path, source := range allSources {
		if _, exists := modules[path]; exists {
			continue
		}
		module, err := ParseAST(source)
		if err != nil {
			return nil, fmt.Errorf("parse workspace module %q: %w", path, sourceDiagnosticPath(path, err))
		}
		modules[path] = module
	}

	dependencies := make(map[string][]string, len(modules))
	importers := make(map[string][]string)
	if index.root == root && slices.Equal(index.searchRoots, searchRoots) {
		for dependency, paths := range index.importers {
			importers[dependency] = slices.Clone(paths)
		}
		for path, paths := range index.dependencies {
			if _, exists := modules[path]; exists {
				dependencies[path] = slices.Clone(paths)
			} else {
				removeImporterPath(importers, path)
			}
		}
	}
	for path, module := range modules {
		graph := moduleGraph{root: root}
		resolvedDependencies := make([]string, 0)
		imports, err := graph.imports(path, module)
		if err != nil {
			return nil, err
		}
		for _, imported := range imports {
			resolvedDependencies = append(resolvedDependencies, imported.path)
		}
		slices.Sort(resolvedDependencies)
		resolvedDependencies = slices.Compact(resolvedDependencies)
		if slices.Equal(dependencies[path], resolvedDependencies) {
			continue
		}
		removeImporterPath(importers, path)
		dependencies[path] = resolvedDependencies
		for _, dependency := range resolvedDependencies {
			importers[dependency] = append(importers[dependency], path)
		}
	}
	result, err := dependentModuleSources(canonical, allSources, importers)
	if err != nil {
		return nil, err
	}

	index.root = root
	index.searchRoots = slices.Clone(searchRoots)
	index.hashes = hashes
	index.modules = modules
	index.dependencies = dependencies
	index.importers = importers
	index.diskSources = diskSources
	index.sources = allSources
	index.overlayPaths = overlayPaths
	index.initialized = true

	return result, nil
}

func removeImporterPath(importers map[string][]string, path string) {
	for dependency, paths := range importers {
		filtered := slices.DeleteFunc(paths, func(importer string) bool {
			return importer == path
		})
		if len(filtered) == 0 {
			delete(importers, dependency)

			continue
		}
		importers[dependency] = filtered
	}
}

func canonicalRenameOverlayPath(path, root string, searchRoots []string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		absolute = resolved
	}
	absolute = filepath.Clean(absolute)
	if filepath.Ext(absolute) != ".mcl" || !safepath.Within(root, absolute) || !pathWithinAny(searchRoots, absolute) {
		return "", nil
	}

	return absolute, nil
}

func applyDependentOverlays(diskSources map[string]string, root string, searchRoots []string, overlays map[string]string) (map[string]string, map[string]struct{}, error) {
	sources := maps.Clone(diskSources)
	paths := make(map[string]struct{}, len(overlays))
	seen := make(map[string]string, len(overlays))
	keys := slices.Sorted(maps.Keys(overlays))
	for _, key := range keys {
		path, err := canonicalRenameOverlayPath(key, root, searchRoots)
		if err != nil {
			return nil, nil, err
		}
		if path == "" {
			continue
		}
		if previous, exists := seen[path]; exists && previous != overlays[key] {
			return nil, nil, fmt.Errorf("conflicting open document overlays resolve to module %q", path)
		}
		seen[path] = overlays[key]
		if previous, exists := sources[path]; exists && previous == overlays[key] {
			paths[path] = struct{}{}

			continue
		}
		if len(overlays[key]) > maxMCLSourceBytes {
			return nil, nil, fmt.Errorf("module %q exceeds the %d-byte limit", path, maxMCLSourceBytes)
		}
		if _, exists := sources[path]; !exists && len(sources) >= maxProjectMCLFiles {
			return nil, nil, fmt.Errorf("workspace contains more than %d MCL files", maxProjectMCLFiles)
		}
		sources[path] = overlays[key]
		paths[path] = struct{}{}
	}
	var totalBytes int64
	for _, source := range sources {
		totalBytes += int64(len(source))
		if totalBytes > maxProjectMCLBytes {
			return nil, nil, fmt.Errorf("project MCL sources exceed the %d-byte limit", maxProjectMCLBytes)
		}
	}

	return sources, paths, nil
}

// SourcesFromIndexはファイル監視通知で更新したsnapshotと開いている文書のoverlayを使います。
// indexが未初期化またはscopeが変わった場合は完全snapshotを作成します。
func (index *DependentMCLSourceIndex) SourcesFromIndex(target string, workspaceRoots []string, overlays map[string]string) (map[string]string, error) {
	canonical, root, searchRoots, err := dependentSourceScope(target, workspaceRoots)
	if err != nil {
		return nil, err
	}
	if !index.initialized || index.root != root || !slices.Equal(index.searchRoots, searchRoots) {
		return index.Sources(target, workspaceRoots, overlays)
	}
	diskSources := maps.Clone(index.diskSources)
	overlaySources, nextOverlayPaths, err := applyDependentOverlays(diskSources, root, searchRoots, overlays)
	if err != nil {
		return nil, err
	}
	effectiveSources := maps.Clone(overlaySources)
	changed := make(map[string]struct{})
	for path := range index.overlayPaths {
		if _, remainsOpen := nextOverlayPaths[path]; remainsOpen {
			continue
		}
		content, readErr := readLimitedFileWithLimit(path, maxMCLSourceBytes)
		if readErr != nil {
			if !errors.Is(readErr, os.ErrNotExist) {
				return nil, fmt.Errorf("read module %q after closing overlay: %w", path, readErr)
			}
			delete(diskSources, path)
			delete(effectiveSources, path)
		} else {
			diskSources[path] = string(content)
			effectiveSources[path] = string(content)
		}
		changed[path] = struct{}{}
	}
	for path := range nextOverlayPaths {
		if source, exists := effectiveSources[path]; !exists {
			return nil, fmt.Errorf("overlay module %q is missing from the source snapshot", path)
		} else if old, oldExists := index.sources[path]; !oldExists || old != source {
			changed[path] = struct{}{}
		}
	}
	if err := index.applySnapshotChanges(diskSources, effectiveSources, nextOverlayPaths, changed); err != nil {
		index.initialized = false

		return nil, err
	}

	return dependentModuleSources(canonical, index.sources, index.importers)
}

// DependentMCLFileChangeはLSPが通知したMCLファイルの変更を表します。
type DependentMCLFileChange struct {
	Path    string
	Deleted bool
}

// Invalidateはworkspaceのproject境界が変わった場合にcached snapshotを無効化します。
func (index *DependentMCLSourceIndex) Invalidate() {
	index.initialized = false
}

// ApplyFileChangesは監視通知に含まれるファイルだけを読み直して逆参照indexを更新します。
func (index *DependentMCLSourceIndex) ApplyFileChanges(changes []DependentMCLFileChange) error {
	if !index.initialized {
		return nil
	}
	diskSources := maps.Clone(index.diskSources)
	effectiveSources := maps.Clone(index.sources)
	changed := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		path, err := canonicalWatchedModulePath(change.Path, change.Deleted, diskSources)
		if err != nil {
			index.initialized = false

			return err
		}
		if path == "" {
			index.initialized = false

			return nil
		}
		if filepath.Ext(path) != ".mcl" || !safepath.Within(index.root, path) || !pathWithinAny(index.searchRoots, path) {
			continue
		}
		moduleRoot, rootErr := discoverProjectRoot(filepath.Dir(path))
		if rootErr != nil || moduleRoot != index.root {
			if _, wasIndexed := diskSources[path]; wasIndexed {
				index.initialized = false

				return nil
			}

			continue
		}
		if ignoredRenameSourcePath(path, index.searchRoots) {
			if _, overlaid := index.overlayPaths[path]; overlaid {
				if change.Deleted {
					delete(diskSources, path)
				} else {
					content, err := readLimitedFileWithLimit(path, maxMCLSourceBytes)
					if err != nil {
						index.initialized = false

						return fmt.Errorf("read changed module %q: %w", path, err)
					}
					diskSources[path] = string(content)
				}

				continue
			}
			if _, wasIndexed := diskSources[path]; !wasIndexed {
				continue
			}
			delete(diskSources, path)
			delete(effectiveSources, path)
			changed[path] = struct{}{}

			continue
		}
		if change.Deleted {
			delete(diskSources, path)
		} else {
			content, err := readLimitedFileWithLimit(path, maxMCLSourceBytes)
			if err != nil {
				index.initialized = false

				return fmt.Errorf("read changed module %q: %w", path, err)
			}
			diskSources[path] = string(content)
		}
		if _, overlaid := index.overlayPaths[path]; overlaid {
			continue
		}
		if source, exists := diskSources[path]; exists {
			effectiveSources[path] = source
		} else {
			delete(effectiveSources, path)
		}
		changed[path] = struct{}{}
	}
	if err := index.applySnapshotChanges(diskSources, effectiveSources, index.overlayPaths, changed); err != nil {
		index.initialized = false

		return err
	}

	return nil
}

func ignoredRenameSourcePath(path string, searchRoots []string) bool {
	for _, searchRoot := range searchRoots {
		if !safepath.Within(searchRoot, path) {
			continue
		}
		for directory := filepath.Dir(path); directory != searchRoot && safepath.Within(searchRoot, directory); directory = filepath.Dir(directory) {
			if ignoredRenameIndexDirectory(directory) {
				return true
			}
		}
	}

	return false
}

func canonicalWatchedModulePath(path string, deleted bool, diskSources map[string]string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	directory, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", fmt.Errorf("resolve changed module directory %q: %w", absolute, err)
	}
	canonical := filepath.Clean(filepath.Join(directory, filepath.Base(absolute)))
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		resolved = filepath.Clean(resolved)
		if resolved != canonical {
			return "", nil
		}

		return canonical, nil
	}
	if deleted {
		if _, knownPath := diskSources[canonical]; knownPath {
			return canonical, nil
		}

		return "", nil
	}

	return "", fmt.Errorf("resolve changed module %q: %w", absolute, err)
}

func (index *DependentMCLSourceIndex) applySnapshotChanges(diskSources, sources map[string]string, overlayPaths map[string]struct{}, changed map[string]struct{}) error {
	var totalBytes int64
	for _, source := range sources {
		totalBytes += int64(len(source))
	}
	if len(sources) > maxProjectMCLFiles || totalBytes > maxProjectMCLBytes {
		return errors.New("workspace MCL source index exceeds its configured limits")
	}
	modules := maps.Clone(index.modules)
	dependencies := maps.Clone(index.dependencies)
	importers := cloneImporters(index.importers)
	hashes := maps.Clone(index.hashes)
	for path := range changed {
		source, exists := sources[path]
		if !exists {
			delete(modules, path)
			delete(dependencies, path)
			delete(hashes, path)
			removeImporterPath(importers, path)

			continue
		}
		module, err := ParseAST(source)
		if err != nil {
			return fmt.Errorf("parse workspace module %q: %w", path, sourceDiagnosticPath(path, err))
		}
		modules[path] = module
		hashes[path] = sha256.Sum256([]byte(source))
	}
	for path := range changed {
		module, exists := modules[path]
		if !exists {
			continue
		}
		resolved, err := resolveModuleDependencies(path, module, index.root)
		if err != nil {
			return err
		}
		removeImporterPath(importers, path)
		dependencies[path] = resolved
		for _, dependency := range resolved {
			importers[dependency] = append(importers[dependency], path)
		}
	}
	for path, importersOfPath := range index.importers {
		if _, stillExists := sources[path]; stillExists {
			continue
		}
		for _, importer := range importersOfPath {
			if _, alreadyUpdated := changed[importer]; alreadyUpdated {
				continue
			}
			module, exists := modules[importer]
			if !exists {
				continue
			}
			if _, err := resolveModuleDependencies(importer, module, index.root); err != nil {
				return err
			}
		}
	}
	index.diskSources = maps.Clone(diskSources)
	index.sources = maps.Clone(sources)
	index.overlayPaths = maps.Clone(overlayPaths)
	index.modules = modules
	index.dependencies = dependencies
	index.importers = importers
	index.hashes = hashes

	return nil
}

func cloneImporters(importers map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(importers))
	for dependency, paths := range importers {
		cloned[dependency] = slices.Clone(paths)
	}

	return cloned
}

func resolveModuleDependencies(path string, module ModuleAST, root string) ([]string, error) {
	graph := moduleGraph{root: root}
	dependencies := make([]string, 0)
	imports, err := graph.imports(path, module)
	if err != nil {
		return nil, err
	}
	for _, imported := range imports {
		dependencies = append(dependencies, imported.path)
	}
	slices.Sort(dependencies)

	return slices.Compact(dependencies), nil
}

// DependentMCLSourcesはworkspace内でtargetを直接または間接的にimportするMCLソースを返します。
func DependentMCLSources(target string, workspaceRoots []string, overlays map[string]string) (map[string]string, error) {
	var index DependentMCLSourceIndex

	return index.Sources(target, workspaceRoots, overlays)
}

func dependentSourceScope(target string, workspaceRoots []string) (string, string, []string, error) {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", "", nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve target module %q: %w", target, err)
	}
	root, err := discoverProjectRoot(filepath.Dir(canonical))
	if err != nil {
		return "", "", nil, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", "", nil, err
	}
	canonical = filepath.Clean(canonical)
	if !safepath.Within(root, canonical) {
		return "", "", nil, fmt.Errorf("target module %q is outside project root %q", canonical, root)
	}
	searchRoots, err := projectWorkspaceRoots(root, workspaceRoots)
	if err != nil {
		return "", "", nil, err
	}

	return canonical, root, searchRoots, nil
}

func collectDependentSources(canonical, root string, searchRoots []string) (map[string]string, error) {
	allSources := make(map[string]string)
	var totalBytes int64
	for _, searchRoot := range searchRoots {
		err := filepath.WalkDir(searchRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path != searchRoot && ignoredRenameIndexDirectory(path) {
					return filepath.SkipDir
				}

				return nil
			}
			if filepath.Ext(path) != ".mcl" || !entry.Type().IsRegular() && entry.Type()&os.ModeSymlink == 0 {
				return nil
			}
			modulePath, ok := resolveModuleLink(path)
			if !ok {
				return nil
			}
			modulePath, err := filepath.Abs(modulePath)
			if err != nil {
				return err
			}
			modulePath = filepath.Clean(modulePath)
			if !safepath.Within(root, modulePath) || !safepath.Within(searchRoot, modulePath) {
				return nil
			}
			moduleRoot, err := discoverProjectRoot(filepath.Dir(modulePath))
			if err != nil {
				return err
			}
			if moduleRoot != root {
				return nil
			}
			if _, exists := allSources[modulePath]; exists {
				return nil
			}
			if len(allSources) >= maxProjectMCLFiles {
				return fmt.Errorf("workspace contains more than %d MCL files", maxProjectMCLFiles)
			}
			info, err := os.Stat(modulePath)
			if err != nil {
				return fmt.Errorf("inspect module %q: %w", modulePath, err)
			}
			if info.Size() > maxMCLSourceBytes {
				return fmt.Errorf("module %q exceeds the %d-byte limit", modulePath, maxMCLSourceBytes)
			}
			if totalBytes+info.Size() > maxProjectMCLBytes {
				return fmt.Errorf("workspace MCL sources exceed the %d-byte limit", maxProjectMCLBytes)
			}
			content, err := readLimitedFileWithLimit(modulePath, maxMCLSourceBytes)
			if err != nil {
				return fmt.Errorf("read module %q: %w", modulePath, err)
			}
			allSources[modulePath] = string(content)
			totalBytes += int64(len(content))

			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if _, exists := allSources[canonical]; !exists {
		content, err := readLimitedFileWithLimit(canonical, maxMCLSourceBytes)
		if err != nil {
			return nil, fmt.Errorf("read target module %q: %w", canonical, err)
		}
		if len(allSources) >= maxProjectMCLFiles || totalBytes+int64(len(content)) > maxProjectMCLBytes {
			return nil, errors.New("workspace MCL source index exceeds its configured limits")
		}
		allSources[canonical] = string(content)
		totalBytes += int64(len(content))
	}

	return allSources, nil
}

func projectWorkspaceRoots(projectRoot string, workspaceRoots []string) ([]string, error) {
	if len(workspaceRoots) == 0 {
		return []string{projectRoot}, nil
	}
	roots := make([]string, 0, len(workspaceRoots))
	for _, workspaceRoot := range workspaceRoots {
		absolute, err := filepath.Abs(workspaceRoot)
		if err != nil {
			return nil, err
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = resolved
		}
		absolute = filepath.Clean(absolute)
		if !safepath.Within(projectRoot, absolute) && !safepath.Within(absolute, projectRoot) {
			continue
		}
		if safepath.Within(projectRoot, absolute) {
			roots = append(roots, absolute)

			continue
		}
		roots = append(roots, projectRoot)
	}
	slices.Sort(roots)
	roots = slices.Compact(roots)
	if len(roots) == 0 {
		return nil, fmt.Errorf("project root %q is outside all workspace roots", projectRoot)
	}
	nonOverlapping := make([]string, 0, len(roots))
	for _, candidate := range roots {
		if pathWithinAny(nonOverlapping, candidate) {
			continue
		}
		nonOverlapping = append(nonOverlapping, candidate)
	}

	return nonOverlapping, nil
}

func pathWithinAny(roots []string, path string) bool {
	for _, root := range roots {
		if safepath.Within(root, path) {
			return true
		}
	}

	return false
}

func ignoredRenameIndexDirectory(name string) bool {
	return filepath.Base(name) == ".git" || filepath.Base(name) == "node_modules" || filepath.Base(name) == "cache" && filepath.Base(filepath.Dir(name)) == ".mcl"
}

func dependentModuleSources(target string, sources map[string]string, importers map[string][]string) (map[string]string, error) {
	dependent := map[string]struct{}{target: {}}
	queue := []string{target}
	for len(queue) > 0 {
		dependency := queue[0]
		queue = queue[1:]
		for _, importer := range importers[dependency] {
			if _, exists := dependent[importer]; exists {
				continue
			}
			dependent[importer] = struct{}{}
			queue = append(queue, importer)
		}
	}
	result := make(map[string]string, len(dependent))
	for path := range dependent {
		if source, exists := sources[path]; exists {
			result[path] = source
		}
	}

	return result, nil
}

func evaluateFile(ctx context.Context, path string, update bool, operation string) (Bundle, error) {
	return evaluateFileWithFilesystem(ctx, path, update, operation, nil)
}

func evaluateFileWithFilesystem(ctx context.Context, path string, update bool, operation string, files *projectFilesystem) (Bundle, error) {
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Bundle{}, err
	}
	var canonical string
	root := ""
	if files == nil {
		canonical, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return Bundle{}, err
		}
		root, err = discoverProjectRoot(filepath.Dir(canonical))
		if err != nil {
			return Bundle{}, err
		}
		files = newOSProjectFilesystem(root)
	} else {
		root = files.root
		canonical, err = files.resolveReadPath(absolute)
		if err != nil {
			return Bundle{}, fmt.Errorf("resolve entry source %q: %w", absolute, err)
		}
	}
	source, err := files.readLimitedFile(canonical)
	if err != nil {
		return Bundle{}, fmt.Errorf("read entry source %q: %w", canonical, err)
	}
	catalog, err := loadKubernetesResourceCatalog(root)
	if err != nil {
		return Bundle{}, err
	}
	dependencies, err := dependency.Open(ctx, root, update, dependencyToolchainIdentity())
	if err != nil {
		return Bundle{}, err
	}
	loader := moduleLoader{
		ctx:  ctx,
		root: root, files: files, entry: canonical, entryText: string(source),
		runtimeCache: make(map[string][]RuntimeEntry), runtimeTypes: make(map[string]map[string]ResolvedType),
		dependencies: dependencies, operation: operation, loadedModules: make(map[string]struct{}), budget: &functionBudget{},
		analysis: &TypeAnalysisContext{},
		catalog:  catalog,
	}
	_, runtimeBundle, state, err := loader.evaluateRuntime(canonical, true) //nolint:contextcheck // moduleLoaderが評価全体のContextを保持します。
	if err != nil {
		return Bundle{}, err
	}
	if err := resolveRuntimeBundle(runtimeBundle, state); err != nil { //nolint:contextcheck // EvaluationState経由で評価ContextをAdapterへ渡します。
		return Bundle{}, err
	}
	bundle, err := runtimeBundleToBundle(*runtimeBundle)
	if err != nil {
		return Bundle{}, err
	}
	if update {
		if err := dependencies.Save(); err != nil {
			return Bundle{}, err
		}
	}

	return bundle, nil
}

func discoverProjectRoot(directory string) (string, error) {
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	defaultRoot := root
	for {
		marker := filepath.Join(root, "mcl.toml")
		info, err := os.Lstat(marker)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("project root marker %q must be a regular file", marker)
			}

			return root, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect project root marker %q: %w", marker, err)
		}
		parent := filepath.Dir(root)
		if parent == root {
			return defaultRoot, nil
		}
		root = parent
	}
}

func EvaluateSource(path, source string) (Bundle, error) {
	return EvaluateSourceContext(context.Background(), path, source)
}

// EvaluateSourceContextは呼び出し元のContextを使ってソーステキストを評価します。
func EvaluateSourceContext(ctx context.Context, path, source string) (Bundle, error) {
	return EvaluateSourceWithOverlaysContext(ctx, path, source, nil)
}

// EvaluateSourceWithOverlays は保存前のモジュール内容を使ってソースを評価します。
func EvaluateSourceWithOverlays(path, source string, overlays map[string]string) (Bundle, error) {
	return EvaluateSourceWithOverlaysContext(context.Background(), path, source, overlays)
}

// EvaluateSourceWithOverlaysContextは呼び出し元のContextを使ってソースとオーバーレイを評価します。
func EvaluateSourceWithOverlaysContext(ctx context.Context, path, source string, overlays map[string]string) (Bundle, error) {
	bundle, _, err := EvaluateSourceWithOverlaysAndDependenciesContext(ctx, path, source, overlays)

	return bundle, err
}

// EvaluateSourceWithOverlaysAndDependencies は評価結果と読み込んだモジュールのパスを返します。
func EvaluateSourceWithOverlaysAndDependencies(path, source string, overlays map[string]string) (Bundle, []string, error) {
	return EvaluateSourceWithOverlaysAndDependenciesContext(context.Background(), path, source, overlays)
}

// EvaluateSourceWithOverlaysAndDependenciesContextは呼び出し元のContextを使ってソースを評価し、読み込んだモジュールのパスを返します。
func EvaluateSourceWithOverlaysAndDependenciesContext(ctx context.Context, path, source string, overlays map[string]string) (Bundle, []string, error) {
	return evaluateSourceWithFilesystem(ctx, path, source, overlays, nil)
}

func evaluateSourceWithFilesystem(ctx context.Context, path, source string, overlays map[string]string, files *projectFilesystem) (Bundle, []string, error) {
	if err := ctx.Err(); err != nil {
		return Bundle{}, nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Bundle{}, nil, err
	}
	root := ""
	if files == nil {
		root, err = discoverProjectRoot(filepath.Dir(absolute))
		if err != nil {
			return Bundle{}, nil, err
		}
		files = newOSProjectFilesystem(root)
	} else {
		root = files.root
	}
	catalog, err := loadKubernetesResourceCatalog(root)
	if err != nil {
		return Bundle{}, nil, err
	}
	dependencies, err := dependency.Open(ctx, root, false, dependencyToolchainIdentity())
	if err != nil {
		return Bundle{}, nil, err
	}
	entry := absolute
	if files.osBacked {
		if canonical, err := filepath.EvalSymlinks(absolute); err == nil {
			entry = canonical
		}
	} else if _, err := files.relativePath(absolute); err != nil {
		return Bundle{}, nil, fmt.Errorf("resolve entry source %q: %w", absolute, err)
	}
	canonicalOverlays := make(map[string]string, len(overlays))
	for overlayPath, overlaySource := range overlays {
		absoluteOverlay, err := filepath.Abs(overlayPath)
		if err != nil {
			return Bundle{}, nil, err
		}
		canonicalOverlay := absoluteOverlay
		if files.osBacked {
			if resolved, err := filepath.EvalSymlinks(absoluteOverlay); err == nil {
				canonicalOverlay = resolved
			}
		}
		relative, err := filepath.Rel(root, canonicalOverlay)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		canonicalOverlays[canonicalOverlay] = overlaySource
	}
	loader := moduleLoader{
		ctx:           ctx,
		root:          root,
		files:         files,
		entry:         entry,
		entryText:     source,
		runtimeCache:  make(map[string][]RuntimeEntry),
		runtimeTypes:  make(map[string]map[string]ResolvedType),
		dependencies:  dependencies,
		overlays:      canonicalOverlays,
		loadedModules: make(map[string]struct{}),
		budget:        &functionBudget{},
		allocations:   &runtimeAllocationBudget{},
		analysis:      &TypeAnalysisContext{},
		catalog:       catalog,
	}
	_, runtimeBundle, state, err := loader.evaluateRuntime(entry, true) //nolint:contextcheck // moduleLoaderが評価全体のContextを保持します。
	loadedModules := loader.loadedModulePaths()
	if err != nil {
		return Bundle{}, loadedModules, err
	}
	if err := resolveRuntimeBundle(runtimeBundle, state); err != nil { //nolint:contextcheck // EvaluationState経由で評価ContextをAdapterへ渡します。
		return Bundle{}, loadedModules, err
	}
	bundle, err := runtimeBundleToBundle(*runtimeBundle)
	if err != nil {
		return Bundle{}, loadedModules, err
	}

	return bundle, loadedModules, nil
}

func (loader *moduleLoader) evaluateRuntime(path string, entry bool) ([]RuntimeEntry, *RuntimeBundle, *EvaluationState, error) {
	if loader.ctx == nil {
		loader.ctx = context.Background()
	}
	if loader.files == nil {
		loader.files = newOSProjectFilesystem(loader.root)
	}
	if loader.runtimeCache == nil {
		loader.runtimeCache = make(map[string][]RuntimeEntry)
	}
	if loader.runtimeTypes == nil {
		loader.runtimeTypes = make(map[string]map[string]ResolvedType)
	}
	if err := loader.ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	if loader.budget == nil {
		loader.budget = &functionBudget{}
	}
	if loader.allocations == nil {
		loader.allocations = &runtimeAllocationBudget{}
	}
	loader.loadedModules[path] = struct{}{}
	if !entry {
		if exports, cached := loader.runtimeCache[path]; cached {
			state := &EvaluationState{Types: maps.Clone(loader.runtimeTypes[path]), Allocations: loader.allocations}

			return exports, &RuntimeBundle{}, state, nil
		}
	}
	if loader.graph == nil {
		loader.graph = &moduleGraph{root: loader.root, files: loader.files}
	}
	if err := loader.graph.enter(path); err != nil {
		return nil, nil, nil, err
	}
	defer loader.graph.leave(path)
	source := loader.entryText
	if path != loader.entry {
		if overlaySource, exists := loader.overlays[path]; exists {
			source = overlaySource
		} else {
			data, err := loader.files.readLimitedFile(path)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("read module %q: %w", path, err)
			}
			source = string(data)
		}
	}
	module, err := loader.graph.parse(path, source)
	if err != nil {
		return nil, nil, nil, err
	}
	setModuleASTSourcePath(module, path)
	types, err := typeDefinitionsAST(module)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := loadTypeSchemaFilesWithFilesystem(types, loader.root, path, loader.dependencies, loader.files, loader.analysis); err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	moduleContext := &ModuleContext{Context: loader.ctx, ProjectRoot: loader.root, filesystem: loader.files, Dependencies: loader.dependencies, HelmOperation: loader.operation, Analysis: loader.analysis, ResourceCatalog: loader.catalog}
	state := &EvaluationState{Environment: make(map[string]RuntimeValue), StaticTypes: make(map[string]TypeAnalysis), Types: types, Stack: make(map[string]bool), Budget: loader.budget, Allocations: loader.allocations, SourcePath: path, Module: moduleContext, ResourceCatalog: loader.catalog}
	for _, node := range module.Nodes {
		declaration, ok := node.(*ImportDeclaration)
		if !ok {
			continue
		}
		if _, exists := state.Environment[declaration.Name]; exists {
			return nil, nil, nil, fmt.Errorf("%s:%d: %q is already defined", path, declaration.Line, declaration.Name)
		}
		var entries []RuntimeEntry
		if strings.HasPrefix(declaration.ModulePath, "std:") {
			var exists bool
			entries, exists = standardRuntimeLibrary(declaration.ModulePath)
			if !exists {
				return nil, nil, nil, fmt.Errorf("%s:%d: standard library module %q is not available", path, declaration.Line, declaration.ModulePath)
			}
			for index := range entries {
				if entries[index].Value.Kind() == Function {
					entries[index].Value.Value.SourcePath = path
					entries[index].Value.ModuleContext = moduleContext
				}
			}
		} else {
			dependency, err := loader.resolveImport(path, declaration.ModulePath)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s:%d: %w", path, declaration.Line, err)
			}
			var dependencyBundle *RuntimeBundle
			var dependencyState *EvaluationState
			entries, dependencyBundle, dependencyState, err = loader.evaluateRuntime(dependency, false)
			if err != nil {
				return nil, nil, nil, err
			}
			if dependencyBundle != nil && len(dependencyBundle.Artifacts) != 0 {
				return nil, nil, nil, fmt.Errorf("imported module %q emitted values", dependency)
			}
			for name, typeExpr := range loader.runtimeTypes[dependency] {
				state.Types[declaration.Name+"."+name] = typeExpr
			}
			_ = dependencyState
		}
		if err := loader.allocations.consume(len(entries) + 1); err != nil {
			return nil, nil, nil, sourceDiagnostic(path, declaration.Span, err)
		}
		imported := RuntimeValue{Value: runtimeValueData{Kind: Map}, Map: entries}
		state.Environment[declaration.Name] = imported
		if inferred, known := inferRuntimeValueStaticType(imported); known {
			state.StaticTypes[declaration.Name] = inferred
		}
	}
	declarationValues := 0
	for _, node := range module.Nodes {
		switch node.(type) {
		case *FunctionDeclaration, *RuleDeclaration:
			declarationValues++
		}
	}
	if err := loader.allocations.consume(declarationValues); err != nil {
		return nil, nil, nil, sourceDiagnostic(path, SourceSpan{}, err)
	}
	exports, bundle, err := evaluateModuleASTRuntime(module, state, entry)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if bundle == nil {
		bundle = &RuntimeBundle{}
	}
	if !entry {
		loader.runtimeCache[path] = slices.Clone(exports)
		exportedTypes := make(map[string]ResolvedType)
		typeScope := maps.Clone(state.Types)
		scopeID := TypeScopeID(path)
		state.Module.Analysis.registerScope(scopeID, typeScope)
		for _, node := range module.Nodes {
			declaration, ok := node.(*TypeDeclaration)
			if ok && declaration.Exported {
				typeExpr := state.Types[declaration.Name]
				typeExpr.ScopeID = scopeID
				exportedTypes[declaration.Name] = typeExpr
			}
		}
		loader.runtimeTypes[path] = exportedTypes
	}

	return exports, bundle, state, nil
}

func (loader *moduleLoader) loadedModulePaths() []string {
	paths := make([]string, 0, len(loader.loadedModules))
	for path := range loader.loadedModules {
		paths = append(paths, path)
	}
	slices.Sort(paths)

	return paths
}

func (loader *moduleLoader) resolveImport(importer, modulePath string) (string, error) {
	if loader.graph == nil {
		loader.graph = &moduleGraph{root: loader.root, files: loader.files}
	}

	return loader.graph.resolveImport(importer, modulePath)
}

func setModuleASTSourcePath(module ModuleAST, path string) {
	for _, node := range module.Nodes {
		switch declaration := node.(type) {
		case *ImportDeclaration:
			declaration.SourcePath = path
		case *TypeDeclaration:
			declaration.SourcePath = path
		case *RuleDeclaration:
			declaration.SourcePath = path
			if declaration.Rule != nil {
				for index := range declaration.Rule.Requirements {
					declaration.Rule.Requirements[index].SourcePath = path
				}
			}
		case *FunctionDeclaration:
			declaration.SourcePath = path
		case *LetDeclaration:
			declaration.SourcePath = path
		case *EmitStatement:
			declaration.SourcePath = path
		case *ValidateStatement:
			declaration.SourcePath = path
		}
	}
}

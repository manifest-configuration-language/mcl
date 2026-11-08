package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/manifest-configuration-language/mcl/internal/k8sschema"
	"github.com/manifest-configuration-language/mcl/internal/project"
	"github.com/manifest-configuration-language/mcl/internal/safepath"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

const maxMCLSourceBytes = syntax.MaxSourceBytes

// resolveModuleLinkはモジュールファイルのシンボリックリンクを解決する。解決できない場合はfalseを返す。
func resolveModuleLink(path string) (string, bool) {
	modulePath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}

	return modulePath, true
}

const (
	maxProjectMCLFiles    = 4096
	maxProjectMCLBytes    = 64 << 20
	projectConfigFileName = "mcl.toml"
)

// EvaluationOptionsはMCL評価全体に適用する実行オプションを保持します。
type EvaluationOptions struct {
	AllowNondeterministicOutput bool
	// CacheDirectory shares digest-addressed dependencies between project evaluations. It must be absolute and requires an OS-backed project filesystem; empty keeps the project-local cache.
	CacheDirectory        string
	KubernetesVersion     string
	ParameterSources      map[string]ParameterSource
	GitCredentialProvider GitCredentialProvider
}

type GitCredentials struct {
	Username string
	Password string
}

type GitCredentialProvider func(context.Context, string) (GitCredentials, error)

type moduleLoader struct {
	//nolint:containedctx // Contextはモジュール評価とAdapter呼び出しの間で共有します。
	ctx                         context.Context
	root                        string
	files                       *projectFilesystem
	graph                       *moduleGraph
	entry                       string
	entryText                   string
	runtimeCache                map[string][]RuntimeEntry
	runtimeTypes                map[string]map[string]ResolvedType
	dependencies                *dependency.Lock
	operation                   string
	allowNondeterministicOutput bool
	kubernetesVersion           string
	parameters                  map[string]jsontext.Value
	parameterSources            map[string]ParameterSource
	overlays                    map[string]string
	loadedModules               map[string]struct{}
	textImports                 map[string]string
	textBytes                   int64
	session                     *EvaluationSession
	analysis                    *TypeAnalysisContext
	crdSchemas                  map[customResourceDefinitionKey]crdVersionSchema
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
	return EvaluateFileWithParametersContext(ctx, path, nil)
}

// EvaluateFileWithParametersContext evaluates an MCL file with explicit runtime parameters.
func EvaluateFileWithParametersContext(ctx context.Context, path string, parameters map[string]jsontext.Value) (Bundle, error) {
	return EvaluateFileWithParametersAndOptionsContext(ctx, path, parameters, EvaluationOptions{})
}

// EvaluateFileWithOptionsContextは実行オプションを指定してMCLファイルを評価します。
func EvaluateFileWithOptionsContext(ctx context.Context, path string, options EvaluationOptions) (Bundle, error) {
	return EvaluateFileWithParametersAndOptionsContext(ctx, path, nil, options)
}

// EvaluateFileWithParametersAndOptionsContextは実行オプションとruntime parametersを指定してMCLファイルを評価します。
func EvaluateFileWithParametersAndOptionsContext(ctx context.Context, path string, parameters map[string]jsontext.Value, options EvaluationOptions) (Bundle, error) {
	return evaluateFileWithParameters(ctx, path, false, "", parameters, options)
}

// EvaluateFileForOperation は指定した操作向けにMCLファイルを評価します。
func EvaluateFileForOperation(path, operation string) (Bundle, error) {
	return EvaluateFileForOperationContext(context.Background(), path, operation)
}

// EvaluateFileForOperationContextは呼び出し元のContextを使って指定した操作向けにMCLファイルを評価します。
func EvaluateFileForOperationContext(ctx context.Context, path, operation string) (Bundle, error) {
	return EvaluateFileForOperationWithParametersContext(ctx, path, operation, nil)
}

// EvaluateFileForOperationWithParametersContext evaluates an MCL file for an operation with explicit runtime parameters.
func EvaluateFileForOperationWithParametersContext(ctx context.Context, path, operation string, parameters map[string]jsontext.Value) (Bundle, error) {
	if !validOperation(operation) {
		return Bundle{}, fmt.Errorf("unknown operation %q", operation)
	}

	return EvaluateFileForOperationWithParametersAndOptionsContext(ctx, path, operation, parameters, EvaluationOptions{})
}

// EvaluateFileForOperationWithOptionsContextは実行オプションを指定して操作向けにMCLファイルを評価します。
func EvaluateFileForOperationWithOptionsContext(ctx context.Context, path, operation string, options EvaluationOptions) (Bundle, error) {
	return EvaluateFileForOperationWithParametersAndOptionsContext(ctx, path, operation, nil, options)
}

// EvaluateFileForOperationWithParametersAndOptionsContextは実行オプションとruntime parametersを指定して操作向けにMCLファイルを評価します。
func EvaluateFileForOperationWithParametersAndOptionsContext(ctx context.Context, path, operation string, parameters map[string]jsontext.Value, options EvaluationOptions) (Bundle, error) {
	if !validOperation(operation) {
		return Bundle{}, fmt.Errorf("unknown operation %q", operation)
	}

	return evaluateFileWithParameters(ctx, path, false, operation, parameters, options)
}

// EvaluateFileForOperationWithParametersAndOptionsAndReadFilesContext evaluates an MCL file and returns project-relative paths opened during evaluation.
func EvaluateFileForOperationWithParametersAndOptionsAndReadFilesContext(ctx context.Context, path, operation string, parameters map[string]jsontext.Value, options EvaluationOptions) (Bundle, []string, error) {
	if !validOperation(operation) {
		return Bundle{}, nil, fmt.Errorf("unknown operation %q", operation)
	}
	if err := ctx.Err(); err != nil {
		return Bundle{}, nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Bundle{}, nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Bundle{}, nil, err
	}
	root, err := discoverProjectRoot(filepath.Dir(canonical))
	if err != nil {
		return Bundle{}, nil, err
	}
	files := newOSProjectFilesystem(root)
	bundle, err := evaluateFileWithFilesystemMode(ctx, canonical, false, false, operation, parameters, options, files)
	if info, statErr := os.Stat(filepath.Join(root, "mcl.lock")); statErr == nil && info.Mode().IsRegular() {
		files.reads.record("mcl.lock")
	}

	return bundle, files.readPaths(), err
}

func UpdateFile(path string) error {
	return UpdateFileContext(context.Background(), path)
}

// UpdateFileContextは呼び出し元のContextを使って依存ロックを更新します。
func UpdateFileContext(ctx context.Context, path string) error {
	return UpdateFileWithParametersContext(ctx, path, nil)
}

// UpdateFileWithParametersContext updates dependencies after evaluating an MCL file with explicit runtime parameters.
func UpdateFileWithParametersContext(ctx context.Context, path string, parameters map[string]jsontext.Value) error {
	return UpdateFileWithParametersAndOptionsContext(ctx, path, parameters, EvaluationOptions{})
}

// UpdateFileWithParametersAndOptionsContextは実行オプションとruntime parametersを指定して依存lockを更新します。
func UpdateFileWithParametersAndOptionsContext(ctx context.Context, path string, parameters map[string]jsontext.Value, options EvaluationOptions) error {
	_, err := evaluateFileWithParameters(ctx, path, true, "", parameters, options)

	return err
}

// FetchFileWithParametersAndOptionsContextはmcl.lockに記録された依存だけをcacheへ取得します。
func FetchFileWithParametersAndOptionsContext(ctx context.Context, path string, parameters map[string]jsontext.Value, options EvaluationOptions) error {
	_, err := evaluateFileWithParametersMode(ctx, path, false, true, "", parameters, options)

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
	mu           sync.Mutex
	files        *projectFilesystem
	snapshot     *project.WorkspaceSnapshot
	diskSources  map[string]string
	overlayPaths map[string]struct{}
	initialized  bool
}

// WorkspaceSnapshot returns the current immutable workspace snapshot, or nil when the index is uninitialized.
func (index *DependentMCLSourceIndex) WorkspaceSnapshot() *project.WorkspaceSnapshot {
	index.mu.Lock()
	defer index.mu.Unlock()
	if !index.initialized || index.snapshot == nil {
		return nil
	}

	return index.snapshot
}

// Sourcesはworkspaceを完全snapshotし、targetを直接または間接的にimportするMCLソースを返します。
func (index *DependentMCLSourceIndex) Sources(target string, workspaceRoots []string, overlays map[string]string) (map[string]string, error) {
	return index.SourcesWithContext(context.Background(), target, workspaceRoots, overlays)
}

// SourcesWithContext snapshots the workspace and returns sources that depend on target.
func (index *DependentMCLSourceIndex) SourcesWithContext(ctx context.Context, target string, workspaceRoots []string, overlays map[string]string) (map[string]string, error) {
	sources, _, err := index.SourcesWithSnapshotWithContext(ctx, target, workspaceRoots, overlays)

	return sources, err
}

// SourcesWithSnapshotWithContext snapshots the workspace and returns dependent sources with the exact immutable snapshot used.
func (index *DependentMCLSourceIndex) SourcesWithSnapshotWithContext(ctx context.Context, target string, workspaceRoots []string, overlays map[string]string) (map[string]string, *project.WorkspaceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	canonical, root, searchRoots, err := dependentSourceScope(target, workspaceRoots)
	if err != nil {
		return nil, nil, err
	}
	sources, err := index.sourcesForFilesystemWithContext(ctx, canonical, root, searchRoots, newOSProjectFilesystem(root), overlays)
	if err != nil {
		return nil, nil, err
	}
	return sources, index.snapshot, nil
}

// SourcesForProjectFS snapshots an injected project filesystem and returns sources that depend on targetPath.
// Paths use an absolute logical project root and slash-separated root-relative module paths.
func (index *DependentMCLSourceIndex) SourcesForProjectFS(projectRoot, targetPath string, files fs.FS, overlays map[string]string) (map[string]string, error) {
	return index.SourcesForProjectFSWithContext(context.Background(), projectRoot, targetPath, files, overlays)
}

// SourcesForProjectFSWithContext snapshots an injected project filesystem with cancellation support.
func (index *DependentMCLSourceIndex) SourcesForProjectFSWithContext(ctx context.Context, projectRoot, targetPath string, files fs.FS, overlays map[string]string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	filesystem, canonical, root, searchRoots, err := dependentProjectFSscope(projectRoot, targetPath, files)
	if err != nil {
		return nil, err
	}

	return index.sourcesForFilesystemWithContext(ctx, canonical, root, searchRoots, filesystem, overlays)
}

func (index *DependentMCLSourceIndex) sourcesForFilesystemWithContext(ctx context.Context, canonical, root string, searchRoots []string, filesystem *projectFilesystem, overlays map[string]string) (map[string]string, error) {
	diskSources, err := collectDependentSourcesWithFilesystemContext(ctx, filesystem, canonical, root, searchRoots)
	if err != nil {
		return nil, err
	}
	allSources, overlayPaths, err := applyDependentOverlaysWithContext(ctx, diskSources, root, searchRoots, overlays)
	if err != nil {
		return nil, err
	}
	modules := make(map[string]ModuleAST, len(allSources))
	if index.snapshot != nil && index.snapshot.Root() == root && slices.Equal(index.snapshot.SearchRoots(), searchRoots) {
		for path, source := range allSources {
			if _, hash, exists := index.snapshot.Source(path); !exists || hash != sha256.Sum256([]byte(source)) {
				continue
			}
			if module, exists := index.snapshot.Module(path); exists {
				modules[path] = module
			}
		}
	}
	for path, source := range allSources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
	if index.snapshot != nil && index.snapshot.Root() == root && slices.Equal(index.snapshot.SearchRoots(), searchRoots) {
		for _, path := range index.snapshot.Paths() {
			dependencies[path] = index.snapshot.Dependencies(path)
			if paths := index.snapshot.Importers(path); len(paths) != 0 {
				importers[path] = paths
			}
			if _, exists := modules[path]; !exists {
				delete(dependencies, path)
				removeImporterPath(importers, path)
			}
		}
	}
	for path, module := range modules {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resolvedDependencies, err := resolveModuleDependencies(path, module, root, filesystem)
		if err != nil {
			return nil, err
		}
		if slices.Equal(dependencies[path], resolvedDependencies) {
			continue
		}
		removeImporterPath(importers, path)
		dependencies[path] = resolvedDependencies
		for _, dependency := range resolvedDependencies {
			importers[dependency] = append(importers[dependency], path)
		}
	}
	generation := uint64(1)
	if index.snapshot != nil {
		generation = index.snapshot.Generation() + 1
	}
	snapshot := index.snapshot
	matches, err := workspaceSnapshotMatches(ctx, snapshot, root, searchRoots, allSources, dependencies)
	if err != nil {
		return nil, err
	}
	if !matches {
		snapshot, err = project.NewWorkspaceSnapshotFromParsed(generation, root, searchRoots, allSources, modules, dependencies, importers)
		if err != nil {
			return nil, err
		}
	}
	result, err := snapshot.DependentSources(ctx, canonical)
	if err != nil {
		return nil, err
	}
	index.snapshot = snapshot
	index.files = filesystem
	index.diskSources = diskSources
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

func workspaceSnapshotMatches(ctx context.Context, snapshot *project.WorkspaceSnapshot, root string, searchRoots []string, sources map[string]string, dependencies map[string][]string) (bool, error) {
	if snapshot == nil {
		return false, nil
	}
	canonicalSearchRoots, err := canonicalProjectSnapshotPaths(ctx, searchRoots)
	if err != nil {
		return false, err
	}
	if snapshot.Root() != canonicalProjectSnapshotPath(root) || !slices.Equal(snapshot.SearchRoots(), canonicalSearchRoots) {
		return false, nil
	}

	canonicalSources := make(map[string]string, len(sources))
	for path, source := range sources {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		canonicalPath := canonicalProjectSnapshotPath(path)
		if _, exists := canonicalSources[canonicalPath]; exists {
			return false, nil
		}
		canonicalSources[canonicalPath] = source
	}
	paths := snapshot.Paths()
	if len(paths) != len(canonicalSources) {
		return false, nil
	}
	for path, source := range canonicalSources {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if previous, _, exists := snapshot.Source(path); !exists || previous != source {
			return false, nil
		}
	}

	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if _, exists := canonicalSources[canonicalProjectSnapshotPath(path)]; !exists {
			return false, nil
		}
	}
	canonicalDependencies := make(map[string][]string, len(dependencies))
	for path, imported := range dependencies {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		canonicalPath := canonicalProjectSnapshotPath(path)
		if _, exists := canonicalDependencies[canonicalPath]; exists {
			return false, nil
		}
		canonicalImports, err := canonicalProjectSnapshotPaths(ctx, imported)
		if err != nil {
			return false, err
		}
		canonicalDependencies[canonicalPath] = canonicalImports
	}
	if len(canonicalDependencies) != len(paths) {
		return false, nil
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !slices.Equal(snapshot.Dependencies(path), canonicalDependencies[canonicalProjectSnapshotPath(path)]) {
			return false, nil
		}
	}

	return true, nil
}

func canonicalProjectSnapshotPath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}

func canonicalProjectSnapshotPaths(ctx context.Context, paths []string) ([]string, error) {
	canonical := make([]string, len(paths))
	for index, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		canonical[index] = canonicalProjectSnapshotPath(path)
	}
	return canonical, nil
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

func applyDependentOverlaysWithContext(ctx context.Context, diskSources map[string]string, root string, searchRoots []string, overlays map[string]string) (map[string]string, map[string]struct{}, error) {
	sources := maps.Clone(diskSources)
	paths := make(map[string]struct{}, len(overlays))
	seen := make(map[string]string, len(overlays))
	keys := slices.Sorted(maps.Keys(overlays))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
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
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
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
	return index.SourcesFromIndexWithContext(context.Background(), target, workspaceRoots, overlays)
}

func (index *DependentMCLSourceIndex) SourcesFromIndexWithContext(ctx context.Context, target string, workspaceRoots []string, overlays map[string]string) (map[string]string, error) {
	sources, _, err := index.SourcesFromIndexWithSnapshotWithContext(ctx, target, workspaceRoots, overlays)

	return sources, err
}

// SourcesFromIndexWithSnapshotWithContext returns dependent sources and the exact immutable snapshot used.
func (index *DependentMCLSourceIndex) SourcesFromIndexWithSnapshotWithContext(ctx context.Context, target string, workspaceRoots []string, overlays map[string]string) (map[string]string, *project.WorkspaceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	canonical, root, searchRoots, err := dependentSourceScope(target, workspaceRoots)
	if err != nil {
		return nil, nil, err
	}
	if !index.initialized || index.snapshot == nil || index.snapshot.Root() != root || !slices.Equal(index.snapshot.SearchRoots(), searchRoots) || index.files == nil || !index.files.osBacked {
		sources, err := index.sourcesForFilesystemWithContext(ctx, canonical, root, searchRoots, newOSProjectFilesystem(root), overlays)
		if err != nil {
			return nil, nil, err
		}
		return sources, index.snapshot, nil
	}
	sources, err := index.sourcesFromIndexWithContext(ctx, canonical, root, searchRoots, index.files, overlays)
	if err != nil {
		return nil, nil, err
	}
	return sources, index.snapshot, nil
}

// SourcesFromProjectFSIndex applies overlays to the last snapshot created from the supplied filesystem.
// If no compatible snapshot exists, it creates a complete snapshot first.
func (index *DependentMCLSourceIndex) SourcesFromProjectFSIndex(projectRoot, targetPath string, files fs.FS, overlays map[string]string) (map[string]string, error) {
	return index.SourcesFromProjectFSIndexWithContext(context.Background(), projectRoot, targetPath, files, overlays)
}

func (index *DependentMCLSourceIndex) SourcesFromProjectFSIndexWithContext(ctx context.Context, projectRoot, targetPath string, files fs.FS, overlays map[string]string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	filesystem, canonical, root, searchRoots, err := dependentProjectFSscope(projectRoot, targetPath, files)
	if err != nil {
		return nil, err
	}
	if !index.initialized || index.snapshot == nil || index.snapshot.Root() != root || !slices.Equal(index.snapshot.SearchRoots(), searchRoots) || index.files == nil || index.files.osBacked {
		return index.sourcesForFilesystemWithContext(ctx, canonical, root, searchRoots, filesystem, overlays)
	}

	return index.sourcesFromIndexWithContext(ctx, canonical, root, searchRoots, filesystem, overlays)
}

func (index *DependentMCLSourceIndex) sourcesFromIndexWithContext(ctx context.Context, canonical, root string, searchRoots []string, files *projectFilesystem, overlays map[string]string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	diskSources := maps.Clone(index.diskSources)
	overlaySources, nextOverlayPaths, err := applyDependentOverlaysWithContext(ctx, diskSources, root, searchRoots, overlays)
	if err != nil {
		return nil, err
	}
	effectiveSources := maps.Clone(overlaySources)
	changed := make(map[string]struct{})
	for path := range index.overlayPaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, remainsOpen := nextOverlayPaths[path]; remainsOpen {
			continue
		}
		content, readErr := files.readLimitedFile(path)
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
		} else if old, _, oldExists := index.snapshot.Source(path); !oldExists || old != source {
			changed[path] = struct{}{}
		}
	}
	index.files = files
	if err := index.applySnapshotChangesWithContext(ctx, diskSources, effectiveSources, nextOverlayPaths, changed, files); err != nil {
		index.initialized = false

		return nil, err
	}
	index.diskSources = diskSources
	index.overlayPaths = nextOverlayPaths
	index.files = files

	return index.snapshot.DependentSources(ctx, canonical)
}

// DependentMCLFileChangeはLSPが通知したMCLファイルの変更を表します。
type DependentMCLFileChange struct {
	Path    string
	Deleted bool
}

// Invalidateはworkspaceのproject境界が変わった場合にcached snapshotを無効化します。
func (index *DependentMCLSourceIndex) Invalidate() {
	index.mu.Lock()
	defer index.mu.Unlock()
	index.initialized = false
}

// ApplyFileChangesは監視通知に含まれるファイルだけを読み直して逆参照indexを更新します。
func (index *DependentMCLSourceIndex) ApplyFileChanges(changes []DependentMCLFileChange) error {
	return index.ApplyFileChangesWithContext(context.Background(), changes)
}

// ApplyFileChangesWithContext applies watched file changes using the supplied context.
func (index *DependentMCLSourceIndex) ApplyFileChangesWithContext(ctx context.Context, changes []DependentMCLFileChange) error {
	index.mu.Lock()
	defer index.mu.Unlock()
	if !index.initialized || index.snapshot == nil {
		return nil
	}
	diskSources := maps.Clone(index.diskSources)
	files := index.files
	if files == nil {
		files = newOSProjectFilesystem(index.snapshot.Root())
	}
	effectiveSources := index.snapshot.Sources()
	changed := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		path, err := canonicalWatchedModulePath(change.Path, change.Deleted, diskSources, files)
		if err != nil {
			index.initialized = false

			return err
		}
		if path == "" {
			index.initialized = false

			return nil
		}
		if filepath.Ext(path) != ".mcl" || !safepath.Within(index.snapshot.Root(), path) || !pathWithinAny(index.snapshot.SearchRoots(), path) {
			continue
		}
		if files.osBacked {
			moduleRoot, rootErr := discoverProjectRoot(filepath.Dir(path))
			if rootErr != nil || moduleRoot != index.snapshot.Root() {
				if _, wasIndexed := diskSources[path]; wasIndexed {
					index.initialized = false

					return nil
				}

				continue
			}
		}
		if ignoredRenameSourcePath(path, index.snapshot.SearchRoots()) {
			if _, overlaid := index.overlayPaths[path]; overlaid {
				if change.Deleted {
					delete(diskSources, path)
				} else {
					content, err := files.readLimitedFile(path)
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
			content, err := files.readLimitedFile(path)
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
	if err := index.applySnapshotChangesWithContext(ctx, diskSources, effectiveSources, index.overlayPaths, changed, files); err != nil {
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

func canonicalWatchedModulePath(path string, deleted bool, diskSources map[string]string, files *projectFilesystem) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if !files.osBacked {
		return absolute, nil
	}
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

func (index *DependentMCLSourceIndex) applySnapshotChangesWithContext(ctx context.Context, diskSources, sources map[string]string, overlayPaths map[string]struct{}, changed map[string]struct{}, files *projectFilesystem) error {
	if index.snapshot == nil {
		return errors.New("workspace source snapshot is not initialized")
	}
	var totalBytes int64
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		totalBytes += int64(len(source))
	}
	if len(sources) > maxProjectMCLFiles || totalBytes > maxProjectMCLBytes {
		return errors.New("workspace MCL source index exceeds its configured limits")
	}
	modules := make(map[string]ModuleAST, len(sources))
	dependencies := make(map[string][]string)
	importers := make(map[string][]string)
	if index.snapshot != nil {
		modules = index.snapshot.Modules()
		for _, path := range index.snapshot.Paths() {
			dependencies[path] = index.snapshot.Dependencies(path)
			if paths := index.snapshot.Importers(path); len(paths) != 0 {
				importers[path] = paths
			}
		}
	}
	for path := range changed {
		if err := ctx.Err(); err != nil {
			return err
		}
		source, exists := sources[path]
		if !exists {
			delete(modules, path)
			delete(dependencies, path)
			removeImporterPath(importers, path)

			continue
		}
		module, err := ParseAST(source)
		if err != nil {
			return fmt.Errorf("parse workspace module %q: %w", path, sourceDiagnosticPath(path, err))
		}
		modules[path] = module
	}
	for path := range changed {
		if err := ctx.Err(); err != nil {
			return err
		}
		module, exists := modules[path]
		if !exists {
			continue
		}
		resolved, err := resolveModuleDependencies(path, module, index.snapshot.Root(), files)
		if err != nil {
			return err
		}
		removeImporterPath(importers, path)
		dependencies[path] = resolved
		for _, dependency := range resolved {
			importers[dependency] = append(importers[dependency], path)
		}
	}
	for path, importersOfPath := range importers {
		if err := ctx.Err(); err != nil {
			return err
		}
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
			if _, err := resolveModuleDependencies(importer, module, index.snapshot.Root(), files); err != nil {
				return err
			}
		}
	}
	generation := uint64(1)
	generation = index.snapshot.Generation() + 1
	matches, err := workspaceSnapshotMatches(ctx, index.snapshot, index.snapshot.Root(), index.snapshot.SearchRoots(), sources, dependencies)
	if err != nil {
		return err
	}
	if matches {
		index.diskSources = maps.Clone(diskSources)
		index.overlayPaths = maps.Clone(overlayPaths)
		index.files = files

		return nil
	}
	snapshot, err := project.NewWorkspaceSnapshotFromParsed(generation, index.snapshot.Root(), index.snapshot.SearchRoots(), sources, modules, dependencies, importers)
	if err != nil {
		return err
	}
	index.snapshot = snapshot
	index.diskSources = maps.Clone(diskSources)
	index.overlayPaths = maps.Clone(overlayPaths)
	index.files = files

	return nil
}

func resolveModuleDependencies(path string, module ModuleAST, root string, files *projectFilesystem) ([]string, error) {
	graph := moduleGraph{root: root, files: files}
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

// DependentMCLSourcesForProjectFS returns project modules that directly or indirectly import targetPath.
// Project and target paths use an absolute logical root and slash-separated root-relative names.
func DependentMCLSourcesForProjectFS(projectRoot, targetPath string, files fs.FS) (map[string]string, error) {
	filesystem, target, root, searchRoots, err := dependentProjectFSscope(projectRoot, targetPath, files)
	if err != nil {
		return nil, err
	}
	sources, err := collectDependentSourcesWithFilesystem(filesystem, target, root, searchRoots)
	if err != nil {
		return nil, err
	}
	importers := make(map[string][]string)
	for modulePath, source := range sources {
		module, err := ParseAST(source)
		if err != nil {
			return nil, fmt.Errorf("parse workspace module %q: %w", modulePath, sourceDiagnosticPath(modulePath, err))
		}
		graph := moduleGraph{root: root, files: filesystem}
		imports, err := graph.imports(modulePath, module)
		if err != nil {
			return nil, err
		}
		for _, imported := range imports {
			importers[imported.path] = append(importers[imported.path], modulePath)
		}
	}

	return dependentModuleSources(target, sources, importers)
}

func dependentProjectFSscope(projectRoot, targetPath string, files fs.FS) (*projectFilesystem, string, string, []string, error) {
	if files == nil {
		return nil, "", "", nil, errors.New("project filesystem is required")
	}
	if !filepath.IsAbs(projectRoot) {
		return nil, "", "", nil, fmt.Errorf("project root %q must be absolute", projectRoot)
	}
	if !validProjectPath(targetPath) || filepath.Ext(targetPath) != ".mcl" {
		return nil, "", "", nil, fmt.Errorf("invalid project target path %q", targetPath)
	}
	root := filepath.Clean(projectRoot)
	target := filepath.Join(root, filepath.FromSlash(targetPath))
	filesystem := newProjectFilesystem(root, files)
	relative, err := filesystem.relativePath(target)
	if err != nil {
		return nil, "", "", nil, err
	}
	info, err := fs.Stat(filesystem.files, relative)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("inspect target module %q: %w", target, err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", "", nil, fmt.Errorf("target module %q must be a regular file", target)
	}

	return filesystem, target, root, []string{root}, nil
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

func collectDependentSourcesWithFilesystem(files *projectFilesystem, canonical, root string, searchRoots []string) (map[string]string, error) {
	return collectDependentSourcesWithFilesystemContext(context.Background(), files, canonical, root, searchRoots)
}

func collectDependentSourcesWithFilesystemContext(ctx context.Context, files *projectFilesystem, canonical, root string, searchRoots []string) (map[string]string, error) {
	allSources := make(map[string]string)
	var totalBytes int64
	for _, searchRoot := range searchRoots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		searchPath, err := files.relativePath(searchRoot)
		if err != nil {
			return nil, err
		}
		err = fs.WalkDir(files.files, searchPath, func(path string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			absolutePath := filepath.Join(root, filepath.FromSlash(path))
			if entry.IsDir() {
				if absolutePath != searchRoot && ignoredRenameIndexDirectory(absolutePath) {
					return filepath.SkipDir
				}

				return nil
			}
			if filepath.Ext(absolutePath) != ".mcl" {
				return nil
			}
			if !entry.Type().IsRegular() && (!files.osBacked || entry.Type()&fs.ModeSymlink == 0) {
				return nil
			}
			modulePath := absolutePath
			if files.osBacked {
				var ok bool
				modulePath, ok = resolveModuleLink(absolutePath)
				if !ok {
					return nil
				}
			}
			modulePath, err := filepath.Abs(modulePath)
			if err != nil {
				return err
			}
			modulePath = filepath.Clean(modulePath)
			if !safepath.Within(root, modulePath) || !safepath.Within(searchRoot, modulePath) {
				return nil
			}
			if files.osBacked {
				moduleRoot, err := discoverProjectRoot(filepath.Dir(modulePath))
				if err != nil {
					return err
				}
				if moduleRoot != root {
					return nil
				}
			}
			if _, exists := allSources[modulePath]; exists {
				return nil
			}
			if len(allSources) >= maxProjectMCLFiles {
				return fmt.Errorf("workspace contains more than %d MCL files", maxProjectMCLFiles)
			}
			relativePath, err := files.relativePath(modulePath)
			if err != nil {
				return err
			}
			info, err := fs.Stat(files.files, relativePath)
			if err != nil {
				return fmt.Errorf("inspect module %q: %w", modulePath, err)
			}
			if info.Size() > maxMCLSourceBytes {
				return fmt.Errorf("module %q exceeds the %d-byte limit", modulePath, maxMCLSourceBytes)
			}
			if totalBytes+info.Size() > maxProjectMCLBytes {
				return fmt.Errorf("workspace MCL sources exceed the %d-byte limit", maxProjectMCLBytes)
			}
			content, err := files.readLimitedFile(modulePath)
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, err := files.readLimitedFile(canonical)
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
	return dependentModuleSourcesWithContext(context.Background(), target, sources, importers)
}

func dependentModuleSourcesWithContext(ctx context.Context, target string, sources map[string]string, importers map[string][]string) (map[string]string, error) {
	dependent := map[string]struct{}{target: {}}
	queue := []string{target}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if source, exists := sources[path]; exists {
			result[path] = source
		}
	}

	return result, nil
}

func evaluateFileWithParameters(ctx context.Context, path string, update bool, operation string, parameters map[string]jsontext.Value, options EvaluationOptions) (Bundle, error) {
	return evaluateFileWithParametersMode(ctx, path, update, false, operation, parameters, options)
}

func evaluateFileWithParametersMode(ctx context.Context, path string, update, fetchLocked bool, operation string, parameters map[string]jsontext.Value, options EvaluationOptions) (Bundle, error) {
	return evaluateFileWithFilesystemMode(ctx, path, update, fetchLocked, operation, parameters, options, nil)
}

func evaluateFileWithFilesystem(ctx context.Context, path string, update bool, operation string, parameters map[string]jsontext.Value, options EvaluationOptions, files *projectFilesystem) (Bundle, error) {
	return evaluateFileWithFilesystemMode(ctx, path, update, false, operation, parameters, options, files)
}

func evaluateFileWithFilesystemMode(ctx context.Context, path string, update, fetchLocked bool, operation string, parameters map[string]jsontext.Value, options EvaluationOptions, files *projectFilesystem) (Bundle, error) {
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	version, err := k8sschema.NormalizeKubernetesVersion(options.KubernetesVersion)
	if err != nil {
		return Bundle{}, err
	}
	options.KubernetesVersion = version
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
	var dependencies *dependency.Lock
	if fetchLocked && files.osBacked {
		dependencies, err = dependency.OpenLockedFetch(ctx, root, dependencyToolchainIdentity())
	} else if fetchLocked {
		dependencies, err = dependency.OpenLockedFetchWithFS(ctx, root, dependencyToolchainIdentity(), files.files)
	} else if files.osBacked {
		dependencies, err = dependency.Open(ctx, root, update, dependencyToolchainIdentity())
	} else {
		dependencies, err = dependency.OpenWithFS(ctx, root, update, dependencyToolchainIdentity(), files.files)
	}
	if err != nil {
		return Bundle{}, err
	}
	if options.CacheDirectory != "" {
		if err := dependencies.SetCacheDirectory(options.CacheDirectory); err != nil {
			return Bundle{}, err
		}
	}
	if options.GitCredentialProvider != nil {
		dependencies.SetGitCredentialProvider(func(ctx context.Context, source string) (dependency.GitCredentials, error) {
			credentials, err := options.GitCredentialProvider(ctx, source)

			return dependency.GitCredentials{Username: credentials.Username, Password: credentials.Password}, err
		})
	}
	crdSchemaSources, err := configuredCRDSchemaSources(files)
	if err != nil {
		return Bundle{}, err
	}
	loader := moduleLoader{
		ctx:  ctx,
		root: root, files: files, entry: canonical, entryText: string(source),
		runtimeCache: make(map[string][]RuntimeEntry), runtimeTypes: make(map[string]map[string]ResolvedType),
		dependencies: dependencies, operation: operation, parameters: maps.Clone(parameters),
		allowNondeterministicOutput: options.AllowNondeterministicOutput, parameterSources: cloneParameterSources(options.ParameterSources), loadedModules: make(map[string]struct{}), session: &EvaluationSession{Budget: &functionBudget{}},
		kubernetesVersion: options.KubernetesVersion,
		analysis:          &TypeAnalysisContext{},
	}
	_, runtimeBundle, state, err := loader.evaluateRuntime(canonical, true) //nolint:contextcheck // moduleLoaderが評価全体のContextを保持します。
	if err != nil {
		return Bundle{}, err
	}
	crdSchemas, err := loadCRDSchemaCatalogForRuntimeBundle(ctx, dependencies, crdSchemaSources, runtimeBundle, update)
	if err != nil {
		return Bundle{}, err
	}
	loader.crdSchemas = crdSchemas
	if state.Module != nil {
		state.Module.CRDSchemas = crdSchemas
	}
	if err := resolveRuntimeBundle(runtimeBundle, state); err != nil { //nolint:contextcheck // EvaluationState経由で評価ContextをAdapterへ渡します。
		return Bundle{}, err
	}
	bundle, err := runtimeBundleToBundle(*runtimeBundle)
	if err != nil {
		return Bundle{}, err
	}
	if state.Module != nil {
		bundle.resourceDescriptors, err = kubernetesResourceDescriptorsFromCRDSchemas(state.Module.CRDSchemas)
		if err != nil {
			return Bundle{}, err
		}
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
		marker := filepath.Join(root, projectConfigFileName)
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

// EvaluateProjectSourceWithParametersContext evaluates source from a project filesystem with explicit runtime parameters.
func EvaluateProjectSourceWithParametersContext(ctx context.Context, projectRoot, entryPath, source string, files fs.FS, overlays map[string]string, parameters map[string]jsontext.Value) (Bundle, error) {
	if ctx == nil {
		return Bundle{}, errors.New("evaluation context is required")
	}
	if files == nil {
		return Bundle{}, errors.New("project filesystem is required")
	}
	if !filepath.IsAbs(projectRoot) {
		return Bundle{}, fmt.Errorf("project root %q must be absolute", projectRoot)
	}
	if !validProjectPath(entryPath) {
		return Bundle{}, fmt.Errorf("invalid project entry path %q", entryPath)
	}
	root := filepath.Clean(projectRoot)
	entry := filepath.Join(root, filepath.FromSlash(entryPath))
	absoluteOverlays := make(map[string]string, len(overlays))
	for overlayPath, overlaySource := range overlays {
		if !validProjectPath(overlayPath) {
			return Bundle{}, fmt.Errorf("invalid project overlay path %q", overlayPath)
		}
		absoluteOverlays[filepath.Join(root, filepath.FromSlash(overlayPath))] = overlaySource
	}
	bundle, _, err := evaluateSourceWithFilesystemAndParameters(ctx, entry, source, absoluteOverlays, newProjectFilesystem(root, files), parameters, EvaluationOptions{})

	return bundle, err
}

func evaluateSourceWithFilesystem(ctx context.Context, path, source string, overlays map[string]string, files *projectFilesystem) (Bundle, []string, error) {
	return evaluateSourceWithFilesystemAndParameters(ctx, path, source, overlays, files, nil, EvaluationOptions{})
}

func evaluateSourceWithFilesystemAndParameters(ctx context.Context, path, source string, overlays map[string]string, files *projectFilesystem, parameters map[string]jsontext.Value, options EvaluationOptions) (Bundle, []string, error) {
	if err := ctx.Err(); err != nil {
		return Bundle{}, nil, err
	}
	version, err := k8sschema.NormalizeKubernetesVersion(options.KubernetesVersion)
	if err != nil {
		return Bundle{}, nil, err
	}
	options.KubernetesVersion = version
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
	dependencies, err := dependency.OpenWithFS(ctx, root, false, dependencyToolchainIdentity(), files.files)
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
	crdSchemaSources, err := configuredCRDSchemaSources(files)
	if err != nil {
		return Bundle{}, nil, err
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
		ctx:                         ctx,
		root:                        root,
		files:                       files,
		entry:                       entry,
		entryText:                   source,
		parameters:                  maps.Clone(parameters),
		runtimeCache:                make(map[string][]RuntimeEntry),
		runtimeTypes:                make(map[string]map[string]ResolvedType),
		dependencies:                dependencies,
		allowNondeterministicOutput: options.AllowNondeterministicOutput,
		kubernetesVersion:           options.KubernetesVersion,
		parameterSources:            cloneParameterSources(options.ParameterSources),
		overlays:                    canonicalOverlays,
		loadedModules:               make(map[string]struct{}),
		session:                     &EvaluationSession{Budget: &functionBudget{}, Allocations: &runtimeAllocationBudget{}},
		analysis:                    &TypeAnalysisContext{},
	}
	_, runtimeBundle, state, err := loader.evaluateRuntime(entry, true) //nolint:contextcheck // moduleLoaderが評価全体のContextを保持します。
	loadedModules := loader.loadedModulePaths()
	if err != nil {
		return Bundle{}, loadedModules, err
	}
	crdSchemas, err := loadCRDSchemaCatalogForRuntimeBundle(ctx, dependencies, crdSchemaSources, runtimeBundle, false)
	if err != nil {
		return Bundle{}, loadedModules, err
	}
	loader.crdSchemas = crdSchemas
	if state.Module != nil {
		state.Module.CRDSchemas = crdSchemas
	}
	if err := resolveRuntimeBundle(runtimeBundle, state); err != nil { //nolint:contextcheck // EvaluationState経由で評価ContextをAdapterへ渡します。
		return Bundle{}, loadedModules, err
	}
	bundle, err := runtimeBundleToBundle(*runtimeBundle)
	if err != nil {
		return Bundle{}, loadedModules, err
	}
	if state.Module != nil {
		bundle.resourceDescriptors, err = kubernetesResourceDescriptorsFromCRDSchemas(state.Module.CRDSchemas)
		if err != nil {
			return Bundle{}, loadedModules, err
		}
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
	if loader.analysis == nil {
		loader.analysis = &TypeAnalysisContext{}
	}
	if err := loader.ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	if loader.session == nil {
		loader.session = &EvaluationSession{}
	}
	if loader.session.Budget == nil {
		loader.session.Budget = &functionBudget{}
	}
	if loader.session.Allocations == nil {
		loader.session.Allocations = &runtimeAllocationBudget{}
	}
	loader.loadedModules[path] = struct{}{}
	if !entry {
		if exports, cached := loader.runtimeCache[path]; cached {
			state := &EvaluationState{
				EvaluationSession: loader.session,
				Types:             maps.Clone(loader.runtimeTypes[path]),
			}

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
	types, err := collectTypeDefinitionsAST(module)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := loadTypeSchemaFilesWithFilesystem(types, loader.root, path, loader.dependencies, loader.files, loader.analysis); err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	moduleContext := &ModuleContext{Context: loader.ctx, ProjectRoot: loader.root, filesystem: loader.files, Dependencies: loader.dependencies, HelmOperation: loader.operation, AllowNondeterministicOutput: loader.allowNondeterministicOutput, KubernetesVersion: loader.kubernetesVersion, Analysis: loader.analysis, CRDSchemas: loader.crdSchemas}
	state := &EvaluationState{
		EvaluationSession: loader.session,
		Environment:       make(map[string]RuntimeValue), StaticTypes: make(map[string]TypeAnalysis), Types: types,
		Stack: make(map[string]bool), SourcePath: path, Module: moduleContext,
	}
	if entry {
		state.Parameters = maps.Clone(loader.parameters)
		state.ParameterSources = loader.parameterSources
	}
	for _, node := range module.Nodes {
		declaration, ok := node.(*ImportDeclaration)
		if !ok {
			continue
		}
		if _, exists := state.Environment[declaration.Name]; exists {
			return nil, nil, nil, fmt.Errorf("%s:%d: %q is already defined", path, declaration.Line, declaration.Name)
		}
		var imported RuntimeValue
		var entries []RuntimeEntry
		if declaration.Text {
			dependency, err := loader.resolveImport(path, declaration.ModulePath)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s:%d: %w", path, declaration.Line, err)
			}
			text, err := loader.readTextImport(dependency)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s:%d: read imported text %q: %w", path, declaration.Line, declaration.ModulePath, err)
			}
			imported = RuntimeValue{Value: runtimeValueData{Kind: String, Text: text}}
		} else if strings.HasPrefix(declaration.ModulePath, "std:") {
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
			entries, dependencyBundle, _, err = loader.evaluateRuntime(dependency, false)
			if err != nil {
				return nil, nil, nil, err
			}
			if dependencyBundle != nil && len(dependencyBundle.Artifacts) != 0 {
				return nil, nil, nil, fmt.Errorf("imported module %q emitted values", dependency)
			}
			for name, typeExpr := range loader.runtimeTypes[dependency] {
				state.Types[declaration.Name+"."+name] = typeExpr
			}
		}
		if err := loader.session.Allocations.consume(len(entries) + 1); err != nil {
			return nil, nil, nil, sourceDiagnostic(path, declaration.Span, err)
		}
		if !declaration.Text {
			imported = RuntimeValue{Value: runtimeValueData{Kind: Map}, Map: entries}
		}
		state.Environment[declaration.Name] = imported
		if inferred, known := inferRuntimeValueStaticType(imported); known {
			state.StaticTypes[declaration.Name] = inferred
		}
	}
	if err := validateTypeDefinitionsAST(module, state.Types); err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	declarationValues := 0
	for _, node := range module.Nodes {
		switch node.(type) {
		case *FunctionDeclaration, *RuleDeclaration:
			declarationValues++
		}
	}
	if err := loader.session.Allocations.consume(declarationValues); err != nil {
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
				typeExpr = expandStaticTypeAliases(typeExpr, state.Types, state.Module.Analysis, make(map[string]bool))
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

func (loader *moduleLoader) readTextImport(path string) (string, error) {
	if text, exists := loader.textImports[path]; exists {
		return text, nil
	}
	if len(loader.textImports) >= maxProjectMCLFiles {
		return "", fmt.Errorf("project imports more than %d text files", maxProjectMCLFiles)
	}
	data, err := loader.files.readLimitedFile(path)
	if err != nil {
		return "", err
	}
	if loader.textBytes+int64(len(data)) > maxProjectMCLBytes {
		return "", fmt.Errorf("project imported text exceeds the %d-byte limit", maxProjectMCLBytes)
	}
	if !utf8.Valid(data) {
		return "", errors.New("imported text is not valid UTF-8")
	}
	text := string(data)
	if loader.textImports == nil {
		loader.textImports = make(map[string]string)
	}
	loader.textImports[path] = text
	loader.textBytes += int64(len(data))

	return text, nil
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

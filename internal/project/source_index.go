package project

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/manifest-configuration-language/mcl/internal/safepath"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

// DependentMCLSourceIndexはworkspaceのimport逆参照indexを保持します。
type DependentMCLSourceIndex struct {
	mu           sync.Mutex
	files        *Filesystem
	snapshot     *WorkspaceSnapshot
	diskSources  map[string]string
	overlayPaths map[string]struct{}
	initialized  bool
}

// WorkspaceSnapshot returns the current immutable workspace snapshot, or nil when the index is uninitialized.
func (index *DependentMCLSourceIndex) WorkspaceSnapshot() *WorkspaceSnapshot {
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
func (index *DependentMCLSourceIndex) SourcesWithSnapshotWithContext(ctx context.Context, target string, workspaceRoots []string, overlays map[string]string) (map[string]string, *WorkspaceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	canonical, root, searchRoots, err := dependentSourceScope(target, workspaceRoots)
	if err != nil {
		return nil, nil, err
	}
	sources, err := index.sourcesForFilesystemWithContext(ctx, canonical, root, searchRoots, NewOSFilesystem(root), overlays)
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

func (index *DependentMCLSourceIndex) sourcesForFilesystemWithContext(ctx context.Context, canonical, root string, searchRoots []string, filesystem *Filesystem, overlays map[string]string) (map[string]string, error) {
	diskSources, err := collectDependentSourcesWithFilesystemContext(ctx, filesystem, canonical, root, searchRoots)
	if err != nil {
		return nil, err
	}
	allSources, overlayPaths, err := applyDependentOverlaysWithContext(ctx, diskSources, root, searchRoots, overlays)
	if err != nil {
		return nil, err
	}
	modules := make(map[string]syntax.ModuleAST, len(allSources))
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
		module, err := parseWorkspaceModule(path, source)
		if err != nil {
			return nil, err
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
		resolvedDependencies, err := resolveModuleDependencies(path, module, filesystem)
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
		snapshot, err = NewWorkspaceSnapshotFromParsed(generation, root, searchRoots, allSources, modules, dependencies, importers)
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

func workspaceSnapshotMatches(ctx context.Context, snapshot *WorkspaceSnapshot, root string, searchRoots []string, sources map[string]string, dependencies map[string][]string) (bool, error) {
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
	if filepath.Ext(absolute) != ".mcl" || !safepath.Within(root, absolute) || !WithinAny(searchRoots, absolute) {
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
		if len(overlays[key]) > MaxSourceBytes {
			return nil, nil, fmt.Errorf("module %q exceeds the %d-byte limit", path, MaxSourceBytes)
		}
		if _, exists := sources[path]; !exists && len(sources) >= MaxSourceFiles {
			return nil, nil, fmt.Errorf("workspace contains more than %d MCL files", MaxSourceFiles)
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
		if totalBytes > MaxSourceTotal {
			return nil, nil, fmt.Errorf("project MCL sources exceed the %d-byte limit", MaxSourceTotal)
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
func (index *DependentMCLSourceIndex) SourcesFromIndexWithSnapshotWithContext(ctx context.Context, target string, workspaceRoots []string, overlays map[string]string) (map[string]string, *WorkspaceSnapshot, error) {
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
		sources, err := index.sourcesForFilesystemWithContext(ctx, canonical, root, searchRoots, NewOSFilesystem(root), overlays)
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

func (index *DependentMCLSourceIndex) sourcesFromIndexWithContext(ctx context.Context, canonical, root string, searchRoots []string, files *Filesystem, overlays map[string]string) (map[string]string, error) {
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
		files = NewOSFilesystem(index.snapshot.Root())
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
		if filepath.Ext(path) != ".mcl" || !safepath.Within(index.snapshot.Root(), path) || !WithinAny(index.snapshot.SearchRoots(), path) {
			continue
		}
		if files.osBacked {
			moduleRoot, rootErr := DiscoverRoot(filepath.Dir(path))
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
			if IgnoredRenameIndexDirectory(directory) {
				return true
			}
		}
	}

	return false
}

func canonicalWatchedModulePath(path string, deleted bool, diskSources map[string]string, files *Filesystem) (string, error) {
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

func resolveModuleLink(path string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}

	return resolved, true
}

func (index *DependentMCLSourceIndex) applySnapshotChangesWithContext(ctx context.Context, diskSources, sources map[string]string, overlayPaths map[string]struct{}, changed map[string]struct{}, files *Filesystem) error {
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
	if len(sources) > MaxSourceFiles || totalBytes > MaxSourceTotal {
		return errors.New("workspace MCL source index exceeds its configured limits")
	}
	modules := make(map[string]syntax.ModuleAST, len(sources))
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
		module, err := parseWorkspaceModule(path, source)
		if err != nil {
			return err
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
		resolved, err := resolveModuleDependencies(path, module, files)
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
			if _, err := resolveModuleDependencies(importer, module, files); err != nil {
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
	snapshot, err := NewWorkspaceSnapshotFromParsed(generation, index.snapshot.Root(), index.snapshot.SearchRoots(), sources, modules, dependencies, importers)
	if err != nil {
		return err
	}
	index.snapshot = snapshot
	index.diskSources = maps.Clone(diskSources)
	index.overlayPaths = maps.Clone(overlayPaths)
	index.files = files

	return nil
}

func resolveModuleDependencies(path string, module syntax.ModuleAST, files *Filesystem) ([]string, error) {
	dependencies := make([]string, 0)
	for _, node := range module.Nodes {
		declaration, ok := node.(*syntax.ImportDeclaration)
		if !ok || (!declaration.Text && strings.HasPrefix(declaration.ModulePath, "std:")) {
			continue
		}
		resolved, err := files.ResolveModuleImport(path, declaration.ModulePath)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, declaration.Line, err)
		}
		dependencies = append(dependencies, resolved)
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
		module, err := parseWorkspaceModule(modulePath, source)
		if err != nil {
			return nil, err
		}
		dependencies, err := resolveModuleDependencies(modulePath, module, filesystem)
		if err != nil {
			return nil, err
		}
		for _, dependency := range dependencies {
			importers[dependency] = append(importers[dependency], modulePath)
		}
	}

	return dependentModuleSources(target, sources, importers)
}

func dependentProjectFSscope(projectRoot, targetPath string, files fs.FS) (*Filesystem, string, string, []string, error) {
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
	filesystem := NewFilesystem(root, files)
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
	root, err := DiscoverRoot(filepath.Dir(canonical))
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
	searchRoots, err := WorkspaceRoots(root, workspaceRoots)
	if err != nil {
		return "", "", nil, err
	}

	return canonical, root, searchRoots, nil
}

func collectDependentSourcesWithFilesystem(files *Filesystem, canonical, root string, searchRoots []string) (map[string]string, error) {
	return collectDependentSourcesWithFilesystemContext(context.Background(), files, canonical, root, searchRoots)
}

func collectDependentSourcesWithFilesystemContext(ctx context.Context, files *Filesystem, canonical, root string, searchRoots []string) (map[string]string, error) {
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
				if absolutePath != searchRoot && IgnoredRenameIndexDirectory(absolutePath) {
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
				moduleRoot, err := DiscoverRoot(filepath.Dir(modulePath))
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
			if len(allSources) >= MaxSourceFiles {
				return fmt.Errorf("workspace contains more than %d MCL files", MaxSourceFiles)
			}
			relativePath, err := files.relativePath(modulePath)
			if err != nil {
				return err
			}
			info, err := fs.Stat(files.files, relativePath)
			if err != nil {
				return fmt.Errorf("inspect module %q: %w", modulePath, err)
			}
			if info.Size() > MaxSourceBytes {
				return fmt.Errorf("module %q exceeds the %d-byte limit", modulePath, MaxSourceBytes)
			}
			if totalBytes+info.Size() > MaxSourceTotal {
				return fmt.Errorf("workspace MCL sources exceed the %d-byte limit", MaxSourceTotal)
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
		if len(allSources) >= MaxSourceFiles || totalBytes+int64(len(content)) > MaxSourceTotal {
			return nil, errors.New("workspace MCL source index exceeds its configured limits")
		}
		allSources[canonical] = string(content)
		totalBytes += int64(len(content))
	}

	return allSources, nil
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

package kustomizeadapter

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/content"
	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/manifest-configuration-language/mcl/internal/kustomizeconfig"
	"github.com/manifest-configuration-language/mcl/internal/safepath"
	"gopkg.in/yaml.v3"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

const maxRemoteReferences = 64

const maxRemoteDepth = 16

const (
	maxRemoteBytes = 64 << 20
)

type overlayFileSystem struct {
	filesys.FileSystem
	sourceFileSystem filesys.FileSystem
	sourceRoot       string
	overlayRoot      string
	fileCount        int
	totalBytes       int64
	materialized     map[string]bool
	sourceFiles      map[string][]byte
	remoteRoots      []remoteSourceRoot
}

type remoteSourceRoot struct {
	repository string
	revision   string
	path       string
}

func createOverlay(sourceRoot, buildDirectory string, sourceFileSystem filesys.FileSystem) (string, *overlayFileSystem, error) {
	if sourceFileSystem == nil {
		return "", nil, errors.New("create Kustomize overlay: source filesystem is required")
	}
	const overlayRoot = "/mcl-overlay"
	overlay := &overlayFileSystem{
		FileSystem: filesys.MakeFsInMemory(), sourceFileSystem: sourceFileSystem, sourceRoot: sourceRoot, overlayRoot: overlayRoot, materialized: make(map[string]bool),
		sourceFiles: make(map[string][]byte),
	}
	if err := overlay.MkdirAll(overlayRoot); err != nil {
		return "", nil, fmt.Errorf("create in-memory Kustomize overlay: %w", err)
	}
	if buildDirectory != "." {
		cleanDirectory := filepath.Clean(filepath.FromSlash(buildDirectory))
		if cleanDirectory == ".." || filepath.IsAbs(cleanDirectory) || strings.HasPrefix(cleanDirectory, ".."+string(filepath.Separator)) {
			return "", nil, fmt.Errorf("kustomize directory escapes the source: %q", buildDirectory)
		}
		sourceDirectory := filepath.Join(sourceRoot, cleanDirectory)
		if !sourceFileSystem.IsDir(sourceDirectory) {
			return "", nil, fmt.Errorf("kustomize directory is not a directory: %q", buildDirectory)
		}
		if err := overlay.MkdirAll(filepath.Join(overlayRoot, cleanDirectory)); err != nil {
			return "", nil, fmt.Errorf("prepare Kustomize overlay directory: %w", err)
		}
	}

	return overlayRoot, overlay, nil
}

func loadSourceFS(source fs.FS) (filesys.FileSystem, string, error) {
	const root = "/mcl-source"
	memoryFS := filesys.MakeFsInMemory()
	var totalBytes int64
	fileCount := 0
	err := fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("source contains unsupported symlink %q", name)
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if entry.IsDir() {
			return memoryFS.MkdirAll(target)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source contains unsupported file %q", name)
		}
		fileCount++
		if fileCount > maxSourceFiles || info.Size() > maxSourceBytes-totalBytes {
			return fmt.Errorf("source exceeds the file-count or %d-byte limit", maxSourceBytes)
		}
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		totalBytes += int64(len(data))

		return memoryFS.WriteFile(target, data)
	})
	if err != nil {
		return nil, "", err
	}

	return memoryFS, root, nil
}

func (fs *overlayFileSystem) ReadFile(path string) ([]byte, error) {
	relative, err := filepath.Rel(fs.overlayRoot, path)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("kustomize read escapes the overlay: %q", path)
	}
	if relative == "." {
		return nil, fmt.Errorf("kustomize read names a directory: %q", path)
	}
	if isManagedDirectory(relative) {
		return nil, fmt.Errorf("kustomize input is in a managed directory: %q", path)
	}
	target := filepath.Join(fs.overlayRoot, relative)
	if fs.materialized[relative] {
		return fs.FileSystem.ReadFile(target)
	}
	if fs.FileSystem.Exists(target) {
		return fs.FileSystem.ReadFile(target)
	}

	return fs.readSourceFile(filepath.Join(fs.sourceRoot, relative), relative)
}

func (fs *overlayFileSystem) Exists(path string) bool {
	if fs.FileSystem.Exists(path) {
		return true
	}
	source, ok := fs.sourcePath(path)
	if !ok {
		return false
	}

	return fs.sourceFileSystem.Exists(source)
}

func (fs *overlayFileSystem) IsDir(path string) bool {
	if fs.FileSystem.IsDir(path) {
		return true
	}
	source, ok := fs.sourcePath(path)
	if !ok {
		return false
	}
	relative, err := filepath.Rel(fs.sourceRoot, source)
	if err != nil || isManagedDirectory(relative) {
		return false
	}

	return fs.sourceFileSystem.IsDir(source)
}

func (fs *overlayFileSystem) ReadDir(path string) ([]string, error) {
	entries := make(map[string]bool)
	if names, err := fs.FileSystem.ReadDir(path); err == nil {
		for _, name := range names {
			entries[name] = true
		}
	}
	source, ok := fs.sourcePath(path)
	if !ok {
		return nil, fmt.Errorf("kustomize directory escapes the overlay: %q", path)
	}
	relative, err := filepath.Rel(fs.sourceRoot, source)
	if err != nil || isManagedDirectory(relative) {
		return nil, fmt.Errorf("kustomize directory is managed: %q", path)
	}
	if names, err := fs.sourceFileSystem.ReadDir(source); err == nil {
		for _, entry := range names {
			if !isManagedDirectory(entry) {
				entries[entry] = true
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)

	return names, nil
}

func (fs *overlayFileSystem) Glob(pattern string) ([]string, error) {
	if err := fs.materializeSourceTree(fs.overlayRoot); err != nil {
		return nil, err
	}

	return fs.FileSystem.Glob(pattern)
}

func (fs *overlayFileSystem) Open(path string) (filesys.File, error) {
	if _, err := fs.ReadFile(path); err != nil {
		return nil, err
	}

	return fs.FileSystem.Open(path)
}

func (fs *overlayFileSystem) sourcePath(overlayPath string) (string, bool) {
	relative, err := filepath.Rel(fs.overlayRoot, overlayPath)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}

	return filepath.Join(fs.sourceRoot, relative), true
}

func (fs *overlayFileSystem) CleanedAbs(overlayPath string) (filesys.ConfirmedDir, string, error) {
	if directory, name, err := fs.FileSystem.CleanedAbs(overlayPath); err == nil {
		return directory, name, nil
	}
	absPath, err := filepath.Abs(overlayPath)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(fs.overlayRoot, absPath)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("kustomize path escapes the overlay: %q", overlayPath)
	}
	source := filepath.Join(fs.sourceRoot, relative)
	if !fs.sourceFileSystem.Exists(source) {
		return "", "", os.ErrNotExist
	}
	if fs.sourceFileSystem.IsDir(source) {
		return filesys.ConfirmedDir(filepath.Join(fs.overlayRoot, relative)), "", nil
	}

	return filesys.ConfirmedDir(filepath.Join(fs.overlayRoot, filepath.Dir(relative))), filepath.Base(relative), nil
}

func (fs *overlayFileSystem) Walk(overlayPath string, walkFn filepath.WalkFunc) error {
	if err := fs.materializeSourceTree(overlayPath); err != nil {
		return err
	}

	return fs.FileSystem.Walk(overlayPath, walkFn)
}

func (fs *overlayFileSystem) materializeSourceTree(overlayPath string) error {
	sourcePath, ok := fs.sourcePath(overlayPath)
	if !ok {
		return fmt.Errorf("kustomize path escapes the overlay: %q", overlayPath)
	}
	relative, err := filepath.Rel(fs.sourceRoot, sourcePath)
	if err != nil || isManagedDirectory(relative) || !fs.sourceFileSystem.Exists(sourcePath) {
		return nil
	}
	if !fs.sourceFileSystem.IsDir(sourcePath) {
		if fs.FileSystem.Exists(overlayPath) {
			return nil
		}
		_, err := fs.readSourceFile(sourcePath, relative)
		return err
	}

	return fs.sourceFileSystem.Walk(sourcePath, func(sourcePath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(fs.sourceRoot, sourcePath)
		if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("kustomize walk escapes the source: %q", sourcePath)
		}
		if isManagedDirectory(relative) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(fs.overlayRoot, relative)
		if info.IsDir() {
			return fs.MkdirAll(target)
		}
		if fs.FileSystem.Exists(target) {
			return nil
		}

		_, err = fs.readSourceFile(sourcePath, relative)
		return err
	})
}

func (fs *overlayFileSystem) readSourceFile(path, relative string) ([]byte, error) {
	if isManagedDirectory(relative) {
		return nil, fmt.Errorf("kustomize input is in a managed directory: %q", path)
	}
	data, err := fs.sourceFileSystem.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !fs.materialized[relative] {
		if fs.fileCount >= maxSourceFiles || int64(len(data)) > maxSourceBytes-fs.totalBytes {
			return nil, fmt.Errorf("kustomize inputs exceed the file-count or %d-byte limit", maxSourceBytes)
		}
		fs.fileCount++
		fs.totalBytes += int64(len(data))
		fs.materialized[relative] = true
		fs.sourceFiles[filepath.ToSlash(relative)] = bytes.Clone(data)
		target := filepath.Join(fs.overlayRoot, relative)
		if err := fs.MkdirAll(filepath.Dir(target)); err != nil {
			return nil, err
		}
		if err := fs.WriteFile(target, data); err != nil {
			return nil, err
		}

	}

	return data, nil
}

func yamlSyntaxErrorLine(err error) (int, bool) {
	const prefix = "yaml: line "
	lineText, _, ok := strings.Cut(strings.TrimPrefix(err.Error(), prefix), ":")
	if !strings.HasPrefix(err.Error(), prefix) || !ok {
		return 0, false
	}
	line, parseErr := strconv.Atoi(lineText)

	return line, parseErr == nil && line > 0
}

func loadGitSourceFS(archive []byte) (filesys.FileSystem, string, error) {
	const root = "/mcl-source"
	memoryFS := filesys.MakeFsInMemory()
	if _, err := writeGitTreeToFS(archive, root, memoryFS, false); err != nil {
		return nil, "", err
	}

	return memoryFS, root, nil
}

func writeGitTreeToFS(archive []byte, root string, destination filesys.FileSystem, recordFiles bool) (map[string][]byte, error) {
	reader := tar.NewReader(bytes.NewReader(archive))
	files := make(map[string][]byte)
	seenFiles := make(map[string]bool)
	var totalBytes int64
	fileCount := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read cached Kustomize Git archive: %w", err)
		}
		name := path.Clean(header.Name)
		if header.Typeflag != tar.TypeReg || name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || header.Size < 0 {
			return nil, errors.New("cached Kustomize Git archive contains an invalid entry")
		}
		fileCount++
		if fileCount > maxSourceFiles || header.Size > maxSourceBytes-totalBytes {
			return nil, errors.New("cached Kustomize Git archive exceeds source limits")
		}
		totalBytes += header.Size
		if seenFiles[name] {
			return nil, fmt.Errorf("cached Kustomize Git archive repeats file %q", name)
		}
		seenFiles[name] = true
		data, exceeded, err := content.ReadAllLimited(reader, header.Size)
		if err != nil {
			return nil, fmt.Errorf("read cached Kustomize file %q: %w", name, err)
		}
		if exceeded || int64(len(data)) != header.Size {
			return nil, fmt.Errorf("read cached Kustomize file %q: archive entry length does not match its header", name)
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if !pathWithin(root, target) {
			return nil, errors.New("cached Kustomize Git archive escapes the in-memory source")
		}
		if err := destination.MkdirAll(filepath.Dir(target)); err != nil {
			return nil, fmt.Errorf("create cached Kustomize directory: %w", err)
		}
		if err := destination.WriteFile(target, data); err != nil {
			return nil, fmt.Errorf("write cached Kustomize file: %w", err)
		}
		if recordFiles {
			files[filepath.ToSlash(name)] = data
		}
	}
}

func pathWithin(root, target string) bool {
	return safepath.Within(root, target)
}

func isManagedDirectory(relative string) bool {
	relative = filepath.ToSlash(filepath.Clean(relative))

	return relative == ".git" || strings.HasPrefix(relative, ".git/") || relative == ".mcl/cache" || strings.HasPrefix(relative, ".mcl/cache/")
}

func resolveRemoteReferences(root string, dependencies *dependency.Lock, overlayFS *overlayFileSystem) error {
	if dependencies == nil {
		return errors.New("kustomize build requires dependency locking")
	}
	type visit struct {
		directory string
		depth     int
		ancestors map[string]bool
	}
	queue := []visit{{directory: root}}
	seenDirectories := make(map[string]bool)
	materialized := make(map[string]string)
	remoteCount := 0
	var remoteBytes int64
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if !pathWithin(overlayFS.overlayRoot, current.directory) || !overlayFS.IsDir(current.directory) {
			return fmt.Errorf("resolve Kustomize directory: %q is outside the overlay or is not a directory", current.directory)
		}
		canonical := filepath.Clean(current.directory)
		if seenDirectories[canonical] {
			continue
		}
		seenDirectories[canonical] = true
		for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
			configPath := filepath.Join(current.directory, name)
			data, err := overlayFS.ReadFile(configPath)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return fmt.Errorf("read Kustomize configuration %q: %w", configPath, err)
			}
			var document map[string]any
			if err := yaml.Unmarshal(data, &document); err != nil {
				line, _ := yamlSyntaxErrorLine(err)

				return ConfigSyntaxError{Path: configPath, SourceData: bytes.Clone(data), Line: line, Cause: err}
			}
			localDirectories := localDirectories(document, current.directory, root, overlayFS)
			rewriteRemote := func(reference string) (string, error) {
				parsed, err := parseLockedKustomizeReference(reference)
				if err != nil {
					return "", err
				}
				remoteCount++
				if remoteCount > maxRemoteReferences {
					return "", fmt.Errorf("kustomize source exceeds the %d-remote-reference limit", maxRemoteReferences)
				}
				if current.depth+1 > maxRemoteDepth {
					return "", fmt.Errorf("kustomize remote references exceed the %d-level depth limit", maxRemoteDepth)
				}
				key := dependency.GitSourceKey(parsed.source, parsed.revision, parsed.directory)
				if current.ancestors[key] {
					return "", fmt.Errorf("kustomize remote reference cycle detected for %q", reference)
				}
				materializationKey := current.directory + "\n" + key
				if existing, ok := materialized[materializationKey]; ok {
					return "./" + filepath.Base(existing), nil
				}
				archive, err := dependencies.LoadGitSource(parsed.source, parsed.revision, parsed.directory)
				if err != nil {
					return "", err
				}
				remoteBytes += int64(len(archive))
				if remoteBytes > maxRemoteBytes {
					return "", fmt.Errorf("kustomize remote sources exceed the %d-byte aggregate archive limit", maxRemoteBytes)
				}
				archiveDigest := content.SHA256(archive)
				localName := ".mcl-remote-" + archiveDigest
				targetRoot := filepath.Join(current.directory, localName)
				if overlayFS.FileSystem.Exists(targetRoot) {
					return "", fmt.Errorf("kustomize remote materialization path %q already exists", targetRoot)
				}
				remoteFiles, err := writeGitTreeToFS(archive, targetRoot, overlayFS.FileSystem, true)
				if err != nil {
					return "", fmt.Errorf("materialize Kustomize remote source: %w", err)
				}
				if !pathWithin(overlayFS.overlayRoot, targetRoot) {
					return "", errors.New("kustomize remote source escapes the source directory")
				}
				relativeRoot, err := filepath.Rel(overlayFS.overlayRoot, targetRoot)
				if err != nil {
					return "", errors.New("kustomize remote source escapes the source directory")
				}
				for name, data := range remoteFiles {
					overlayFS.sourceFiles[filepath.ToSlash(filepath.Join(relativeRoot, filepath.FromSlash(name)))] = data
				}
				overlayFS.remoteRoots = append(overlayFS.remoteRoots, remoteSourceRoot{
					repository: strings.TrimPrefix(parsed.source, "git+"),
					revision:   parsed.revision,
					path:       filepath.ToSlash(relativeRoot),
				})
				childAncestors := make(map[string]bool, len(current.ancestors)+1)
				for ancestor := range current.ancestors {
					childAncestors[ancestor] = true
				}
				childAncestors[key] = true
				materialized[materializationKey] = targetRoot
				queue = append(queue, visit{directory: targetRoot, depth: current.depth + 1, ancestors: childAncestors})

				return "./" + localName, nil
			}
			if err := kustomizeconfig.RewriteDocument(document, func(reference string) (string, error) {
				prefix, sourcePath, found := strings.Cut(reference, "=")
				if found && !kustomizeconfig.IsRemoteReference(reference) && kustomizeconfig.IsRemoteReference(sourcePath) {
					updated, err := rewriteRemote(sourcePath)
					if err != nil {
						return "", err
					}

					return prefix + "=" + updated, nil
				}
				if !kustomizeconfig.IsRemoteReference(reference) {
					return reference, nil
				}

				return rewriteRemote(reference)
			}); err != nil {
				return fmt.Errorf("resolve references in Kustomize configuration %q: %w", configPath, err)
			}
			for _, directory := range localDirectories {
				queue = append(queue, visit{directory: directory, depth: current.depth, ancestors: current.ancestors})
			}
			updated, err := yaml.Marshal(document)
			if err != nil {
				return fmt.Errorf("rewrite Kustomize configuration %q: %w", configPath, err)
			}
			if err := overlayFS.WriteFile(configPath, updated); err != nil {
				return fmt.Errorf("write Kustomize configuration %q: %w", configPath, err)
			}
		}
	}

	return nil
}

type lockedKustomizeReference struct {
	source    string
	revision  string
	directory string
}

func parseLockedKustomizeReference(reference string) (lockedKustomizeReference, error) {
	reference = strings.TrimSpace(reference)
	if !strings.HasPrefix(reference, "https://") && !strings.HasPrefix(reference, "git+https://") {
		return lockedKustomizeReference{}, fmt.Errorf("remote Kustomize reference %q must use HTTPS with a fixed commit", reference)
	}
	parsed, err := url.Parse(strings.TrimSpace(reference))
	if err != nil || parsed.Fragment != "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawPath != "" {
		return lockedKustomizeReference{}, fmt.Errorf("invalid fixed-commit Kustomize reference %q", reference)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) != 1 || len(query["ref"]) != 1 || !dependency.ValidGitRevision(query.Get("ref")) {
		return lockedKustomizeReference{}, fmt.Errorf("kustomize Git reference %q must specify exactly one 40-character lowercase SHA-1 ref query", reference)
	}
	repositoryPath, directory, found := strings.Cut(parsed.Path, "//")
	if !found {
		directory = "."
	}
	if directory == "" {
		return lockedKustomizeReference{}, fmt.Errorf("kustomize Git reference %q must name a repository directory", reference)
	}
	if err := dependency.ValidateGitDirectory(directory); err != nil {
		return lockedKustomizeReference{}, fmt.Errorf("invalid Kustomize Git reference %q: %w", reference, err)
	}
	parsed.Path = repositoryPath
	parsed.RawPath = ""
	parsed.Scheme = "https"
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	source := "git+" + parsed.String()
	if err := dependency.ValidateGitSource(source); err != nil {
		return lockedKustomizeReference{}, fmt.Errorf("invalid Kustomize Git reference %q: %w", reference, err)
	}

	return lockedKustomizeReference{source: source, revision: query.Get("ref"), directory: directory}, nil
}

func localDirectories(document map[string]any, currentDirectory, root string, overlayFS *overlayFileSystem) []string {
	directories := make(map[string]bool)
	var collect func(any)
	collect = func(value any) {
		switch value := value.(type) {
		case string:
			_, reference, found := strings.Cut(value, "=")
			if !found {
				reference = value
			}
			if reference == "" || strings.Contains(reference, "://") || filepath.IsAbs(reference) || strings.ContainsAny(reference, "*?[") {
				return
			}
			target := filepath.Clean(filepath.Join(currentDirectory, filepath.FromSlash(reference)))
			if pathWithin(root, target) && overlayFS.IsDir(target) {
				directories[target] = true
			}
		case []any:
			for _, item := range value {
				collect(item)
			}
		}
	}
	for _, field := range kustomizeconfig.RootReferenceFields() {
		collect(document[field])
	}
	result := make([]string, 0, len(directories))
	for directory := range directories {
		result = append(result, directory)
	}
	slices.Sort(result)

	return result
}

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
	maxSourceFiles = 10000
)

type overlayFileSystem struct {
	filesys.FileSystem
	sourceRoot   string
	overlayRoot  string
	fileCount    int
	totalBytes   int64
	materialized map[string]bool
	sourceFiles  map[string][]byte
	remoteRoots  []remoteSourceRoot
}

type remoteSourceRoot struct {
	repository string
	revision   string
	path       string
}

func createOverlay(sourceRoot, buildDirectory string) (string, *overlayFileSystem, error) {
	temporaryRoot, err := os.MkdirTemp("", "mcl-kustomize-overlay-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temporary Kustomize overlay: %w", err)
	}
	overlayRoot, err := filepath.EvalSymlinks(temporaryRoot)
	if err != nil {
		_ = os.RemoveAll(temporaryRoot)

		return "", nil, fmt.Errorf("resolve temporary Kustomize overlay: %w", err)
	}
	overlay := &overlayFileSystem{
		FileSystem: filesys.MakeFsOnDisk(), sourceRoot: sourceRoot, overlayRoot: overlayRoot, materialized: make(map[string]bool),
		sourceFiles: make(map[string][]byte),
	}
	if buildDirectory != "." {
		cleanDirectory := filepath.Clean(filepath.FromSlash(buildDirectory))
		if cleanDirectory == ".." || filepath.IsAbs(cleanDirectory) || strings.HasPrefix(cleanDirectory, ".."+string(filepath.Separator)) {
			_ = os.RemoveAll(overlayRoot)

			return "", nil, fmt.Errorf("kustomize directory escapes the source: %q", buildDirectory)
		}
		sourceDirectory := filepath.Join(sourceRoot, cleanDirectory)
		if err := rejectKustomizeSourceSymlinks(sourceRoot, sourceDirectory); err != nil {
			_ = os.RemoveAll(overlayRoot)

			return "", nil, fmt.Errorf("resolve Kustomize directory: %w", err)
		}
		info, err := os.Stat(sourceDirectory)
		if err != nil {
			_ = os.RemoveAll(overlayRoot)

			return "", nil, fmt.Errorf("resolve Kustomize directory: %w", err)
		}
		if !info.IsDir() {
			_ = os.RemoveAll(overlayRoot)

			return "", nil, fmt.Errorf("kustomize directory is not a directory: %q", buildDirectory)
		}
		if err := os.MkdirAll(filepath.Join(overlayRoot, cleanDirectory), 0o755); err != nil {
			_ = os.RemoveAll(overlayRoot)

			return "", nil, fmt.Errorf("prepare Kustomize overlay directory: %w", err)
		}
	}
	if err := rejectKustomizeSourceSymlinks(sourceRoot, sourceRoot); err != nil {
		_ = os.RemoveAll(overlayRoot)

		return "", nil, fmt.Errorf("resolve Kustomize source: %w", err)
	}

	return overlayRoot, overlay, nil
}

func materializeSourceFS(source fs.FS) (string, error) {
	root, err := os.MkdirTemp("", "mcl-kustomize-source-*")
	if err != nil {
		return "", err
	}
	var totalBytes int64
	fileCount := 0
	err = fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, walkErr error) error {
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
			return os.MkdirAll(target, 0o700)
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
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}

		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		_ = os.RemoveAll(root)

		return "", err
	}

	return root, nil
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
		return os.ReadFile(target)
	}
	if _, err := os.Lstat(target); err == nil {
		return os.ReadFile(target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
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
	_, err := os.Lstat(source)

	return err == nil
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
	info, err := os.Stat(source)

	return err == nil && info.IsDir()
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
	if err := rejectKustomizeSourceSymlinks(fs.sourceRoot, source); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if names, err := os.ReadDir(source); err == nil {
		for _, entry := range names {
			if !isManagedDirectory(entry.Name()) {
				entries[entry.Name()] = true
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
	return fs.FileSystem.Glob(pattern)
}

func (fs *overlayFileSystem) Open(path string) (filesys.File, error) {
	if _, err := fs.ReadFile(path); err != nil {
		return nil, err
	}

	return os.Open(path)
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
	if err := rejectKustomizeSourceSymlinks(fs.sourceRoot, source); err != nil {
		return "", "", err
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		return filesys.ConfirmedDir(filepath.Join(fs.overlayRoot, relative)), "", nil
	}

	return filesys.ConfirmedDir(filepath.Join(fs.overlayRoot, filepath.Dir(relative))), filepath.Base(relative), nil
}

func (fs *overlayFileSystem) Walk(overlayPath string, walkFn filepath.WalkFunc) error {
	return fs.FileSystem.Walk(overlayPath, walkFn)
}

func (fs *overlayFileSystem) readSourceFile(path, relative string) ([]byte, error) {
	resolved, err := resolveKustomizeSourcePath(fs.sourceRoot, path)
	if err != nil {
		return nil, fmt.Errorf("resolve Kustomize input %q: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("kustomize input is not a regular file: %q", path)
	}
	if !fs.materialized[relative] {
		if fs.fileCount >= maxSourceFiles || fs.totalBytes+info.Size() > maxSourceBytes {
			return nil, fmt.Errorf("kustomize inputs exceed the file-count or %d-byte limit", maxSourceBytes)
		}
	}
	data, err := readLimitedFile(resolved, maxSourceBytes)
	if err != nil {
		return nil, err
	}
	if !fs.materialized[relative] {
		fs.fileCount++
		fs.totalBytes += int64(len(data))
		fs.materialized[relative] = true
		sourceRelative, _ := filepath.Rel(fs.sourceRoot, resolved)
		fs.sourceFiles[filepath.ToSlash(sourceRelative)] = bytes.Clone(data)
		target := filepath.Join(fs.overlayRoot, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return nil, err
		}
	}

	return data, nil
}

func rejectKustomizeSourceSymlinks(root, target string) error {
	_, err := resolveKustomizeSourcePath(root, target)

	return err
}

func resolveKustomizeSourcePath(root, target string) (string, error) {
	if !safepath.Within(root, target) {
		return "", fmt.Errorf("kustomize input escapes the source directory: %q", target)
	}
	resolved, err := safepath.ResolveWithin(root, target)
	if err != nil {
		return "", fmt.Errorf("resolve Kustomize input within source directory: %w", err)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("kustomize input escapes the source directory: %q", target)
	}
	current := root
	for component := range strings.SplitSeq(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("kustomize input contains unsupported symlink %q", current)
		}
	}

	return resolved, nil
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

func readLimitedFile(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("file %q exceeds the %d-byte limit", path, maximum)
	}

	return data, nil
}

func materializeGitTree(archive []byte) (string, error) {
	root, err := os.MkdirTemp("", "mcl-kustomize-git-*")
	if err != nil {
		return "", fmt.Errorf("create temporary Kustomize tree: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(root)
		}
	}()
	reader := tar.NewReader(bytes.NewReader(archive))
	var totalBytes int64
	fileCount := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read cached Kustomize Git archive: %w", err)
		}
		name := path.Clean(header.Name)
		if header.Typeflag != tar.TypeReg || name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || header.Size < 0 {
			return "", errors.New("cached Kustomize Git archive contains an invalid entry")
		}
		fileCount++
		totalBytes += header.Size
		if fileCount > maxSourceFiles || totalBytes > maxSourceBytes {
			return "", errors.New("cached Kustomize Git archive exceeds source limits")
		}
		filePath := filepath.Join(root, filepath.FromSlash(name))
		if !pathWithin(root, filePath) {
			return "", errors.New("cached Kustomize Git archive escapes its temporary directory")
		}
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			return "", fmt.Errorf("create cached Kustomize directory: %w", err)
		}
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode&0o755))
		if err != nil {
			return "", fmt.Errorf("write cached Kustomize file: %w", err)
		}
		_, copyErr := io.CopyN(file, reader, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return "", fmt.Errorf("write cached Kustomize file: %w", copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close cached Kustomize file: %w", closeErr)
		}
	}
	failed = false

	return root, nil
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
		canonical, err := safepath.ResolveWithin(overlayFS.overlayRoot, current.directory)
		if err != nil {
			return fmt.Errorf("resolve Kustomize directory: %w", err)
		}
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
				remoteRoot, err := materializeGitTree(archive)
				if err != nil {
					return "", err
				}
				keepRemoteRoot := false
				defer func() {
					if !keepRemoteRoot {
						_ = os.RemoveAll(remoteRoot)
					}
				}()
				archiveDigest := content.SHA256(archive)
				localName := ".mcl-remote-" + archiveDigest
				targetRoot := filepath.Join(current.directory, localName)
				if _, err := os.Lstat(targetRoot); err == nil {
					return "", fmt.Errorf("kustomize remote materialization path %q already exists", targetRoot)
				} else if !errors.Is(err, os.ErrNotExist) {
					return "", fmt.Errorf("inspect Kustomize remote materialization path: %w", err)
				}
				if err := os.Rename(remoteRoot, targetRoot); err != nil {
					return "", fmt.Errorf("materialize Kustomize remote source: %w", err)
				}
				keepRemoteRoot = true
				if !safepath.Within(overlayFS.sourceRoot, targetRoot) {
					return "", errors.New("kustomize remote source escapes the source directory")
				}
				relativeRoot, err := filepath.Rel(overlayFS.sourceRoot, targetRoot)
				if err != nil {
					return "", errors.New("kustomize remote source escapes the source directory")
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
			if err := os.WriteFile(configPath, updated, 0o644); err != nil { //nolint:gosec // 元ファイルの権限を引き継ぐ意図で0644
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

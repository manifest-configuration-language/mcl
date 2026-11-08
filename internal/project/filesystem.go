package project

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/manifest-configuration-language/mcl/internal/safepath"
)

const (
	MaxSourceBytes = 16 << 20
	MaxSourceFiles = 4096
	MaxSourceTotal = 64 << 20
	ConfigFileName = "mcl.toml"
)

// Filesystem exposes project files through root-relative io/fs paths.
// OS-backed filesystems resolve symlinks before opening so paths cannot escape the project root.
type Filesystem struct {
	root     string
	files    fs.FS
	osBacked bool
	reads    *readTrackingFS
}

type readTrackingFS struct {
	fs.FS
	mu   sync.Mutex
	seen map[string]struct{}
}

func (filesystem *readTrackingFS) Open(name string) (fs.File, error) {
	file, err := filesystem.FS.Open(name)
	if err != nil {
		return nil, err
	}

	filesystem.record(name)

	return file, nil
}

func (filesystem *readTrackingFS) record(name string) {
	clean := path.Clean(name)
	if clean != "." && fs.ValidPath(clean) {
		filesystem.mu.Lock()
		filesystem.seen[clean] = struct{}{}
		filesystem.mu.Unlock()
	}
}

func (filesystem *readTrackingFS) paths() []string {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()

	paths := make([]string, 0, len(filesystem.seen))
	for name := range filesystem.seen {
		paths = append(paths, name)
	}
	sort.Strings(paths)

	return paths
}

type rootConfinedFS struct {
	files fs.FS
}

func (filesystem rootConfinedFS) Open(name string) (fs.File, error) {
	links, ok := filesystem.files.(fs.ReadLinkFS)
	if ok {
		current := ""
		for component := range strings.SplitSeq(name, "/") {
			current = path.Join(current, component)
			info, err := links.Lstat(current)
			if err != nil {
				return nil, err
			}
			if info.Mode()&fs.ModeSymlink != 0 {
				return nil, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("symbolic link %q is not allowed: %w", current, fs.ErrPermission)}
			}
		}
	}

	return filesystem.files.Open(name)
}

// NewOSFilesystem creates a project filesystem rooted at an OS directory.
func NewOSFilesystem(root string) *Filesystem {
	return newFilesystem(root, os.DirFS(root), true)
}

// NewFilesystem confines a root-relative filesystem to its logical project root.
func NewFilesystem(root string, files fs.FS) *Filesystem {
	return newFilesystem(root, rootConfinedFS{files: files}, false)
}

func newFilesystem(root string, files fs.FS, osBacked bool) *Filesystem {
	reads := &readTrackingFS{FS: files, seen: make(map[string]struct{})}

	return &Filesystem{root: filepath.Clean(root), files: reads, osBacked: osBacked, reads: reads}
}

// Root returns the filesystem's absolute logical root.
func (filesystem *Filesystem) Root() string { return filesystem.root }

// OSBacked reports whether the filesystem resolves paths against the host OS.
func (filesystem *Filesystem) OSBacked() bool { return filesystem.osBacked }

// FS returns the root-relative filesystem used to read project files.
func (filesystem *Filesystem) FS() fs.FS { return filesystem.files }

// ReadPaths returns root-relative paths opened through this filesystem in lexical order.
func (filesystem *Filesystem) ReadPaths() []string { return filesystem.reads.paths() }

// RecordRead adds a root-relative path to the read set.
func (filesystem *Filesystem) RecordRead(name string) { filesystem.reads.record(name) }

func (filesystem *Filesystem) relativePath(filename string) (string, error) {
	return filesystem.RelativePath(filename)
}

func (filesystem *Filesystem) resolveReadPath(filename string) (string, error) {
	return filesystem.ResolveReadPath(filename)
}

func (filesystem *Filesystem) readLimitedFile(filename string) ([]byte, error) {
	return filesystem.ReadLimitedFile(filename, MaxSourceBytes)
}

// RelativePath converts an absolute filename to a validated root-relative slash path.
func (filesystem *Filesystem) RelativePath(filename string) (string, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(filesystem.root, absolute)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside project root %q", filename, filesystem.root)
	}
	relative = filepath.ToSlash(relative)
	if !fs.ValidPath(relative) {
		return "", fmt.Errorf("invalid project path %q", relative)
	}

	return relative, nil
}

// ResolveReadPath validates and resolves an absolute project filename.
func (filesystem *Filesystem) ResolveReadPath(filename string) (string, error) {
	relative, err := filesystem.RelativePath(filename)
	if err != nil {
		return "", err
	}
	if filesystem.osBacked {
		return safepath.ResolveWithin(filesystem.root, filepath.Join(filesystem.root, filepath.FromSlash(relative)))
	}
	if _, err := fs.Stat(filesystem.files, relative); err != nil {
		return "", err
	}

	return filepath.Join(filesystem.root, filepath.FromSlash(relative)), nil
}

// ResolveModuleImport resolves a module path relative to importer while enforcing the project root.
func (filesystem *Filesystem) ResolveModuleImport(importer, modulePath string) (string, error) {
	if filepath.IsAbs(modulePath) || path.IsAbs(modulePath) {
		return "", errors.New("absolute module paths are not allowed")
	}
	importerPath := importer
	if filesystem.osBacked {
		absoluteImporter, err := filepath.Abs(importer)
		if err != nil {
			return "", err
		}
		canonicalImporter, err := filepath.EvalSymlinks(absoluteImporter)
		if err != nil {
			canonicalDirectory, directoryErr := filepath.EvalSymlinks(filepath.Dir(absoluteImporter))
			if directoryErr != nil {
				return "", fmt.Errorf("resolve importer directory: %w", directoryErr)
			}
			canonicalImporter = filepath.Join(canonicalDirectory, filepath.Base(absoluteImporter))
		}
		importerPath = canonicalImporter
	}
	importerRelative, err := filesystem.RelativePath(importerPath)
	if err != nil {
		return "", fmt.Errorf("resolve importer: %w", err)
	}
	joined := path.Clean(path.Join(path.Dir(importerRelative), filepath.ToSlash(filepath.FromSlash(modulePath))))
	if !fs.ValidPath(joined) {
		return "", fmt.Errorf("module path %q escapes the project directory", modulePath)
	}
	candidate := filepath.Join(filesystem.root, filepath.FromSlash(joined))
	if filesystem.osBacked {
		canonical, err := safepath.ResolveWithin(filesystem.root, candidate)
		if err != nil {
			if errors.Is(err, safepath.ErrOutsideRoot) {
				return "", fmt.Errorf("module path %q escapes the project directory", modulePath)
			}

			return "", fmt.Errorf("resolve module %q: %w", modulePath, err)
		}

		return canonical, nil
	}
	if _, err := fs.Stat(filesystem.files, joined); err != nil {
		return "", fmt.Errorf("resolve module %q: %w", modulePath, err)
	}

	return candidate, nil
}

// ReadLimitedFile reads an absolute project file up to maximum bytes.
func (filesystem *Filesystem) ReadLimitedFile(filename string, maximum int64) ([]byte, error) {
	resolved, err := filesystem.ResolveReadPath(filename)
	if err != nil {
		return nil, err
	}
	relative, err := filesystem.RelativePath(resolved)
	if err != nil {
		return nil, err
	}
	file, err := filesystem.files.Open(relative)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("file %q exceeds the %d-byte limit", filename, maximum)
	}

	return data, nil
}

func validProjectPath(name string) bool {
	return fs.ValidPath(name) && name != "." && filepath.IsLocal(filepath.FromSlash(name)) && !strings.Contains(name, `\`)
}

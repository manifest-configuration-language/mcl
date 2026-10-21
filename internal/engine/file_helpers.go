package engine

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/safepath"
)

const maxYAMLBytes = 16 << 20

const (
	maxYAMLNodes = 300_000
	maxYAMLDepth = 128
)

const (
	kustomizeSourceLocationLabel            = "Original Kustomize resource"
	kustomizeTransformedSourceLocationLabel = "Kustomize input transformed into this value"
	helmRenderedTemplateSourceLabel         = "Rendered from Helm template"
)

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)

	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// projectFilesystem presents project files through root-relative io/fs paths.
// osBacked filesystems resolve symlinks before opening so an OS path cannot
// escape the project root through a link.
type projectFilesystem struct {
	root     string
	files    fs.FS
	osBacked bool
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

func newOSProjectFilesystem(root string) *projectFilesystem {
	return &projectFilesystem{root: filepath.Clean(root), files: os.DirFS(root), osBacked: true}
}

func newProjectFilesystem(root string, files fs.FS) *projectFilesystem {
	return &projectFilesystem{root: filepath.Clean(root), files: rootConfinedFS{files: files}}
}

func projectFilesystemFor(root string, filesystems []*projectFilesystem) *projectFilesystem {
	if len(filesystems) > 0 && filesystems[0] != nil {
		return filesystems[0]
	}

	return newOSProjectFilesystem(root)
}

func (filesystem *projectFilesystem) relativePath(filename string) (string, error) {
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

func (filesystem *projectFilesystem) resolveReadPath(filename string) (string, error) {
	relative, err := filesystem.relativePath(filename)
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

func (filesystem *projectFilesystem) resolveModuleImport(importer, modulePath string) (string, error) {
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
	importerRelative, err := filesystem.relativePath(importerPath)
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

func (filesystem *projectFilesystem) readLimitedFile(filename string) ([]byte, error) {
	return filesystem.readLimitedFileWithLimit(filename, maxMCLSourceBytes)
}

func (filesystem *projectFilesystem) readLimitedFileWithLimit(filename string, maximumSize int64) ([]byte, error) {
	resolved, err := filesystem.resolveReadPath(filename)
	if err != nil {
		return nil, err
	}
	relative, err := filesystem.relativePath(resolved)
	if err != nil {
		return nil, err
	}
	file, err := filesystem.files.Open(relative)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maximumSize+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if int64(len(data)) > maximumSize {
		return nil, fmt.Errorf("file %q exceeds the %d-byte limit", filename, maximumSize)
	}

	return data, nil
}

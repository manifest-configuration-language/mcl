package engine

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
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

func readLimitedFile(path string) ([]byte, error) {
	return readLimitedFileWithLimit(path, maxYAMLBytes)
}

func readLimitedFileWithLimit(path string, maximumSize int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximumSize {
		return nil, fmt.Errorf("file %q exceeds the %d-byte limit", path, maximumSize)
	}

	return data, nil
}

// projectFilesystem presents project files through root-relative io/fs paths.
// osBacked filesystems resolve symlinks before opening so an OS path cannot
// escape the project root through a link.
type projectFilesystem struct {
	root     string
	files    fs.FS
	osBacked bool
}

func newOSProjectFilesystem(root string) *projectFilesystem {
	return &projectFilesystem{root: filepath.Clean(root), files: os.DirFS(root), osBacked: true}
}

func newProjectFilesystem(root string, files fs.FS) *projectFilesystem {
	return &projectFilesystem{root: filepath.Clean(root), files: files}
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

func (filesystem *projectFilesystem) readLimitedFile(filename string) ([]byte, error) {
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
	data, readErr := io.ReadAll(io.LimitReader(file, maxMCLSourceBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if int64(len(data)) > maxMCLSourceBytes {
		return nil, fmt.Errorf("file %q exceeds the %d-byte limit", filename, maxMCLSourceBytes)
	}

	return data, nil
}

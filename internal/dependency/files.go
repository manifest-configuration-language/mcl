package dependency

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	contentutil "github.com/manifest-configuration-language/mcl/internal/content"
	"github.com/manifest-configuration-language/mcl/internal/safepath"
)

type rootFilesystem struct {
	root     string
	files    fs.FS
	writer   AtomicFileWriter
	osBacked bool
}

// AtomicFileWriter creates missing parent directories and writes a root-relative
// file atomically, replacing any existing file only after the complete new
// content is available. Implementers must reject paths that are not valid
// io/fs paths and must keep writes within the filesystem root.
type AtomicFileWriter interface {
	WriteFileAtomic(name string, data []byte) error
}

func newOSRootFilesystem(root string) *rootFilesystem {
	return &rootFilesystem{root: filepath.Clean(root), files: os.DirFS(root), osBacked: true}
}

func newRootFilesystem(root string, files fs.FS) *rootFilesystem {
	filesystem := &rootFilesystem{root: filepath.Clean(root), files: files}
	filesystem.writer, _ = files.(AtomicFileWriter)

	return filesystem
}

func (filesystem *rootFilesystem) writeFileAtomic(name string, data []byte) error {
	if !fs.ValidPath(name) || name == "." {
		return fs.ErrInvalid
	}
	if filesystem.osBacked {
		path := filepath.Join(filesystem.root, filepath.FromSlash(name))
		if _, err := safepath.ResolveWithin(filesystem.root, path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := ensureDirectoryWithin(filesystem.root, filepath.Dir(path)); err != nil {
			return err
		}

		return contentutil.WriteFileAtomic(path, data)
	}
	if filesystem.writer == nil {
		return errors.New("dependency filesystem does not support atomic writes")
	}

	return filesystem.writer.WriteFileAtomic(name, data)
}

func (filesystem *rootFilesystem) stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, fs.ErrInvalid
	}
	if filesystem.osBacked {
		return os.Lstat(filepath.Join(filesystem.root, filepath.FromSlash(name)))
	}

	return fs.Stat(filesystem.files, name)
}

func (filesystem *rootFilesystem) readLimitedFile(name string, maximumSize int64) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, fs.ErrInvalid
	}
	if filesystem.osBacked {
		path := filepath.Join(filesystem.root, filepath.FromSlash(name))
		resolved, err := safepath.ResolveWithin(filesystem.root, path)
		if err != nil {
			return nil, err
		}
		root, err := filepath.EvalSymlinks(filesystem.root)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(root, resolved)
		if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, safepath.ErrOutsideRoot
		}
		name = filepath.ToSlash(relative)
	}
	file, err := filesystem.files.Open(name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maximumSize+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if int64(len(data)) > maximumSize {
		return nil, fmt.Errorf("file %q exceeds the %d-byte limit", name, maximumSize)
	}

	return data, nil
}

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing/fstest"

	"github.com/manifest-configuration-language/mcl/internal/wasmprotocol"
)

type projectFilesystem struct {
	mu          sync.RWMutex
	files       fstest.MapFS
	sourceBytes int
}

func (filesystem *projectFilesystem) Open(name string) (fs.File, error) {
	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()

	return filesystem.files.Open(name)
}

func (filesystem *projectFilesystem) WriteFileAtomic(name string, data []byte) error {
	if !fs.ValidPath(name) || name == "." || strings.Contains(name, `\`) {
		return fs.ErrInvalid
	}
	if len(data) > wasmprotocol.MaxProjectFileBytes {
		return fmt.Errorf("project file %q exceeds the %d-byte limit", name, wasmprotocol.MaxProjectFileBytes)
	}

	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()

	_, exists := filesystem.files[name]
	if !exists && len(filesystem.files)+1 >= wasmprotocol.MaxProjectFiles {
		return fmt.Errorf("project has more than %d files", wasmprotocol.MaxProjectFiles)
	}
	totalBytes := filesystem.sourceBytes + len(data)
	for path, file := range filesystem.files {
		if path == name {
			continue
		}
		totalBytes += len(file.Data)
	}
	if totalBytes > wasmprotocol.MaxProjectBytes {
		return fmt.Errorf("project files exceed the %d-byte limit", wasmprotocol.MaxProjectBytes)
	}
	filesystem.files[name] = &fstest.MapFile{Data: bytes.Clone(data), Mode: 0o444}

	return nil
}

func newProjectFiles(source string, files map[string]wasmprotocol.FileInput, binaryFiles map[string][]byte, entrypoint string) (*projectFilesystem, error) {
	if len(files)+1 > wasmprotocol.MaxProjectFiles {
		return nil, fmt.Errorf("project has more than %d files", wasmprotocol.MaxProjectFiles)
	}
	if len(source) > wasmprotocol.MaxSourceBytes {
		return nil, fmt.Errorf("source exceeds the %d-byte limit", wasmprotocol.MaxSourceBytes)
	}
	totalBytes := len(source)
	project := &projectFilesystem{files: make(fstest.MapFS, len(files)), sourceBytes: len(source)}
	binaryCount := 0
	for relativePath, input := range files {
		if !filepath.IsLocal(relativePath) || path.Clean(relativePath) != relativePath || relativePath == "." || relativePath == entrypoint || strings.Contains(relativePath, `\`) {
			return nil, fmt.Errorf("invalid project file path %q", relativePath)
		}
		var content []byte
		switch input.Encoding {
		case wasmprotocol.FileInputEncodingUtf8:
			if input.Content == nil {
				return nil, fmt.Errorf("project file %q is missing JSON content", relativePath)
			}
			content = []byte(*input.Content)
		case wasmprotocol.FileInputEncodingBinary:
			if input.Content != nil {
				return nil, fmt.Errorf("binary project file %q cannot include JSON content", relativePath)
			}
			var ok bool
			content, ok = binaryFiles[relativePath]
			if !ok {
				return nil, fmt.Errorf("binary project file %q is missing from the binary file map", relativePath)
			}
			binaryCount++
		default:
			return nil, fmt.Errorf("project file %q encoding must be utf8 or binary", relativePath)
		}
		if len(content) > wasmprotocol.MaxProjectFileBytes {
			return nil, fmt.Errorf("project file %q exceeds the %d-byte limit", relativePath, wasmprotocol.MaxProjectFileBytes)
		}
		totalBytes += len(content)
		if totalBytes > wasmprotocol.MaxProjectBytes {
			return nil, fmt.Errorf("project files exceed the %d-byte limit", wasmprotocol.MaxProjectBytes)
		}
		project.files[relativePath] = &fstest.MapFile{Data: content, Mode: 0o444}
	}
	if binaryCount != len(binaryFiles) {
		return nil, errors.New("binary file map contains files without binary metadata")
	}

	return project, nil
}

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"strings"
	"sync"
	"testing/fstest"
	"unicode/utf8"

	"github.com/manifest-configuration-language/mcl/internal/wasmprotocol"
)

type projectFilesystem struct {
	mu          sync.RWMutex
	files       fstest.MapFS
	sourceBytes int
	totalBytes  int
	entrypoints int
}

//nolint:unused // Used by the js/wasm entry point, which golangci-lint does not analyze by default.
type projectFilesystemView struct {
	filesystem *projectFilesystem
	excluded   string
}

//nolint:unused // Implements fs.FS for browser projects while excluding the active entrypoint.
func (view projectFilesystemView) Open(name string) (fs.File, error) {
	if name == view.excluded {
		return nil, fs.ErrNotExist
	}

	return view.filesystem.Open(name)
}

func (filesystem *projectFilesystem) Open(name string) (fs.File, error) {
	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()

	return filesystem.files.Open(name)
}

func (filesystem *projectFilesystem) WriteFileAtomic(name string, data []byte) error {
	if !validProjectPath(name) {
		return fs.ErrInvalid
	}
	if len(data) > wasmprotocol.MaxProjectFileBytes {
		return fmt.Errorf("project file %q exceeds the %d-byte limit", name, wasmprotocol.MaxProjectFileBytes)
	}

	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()

	existing, exists := filesystem.files[name]
	if !exists && len(filesystem.files)+filesystem.entrypoints >= wasmprotocol.MaxProjectFiles {
		return fmt.Errorf("project has more than %d files", wasmprotocol.MaxProjectFiles)
	}
	totalBytes := filesystem.totalBytes
	if exists {
		totalBytes -= len(existing.Data)
	}
	totalBytes += len(data)
	if totalBytes > wasmprotocol.MaxProjectBytes {
		return fmt.Errorf("project files exceed the %d-byte limit", wasmprotocol.MaxProjectBytes)
	}
	filesystem.files[name] = &fstest.MapFile{Data: bytes.Clone(data), Mode: 0o444}
	filesystem.totalBytes = totalBytes

	return nil
}

//nolint:unused // Called by the js/wasm entry point, which golangci-lint does not analyze by default.
func (filesystem *projectFilesystem) applyDelta(upserts map[string]wasmprotocol.FileInput, binaryFiles map[string][]byte, deletes []string, reset bool) error {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()

	files := filesystem.files
	totalBytes := filesystem.totalBytes
	if reset {
		files = make(fstest.MapFS, len(upserts))
		totalBytes = 0
	} else {
		copyOfFiles := make(fstest.MapFS, len(files)+len(upserts))
		maps.Copy(copyOfFiles, files)
		files = copyOfFiles
	}

	for _, filePath := range deletes {
		if !validProjectPath(filePath) {
			return fmt.Errorf("invalid project file path %q", filePath)
		}
		if file, exists := files[filePath]; exists {
			totalBytes -= len(file.Data)
			delete(files, filePath)
		}
	}

	binaryCount := 0
	for filePath, input := range upserts {
		if !validProjectPath(filePath) {
			return fmt.Errorf("invalid project file path %q", filePath)
		}
		var content []byte
		switch input.Encoding {
		case wasmprotocol.FileInputEncodingUtf8:
			if input.Content == nil {
				return fmt.Errorf("project file %q is missing JSON content", filePath)
			}
			content = []byte(*input.Content)
		case wasmprotocol.FileInputEncodingBinary:
			if input.Content != nil {
				return fmt.Errorf("binary project file %q cannot include JSON content", filePath)
			}
			var ok bool
			content, ok = binaryFiles[filePath]
			if !ok {
				return fmt.Errorf("binary project file %q is missing from the binary file map", filePath)
			}
			binaryCount++
		default:
			return fmt.Errorf("project file %q encoding must be utf8 or binary", filePath)
		}
		if len(content) > wasmprotocol.MaxProjectFileBytes {
			return fmt.Errorf("project file %q exceeds the %d-byte limit", filePath, wasmprotocol.MaxProjectFileBytes)
		}
		if previous, exists := files[filePath]; exists {
			totalBytes -= len(previous.Data)
		}
		totalBytes += len(content)
		if totalBytes > wasmprotocol.MaxProjectBytes {
			return fmt.Errorf("project files exceed the %d-byte limit", wasmprotocol.MaxProjectBytes)
		}
		files[filePath] = &fstest.MapFile{Data: bytes.Clone(content), Mode: 0o444}
	}
	if binaryCount != len(binaryFiles) {
		return errors.New("binary file map contains files without binary metadata")
	}
	if len(files) > wasmprotocol.MaxProjectFiles {
		return fmt.Errorf("project has more than %d files", wasmprotocol.MaxProjectFiles)
	}
	filesystem.files = files
	filesystem.totalBytes = totalBytes
	filesystem.sourceBytes = 0
	filesystem.entrypoints = 0

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
	project := &projectFilesystem{files: make(fstest.MapFS, len(files)), sourceBytes: len(source), totalBytes: len(source), entrypoints: 1}
	binaryCount := 0
	for relativePath, input := range files {
		if !validProjectPath(relativePath) || relativePath == entrypoint {
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

	project.totalBytes = totalBytes

	return project, nil
}

func validProjectPath(value string) bool {
	if !fs.ValidPath(value) || value == "." || utf8.RuneCountInString(value) > wasmprotocol.MaxProjectPathLength || strings.Contains(value, `\`) || strings.ContainsRune(value, '\x00') {
		return false
	}
	if len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' {
		return false
	}

	return len(strings.Split(value, "/")) <= wasmprotocol.MaxProjectPathDepth
}

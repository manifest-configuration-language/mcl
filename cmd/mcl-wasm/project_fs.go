package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"testing/fstest"

	"github.com/manifest-configuration-language/mcl/internal/wasmprotocol"
)

const (
	maxSourceBytes      = 64 << 10
	maxProjectFiles     = 4096
	maxProjectFileBytes = 16 << 20
	maxProjectBytes     = 64 << 20
)

func newProjectFiles(source string, files map[string]wasmprotocol.FileInput, binaryFiles map[string][]byte, entrypoint string) (fs.FS, error) {
	if len(files)+1 > maxProjectFiles {
		return nil, fmt.Errorf("project has more than %d files", maxProjectFiles)
	}
	if len(source) > maxSourceBytes {
		return nil, fmt.Errorf("source exceeds the %d-byte limit", maxSourceBytes)
	}
	totalBytes := len(source)
	project := make(fstest.MapFS, len(files))
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
		case wasmprotocol.FileInputEncodingBase64:
			if input.Content == nil {
				return nil, fmt.Errorf("project file %q is missing JSON content", relativePath)
			}
			decoded, err := base64.StdEncoding.DecodeString(*input.Content)
			if err != nil {
				return nil, fmt.Errorf("decode project file %q: invalid base64 content", relativePath)
			}
			content = decoded
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
			return nil, fmt.Errorf("project file %q encoding must be utf8, base64, or binary", relativePath)
		}
		if len(content) > maxProjectFileBytes {
			return nil, fmt.Errorf("project file %q exceeds the %d-byte limit", relativePath, maxProjectFileBytes)
		}
		totalBytes += len(content)
		if totalBytes > maxProjectBytes {
			return nil, fmt.Errorf("project files exceed the %d-byte limit", maxProjectBytes)
		}
		project[relativePath] = &fstest.MapFile{Data: content, Mode: 0o444}
	}
	if binaryCount != len(binaryFiles) {
		return nil, errors.New("binary file map contains files without binary metadata")
	}

	return project, nil
}

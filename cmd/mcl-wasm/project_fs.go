//go:build js && wasm

package main

import (
	"encoding/base64"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"testing/fstest"

	"github.com/manifest-configuration-language/mcl/internal/wasmprotocol"
)

func newProjectFiles(source string, files map[string]fileInput, entrypoint string) (fs.FS, error) {
	if len(files)+1 > maxProjectFiles {
		return nil, fmt.Errorf("project has more than %d files", maxProjectFiles)
	}
	if len(source) > maxSourceBytes {
		return nil, fmt.Errorf("source exceeds the %d-byte limit", maxSourceBytes)
	}
	totalBytes := len(source)
	project := make(fstest.MapFS, len(files))
	for relativePath, input := range files {
		if !filepath.IsLocal(relativePath) || path.Clean(relativePath) != relativePath || relativePath == "." || relativePath == entrypoint || strings.Contains(relativePath, `\`) {
			return nil, fmt.Errorf("invalid project file path %q", relativePath)
		}
		var content []byte
		switch input.Encoding {
		case wasmprotocol.FileInputEncodingUtf8:
			content = []byte(input.Content)
		case wasmprotocol.FileInputEncodingBase64:
			decoded, err := base64.StdEncoding.DecodeString(input.Content)
			if err != nil {
				return nil, fmt.Errorf("decode project file %q: invalid base64 content", relativePath)
			}
			content = decoded
		default:
			return nil, fmt.Errorf("project file %q encoding must be utf8 or base64", relativePath)
		}
		if len(content) > maxProjectFileBytes {
			return nil, fmt.Errorf("project file %q exceeds the %d-byte limit", relativePath, maxProjectFileBytes)
		}
		totalBytes += len(content)
		if totalBytes > maxProjectBytes {
			return nil, fmt.Errorf("project files exceed the %d-byte limit", maxProjectBytes)
		}
		project[relativePath] = &fstest.MapFile{Data: append([]byte(nil), content...), Mode: 0o444}
	}

	return project, nil
}

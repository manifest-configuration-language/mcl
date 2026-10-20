package engine

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

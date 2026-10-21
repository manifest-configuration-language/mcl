package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// EvaluateProjectSourceContext evaluates source as entryPath in a project
// whose files are exposed through root-relative files. Entry paths, overlay
// paths, and paths opened through files use slash-separated names.
func EvaluateProjectSourceContext(ctx context.Context, projectRoot, entryPath, source string, files fs.FS, overlays map[string]string) (Bundle, error) {
	bundle, _, err := EvaluateProjectSourceWithDependenciesContext(ctx, projectRoot, entryPath, source, files, overlays)

	return bundle, err
}

// EvaluateProjectSourceWithDependenciesContext evaluates source from a
// root-relative filesystem and returns the absolute logical paths of modules
// loaded during evaluation.
func EvaluateProjectSourceWithDependenciesContext(ctx context.Context, projectRoot, entryPath, source string, files fs.FS, overlays map[string]string) (Bundle, []string, error) {
	if ctx == nil {
		return Bundle{}, nil, errors.New("evaluation context is required")
	}
	if files == nil {
		return Bundle{}, nil, errors.New("project filesystem is required")
	}
	if !filepath.IsAbs(projectRoot) {
		return Bundle{}, nil, fmt.Errorf("project root %q must be absolute", projectRoot)
	}
	if !validProjectPath(entryPath) {
		return Bundle{}, nil, fmt.Errorf("invalid project entry path %q", entryPath)
	}
	root := filepath.Clean(projectRoot)
	entry := filepath.Join(root, filepath.FromSlash(entryPath))
	absoluteOverlays := make(map[string]string, len(overlays))
	for overlayPath, overlaySource := range overlays {
		if !validProjectPath(overlayPath) {
			return Bundle{}, nil, fmt.Errorf("invalid project overlay path %q", overlayPath)
		}
		absoluteOverlays[filepath.Join(root, filepath.FromSlash(overlayPath))] = overlaySource
	}
	filesystem := newProjectFilesystem(root, files)
	bundle, loaded, err := evaluateSourceWithFilesystem(ctx, entry, source, absoluteOverlays, filesystem)

	return bundle, loaded, err
}

// LoadKubernetesResourceCatalogForProjectFS reads the project configuration
// from a root-relative filesystem. projectRoot must be an absolute logical
// root and files must use slash-separated paths relative to that root.
func LoadKubernetesResourceCatalogForProjectFS(projectRoot string, files fs.FS) (KubernetesResourceCatalog, error) {
	if files == nil {
		return KubernetesResourceCatalog{}, errors.New("project filesystem is required")
	}
	if !filepath.IsAbs(projectRoot) {
		return KubernetesResourceCatalog{}, fmt.Errorf("project root %q must be absolute", projectRoot)
	}
	root := filepath.Clean(projectRoot)

	return loadKubernetesResourceCatalogWithFilesystem(root, newProjectFilesystem(root, files))
}

func validProjectPath(name string) bool {
	return fs.ValidPath(name) && name != "." && filepath.IsLocal(filepath.FromSlash(name)) && !strings.Contains(name, `\`)
}

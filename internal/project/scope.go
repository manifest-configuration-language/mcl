package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/manifest-configuration-language/mcl/internal/safepath"
)

// DiscoverRoot finds the nearest regular mcl.toml marker or uses the filesystem root.
func DiscoverRoot(directory string) (string, error) {
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	defaultRoot := root
	for {
		marker := filepath.Join(root, ConfigFileName)
		info, err := os.Lstat(marker)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("project root marker %q must be a regular file", marker)
			}

			return root, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect project root marker %q: %w", marker, err)
		}
		parent := filepath.Dir(root)
		if parent == root {
			return defaultRoot, nil
		}
		root = parent
	}
}

// ResolveImportPath resolves an OS-backed importer reference inside its MCL project.
func ResolveImportPath(importer, modulePath string) (string, error) {
	absolute, err := filepath.Abs(importer)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve importer %q: %w", importer, err)
	}
	root, err := DiscoverRoot(filepath.Dir(canonical))
	if err != nil {
		return "", err
	}

	return NewOSFilesystem(root).ResolveModuleImport(canonical, modulePath)
}

// IsValidProjectPath reports whether name is a valid root-relative project path.
func IsValidProjectPath(name string) bool {
	return validProjectPath(name)
}

// WorkspaceRoots resolves requested roots within the discovered project root.
func WorkspaceRoots(projectRoot string, workspaceRoots []string) ([]string, error) {
	if len(workspaceRoots) == 0 {
		return []string{projectRoot}, nil
	}
	roots := make([]string, 0, len(workspaceRoots))
	for _, workspaceRoot := range workspaceRoots {
		absolute, err := filepath.Abs(workspaceRoot)
		if err != nil {
			return nil, err
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = resolved
		}
		absolute = filepath.Clean(absolute)
		if !safepath.Within(projectRoot, absolute) && !safepath.Within(absolute, projectRoot) {
			continue
		}
		if safepath.Within(projectRoot, absolute) {
			roots = append(roots, absolute)

			continue
		}
		roots = append(roots, projectRoot)
	}
	slices.Sort(roots)
	roots = slices.Compact(roots)
	if len(roots) == 0 {
		return nil, fmt.Errorf("project root %q is outside all workspace roots", projectRoot)
	}
	nonOverlapping := make([]string, 0, len(roots))
	for _, candidate := range roots {
		if WithinAny(nonOverlapping, candidate) {
			continue
		}
		nonOverlapping = append(nonOverlapping, candidate)
	}

	return nonOverlapping, nil
}

// WithinAny reports whether path is inside any root.
func WithinAny(roots []string, path string) bool {
	for _, root := range roots {
		if safepath.Within(root, path) {
			return true
		}
	}

	return false
}

// IgnoredRenameIndexDirectory reports directories that should not participate in workspace rename indexing.
func IgnoredRenameIndexDirectory(name string) bool {
	return filepath.Base(name) == ".git" || filepath.Base(name) == "node_modules" || filepath.Base(name) == "cache" && filepath.Base(filepath.Dir(name)) == ".mcl"
}

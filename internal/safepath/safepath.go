// Package safepath provides path containment checks shared by filesystem boundaries.
package safepath

import (
	"errors"
	"path/filepath"
	"strings"
)

var ErrOutsideRoot = errors.New("path escapes root")

// Within reports whether target is root or one of its descendants lexically.
// Callers that follow symbolic links must resolve them before relying on this check.
func Within(root, target string) bool {
	relative, err := filepath.Rel(root, target)

	return err == nil && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// ResolveWithin resolves existing paths and rejects targets outside the resolved root.
func ResolveWithin(root, target string) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	if !Within(resolvedRoot, resolvedTarget) {
		return "", ErrOutsideRoot
	}

	return resolvedTarget, nil
}

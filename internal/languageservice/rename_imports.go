package languageservice

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/manifest-configuration-language/mcl/internal/syntax"
	"github.com/manifest-configuration-language/mcl/internal/wasmprotocol"
)

// RenameFileImportsResult contains import-path edits needed when an MCL file moves.
type RenameFileImportsResult struct {
	OldPath string       `json:"oldPath"`
	NewPath string       `json:"newPath"`
	Edits   []RenameEdit `json:"edits"`
}

// ImportRenameError identifies a project source that could not be safely analyzed.
type ImportRenameError struct {
	Path string
	Err  error
}

func (err *ImportRenameError) Error() string {
	return fmt.Sprintf("cannot update imports because %s could not be parsed: %v", err.Path, err.Err)
}

func (err *ImportRenameError) Unwrap() error { return err.Err }

// RenameFileImportsForProjectFS updates relative imports affected by moving an MCL file.
// The caller applies the returned edits and moves the file in its project model.
func RenameFileImportsForProjectFS(params NavigationParams, projectFiles fs.FS, oldPath, newPath string) (*RenameFileImportsResult, error) {
	if !validMCLProjectPath(oldPath) || !validMCLProjectPath(newPath) || oldPath == newPath {
		return nil, fmt.Errorf("old and new paths must be distinct project-relative MCL paths")
	}
	sources, _, ok := projectSourceSnapshot(params, projectFiles)
	if !ok {
		return nil, fmt.Errorf("could not read the MCL project")
	}
	if _, exists := sources[oldPath]; !exists {
		return nil, fmt.Errorf("MCL source does not exist: %s", oldPath)
	}
	if _, exists := sources[newPath]; exists {
		return nil, fmt.Errorf("MCL source already exists: %s", newPath)
	}

	paths := make([]string, 0, len(sources))
	for sourcePath := range sources {
		paths = append(paths, sourcePath)
	}
	slices.Sort(paths)
	result := &RenameFileImportsResult{OldPath: oldPath, NewPath: newPath, Edits: make([]RenameEdit, 0)}
	for _, importer := range paths {
		source := sources[importer]
		module, err := syntax.ParseAST(source)
		if err != nil {
			return nil, &ImportRenameError{Path: importer, Err: err}
		}
		newImporter := importer
		if importer == oldPath {
			newImporter = newPath
		}
		for _, node := range module.Nodes {
			declaration, ok := node.(*syntax.ImportDeclaration)
			if !ok || strings.HasPrefix(declaration.ModulePath, "std:") {
				continue
			}
			resolved, exists := resolveMCLImport(importer, declaration.ModulePath, sources)
			if !exists {
				continue
			}
			newTarget := resolved
			if resolved == oldPath {
				newTarget = newPath
			}
			newModulePath, err := relativeMCLImport(newImporter, newTarget)
			if err != nil {
				return nil, err
			}
			if newModulePath == declaration.ModulePath {
				continue
			}
			result.Edits = append(result.Edits, RenameEdit{
				Path:    importer,
				Range:   SourceRangeAt(source, declaration.ModulePathSpan),
				NewText: strconv.Quote(newModulePath),
			})
		}
	}
	slices.SortFunc(result.Edits, func(left, right RenameEdit) int {
		if left.Path != right.Path {
			return strings.Compare(left.Path, right.Path)
		}
		if left.Range.Start.Line != right.Range.Start.Line {
			return left.Range.Start.Line - right.Range.Start.Line
		}
		return left.Range.Start.Character - right.Range.Start.Character
	})

	return result, nil
}

func validMCLProjectPath(value string) bool {
	if !fs.ValidPath(value) || value == "." || utf8.RuneCountInString(value) > wasmprotocol.MaxProjectPathLength || strings.Contains(value, `\`) || strings.ContainsRune(value, '\x00') || path.Ext(value) != ".mcl" {
		return false
	}
	if len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' {
		return false
	}

	return len(strings.Split(value, "/")) <= wasmprotocol.MaxProjectPathDepth
}

func resolveMCLImport(importer, modulePath string, sources map[string]string) (string, bool) {
	if path.IsAbs(modulePath) || strings.Contains(modulePath, `\`) {
		return "", false
	}
	resolved := path.Clean(path.Join(path.Dir(importer), modulePath))
	if !validMCLProjectPath(resolved) {
		return "", false
	}
	_, exists := sources[resolved]

	return resolved, exists
}

func relativeMCLImport(importer, target string) (string, error) {
	relative, err := filepath.Rel(filepath.FromSlash(path.Dir(importer)), filepath.FromSlash(target))
	if err != nil {
		return "", fmt.Errorf("could not calculate import path from %s to %s: %w", importer, target, err)
	}
	if !strings.HasPrefix(relative, ".") {
		relative = "./" + relative
	}

	return filepath.ToSlash(relative), nil
}

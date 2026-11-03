package lsp

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/sourceindex"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

const windowsOS = "windows"

type documentState struct {
	Text         string
	Version      int
	SyncLost     bool
	Dependencies []string
	Diagnostics  map[string][]diagnostic
}

type documentParams struct {
	TextDocument struct {
		URI     string `json:"uri"`
		Text    string `json:"text"`
		Version int    `json:"version"`
	} `json:"textDocument"`
	ContentChanges []contentChange `json:"contentChanges"`
}

type contentChange struct {
	Range *textRange `json:"range"`
	Text  string     `json:"text"`
}

func (s *server) formatDocument(documentURI string) []textEdit {
	state, exists := s.documents[documentURI]
	if !exists {
		return []textEdit{}
	}
	formatted, err := syntax.FormatSource(state.Text)
	if err != nil || formatted == state.Text {
		return []textEdit{}
	}

	lines := strings.Split(state.Text, "\n")
	lastLine := lines[len(lines)-1]
	lastLine = strings.TrimSuffix(lastLine, "\r")
	edit := textEdit{NewText: formatted}
	edit.Range.Start = location{}
	edit.Range.End = location{Line: len(lines) - 1, Character: utf16Length([]rune(lastLine))}

	return []textEdit{edit}
}

type textRange struct {
	Start location `json:"start"`
	End   location `json:"end"`
}

func workspaceFolderPaths(folders []workspaceFolder) []string {
	paths := make([]string, 0, len(folders))
	for _, folder := range folders {
		if path := documentPath(folder.URI.String()); path != "" {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)

	return slices.Compact(paths)
}

// applyContentChangesはcontentChangesを1件ずつテキストへ適用する。
// 1件でも無効な変更が含まれていた場合はfalseを返し、呼び出し側はドキュメントを保存しない。
func applyContentChanges(text string, changes []contentChange) (string, bool) {
	for _, change := range changes {
		var err error
		text, err = applyContentChange(text, change)
		if err != nil {
			return text, false
		}
	}

	return text, true
}

func applyContentChange(text string, change contentChange) (string, error) {
	if change.Range == nil {
		return change.Text, nil
	}
	index := sourceindex.New(text)
	start, err := index.OffsetAt(sourceindex.Position{Line: change.Range.Start.Line, Character: change.Range.Start.Character})
	if err != nil {
		return text, err
	}
	end, err := index.OffsetAt(sourceindex.Position{Line: change.Range.End.Line, Character: change.Range.End.Character})
	if err != nil {
		return text, err
	}
	if end < start {
		return text, errors.New("text document change range is reversed")
	}

	return text[:start] + change.Text + text[end:], nil
}

func offsetAtUTF16(text string, position location) (int, error) {
	return sourceindex.New(text).OffsetAt(sourceindex.Position{Line: position.Line, Character: position.Character})
}

func (s *server) sourceForPath(path, fallback string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fallback
	}
	if _, source, ok := s.openDocumentForPath(absolute); ok {
		return source
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return fallback
	}

	return string(content)
}

func (s *server) diagnosticURIForPath(path string) string {
	if uri, _, ok := s.openDocumentForPath(path); ok {
		return uri
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}

	return filePathToURI(absolute)
}

func (s *server) openDocumentForPath(path string) (string, string, bool) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", false
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	uris := make([]string, 0, len(s.documents))
	for uri := range s.documents {
		uris = append(uris, uri)
	}
	slices.Sort(uris)
	for _, uri := range uris {
		state := s.documents[uri]
		documentPath, ok := fileURIToPath(uri)
		if !ok {
			continue
		}
		documentPath, err := filepath.Abs(documentPath)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(documentPath); err == nil {
			documentPath = resolved
		}
		if filepath.Clean(documentPath) == filepath.Clean(absolute) {
			return uri, state.Text, true
		}
	}

	return "", "", false
}

func documentPath(uri string) string {
	path, ok := fileURIToPath(uri)
	if !ok {
		return ""
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}

	return filepath.Clean(path)
}

func fileURIToPath(uri string) (string, bool) {
	return fileURIToPathForOS(uri, runtime.GOOS)
}

func fileURIToPathForOS(uri, goos string) (string, bool) {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != uriSchemeFile || parsed.Opaque != "" {
		return "", false
	}
	path := parsed.Path
	if goos == windowsOS {
		if parsed.Host != "" && parsed.Host != localhostHost {
			return `\\` + parsed.Host + strings.ReplaceAll(path, "/", `\`), true
		}
		if len(path) >= 3 && path[0] == '/' && ((path[1] >= 'A' && path[1] <= 'Z') || (path[1] >= 'a' && path[1] <= 'z')) && path[2] == ':' {
			path = path[1:]
		}

		return strings.ReplaceAll(path, "/", `\`), true
	}
	if parsed.Host != "" && parsed.Host != localhostHost {
		return "", false
	}

	return filepath.FromSlash(path), true
}

func filePathToURI(path string) string {
	return filePathToURIForOS(path, runtime.GOOS)
}

func filePathToURIForOS(path, goos string) string {
	if goos == windowsOS && strings.HasPrefix(path, `\\`) {
		unc := strings.TrimPrefix(path, `\\`)
		host, remaining, found := strings.Cut(unc, `\`)
		if found {
			return (&url.URL{Scheme: uriSchemeFile, Host: host, Path: "/" + strings.ReplaceAll(remaining, `\`, "/")}).String()
		}
	}
	if goos == windowsOS {
		path = strings.ReplaceAll(path, `\`, "/")
	} else {
		path = filepath.ToSlash(path)
	}
	if goos == windowsOS && len(path) >= 2 && path[1] == ':' {
		path = "/" + path
	}

	return (&url.URL{Scheme: uriSchemeFile, Path: path}).String()
}

func (s *server) overlayFiles() map[string]string {
	overlays := make(map[string]string)
	for uri, state := range s.documents {
		path, ok := fileURIToPath(uri)
		if !ok {
			continue
		}
		overlays[path] = state.Text
	}

	return overlays
}

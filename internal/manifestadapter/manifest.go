package manifestadapter

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/manifest-configuration-language/mcl/internal/safepath"
)

const maxManifestBytes = 16 << 20

// RequestはManifest sourceの解決と読み込みに必要な情報を保持します。
type Request struct {
	Source       string
	ProjectRoot  string
	SourcePath   string
	Dependencies *dependency.Lock
	// SourceFS optionally supplies project files through root-relative io/fs paths.
	SourceFS fs.FS
}

// Resultは読み込んだManifestとsource情報を保持します。
type Result struct {
	YAML            []byte
	LocalSourcePath string
	Origin          string
}

// Loadはlocalまたはlock済みremote Manifestを読み込みます。
func Load(request Request) (Result, error) {
	if strings.Contains(request.Source, "://") {
		if request.Dependencies == nil {
			return Result{}, errors.New("manifest.load requires file-based evaluation")
		}
		data, err := request.Dependencies.LoadManifest(request.Source)
		if err != nil {
			return Result{}, err
		}

		return Result{YAML: data, Origin: request.Source}, nil
	}
	if filepath.IsAbs(request.Source) {
		return Result{}, errors.New("manifest.load accepts only local relative sources")
	}
	if request.SourceFS != nil {
		return loadFromSourceFS(request)
	}
	path, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(request.SourcePath), filepath.FromSlash(request.Source)))
	if err != nil {
		return Result{}, fmt.Errorf("resolve Manifest source: %w", err)
	}
	if !safepath.Within(request.ProjectRoot, path) {
		return Result{}, errors.New("manifest source escapes the project directory")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, fmt.Errorf("stat Manifest source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Result{}, errors.New("manifest source must be a regular file")
	}
	data, err := readLimitedFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("read Manifest source: %w", err)
	}
	relativePath, err := filepath.Rel(request.ProjectRoot, path)
	if err != nil {
		return Result{}, fmt.Errorf("relativize Manifest source: %w", err)
	}

	return Result{YAML: data, LocalSourcePath: path, Origin: filepath.ToSlash(relativePath)}, nil
}

func loadFromSourceFS(request Request) (Result, error) {
	projectPath, err := filepath.Rel(request.ProjectRoot, filepath.Clean(request.SourcePath))
	if err != nil || projectPath == ".." || filepath.IsAbs(projectPath) || strings.HasPrefix(projectPath, ".."+string(filepath.Separator)) {
		return Result{}, fmt.Errorf("resolve Manifest source path %q: outside project root", request.SourcePath)
	}
	projectPath = filepath.ToSlash(projectPath)
	if !fs.ValidPath(projectPath) || strings.ContainsAny(request.Source, `\\`) || path.IsAbs(request.Source) {
		return Result{}, errors.New("manifest.load accepts only local relative sources")
	}
	relativePath := path.Join(path.Dir(projectPath), request.Source)
	if !fs.ValidPath(relativePath) {
		return Result{}, errors.New("manifest source escapes the project directory")
	}
	info, err := fs.Stat(request.SourceFS, relativePath)
	if err != nil {
		return Result{}, fmt.Errorf("stat Manifest source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Result{}, errors.New("manifest source must be a regular file")
	}
	data, err := readLimitedSourceFS(request.SourceFS, relativePath)
	if err != nil {
		return Result{}, fmt.Errorf("read Manifest source: %w", err)
	}
	localPath := filepath.Join(request.ProjectRoot, filepath.FromSlash(relativePath))

	return Result{YAML: data, LocalSourcePath: localPath, Origin: relativePath}, nil
}

func readLimitedSourceFS(files fs.FS, name string) ([]byte, error) {
	file, err := files.Open(name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if int64(len(data)) > maxManifestBytes {
		return nil, fmt.Errorf("file %q exceeds the %d-byte limit", name, maxManifestBytes)
	}

	return data, nil
}

func readLimitedFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxManifestBytes {
		return nil, fmt.Errorf("file %q exceeds the %d-byte limit", path, maxManifestBytes)
	}

	return data, nil
}

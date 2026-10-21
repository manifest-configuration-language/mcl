package engine

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/manifest-configuration-language/mcl/internal/safepath"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type projectSchemaLoader struct {
	root         string
	files        *projectFilesystem
	dependencies *dependency.Lock
	loadedFiles  map[string]struct{}
	loadedURLs   map[string]struct{}
	loadedBytes  int64
}

const maxSchemaFiles = 128

const maxSchemaBytes = 32 << 20

func compileSchema(source, projectRoot, modulePath string, dependencies *dependency.Lock, filesystems ...*projectFilesystem) (*jsonschema.Schema, error) {
	files := projectFilesystemFor(projectRoot, filesystems)
	var resourceURL string
	var data []byte
	loadedFiles := make(map[string]struct{})
	loadedURLs := make(map[string]struct{})
	if strings.Contains(source, "://") {
		if dependencies == nil {
			return nil, fmt.Errorf("external Schema %q requires file-based evaluation", source)
		}
		contentURL, resolvedURL, err := dependency.SchemaSourceURL(source)
		if err != nil {
			return nil, err
		}
		resourceURL = resolvedURL
		data, err = dependencies.LoadSchema(contentURL)
		if err != nil {
			return nil, fmt.Errorf("load Schema %q: %w", source, err)
		}
		loadedURLs[contentURL] = struct{}{}
	} else {
		if filepath.IsAbs(source) {
			return nil, errors.New("schema() accepts only local relative paths or HTTPS URLs")
		}
		path := filepath.Join(filepath.Dir(modulePath), filepath.FromSlash(source))
		path, err := files.resolveReadPath(path)
		if err != nil {
			if errors.Is(err, safepath.ErrOutsideRoot) || !pathWithin(projectRoot, filepath.Clean(filepath.Join(filepath.Dir(modulePath), filepath.FromSlash(source)))) {
				return nil, fmt.Errorf("schema %q escapes the project directory", source)
			}

			return nil, fmt.Errorf("resolve Schema %q: %w", source, err)
		}
		data, err = files.readLimitedFileWithLimit(path, maxYAMLBytes)
		if err != nil {
			return nil, fmt.Errorf("read Schema %q: %w", source, err)
		}

		resourceURL = (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
		loadedFiles[path] = struct{}{}
	}
	if len(data) > maxYAMLBytes {
		return nil, fmt.Errorf("schema %q exceeds the %d-byte limit", source, maxYAMLBytes)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse Schema %q: %w", source, err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(&projectSchemaLoader{
		root: projectRoot, files: files, dependencies: dependencies, loadedFiles: loadedFiles,
		loadedURLs: loadedURLs, loadedBytes: int64(len(data)),
	})
	if err := compiler.AddResource(resourceURL, document); err != nil {
		return nil, fmt.Errorf("register Schema %q: %w", source, err)
	}
	compiled, err := compiler.Compile(resourceURL)
	if err != nil {
		return nil, fmt.Errorf("compile Schema %q: %w", source, err)
	}

	return compiled, nil
}

func (loader *projectSchemaLoader) Load(source string) (any, error) {
	parsed, err := url.Parse(source)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == schemeHTTPS {
		return loader.loadExternal(source)
	}
	if parsed.Scheme != "file" || parsed.Host != "" {
		return nil, fmt.Errorf("schema reference %q is not a local file", source)
	}
	path, err := loader.resolveFileURLPath(filepath.FromSlash(parsed.Path))
	if err != nil {
		return nil, err
	}
	files := projectFilesystemFor(loader.root, []*projectFilesystem{loader.files})
	data, err := files.readLimitedFileWithLimit(path, maxYAMLBytes)
	if err != nil {
		return nil, err
	}
	if _, exists := loader.loadedFiles[path]; !exists {
		if len(loader.loadedFiles)+len(loader.loadedURLs) >= maxSchemaFiles {
			return nil, fmt.Errorf("schema dependency graph exceeds the %d-file limit", maxSchemaFiles)
		}
		loader.loadedFiles[path] = struct{}{}
		loader.loadedBytes += int64(len(data))
		if loader.loadedBytes > maxSchemaBytes {
			return nil, fmt.Errorf("schema dependency graph exceeds the %d-byte limit", maxSchemaBytes)
		}
	}

	return jsonschema.UnmarshalJSON(bytes.NewReader(data))
}

func (loader *projectSchemaLoader) resolveFileURLPath(path string) (string, error) {
	files := projectFilesystemFor(loader.root, []*projectFilesystem{loader.files})
	resolved, err := files.resolveReadPath(path)
	if errors.Is(err, safepath.ErrOutsideRoot) || err != nil && !pathWithin(loader.root, filepath.Clean(path)) {
		return "", fmt.Errorf("schema reference escapes the project directory: %w", err)
	}

	return resolved, err
}

func (loader *projectSchemaLoader) loadExternal(source string) (any, error) {
	if loader.dependencies == nil {
		return nil, fmt.Errorf("external Schema reference %q requires file-based evaluation", source)
	}
	contentURL, _, err := dependency.SchemaSourceURL(source)
	if err != nil {
		return nil, err
	}
	data, err := loader.dependencies.LoadSchema(contentURL)
	if err != nil {
		return nil, err
	}
	if _, exists := loader.loadedURLs[contentURL]; !exists {
		if len(loader.loadedFiles)+len(loader.loadedURLs) >= maxSchemaFiles {
			return nil, fmt.Errorf("schema dependency graph exceeds the %d-file limit", maxSchemaFiles)
		}
		loader.loadedURLs[contentURL] = struct{}{}
		loader.loadedBytes += int64(len(data))
		if loader.loadedBytes > maxSchemaBytes {
			return nil, fmt.Errorf("schema dependency graph exceeds the %d-byte limit", maxSchemaBytes)
		}
	}

	return jsonschema.UnmarshalJSON(bytes.NewReader(data))
}

func loadTypeSchemaFilesWithFilesystem(types map[string]ResolvedType, projectRoot, modulePath string, dependencies *dependency.Lock, files *projectFilesystem, analyses ...*TypeAnalysisContext) error {
	for _, name := range slices.Sorted(maps.Keys(types)) {
		typeExpr := types[name]
		if err := loadSchemaTypesWithFilesystem(&typeExpr, projectRoot, modulePath, dependencies, files, analyses...); err != nil {
			return fmt.Errorf("type %q: %w", name, err)
		}
		types[name] = typeExpr
	}

	return nil
}

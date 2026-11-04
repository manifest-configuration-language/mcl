package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"helm.sh/helm/v4/pkg/chart/loader"
	v2 "helm.sh/helm/v4/pkg/chart/v2"
)

type projectConfig struct {
	CRDSchemas []projectCRDSchemaSource `toml:"crd_schemas"`
}

type projectCRDSchemaSource struct {
	Path       string `toml:"path"`
	File       string `toml:"file"`
	Source     string `toml:"source"`
	Revision   string `toml:"revision"`
	Directory  string `toml:"directory"`
	Version    string `toml:"version"`
	SelectKind string `toml:"select_kind"`
}

type resolvedCRDSchemaFile struct {
	name string
	data []byte
}

func loadCRDSchemaCatalog(ctx context.Context, files *projectFilesystem, dependencies *dependency.Lock) (map[customResourceDefinitionKey]crdVersionSchema, error) {
	configPath := filepath.Join(files.root, projectConfigFileName)
	configData, err := files.readLimitedFile(configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("read project configuration: %w", err)
	}
	var config projectConfig
	metadata, err := toml.Decode(string(configData), &config)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", projectConfigFileName, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		return nil, fmt.Errorf("decode %s: unsupported setting %q", projectConfigFileName, undecoded[0].String())
	}

	sources := slices.Clone(config.CRDSchemas)
	slices.SortFunc(sources, func(left, right projectCRDSchemaSource) int {
		return strings.Compare(crdSchemaSourceKey(left), crdSchemaSourceKey(right))
	})
	for index, source := range sources {
		if err := validateCRDSchemaSource(source); err != nil {
			return nil, fmt.Errorf("%s crd_schemas entry %d: %w", projectConfigFileName, index+1, err)
		}
		if index > 0 && crdSchemaSourceKey(sources[index-1]) == crdSchemaSourceKey(source) {
			return nil, fmt.Errorf("%s: duplicate crd_schemas source %q", projectConfigFileName, crdSchemaSourceKey(source))
		}
	}

	catalog := make(map[customResourceDefinitionKey]crdVersionSchema)
	for _, source := range sources {
		resolvedFiles, err := loadCRDSchemaSource(ctx, files, dependencies, source)
		if err != nil {
			return nil, fmt.Errorf("load CRD schema source %q: %w", crdSchemaSourceKey(source), err)
		}
		var crdDocumentCount int
		for _, resolvedFile := range resolvedFiles {
			documents, err := decodeRuntimeYAMLDocuments(resolvedFile.data, resolvedFile.name)
			if err != nil {
				return nil, fmt.Errorf("decode CRD schema %q: %w", resolvedFile.name, err)
			}
			if len(documents.List) == 0 {
				return nil, fmt.Errorf("CRD schema %q contains no documents", resolvedFile.name)
			}
			for documentIndex, document := range documents.List {
				kind, _ := runtimeStringField(document, "kind")
				if kind != kindCustomResourceDefinition {
					if source.SelectKind == kindCustomResourceDefinition {
						continue
					}

					return nil, fmt.Errorf("CRD schema %q document %d has kind %q, expected %q", resolvedFile.name, documentIndex+1, kind, kindCustomResourceDefinition)
				}
				crdDocumentCount++
				if err := registerRuntimeCustomResourceDefinition(ctx, catalog, document); err != nil {
					return nil, fmt.Errorf("CRD schema %q document %d: %w", resolvedFile.name, documentIndex+1, err)
				}
			}
		}
		if crdDocumentCount == 0 {
			return nil, fmt.Errorf("CRD schema source %q contains no CustomResourceDefinition documents", crdSchemaSourceKey(source))
		}
	}

	return catalog, nil
}

func crdSchemaSourceKey(source projectCRDSchemaSource) string {
	return strings.Join([]string{source.Path, source.File, source.Source, source.Revision, source.Directory, source.Version, source.SelectKind}, "\n")
}

func validateCRDSchemaSource(source projectCRDSchemaSource) error {
	if source.SelectKind != "" && source.SelectKind != kindCustomResourceDefinition {
		return fmt.Errorf("select_kind must be %q", kindCustomResourceDefinition)
	}
	if source.Path != "" {
		if source.File != "" || source.Source != "" || source.Revision != "" || source.Directory != "" || source.Version != "" {
			return errors.New("local path cannot be combined with remote source fields")
		}
		if filepath.IsAbs(source.Path) || strings.Contains(source.Path, "\\") || filepath.ToSlash(filepath.Clean(source.Path)) != source.Path || source.Path == "." || source.Path == ".." || strings.HasPrefix(source.Path, "../") {
			return errors.New("path must be a normalized project-relative file path")
		}

		return nil
	}
	if source.Source == "" {
		return errors.New("either path or source is required")
	}
	switch {
	case strings.HasPrefix(source.Source, "https://"):
		if source.File != "" || source.Revision != "" || source.Directory != "" || source.Version != "" {
			return errors.New("HTTPS YAML source cannot include file, revision, directory, or version")
		}
		parsed, err := url.Parse(source.Source)
		if err != nil {
			return err
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("HTTPS YAML source must not include a query or fragment")
		}
		if _, _, err := dependency.SchemaSourceURL(source.Source); err != nil {
			return err
		}
	case strings.HasPrefix(source.Source, "git+"):
		if source.Version != "" {
			return errors.New("git source cannot include version")
		}
		if err := dependency.ValidateGitSource(source.Source); err != nil {
			return err
		}
		if !dependency.ValidGitRevision(source.Revision) {
			return errors.New("git source revision must be a complete lowercase commit SHA-1")
		}
		if err := dependency.ValidateGitDirectory(source.Directory); err != nil {
			return err
		}
		if source.File != "" && !validCRDSchemaArchivePath(source.File) {
			return errors.New("git path must be a normalized relative YAML file path")
		}
	case strings.HasPrefix(source.Source, "oci://"):
		if source.Revision != "" || source.Directory != "" {
			return errors.New("OCI source cannot include revision or directory")
		}
		if err := dependency.ValidateOCIChartSource(source.Source); err != nil {
			return err
		}
		if source.Version == "" {
			return errors.New("OCI Helm Chart source requires a version")
		}
		if source.File != "" && !validCRDSchemaArchivePath(source.File) {
			return errors.New("OCI path must be a normalized relative YAML file path under crds/")
		}
	default:
		return errors.New("source must be an HTTPS YAML URL, git+https repository, or OCI Helm Chart")
	}

	return nil
}

func validCRDSchemaArchivePath(value string) bool {
	return value != "" && !strings.Contains(value, "\\") && !path.IsAbs(value) && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../") && (strings.HasSuffix(value, ".yaml") || strings.HasSuffix(value, ".yml"))
}

func loadCRDSchemaSource(ctx context.Context, files *projectFilesystem, dependencies *dependency.Lock, source projectCRDSchemaSource) ([]resolvedCRDSchemaFile, error) {
	if source.Path != "" {
		path, err := files.resolveReadPath(filepath.Join(files.root, filepath.FromSlash(source.Path)))
		if err != nil {
			return nil, fmt.Errorf("resolve local CRD schema path: %w", err)
		}
		data, err := files.readLimitedFile(path)
		if err != nil {
			return nil, fmt.Errorf("read local CRD schema: %w", err)
		}

		return []resolvedCRDSchemaFile{{name: path, data: data}}, nil
	}
	if dependencies == nil {
		return nil, errors.New("remote CRD schemas require project dependency locking")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch {
	case strings.HasPrefix(source.Source, "https://"):
		data, err := dependencies.LoadSchema(source.Source)
		if err != nil {
			return nil, err
		}

		return []resolvedCRDSchemaFile{{name: source.Source, data: data}}, nil
	case strings.HasPrefix(source.Source, "git+"):
		archive, err := dependencies.LoadGitSource(source.Source, source.Revision, source.Directory)
		if err != nil {
			return nil, err
		}

		return crdSchemasFromTar(source.Source+"@"+source.Revision+"/"+source.Directory, archive, source.File)
	case strings.HasPrefix(source.Source, "oci://"):
		archive, err := dependencies.LoadOCIChart(source.Source, source.Version)
		if err != nil {
			return nil, err
		}
		chart, err := loader.LoadArchive(bytes.NewReader(archive))
		if err != nil {
			return nil, fmt.Errorf("load OCI Helm Chart archive: %w", err)
		}
		loadedChart, ok := chart.(*v2.Chart)
		if !ok || loadedChart.Metadata == nil || loadedChart.Metadata.Version != source.Version {
			return nil, fmt.Errorf("OCI Helm Chart archive version does not match requested version %q", source.Version)
		}
		files := make([]resolvedCRDSchemaFile, 0, len(loadedChart.CRDObjects()))
		var totalBytes int
		for _, crd := range loadedChart.CRDObjects() {
			if crd.File == nil || crd.File.Name == "" || crd.File.Data == nil {
				return nil, errors.New("OCI Helm Chart contains an invalid CRD file")
			}
			name := crd.File.Name
			if !strings.HasPrefix(name, "crds/") || !validCRDSchemaArchivePath(name) {
				return nil, fmt.Errorf("OCI Helm Chart contains invalid CRD path %q", name)
			}
			if source.File != "" && strings.TrimPrefix(name, "crds/") != source.File {
				continue
			}
			if len(crd.File.Data) > dependency.MaxKustomizeSourceBytes {
				return nil, fmt.Errorf("OCI Helm Chart CRD %q exceeds the %d-byte limit", name, dependency.MaxKustomizeSourceBytes)
			}
			totalBytes += len(crd.File.Data)
			if totalBytes > dependency.MaxKustomizeSourceBytes {
				return nil, fmt.Errorf("OCI Helm Chart CRD files exceed the %d-byte limit", dependency.MaxKustomizeSourceBytes)
			}
			files = append(files, resolvedCRDSchemaFile{name: source.Source + "@" + source.Version + "/" + name, data: crd.File.Data})
		}
		if len(files) == 0 {
			return nil, errors.New("OCI Helm Chart contains no matching static CRDs under crds/")
		}
		slices.SortFunc(files, func(left, right resolvedCRDSchemaFile) int { return strings.Compare(left.name, right.name) })

		return files, nil
	default:
		return nil, errors.New("unsupported CRD schema source")
	}
}

func crdSchemasFromTar(source string, archive []byte, selectedPath string) ([]resolvedCRDSchemaFile, error) {
	reader := tar.NewReader(bytes.NewReader(archive))
	files := make([]resolvedCRDSchemaFile, 0)
	seen := make(map[string]struct{})
	var totalBytes int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read locked Git source archive: %w", err)
		}
		name := path.Clean(header.Name)
		if header.Typeflag != tar.TypeReg || header.Name != name || path.IsAbs(name) || name == "." || name == ".." || strings.HasPrefix(name, "../") || header.Size < 0 {
			return nil, errors.New("locked Git source archive contains an invalid entry")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("locked Git source archive repeats file %q", name)
		}
		seen[name] = struct{}{}
		if header.Size > dependency.MaxKustomizeSourceBytes-totalBytes {
			return nil, errors.New("locked Git source archive exceeds the CRD schema size limit")
		}
		totalBytes += header.Size
		if selectedPath != "" && name != selectedPath {
			continue
		}
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil {
			return nil, fmt.Errorf("read locked Git CRD schema %q: %w", name, err)
		}
		if int64(len(data)) != header.Size {
			return nil, fmt.Errorf("locked Git CRD schema %q does not match its archive size", name)
		}
		files = append(files, resolvedCRDSchemaFile{name: source + "/" + name, data: data})
	}
	if selectedPath != "" && len(files) == 0 {
		return nil, fmt.Errorf("git source does not contain CRD schema file %q", selectedPath)
	}
	if len(files) == 0 {
		return nil, errors.New("git source directory contains no YAML CRD schema files")
	}
	slices.SortFunc(files, func(left, right resolvedCRDSchemaFile) int { return strings.Compare(left.name, right.name) })

	return files, nil
}

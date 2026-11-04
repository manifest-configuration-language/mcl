package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
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
	Files     []string `toml:"files"`
	Source    string   `toml:"source"`
	Revision  string   `toml:"revision"`
	Directory string   `toml:"directory"`
	Version   string   `toml:"version"`
}

type indexedCRDSchemaSource struct {
	index  int
	source projectCRDSchemaSource
}

type groupedCRDSchemaSource struct {
	source    projectCRDSchemaSource
	files     []string
	selectAll bool
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

	sources := make([]indexedCRDSchemaSource, len(config.CRDSchemas))
	for index, source := range config.CRDSchemas {
		sources[index] = indexedCRDSchemaSource{index: index + 1, source: source}
	}
	for _, indexed := range sources {
		source := indexed.source
		if err := validateCRDSchemaSource(source); err != nil {
			return nil, fmt.Errorf("%s crd_schemas entry %d: %w", projectConfigFileName, indexed.index, err)
		}
	}
	groups, err := groupCRDSchemaSources(sources)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", projectConfigFileName, err)
	}

	catalog := make(map[customResourceDefinitionKey]crdVersionSchema)
	for _, group := range groups {
		source := group.source
		resolvedFiles, err := loadCRDSchemaSource(ctx, dependencies, source)
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
					continue
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

func crdSchemaSourceIdentity(source projectCRDSchemaSource) string {
	return strings.Join([]string{source.Source, source.Revision, source.Directory, source.Version}, "\n")
}

func crdSchemaSourceKey(source projectCRDSchemaSource) string {
	return crdSchemaSourceIdentity(source)
}

func groupCRDSchemaSources(sources []indexedCRDSchemaSource) ([]groupedCRDSchemaSource, error) {
	groupsByIdentity := make(map[string]*groupedCRDSchemaSource, len(sources))
	for _, indexed := range sources {
		source := indexed.source
		key := crdSchemaSourceIdentity(source)
		group := groupsByIdentity[key]
		if group == nil {
			group = &groupedCRDSchemaSource{source: source, selectAll: len(source.Files) == 0}
			groupsByIdentity[key] = group
		} else if group.selectAll != (len(source.Files) == 0) {
			return nil, fmt.Errorf("crd_schemas entry %d mixes all-file and selected-file declarations for source %q", indexed.index, key)
		}
		for _, file := range source.Files {
			if slices.Contains(group.files, file) {
				return nil, fmt.Errorf("crd_schemas entry %d repeats file %q for source %q", indexed.index, file, key)
			}
			group.files = append(group.files, file)
		}
	}

	groups := make([]groupedCRDSchemaSource, 0, len(groupsByIdentity))
	for _, group := range groupsByIdentity {
		slices.Sort(group.files)
		if !group.selectAll {
			group.source.Files = group.files
		}
		groups = append(groups, *group)
	}
	slices.SortFunc(groups, func(left, right groupedCRDSchemaSource) int {
		return strings.Compare(crdSchemaSourceIdentity(left.source), crdSchemaSourceIdentity(right.source))
	})

	return groups, nil
}

func validateCRDSchemaSource(source projectCRDSchemaSource) error {
	if source.Source == "" {
		return errors.New("remote source is required")
	}
	for _, file := range source.Files {
		if !validCRDSchemaArchivePath(file) {
			return errors.New("files must contain normalized relative YAML file paths")
		}
	}
	switch {
	case strings.HasPrefix(source.Source, "https://"):
		if len(source.Files) != 0 || source.Revision != "" || source.Directory != "" || source.Version != "" {
			return errors.New("HTTPS YAML source cannot include files, revision, directory, or version")
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
	default:
		return errors.New("source must be an HTTPS YAML URL, git+https repository, or OCI Helm Chart")
	}

	return nil
}

func validCRDSchemaArchivePath(value string) bool {
	return value != "" && !strings.Contains(value, "\\") && !path.IsAbs(value) && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../") && (strings.HasSuffix(value, ".yaml") || strings.HasSuffix(value, ".yml"))
}

func loadCRDSchemaSource(ctx context.Context, dependencies *dependency.Lock, source projectCRDSchemaSource) ([]resolvedCRDSchemaFile, error) {
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

		return crdSchemasFromTar(source.Source+"@"+source.Revision+"/"+source.Directory, archive, source.Files)
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
		selectedFiles := make(map[string]struct{}, len(source.Files))
		selectFiles := len(source.Files) != 0
		for _, file := range source.Files {
			selectedFiles[file] = struct{}{}
		}
		for _, crd := range loadedChart.CRDObjects() {
			if crd.File == nil || crd.File.Name == "" || crd.File.Data == nil {
				return nil, errors.New("OCI Helm Chart contains an invalid CRD file")
			}
			name := crd.File.Name
			if !strings.HasPrefix(name, "crds/") || !validCRDSchemaArchivePath(name) {
				return nil, fmt.Errorf("OCI Helm Chart contains invalid CRD path %q", name)
			}
			if selectFiles {
				fileName := strings.TrimPrefix(name, "crds/")
				if _, selected := selectedFiles[fileName]; !selected {
					continue
				}
				delete(selectedFiles, fileName)
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
		if len(selectedFiles) != 0 {
			return nil, fmt.Errorf("OCI Helm Chart does not contain selected CRD files %q", slices.Sorted(maps.Keys(selectedFiles)))
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

func crdSchemasFromTar(source string, archive []byte, selectedPaths []string) ([]resolvedCRDSchemaFile, error) {
	reader := tar.NewReader(bytes.NewReader(archive))
	files := make([]resolvedCRDSchemaFile, 0)
	selected := make(map[string]struct{}, len(selectedPaths))
	selectFiles := len(selectedPaths) != 0
	for _, selectedPath := range selectedPaths {
		selected[selectedPath] = struct{}{}
	}
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
		if selectFiles {
			if _, ok := selected[name]; !ok {
				continue
			}
			delete(selected, name)
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
	if len(selected) != 0 {
		return nil, fmt.Errorf("git source does not contain selected CRD schema files %q", slices.Sorted(maps.Keys(selected)))
	}
	if len(files) == 0 {
		return nil, errors.New("git source directory contains no YAML CRD schema files")
	}
	slices.SortFunc(files, func(left, right resolvedCRDSchemaFile) int { return strings.Compare(left.name, right.name) })

	return files, nil
}

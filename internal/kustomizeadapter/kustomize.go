package kustomizeadapter

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"gopkg.in/yaml.v3"
	"sigs.k8s.io/kustomize/api/konfig"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

const yamlStringTag = "!!str"

const (
	maxSourceBytes = dependency.MaxKustomizeSourceBytes
	maxSourceFiles = dependency.MaxKustomizeSourceFiles
)

// RequestはKustomize sourceの解決とbuildに必要な入力を保持します。
type Request struct {
	Source       string
	Revision     string
	Directory    string
	ProjectRoot  string
	SourcePath   string
	Dependencies *dependency.Lock
	// SourceFS supplies the project files using root-relative io/fs paths for local sources.
	SourceFS fs.FS
	// SourcePathsOSBacked indicates whether diagnostic source paths refer to host files.
	SourcePathsOSBacked bool
}

// Resultはbuild済みYAMLとsource provenanceを保持します。
type Result struct {
	YAML            []byte
	Origins         []ResourceOrigin
	Origin          string
	SourceRoot      string
	LocalSourceRoot string
	SourceOSBacked  bool
	SourceDirectory string
	SourceFiles     map[string][]byte
	SourceLocators  map[string][]byte
}

// ResourceOriginはKustomize resourceのsource位置を保持します。
type ResourceOrigin struct {
	Path         string
	ConfiguredIn string
	Repo         string
	Ref          string
}

// ConfigSyntaxErrorはKustomize設定の構文解析errorとsource位置を保持します。
type ConfigSyntaxError struct {
	Path       string
	SourcePath string
	SourceData []byte
	Line       int
	Cause      error
}

func (err ConfigSyntaxError) Error() string {
	return fmt.Sprintf("parse Kustomize configuration %q: %v", err.Path, err.Cause)
}
func (err ConfigSyntaxError) Unwrap() error { return err.Cause }

type buildSource struct {
	root               string
	filesystem         filesys.FileSystem
	localRoot          string
	sourceMapRoot      string
	sourceMapDirectory string
	buildDirectory     string
	origin             string
}

type overlay struct {
	root                      string
	buildDirectory            string
	fileSystem                *overlayFileSystem
	injectedOriginAnnotations bool
}

// Buildはsourceを解決し、Kustomize resourcesをYAMLへ変換します。
func Build(request Request) (result Result, err error) {
	if request.Directory == "" {
		return Result{}, errors.New("kustomize directory is required")
	}
	if filepath.IsAbs(request.Directory) {
		return Result{}, errors.New("kustomize directory must be relative to the source")
	}
	source, err := resolveSource(request)
	if err != nil {
		return Result{}, err
	}
	workingRoot, fileSystem, err := createOverlay(source.root, source.buildDirectory, source.filesystem)
	if err != nil {
		return Result{}, err
	}
	overlayValue := overlay{root: workingRoot, fileSystem: fileSystem}
	resolverRoot := filepath.Join(workingRoot, filepath.FromSlash(source.buildDirectory))
	if err := resolveRemoteReferences(resolverRoot, request.Dependencies, fileSystem); err != nil {
		if source.sourceMapRoot != "" {
			if configError, ok := errors.AsType[ConfigSyntaxError](err); ok && configError.Line > 0 {
				relative, relErr := filepath.Rel(workingRoot, configError.Path)
				if relErr == nil && relative != "." && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					configError.SourcePath = filepath.Join(source.sourceMapRoot, relative)
					err = configError
				}
			}
		}

		return Result{}, err
	}
	buildDir, err := validateOverlayDirectory(workingRoot, source.buildDirectory, fileSystem)
	if err != nil {
		return Result{}, err
	}
	_, overlayValue.injectedOriginAnnotations = enableOriginTracking(fileSystem, buildDir)
	overlayValue.buildDirectory = buildDir
	options := krusty.MakeDefaultOptions()
	options.LoadRestrictions = types.LoadRestrictionsRootOnly
	resources, err := krusty.MakeKustomizer(options).Run(fileSystem, buildDir)
	if err != nil {
		return Result{}, fmt.Errorf("build Kustomize directory: %w", err)
	}
	origins := make([]ResourceOrigin, len(resources.Resources()))
	for index, item := range resources.Resources() {
		origin, _ := item.GetOrigin()
		if origin != nil {
			origins[index] = ResourceOrigin{Path: origin.Path, ConfiguredIn: origin.ConfiguredIn, Repo: origin.Repo, Ref: origin.Ref}
		}
	}
	if overlayValue.injectedOriginAnnotations {
		if err := resources.RemoveOriginAnnotations(); err != nil {
			return Result{}, fmt.Errorf("remove temporary Kustomize origin annotations: %w", err)
		}
	}
	output, err := resources.AsYaml()
	if err != nil {
		return Result{}, fmt.Errorf("serialize Kustomize resources: %w", err)
	}

	return Result{YAML: output, Origins: origins, Origin: source.origin, SourceRoot: source.sourceMapRoot, LocalSourceRoot: source.localRoot, SourceOSBacked: request.SourcePathsOSBacked, SourceDirectory: source.sourceMapDirectory, SourceFiles: fileSystem.sourceFiles, SourceLocators: mapResourceSourceLocators(origins, fileSystem)}, nil
}

func mapResourceSourceLocators(origins []ResourceOrigin, fileSystem *overlayFileSystem) map[string][]byte {
	locators := make(map[string][]byte)
	for _, origin := range origins {
		if origin.Repo == "" {
			continue
		}
		resourcePath := origin.Path
		if resourcePath == "" {
			resourcePath = origin.ConfiguredIn
		}
		resourcePath, ok := canonicalResourcePath(resourcePath)
		if !ok {
			continue
		}
		var sourcePath string
		for _, remoteRoot := range fileSystem.remoteRoots {
			if remoteRoot.repository != strings.TrimPrefix(origin.Repo, "git+") || remoteRoot.revision != origin.Ref {
				continue
			}
			rootPath := filepath.ToSlash(filepath.Clean(remoteRoot.path))
			candidate := resourcePath
			if candidate != rootPath && !strings.HasPrefix(candidate, rootPath+"/") {
				candidate = path.Join(rootPath, candidate)
			}
			if _, exists := fileSystem.sourceFiles[candidate]; !exists {
				continue
			}
			if sourcePath != "" && sourcePath != candidate {
				sourcePath = ""

				break
			}
			sourcePath = candidate
		}
		if sourcePath == "" {
			continue
		}
		locator := "kustomize:" + strings.TrimPrefix(origin.Repo, "git+") + "@" + origin.Ref + "/" + resourcePath
		locators[locator] = fileSystem.sourceFiles[sourcePath]
	}

	return locators
}

func canonicalResourcePath(resourcePath string) (string, bool) {
	resourcePath = filepath.ToSlash(resourcePath)
	cleaned := path.Clean(resourcePath)
	if cleaned == "." || cleaned == ".." || path.IsAbs(cleaned) || strings.HasPrefix(cleaned, "../") {
		return "", false
	}

	return cleaned, true
}

func resolveSource(request Request) (buildSource, error) {
	source := buildSource{buildDirectory: request.Directory, sourceMapDirectory: request.Directory}
	if strings.HasPrefix(request.Source, "git+") {
		if request.Revision == "" {
			return buildSource{}, errors.New("kustomize Git source requires a full commit SHA-1 revision")
		}
		if request.Dependencies == nil {
			return buildSource{}, errors.New("kustomize Git source requires dependency locking")
		}
		archive, err := request.Dependencies.LoadGitSource(request.Source, request.Revision, request.Directory)
		if err != nil {
			return buildSource{}, err
		}
		memoryFS, root, err := loadGitSourceFS(archive)
		if err != nil {
			return buildSource{}, err
		}
		source.root, source.filesystem, source.sourceMapRoot, source.sourceMapDirectory = root, memoryFS, root, "."
		source.buildDirectory = "."
		source.origin = fmt.Sprintf("kustomize:%s@%s/%s", request.Source, request.Revision, request.Directory)
	} else {
		if request.Revision != "" {
			return buildSource{}, errors.New("local Kustomize source requires an empty revision")
		}
		if strings.Contains(request.Source, "://") || filepath.IsAbs(request.Source) {
			return buildSource{}, errors.New("kustomize.build accepts only local relative or fixed Git sources")
		}
		if request.SourceFS == nil {
			return buildSource{}, errors.New("local Kustomize source requires a source filesystem")
		}
		projectPath, err := filepath.Rel(request.ProjectRoot, filepath.Clean(request.SourcePath))
		if err != nil {
			return buildSource{}, fmt.Errorf("resolve Kustomize source path %q: outside project root", request.SourcePath)
		}
		if projectPath == ".." || filepath.IsAbs(projectPath) || strings.HasPrefix(projectPath, ".."+string(filepath.Separator)) {
			return buildSource{}, fmt.Errorf("resolve Kustomize source path %q: outside project root", request.SourcePath)
		}
		relativeSource := path.Join(path.Dir(filepath.ToSlash(projectPath)), request.Source)
		if relativeSource != "." && !fs.ValidPath(relativeSource) {
			return buildSource{}, fmt.Errorf("resolve Kustomize source: invalid project path %q", relativeSource)
		}
		sourceFS, err := fs.Sub(request.SourceFS, relativeSource)
		if err != nil {
			return buildSource{}, fmt.Errorf("resolve Kustomize source: %w", err)
		}
		memoryFS, root, err := loadSourceFS(sourceFS)
		if err != nil {
			return buildSource{}, fmt.Errorf("load Kustomize source: %w", err)
		}
		originalRoot := filepath.Join(request.ProjectRoot, filepath.FromSlash(relativeSource))
		source.root, source.filesystem, source.localRoot, source.sourceMapRoot = root, memoryFS, originalRoot, originalRoot
		source.origin = "kustomize:" + filepath.ToSlash(filepath.Join(filepath.Dir(projectPath), filepath.FromSlash(request.Source), filepath.FromSlash(request.Directory)))
	}

	return source, nil
}

func validateOverlayDirectory(overlayRoot, buildDirectory string, fileSystem filesys.FileSystem) (string, error) {
	directory := filepath.Join(overlayRoot, filepath.FromSlash(buildDirectory))
	if !pathWithin(overlayRoot, directory) || !fileSystem.IsDir(directory) {
		return "", fmt.Errorf("resolve Kustomize directory: %q is not a directory within the source", buildDirectory)
	}

	return directory, nil
}

func enableOriginTracking(fileSystem filesys.FileSystem, root string) (bool, bool) {
	for _, name := range konfig.RecognizedKustomizationFileNames() {
		path := filepath.Join(root, name)
		data, err := fileSystem.ReadFile(path)
		if err != nil {
			continue
		}
		var document yaml.Node
		if yaml.Unmarshal(data, &document) != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return false, false
		}
		mapping := document.Content[0]
		for index := 0; index+1 < len(mapping.Content); index += 2 {
			if mapping.Content[index].Value != "buildMetadata" {
				continue
			}
			metadata := mapping.Content[index+1]
			if metadata.Kind != yaml.SequenceNode {
				return false, false
			}
			for _, option := range metadata.Content {
				if option.Value == types.OriginAnnotations {
					return true, false
				}
			}
			metadata.Content = append(metadata.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStringTag, Value: types.OriginAnnotations})
			formatted, err := yaml.Marshal(&document)
			if err != nil || fileSystem.WriteFile(path, formatted) != nil {
				return false, false
			}

			return true, true
		}
		mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStringTag, Value: "buildMetadata"}, &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: yamlStringTag, Value: types.OriginAnnotations}}})
		formatted, err := yaml.Marshal(&document)
		if err != nil || fileSystem.WriteFile(path, formatted) != nil {
			return false, false
		}

		return false, true
	}

	return false, false
}

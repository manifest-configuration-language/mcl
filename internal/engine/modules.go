package engine

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/manifest-configuration-language/mcl/internal/k8sschema"
	"github.com/manifest-configuration-language/mcl/internal/project"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
)

const maxMCLSourceBytes = syntax.MaxSourceBytes

// resolveModuleLinkはモジュールファイルのシンボリックリンクを解決する。解決できない場合はfalseを返す。
func resolveModuleLink(path string) (string, bool) {
	modulePath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}

	return modulePath, true
}

const (
	maxProjectMCLFiles    = project.MaxSourceFiles
	maxProjectMCLBytes    = project.MaxSourceTotal
	projectConfigFileName = project.ConfigFileName
)

// EvaluationOptionsはMCL評価全体に適用する実行オプションを保持します。
type EvaluationOptions struct {
	AllowNondeterministicOutput bool
	// CacheDirectory shares digest-addressed dependencies between project evaluations. It must be absolute and requires an OS-backed project filesystem; empty keeps the project-local cache.
	CacheDirectory        string
	KubernetesVersion     string
	ParameterSources      map[string]ParameterSource
	GitCredentialProvider GitCredentialProvider
}

type GitCredentials struct {
	Username string
	Password string
}

type GitCredentialProvider func(context.Context, string) (GitCredentials, error)

type moduleLoader struct {
	//nolint:containedctx // Contextはモジュール評価とAdapter呼び出しの間で共有します。
	ctx                         context.Context
	root                        string
	files                       *projectFilesystem
	graph                       *moduleGraph
	entry                       string
	entryText                   string
	dependencies                *dependency.Lock
	operation                   string
	allowNondeterministicOutput bool
	kubernetesVersion           string
	parameters                  map[string]jsontext.Value
	parameterSources            map[string]ParameterSource
	overlays                    map[string]string
	loadedModules               map[string]struct{}
	textImports                 map[string]string
	textBytes                   int64
	session                     *EvaluationSession
	projectSession              *ProjectSession
	crdSchemas                  map[customResourceDefinitionKey]crdVersionSchema
}

func runtimeBundleToBundle(runtimeBundle RuntimeBundle) (Bundle, error) {
	bundle := Bundle{Artifacts: make([]Artifact, len(runtimeBundle.Artifacts))}
	for index, artifact := range runtimeBundle.Artifacts {
		value, err := runtimeValueToValue(artifact.Value)
		if err != nil {
			return Bundle{}, fmt.Errorf("emitted value %d: %w", index+1, err)
		}
		bundle.Artifacts[index] = Artifact{Value: value, Lifecycle: artifact.Lifecycle}
	}

	return bundle, nil
}

func EvaluateFile(path string) (Bundle, error) {
	return EvaluateFileContext(context.Background(), path)
}

// EvaluateFileContextは呼び出し元のContextを使ってMCLファイルを評価します。
func EvaluateFileContext(ctx context.Context, path string) (Bundle, error) {
	return EvaluateFileWithParametersContext(ctx, path, nil)
}

// EvaluateFileWithParametersContext evaluates an MCL file with explicit runtime parameters.
func EvaluateFileWithParametersContext(ctx context.Context, path string, parameters map[string]jsontext.Value) (Bundle, error) {
	return EvaluateFileWithParametersAndOptionsContext(ctx, path, parameters, EvaluationOptions{})
}

// EvaluateFileWithOptionsContextは実行オプションを指定してMCLファイルを評価します。
func EvaluateFileWithOptionsContext(ctx context.Context, path string, options EvaluationOptions) (Bundle, error) {
	return EvaluateFileWithParametersAndOptionsContext(ctx, path, nil, options)
}

// EvaluateFileWithParametersAndOptionsContextは実行オプションとruntime parametersを指定してMCLファイルを評価します。
func EvaluateFileWithParametersAndOptionsContext(ctx context.Context, path string, parameters map[string]jsontext.Value, options EvaluationOptions) (Bundle, error) {
	return evaluateFileWithParameters(ctx, path, false, "", parameters, options)
}

// EvaluateFileForOperation は指定した操作向けにMCLファイルを評価します。
func EvaluateFileForOperation(path, operation string) (Bundle, error) {
	return EvaluateFileForOperationContext(context.Background(), path, operation)
}

// EvaluateFileForOperationContextは呼び出し元のContextを使って指定した操作向けにMCLファイルを評価します。
func EvaluateFileForOperationContext(ctx context.Context, path, operation string) (Bundle, error) {
	return EvaluateFileForOperationWithParametersContext(ctx, path, operation, nil)
}

// EvaluateFileForOperationWithParametersContext evaluates an MCL file for an operation with explicit runtime parameters.
func EvaluateFileForOperationWithParametersContext(ctx context.Context, path, operation string, parameters map[string]jsontext.Value) (Bundle, error) {
	if !validOperation(operation) {
		return Bundle{}, fmt.Errorf("unknown operation %q", operation)
	}

	return EvaluateFileForOperationWithParametersAndOptionsContext(ctx, path, operation, parameters, EvaluationOptions{})
}

// EvaluateFileForOperationWithOptionsContextは実行オプションを指定して操作向けにMCLファイルを評価します。
func EvaluateFileForOperationWithOptionsContext(ctx context.Context, path, operation string, options EvaluationOptions) (Bundle, error) {
	return EvaluateFileForOperationWithParametersAndOptionsContext(ctx, path, operation, nil, options)
}

// EvaluateFileForOperationWithParametersAndOptionsContextは実行オプションとruntime parametersを指定して操作向けにMCLファイルを評価します。
func EvaluateFileForOperationWithParametersAndOptionsContext(ctx context.Context, path, operation string, parameters map[string]jsontext.Value, options EvaluationOptions) (Bundle, error) {
	if !validOperation(operation) {
		return Bundle{}, fmt.Errorf("unknown operation %q", operation)
	}

	return evaluateFileWithParameters(ctx, path, false, operation, parameters, options)
}

// EvaluateFileForOperationWithParametersAndOptionsAndReadFilesContext evaluates an MCL file and returns project-relative paths opened during evaluation.
func EvaluateFileForOperationWithParametersAndOptionsAndReadFilesContext(ctx context.Context, path, operation string, parameters map[string]jsontext.Value, options EvaluationOptions) (Bundle, []string, error) {
	if !validOperation(operation) {
		return Bundle{}, nil, fmt.Errorf("unknown operation %q", operation)
	}
	if err := ctx.Err(); err != nil {
		return Bundle{}, nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Bundle{}, nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Bundle{}, nil, err
	}
	root, err := discoverProjectRoot(filepath.Dir(canonical))
	if err != nil {
		return Bundle{}, nil, err
	}
	files := newOSProjectFilesystem(root)
	bundle, err := evaluateFileWithFilesystemMode(ctx, canonical, false, false, operation, parameters, options, files)
	if info, statErr := os.Stat(filepath.Join(root, "mcl.lock")); statErr == nil && info.Mode().IsRegular() {
		files.recordRead("mcl.lock")
	}

	return bundle, files.readPaths(), err
}

func UpdateFile(path string) error {
	return UpdateFileContext(context.Background(), path)
}

// UpdateFileContextは呼び出し元のContextを使って依存ロックを更新します。
func UpdateFileContext(ctx context.Context, path string) error {
	return UpdateFileWithParametersContext(ctx, path, nil)
}

// UpdateFileWithParametersContext updates dependencies after evaluating an MCL file with explicit runtime parameters.
func UpdateFileWithParametersContext(ctx context.Context, path string, parameters map[string]jsontext.Value) error {
	return UpdateFileWithParametersAndOptionsContext(ctx, path, parameters, EvaluationOptions{})
}

// UpdateFileWithParametersAndOptionsContextは実行オプションとruntime parametersを指定して依存lockを更新します。
func UpdateFileWithParametersAndOptionsContext(ctx context.Context, path string, parameters map[string]jsontext.Value, options EvaluationOptions) error {
	_, err := evaluateFileWithParameters(ctx, path, true, "", parameters, options)

	return err
}

// FetchFileWithParametersAndOptionsContextはmcl.lockに記録された依存だけをcacheへ取得します。
func FetchFileWithParametersAndOptionsContext(ctx context.Context, path string, parameters map[string]jsontext.Value, options EvaluationOptions) error {
	_, err := evaluateFileWithParametersMode(ctx, path, false, true, "", parameters, options)

	return err
}

// ResolveImportPath resolves an imported module inside its project root.
func ResolveImportPath(importer, modulePath string) (string, error) {
	return project.ResolveImportPath(importer, modulePath)
}

type DependentMCLSourceIndex = project.DependentMCLSourceIndex

type DependentMCLFileChange = project.DependentMCLFileChange

func DependentMCLSources(target string, workspaceRoots []string, overlays map[string]string) (map[string]string, error) {
	return project.DependentMCLSources(target, workspaceRoots, overlays)
}

func DependentMCLSourcesForProjectFS(projectRoot, targetPath string, files fs.FS) (map[string]string, error) {
	return project.DependentMCLSourcesForProjectFS(projectRoot, targetPath, files)
}

func evaluateFileWithParameters(ctx context.Context, path string, update bool, operation string, parameters map[string]jsontext.Value, options EvaluationOptions) (Bundle, error) {
	return evaluateFileWithParametersMode(ctx, path, update, false, operation, parameters, options)
}

func evaluateFileWithParametersMode(ctx context.Context, path string, update, fetchLocked bool, operation string, parameters map[string]jsontext.Value, options EvaluationOptions) (Bundle, error) {
	return evaluateFileWithFilesystemMode(ctx, path, update, fetchLocked, operation, parameters, options, nil)
}

func evaluateFileWithFilesystem(ctx context.Context, path string, update bool, operation string, parameters map[string]jsontext.Value, options EvaluationOptions, files *projectFilesystem) (Bundle, error) {
	return evaluateFileWithFilesystemMode(ctx, path, update, false, operation, parameters, options, files)
}

func evaluateFileWithFilesystemMode(ctx context.Context, path string, update, fetchLocked bool, operation string, parameters map[string]jsontext.Value, options EvaluationOptions, files *projectFilesystem) (Bundle, error) {
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	version, err := k8sschema.NormalizeKubernetesVersion(options.KubernetesVersion)
	if err != nil {
		return Bundle{}, err
	}
	options.KubernetesVersion = version
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Bundle{}, err
	}
	var canonical string
	root := ""
	if files == nil {
		canonical, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return Bundle{}, err
		}
		root, err = discoverProjectRoot(filepath.Dir(canonical))
		if err != nil {
			return Bundle{}, err
		}
		files = newOSProjectFilesystem(root)
	} else {
		root = files.root
		canonical, err = files.resolveReadPath(absolute)
		if err != nil {
			return Bundle{}, fmt.Errorf("resolve entry source %q: %w", absolute, err)
		}
	}
	source, err := files.readLimitedFile(canonical)
	if err != nil {
		return Bundle{}, fmt.Errorf("read entry source %q: %w", canonical, err)
	}
	var dependencies *dependency.Lock
	if fetchLocked && files.osBacked {
		dependencies, err = dependency.OpenLockedFetch(ctx, root, dependencyToolchainIdentity())
	} else if fetchLocked {
		dependencies, err = dependency.OpenLockedFetchWithFS(ctx, root, dependencyToolchainIdentity(), files.files)
	} else if files.osBacked {
		dependencies, err = dependency.Open(ctx, root, update, dependencyToolchainIdentity())
	} else {
		dependencies, err = dependency.OpenWithFS(ctx, root, update, dependencyToolchainIdentity(), files.files)
	}
	if err != nil {
		return Bundle{}, err
	}
	if options.CacheDirectory != "" {
		if err := dependencies.SetCacheDirectory(options.CacheDirectory); err != nil {
			return Bundle{}, err
		}
	}
	if options.GitCredentialProvider != nil {
		dependencies.SetGitCredentialProvider(func(ctx context.Context, source string) (dependency.GitCredentials, error) {
			credentials, err := options.GitCredentialProvider(ctx, source)

			return dependency.GitCredentials{Username: credentials.Username, Password: credentials.Password}, err
		})
	}
	crdSchemaSources, err := configuredCRDSchemaSources(files)
	if err != nil {
		return Bundle{}, err
	}
	loader := moduleLoader{
		ctx:  ctx,
		root: root, files: files, entry: canonical, entryText: string(source),
		dependencies: dependencies, operation: operation, parameters: maps.Clone(parameters),
		allowNondeterministicOutput: options.AllowNondeterministicOutput, parameterSources: cloneParameterSources(options.ParameterSources), loadedModules: make(map[string]struct{}), session: &EvaluationSession{Budget: &functionBudget{}},
		kubernetesVersion: options.KubernetesVersion,
		projectSession:    newProjectSession(),
	}
	_, runtimeBundle, state, err := loader.evaluateRuntime(canonical, true) //nolint:contextcheck // moduleLoaderが評価全体のContextを保持します。
	if err != nil {
		return Bundle{}, err
	}
	crdSchemas, err := loadCRDSchemaCatalogForRuntimeBundle(ctx, dependencies, crdSchemaSources, runtimeBundle, update)
	if err != nil {
		return Bundle{}, err
	}
	loader.crdSchemas = crdSchemas
	if state.Module != nil {
		state.Module.CRDSchemas = crdSchemas
	}
	if err := resolveRuntimeBundle(runtimeBundle, state); err != nil { //nolint:contextcheck // EvaluationState経由で評価ContextをAdapterへ渡します。
		return Bundle{}, err
	}
	bundle, err := runtimeBundleToBundle(*runtimeBundle)
	if err != nil {
		return Bundle{}, err
	}
	if state.Module != nil {
		bundle.resourceDescriptors, err = kubernetesResourceDescriptorsFromCRDSchemas(state.Module.CRDSchemas)
		if err != nil {
			return Bundle{}, err
		}
	}
	if update {
		if err := dependencies.Save(); err != nil {
			return Bundle{}, err
		}
	}

	return bundle, nil
}

func discoverProjectRoot(directory string) (string, error) {
	return project.DiscoverRoot(directory)
}

func EvaluateSource(path, source string) (Bundle, error) {
	return EvaluateSourceContext(context.Background(), path, source)
}

// EvaluateSourceContextは呼び出し元のContextを使ってソーステキストを評価します。
func EvaluateSourceContext(ctx context.Context, path, source string) (Bundle, error) {
	return EvaluateSourceWithOverlaysContext(ctx, path, source, nil)
}

// EvaluateSourceWithOverlays は保存前のモジュール内容を使ってソースを評価します。
func EvaluateSourceWithOverlays(path, source string, overlays map[string]string) (Bundle, error) {
	return EvaluateSourceWithOverlaysContext(context.Background(), path, source, overlays)
}

// EvaluateSourceWithOverlaysContextは呼び出し元のContextを使ってソースとオーバーレイを評価します。
func EvaluateSourceWithOverlaysContext(ctx context.Context, path, source string, overlays map[string]string) (Bundle, error) {
	bundle, _, err := EvaluateSourceWithOverlaysAndDependenciesContext(ctx, path, source, overlays)

	return bundle, err
}

// EvaluateSourceWithOverlaysAndDependencies は評価結果と読み込んだモジュールのパスを返します。
func EvaluateSourceWithOverlaysAndDependencies(path, source string, overlays map[string]string) (Bundle, []string, error) {
	return EvaluateSourceWithOverlaysAndDependenciesContext(context.Background(), path, source, overlays)
}

// EvaluateSourceWithOverlaysAndDependenciesContextは呼び出し元のContextを使ってソースを評価し、読み込んだモジュールのパスを返します。
func EvaluateSourceWithOverlaysAndDependenciesContext(ctx context.Context, path, source string, overlays map[string]string) (Bundle, []string, error) {
	return evaluateSourceWithFilesystem(ctx, path, source, overlays, nil)
}

// EvaluateProjectSourceWithParametersContext evaluates source from a project filesystem with explicit runtime parameters.
func EvaluateProjectSourceWithParametersContext(ctx context.Context, projectRoot, entryPath, source string, files fs.FS, overlays map[string]string, parameters map[string]jsontext.Value) (Bundle, error) {
	if ctx == nil {
		return Bundle{}, errors.New("evaluation context is required")
	}
	if files == nil {
		return Bundle{}, errors.New("project filesystem is required")
	}
	if !filepath.IsAbs(projectRoot) {
		return Bundle{}, fmt.Errorf("project root %q must be absolute", projectRoot)
	}
	if !validProjectPath(entryPath) {
		return Bundle{}, fmt.Errorf("invalid project entry path %q", entryPath)
	}
	root := filepath.Clean(projectRoot)
	entry := filepath.Join(root, filepath.FromSlash(entryPath))
	absoluteOverlays := make(map[string]string, len(overlays))
	for overlayPath, overlaySource := range overlays {
		if !validProjectPath(overlayPath) {
			return Bundle{}, fmt.Errorf("invalid project overlay path %q", overlayPath)
		}
		absoluteOverlays[filepath.Join(root, filepath.FromSlash(overlayPath))] = overlaySource
	}
	bundle, _, err := evaluateSourceWithFilesystemAndParameters(ctx, entry, source, absoluteOverlays, newProjectFilesystem(root, files), parameters, EvaluationOptions{})

	return bundle, err
}

func evaluateSourceWithFilesystem(ctx context.Context, path, source string, overlays map[string]string, files *projectFilesystem) (Bundle, []string, error) {
	return evaluateSourceWithFilesystemAndParameters(ctx, path, source, overlays, files, nil, EvaluationOptions{})
}

func evaluateSourceWithFilesystemAndParameters(ctx context.Context, path, source string, overlays map[string]string, files *projectFilesystem, parameters map[string]jsontext.Value, options EvaluationOptions) (Bundle, []string, error) {
	if err := ctx.Err(); err != nil {
		return Bundle{}, nil, err
	}
	version, err := k8sschema.NormalizeKubernetesVersion(options.KubernetesVersion)
	if err != nil {
		return Bundle{}, nil, err
	}
	options.KubernetesVersion = version
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Bundle{}, nil, err
	}
	root := ""
	if files == nil {
		root, err = discoverProjectRoot(filepath.Dir(absolute))
		if err != nil {
			return Bundle{}, nil, err
		}
		files = newOSProjectFilesystem(root)
	} else {
		root = files.root
	}
	dependencies, err := dependency.OpenWithFS(ctx, root, false, dependencyToolchainIdentity(), files.files)
	if err != nil {
		return Bundle{}, nil, err
	}
	entry := absolute
	if files.osBacked {
		if canonical, err := filepath.EvalSymlinks(absolute); err == nil {
			entry = canonical
		}
	} else if _, err := files.relativePath(absolute); err != nil {
		return Bundle{}, nil, fmt.Errorf("resolve entry source %q: %w", absolute, err)
	}
	crdSchemaSources, err := configuredCRDSchemaSources(files)
	if err != nil {
		return Bundle{}, nil, err
	}
	canonicalOverlays := make(map[string]string, len(overlays))
	for overlayPath, overlaySource := range overlays {
		absoluteOverlay, err := filepath.Abs(overlayPath)
		if err != nil {
			return Bundle{}, nil, err
		}
		canonicalOverlay := absoluteOverlay
		if files.osBacked {
			if resolved, err := filepath.EvalSymlinks(absoluteOverlay); err == nil {
				canonicalOverlay = resolved
			}
		}
		relative, err := filepath.Rel(root, canonicalOverlay)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		canonicalOverlays[canonicalOverlay] = overlaySource
	}
	loader := moduleLoader{
		ctx:                         ctx,
		root:                        root,
		files:                       files,
		entry:                       entry,
		entryText:                   source,
		parameters:                  maps.Clone(parameters),
		dependencies:                dependencies,
		allowNondeterministicOutput: options.AllowNondeterministicOutput,
		kubernetesVersion:           options.KubernetesVersion,
		parameterSources:            cloneParameterSources(options.ParameterSources),
		overlays:                    canonicalOverlays,
		loadedModules:               make(map[string]struct{}),
		session:                     &EvaluationSession{Budget: &functionBudget{}, Allocations: &runtimeAllocationBudget{}},
		projectSession:              newProjectSession(),
	}
	_, runtimeBundle, state, err := loader.evaluateRuntime(entry, true) //nolint:contextcheck // moduleLoaderが評価全体のContextを保持します。
	loadedModules := loader.loadedModulePaths()
	if err != nil {
		return Bundle{}, loadedModules, err
	}
	crdSchemas, err := loadCRDSchemaCatalogForRuntimeBundle(ctx, dependencies, crdSchemaSources, runtimeBundle, false)
	if err != nil {
		return Bundle{}, loadedModules, err
	}
	loader.crdSchemas = crdSchemas
	if state.Module != nil {
		state.Module.CRDSchemas = crdSchemas
	}
	if err := resolveRuntimeBundle(runtimeBundle, state); err != nil { //nolint:contextcheck // EvaluationState経由で評価ContextをAdapterへ渡します。
		return Bundle{}, loadedModules, err
	}
	bundle, err := runtimeBundleToBundle(*runtimeBundle)
	if err != nil {
		return Bundle{}, loadedModules, err
	}
	if state.Module != nil {
		bundle.resourceDescriptors, err = kubernetesResourceDescriptorsFromCRDSchemas(state.Module.CRDSchemas)
		if err != nil {
			return Bundle{}, loadedModules, err
		}
	}

	return bundle, loadedModules, nil
}

func (loader *moduleLoader) evaluateRuntime(path string, entry bool) ([]RuntimeEntry, *RuntimeBundle, *EvaluationState, error) {
	if loader.ctx == nil {
		loader.ctx = context.Background()
	}
	if loader.files == nil {
		loader.files = newOSProjectFilesystem(loader.root)
	}
	if loader.projectSession == nil {
		loader.projectSession = newProjectSession()
	}
	if err := loader.ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	if loader.session == nil {
		loader.session = &EvaluationSession{}
	}
	if loader.session.Budget == nil {
		loader.session.Budget = &functionBudget{}
	}
	if loader.session.Allocations == nil {
		loader.session.Allocations = &runtimeAllocationBudget{}
	}
	loader.loadedModules[path] = struct{}{}
	if !entry {
		if exports, cached := loader.projectSession.runtimeCache[path]; cached {
			state := &EvaluationState{
				EvaluationSession: loader.session,
				Types:             maps.Clone(loader.projectSession.runtimeTypes[path]),
			}

			return exports, &RuntimeBundle{}, state, nil
		}
	}
	if loader.graph == nil {
		loader.graph = &moduleGraph{root: loader.root, files: loader.files}
	}
	if err := loader.graph.enter(path); err != nil {
		return nil, nil, nil, err
	}
	defer loader.graph.leave(path)
	source := loader.entryText
	if path != loader.entry {
		if overlaySource, exists := loader.overlays[path]; exists {
			source = overlaySource
		} else {
			data, err := loader.files.readLimitedFile(path)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("read module %q: %w", path, err)
			}
			source = string(data)
		}
	}
	module, err := loader.graph.parse(path, source)
	if err != nil {
		return nil, nil, nil, err
	}
	setModuleASTSourcePath(module, path)
	types, err := collectTypeDefinitionsAST(module)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := loadTypeSchemaFilesWithFilesystem(types, loader.root, path, loader.dependencies, loader.files, loader.projectSession.analysis); err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	moduleContext := &ModuleContext{Context: loader.ctx, ProjectRoot: loader.root, filesystem: loader.files, Dependencies: loader.dependencies, HelmOperation: loader.operation, AllowNondeterministicOutput: loader.allowNondeterministicOutput, KubernetesVersion: loader.kubernetesVersion, Analysis: loader.projectSession.analysis, CRDSchemas: loader.crdSchemas}
	state := &EvaluationState{
		EvaluationSession: loader.session,
		Environment:       make(map[string]RuntimeValue), StaticTypes: make(map[string]TypeAnalysis), Types: types,
		Stack: make(map[string]bool), SourcePath: path, Module: moduleContext,
	}
	if entry {
		state.Parameters = maps.Clone(loader.parameters)
		state.ParameterSources = loader.parameterSources
	}
	for _, node := range module.Nodes {
		declaration, ok := node.(*ImportDeclaration)
		if !ok {
			continue
		}
		if _, exists := state.Environment[declaration.Name]; exists {
			return nil, nil, nil, fmt.Errorf("%s:%d: %q is already defined", path, declaration.Line, declaration.Name)
		}
		var imported RuntimeValue
		var entries []RuntimeEntry
		if declaration.Text {
			dependency, err := loader.resolveImport(path, declaration.ModulePath)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s:%d: %w", path, declaration.Line, err)
			}
			text, err := loader.readTextImport(dependency)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s:%d: read imported text %q: %w", path, declaration.Line, declaration.ModulePath, err)
			}
			imported = RuntimeValue{Value: runtimeValueData{Kind: String, Text: text}}
		} else if strings.HasPrefix(declaration.ModulePath, "std:") {
			var exists bool
			entries, exists = standardRuntimeLibrary(declaration.ModulePath)
			if !exists {
				return nil, nil, nil, fmt.Errorf("%s:%d: standard library module %q is not available", path, declaration.Line, declaration.ModulePath)
			}
			for index := range entries {
				if entries[index].Value.Kind() == Function {
					entries[index].Value.Value.SourcePath = path
					entries[index].Value.ModuleContext = moduleContext
				}
			}
		} else {
			dependency, err := loader.resolveImport(path, declaration.ModulePath)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s:%d: %w", path, declaration.Line, err)
			}
			var dependencyBundle *RuntimeBundle
			entries, dependencyBundle, _, err = loader.evaluateRuntime(dependency, false)
			if err != nil {
				return nil, nil, nil, err
			}
			if dependencyBundle != nil && len(dependencyBundle.Artifacts) != 0 {
				return nil, nil, nil, fmt.Errorf("imported module %q emitted values", dependency)
			}
			for name, typeExpr := range loader.projectSession.runtimeTypes[dependency] {
				state.Types[declaration.Name+"."+name] = typeExpr
			}
		}
		if err := loader.session.Allocations.consume(len(entries) + 1); err != nil {
			return nil, nil, nil, sourceDiagnostic(path, declaration.Span, err)
		}
		if !declaration.Text {
			imported = RuntimeValue{Value: runtimeValueData{Kind: Map}, Map: entries}
		}
		state.Environment[declaration.Name] = imported
		if inferred, known := inferRuntimeValueStaticType(imported); known {
			state.StaticTypes[declaration.Name] = inferred
		}
	}
	if err := validateTypeDefinitionsAST(module, state.Types); err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	declarationValues := 0
	for _, node := range module.Nodes {
		switch node.(type) {
		case *FunctionDeclaration, *RuleDeclaration:
			declarationValues++
		}
	}
	if err := loader.session.Allocations.consume(declarationValues); err != nil {
		return nil, nil, nil, sourceDiagnostic(path, SourceSpan{}, err)
	}
	exports, bundle, err := evaluateModuleASTRuntime(module, state, entry)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if bundle == nil {
		bundle = &RuntimeBundle{}
	}
	if !entry {
		loader.projectSession.runtimeCache[path] = slices.Clone(exports)
		exportedTypes := make(map[string]ResolvedType)
		typeScope := maps.Clone(state.Types)
		scopeID := TypeScopeID(path)
		state.Module.Analysis.registerScope(scopeID, typeScope)
		for _, node := range module.Nodes {
			declaration, ok := node.(*TypeDeclaration)
			if ok && declaration.Exported {
				typeExpr := state.Types[declaration.Name]
				typeExpr.ScopeID = scopeID
				typeExpr = expandStaticTypeAliases(typeExpr, state.Types, state.Module.Analysis, make(map[string]bool))
				exportedTypes[declaration.Name] = typeExpr
			}
		}
		loader.projectSession.runtimeTypes[path] = exportedTypes
	}

	return exports, bundle, state, nil
}

func (loader *moduleLoader) loadedModulePaths() []string {
	paths := make([]string, 0, len(loader.loadedModules))
	for path := range loader.loadedModules {
		paths = append(paths, path)
	}
	slices.Sort(paths)

	return paths
}

func (loader *moduleLoader) resolveImport(importer, modulePath string) (string, error) {
	if loader.graph == nil {
		loader.graph = &moduleGraph{root: loader.root, files: loader.files}
	}

	return loader.graph.resolveImport(importer, modulePath)
}

func (loader *moduleLoader) readTextImport(path string) (string, error) {
	if text, exists := loader.textImports[path]; exists {
		return text, nil
	}
	if len(loader.textImports) >= maxProjectMCLFiles {
		return "", fmt.Errorf("project imports more than %d text files", maxProjectMCLFiles)
	}
	data, err := loader.files.readLimitedFile(path)
	if err != nil {
		return "", err
	}
	if loader.textBytes+int64(len(data)) > maxProjectMCLBytes {
		return "", fmt.Errorf("project imported text exceeds the %d-byte limit", maxProjectMCLBytes)
	}
	if !utf8.Valid(data) {
		return "", errors.New("imported text is not valid UTF-8")
	}
	text := string(data)
	if loader.textImports == nil {
		loader.textImports = make(map[string]string)
	}
	loader.textImports[path] = text
	loader.textBytes += int64(len(data))

	return text, nil
}

func setModuleASTSourcePath(module ModuleAST, path string) {
	for _, node := range module.Nodes {
		switch declaration := node.(type) {
		case *ImportDeclaration:
			declaration.SourcePath = path
		case *TypeDeclaration:
			declaration.SourcePath = path
		case *RuleDeclaration:
			declaration.SourcePath = path
			if declaration.Rule != nil {
				for index := range declaration.Rule.Requirements {
					declaration.Rule.Requirements[index].SourcePath = path
				}
			}
		case *FunctionDeclaration:
			declaration.SourcePath = path
		case *LetDeclaration:
			declaration.SourcePath = path
		case *EmitStatement:
			declaration.SourcePath = path
		case *ValidateStatement:
			declaration.SourcePath = path
		}
	}
}

package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/manifest-configuration-language/mcl/internal/helmrender"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/kustomize/kyaml/openapi"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

func SchemaFieldCompletions(path, source, placeholder string) []string {
	fields, _ := SchemaFieldCompletionsWithError(path, source, placeholder)

	return fields
}

// SchemaFieldCompletionsWithErrorは型定義とSchema依存だけを解析し、候補または解析エラーを返します。
func SchemaFieldCompletionsWithError(path, source, placeholder string) ([]string, error) {
	return SchemaFieldCompletionsWithContext(context.Background(), path, source, placeholder)
}

// SchemaFieldCompletionsWithContextは呼び出し元のcontextを使ってSchemaフィールド候補を解析します。
func SchemaFieldCompletionsWithContext(ctx context.Context, path, source, placeholder string) ([]string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	modulePath, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		modulePath = absolute
	}
	root, err := discoverProjectRoot(filepath.Dir(modulePath))
	if err != nil {
		return nil, err
	}
	files := newOSProjectFilesystem(root)
	dependencies, err := schemaCompletionDependenciesWithContext(ctx, root, files)
	if err != nil {
		return nil, err
	}

	return schemaFieldCompletionsWithFilesystem(modulePath, source, placeholder, root, files, dependencies)
}

// SchemaFieldCompletionsWithErrorForProjectFSはfs.FS上のMCLファイルとSchemaからフィールド補完候補を返します。
func SchemaFieldCompletionsWithErrorForProjectFS(projectRoot, entryPath, source, placeholder string, projectFiles fs.FS) ([]string, error) {
	root, modulePath, files, dependencies, err := schemaCompletionProjectFS(projectRoot, entryPath, projectFiles)
	if err != nil {
		return nil, err
	}

	return schemaFieldCompletionsWithFilesystem(modulePath, source, placeholder, root, files, dependencies)
}

func schemaFieldCompletionsWithFilesystem(modulePath, source, placeholder, root string, files *projectFilesystem, dependencies *dependency.Lock) ([]string, error) {
	module, err := ParseAST(source)
	if err != nil {
		return nil, err
	}
	loader := newSchemaCompletionLoader(root, modulePath, source, files, dependencies)
	types, err := loader.analyze(modulePath, true)
	if err != nil {
		return nil, err
	}
	for _, node := range module.Nodes {
		declaration, ok := node.(*LetDeclaration)
		if !ok || declaration.Annotation == nil {
			continue
		}
		fieldPath, existing, found := locateCompletionMap(declaration.Expression, nil, placeholder)
		if !found {
			continue
		}
		typeExpr := semanticTypeFromSyntax(*declaration.Annotation)
		if err := loadSchemaTypesWithFilesystem(&typeExpr, root, modulePath, dependencies, files, loader.analysis); err != nil {
			return nil, err
		}
		schema := schemaForType(typeExpr, types, make(map[string]bool), loader.analysis)
		for _, field := range fieldPath {
			schema = schemaForProperty(schema, field, make(map[*jsonschema.Schema]bool))
			if schema == nil {
				break
			}
		}
		if schema == nil {
			continue
		}
		properties := make(map[string]struct{})
		collectSchemaProperties(schema, properties, make(map[*jsonschema.Schema]bool))
		for _, field := range existing {
			delete(properties, field)
		}
		completions := make([]string, 0, len(properties))
		for field := range properties {
			completions = append(completions, field)
		}
		slices.Sort(completions)

		return completions, nil
	}

	return nil, nil
}

// SchemaTypeFieldsは型注釈とネストしたフィールド経路に対応するSchemaのプロパティ名を返します。
func SchemaTypeFields(path, source, annotation string, fieldPath []string) ([]string, error) {
	return SchemaTypeFieldsWithContext(context.Background(), path, source, annotation, fieldPath)
}

// SchemaTypeFieldsWithContextは呼び出し元のcontextを使ってSchemaプロパティ名を返します。
func SchemaTypeFieldsWithContext(ctx context.Context, path, source, annotation string, fieldPath []string) ([]string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	modulePath, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		modulePath = absolute
	}
	root, err := discoverProjectRoot(filepath.Dir(modulePath))
	if err != nil {
		return nil, err
	}
	files := newOSProjectFilesystem(root)
	dependencies, err := schemaCompletionDependenciesWithContext(ctx, root, files)
	if err != nil {
		return nil, err
	}

	return schemaTypeFieldsWithFilesystem(modulePath, source, annotation, fieldPath, root, files, dependencies)
}

// SchemaTypeFieldsForProjectFSはfs.FS上のMCLファイルとSchemaから型のフィールド候補を返します。
func SchemaTypeFieldsForProjectFS(projectRoot, entryPath, source, annotation string, fieldPath []string, projectFiles fs.FS) ([]string, error) {
	root, modulePath, files, dependencies, err := schemaCompletionProjectFS(projectRoot, entryPath, projectFiles)
	if err != nil {
		return nil, err
	}

	return schemaTypeFieldsWithFilesystem(modulePath, source, annotation, fieldPath, root, files, dependencies)
}

func schemaTypeFieldsWithFilesystem(modulePath, source, annotation string, fieldPath []string, root string, files *projectFilesystem, dependencies *dependency.Lock) ([]string, error) {
	probeSource := "let __mcl_schema_type_probe: " + annotation + " = {}\n"
	module, err := ParseAST(probeSource)
	if err != nil {
		return nil, err
	}
	loader := newSchemaCompletionLoader(root, modulePath, source, files, dependencies)
	types, err := loader.analyze(modulePath, true)
	if err != nil {
		return nil, err
	}
	declaration := module.Nodes[0].(*LetDeclaration)
	if declaration.Annotation == nil {
		return nil, errors.New("schema type probe has no annotation")
	}
	typeExpr := semanticTypeFromSyntax(*declaration.Annotation)
	if err := loadSchemaTypesWithFilesystem(&typeExpr, root, modulePath, dependencies, files, loader.analysis); err != nil {
		return nil, err
	}
	schema := schemaForType(typeExpr, types, make(map[string]bool), loader.analysis)
	for _, field := range fieldPath {
		if schema != nil {
			schema = schemaForProperty(schema, field, make(map[*jsonschema.Schema]bool))

			continue
		}
		resolved, known := resolveStaticAlias(typeExpr, types, make(map[string]bool), loader.analysis)
		if !known {
			return nil, nil
		}
		next, known, found, _ := staticField(resolved, field, types, make(map[string]bool))
		if !known || !found {
			return nil, nil
		}
		typeExpr = next
	}
	if schema != nil {
		properties := make(map[string]struct{})
		collectSchemaProperties(schema, properties, make(map[*jsonschema.Schema]bool))
		fields := make([]string, 0, len(properties))
		for field := range properties {
			fields = append(fields, field)
		}
		slices.Sort(fields)

		return fields, nil
	}
	resolved, known := resolveStaticAlias(typeExpr, types, make(map[string]bool), loader.analysis)
	if !known {
		return nil, nil
	}
	fields := make([]string, 0, len(resolved.Fields))
	for _, field := range resolved.Fields {
		fields = append(fields, field.Name)
	}
	slices.Sort(fields)

	return fields, nil
}

func schemaCompletionProjectFS(projectRoot, entryPath string, projectFiles fs.FS) (string, string, *projectFilesystem, *dependency.Lock, error) {
	if projectFiles == nil {
		return "", "", nil, nil, errors.New("project filesystem is required")
	}
	if !filepath.IsAbs(projectRoot) {
		return "", "", nil, nil, fmt.Errorf("project root %q must be absolute", projectRoot)
	}
	if !validProjectPath(entryPath) {
		return "", "", nil, nil, fmt.Errorf("invalid project entry path %q", entryPath)
	}
	root := filepath.Clean(projectRoot)
	files := newProjectFilesystem(root, projectFiles)
	modulePath := filepath.Join(root, filepath.FromSlash(entryPath))
	dependencies, err := schemaCompletionDependencies(root, files)
	if err != nil {
		return "", "", nil, nil, err
	}

	return root, modulePath, files, dependencies, nil
}

func schemaCompletionDependencies(root string, files *projectFilesystem) (*dependency.Lock, error) {
	return schemaCompletionDependenciesWithContext(context.Background(), root, files)
}

func schemaCompletionDependenciesWithContext(ctx context.Context, root string, files *projectFilesystem) (*dependency.Lock, error) {
	if files.osBacked {
		return dependency.Open(ctx, root, false, dependencyToolchainIdentity())
	}

	return dependency.OpenWithFS(ctx, root, false, dependencyToolchainIdentity(), files.files)
}

func newSchemaCompletionLoader(root, modulePath, source string, files *projectFilesystem, dependencies *dependency.Lock) schemaAnalysisLoader {
	return schemaAnalysisLoader{
		root: root, files: files, entry: modulePath, entrySource: source, dependencies: dependencies,
		cache: make(map[string]map[string]ResolvedType), active: make(map[string]bool), analysis: &TypeAnalysisContext{},
	}
}

func KubernetesSchemaFieldCompletions(source, placeholder string) []string {
	module, err := ParseAST(source)
	if err != nil {
		return nil
	}
	customResources := customResourceCompletionSchemas(module)
	for _, node := range module.Nodes {
		if expression := completionExpression(node); expression != nil {
			if fields, found := kubernetesResourceFieldCompletions(expression, placeholder, customResources); found {
				return fields
			}
		}
	}

	return nil
}

func kubernetesResourceFieldCompletions(expression Expr, placeholder string, customResources map[customResourceCompletionKey]ResolvedType) ([]string, bool) {
	switch node := expression.(type) {
	case *NodeExpr:
		return kubernetesResourceFieldCompletions(node.Body, placeholder, customResources)
	case *MapExpr:
		apiVersion, hasAPIVersion := literalMapString(expression, "apiVersion")
		kind, hasKind := literalMapString(expression, "kind")
		if hasAPIVersion && hasKind {
			fieldPath, existing, found := locateKubernetesCompletionMap(expression, nil, placeholder)
			if found {
				properties, known := kubernetesCompletionSchemaProperties(apiVersion, kind, fieldPath, customResources)
				if !known {
					return nil, true
				}
				fields := make([]string, 0, len(properties))
				for name := range properties {
					if !slices.Contains(existing, name) {
						fields = append(fields, name)
					}
				}
				slices.Sort(fields)

				return fields, true
			}
		}
		for _, entry := range node.Entries {
			if fields, found := kubernetesResourceFieldCompletions(entry.Value, placeholder, customResources); found {
				return fields, true
			}
		}
	case *ListExpr:
		for _, item := range node.Items {
			if fields, found := kubernetesResourceFieldCompletions(item, placeholder, customResources); found {
				return fields, true
			}
		}
	}

	return nil, false
}

type customResourceCompletionKey struct {
	apiVersion string
	kind       string
}

func kubernetesCompletionSchemaProperties(apiVersion, kind string, fieldPath []string, customResources map[customResourceCompletionKey]ResolvedType) (map[string]struct{}, bool) {
	if customSchema, exists := customResources[customResourceCompletionKey{apiVersion: apiVersion, kind: kind}]; exists {
		for _, field := range fieldPath {
			var found bool
			customSchema, found = completionTypeField(customSchema, field)
			if !found {
				return nil, false
			}
		}
		properties := make(map[string]struct{}, len(customSchema.Fields))
		for _, field := range customSchema.Fields {
			properties[field.Name] = struct{}{}
		}

		return properties, true
	}
	resourceSchema := openapi.SchemaForResourceType(yaml.TypeMeta{APIVersion: apiVersion, Kind: kind})
	if resourceSchema == nil {
		return nil, false
	}
	resourceSchema = resourceSchema.Lookup(fieldPath...)
	if resourceSchema == nil || resourceSchema.Schema == nil {
		return nil, false
	}
	properties := make(map[string]struct{}, len(resourceSchema.Schema.Properties))
	for name := range resourceSchema.Schema.Properties {
		properties[name] = struct{}{}
	}

	return properties, true
}

func completionTypeField(schema ResolvedType, field string) (ResolvedType, bool) {
	if field == openapi.Elements {
		if schema.Element == nil {
			return ResolvedType{}, false
		}

		return *schema.Element, true
	}
	fieldType, _, found, _ := staticField(schema, field, nil, make(map[string]bool))
	if !found {
		return ResolvedType{}, false
	}

	return fieldType, true
}

func customResourceCompletionSchemas(module ModuleAST) map[customResourceCompletionKey]ResolvedType {
	schemas := make(map[customResourceCompletionKey]ResolvedType)
	for _, node := range module.Nodes {
		expression := completionExpression(node)
		collectCustomResourceCompletionSchemas(expression, schemas)
	}

	return schemas
}

func collectCustomResourceCompletionSchemas(expression Expr, schemas map[customResourceCompletionKey]ResolvedType) {
	switch node := expression.(type) {
	case *NodeExpr:
		collectCustomResourceCompletionSchemas(node.Body, schemas)
	case *MapExpr:
		if kind, ok := literalMapString(expression, "kind"); ok && kind == kindCustomResourceDefinition {
			if value, ok := completionLiteralJSON(expression); ok {
				if crd, ok := value.(map[string]any); ok {
					maps.Copy(schemas, completionSchemasFromCRD(crd))
				}
			}
		}
		for _, entry := range node.Entries {
			collectCustomResourceCompletionSchemas(entry.Value, schemas)
		}
	case *ListExpr:
		for _, item := range node.Items {
			collectCustomResourceCompletionSchemas(item, schemas)
		}
	}
}

func completionSchemasFromCRD(crd map[string]any) map[customResourceCompletionKey]ResolvedType {
	result := make(map[customResourceCompletionKey]ResolvedType)
	spec, ok := crd["spec"].(map[string]any)
	if !ok {
		return result
	}
	group, _ := spec["group"].(string)
	names, _ := spec["names"].(map[string]any)
	kind, _ := names["kind"].(string)
	versions, _ := spec["versions"].([]any)
	if group == "" || kind == "" {
		return result
	}
	for _, rawVersion := range versions {
		version, ok := rawVersion.(map[string]any)
		if !ok || version["served"] != true {
			continue
		}
		name, _ := version["name"].(string)
		versionSchema, _ := version["schema"].(map[string]any)
		openAPISchema, ok := versionSchema["openAPIV3Schema"].(map[string]any)
		if name == "" || !ok {
			continue
		}
		_, structural, _, err := compileCRDValidatorJSON(openAPISchema)
		if err != nil {
			continue
		}
		result[customResourceCompletionKey{apiVersion: group + "/" + name, kind: kind}] = structuralSchemaType(structural, 0)
	}

	return result
}

func completionLiteralJSON(expression Expr) (any, bool) {
	switch node := expression.(type) {
	case *NullExpr:
		return nil, true
	case *BoolExpr:
		return node.Value, true
	case *IntegerExpr:
		value, err := strconv.ParseInt(node.Value, 10, 64)

		return value, err == nil
	case *DecimalExpr:
		value, err := strconv.ParseFloat(node.Value, 64)

		return value, err == nil && !math.IsInf(value, 0) && !math.IsNaN(value)
	case *StringExpr:
		return node.Value, true
	case *EmptyMapExpr:
		return map[string]any{}, true
	case *EmptyListExpr:
		return []any{}, true
	case *ListExpr:
		items := make([]any, 0, len(node.Items))
		for _, item := range node.Items {
			value, ok := completionLiteralJSON(item)
			if !ok {
				return nil, false
			}
			items = append(items, value)
		}

		return items, true
	case *MapExpr:
		entries := make(map[string]any, len(node.Entries))
		for _, entry := range node.Entries {
			if entry.KeyExpr != nil {
				return nil, false
			}
			value, ok := completionLiteralJSON(entry.Value)
			if !ok {
				return nil, false
			}
			entries[entry.Key] = value
		}

		return entries, true
	default:
		return nil, false
	}
}

func locateKubernetesCompletionMap(expression Expr, path []string, placeholder string) ([]string, []string, bool) {
	switch node := expression.(type) {
	case *NodeExpr:
		return locateKubernetesCompletionMap(node.Body, path, placeholder)
	case *MapExpr:
		existing := make([]string, 0, len(node.Entries))
		for _, entry := range node.Entries {
			if entry.Key == placeholder {
				return slices.Clone(path), existing, true
			}
			existing = append(existing, entry.Key)
		}
		for _, entry := range node.Entries {
			if fieldPath, fields, found := locateKubernetesCompletionMap(entry.Value, append(slices.Clone(path), entry.Key), placeholder); found {
				return fieldPath, fields, true
			}
		}
	case *ListExpr:
		for _, item := range node.Items {
			if fieldPath, fields, found := locateKubernetesCompletionMap(item, append(slices.Clone(path), openapi.Elements), placeholder); found {
				return fieldPath, fields, true
			}
		}
	}

	return nil, nil, false
}

func literalMapString(expression Expr, key string) (string, bool) {
	node, ok := expression.(*MapExpr)
	if !ok {
		return "", false
	}
	for _, entry := range node.Entries {
		if entry.Key == key {
			if value, ok := entry.Value.(*StringExpr); ok {
				return value.Value, true
			}
		}
	}

	return "", false
}

func HelmSchemaFieldCompletions(path, source, placeholder string) []string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	modulePath, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		modulePath = absolute
	}
	root, err := discoverProjectRoot(filepath.Dir(modulePath))
	if err != nil {
		return nil
	}
	files := newOSProjectFilesystem(root)
	dependencies, err := schemaCompletionDependencies(root, files)
	if err != nil {
		return nil
	}

	return helmSchemaFieldCompletionsWithFilesystem(modulePath, source, placeholder, root, files, dependencies)
}

// HelmSchemaFieldCompletionsForProjectFSはfs.FS上のHelm chartからvaluesの補完候補を返します。
func HelmSchemaFieldCompletionsForProjectFS(projectRoot, entryPath, source, placeholder string, projectFiles fs.FS) []string {
	root, modulePath, files, dependencies, err := schemaCompletionProjectFS(projectRoot, entryPath, projectFiles)
	if err != nil {
		return nil
	}

	return helmSchemaFieldCompletionsWithFilesystem(modulePath, source, placeholder, root, files, dependencies)
}

func helmSchemaFieldCompletionsWithFilesystem(modulePath, source, placeholder, root string, files *projectFilesystem, dependencies *dependency.Lock) []string {
	module, err := ParseAST(source)
	if err != nil {
		return nil
	}
	helmAliases := make(map[string]bool)
	definitions := make(map[string]Expr)
	for _, node := range module.Nodes {
		switch declaration := node.(type) {
		case *ImportDeclaration:
			if declaration.ModulePath == "std:helm" {
				helmAliases[declaration.Name] = true
			}
		case *LetDeclaration:
			definitions[declaration.Name] = declaration.Expression
		}
	}
	chartSource, chartVersion, fieldPath, existing, found := findHelmCompletionTarget(module, helmAliases, definitions, placeholder)
	if !found || chartSource == "" {
		return nil
	}
	var schema *jsonschema.Schema
	if strings.HasPrefix(chartSource, "oci://") || strings.HasPrefix(chartSource, "https://") {
		if chartVersion == "" {
			return nil
		}
		schema, err = compileLockedHelmChartSchema(dependencies, chartSource, chartVersion)
	} else {
		if strings.Contains(chartSource, "://") || filepath.IsAbs(chartSource) {
			return nil
		}
		chartPath, resolveErr := files.resolveReadPath(filepath.Join(filepath.Dir(modulePath), filepath.FromSlash(chartSource)))
		if resolveErr != nil || !pathWithin(root, chartPath) {
			return nil
		}
		schemaPath, resolveErr := files.resolveReadPath(filepath.Join(chartPath, "values.schema.json"))
		if resolveErr != nil || !pathWithin(root, schemaPath) {
			return nil
		}
		relativeSchema, resolveErr := filepath.Rel(filepath.Dir(modulePath), schemaPath)
		if resolveErr != nil {
			return nil
		}
		schema, err = compileSchema(filepath.ToSlash(relativeSchema), root, modulePath, dependencies, files)
	}
	if err != nil {
		return nil
	}
	for _, field := range fieldPath {
		schema = schemaForProperty(schema, field, make(map[*jsonschema.Schema]bool))
		if schema == nil {
			return nil
		}
	}
	properties := make(map[string]struct{})
	collectSchemaProperties(schema, properties, make(map[*jsonschema.Schema]bool))
	for _, field := range existing {
		delete(properties, field)
	}
	completions := make([]string, 0, len(properties))
	for field := range properties {
		completions = append(completions, field)
	}
	slices.Sort(completions)

	return completions
}

func compileLockedHelmChartSchema(dependencies *dependency.Lock, source, version string) (*jsonschema.Schema, error) {
	archive, err := helmrender.LoadSchemaArchive(dependencies, source, version)
	if err != nil || len(archive.Schema) == 0 {
		return nil, err
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(archive.Schema))
	if err != nil {
		return nil, fmt.Errorf("parse locked Helm values Schema: %w", err)
	}
	resourcePath := path.Join(archive.Root, "values.schema.json")
	resourceURL := (&url.URL{Scheme: "mcl-helm", Host: archive.Digest, Path: "/" + resourcePath}).String()
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(helmArchiveSchemaCompletionLoader{digest: archive.Digest, root: archive.Root, files: archive.Files})
	if err := compiler.AddResource(resourceURL, document); err != nil {
		return nil, fmt.Errorf("register locked Helm values Schema: %w", err)
	}
	schema, err := compiler.Compile(resourceURL)
	if err != nil {
		return nil, fmt.Errorf("compile locked Helm values Schema: %w", err)
	}

	return schema, nil
}

type helmArchiveSchemaCompletionLoader struct {
	digest string
	root   string
	files  map[string][]byte
}

func (loader helmArchiveSchemaCompletionLoader) Load(source string) (any, error) {
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "mcl-helm" || parsed.Host != loader.digest || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("schema reference %q is unavailable during offline completion", source)
	}
	if !strings.HasPrefix(parsed.Path, "/") || path.Clean(parsed.Path) != parsed.Path {
		return nil, errors.New("schema reference escapes the Helm Chart archive")
	}
	archivePath := strings.TrimPrefix(parsed.Path, "/")
	relative, ok := strings.CutPrefix(archivePath, loader.root+"/")
	if !ok || relative == "" {
		return nil, errors.New("schema reference is outside the Helm Chart archive")
	}
	data, exists := loader.files[relative]
	if !exists {
		return nil, fmt.Errorf("schema reference %q is missing from the Helm Chart archive", source)
	}

	return jsonschema.UnmarshalJSON(bytes.NewReader(data))
}

func findHelmCompletionTarget(module ModuleAST, aliases map[string]bool, definitions map[string]Expr, placeholder string) (string, string, []string, []string, bool) {
	for _, node := range module.Nodes {
		expression := completionExpression(node)
		if expression == nil {
			continue
		}
		chartSource, chartVersion, fieldPath, existing, found := findHelmCompletionTargetInExpr(expression, aliases, definitions, placeholder)
		if found {
			return chartSource, chartVersion, fieldPath, existing, true
		}
	}

	return "", "", nil, nil, false
}

func completionExpression(node ASTNode) Expr {
	switch node := node.(type) {
	case *LetDeclaration:
		return node.Expression
	case *EmitStatement:
		return node.Expression
	case *ValidateStatement:
		return node.Expression
	default:
		return nil
	}
}

func findHelmCompletionTargetInExpr(expression Expr, aliases map[string]bool, definitions map[string]Expr, placeholder string) (string, string, []string, []string, bool) {
	if call, ok := expression.(*CallExpr); ok {
		callee, calleeOK := call.Callee.(*ReferenceExpr)
		if calleeOK {
			alias, method, qualified := strings.CutLast(callee.Name, ".")
			if qualified && aliases[alias] && method == "render" && len(call.Arguments) == 2 {
				chart, chartOK := call.Arguments[0].(*StringExpr)
				options := call.Arguments[1]
				if ref, ok := options.(*ReferenceExpr); ok {
					options = definitions[ref.Name]
				}
				if chartOK {
					if optionMap, ok := options.(*MapExpr); ok {
						for _, entry := range optionMap.Entries {
							if entry.Key != "values" {
								continue
							}
							if fieldPath, existing, found := locateCompletionMap(entry.Value, nil, placeholder); found {
								version := ""
								if versionExpr := helmCompletionOption(optionMap, "version"); !isNilExpr(versionExpr) {
									version = completionString(versionExpr, definitions, make(map[string]bool))
								}

								return chart.Value, version, fieldPath, existing, true
							}
						}
					}
				}
			}
		}
	}
	var result string
	var version string
	var fieldPath, existing []string
	found := false
	walkExprChildren(expression, func(child Expr) {
		if found {
			return
		}
		result, version, fieldPath, existing, found = findHelmCompletionTargetInExpr(child, aliases, definitions, placeholder)
	})

	return result, version, fieldPath, existing, found
}

func helmCompletionOption(options *MapExpr, name string) Expr {
	for _, entry := range options.Entries {
		if entry.Key == name {
			return entry.Value
		}
	}

	return nil
}

func completionString(expression Expr, definitions map[string]Expr, resolving map[string]bool) string {
	switch node := expression.(type) {
	case *StringExpr:
		return node.Value
	case *ReferenceExpr:
		if resolving[node.Name] {
			return ""
		}
		resolving[node.Name] = true
		value := completionString(definitions[node.Name], definitions, resolving)
		delete(resolving, node.Name)

		return value
	default:
		return ""
	}
}

func locateCompletionMap(expression Expr, path []string, placeholder string) ([]string, []string, bool) {
	switch node := expression.(type) {
	case *NodeExpr:
		return locateCompletionMap(node.Body, path, placeholder)
	case *ListExpr:
		for _, item := range node.Items {
			if fieldPath, existing, found := locateCompletionMap(item, path, placeholder); found {
				return fieldPath, existing, true
			}
		}
	case *MapExpr:
		existing := make([]string, 0, len(node.Entries))
		for _, entry := range node.Entries {
			if entry.Key == placeholder {
				return slices.Clone(path), existing, true
			}
			existing = append(existing, entry.Key)
		}
		for _, entry := range node.Entries {
			if fieldPath, fields, found := locateCompletionMap(entry.Value, append(slices.Clone(path), entry.Key), placeholder); found {
				return fieldPath, fields, true
			}
		}
	}

	return nil, nil, false
}

func schemaForType(typeExpr ResolvedType, types map[string]ResolvedType, resolving map[string]bool, analyses ...*TypeAnalysisContext) *jsonschema.Schema {
	var analysis *TypeAnalysisContext
	if len(analyses) != 0 {
		analysis = analyses[0]
	}
	if schema := analysis.schema(typeExpr.SchemaID); schema != nil {
		return schema
	}
	name := typeExpr.Name
	if scoped := analysis.scope(typeExpr.ScopeID); scoped != nil {
		types = scoped
		if _, qualifiedName, ok := strings.CutLast(name, "."); ok {
			name = qualifiedName
		}
	}
	if name == "" || resolving[name] {
		return nil
	}
	definition, exists := types[name]
	if !exists {
		return nil
	}
	instantiated, err := instantiateTypeAlias(typeExpr, definition)
	if err != nil {
		return nil
	}
	resolving[name] = true
	schema := schemaForType(instantiated, types, resolving, analysis)
	delete(resolving, name)

	return schema
}

func schemaForProperty(schema *jsonschema.Schema, name string, visited map[*jsonschema.Schema]bool) *jsonschema.Schema {
	if schema == nil || visited[schema] {
		return nil
	}
	visited[schema] = true
	if schema.Ref != nil {
		if result := schemaForProperty(schema.Ref, name, visited); result != nil {
			return result
		}
	}
	if property, exists := schema.Properties[name]; exists {
		return property
	}
	if additional, ok := schema.AdditionalProperties.(*jsonschema.Schema); ok {
		return additional
	}
	for _, alternatives := range [][]*jsonschema.Schema{schema.AllOf, schema.AnyOf, schema.OneOf} {
		for _, alternative := range alternatives {
			if property := schemaForProperty(alternative, name, visited); property != nil {
				return property
			}
		}
	}

	return nil
}

func collectSchemaProperties(schema *jsonschema.Schema, properties map[string]struct{}, visited map[*jsonschema.Schema]bool) {
	if schema == nil || visited[schema] {
		return
	}
	visited[schema] = true
	for name := range schema.Properties {
		properties[name] = struct{}{}
	}
	if schema.Ref != nil {
		collectSchemaProperties(schema.Ref, properties, visited)
	}
	for _, alternatives := range [][]*jsonschema.Schema{schema.AllOf, schema.AnyOf, schema.OneOf} {
		for _, alternative := range alternatives {
			collectSchemaProperties(alternative, properties, visited)
		}
	}
}

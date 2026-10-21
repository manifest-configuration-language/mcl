package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
	"github.com/manifest-configuration-language/mcl/internal/helmrender"
)

type helmRenderCacheValue struct {
	rendered      map[string]string
	fieldMappings map[string][]helmrender.FieldMapping
}

var helmRenderedCache renderCache[helmRenderCacheValue]

func cloneHelmRenderedTemplates(rendered map[string]string) map[string]string {
	cloned := make(map[string]string, len(rendered))
	maps.Copy(cloned, rendered)

	return cloned
}

func cloneHelmFieldMappings(mappings map[string][]helmrender.FieldMapping) map[string][]helmrender.FieldMapping {
	cloned := make(map[string][]helmrender.FieldMapping, len(mappings))
	for path, values := range mappings {
		cloned[path] = make([]helmrender.FieldMapping, len(values))
		for index, value := range values {
			value.Source = slices.Clone(value.Source)
			value.Locations = slices.Clone(value.Locations)
			for locationIndex := range value.Locations {
				value.Locations[locationIndex].Path = slices.Clone(value.Locations[locationIndex].Path)
			}
			cloned[path][index] = value
		}
	}

	return cloned
}

func helmRenderCacheWeight(value helmRenderCacheValue) int {
	weight := 0
	for path, content := range value.rendered {
		weight += len(path) + len(content) + 512
	}
	for path, mappings := range value.fieldMappings {
		weight += len(path) + 512
		for _, mapping := range mappings {
			weight += len(mapping.Marker) + len(mapping.Source) + 256
			for _, location := range mapping.Locations {
				weight += len(location.Path)*8 + 32
			}
		}
	}

	return weight
}

func cloneHelmRenderValues(values map[string]any) map[string]any {
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = cloneHelmRenderValue(value)
	}

	return cloned
}

func cloneHelmRenderValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneHelmRenderValues(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneHelmRenderValue(item)
		}

		return cloned
	case []string:
		return slices.Clone(typed)
	default:
		return value
	}
}

type helmLoadedChart = helmrender.Chart

type helmPreparedRender struct {
	chart             helmrender.Chart
	values            map[string]any
	mappingValues     map[string]any
	chartRoot         string
	chartSourcePath   string
	chartSourceFiles  map[string][]byte
	cacheKey          string
	cacheKeyAvailable bool
}

type helmRenderedChart struct {
	prepared      helmPreparedRender
	templates     map[string]string
	fieldMappings map[string][]helmrender.FieldMapping
}

func helmRender(arguments []RuntimeValue, state *EvaluationState) (RuntimeValue, error) {
	var parentContext context.Context
	var files *projectFilesystem
	projectRoot, sourcePath, dependencies := adapterModuleInputs(state, arguments...)
	operationOverride := ""
	if state != nil && state.Module != nil {
		parentContext = state.Module.Context
		files = state.Module.filesystem
		operationOverride = state.Module.HelmOperation
	}
	if files == nil && projectRoot != "" {
		files = newOSProjectFilesystem(projectRoot)
	}
	if parentContext == nil {
		parentContext = context.Background()
	}
	if err := parentContext.Err(); err != nil {
		return RuntimeValue{}, err
	}
	if len(arguments) != 2 || arguments[0].Kind() != String || arguments[1].Kind() != Map {
		return RuntimeValue{}, errors.New("helm.render expects a source String and an options Map")
	}
	if projectRoot == "" || sourcePath == "" {
		return RuntimeValue{}, errors.New("helm.render requires file-based evaluation")
	}
	source := arguments[0].TextValue()
	options, err := helmOptions(arguments[1])
	if err != nil {
		return RuntimeValue{}, err
	}
	if operationOverride != "" {
		options.operation = operationOverride
	}
	loaded, err := loadHelmChart(source, options, projectRoot, sourcePath, dependencies, files)
	if err != nil {
		return RuntimeValue{}, err
	}
	prepared, err := prepareHelmRender(loaded, options, projectRoot)
	if err != nil {
		return RuntimeValue{}, err
	}
	ctx, cancel := context.WithTimeout(parentContext, 30*time.Second)
	defer cancel()
	allocations := runtimeAllocationBudgetOf(state)
	rendered, err := renderHelmTemplates(ctx, prepared, allocations, files)
	if err != nil {
		return RuntimeValue{}, err
	}

	return buildHelmBundle(rendered, allocations, files)
}

func loadHelmChart(source string, options helmRenderOptions, projectRoot, sourcePath string, dependencies *dependency.Lock, files *projectFilesystem) (helmLoadedChart, error) {
	return helmrender.Load(helmrender.LoadRequest{
		Source: source, Version: options.version, ProjectRoot: projectRoot, SourcePath: sourcePath, Lock: dependencies, SourceFS: files.files,
	})
}

func prepareHelmRender(loaded helmLoadedChart, options helmRenderOptions, projectRoot string) (helmPreparedRender, error) {
	prepared, err := helmrender.Prepare(loaded, helmrender.RenderOptions{
		Values: options.values, ReleaseName: options.releaseName, Namespace: options.namespace,
		Operation: options.operation, Revision: options.revision, KubeVersion: options.kubeVersion,
		APIVersions: options.apiVersions,
	}, projectRoot)
	if err != nil {
		if helmrender.IsValuesError(err) {
			valuesPath, valuesSpan := options.valuesSource.Source()

			return helmPreparedRender{}, sourceDiagnostic(valuesPath, valuesSpan, err)
		}

		return helmPreparedRender{}, err
	}

	return helmPreparedRender{
		chart: loaded, values: prepared.Values, mappingValues: cloneHelmRenderValues(prepared.Values),
		chartRoot: prepared.ChartRoot, chartSourcePath: prepared.ChartSourcePath,
		chartSourceFiles: prepared.ChartSourceFiles,
		cacheKey:         prepared.CacheKey, cacheKeyAvailable: prepared.CacheKeyAvailable,
	}, nil
}

func renderHelmTemplates(ctx context.Context, prepared helmPreparedRender, allocations *runtimeAllocationBudget, filesystems ...*projectFilesystem) (helmRenderedChart, error) {
	result := helmRenderedChart{prepared: prepared}
	if prepared.cacheKeyAvailable {
		if cached, ok := helmRenderedCache.get(prepared.cacheKey); ok {
			result.templates = cloneHelmRenderedTemplates(cached.rendered)
			result.fieldMappings = cloneHelmFieldMappings(cached.fieldMappings)
		}
	}
	if result.templates == nil {
		rendered, err := helmrender.Render(ctx, prepared.chart, prepared.values)
		if err != nil {
			renderError := fmt.Errorf("render Helm chart: %w", err)
			if diagnostic, ok := helmTemplateSourceDiagnostic(err, prepared.chartSourcePath, prepared.chartRoot, prepared.chartSourceFiles, filesystems...); ok {
				return helmRenderedChart{}, diagnostic
			}

			return helmRenderedChart{}, renderError
		}
		result.templates = rendered
		result.fieldMappings, err = helmRenderedFieldMappings(ctx, prepared.chart, prepared.mappingValues, rendered, allocations)
		if err != nil {
			return helmRenderedChart{}, err
		}
		if prepared.cacheKeyAvailable {
			cached := helmRenderCacheValue{
				rendered: cloneHelmRenderedTemplates(result.templates), fieldMappings: cloneHelmFieldMappings(result.fieldMappings),
			}
			helmRenderedCache.set(prepared.cacheKey, cached, helmRenderCacheWeight(cached))
		}
	}

	return result, nil
}

func helmRenderedFieldMappings(ctx context.Context, source helmrender.Chart, values map[string]any, original map[string]string, allocations *runtimeAllocationBudget) (map[string][]helmrender.FieldMapping, error) {
	mappings, err := helmrender.RenderedFieldMappings(ctx, source, values, original, func(rendered string) ([]helmrender.Document, error) {
		documents, err := decodeRuntimeYAMLDocuments([]byte(rendered), "", allocations)
		if errors.Is(err, errRuntimeAllocationLimit) {
			return nil, helmrender.ErrMappingResourceLimit
		}
		if err != nil {
			return nil, err
		}
		result := make([]helmrender.Document, len(documents.List))
		for index, document := range documents.List {
			result[index] = helmrender.Document{Value: helmMappingValue(document)}
		}

		return result, nil
	})
	if errors.Is(err, helmrender.ErrMappingResourceLimit) {
		return nil, errRuntimeAllocationLimit
	}

	return mappings, err
}

func helmMappingValue(value RuntimeValue) helmrender.Value {
	value = runtimeNodeContents(value)
	result := helmrender.Value{}
	switch value.Kind() {
	case Map:
		result.Kind = helmrender.ValueMap
		result.Entries = make([]helmrender.Entry, len(value.Map))
		for index, entry := range value.Map {
			result.Entries[index] = helmrender.Entry{Key: entry.Key, Value: helmMappingValue(entry.Value)}
		}
	case List, AdapterBundle:
		result.Kind = helmrender.ValueList
		result.Items = make([]helmrender.Value, len(value.List))
		for index, item := range value.List {
			result.Items[index] = helmMappingValue(item)
		}
	case String:
		result.Kind = helmrender.ValueString
		result.Text = value.TextValue()
	case Integer:
		result.Kind = helmrender.ValueInteger
		result.Text = value.TextValue()
	case Decimal:
		result.Kind = helmrender.ValueDecimal
		result.Text = value.TextValue()
	case Bool:
		result.Kind = helmrender.ValueBool
		result.Bool = value.BoolValue()
	case Null:
		result.Kind = helmrender.ValueNull
	default:
		result.Kind = helmrender.ValueOther
	}

	return result
}

func buildHelmBundle(rendered helmRenderedChart, allocations *runtimeAllocationBudget, filesystems ...*projectFilesystem) (RuntimeValue, error) {
	chartSource := rendered.prepared.chart
	chartRoot := rendered.prepared.chartRoot
	chartSourcePath := rendered.prepared.chartSourcePath
	paths := slices.Sorted(maps.Keys(rendered.templates))
	var artifacts []RuntimeArtifact
	for _, path := range paths {
		if filepath.Base(path) == "NOTES.txt" {
			continue
		}
		content := rendered.templates[path]
		if strings.TrimSpace(content) == "" {
			continue
		}
		templateSourcePath, hasTemplateSourcePath := helmStaticTemplateSourcePath([]byte(content), path, chartRoot, chartSourcePath, rendered.prepared.chartSourceFiles, filesystems...)
		documents, err := decodeRuntimeYAMLDocuments([]byte(content), templateSourcePathIfKnown(templateSourcePath, hasTemplateSourcePath), allocations)
		if err != nil {
			if hasTemplateSourcePath {
				if line, ok := yamlDecodeErrorLine(err); ok {
					if span, ok := sourceLineSpan([]byte(content), line); ok {
						return RuntimeValue{}, SourceDiagnosticError{
							Path: templateSourcePath, Code: "MCL-YAML", Message: err.Error(), Span: span, Cause: err,
						}
					}
				}
			}

			return RuntimeValue{}, fmt.Errorf("parse Helm template %q: %w", path, err)
		}
		for documentIndex, document := range documents.ListItems() {
			mappedTemplateFields := false
			if mappings := rendered.fieldMappings[path]; len(mappings) > 0 {
				if templateSourcePath, source, ok := helmTemplateSource(chartSourcePath, chartRoot, path, rendered.prepared.chartSourceFiles, filesystems...); ok && bytes.Equal(source, mappings[0].Source) {
					helmApplyFieldMappings(&document, documentIndex, mappings, templateSourcePath)
					mappedTemplateFields = true
					if mappings[0].Fallback {
						location := SourceDiagnosticLocation{Path: templateSourcePath, Span: SourceSpan{End: len(source)}, Message: helmRenderedTemplateSourceLabel, Origin: SourceOriginHelmFallback, Multiplicity: SourceOriginAmbiguous}
						document.Value.SourceLocations = appendSourceLocations(document.Value.SourceLocations, []SourceDiagnosticLocation{location}, "", SourceSpan{}, "")
					}
				}
			}
			if hasTemplateSourcePath {
				setRuntimeSourcePath(&document, templateSourcePath)
			} else if !mappedTemplateFields {
				if templateSourcePath, source, ok := helmTemplateSource(chartSourcePath, chartRoot, path, rendered.prepared.chartSourceFiles, filesystems...); ok {
					location := SourceDiagnosticLocation{
						Path: templateSourcePath, Span: SourceSpan{End: len(source)}, Message: helmRenderedTemplateSourceLabel,
					}
					helmApplyMappedSourceLocation(&document, location)
				}
			}
			lifecycleOrigin := helmLifecycleOrigin(rendered.prepared.chart.Origin, chartRoot, path)
			lifecycle, err := helmLifecycle(lifecycleOrigin, document)
			if err != nil {
				if chartSourcePath != "" && chartRoot != "" {
					relativeTemplatePath, found := strings.CutPrefix(path, chartRoot+"/")
					if found {
						templatePath := filepath.Join(chartSourcePath, filepath.FromSlash(relativeTemplatePath))
						if pathWithin(chartSourcePath, templatePath) {
							if source, readErr := readHelmChartSourceFile(templatePath, chartSourcePath, rendered.prepared.chartSourceFiles, filesystems...); readErr == nil && len(source) > 0 {
								message := fmt.Sprintf("Helm output from root template %q failed lifecycle validation: %v", path, err)

								return RuntimeValue{}, SourceDiagnosticError{
									Path:    templatePath,
									Code:    "MCL-HELM",
									Message: message,
									Span:    SourceSpan{End: len(source)},
									Cause:   err,
								}
							}
						}
					}
				}

				return RuntimeValue{}, fmt.Errorf("helm template %q: %w", path, err)
			}
			artifacts = append(artifacts, RuntimeArtifact{Value: document, Lifecycle: lifecycle})
		}
	}
	crds, err := helmrender.CRDObjects(chartSource)
	if err != nil {
		return RuntimeValue{}, err
	}
	for _, crd := range crds {
		sourcePath, hasSourcePath := helmLocalChartSourcePath(crd.Filename, chartRoot, chartSourcePath, rendered.prepared.chartSourceFiles)
		if !hasSourcePath {
			sourcePath = ""
		}
		documents, err := decodeRuntimeYAMLDocuments(crd.Data, sourcePath, allocations)
		if err != nil {
			return RuntimeValue{}, fmt.Errorf("parse Helm CRD %q: %w", crd.Filename, err)
		}
		for _, document := range documents.ListItems() {
			lifecycle := Lifecycle{
				Events: []string{phaseBootstrap}, Wait: true,
				Origin: helmLifecycleOrigin(rendered.prepared.chart.Origin, chartRoot, crd.Filename),
			}
			artifacts = append(artifacts, RuntimeArtifact{Value: document, Lifecycle: lifecycle})
		}
	}

	return runtimeBundleValueWithBudget(artifacts, "", allocations)
}

func helmLifecycleOrigin(origin, chartRoot, filename string) string {
	if origin == "" {
		return filename
	}
	if chartRoot == "" || filename == "" {
		return origin
	}
	relative, err := filepath.Rel(filepath.FromSlash(chartRoot), filepath.FromSlash(filename))
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return origin
	}

	return origin + "/" + filepath.ToSlash(relative)
}

func templateSourcePathIfKnown(path string, known bool) string {
	if known {
		return path
	}

	return ""
}

func setRuntimeSourcePath(value *RuntimeValue, path string) {
	if value == nil {
		return
	}
	value.Value.SourcePath = path
	for index := range value.List {
		setRuntimeSourcePath(&value.List[index], path)
	}
	for index := range value.Map {
		value.Map[index].KeySourcePath = path
		setRuntimeSourcePath(&value.Map[index].Value, path)
	}
}

func helmApplyFieldMappings(value *RuntimeValue, documentIndex int, mappings []helmrender.FieldMapping, sourcePath string) {
	if value == nil {
		return
	}
	for _, mapping := range mappings {
		for _, location := range mapping.Locations {
			if location.Document != documentIndex {
				continue
			}
			if location.Key {
				if len(location.Path) == 0 {
					continue
				}
				current := value
				for _, index := range location.Path[:len(location.Path)-1] {
					current = runtimeNodeContentsPointer(current)
					if current == nil {
						break
					}
					if current.Kind() == Map && index < len(current.Map) {
						current = &current.Map[index].Value
					} else if (current.Kind() == List || current.Kind() == AdapterBundle) && index < len(current.List) {
						current = &current.List[index]
					} else {
						current = nil

						break
					}
				}
				current = runtimeNodeContentsPointer(current)
				entryIndex := location.Path[len(location.Path)-1]
				if current != nil && current.Kind() == Map && entryIndex < len(current.Map) {
					current.Map[entryIndex].KeySourcePath = sourcePath
					current.Map[entryIndex].KeySpan = helmSourceSpan(mapping.Span)
					multiplicity := SourceOriginUnique
					occurrences := len(mapping.Locations)
					if occurrences > 1 {
						multiplicity = SourceOriginRepeated
					}
					if mapping.Fallback {
						multiplicity = SourceOriginAmbiguous
					}
					origin := SourceOriginHelmExpression
					if mapping.Branch {
						origin = SourceOriginHelmBranch
					}
					location := SourceDiagnosticLocation{Path: sourcePath, Span: helmSourceSpan(mapping.Span), Message: helmRenderedTemplateSourceLabel, Origin: origin, Multiplicity: multiplicity, Occurrences: occurrences}
					current.Map[entryIndex].KeySourceLocations = appendSourceLocations(current.Map[entryIndex].KeySourceLocations, []SourceDiagnosticLocation{location}, "", SourceSpan{}, "")
				}

				continue
			}
			current := value
			for _, index := range location.Path {
				current = runtimeNodeContentsPointer(current)
				if current == nil {
					break
				}
				if current.Kind() == Map && index < len(current.Map) {
					current = &current.Map[index].Value
				} else if (current.Kind() == List || current.Kind() == AdapterBundle) && index < len(current.List) {
					current = &current.List[index]
				} else {
					current = nil

					break
				}
			}
			current = runtimeNodeContentsPointer(current)
			if current != nil {
				multiplicity := SourceOriginUnique
				occurrences := len(mapping.Locations)
				if occurrences > 1 {
					multiplicity = SourceOriginRepeated
				}
				if mapping.Fallback {
					multiplicity = SourceOriginAmbiguous
				}
				origin := SourceOriginHelmExpression
				if mapping.Branch {
					origin = SourceOriginHelmBranch
				}
				location := SourceDiagnosticLocation{Path: sourcePath, Span: helmSourceSpan(mapping.Span), Message: helmRenderedTemplateSourceLabel, Origin: origin, Multiplicity: multiplicity, Occurrences: occurrences}
				helmApplyMappedSourceLocation(current, location)
			}
		}
	}
}

func helmSourceSpan(span helmrender.Span) SourceSpan {
	return SourceSpan{Start: span.Start, End: span.End}
}

func helmApplyMappedSourceLocation(value *RuntimeValue, location SourceDiagnosticLocation) {
	if value == nil {
		return
	}
	if value.Kind() == Node && value.Base != nil {
		helmApplyMappedSourceLocation(value.Base, location)

		return
	}
	value.Value.SourcePath = location.Path
	value.Value.Span = location.Span
	value.Value.SourceLocations = appendSourceLocations(value.Value.SourceLocations, []SourceDiagnosticLocation{location}, "", SourceSpan{}, "")
	for index := range value.Map {
		helmApplyMappedSourceLocation(&value.Map[index].Value, location)
	}
	for index := range value.List {
		helmApplyMappedSourceLocation(&value.List[index], location)
	}
}

func helmTemplateSource(chartSourcePath, chartRoot, filename string, chartSources map[string][]byte, filesystems ...*projectFilesystem) (string, []byte, bool) {
	sourcePath, ok := helmLocalChartSourcePath(filename, chartRoot, chartSourcePath, chartSources, filesystems...)
	if !ok {
		return "", nil, false
	}
	source, err := readHelmChartSourceFile(sourcePath, chartSourcePath, chartSources, filesystems...)
	if err != nil || len(source) == 0 {
		return "", nil, false
	}

	return sourcePath, source, true
}

func helmStaticTemplateSourcePath(rendered []byte, filename, chartRoot, localChartPath string, chartSources map[string][]byte, filesystems ...*projectFilesystem) (string, bool) {
	if bytes.Contains(rendered, []byte("{{")) {
		return "", false
	}
	sourcePath, ok := helmLocalChartSourcePath(filename, chartRoot, localChartPath, chartSources, filesystems...)
	if !ok {
		return "", false
	}
	source, err := readHelmChartSourceFile(sourcePath, localChartPath, chartSources, filesystems...)
	if err != nil || bytes.Contains(source, []byte("{{")) || !bytes.Equal(source, rendered) {
		return "", false
	}

	return sourcePath, true
}

func readHelmProjectFile(filename, localChartPath string, filesystems ...*projectFilesystem) ([]byte, error) {
	return projectFilesystemFor(localChartPath, filesystems).readLimitedFile(filename)
}

func readHelmChartSourceFile(filename, localChartPath string, chartSources map[string][]byte, filesystems ...*projectFilesystem) ([]byte, error) {
	if chartSources == nil {
		return readHelmProjectFile(filename, localChartPath, filesystems...)
	}
	relativePath, err := filepath.Rel(localChartPath, filename)
	if err != nil || relativePath == "." || filepath.IsAbs(relativePath) || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("helm chart source path %q escapes its source root", filename)
	}
	data, ok := chartSources[filepath.ToSlash(relativePath)]
	if !ok {
		return nil, fs.ErrNotExist
	}

	return bytes.Clone(data), nil
}

func helmLocalChartSourcePath(filename, chartRoot, localChartPath string, chartSources map[string][]byte, filesystems ...*projectFilesystem) (string, bool) {
	if filename == "" || chartRoot == "" || localChartPath == "" {
		return "", false
	}
	relativePath, err := filepath.Rel(chartRoot, filename)
	if err != nil || relativePath == "." || filepath.IsAbs(relativePath) || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return "", false
	}
	joinedPath := filepath.Join(localChartPath, relativePath)
	if chartSources != nil {
		if !pathWithin(localChartPath, joinedPath) {
			return "", false
		}
		if _, ok := chartSources[filepath.ToSlash(relativePath)]; !ok {
			return "", false
		}

		return joinedPath, true
	}
	filesystem := projectFilesystemFor(localChartPath, filesystems)
	if !filesystem.osBacked {
		projectRelative, err := filesystem.relativePath(joinedPath)
		if err != nil {
			return "", false
		}
		if _, err := fs.Stat(filesystem.files, projectRelative); err != nil {
			return "", false
		}

		return joinedPath, true
	}
	sourcePath, err := filepath.EvalSymlinks(joinedPath)
	if err != nil || !pathWithin(localChartPath, sourcePath) {
		return "", false
	}

	return sourcePath, true
}

func helmTemplateSourceDiagnostic(err error, chartPath, chartRoot string, chartSources map[string][]byte, filesystems ...*projectFilesystem) (SourceDiagnosticError, bool) {
	if chartPath == "" || chartRoot == "" {
		return SourceDiagnosticError{}, false
	}
	message := err.Error()
	for _, marker := range []string{"parse error at (", "execution error at ("} {
		_, after, ok0 := strings.Cut(message, marker)
		if !ok0 {
			continue
		}
		location, _, ok := strings.Cut(after, ")")
		if !ok {
			continue
		}
		lineLocation := location
		if marker == "execution error at (" {
			lineLocation, _, ok = strings.CutLast(lineLocation, ":")
			if !ok {
				continue
			}
		}
		templateName, lineText, ok := strings.CutLast(lineLocation, ":")
		if !ok {
			continue
		}
		line, parseErr := strconv.Atoi(lineText)
		if parseErr != nil || line <= 0 {
			continue
		}
		relativePath, ok := strings.CutPrefix(templateName, chartRoot+"/")
		if !ok {
			continue
		}
		templatePath := filepath.Join(chartPath, filepath.FromSlash(relativePath))
		if !pathWithin(chartPath, templatePath) {
			continue
		}
		templateSource, readErr := readHelmChartSourceFile(templatePath, chartPath, chartSources, filesystems...)
		if readErr != nil {
			continue
		}
		span, ok := sourceLineSpan(templateSource, line)
		if !ok {
			continue
		}
		cause := fmt.Errorf("render Helm chart: %w", err)

		return SourceDiagnosticError{
			Path:    templatePath,
			Code:    "MCL-HELM",
			Message: cause.Error(),
			Span:    span,
			Cause:   cause,
		}, true
	}

	return SourceDiagnosticError{}, false
}

func helmLifecycle(origin string, document RuntimeValue) (Lifecycle, error) {
	lifecycle := Lifecycle{Events: []string{phaseMain}, Origin: origin}
	document = runtimeNodeContents(document)
	if document.Kind() != Map {
		return lifecycle, nil
	}
	metadataIndex := -1
	for index := range document.Map {
		if document.Map[index].Key == "metadata" {
			metadataIndex = index

			break
		}
	}
	if metadataIndex < 0 || document.Map[metadataIndex].Value.Kind() != Map {
		return lifecycle, nil
	}
	metadata := &document.Map[metadataIndex].Value
	annotationsIndex := -1
	for index := range metadata.Map {
		if metadata.Map[index].Key == "annotations" {
			annotationsIndex = index

			break
		}
	}
	if annotationsIndex < 0 || metadata.Map[annotationsIndex].Value.Kind() != Map {
		return lifecycle, nil
	}
	annotations := &metadata.Map[annotationsIndex].Value
	annotationValues := make(map[string]string)
	for _, entry := range annotations.Map {
		if isHelmHookAnnotation(entry.Key) {
			if entry.Value.Kind() != String {
				return lifecycle, fmt.Errorf("annotation %q must be a String", entry.Key)
			}
			annotationValues[entry.Key] = entry.Value.TextValue()
		}
	}
	hookValue, hasHook := annotationValues["helm.sh/hook"]
	if !hasHook {
		if len(annotationValues) != 0 {
			return lifecycle, errors.New("helm hook metadata requires helm.sh/hook")
		}

		return lifecycle, nil
	}
	events := make([]string, 0)
	for hook := range strings.SplitSeq(hookValue, ",") {
		event, exists := helmHookEvent(strings.TrimSpace(hook))
		if !exists {
			return lifecycle, fmt.Errorf("unsupported Helm hook event %q", strings.TrimSpace(hook))
		}
		if !slices.Contains(events, event) {
			events = append(events, event)
		}
	}
	if len(events) == 0 {
		return lifecycle, errors.New("helm.sh/hook must contain at least one event")
	}
	lifecycle.Events = events
	lifecycle.Wait = true
	if weight, exists := annotationValues["helm.sh/hook-weight"]; exists {
		parsed, err := strconv.Atoi(weight)
		if err != nil {
			return lifecycle, fmt.Errorf("invalid helm.sh/hook-weight %q", weight)
		}
		lifecycle.Priority = parsed
	}
	if policy, exists := annotationValues["helm.sh/hook-delete-policy"]; exists {
		for raw := range strings.SplitSeq(policy, ",") {
			cleanup, exists := helmCleanupPolicy(strings.TrimSpace(raw))
			if !exists {
				return lifecycle, fmt.Errorf("unsupported Helm hook delete policy %q", strings.TrimSpace(raw))
			}
			if !slices.Contains(lifecycle.Cleanup, cleanup) {
				lifecycle.Cleanup = append(lifecycle.Cleanup, cleanup)
			}
		}
	} else {
		lifecycle.Cleanup = []string{"before-next-run"}
	}
	filtered := annotations.Map[:0]
	for _, entry := range annotations.Map {
		if !isHelmHookAnnotation(entry.Key) {
			filtered = append(filtered, entry)
		}
	}
	annotations.Map = filtered
	if len(annotations.Map) == 0 {
		metadata.Map = append(metadata.Map[:annotationsIndex], metadata.Map[annotationsIndex+1:]...)
	}

	return lifecycle, nil
}

func isHelmHookAnnotation(key string) bool {
	switch key {
	case "helm.sh/hook", "helm.sh/hook-weight", "helm.sh/hook-delete-policy":
		return true
	default:
		return false
	}
}

func helmHookEvent(hook string) (string, bool) {
	switch hook {
	case "pre-install", "crd-install":
		if hook == "crd-install" {
			return phaseBootstrap, true
		}

		return phaseBeforeCreate, true
	case "post-install":
		return phaseAfterCreate, true
	case "pre-upgrade":
		return phaseBeforeUpdate, true
	case "post-upgrade":
		return phaseAfterUpdate, true
	case "pre-delete":
		return phaseBeforeDelete, true
	case "post-delete":
		return phaseAfterDelete, true
	case "pre-rollback":
		return phaseBeforeRollback, true
	case "post-rollback":
		return phaseAfterRollback, true
	case "test", "test-success", "test-failure":
		return phaseVerify, true
	default:
		return "", false
	}
}

func helmCleanupPolicy(policy string) (string, bool) {
	switch policy {
	case "before-hook-creation":
		return "before-next-run", true
	case "hook-succeeded":
		return "on-success", true
	case "hook-failed":
		return "on-failure", true
	default:
		return "", false
	}
}

type helmRenderOptions struct {
	values       map[string]any
	valuesSource RuntimeValue
	releaseName  string
	namespace    string
	operation    string
	revision     int
	kubeVersion  string
	apiVersions  []string
	version      string
}

func helmOptions(value RuntimeValue) (helmRenderOptions, error) {
	options := helmRenderOptions{releaseName: "mcl", namespace: "default", operation: operationCreate, revision: 1, values: make(map[string]any)}
	for _, entry := range value.Entries() {
		switch entry.Key {
		case "values":
			if entry.Value.Kind() != Map {
				return options, errors.New("helm.render option values must be a Map")
			}
			options.valuesSource = entry.Value
			converted, err := helmValue(entry.Value)
			if err != nil {
				return options, fmt.Errorf("convert Helm values: %w", err)
			}
			values, ok := converted.(map[string]any)
			if !ok {
				return options, errors.New("helm.render option values must be a Map")
			}
			options.values = values
		case "revision":
			if entry.Value.Kind() != Integer {
				return options, errors.New("helm.render option revision must be an Int")
			}
			revision, err := strconv.Atoi(entry.Value.TextValue())
			if err != nil || revision < 1 {
				return options, errors.New("helm.render option revision must be a positive Int")
			}
			options.revision = revision
		case "releaseName", "namespace", "operation", "kubeVersion", "version":
			if entry.Value.Kind() != String {
				return options, fmt.Errorf("helm.render option %s must be a String", entry.Key)
			}
			switch entry.Key {
			case "releaseName":
				options.releaseName = entry.Value.TextValue()
			case "namespace":
				options.namespace = entry.Value.TextValue()
			case "operation":
				switch entry.Value.TextValue() {
				case operationCreate, operationUpdate, operationDelete, operationRollback, phaseVerify:
				default:
					return options, errors.New("helm.render option operation must be create, update, delete, rollback, or verify")
				}
				options.operation = entry.Value.TextValue()
			case "kubeVersion":
				options.kubeVersion = entry.Value.TextValue()
			case "version":
				options.version = entry.Value.TextValue()
			}
		case "apiVersions":
			if entry.Value.Kind() != List && entry.Value.Kind() != AdapterBundle {
				return options, errors.New("helm.render option apiVersions must be a List")
			}
			options.apiVersions = make([]string, len(entry.Value.ListItems()))
			for index, version := range entry.Value.ListItems() {
				if version.Kind() != String {
					return options, errors.New("helm.render option apiVersions must contain only Strings")
				}
				options.apiVersions[index] = version.TextValue()
			}
		default:
			return options, fmt.Errorf("helm.render has no option named %q", entry.Key)
		}
	}

	return options, nil
}

func helmValue(value RuntimeValue) (any, error) {
	switch value.Kind() {
	case Null:
		return nil, nil
	case Bool:
		return value.BoolValue(), nil
	case Integer:
		if _, err := parseRuntimeInteger(value.TextValue()); err != nil {
			return nil, fmt.Errorf("integer %q exceeds the %d-digit limit", value.TextValue(), maxNumericCoefficientDigits)
		}
		integer, err := strconv.ParseInt(value.TextValue(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("integer %q is outside Helm's supported range", value.TextValue())
		}

		return integer, nil
	case Decimal:
		parsed, err := parseRuntimeDecimal(value.TextValue())
		if err != nil {
			return nil, fmt.Errorf("invalid decimal %q", value.TextValue())
		}
		if err := validateRuntimeDecimal(parsed); err != nil {
			return nil, err
		}
		number, exact := parsed.Float64()
		if !exact || math.IsInf(number, 0) || math.IsNaN(number) {
			return nil, fmt.Errorf("decimal %q cannot be represented exactly as a Helm value", value.TextValue())
		}

		return number, nil
	case String:
		return value.TextValue(), nil
	case List, AdapterBundle:
		result := make([]any, len(value.ListItems()))
		for index, item := range value.ListItems() {
			converted, err := helmValue(item)
			if err != nil {
				return nil, err
			}
			result[index] = converted
		}

		return result, nil
	case Map:
		result := make(map[string]any, len(value.Entries()))
		for _, entry := range value.Entries() {
			converted, err := helmValue(entry.Value)
			if err != nil {
				return nil, err
			}
			result[entry.Key] = converted
		}

		return result, nil
	default:
		return nil, fmt.Errorf("helm values cannot contain %s", kindName(value.Kind()))
	}
}

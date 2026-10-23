package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/helmrender"
)

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

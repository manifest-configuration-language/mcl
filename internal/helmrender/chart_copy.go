package helmrender

import (
	"maps"
	"slices"
	"strings"

	"helm.sh/helm/v4/pkg/chart/common"
	v2 "helm.sh/helm/v4/pkg/chart/v2"
)

func cloneChart(source *v2.Chart) *v2.Chart {
	clone := *source
	clone.Raw = cloneChartFiles(source.Raw)
	clone.Templates = cloneChartFiles(source.Templates)
	clone.Files = cloneChartFiles(source.Files)
	clone.Schema = slices.Clone(source.Schema)
	clone.Values = cloneChartValues(source.Values)

	if source.Metadata != nil {
		metadata := *source.Metadata
		metadata.Sources = slices.Clone(source.Metadata.Sources)
		metadata.Keywords = slices.Clone(source.Metadata.Keywords)
		metadata.Annotations = maps.Clone(source.Metadata.Annotations)
		metadata.Maintainers = make([]*v2.Maintainer, len(source.Metadata.Maintainers))
		for index, maintainer := range source.Metadata.Maintainers {
			if maintainer == nil {
				continue
			}
			maintainerCopy := *maintainer
			metadata.Maintainers[index] = &maintainerCopy
		}
		if source.Metadata.Dependencies != nil {
			metadata.Dependencies = make([]*v2.Dependency, len(source.Metadata.Dependencies))
			for index, dependency := range source.Metadata.Dependencies {
				if dependency == nil {
					continue
				}
				dependencyCopy := *dependency
				dependencyCopy.Tags = slices.Clone(dependency.Tags)
				dependencyCopy.ImportValues = cloneChartAnySlice(dependency.ImportValues)
				metadata.Dependencies[index] = &dependencyCopy
			}
		}
		clone.Metadata = &metadata
	}
	if source.Lock != nil {
		lock := *source.Lock
		lock.Dependencies = make([]*v2.Dependency, len(source.Lock.Dependencies))
		for index, dependency := range source.Lock.Dependencies {
			if dependency == nil {
				continue
			}
			dependencyCopy := *dependency
			dependencyCopy.Tags = slices.Clone(dependency.Tags)
			dependencyCopy.ImportValues = cloneChartAnySlice(dependency.ImportValues)
			lock.Dependencies[index] = &dependencyCopy
		}
		clone.Lock = &lock
	}

	dependencies := source.Dependencies()
	clonedDependencies := make([]*v2.Chart, len(dependencies))
	for index, dependency := range dependencies {
		clonedDependencies[index] = cloneChart(dependency)
	}
	clone.SetDependencies(clonedDependencies...)

	return &clone
}

func cloneChartAnySlice(values []any) []any {
	if values == nil {
		return nil
	}
	cloned := make([]any, len(values))
	for index, value := range values {
		cloned[index] = cloneChartValue(value)
	}

	return cloned
}

func cloneChartFiles(files []*common.File) []*common.File {
	if files == nil {
		return nil
	}
	cloned := make([]*common.File, len(files))
	for index, file := range files {
		if file == nil {
			continue
		}
		fileCopy := *file
		fileCopy.Data = slices.Clone(file.Data)
		cloned[index] = &fileCopy
	}

	return cloned
}

func cloneChartWithoutNotes(source *v2.Chart) *v2.Chart {
	clone := cloneChart(source)
	removeNotesTemplates(clone)

	return clone
}

func removeNotesTemplates(source *v2.Chart) {
	source.Templates = slices.DeleteFunc(source.Templates, func(file *common.File) bool {
		return file == nil || strings.HasSuffix(file.Name, "NOTES.txt")
	})
	for _, dependency := range source.Dependencies() {
		removeNotesTemplates(dependency)
	}
}

func cloneChartValues(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}

	clone := maps.Clone(values)
	for key, value := range clone {
		clone[key] = cloneChartValue(value)
	}

	return clone
}

func cloneChartValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneChartValues(value)
	case []any:
		clone := make([]any, len(value))
		for index, item := range value {
			clone[index] = cloneChartValue(item)
		}

		return clone
	default:
		return value
	}
}

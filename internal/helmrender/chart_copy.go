package helmrender

import (
	"maps"
	"slices"

	v2 "helm.sh/helm/v4/pkg/chart/v2"
)

func cloneChart(source *v2.Chart) *v2.Chart {
	clone := *source
	clone.Values = cloneChartValues(source.Values)

	if source.Metadata != nil {
		metadata := *source.Metadata
		if source.Metadata.Dependencies != nil {
			metadata.Dependencies = make([]*v2.Dependency, len(source.Metadata.Dependencies))
			for index, dependency := range source.Metadata.Dependencies {
				if dependency == nil {
					continue
				}
				dependencyCopy := *dependency
				dependencyCopy.Tags = slices.Clone(dependency.Tags)
				dependencyCopy.ImportValues = slices.Clone(dependency.ImportValues)
				metadata.Dependencies[index] = &dependencyCopy
			}
		}
		clone.Metadata = &metadata
	}

	dependencies := source.Dependencies()
	clonedDependencies := make([]*v2.Chart, len(dependencies))
	for index, dependency := range dependencies {
		clonedDependencies[index] = cloneChart(dependency)
	}
	clone.SetDependencies(clonedDependencies...)

	return &clone
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

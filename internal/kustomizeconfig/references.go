package kustomizeconfig

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var remoteShorthandPattern = regexp.MustCompile(`^[[:alnum:]][[:alnum:].-]*\.[[:alpha:]]{2,}/[^/]+/[^/]+`)

var rootReferenceFields = []string{
	"resources",
	"bases",
	"components",
	"crds",
	"configurations",
	"generators",
	"transformers",
	"patchesStrategicMerge",
}

// RootReferenceFieldsはローカル参照を含むKustomizationフィールド名を返します。
func RootReferenceFields() []string {
	return slices.Clone(rootReferenceFields)
}

// RemoteReferenceはKustomization文書に含まれる外部参照を返します。
func RemoteReference(document map[string]any) (string, bool) {
	var find func(any) (string, bool)
	find = func(value any) (string, bool) {
		switch value := value.(type) {
		case string:
			if IsRemoteReference(value) {
				return value, true
			}
		case []any:
			for _, item := range value {
				if found, ok := find(item); ok {
					return found, true
				}
			}
		}

		return "", false
	}
	for _, field := range rootReferenceFields {
		if found, ok := find(document[field]); ok {
			return found, true
		}
	}
	if patches, ok := document["patchesJson6902"].([]any); ok {
		for _, patch := range patches {
			if fields, ok := patch.(map[string]any); ok {
				if path, ok := fields["path"].(string); ok && IsRemoteReference(path) {
					return path, true
				}
			}
		}
	}
	if patches, ok := document["patches"].([]any); ok {
		for _, patch := range patches {
			if fields, ok := patch.(map[string]any); ok {
				if found, ok := find(fields["path"]); ok {
					return found, true
				}
			}
		}
	}
	if openapi, ok := document["openapi"].(map[string]any); ok {
		if found, ok := find(openapi["path"]); ok {
			return found, true
		}
	}
	for _, generatorName := range []string{"configMapGenerator", "secretGenerator"} {
		if generators, ok := document[generatorName].([]any); ok {
			for _, generator := range generators {
				fields, ok := generator.(map[string]any)
				if !ok {
					continue
				}
				for _, field := range []string{"files", "envs"} {
					if found, ok := find(fields[field]); ok {
						return found, true
					}
				}
			}
		}
	}
	if replacements, ok := document["replacements"].([]any); ok {
		for _, replacement := range replacements {
			fields, ok := replacement.(map[string]any)
			if !ok {
				continue
			}
			if source, ok := fields["source"].(map[string]any); ok {
				if found, ok := find(source["file"]); ok {
					return found, true
				}
			}
		}
	}
	if charts, ok := document["helmCharts"].([]any); ok {
		for _, chart := range charts {
			if fields, ok := chart.(map[string]any); ok {
				for _, field := range []string{"repo", "valuesFile"} {
					if found, ok := find(fields[field]); ok {
						return found, true
					}
				}
			}
		}
	}

	return "", false
}

// IsRemoteReferenceはKustomizationの外部参照形式を判定します。
func IsRemoteReference(reference string) bool {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return false
	}
	if strings.HasPrefix(reference, "git@") || strings.HasPrefix(reference, "git::") || strings.Contains(reference, "://") {
		return true
	}
	parsed, err := url.Parse(reference)
	if err == nil && parsed.Scheme != "" {
		return true
	}

	return remoteShorthandPattern.MatchString(reference)
}

// RewriteDocumentはKustomization文書の参照フィールドを書き換えます。
func RewriteDocument(document map[string]any, rewrite func(string) (string, error)) error {
	var rewriteStrings func(any) (any, error)
	rewriteStrings = func(value any) (any, error) {
		switch value := value.(type) {
		case string:
			return rewrite(value)
		case []any:
			for index, item := range value {
				updated, err := rewriteStrings(item)
				if err != nil {
					return nil, err
				}
				value[index] = updated
			}
		}

		return value, nil
	}
	for _, field := range rootReferenceFields {
		updated, err := rewriteStrings(document[field])
		if err != nil {
			return err
		}
		if updated != nil {
			document[field] = updated
		}
	}
	for _, field := range []string{"patchesJson6902", "patches"} {
		patches, ok := document[field].([]any)
		if !ok {
			continue
		}
		for _, patch := range patches {
			fields, ok := patch.(map[string]any)
			if !ok {
				continue
			}
			if pathValue, ok := fields["path"].(string); ok {
				updated, err := rewrite(pathValue)
				if err != nil {
					return err
				}
				fields["path"] = updated
			}
		}
	}
	if openapi, ok := document["openapi"].(map[string]any); ok {
		if source, ok := openapi["path"].(string); ok {
			updated, err := rewrite(source)
			if err != nil {
				return err
			}
			openapi["path"] = updated
		}
	}
	for _, name := range []string{"configMapGenerator", "secretGenerator"} {
		generators, ok := document[name].([]any)
		if !ok {
			continue
		}
		for _, generator := range generators {
			fields, ok := generator.(map[string]any)
			if !ok {
				continue
			}
			for _, field := range []string{"files", "envs"} {
				updated, err := rewriteStrings(fields[field])
				if err != nil {
					return err
				}
				if updated != nil {
					fields[field] = updated
				}
			}
		}
	}
	if replacements, ok := document["replacements"].([]any); ok {
		for _, replacement := range replacements {
			fields, ok := replacement.(map[string]any)
			if !ok {
				continue
			}
			if source, ok := fields["source"].(map[string]any); ok {
				if filename, ok := source["file"].(string); ok {
					updated, err := rewrite(filename)
					if err != nil {
						return err
					}
					source["file"] = updated
				}
			}
		}
	}
	if charts, ok := document["helmCharts"].([]any); ok {
		for _, chart := range charts {
			fields, ok := chart.(map[string]any)
			if !ok {
				continue
			}
			for _, field := range []string{"repo", "valuesFile"} {
				if source, ok := fields[field].(string); ok {
					updated, err := rewrite(source)
					if err != nil {
						return err
					}
					fields[field] = updated
				}
			}
		}
	}

	return nil
}

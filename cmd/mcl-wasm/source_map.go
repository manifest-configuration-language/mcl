//go:build js && wasm

package main

import (
	"encoding/json"
	"io/fs"
	"path"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/sourceindex"
	"github.com/manifest-configuration-language/mcl/internal/wasmprotocol"
)

func wasmSourceMap(sourceMap engine.ManifestSourceMap, output []byte, files fs.FS, entrypoint, entrypointSource string) wasmprotocol.SourceMap {
	result := wasmprotocol.SourceMap{Complete: true, Entries: make([]wasmprotocol.SourceMapEntry, 0)}
	outputIndex := sourceindex.New(string(output))
	sourceIndices := make(map[string]*sourceindex.Index)
	sourceLengths := make(map[string]int)
	missingSources := make(map[string]bool)
	usedBytes := 0
	if encoded, err := json.Marshal(result); err == nil {
		usedBytes = len(encoded) - len("[]")
	} else {
		result.Complete = false
		return result
	}

	for _, document := range sourceMap.Documents {
		for _, entry := range document.Entries {
			if !entry.HasSource {
				continue
			}
			if entry.OutputStart < 0 || entry.OutputEnd < entry.OutputStart || entry.OutputEnd > len(output) {
				result.Complete = false
				continue
			}
			origins := make([]wasmprotocol.SourceMapOrigin, 0, len(entry.Origins))
			for _, origin := range entry.Origins {
				if !mclSourceOrigin(origin.Kind) {
					continue
				}
				projectPath, ok := sourceMapProjectPath(origin.Path, entrypoint)
				if !ok {
					result.Complete = false
					continue
				}
				sourceIndex, exists := sourceIndices[projectPath]
				if !exists && !missingSources[projectPath] {
					source, found := sourceMapSource(files, projectPath, entrypoint, entrypointSource)
					if found {
						sourceIndex = sourceindex.New(source)
						sourceIndices[projectPath] = sourceIndex
						sourceLengths[projectPath] = len(source)
					} else {
						missingSources[projectPath] = true
					}
				}
				if sourceIndex == nil {
					result.Complete = false
					continue
				}
				if origin.Span.Start < 0 || origin.Span.End < origin.Span.Start || origin.Span.End > sourceLengths[projectPath] {
					result.Complete = false
					continue
				}
				kind, ok := wasmSourceOriginKind(origin.Kind)
				if !ok {
					result.Complete = false
					continue
				}
				multiplicity, ok := wasmSourceOriginMultiplicity(origin.Multiplicity)
				if !ok {
					result.Complete = false
					continue
				}
				origins = append(origins, wasmprotocol.SourceMapOrigin{
					Path: wasmprotocol.ProjectPath(projectPath), Range: wasmSourceRange(sourceIndex, origin.Span.Start, origin.Span.End),
					Kind: kind, Multiplicity: multiplicity, Occurrences: uint64(origin.Occurrences),
				})
			}
			if len(origins) == 0 {
				continue
			}
			kind := wasmprotocol.SourceMapEntryKindValue
			if entry.Kind == "key" {
				kind = wasmprotocol.SourceMapEntryKindKey
			}
			candidate := wasmprotocol.SourceMapEntry{
				Document: uint64(document.Index), Path: entry.Path, Kind: kind,
				Output: wasmSourceRange(outputIndex, entry.OutputStart, entry.OutputEnd), Sources: origins,
			}
			encoded, err := json.Marshal(candidate)
			entryBytes := len(encoded)
			if len(result.Entries) > 0 {
				entryBytes++
			}
			if err != nil || usedBytes+entryBytes > wasmprotocol.MaxSourceMapBytes {
				result.Complete = false
				return result
			}
			usedBytes += entryBytes
			result.Entries = append(result.Entries, candidate)
		}
	}

	return result
}

func sourceMapProjectPath(sourcePath, entrypoint string) (string, bool) {
	if sourcePath == "" {
		return "", false
	}
	cleaned := path.Clean(sourcePath)
	root := browserProjectRoot()
	if cleaned == root {
		cleaned = entrypoint
	} else if strings.HasPrefix(cleaned, root+"/") {
		cleaned = strings.TrimPrefix(cleaned, root+"/")
	} else if path.IsAbs(cleaned) {
		return "", false
	}
	if !validProjectPath(cleaned) {
		return "", false
	}

	return cleaned, true
}

func sourceMapSource(files fs.FS, sourcePath, entrypoint, entrypointSource string) (string, bool) {
	if sourcePath == entrypoint {
		return entrypointSource, true
	}
	if files == nil {
		return "", false
	}
	source, err := fs.ReadFile(files, sourcePath)
	if err != nil {
		return "", false
	}

	return string(source), true
}

func mclSourceOrigin(kind engine.SourceOriginKind) bool {
	_, ok := wasmSourceOriginKind(kind)
	return ok
}

func wasmSourceOriginKind(kind engine.SourceOriginKind) (wasmprotocol.SourceMapOriginKind, bool) {
	switch kind {
	case engine.SourceOriginMCL:
		return wasmprotocol.SourceMapOriginKindMcl, true
	case engine.SourceOriginMCLOverlayBase:
		return wasmprotocol.SourceMapOriginKindMclOverlayBase, true
	case engine.SourceOriginMCLOverlayPatch:
		return wasmprotocol.SourceMapOriginKindMclOverlayPatch, true
	default:
		return "", false
	}
}

func wasmSourceOriginMultiplicity(multiplicity engine.SourceOriginMultiplicity) (wasmprotocol.SourceMapOriginMultiplicity, bool) {
	switch multiplicity {
	case engine.SourceOriginUnique:
		return wasmprotocol.SourceMapOriginMultiplicityUnique, true
	case engine.SourceOriginRepeated:
		return wasmprotocol.SourceMapOriginMultiplicityRepeated, true
	case engine.SourceOriginAmbiguous:
		return wasmprotocol.SourceMapOriginMultiplicityAmbiguous, true
	default:
		return "", false
	}
}

func wasmSourceRange(index *sourceindex.Index, start, end int) wasmprotocol.SourceRange {
	startPosition := index.PositionAt(start)
	endPosition := index.PositionAt(end)
	return wasmprotocol.SourceRange{
		Start: wasmprotocol.CompletionPosition{Line: uint64(startPosition.Line), Character: uint64(startPosition.Character)},
		End:   wasmprotocol.CompletionPosition{Line: uint64(endPosition.Line), Character: uint64(endPosition.Character)},
	}
}

package lsp

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/manifest-configuration-language/mcl/internal/sourceindex"
	"github.com/manifest-configuration-language/mcl/internal/syntax"
	"github.com/manifest-configuration-language/mcl/pkg/mcl"
)

const diagnosticDebounceInterval = 100 * time.Millisecond

type diagnosticResult struct {
	generation  uint64
	versions    map[string]int
	states      map[string]documentState
	pendingURIs []string
}

type diagnosticRelatedInformation struct {
	Location struct {
		URI   string `json:"uri"`
		Range struct {
			Start location `json:"start"`
			End   location `json:"end"`
		} `json:"range"`
	} `json:"location"`
	Message string `json:"message"`
}

type diagnostic struct {
	Range struct {
		Start location `json:"start"`
		End   location `json:"end"`
	} `json:"range"`
	Severity           int                            `json:"severity"`
	Code               string                         `json:"code,omitzero"`
	Source             string                         `json:"source"`
	Message            string                         `json:"message"`
	RelatedInformation []diagnosticRelatedInformation `json:"relatedInformation,omitempty"`
}

func (s *server) diagnosticsForDocument(ctx context.Context, uri, source string, includeEvaluation bool) []diagnostic {
	byURI, _ := s.diagnosticsByURIForDocument(ctx, uri, source, includeEvaluation)
	diagnosticURIs := make([]string, 0, len(byURI))
	for diagnosticURI := range byURI {
		diagnosticURIs = append(diagnosticURIs, diagnosticURI)
	}
	slices.Sort(diagnosticURIs)
	var diagnostics []diagnostic
	for _, diagnosticURI := range diagnosticURIs {
		diagnostics = append(diagnostics, byURI[diagnosticURI]...)
	}

	return diagnostics
}

func (s *server) diagnosticsByURIForDocument(ctx context.Context, uri, source string, includeEvaluation bool) (map[string][]diagnostic, []string) {
	diagnostics := make(map[string][]diagnostic)
	appendDiagnostic := func(uri string, value diagnostic) {
		diagnostics[uri] = append(diagnostics[uri], value)
	}
	indices := map[string]*sourceindex.Index{uri: sourceindex.New(source)}
	indexFor := func(uri, source string) *sourceindex.Index {
		if index, exists := indices[uri]; exists {
			return index
		}
		index := sourceindex.New(source)
		indices[uri] = index

		return index
	}
	dependencies := []string{documentPath(uri)}
	module, err := syntax.ParseAST(source)
	if err != nil {
		if sourceDiagnostics := collectSourceDiagnostics(err); len(sourceDiagnostics) > 0 {
			for _, sourceDiagnostic := range sourceDiagnostics {
				appendDiagnostic(uri, makeSpanDiagnostic(indices[uri], sourceDiagnostic.Span, sourceDiagnostic.Code, 1, sourceDiagnostic.DisplayMessage()))
			}
		} else {
			appendDiagnostic(uri, makeDiagnostic(0, 1, err.Error()))
		}
	} else {
		for _, warning := range engine.LintAST(module) {
			appendDiagnostic(uri, makeSpanDiagnostic(indices[uri], warning.Span, warning.Code, warning.Severity, warning.Message))
		}
		if includeEvaluation {
			loadedModules, err := evaluateDocument(ctx, uri, source, module, s.overlayFiles())
			dependencies = append(dependencies, loadedModules...)
			if err != nil {
				if sourceDiagnostics := collectSourceDiagnostics(err); len(sourceDiagnostics) > 0 {
					for _, sourceDiagnostic := range sourceDiagnostics {
						diagnosticURI := uri
						diagnosticSource := source
						if sourceDiagnostic.Path != "" {
							diagnosticURI = s.diagnosticURIForPath(sourceDiagnostic.Path)
							diagnosticSource = s.sourceForPath(sourceDiagnostic.Path, source)
						}
						diagnosticIndex := indexFor(diagnosticURI, diagnosticSource)
						result := makeSpanDiagnostic(diagnosticIndex, sourceDiagnostic.Span, sourceDiagnostic.Code, 1, sourceDiagnostic.DisplayMessage())
						for _, related := range sourceDiagnostic.Related {
							if related.Path == "" || related.Span.End <= related.Span.Start {
								continue
							}
							relatedURI := s.diagnosticURIForPath(related.Path)
							relatedFallback := ""
							if relatedURI == diagnosticURI {
								relatedFallback = diagnosticSource
							}
							relatedSource := s.sourceForPath(related.Path, relatedFallback)
							information := diagnosticRelatedInformation{Message: related.Message}
							information.Location.URI = relatedURI
							relatedIndex := indexFor(relatedURI, relatedSource)
							information.Location.Range.Start = sourcePositionAt(relatedIndex, related.Span.Start)
							information.Location.Range.End = sourcePositionAt(relatedIndex, related.Span.End)
							result.RelatedInformation = append(result.RelatedInformation, information)
						}
						appendDiagnostic(diagnosticURI, result)
					}
				} else {
					appendDiagnostic(uri, makeDiagnostic(0, 1, err.Error()))
				}
			}
		}
	}
	dependencies = normalizePaths(dependencies)

	return diagnostics, dependencies
}

func collectSourceDiagnostics(err error) []mcl.SourceDiagnosticError {
	var diagnostics []mcl.SourceDiagnosticError
	var visit func(error)
	visit = func(current error) {
		if current == nil {
			return
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child)
			}

			return
		}
		if diagnostic, ok := current.(mcl.SourceDiagnosticError); ok { //nolint:errorlint // Joinと単一wrapを先にたどるため、ここでは直接型を確認します。
			diagnostics = append(diagnostics, diagnostic)

			return
		}
		if wrapped, ok := current.(interface{ Unwrap() error }); ok {
			visit(wrapped.Unwrap())

			return
		}
		if diagnostic, ok := mcl.SourceDiagnosticFromError(current); ok {
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	visit(err)

	return diagnostics
}

func normalizePaths(paths []string) []string {
	unique := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if _, exists := unique[path]; exists {
			continue
		}
		unique[path] = struct{}{}
		result = append(result, path)
	}
	slices.Sort(result)

	return result
}

func (s *server) scheduleDiagnostics(ctx context.Context, changedURI string, results chan<- diagnosticResult) {
	s.scheduleDiagnosticsForChanges(ctx, []string{changedURI}, results)
}

func (s *server) scheduleDiagnosticsForChanges(ctx context.Context, changedURIs []string, results chan<- diagnosticResult) {
	s.scheduleDiagnosticsForChangesWithMode(ctx, changedURIs, results, true)
}

func (s *server) scheduleStaticDiagnostics(ctx context.Context, uri string, results chan<- diagnosticResult) {
	s.scheduleDiagnosticsForChangesWithMode(ctx, []string{uri}, results, false)
}

func (s *server) scheduleDiagnosticsForChangesWithMode(parent context.Context, changedURIs []string, results chan<- diagnosticResult, includeEvaluation bool) {
	if s.pendingDiagnosticURIs == nil {
		s.pendingDiagnosticURIs = make(map[string]struct{})
	}
	for _, uri := range changedURIs {
		s.pendingDiagnosticURIs[uri] = struct{}{}
	}
	if s.diagnosticCancel != nil {
		s.diagnosticCancel()
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	s.diagnosticCancel = cancel
	s.diagnosticGeneration++
	generation := s.diagnosticGeneration
	documents := make(map[string]documentState, len(s.documents))
	versions := make(map[string]int, len(s.documents))
	for uri, state := range s.documents {
		documents[uri] = state
		versions[uri] = state.Version
	}
	snapshot := &server{documents: documents}
	pendingURIs := make([]string, 0, len(s.pendingDiagnosticURIs))
	pendingPaths := make([]string, 0, len(s.pendingDiagnosticURIs))
	forceAll := false
	for uri := range s.pendingDiagnosticURIs {
		pendingURIs = append(pendingURIs, uri)
		path := documentPath(uri)
		pendingPaths = append(pendingPaths, path)
		if filepath.Base(path) == "mcl.toml" {
			forceAll = true
		}
	}
	slices.Sort(pendingURIs)
	pendingPaths = normalizePaths(pendingPaths)
	targets := make([]string, 0, len(documents))
	for uri, state := range documents {
		if forceAll || hasURI(s.pendingDiagnosticURIs, uri) || dependsOnAnyPath(state.Dependencies, pendingPaths) {
			targets = append(targets, uri)
		}
	}
	slices.Sort(targets)
	changedSet := make(map[string]struct{}, len(changedURIs))
	for _, uri := range changedURIs {
		changedSet[uri] = struct{}{}
	}
	go func() {
		timer := time.NewTimer(diagnosticDebounceInterval)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}

		updatedDocuments := make(map[string]documentState, len(documents))
		maps.Copy(updatedDocuments, documents)
		for _, uri := range targets {
			state := documents[uri]
			if state.SyncLost {
				state.Diagnostics = nil
				updatedDocuments[uri] = state

				continue
			}
			_, sourceChanged := changedSet[uri]
			evaluate := includeEvaluation || !sourceChanged
			diagnostics, dependencies := snapshot.diagnosticsByURIForDocument(ctx, uri, state.Text, evaluate)
			if evaluate {
				state.Dependencies = dependencies
			}
			state.Diagnostics = diagnostics
			updatedDocuments[uri] = state
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case results <- diagnosticResult{generation: generation, versions: versions, states: updatedDocuments, pendingURIs: pendingURIs}:
		case <-ctx.Done():
		}
	}()
}

func hasURI(uris map[string]struct{}, uri string) bool {
	_, exists := uris[uri]

	return exists
}

func (s *server) publishDiagnosticResult(result diagnosticResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if result.generation != s.diagnosticGeneration {
		return nil
	}
	for uri, version := range result.versions {
		state, exists := s.documents[uri]
		if !exists || state.Version != version {
			return nil
		}
	}
	for uri, state := range result.states {
		if current, exists := s.documents[uri]; exists && current.Version == result.versions[uri] {
			s.documents[uri] = state
		}
	}
	currentDiagnostics := make(map[string][]diagnostic, len(s.documents))
	documentURIs := make([]string, 0, len(s.documents))
	for uri := range s.documents {
		documentURIs = append(documentURIs, uri)
	}
	slices.Sort(documentURIs)
	for _, documentURI := range documentURIs {
		state := s.documents[documentURI]
		diagnosticURIs := make([]string, 0, len(state.Diagnostics))
		for uri := range state.Diagnostics {
			diagnosticURIs = append(diagnosticURIs, uri)
		}
		slices.Sort(diagnosticURIs)
		for _, uri := range diagnosticURIs {
			diagnostics := state.Diagnostics[uri]
			currentDiagnostics[uri] = append(currentDiagnostics[uri], diagnostics...)
		}
	}
	currentURIs := make(map[string]struct{}, len(currentDiagnostics))
	for uri := range currentDiagnostics {
		currentURIs[uri] = struct{}{}
	}
	notificationURIs := maps.Clone(currentURIs)
	previousURIs := s.diagnosticURIs
	for uri := range previousURIs {
		if _, exists := currentDiagnostics[uri]; !exists {
			currentDiagnostics[uri] = []diagnostic{}
		}
		notificationURIs[uri] = struct{}{}
	}
	notificationURIList := make([]string, 0, len(notificationURIs))
	for uri := range notificationURIs {
		notificationURIList = append(notificationURIList, uri)
	}
	slices.Sort(notificationURIList)
	for _, uri := range notificationURIList {
		if err := s.notify("textDocument/publishDiagnostics", map[string]any{"uri": uri, "diagnostics": currentDiagnostics[uri]}); err != nil {
			return err
		}
	}
	s.diagnosticURIs = currentURIs
	for _, uri := range result.pendingURIs {
		delete(s.pendingDiagnosticURIs, uri)
	}

	return nil
}

func dependsOnAnyPath(dependencies, changedPaths []string) bool {
	for _, path := range changedPaths {
		if pathListContains(dependencies, path) {
			return true
		}
	}

	return false
}

func pathListContains(paths []string, target string) bool {
	if target == "" {
		return false
	}
	for _, path := range paths {
		if filepath.Clean(path) == filepath.Clean(target) {
			return true
		}
	}

	return false
}

func makeSpanDiagnostic(index *sourceindex.Index, span syntax.SourceSpan, code string, severity int, message string) diagnostic {
	result := makeDiagnostic(0, severity, message)
	result.Code = code
	result.Range.Start = sourcePositionAt(index, span.Start)
	result.Range.End = sourcePositionAt(index, span.End)

	return result
}

func sourcePositionAt(index *sourceindex.Index, offset int) location {
	position := index.PositionAt(offset)

	return location{Line: position.Line, Character: position.Character}
}

func evaluateDocument(ctx context.Context, uri, source string, module syntax.ModuleAST, overlays map[string]string) ([]string, error) {
	if strings.HasPrefix(uri, "file:") {
		path, ok := fileURIToPath(uri)
		if !ok || path == "" {
			return nil, fmt.Errorf("non-local file URI %q is not supported", uri)
		}
		_, dependencies, err := mcl.EvaluateSourceWithOverlaysAndDependenciesContext(ctx, path, source, overlays)

		return dependencies, err
	}
	//nolint:contextcheck // AST評価APIはContextを受け取らず、解析処理は同期的に完了します。
	_, err := engine.EvaluateAST(module)

	return nil, err
}

func makeDiagnostic(line, severity int, message string) diagnostic {
	result := diagnostic{Severity: severity, Source: "mcl", Message: message}
	result.Range.Start = location{Line: line}
	result.Range.End = location{Line: line, Character: 1}

	return result
}

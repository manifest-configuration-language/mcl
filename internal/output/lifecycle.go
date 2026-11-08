package output

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/kubernetes"
	"github.com/manifest-configuration-language/mcl/internal/value"
)

const (
	PhaseMain           = "main"
	PhaseBootstrap      = "bootstrap"
	PhaseBeforeCreate   = "before.create"
	PhaseAfterCreate    = "after.create"
	PhaseBeforeUpdate   = "before.update"
	PhaseAfterUpdate    = "after.update"
	PhaseBeforeDelete   = "before.delete"
	PhaseAfterDelete    = "after.delete"
	PhaseBeforeRollback = "before.rollback"
	PhaseAfterRollback  = "after.rollback"
	PhaseVerify         = "verify"

	OperationCreate   = "create"
	OperationUpdate   = "update"
	OperationDelete   = "delete"
	OperationRollback = "rollback"
	OperationVerify   = "verify"

	cleanupBeforeNextRun = "before-next-run"
	cleanupOnFailure     = "on-failure"
	cleanupOnSuccess     = "on-success"
)

type BundleOutputPlan struct {
	artifacts      []value.Artifact
	phases         []string
	phaseArtifacts map[string][]indexedArtifact
	lifecycleErr   error
	scopes         kubernetes.ScopeIndex
	scopeErr       error
	validated      map[string]bool
	validatedAll   bool
	rendered       map[string][]byte
}

type indexedArtifact struct {
	index      int
	artifact   value.Artifact
	artifactID string
}

type RenderedFile struct {
	Name    string
	Content []byte
}

type RenderedArtifact struct {
	Resource  jsontext.Value
	Lifecycle value.Lifecycle
	// ArtifactID identifies an artifact by its unique source location across evaluations; it is empty when provenance is absent or ambiguous.
	ArtifactID string
	// BundleIndex is the artifact's position in this evaluated Bundle and is only stable within that Bundle.
	BundleIndex int
}

type lifecycleManifest struct {
	Operation string                    `json:"operation"`
	Artifacts []lifecycleArtifactRecord `json:"artifacts"`
}
type lifecycleArtifactRecord struct {
	Index     int                          `json:"index"`
	Events    []string                     `json:"events"`
	Priority  int                          `json:"priority"`
	Cleanup   []string                     `json:"cleanup"`
	Wait      bool                         `json:"wait"`
	Origin    string                       `json:"origin,omitempty"`
	Documents []lifecycleDocumentReference `json:"documents"`
}
type lifecycleDocumentReference struct {
	File     string `json:"file"`
	Document int    `json:"document"`
}

// NewBundleOutputPlan groups artifacts by lifecycle phase while retaining the scope snapshot used for validation.
func NewBundleOutputPlan(artifacts []value.Artifact, scopes kubernetes.ScopeIndex, scopeErr error) *BundleOutputPlan {
	artifactIDs := StableArtifactIDs(artifacts)
	plan := &BundleOutputPlan{
		artifacts:      artifacts,
		phaseArtifacts: make(map[string][]indexedArtifact),
		lifecycleErr:   ValidateLifecycleMetadata(artifacts),
		scopes:         scopes,
		scopeErr:       scopeErr,
		validated:      make(map[string]bool),
		rendered:       make(map[string][]byte),
	}
	for index, artifact := range artifacts {
		seenPhases := make(map[string]bool, len(artifact.Lifecycle.Events))
		for _, phase := range artifact.Lifecycle.Events {
			if seenPhases[phase] {
				continue
			}
			seenPhases[phase] = true
			if _, exists := plan.phaseArtifacts[phase]; !exists {
				plan.phases = append(plan.phases, phase)
				plan.phaseArtifacts[phase] = nil
			}
			plan.phaseArtifacts[phase] = append(plan.phaseArtifacts[phase], indexedArtifact{index: index, artifact: artifact, artifactID: artifactIDs[index]})
		}
	}
	for _, artifacts := range plan.phaseArtifacts {
		slices.SortStableFunc(artifacts, func(left, right indexedArtifact) int {
			if left.artifact.Lifecycle.Priority < right.artifact.Lifecycle.Priority {
				return -1
			}
			if left.artifact.Lifecycle.Priority > right.artifact.Lifecycle.Priority {
				return 1
			}
			return 0
		})
	}
	return plan
}

func (plan *BundleOutputPlan) Validate() error {
	if plan.lifecycleErr != nil {
		return plan.lifecycleErr
	}
	for _, phase := range plan.phases {
		if !ValidLifecycleEvent(phase) {
			return fmt.Errorf("phase %q: unknown output phase %q", phase, phase)
		}
		if err := plan.validatePhase(phase); err != nil {
			return fmt.Errorf("phase %q: %w", phase, err)
		}
	}
	plan.validatedAll = true
	return nil
}

func (plan *BundleOutputPlan) RenderPhase(phase string) ([]byte, error) {
	if !ValidLifecycleEvent(phase) {
		return nil, fmt.Errorf("unknown output phase %q", phase)
	}
	if err := plan.validatePhase(phase); err != nil {
		return nil, err
	}
	if len(plan.phaseArtifacts[phase]) == 0 {
		return []byte{}, nil
	}
	return append([]byte{}, plan.rendered[phase]...), nil
}

func (plan *BundleOutputPlan) RenderPhaseJSON(phase string) ([]byte, error) {
	if !ValidLifecycleEvent(phase) {
		return nil, fmt.Errorf("unknown output phase %q", phase)
	}
	if err := plan.validatePhase(phase); err != nil {
		return nil, err
	}
	return RenderArtifactsJSON(artifactsForPhase(plan.phaseArtifacts[phase]))
}

func (plan *BundleOutputPlan) RenderPhaseWithSourceMap(phase string) ([]byte, ManifestSourceMap, error) {
	if !ValidLifecycleEvent(phase) {
		return nil, ManifestSourceMap{}, fmt.Errorf("unknown output phase %q", phase)
	}
	if err := plan.validatePhase(phase); err != nil {
		return nil, ManifestSourceMap{}, err
	}
	rendered := append([]byte{}, plan.rendered[phase]...)
	artifacts := artifactsForPhase(plan.phaseArtifacts[phase])
	sourceMap, err := ManifestSourceMapForArtifacts(rendered, artifacts)
	if err != nil {
		return nil, ManifestSourceMap{}, err
	}
	return rendered, sourceMap, nil
}

func (plan *BundleOutputPlan) validatePhase(phase string) error {
	if plan.lifecycleErr != nil {
		return plan.lifecycleErr
	}
	if plan.scopeErr != nil {
		return plan.scopeErr
	}
	if plan.validatedAll || plan.validated[phase] {
		return nil
	}
	artifacts := plan.phaseArtifacts[phase]
	if err := validatePhaseArtifacts(artifacts, plan.scopes); err != nil {
		return err
	}
	output, err := RenderArtifacts(artifactsForPhase(artifacts))
	if err != nil {
		return err
	}
	plan.rendered[phase] = output
	plan.validated[phase] = true
	return nil
}

func validatePhaseArtifacts(artifacts []indexedArtifact, scopes kubernetes.ScopeIndex) error {
	values := make([]value.Artifact, len(artifacts))
	for index, artifact := range artifacts {
		if artifact.artifact.Value.Kind == value.List {
			return fmt.Errorf("artifact %d is a List; Kubernetes output requires separate resources, use `emit each`", artifact.index+1)
		}
		values[index] = artifact.artifact
	}
	return kubernetes.ValidateResourceArtifacts(values, scopes)
}

func artifactsForPhase(artifacts []indexedArtifact) []value.Artifact {
	values := make([]value.Artifact, len(artifacts))
	for index, artifact := range artifacts {
		values[index] = artifact.artifact
	}
	return values
}

func (plan *BundleOutputPlan) RenderPhaseArtifacts(phase string) ([]RenderedArtifact, error) {
	if !ValidLifecycleEvent(phase) {
		return nil, fmt.Errorf("unknown output phase %q", phase)
	}
	if err := plan.validatePhase(phase); err != nil {
		return nil, err
	}
	indexed := plan.phaseArtifacts[phase]
	result := make([]RenderedArtifact, 0, len(indexed))
	for _, artifact := range indexed {
		var resource bytes.Buffer
		if err := AppendJSONValue(&resource, artifact.artifact.Value); err != nil {
			return nil, fmt.Errorf("render artifact %d as JSON: %w", artifact.index+1, err)
		}
		lifecycle := artifact.artifact.Lifecycle
		lifecycle.Events = slices.Clone(lifecycle.Events)
		lifecycle.Cleanup = slices.Clone(lifecycle.Cleanup)
		result = append(result, RenderedArtifact{
			Resource:    jsontext.Value(resource.Bytes()),
			Lifecycle:   lifecycle,
			ArtifactID:  artifact.artifactID,
			BundleIndex: artifact.index,
		})
	}
	return result, nil
}

func (plan *BundleOutputPlan) RenderOperation(operation string) ([]RenderedFile, error) {
	return plan.renderOperation(operation, "yaml")
}

func (plan *BundleOutputPlan) RenderOperationWithFormat(operation, format string) ([]RenderedFile, error) {
	if format != "yaml" && format != "json" {
		return nil, fmt.Errorf("unknown output format %q", format)
	}
	return plan.renderOperation(operation, format)
}

func (plan *BundleOutputPlan) renderOperation(operation, format string) ([]RenderedFile, error) {
	if !ValidOperation(operation) {
		return nil, fmt.Errorf("unknown operation %q", operation)
	}
	if plan.lifecycleErr != nil {
		return nil, plan.lifecycleErr
	}
	var phases []string
	switch operation {
	case OperationCreate:
		phases = []string{PhaseBootstrap, PhaseBeforeCreate, PhaseMain, PhaseAfterCreate}
	case OperationUpdate:
		phases = []string{PhaseBeforeUpdate, PhaseMain, PhaseAfterUpdate}
	case OperationDelete:
		phases = []string{PhaseBeforeDelete, PhaseMain, PhaseAfterDelete}
	case OperationRollback:
		phases = []string{PhaseBeforeRollback, PhaseMain, PhaseAfterRollback}
	case OperationVerify:
		phases = []string{PhaseVerify}
	default:
		return nil, fmt.Errorf("unknown operation %q", operation)
	}
	extension := ".yaml"
	if format == "json" {
		extension = ".json"
	}
	bootstrapFile, beforeFile, mainFile := "bootstrap"+extension, "before"+extension, "main"+extension
	afterFile, verifyFile := "after"+extension, "verify"+extension
	phaseGroups := map[string][]string{bootstrapFile: {PhaseBootstrap}, beforeFile: {}, mainFile: {PhaseMain}, afterFile: {}, verifyFile: {PhaseVerify}}
	for _, phase := range phases {
		switch {
		case phase == PhaseBootstrap, phase == PhaseMain, phase == PhaseVerify:
		case strings.HasPrefix(phase, "before."):
			phaseGroups[beforeFile] = append(phaseGroups[beforeFile], phase)
		case strings.HasPrefix(phase, "after."):
			phaseGroups[afterFile] = append(phaseGroups[afterFile], phase)
		}
	}
	files := make([]RenderedFile, 0, len(phaseGroups)+1)
	documents := make(map[int][]lifecycleDocumentReference, len(plan.artifacts))
	for _, name := range []string{bootstrapFile, beforeFile, mainFile, afterFile, verifyFile} {
		var chunks [][]byte
		var operationArtifacts []indexedArtifact
		document := 0
		for _, phase := range phaseGroups[name] {
			if err := plan.validatePhase(phase); err != nil {
				return nil, err
			}
			artifacts, chunk := plan.phaseArtifacts[phase], plan.rendered[phase]
			if len(chunk) != 0 {
				if format == "yaml" {
					chunks = append(chunks, chunk)
				} else {
					operationArtifacts = append(operationArtifacts, artifacts...)
				}
				for _, artifact := range artifacts {
					document++
					documents[artifact.index] = append(documents[artifact.index], lifecycleDocumentReference{File: name, Document: document})
				}
			}
		}
		content := combineYAMLDocuments(chunks)
		if format == "json" {
			var err error
			content, err = RenderArtifactsJSON(artifactsForPhase(operationArtifacts))
			if err != nil {
				return nil, err
			}
		}
		files = append(files, RenderedFile{Name: name, Content: content})
	}
	selected := make(map[string]bool, len(phases))
	for _, phase := range phases {
		selected[phase] = true
	}
	metadata := lifecycleManifest{Operation: operation, Artifacts: make([]lifecycleArtifactRecord, 0)}
	for index, artifact := range plan.artifacts {
		included := false
		for _, event := range artifact.Lifecycle.Events {
			included = included || selected[event]
		}
		if !included {
			continue
		}
		cleanup := append([]string{}, artifact.Lifecycle.Cleanup...)
		metadata.Artifacts = append(metadata.Artifacts, lifecycleArtifactRecord{Index: index, Events: slices.Clone(artifact.Lifecycle.Events), Priority: artifact.Lifecycle.Priority, Cleanup: cleanup, Wait: artifact.Lifecycle.Wait, Origin: artifact.Lifecycle.Origin, Documents: documents[index]})
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode lifecycle metadata: %w", err)
	}
	formatted := jsontext.Value(data)
	if err := formatted.Indent(jsontext.WithIndent("  ")); err != nil {
		return nil, fmt.Errorf("format lifecycle metadata: %w", err)
	}
	files = append(files, RenderedFile{Name: "lifecycle.json", Content: append(formatted, '\n')})
	return files, nil
}

func ValidateLifecycleMetadata(artifacts []value.Artifact) error {
	for index, artifact := range artifacts {
		lifecycle := artifact.Lifecycle
		if len(lifecycle.Events) == 0 {
			return fmt.Errorf("artifact %d has no lifecycle events", index+1)
		}
		seenEvents := make(map[string]struct{}, len(lifecycle.Events))
		for _, event := range lifecycle.Events {
			if !ValidLifecycleEvent(event) {
				return fmt.Errorf("artifact %d has unknown lifecycle event %q", index+1, event)
			}
			if _, exists := seenEvents[event]; exists {
				return fmt.Errorf("artifact %d repeats lifecycle event %q", index+1, event)
			}
			seenEvents[event] = struct{}{}
		}
		if len(lifecycle.Events) > 1 {
			if _, hasMain := seenEvents[PhaseMain]; hasMain {
				return fmt.Errorf("artifact %d cannot combine lifecycle event %q with other events", index+1, PhaseMain)
			}
		}
		seenCleanup := make(map[string]struct{}, len(lifecycle.Cleanup))
		for _, cleanup := range lifecycle.Cleanup {
			switch cleanup {
			case cleanupBeforeNextRun, cleanupOnSuccess, cleanupOnFailure:
			default:
				return fmt.Errorf("artifact %d has unsupported lifecycle cleanup policy %q", index+1, cleanup)
			}
			if _, exists := seenCleanup[cleanup]; exists {
				return fmt.Errorf("artifact %d repeats lifecycle cleanup policy %q", index+1, cleanup)
			}
			seenCleanup[cleanup] = struct{}{}
		}
	}
	return nil
}

func ValidLifecycleEvent(event string) bool {
	switch event {
	case PhaseMain, PhaseBootstrap, PhaseBeforeCreate, PhaseAfterCreate, PhaseBeforeUpdate, PhaseAfterUpdate, PhaseBeforeDelete, PhaseAfterDelete, PhaseBeforeRollback, PhaseAfterRollback, PhaseVerify:
		return true
	default:
		return false
	}
}

func ValidOperation(operation string) bool {
	switch operation {
	case OperationCreate, OperationUpdate, OperationDelete, OperationRollback, OperationVerify:
		return true
	default:
		return false
	}
}

// StableArtifactIDs returns source-location IDs for unambiguous artifacts in a Bundle.
func StableArtifactIDs(artifacts []value.Artifact) []string {
	ids := make([]string, len(artifacts))
	keys := make([]string, len(artifacts))
	counts := make(map[string]int, len(artifacts))
	for index, artifact := range artifacts {
		path, span := artifact.Value.SourcePath, artifact.Value.Span
		if path == "" || span.Start <= 0 || span.End <= span.Start {
			continue
		}
		key := fmt.Sprintf("%s\x00%d\x00%d", path, span.Start, span.End)
		keys[index] = key
		counts[key]++
	}
	for index, key := range keys {
		if key == "" || counts[key] != 1 {
			continue
		}
		digest := sha256.Sum256([]byte(key))
		ids[index] = "artifact-" + hex.EncodeToString(digest[:12])
	}

	return ids
}

func combineYAMLDocuments(chunks [][]byte) []byte {
	var output bytes.Buffer
	for _, chunk := range chunks {
		trimmed := bytes.TrimSpace(chunk)
		if len(trimmed) == 0 {
			continue
		}
		if output.Len() > 0 {
			output.WriteString("\n---\n")
		}
		output.Write(trimmed)
		output.WriteByte('\n')
	}
	return output.Bytes()
}

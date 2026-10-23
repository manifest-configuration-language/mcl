package engine

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func Render(bundle Bundle) ([]byte, error) {
	return RenderPhase(bundle, "main")
}

// RenderWithSourceMapはmain phaseのYAMLとManifest source mapを返します。
func RenderWithSourceMap(bundle Bundle) ([]byte, ManifestSourceMap, error) {
	return RenderPhaseWithSourceMap(bundle, phaseMain)
}

func ValidateBundleOutput(bundle Bundle) error {
	plan := NewBundleOutputPlan(bundle)

	return plan.Validate()
}

// ValidateBundleOutputWithCatalogは指定CatalogとBundle内CRDを使って出力検証します。
func ValidateBundleOutputWithCatalog(bundle Bundle, catalog KubernetesResourceCatalog) error {
	plan, err := NewBundleOutputPlanWithCatalog(bundle, catalog)
	if err != nil {
		return err
	}

	return plan.Validate()
}

func RenderPhase(bundle Bundle, phase string) ([]byte, error) {
	if !validLifecycleEvent(phase) {
		return nil, fmt.Errorf("unknown output phase %q", phase)
	}
	plan := NewBundleOutputPlan(bundle)

	return plan.RenderPhase(phase)
}

// RenderPhaseWithSourceMapは指定phaseのYAMLとManifest source mapを返します。
func RenderPhaseWithSourceMap(bundle Bundle, phase string) ([]byte, ManifestSourceMap, error) {
	if !validLifecycleEvent(phase) {
		return nil, ManifestSourceMap{}, fmt.Errorf("unknown output phase %q", phase)
	}

	return NewBundleOutputPlan(bundle).RenderPhaseWithSourceMap(phase)
}

// BundleOutputPlanは検証と出力に使うBundle全体のKubernetes Scope indexと、priority順のArtifact群を保持します。Planは入力BundleとValueを浅く参照するため、構築後に入力を変更してはいけません。同一Planのメソッドを並行して呼び出すことはできません。
type BundleOutputPlan struct {
	bundle       Bundle
	phases       []string
	artifacts    map[string][]indexedArtifact
	lifecycleErr error
	scopes       kubernetesResourceScopeIndex
	scopeErr     error
	validated    map[string]bool
	validatedAll bool
	rendered     map[string][]byte
}

// NewBundleOutputPlanはBundle全体のScope indexとphaseごとのArtifact群を構築します。phaseのYAML検証はせず、lifecycle metadataの検証エラーは出力メソッドで返します。戻り値は入力BundleとValueを浅く参照するため、以後それらを変更してはいけません。同一Planのメソッドを並行して呼び出すことはできません。
func NewBundleOutputPlan(bundle Bundle) *BundleOutputPlan {
	scopes, err := newKubernetesResourceScopeIndex(bundle.Artifacts)

	return newBundleOutputPlan(bundle, scopes, err)
}

// NewBundleOutputPlanWithCatalogは指定CatalogとBundle内CRDからScope indexを構築します。Catalogのrevision、追加Descriptor、Bundle内CRDに問題がある場合はエラーを返します。
func NewBundleOutputPlanWithCatalog(bundle Bundle, catalog KubernetesResourceCatalog) (*BundleOutputPlan, error) {
	scopes, err := newKubernetesResourceScopeIndexWithCatalog(bundle.Artifacts, catalog)
	if err != nil {
		return nil, err
	}

	return newBundleOutputPlan(bundle, scopes, nil), nil
}

func newBundleOutputPlan(bundle Bundle, scopes kubernetesResourceScopeIndex, scopeErr error) *BundleOutputPlan {
	plan := &BundleOutputPlan{
		bundle:       bundle,
		artifacts:    make(map[string][]indexedArtifact),
		lifecycleErr: validateLifecycleMetadata(bundle.Artifacts),
		scopes:       scopes,
		scopeErr:     scopeErr,
		validated:    make(map[string]bool),
		rendered:     make(map[string][]byte),
	}
	for index, artifact := range bundle.Artifacts {
		seenPhases := make(map[string]bool, len(artifact.Lifecycle.Events))
		for _, phase := range artifact.Lifecycle.Events {
			if seenPhases[phase] {
				continue
			}
			seenPhases[phase] = true
			if _, exists := plan.artifacts[phase]; !exists {
				plan.phases = append(plan.phases, phase)
				plan.artifacts[phase] = nil
			}
			plan.artifacts[phase] = append(plan.artifacts[phase], indexedArtifact{index: index, artifact: artifact})
		}
	}
	for _, artifacts := range plan.artifacts {
		sort.SliceStable(artifacts, func(left, right int) bool {
			return artifacts[left].artifact.Lifecycle.Priority < artifacts[right].artifact.Lifecycle.Priority
		})
	}

	return plan
}

// ValidateはBundle内のすべてのphaseを出現順に検証します。
func (plan *BundleOutputPlan) Validate() error {
	if plan.lifecycleErr != nil {
		return plan.lifecycleErr
	}
	for _, phase := range plan.phases {
		if !validLifecycleEvent(phase) {
			return fmt.Errorf("phase %q: unknown output phase %q", phase, phase)
		}
		if err := plan.validatePhase(phase); err != nil {
			return fmt.Errorf("phase %q: %w", phase, err)
		}
	}
	plan.validatedAll = true

	return nil
}

// RenderPhaseは指定したlifecycle phaseを検証して出力します。
func (plan *BundleOutputPlan) RenderPhase(phase string) ([]byte, error) {
	if !validLifecycleEvent(phase) {
		return nil, fmt.Errorf("unknown output phase %q", phase)
	}
	if err := plan.validatePhase(phase); err != nil {
		return nil, err
	}
	if len(plan.artifacts[phase]) == 0 {
		return []byte{}, nil
	}

	return append([]byte{}, plan.rendered[phase]...), nil
}

// RenderPhaseJSONは指定したlifecycle phaseを検証し、ArtifactをJSON配列で出力します。
func (plan *BundleOutputPlan) RenderPhaseJSON(phase string) ([]byte, error) {
	if !validLifecycleEvent(phase) {
		return nil, fmt.Errorf("unknown output phase %q", phase)
	}
	if err := plan.validatePhase(phase); err != nil {
		return nil, err
	}

	return renderArtifactsJSON(plan.artifacts[phase])
}

// RenderPhaseWithSourceMapは指定phaseのYAMLとManifest source mapを返します。
func (plan *BundleOutputPlan) RenderPhaseWithSourceMap(phase string) ([]byte, ManifestSourceMap, error) {
	if !validLifecycleEvent(phase) {
		return nil, ManifestSourceMap{}, fmt.Errorf("unknown output phase %q", phase)
	}
	if err := plan.validatePhase(phase); err != nil {
		return nil, ManifestSourceMap{}, err
	}
	output := append([]byte{}, plan.rendered[phase]...)
	sourceMap, err := manifestSourceMap(output, plan.artifacts[phase])
	if err != nil {
		return nil, ManifestSourceMap{}, err
	}

	return output, sourceMap, nil
}

type indexedArtifact struct {
	index    int
	artifact Artifact
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
	if err := validatePhaseArtifacts(plan.artifacts[phase], plan.scopes); err != nil {
		return err
	}
	output, err := renderArtifacts(plan.artifacts[phase])
	if err != nil {
		return err
	}
	plan.rendered[phase] = output
	plan.validated[phase] = true

	return nil
}

func validatePhaseArtifacts(artifacts []indexedArtifact, scopes kubernetesResourceScopeIndex) error {
	values := make([]Artifact, len(artifacts))
	for index, artifact := range artifacts {
		if artifact.artifact.Value.Kind == List {
			return fmt.Errorf("artifact %d is a List; Kubernetes output requires separate resources, use `emit each`", artifact.index+1)
		}
		values[index] = artifact.artifact
	}

	return rejectDuplicateKubernetesResources(values, scopes)
}

func renderArtifacts(artifacts []indexedArtifact) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if len(artifacts) == 0 {
		return []byte{}, nil
	}
	for _, artifact := range artifacts {
		node, err := yamlNode(artifact.artifact.Value)
		if err != nil {
			return nil, err
		}
		if err := encoder.Encode(node); err != nil {
			return nil, err
		}
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("encode YAML: %w", err)
	}

	return output.Bytes(), nil
}

func renderArtifactsJSON(artifacts []indexedArtifact) ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('[')
	for index, artifact := range artifacts {
		if index > 0 {
			output.WriteByte(',')
		}
		output.WriteByte('\n')
		output.WriteString("  ")
		if err := appendJSONValue(&output, artifact.artifact.Value); err != nil {
			return nil, err
		}
	}
	if len(artifacts) > 0 {
		output.WriteByte('\n')
	}
	output.WriteByte(']')
	formatted := jsontext.Value(output.Bytes())
	if err := formatted.Indent(jsontext.WithIndent("  ")); err != nil {
		return nil, fmt.Errorf("format JSON output: %w", err)
	}

	return append(formatted, '\n'), nil
}

func appendJSONValue(output *bytes.Buffer, value Value) error {
	value = nodeContents(value)
	switch value.Kind {
	case Null:
		output.WriteString("null")
	case Bool:
		output.WriteString(strconv.FormatBool(value.Bool))
	case Integer, Decimal:
		if !numberPattern.MatchString(value.Text) {
			return fmt.Errorf("cannot render invalid JSON number %q", value.Text)
		}
		output.WriteString(value.Text)
	case String:
		encoded, err := json.Marshal(value.Text)
		if err != nil {
			return fmt.Errorf("encode JSON string: %w", err)
		}
		output.Write(encoded)
	case List, AdapterBundle:
		output.WriteByte('[')
		for index, item := range value.List {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := appendJSONValue(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case Map:
		output.WriteByte('{')
		for index, entry := range value.Map {
			if index > 0 {
				output.WriteByte(',')
			}
			key, err := json.Marshal(entry.Key)
			if err != nil {
				return fmt.Errorf("encode JSON object key: %w", err)
			}
			output.Write(key)
			output.WriteByte(':')
			if err := appendJSONValue(output, entry.Value); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	case Rule:
		return errors.New("rule values cannot be emitted")
	default:
		return fmt.Errorf("cannot render unresolved reference %q", value.Text)
	}

	return nil
}

type kubernetesResourceIdentity struct {
	group        string
	kind         string
	namespace    string
	rawNamespace string
	name         string
	resourceName string
	scope        kubernetesResourceScope
}

const defaultKubernetesNamespace = "default"

func rejectDuplicateKubernetesResources(artifacts []Artifact, scopes kubernetesResourceScopeIndex) error {
	seen := make(map[kubernetesResourceIdentity]int, len(artifacts))
	for index, artifact := range artifacts {
		identity, isResource, err := validatedKubernetesResourceIdentityOf(artifact.Value)
		if err != nil {
			return fmt.Errorf("artifact %d: %w", index+1, err)
		}
		if !isResource {
			continue
		}
		descriptor := scopes[kubernetesResourceKey{group: identity.group, kind: identity.kind}]
		if descriptor.scope == kubernetesResourceCluster && identity.rawNamespace != "" {
			resource := descriptor.resourceName
			if identity.group != "" {
				resource = identity.group + "/" + resource
			}

			return fmt.Errorf("cluster-scoped Kubernetes resource %s with name %q must not set metadata.namespace (artifact %d)", resource, identity.name, index+1)
		}
		identity.resourceName = descriptor.resourceName
		if descriptor.scope == kubernetesResourceUnknown {
			identity.resourceName = identity.kind
		}
		identity.scope = descriptor.scope
		identity.kind = ""
		identity.rawNamespace = ""
		switch descriptor.scope {
		case kubernetesResourceNamespaced:
			if identity.namespace == "" {
				identity.namespace = "default"
			}
		case kubernetesResourceCluster:
			identity.namespace = ""
		case kubernetesResourceUnknown:
			// scope不明のリソースは宣言されたnamespaceで重複を判定する。
		}
		if previousIndex, exists := seen[identity]; exists {
			resource := identity.resourceName
			if identity.group != "" {
				resource = identity.group + "/" + resource
			}
			if identity.scope != kubernetesResourceCluster {
				return fmt.Errorf("duplicate Kubernetes resource %s with name %q in namespace %q (artifacts %d and %d)", resource, identity.name, identity.namespace, previousIndex+1, index+1)
			}

			return fmt.Errorf("duplicate cluster-scoped Kubernetes resource %s with name %q (artifacts %d and %d)", resource, identity.name, previousIndex+1, index+1)
		}
		seen[identity] = index
	}

	return nil
}

func kubernetesResourceIdentityOf(value Value) (kubernetesResourceIdentity, bool) {
	identity, isResource, err := validatedKubernetesResourceIdentityOf(value)
	if err != nil {
		return kubernetesResourceIdentity{}, false
	}
	if isResource {
		identity = normalizeKubernetesResourceIdentity(identity)
	}

	return identity, isResource
}

func normalizeKubernetesResourceIdentity(identity kubernetesResourceIdentity) kubernetesResourceIdentity {
	descriptor, known := builtinKubernetesResourceDescriptors[kubernetesResourceKey{group: identity.group, kind: identity.kind}]
	if !known {
		return identity
	}
	if descriptor.scope == kubernetesResourceCluster {
		identity.namespace = ""
	} else if descriptor.scope == kubernetesResourceNamespaced && identity.namespace == "" {
		identity.namespace = defaultKubernetesNamespace
	}
	identity.rawNamespace = ""

	return identity
}

func validatedKubernetesResourceIdentityOf(value Value) (kubernetesResourceIdentity, bool, error) {
	value = nodeContents(value)
	if value.Kind != Map {
		return kubernetesResourceIdentity{}, false, nil
	}
	apiVersion, hasAPIVersion := mapValue(value, "apiVersion")
	kind, hasKind := mapValue(value, "kind")
	if !hasAPIVersion && !hasKind {
		return kubernetesResourceIdentity{}, false, nil
	}
	if !hasAPIVersion || apiVersion.Kind != String || apiVersion.Text == "" {
		return kubernetesResourceIdentity{}, true, errors.New("kubernetes resource apiVersion must be a non-empty string")
	}
	if !hasKind || kind.Kind != String || kind.Text == "" {
		return kubernetesResourceIdentity{}, true, errors.New("kubernetes resource kind must be a non-empty string")
	}
	metadata, hasMetadata := mapValue(value, "metadata")
	if !hasMetadata || nodeContents(metadata).Kind != Map {
		return kubernetesResourceIdentity{}, true, errors.New("kubernetes resource metadata must be a map containing name")
	}
	name, hasName := mapValue(nodeContents(metadata), "name")
	if !hasName || name.Kind != String || name.Text == "" {
		return kubernetesResourceIdentity{}, true, errors.New("kubernetes resource metadata.name must be a non-empty string")
	}
	group, version, hasGroup := strings.Cut(apiVersion.Text, "/")
	if !hasGroup {
		group = ""
		version = apiVersion.Text
	} else if group == "" || strings.Contains(version, "/") {
		return kubernetesResourceIdentity{}, true, errors.New("kubernetes resource apiVersion is invalid")
	}
	if !validKubernetesAPIVersion(version) {
		return kubernetesResourceIdentity{}, true, errors.New("kubernetes resource apiVersion is invalid")
	}
	namespace := Value{}
	if namespaceValue, exists := mapValue(nodeContents(metadata), "namespace"); exists {
		if namespaceValue.Kind != String {
			return kubernetesResourceIdentity{}, true, errors.New("kubernetes resource metadata.namespace must be a string")
		}
		namespace = namespaceValue
	}

	identity := kubernetesResourceIdentity{
		group:        group,
		kind:         kind.Text,
		namespace:    namespace.Text,
		rawNamespace: namespace.Text,
		name:         name.Text,
	}
	if descriptor, known := builtinKubernetesResourceDescriptors[kubernetesResourceKey{group: identity.group, kind: identity.kind}]; known {
		identity.resourceName = descriptor.resourceName
		identity.scope = descriptor.scope
	}

	return identity, true, nil
}

func validLifecycleEvent(event string) bool {
	switch event {
	case phaseMain, phaseBootstrap, phaseBeforeCreate, phaseAfterCreate, phaseBeforeUpdate, phaseAfterUpdate, phaseBeforeDelete, phaseAfterDelete, phaseBeforeRollback, phaseAfterRollback, phaseVerify:
		return true
	default:
		return false
	}
}

func validateLifecycleMetadata(artifacts []Artifact) error {
	for index, artifact := range artifacts {
		lifecycle := artifact.Lifecycle
		if len(lifecycle.Events) == 0 {
			return fmt.Errorf("artifact %d has no lifecycle events", index+1)
		}
		seenEvents := make(map[string]struct{}, len(lifecycle.Events))
		for _, event := range lifecycle.Events {
			if !validLifecycleEvent(event) {
				return fmt.Errorf("artifact %d has unknown lifecycle event %q", index+1, event)
			}
			if _, exists := seenEvents[event]; exists {
				return fmt.Errorf("artifact %d repeats lifecycle event %q", index+1, event)
			}
			seenEvents[event] = struct{}{}
		}
		if len(lifecycle.Events) > 1 {
			if _, hasMain := seenEvents[phaseMain]; hasMain {
				return fmt.Errorf("artifact %d cannot combine lifecycle event %q with other events", index+1, phaseMain)
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

type RenderedFile struct {
	Name    string
	Content []byte
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

func RenderOperation(bundle Bundle, operation string) ([]RenderedFile, error) {
	if !validOperation(operation) {
		return nil, fmt.Errorf("unknown operation %q", operation)
	}
	plan := NewBundleOutputPlan(bundle)

	return plan.RenderOperation(operation)
}

// RenderOperationはplanに保持したphase群とScope indexを使ってoperationのファイルを出力します。
func (plan *BundleOutputPlan) RenderOperation(operation string) ([]RenderedFile, error) {
	return plan.renderOperation(operation, "yaml")
}

// RenderOperationWithFormatは指定形式でoperationに必要なファイルを出力します。
func (plan *BundleOutputPlan) RenderOperationWithFormat(operation, format string) ([]RenderedFile, error) {
	if format != "yaml" && format != "json" {
		return nil, fmt.Errorf("unknown output format %q", format)
	}

	return plan.renderOperation(operation, format)
}

func (plan *BundleOutputPlan) renderOperation(operation, format string) ([]RenderedFile, error) {
	if !validOperation(operation) {
		return nil, fmt.Errorf("unknown operation %q", operation)
	}
	if plan.lifecycleErr != nil {
		return nil, plan.lifecycleErr
	}
	var phases []string
	switch operation {
	case operationCreate:
		phases = []string{phaseBootstrap, phaseBeforeCreate, phaseMain, phaseAfterCreate}
	case operationUpdate:
		phases = []string{phaseBeforeUpdate, phaseMain, phaseAfterUpdate}
	case operationDelete:
		phases = []string{phaseBeforeDelete, phaseMain, phaseAfterDelete}
	case operationRollback:
		phases = []string{phaseBeforeRollback, phaseMain, phaseAfterRollback}
	case operationVerify:
		phases = []string{phaseVerify}
	default:
		return nil, fmt.Errorf("unknown operation %q", operation)
	}

	extension := ".yaml"
	if format == "json" {
		extension = ".json"
	}
	bootstrapFile := "bootstrap" + extension
	beforeFile := "before" + extension
	mainFile := "main" + extension
	afterFile := "after" + extension
	verifyFile := "verify" + extension
	phaseGroups := map[string][]string{
		bootstrapFile: {phaseBootstrap},
		beforeFile:    {},
		mainFile:      {phaseMain},
		afterFile:     {},
		verifyFile:    {phaseVerify},
	}
	for _, phase := range phases {
		switch {
		case phase == phaseBootstrap, phase == phaseMain, phase == phaseVerify:
		case strings.HasPrefix(phase, "before."):
			phaseGroups[beforeFile] = append(phaseGroups[beforeFile], phase)
		case strings.HasPrefix(phase, "after."):
			phaseGroups[afterFile] = append(phaseGroups[afterFile], phase)
		}
	}

	files := make([]RenderedFile, 0, len(phaseGroups)+1)
	documents := make(map[int][]lifecycleDocumentReference, len(plan.bundle.Artifacts))
	for _, name := range []string{bootstrapFile, beforeFile, mainFile, afterFile, verifyFile} {
		var chunks [][]byte
		var operationArtifacts []indexedArtifact
		document := 0
		for _, phase := range phaseGroups[name] {
			if err := plan.validatePhase(phase); err != nil {
				return nil, err
			}
			artifacts := plan.artifacts[phase]
			chunk := plan.rendered[phase]
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
			content, err = renderArtifactsJSON(operationArtifacts)
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
	for index, artifact := range plan.bundle.Artifacts {
		included := false
		for _, event := range artifact.Lifecycle.Events {
			included = included || selected[event]
		}
		if !included {
			continue
		}
		cleanup := append([]string{}, artifact.Lifecycle.Cleanup...)
		metadata.Artifacts = append(metadata.Artifacts, lifecycleArtifactRecord{
			Index: index, Events: slices.Clone(artifact.Lifecycle.Events), Priority: artifact.Lifecycle.Priority,
			Cleanup: cleanup, Wait: artifact.Lifecycle.Wait, Origin: artifact.Lifecycle.Origin,
			Documents: documents[index],
		})
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

func validOperation(operation string) bool {
	switch operation {
	case operationCreate, operationUpdate, operationDelete, operationRollback, operationVerify:
		return true
	default:
		return false
	}
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

func yamlNode(value Value) (*yaml.Node, error) {
	value = nodeContents(value)
	switch value.Kind {
	case Null:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagNull, Value: schemaTypeNull}, nil
	case Bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagBool, Value: strconv.FormatBool(value.Bool)}, nil
	case Integer:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagInt, Value: value.Text}, nil
	case Decimal:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagFloat, Value: value.Text}, nil
	case String:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagString, Value: value.Text, Style: yaml.DoubleQuotedStyle}, nil
	case List, AdapterBundle:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range value.List {
			child, err := yamlNode(item)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, child)
		}

		return node, nil
	case Map:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, entry := range value.Map {
			key := &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagString, Value: entry.Key}
			child, err := yamlNode(entry.Value)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, key, child)
		}

		return node, nil
	case Rule:
		return nil, errors.New("rule values cannot be emitted")
	default:
		return nil, fmt.Errorf("cannot render unresolved reference %q", value.Text)
	}
}

func runtimeValueToYAMLNode(value RuntimeValue) (*yaml.Node, error) {
	if value.IsRuleResult() {
		return nil, errors.New("rule values cannot be emitted")
	}
	value = runtimeNodeContents(value)
	switch value.Kind() {
	case Null:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagNull, Value: schemaTypeNull}, nil
	case Bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagBool, Value: strconv.FormatBool(value.BoolValue())}, nil
	case Integer:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagInt, Value: value.TextValue()}, nil
	case Decimal:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagFloat, Value: value.TextValue()}, nil
	case String:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagString, Value: value.TextValue(), Style: yaml.DoubleQuotedStyle}, nil
	case List, AdapterBundle:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range value.List {
			child, err := runtimeValueToYAMLNode(item)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, child)
		}

		return node, nil
	case Map:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, entry := range value.Map {
			key := &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlTagString, Value: entry.Key}
			child, err := runtimeValueToYAMLNode(entry.Value)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, key, child)
		}

		return node, nil
	case Rule:
		return nil, errors.New("rule values cannot be emitted")
	default:
		return nil, fmt.Errorf("cannot render unresolved reference %q", value.TextValue())
	}
}

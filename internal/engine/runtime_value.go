package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// RuntimeValueは解決済みの値と未評価の型付き式を分離して保持します。Valueにはruntime scalarと実行時metadataを格納し、Map/Listの子要素と未評価式はこの構造体に保持します。
type RuntimeValue struct {
	Value         runtimeValueData
	Deferred      *typedContinuation
	Lambda        *typedLambda
	Rule          *RuntimeRule
	RuleResult    *runtimeRuleResult
	ModuleContext *ModuleContext
	List          []RuntimeValue
	Map           []RuntimeEntry
	Base          *RuntimeValue
}

type runtimeValueData struct {
	Kind            Kind
	Span            SourceSpan
	Bool            bool
	Text            string
	SourcePath      string
	SourceLocations []SourceDiagnosticLocation
	Typing          *ValueTyping
	NodeID          *NodeIdentity
	Lifecycle       *Lifecycle
}

func (value runtimeValueData) snapshotValue() Value {
	value = value.cloneMetadata()
	result := Value{
		Kind: value.Kind, Span: value.Span, Bool: value.Bool, Text: value.Text,
		SourcePath: value.SourcePath, SourceLocations: value.SourceLocations, typing: value.Typing,
		NodeID: value.NodeID,
	}
	if value.Lifecycle != nil {
		result.output = &valueOutputMetadata{Lifecycle: value.Lifecycle}
	}

	return result
}

func (value runtimeValueData) cloneMetadata() runtimeValueData {
	value.SourceLocations = slices.Clone(value.SourceLocations)
	value.Typing = cloneValueTyping(value.Typing)
	if value.Lifecycle != nil {
		lifecycle := *value.Lifecycle
		value.Lifecycle = &lifecycle
	}

	return value
}

func (value runtimeValueData) lifecycleMetadata() *Lifecycle {
	return value.Lifecycle
}

func (value *runtimeValueData) setLifecycleMetadata(lifecycle *Lifecycle) {
	if lifecycle == nil {
		value.Lifecycle = nil

		return
	}
	value.Lifecycle = lifecycle
}

func (value *runtimeValueData) ensureTyping() *ValueTyping {
	if value.Typing == nil {
		value.Typing = &ValueTyping{}
	}

	return value.Typing
}

type typedLambda struct {
	Parameters     []string
	ParameterSpans []SourceSpan
	Body           typedExpression
	Scope          map[string]RuntimeValue
	ScopeSpan      SourceSpan
	Typing         *LambdaTyping
	ModuleContext  *ModuleContext
}

type RuntimeRule struct {
	Name                 string
	Identity             *runtimeRuleIdentity
	ParameterDefinitions []RuleParameter
	Requirements         []typedRuleRequirement
	Closure              map[string]RuntimeValue
	StaticTypes          map[string]TypeAnalysis
	Types                map[string]ResolvedType
	ModuleContext        *ModuleContext
}

type runtimeRuleIdentity byte

type typedRuleRequirement struct {
	Expression typedExpression
	Source     string
	Message    string
	SourcePath string
	Line       int
	Span       SourceSpan
}

type runtimeRuleResult struct {
	Failures []RuleFailure
}

type runtimeReference struct {
	Targets []*NodeIdentity
	Path    []string
}

type typedContinuation struct {
	Expression  typedExpression
	Scope       map[string]RuntimeValue
	Rule        *RuntimeRule
	Arguments   []RuntimeValue
	Reference   *runtimeReference
	Binary      *typedBinaryContinuation
	Conditional *typedConditionalContinuation
	Predicate   *runtimePredicateContinuation
	Call        *runtimeCallContinuation
	Unary       *typedUnaryContinuation
	Index       *typedIndexContinuation
	For         *typedForContinuation
	Overlay     *typedOverlayContinuation
	Removal     *typedRemovalContinuation
}

type typedUnaryContinuation struct {
	Operand    RuntimeValue
	SourcePath string
	Span       SourceSpan
}

type typedIndexContinuation struct {
	Base       RuntimeValue
	Key        RuntimeValue
	SourcePath string
	Span       SourceSpan
}

func removeRuntimePath(value RuntimeValue, parts []string) (RuntimeValue, error) {
	if len(parts) == 0 {
		return RuntimeValue{}, errors.New("empty removal path")
	}
	if value.Kind() == Node && value.Base != nil {
		base, err := removeRuntimePath(*value.Base, parts)
		if err != nil {
			return RuntimeValue{}, err
		}
		value.Base = &base

		return value, nil
	}
	if value.Kind() != Map {
		return RuntimeValue{}, errors.New("cannot remove a field from a non-map value")
	}
	value.Map = slices.Clone(value.Map)
	index := slices.IndexFunc(value.Map, func(entry RuntimeEntry) bool { return entry.Key == parts[0] })
	if index < 0 {
		return RuntimeValue{}, fmt.Errorf("field %q does not exist", parts[0])
	}
	if len(parts) == 1 {
		value.Map = slices.Delete(value.Map, index, index+1)

		return value, nil
	}
	child, err := removeRuntimePath(value.Map[index].Value, parts[1:])
	if err != nil {
		return RuntimeValue{}, err
	}
	value.Map[index].Value = child

	return value, nil
}

type typedForContinuation struct {
	Source     RuntimeValue
	Body       typedExpression
	LoopName   string
	Scope      map[string]RuntimeValue
	SourcePath string
	Span       SourceSpan
}

type typedOverlayContinuation struct {
	Base       RuntimeValue
	Patch      RuntimeValue
	SourcePath string
	Span       SourceSpan
}

type typedRemovalContinuation struct {
	Value      RuntimeValue
	Paths      []string
	SourcePath string
	Span       SourceSpan
}

type typedBinaryContinuation struct {
	Operator        typedOperator
	Left            RuntimeValue
	Right           RuntimeValue
	RightExpression *typedExpression
	Scope           map[string]RuntimeValue
}

type typedConditionalContinuation struct {
	Condition  RuntimeValue
	ThenBranch typedExpression
	ElseBranch typedExpression
	Scope      map[string]RuntimeValue
}

type runtimePredicateContinuation struct {
	Name      string
	Arguments []RuntimeValue
}

type runtimeCallContinuation struct {
	Callee    RuntimeValue
	Arguments []RuntimeValue
	Names     []string
}

func (rule RuntimeRule) Parameters() []FunctionTypeParameter {
	parameters := make([]FunctionTypeParameter, len(rule.ParameterDefinitions))
	for index, parameter := range rule.ParameterDefinitions {
		parameters[index] = FunctionTypeParameter{Name: parameter.Name, Type: parameter.Type}
	}

	return parameters
}

// Kindはruntime valueの種類を返します。
func (value RuntimeValue) Kind() Kind {
	return value.Value.Kind
}

// BoolValueはBool値を返します。
func (value RuntimeValue) BoolValue() bool {
	return value.Value.Bool
}

// TextValueはscalar値の文字列表現を返します。
func (value RuntimeValue) TextValue() string {
	return value.Value.Text
}

// IsDeferredは型付き式の評価が残っているかを返します。
func (value RuntimeValue) IsDeferred() bool {
	return value.Deferred != nil
}

func runtimeValueHasDeferred(value RuntimeValue) bool {
	if value.IsDeferred() {
		return true
	}
	if value.Kind() == Node && value.Base != nil {
		return runtimeValueHasDeferred(*value.Base)
	}
	if slices.ContainsFunc(value.List, runtimeValueHasDeferred) {
		return true
	}
	for _, entry := range value.Map {
		if entry.KeyExpression != nil || runtimeValueHasDeferred(entry.Value) {
			return true
		}
	}

	return false
}

// IsRuleResultはRule評価の結果かどうかを返します。
func (value RuntimeValue) IsRuleResult() bool {
	return value.RuleResult != nil
}

// ListItemsはruntime Listの要素を返します。
func (value RuntimeValue) ListItems() []RuntimeValue {
	return value.List
}

// Entriesはruntime Mapのentryを返します。
func (value RuntimeValue) Entries() []RuntimeEntry {
	return value.Map
}

func (value RuntimeValue) MapLookup(key string) (RuntimeValue, bool) {
	index := slices.IndexFunc(value.Map, func(entry RuntimeEntry) bool { return entry.Key == key })
	if index < 0 {
		return RuntimeValue{}, false
	}

	return value.Map[index].Value, true
}

func (value *RuntimeValue) MapSet(key string, replacement RuntimeValue) bool {
	index := slices.IndexFunc(value.Map, func(entry RuntimeEntry) bool { return entry.Key == key })
	if index < 0 {
		return false
	}
	value.Map[index].Value = replacement

	return true
}

func (value RuntimeValue) Metadata() runtimeValueData {
	return value.Value
}

func (value *RuntimeValue) EnsureTyping() *ValueTyping {
	return value.Value.ensureTyping()
}

// NodeIdentityはruntime Nodeのidentityを返します。
func (value RuntimeValue) NodeIdentity() *NodeIdentity {
	return value.Value.NodeID
}

// Sourceはruntime valueのsource pathとspanを返します。
func (value RuntimeValue) Source() (string, SourceSpan) {
	return value.Value.SourcePath, value.Value.Span
}

func cloneRuntimeValueGraph(value RuntimeValue, identities map[*NodeIdentity]*NodeIdentity) RuntimeValue {
	cloned, _ := cloneRuntimeValueGraphWithBudget(value, identities, nil)

	return cloned
}

func cloneRuntimeValueGraphWithBudget(value RuntimeValue, identities map[*NodeIdentity]*NodeIdentity, allocations *runtimeAllocationBudget) (RuntimeValue, error) {
	if err := allocations.consume(runtimeValueGraphSize(value)); err != nil {
		return RuntimeValue{}, err
	}
	if identities == nil {
		identities = make(map[*NodeIdentity]*NodeIdentity)
	}
	var references []*runtimeReference
	cloned := cloneRuntimeValueGraphInto(value, identities, &references)
	for _, reference := range references {
		for index, identity := range reference.Targets {
			if replacement, exists := identities[identity]; exists {
				reference.Targets[index] = replacement
			}
		}
	}

	return cloned, nil
}

func runtimeValueGraphSize(value RuntimeValue) int {
	return runtimeValueGraphSizeActive(value, make(map[any]bool))
}

func runtimeValueGraphSizeActive(value RuntimeValue, active map[any]bool) int {
	count := 1
	for _, item := range value.List {
		count += runtimeValueGraphSizeActive(item, active)
	}
	for _, entry := range value.Map {
		count += runtimeValueGraphSizeActive(entry.Value, active)
	}
	if value.Base != nil {
		count += runtimeValueGraphSizeActive(*value.Base, active)
	}
	if value.Deferred != nil {
		if !active[value.Deferred] {
			active[value.Deferred] = true
			for _, argument := range value.Deferred.Arguments {
				count += runtimeValueGraphSizeActive(argument, active)
			}
			for _, captured := range value.Deferred.Scope {
				count += runtimeValueGraphSizeActive(captured, active)
			}
			if unary := value.Deferred.Unary; unary != nil {
				count += runtimeValueGraphSizeActive(unary.Operand, active)
			}
			if index := value.Deferred.Index; index != nil {
				count += runtimeValueGraphSizeActive(index.Base, active)
				count += runtimeValueGraphSizeActive(index.Key, active)
			}
			if loop := value.Deferred.For; loop != nil {
				count += runtimeValueGraphSizeActive(loop.Source, active)
				for _, captured := range loop.Scope {
					count += runtimeValueGraphSizeActive(captured, active)
				}
			}
			if overlay := value.Deferred.Overlay; overlay != nil {
				count += runtimeValueGraphSizeActive(overlay.Base, active)
				count += runtimeValueGraphSizeActive(overlay.Patch, active)
			}
			if removal := value.Deferred.Removal; removal != nil {
				count += runtimeValueGraphSizeActive(removal.Value, active)
			}
			delete(active, value.Deferred)
		}
	}
	if value.Lambda != nil {
		if !active[value.Lambda] {
			active[value.Lambda] = true
			for _, captured := range value.Lambda.Scope {
				count += runtimeValueGraphSizeActive(captured, active)
			}
			delete(active, value.Lambda)
		}
	}
	if value.Rule != nil {
		if !active[value.Rule] {
			active[value.Rule] = true
			for _, captured := range value.Rule.Closure {
				count += runtimeValueGraphSizeActive(captured, active)
			}
			delete(active, value.Rule)
		}
	}

	return count
}

func runtimeBundleValueWithBudget(artifacts []RuntimeArtifact, sourcePath string, allocations *runtimeAllocationBudget) (RuntimeValue, error) {
	if err := allocations.consume(1); err != nil {
		return RuntimeValue{}, err
	}
	value := RuntimeValue{Value: runtimeValueData{Kind: AdapterBundle, SourcePath: sourcePath}, List: make([]RuntimeValue, len(artifacts))}
	for index, artifact := range artifacts {
		document, err := cloneRuntimeValueGraphWithBudget(artifact.Value, nil, allocations)
		if err != nil {
			return RuntimeValue{}, err
		}
		lifecycle := artifact.Lifecycle
		document.Value.setLifecycleMetadata(&lifecycle)
		value.List[index] = document
	}

	return value, nil
}

func cloneRuntimeValueGraphInto(value RuntimeValue, identities map[*NodeIdentity]*NodeIdentity, references *[]*runtimeReference) RuntimeValue {
	if value.Value.NodeID != nil {
		identity, exists := identities[value.Value.NodeID]
		if !exists {
			identity = &NodeIdentity{originPath: value.Value.NodeID.originPath, originSpan: value.Value.NodeID.originSpan}
			identities[value.Value.NodeID] = identity
		}
		value.Value.NodeID = identity
	}
	value.Value = value.Value.cloneMetadata()
	value.List = slices.Clone(value.List)
	for index := range value.List {
		value.List[index] = cloneRuntimeValueGraphInto(value.List[index], identities, references)
	}
	value.Map = slices.Clone(value.Map)
	for index := range value.Map {
		value.Map[index].KeySourceLocations = slices.Clone(value.Map[index].KeySourceLocations)
		value.Map[index].KeyScope = maps.Clone(value.Map[index].KeyScope)
		value.Map[index].Value = cloneRuntimeValueGraphInto(value.Map[index].Value, identities, references)
		if value.Map[index].KeyExpression != nil {
			keyExpression := *value.Map[index].KeyExpression
			value.Map[index].KeyExpression = &keyExpression
		}
	}
	if value.Base != nil {
		base := cloneRuntimeValueGraphInto(*value.Base, identities, references)
		value.Base = &base
	}
	if value.Deferred != nil {
		deferred := *value.Deferred
		deferred.Expression = cloneTypedExpression(deferred.Expression)
		deferred.Arguments = slices.Clone(deferred.Arguments)
		if deferred.Binary != nil {
			binary := *deferred.Binary
			binary.Left = cloneRuntimeValueGraphInto(binary.Left, identities, references)
			binary.Right = cloneRuntimeValueGraphInto(binary.Right, identities, references)
			binary.Scope = maps.Clone(binary.Scope)
			if binary.RightExpression != nil {
				expression := cloneTypedExpression(*binary.RightExpression)
				binary.RightExpression = &expression
			}
			deferred.Binary = &binary
		}
		if deferred.Conditional != nil {
			conditional := *deferred.Conditional
			conditional.Condition = cloneRuntimeValueGraphInto(conditional.Condition, identities, references)
			conditional.ThenBranch = cloneTypedExpression(conditional.ThenBranch)
			conditional.ElseBranch = cloneTypedExpression(conditional.ElseBranch)
			conditional.Scope = maps.Clone(conditional.Scope)
			deferred.Conditional = &conditional
		}
		if deferred.Predicate != nil {
			predicate := *deferred.Predicate
			predicate.Arguments = slices.Clone(predicate.Arguments)
			for index := range predicate.Arguments {
				predicate.Arguments[index] = cloneRuntimeValueGraphInto(predicate.Arguments[index], identities, references)
			}
			deferred.Predicate = &predicate
		}
		if deferred.Call != nil {
			call := *deferred.Call
			call.Callee = cloneRuntimeValueGraphInto(call.Callee, identities, references)
			call.Arguments = slices.Clone(call.Arguments)
			for index := range call.Arguments {
				call.Arguments[index] = cloneRuntimeValueGraphInto(call.Arguments[index], identities, references)
			}
			call.Names = slices.Clone(call.Names)
			deferred.Call = &call
		}
		if deferred.Unary != nil {
			unary := *deferred.Unary
			unary.Operand = cloneRuntimeValueGraphInto(unary.Operand, identities, references)
			deferred.Unary = &unary
		}
		if deferred.Index != nil {
			index := *deferred.Index
			index.Base = cloneRuntimeValueGraphInto(index.Base, identities, references)
			index.Key = cloneRuntimeValueGraphInto(index.Key, identities, references)
			deferred.Index = &index
		}
		if deferred.For != nil {
			loop := *deferred.For
			loop.Source = cloneRuntimeValueGraphInto(loop.Source, identities, references)
			loop.Body = cloneTypedExpression(loop.Body)
			loop.Scope = maps.Clone(loop.Scope)
			for name, captured := range loop.Scope {
				loop.Scope[name] = cloneRuntimeValueGraphInto(captured, identities, references)
			}
			deferred.For = &loop
		}
		if deferred.Overlay != nil {
			overlay := *deferred.Overlay
			overlay.Base = cloneRuntimeValueGraphInto(overlay.Base, identities, references)
			overlay.Patch = cloneRuntimeValueGraphInto(overlay.Patch, identities, references)
			deferred.Overlay = &overlay
		}
		if deferred.Removal != nil {
			removal := *deferred.Removal
			removal.Value = cloneRuntimeValueGraphInto(removal.Value, identities, references)
			removal.Paths = slices.Clone(removal.Paths)
			deferred.Removal = &removal
		}
		for index := range deferred.Arguments {
			deferred.Arguments[index] = cloneRuntimeValueGraphInto(deferred.Arguments[index], identities, references)
		}
		if deferred.Rule != nil {
			rule := cloneRuntimeRule(*deferred.Rule, identities, references)
			deferred.Rule = &rule
		}
		if deferred.Reference != nil {
			reference := *deferred.Reference
			reference.Targets = slices.Clone(reference.Targets)
			reference.Path = slices.Clone(reference.Path)
			deferred.Reference = &reference
			*references = append(*references, deferred.Reference)
		}
		deferred.Scope = maps.Clone(deferred.Scope)
		for name, captured := range deferred.Scope {
			deferred.Scope[name] = cloneRuntimeValueGraphInto(captured, identities, references)
		}
		value.Deferred = &deferred
	}
	if value.RuleResult != nil {
		result := *value.RuleResult
		result.Failures = slices.Clone(result.Failures)
		value.RuleResult = &result
	}
	if value.Lambda != nil {
		lambda := *value.Lambda
		lambda.Parameters = slices.Clone(lambda.Parameters)
		lambda.ParameterSpans = slices.Clone(lambda.ParameterSpans)
		lambda.Body = cloneTypedExpression(lambda.Body)
		lambda.Scope = maps.Clone(lambda.Scope)
		for name, captured := range lambda.Scope {
			lambda.Scope[name] = cloneRuntimeValueGraphInto(captured, identities, references)
		}
		if lambda.Typing != nil {
			typing := *lambda.Typing
			typing.ParameterTypes = slices.Clone(typing.ParameterTypes)
			typing.TypeScope = maps.Clone(typing.TypeScope)
			if typing.ReturnType != nil {
				returnType := *typing.ReturnType
				typing.ReturnType = &returnType
			}
			if typing.InferenceBody != nil {
				inferenceBody := cloneTypedExpression(*typing.InferenceBody)
				typing.InferenceBody = &inferenceBody
			}
			lambda.Typing = &typing
		}
		if lambda.ModuleContext != nil {
			moduleContext := *lambda.ModuleContext
			lambda.ModuleContext = &moduleContext
		}
		value.Lambda = &lambda
	}
	if value.ModuleContext != nil {
		moduleContext := *value.ModuleContext
		value.ModuleContext = &moduleContext
	}
	if value.Rule != nil {
		rule := cloneRuntimeRule(*value.Rule, identities, references)
		value.Rule = &rule
	}

	return value
}

func cloneRuntimeRule(rule RuntimeRule, identities map[*NodeIdentity]*NodeIdentity, references *[]*runtimeReference) RuntimeRule {
	rule.ParameterDefinitions = slices.Clone(rule.ParameterDefinitions)
	rule.Requirements = slices.Clone(rule.Requirements)
	for index := range rule.Requirements {
		rule.Requirements[index].Expression = cloneTypedExpression(rule.Requirements[index].Expression)
	}
	rule.Closure = maps.Clone(rule.Closure)
	for name, captured := range rule.Closure {
		rule.Closure[name] = cloneRuntimeValueGraphInto(captured, identities, references)
	}
	rule.StaticTypes = maps.Clone(rule.StaticTypes)
	rule.Types = maps.Clone(rule.Types)
	if rule.ModuleContext != nil {
		moduleContext := *rule.ModuleContext
		rule.ModuleContext = &moduleContext
	}

	return rule
}

func cloneValueTyping(value *ValueTyping) *ValueTyping {
	if value == nil {
		return nil
	}
	typing := *value
	typing.TypeProvenance = slices.Clone(value.TypeProvenance)
	for index := range typing.TypeProvenance {
		provenance := &typing.TypeProvenance[index]
		provenance.Type = cloneResolvedType(provenance.Type)
		if provenance.Types != nil {
			types := make(map[string]ResolvedType, len(provenance.Types))
			for name, typeExpression := range provenance.Types {
				types[name] = cloneResolvedType(typeExpression)
			}
			provenance.Types = types
		}
	}
	if value.SchemaProvenance != nil {
		provenance := *value.SchemaProvenance
		if provenance.Type != nil {
			typeExpression := cloneResolvedType(*provenance.Type)
			provenance.Type = &typeExpression
		}
		typing.SchemaProvenance = &provenance
	}

	return &typing
}

func cloneResolvedType(typeExpression ResolvedType) ResolvedType {
	typeExpression.TypeParameters = slices.Clone(typeExpression.TypeParameters)
	typeExpression.Arguments = cloneResolvedTypes(typeExpression.Arguments)
	typeExpression.Options = cloneResolvedTypes(typeExpression.Options)
	typeExpression.Fields = slices.Clone(typeExpression.Fields)
	for index := range typeExpression.Fields {
		typeExpression.Fields[index].Type = cloneResolvedType(typeExpression.Fields[index].Type)
	}
	if typeExpression.Element != nil {
		element := cloneResolvedType(*typeExpression.Element)
		typeExpression.Element = &element
	}
	if typeExpression.AdditionalProperties != nil {
		additionalProperties := cloneResolvedType(*typeExpression.AdditionalProperties)
		typeExpression.AdditionalProperties = &additionalProperties
	}
	typeExpression.Parameters = slices.Clone(typeExpression.Parameters)
	for index := range typeExpression.Parameters {
		typeExpression.Parameters[index].Type = cloneResolvedType(typeExpression.Parameters[index].Type)
	}
	if typeExpression.ReturnType != nil {
		returnType := cloneResolvedType(*typeExpression.ReturnType)
		typeExpression.ReturnType = &returnType
	}

	return typeExpression
}

func cloneResolvedTypes(types []ResolvedType) []ResolvedType {
	cloned := slices.Clone(types)
	for index := range cloned {
		cloned[index] = cloneResolvedType(cloned[index])
	}

	return cloned
}

func cloneTypedExpression(expression typedExpression) typedExpression {
	expression.items = slices.Clone(expression.items)
	expression.args = slices.Clone(expression.args)
	expression.argNames = slices.Clone(expression.argNames)
	expression.parameters = slices.Clone(expression.parameters)
	expression.paramSpans = slices.Clone(expression.paramSpans)
	expression.paths = slices.Clone(expression.paths)
	for index := range expression.items {
		expression.items[index] = cloneTypedExpression(expression.items[index])
	}
	for index := range expression.args {
		expression.args[index] = cloneTypedExpression(expression.args[index])
	}
	expression.entries = slices.Clone(expression.entries)
	for index := range expression.entries {
		expression.entries[index].value = cloneTypedExpression(expression.entries[index].value)
		if expression.entries[index].keyExpression != nil {
			keyExpression := cloneTypedExpression(*expression.entries[index].keyExpression)
			expression.entries[index].keyExpression = &keyExpression
		}
	}
	for _, child := range []**typedExpression{&expression.operand, &expression.left, &expression.right, &expression.condition, &expression.thenBranch, &expression.elseBranch, &expression.callee} {
		if *child != nil {
			cloned := cloneTypedExpression(**child)
			*child = &cloned
		}
	}

	return expression
}

type RuntimeEntry struct {
	Key                string
	KeyExpression      *typedExpression
	KeyScope           map[string]RuntimeValue
	KeySpan            SourceSpan
	KeySourcePath      string
	KeySourceLocations []SourceDiagnosticLocation
	Value              RuntimeValue
}

type RuntimeBundle struct {
	Artifacts          []RuntimeArtifact
	PendingChecks      []RuntimeTypeCheck
	PendingValidations []RuntimeValidation
}

type RuntimeArtifact struct {
	Value     RuntimeValue
	Lifecycle Lifecycle
}

type RuntimeTypeCheck struct {
	Value      RuntimeValue
	Annotation ResolvedType
	Types      map[string]ResolvedType
	Message    string
}

type RuntimeValidation struct {
	Value         RuntimeValue
	Rule          RuntimeRule
	Line          int
	Each          bool
	Output        bool
	SourcePath    string
	Span          SourceSpan
	QueryType     *ResolvedType
	QuerySchema   *ResolvedType
	QueryWhere    *RuntimeValue
	MinMatches    int
	MinMatchesSet bool
	Types         map[string]ResolvedType
}

// EvaluationStateは型付き評価とBundle参照解決で共有する実行状態を保持します。
type EvaluationState struct {
	Environment      map[string]RuntimeValue
	StaticTypes      map[string]TypeAnalysis
	Types            map[string]ResolvedType
	Stack            map[string]bool
	Budget           *functionBudget
	Allocations      *runtimeAllocationBudget
	SourcePath       string
	Module           *ModuleContext
	NodeIndex        map[*NodeIdentity]runtimeNodeRecord
	ActiveReferences map[activeReference]bool
	ActiveRules      map[*runtimeRuleIdentity]bool
}

type runtimeAllocationBudget struct {
	values int
	bytes  int64
}

const (
	maxRuntimeValueAllocations = 1_000_000
	maxRuntimeAdapterYAMLBytes = 64 << 20
)

var errRuntimeAllocationLimit = errors.New("RuntimeValue allocation limit exceeded")

func (budget *runtimeAllocationBudget) consume(values int) error {
	if budget == nil || values <= 0 {
		return nil
	}
	if values > maxRuntimeValueAllocations-budget.values {
		return fmt.Errorf("%w: limit is %d", errRuntimeAllocationLimit, maxRuntimeValueAllocations)
	}
	budget.values += values

	return nil
}

func (budget *runtimeAllocationBudget) consumeBytes(size int) error {
	if budget == nil || size <= 0 {
		return nil
	}
	if int64(size) > maxRuntimeAdapterYAMLBytes-budget.bytes {
		return fmt.Errorf("%w: adapter YAML input exceeds %d bytes", errRuntimeAllocationLimit, maxRuntimeAdapterYAMLBytes)
	}
	budget.bytes += int64(size)

	return nil
}

func (budget *runtimeAllocationBudget) chargeValueGraph(value RuntimeValue) error {
	return budget.consume(runtimeValueGraphSize(value))
}

type runtimeNodeRecord struct {
	value     *RuntimeValue
	ancestors []*NodeIdentity
}

// evaluateRuntimeExpressionは型付き式を指定されたEvaluationStateで評価し、deferred syntaxをValueへ保持せずにruntime dataを返します。
func evaluateRuntimeExpression(expression typedExpression, state *EvaluationState) (RuntimeValue, error) {
	if state == nil {
		return RuntimeValue{}, errors.New("evaluation state is nil")
	}
	if state.Allocations == nil {
		state.Allocations = &runtimeAllocationBudget{}
	}
	value, err := expression.evaluate(typedEvaluationContext{
		runtimeEnv:  state.Environment,
		staticTypes: state.StaticTypes,
		types:       state.Types,
		stack:       state.Stack,
		budget:      state.Budget, allocations: state.Allocations,
		sourcePath: state.SourcePath, module: state.Module,
	})
	if err != nil {
		return RuntimeValue{}, err
	}

	return value, nil
}

func evaluateTypedContinuation(continuation *typedContinuation, state *EvaluationState) (RuntimeValue, error) {
	if continuation == nil {
		return RuntimeValue{}, errors.New("typed continuation is nil")
	}
	if state == nil {
		return RuntimeValue{}, errors.New("evaluation state is nil")
	}
	if state.Allocations == nil {
		state.Allocations = &runtimeAllocationBudget{}
	}
	environment := state.Environment
	if continuation.Scope != nil {
		environment = continuation.Scope
	}
	value, err := continuation.Expression.evaluate(typedEvaluationContext{
		runtimeEnv: environment, staticTypes: state.StaticTypes, types: state.Types, stack: state.Stack,
		budget: state.Budget, allocations: state.Allocations, sourcePath: state.SourcePath, module: state.Module,
	})
	if err != nil {
		return RuntimeValue{}, err
	}

	return value, nil
}

func callRuntimeLambda(lambda RuntimeValue, args []RuntimeValue, names []string, state *EvaluationState) (RuntimeValue, error) {
	if lambda.Lambda == nil || state == nil {
		return RuntimeValue{}, errors.New("invalid typed Lambda call")
	}
	definition := lambda.Lambda
	parameters := make([]FunctionTypeParameter, len(definition.Parameters))
	for index, name := range definition.Parameters {
		parameters[index] = FunctionTypeParameter{Name: name}
	}
	ordered, ok := functionCallRuntimeArguments(parameters, args, names)
	if !ok {
		return RuntimeValue{}, errors.New("function call arguments do not match its signature")
	}
	substitutions := make(map[string]ResolvedType)
	if definition.Typing != nil {
		for index, parameterType := range definition.Typing.ParameterTypes {
			if !isLambdaTypeVariable(parameterType) || index >= len(ordered) {
				continue
			}
			argumentAnalysis, known := inferRuntimeValueStaticType(ordered[index])
			if !known {
				return RuntimeValue{}, sourceDiagnostic(lambda.Value.SourcePath, definition.ScopeSpan, errors.New("cannot infer an untyped Lambda parameter at this call"))
			}
			substitutions[parameterType.Name] = argumentAnalysis.Type
		}
	}
	var types map[string]ResolvedType
	if state != nil {
		types = state.Types
	}
	body, err := specializeTypedExpression(definition.Body, substitutions, types)
	if err != nil {
		return RuntimeValue{}, sourceDiagnostic(lambda.Value.SourcePath, definition.ScopeSpan, err)
	}
	if definition.Typing != nil && definition.Typing.ReturnType != nil {
		returnType := substituteTypeParameters(*definition.Typing.ReturnType, substitutions)
		matches, compatible := staticTypeAssignable(body.resolvedType, returnType, types)
		if compatible && !matches {
			return RuntimeValue{}, sourceDiagnostic(lambda.Value.SourcePath, definition.ScopeSpan, fmt.Errorf("lambda returns %s, expected %s", staticTypeName(body.resolvedType), staticTypeName(returnType)))
		}
	}
	leave, err := state.Budget.enterCall()
	if err != nil {
		return RuntimeValue{}, sourceDiagnostic(lambda.Value.SourcePath, definition.ScopeSpan, err)
	}
	defer leave()
	environment := maps.Clone(definition.Scope)
	for index, parameter := range definition.Parameters {
		environment[parameter] = ordered[index]
	}
	childState := *state
	childState.Environment = environment
	childState.SourcePath = lambda.Value.SourcePath
	if definition.ModuleContext != nil {
		childState.Module = definition.ModuleContext
	}

	return evaluateTypedExpressionRuntime(body, &childState)
}

func functionCallRuntimeArguments(parameters []FunctionTypeParameter, args []RuntimeValue, names []string) ([]RuntimeValue, bool) {
	if len(parameters) != len(args) || len(names) != 0 && len(names) != len(args) {
		return nil, false
	}
	ordered := make([]RuntimeValue, len(parameters))
	assigned := make([]bool, len(parameters))
	named := len(names) != 0
	for index, argument := range args {
		parameterIndex := index
		if named && names[index] != "" {
			parameterIndex = slices.IndexFunc(parameters, func(parameter FunctionTypeParameter) bool { return parameter.Name == names[index] })
			if parameterIndex < 0 {
				return nil, false
			}
		} else if named {
			for parameterIndex < len(assigned) && assigned[parameterIndex] {
				parameterIndex++
			}
		}
		if parameterIndex >= len(ordered) || assigned[parameterIndex] {
			return nil, false
		}
		assigned[parameterIndex] = true
		ordered[parameterIndex] = argument
	}
	if slices.Contains(assigned, false) {
		return nil, false
	}

	return ordered, true
}

func evaluateTypedExpressionRuntime(expression typedExpression, state *EvaluationState) (RuntimeValue, error) {
	if state == nil {
		return RuntimeValue{}, errors.New("evaluation state is nil")
	}
	if state.Allocations == nil {
		state.Allocations = &runtimeAllocationBudget{}
	}
	value, err := expression.evaluate(typedEvaluationContext{
		runtimeEnv: state.Environment, staticTypes: state.StaticTypes, types: state.Types,
		stack: state.Stack, budget: state.Budget, allocations: state.Allocations, sourcePath: state.SourcePath, module: state.Module,
	})
	if err != nil {
		return RuntimeValue{}, err
	}

	return value, nil
}

func runtimeValueToValue(value RuntimeValue) (Value, error) {
	if value.Deferred != nil || value.RuleResult != nil || value.Rule != nil {
		return Value{}, errors.New("runtime value cannot be serialized before continuation resolution")
	}
	result := value.Value.snapshotValue()
	if result.Kind == List || result.Kind == AdapterBundle {
		result.List = make([]Value, len(value.List))
		for index, item := range value.List {
			resolved, err := runtimeValueToValue(item)
			if err != nil {
				return Value{}, err
			}
			result.List[index] = resolved
		}
	}
	if result.Kind == Map {
		result.Map = make([]Entry, len(value.Map))
		for index, entry := range value.Map {
			if entry.KeyExpression != nil {
				return Value{}, errors.New("runtime Map entry still has an unevaluated key expression")
			}
			resolved, err := runtimeValueToValue(entry.Value)
			if err != nil {
				return Value{}, err
			}
			result.Map[index] = Entry{
				Key: entry.Key, KeySpan: entry.KeySpan, KeySourcePath: entry.KeySourcePath,
				KeySourceLocations: slices.Clone(entry.KeySourceLocations), Value: resolved,
			}
		}
	}
	if result.Kind == Node && value.Base != nil {
		base, err := runtimeValueToValue(*value.Base)
		if err != nil {
			return Value{}, err
		}
		result.Base = &base
	}

	return result, nil
}

func inferRuntimeValueStaticType(value RuntimeValue) (TypeAnalysis, bool) {
	if value.Deferred != nil {
		return TypeAnalysis{}, false
	}
	if value.Kind() == Node && value.Base != nil {
		baseAnalysis, known := inferRuntimeValueStaticType(*value.Base)
		if known {
			return TypeAnalysis{Type: ResolvedType{Name: typeNode, Arguments: []ResolvedType{baseAnalysis.Type}}}, true
		}

		return TypeAnalysis{}, false
	}
	switch value.Kind() {
	case Null:
		return TypeAnalysis{Type: ResolvedType{Name: typeNull}}, true
	case Bool:
		return TypeAnalysis{Type: ResolvedType{Name: typeBool}}, true
	case Integer:
		return TypeAnalysis{Type: ResolvedType{Name: typeInt}}, true
	case Decimal:
		return TypeAnalysis{Type: ResolvedType{Name: typeDecimal}}, true
	case String:
		return TypeAnalysis{Type: ResolvedType{Name: typeString}, Facts: TypeAnalysisFacts{ConstantString: value.Value.Text, ConstantStringKnown: true}}, true
	case List, AdapterBundle:
		var element ResolvedType
		for index, item := range value.List {
			itemAnalysis, known := inferRuntimeValueStaticType(item)
			if !known {
				return TypeAnalysis{Type: ResolvedType{Name: typeList}, Facts: TypeAnalysisFacts{Bundle: value.Kind() == AdapterBundle, BundleKnown: true}}, true
			}
			if index == 0 {
				element = itemAnalysis.Type
			} else {
				element = joinStaticTypes(element, itemAnalysis.Type)
			}
		}
		if len(value.List) == 0 {
			element = ResolvedType{Name: typeAny}
		}

		return TypeAnalysis{Type: ResolvedType{Name: typeList, Element: &element}, Facts: TypeAnalysisFacts{Bundle: value.Kind() == AdapterBundle, BundleKnown: true}}, true
	case Map:
		fields := make([]TypeField, 0, len(value.Map))
		for _, entry := range value.Map {
			fieldAnalysis, known := inferRuntimeValueStaticType(entry.Value)
			fieldType := ResolvedType{}
			if !known {
			} else {
				fieldType = fieldAnalysis.Type
			}
			fields = append(fields, TypeField{Name: entry.Key, Type: fieldType})
		}

		return TypeAnalysis{Type: ResolvedType{Fields: fields}}, true
	case Function:
		return TypeAnalysis{Type: ResolvedType{Name: typeFunction, SchemaPath: value.Value.Text}}, true
	case Lambda:
		if value.Lambda == nil {
			return TypeAnalysis{}, false
		}
		if value.Lambda.Typing == nil {
			return TypeAnalysis{Type: ResolvedType{Name: typeFunction}}, true
		}
		parameters := make([]FunctionTypeParameter, len(value.Lambda.Parameters))
		for index, name := range value.Lambda.Parameters {
			var parameterType ResolvedType
			if index < len(value.Lambda.Typing.ParameterTypes) {
				parameterType = value.Lambda.Typing.ParameterTypes[index]
			}
			parameters[index] = FunctionTypeParameter{Name: name, Type: parameterType}
		}
		function := ResolvedType{Name: typeFunction, Parameters: parameters, ReturnType: value.Lambda.Typing.ReturnType}
		facts := TypeAnalysisFacts{}
		if value.Lambda.Typing.InferenceBody != nil {
			body := *value.Lambda.Typing.InferenceBody
			facts.FunctionInference = &FunctionInference{TypedBody: &body, Bindings: maps.Clone(value.Lambda.Typing.TypeScope)}
		}

		return TypeAnalysis{Type: function, Facts: facts}, true
	default:
		return TypeAnalysis{}, false
	}
}

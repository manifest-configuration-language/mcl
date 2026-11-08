package engine

import (
	"context"
	"regexp"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
	values "github.com/manifest-configuration-language/mcl/internal/value"
)

var numberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

type Kind = values.Kind

const (
	Null          = values.Null
	Bool          = values.Bool
	Integer       = values.Integer
	Decimal       = values.Decimal
	String        = values.String
	List          = values.List
	AdapterBundle = values.AdapterBundle
	Map           = values.Map
	Replacement   = values.Replacement
	Function      = values.Function
	Lambda        = values.Lambda
	Node          = values.Node
	Rule          = values.Rule
)

type Value = values.Value
type Entry = values.Entry
type Artifact = values.Artifact
type Lifecycle = values.Lifecycle
type NodeIdentity = values.NodeIdentity
type TypeProvenance = values.TypeProvenance
type SchemaProvenance = values.SchemaProvenance
type ValueTyping = values.ValueTyping

func newNodeIdentity(originPath string, originSpan SourceSpan) *NodeIdentity {
	return values.NewNodeIdentity(originPath, originSpan)
}

type LambdaTyping struct {
	ParameterTypes []ResolvedType
	ReturnType     *ResolvedType
	TypeScope      map[string]TypeAnalysis
	Types          map[string]ResolvedType
	InferenceBody  *typedExpression
}

type ModuleContext struct {
	//nolint:containedctx // Adapterへ評価Contextを伝播するため、実行状態に保持します。
	Context                     context.Context
	ProjectRoot                 string
	filesystem                  *projectFilesystem
	Dependencies                *dependency.Lock
	HelmOperation               string
	AllowNondeterministicOutput bool
	KubernetesVersion           string
	Analysis                    *TypeAnalysisContext
	CRDSchemas                  map[customResourceDefinitionKey]crdVersionSchema
}

func valueStringField(value Value, key string) (string, bool) {
	field, exists := mapValue(nodeContents(value), key)

	return field.Text, exists && field.Kind == String
}

type RuleFailure struct {
	Rule       string
	Expression string
	Message    string
	Line       int
	Path       string
	Span       SourceSpan
	Actual     string
	Target     string
}

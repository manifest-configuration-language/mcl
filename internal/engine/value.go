package engine

import (
	"context"
	"regexp"

	"github.com/manifest-configuration-language/mcl/internal/dependency"
)

var numberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

type Kind uint8

const (
	Null Kind = iota
	Bool
	Integer
	Decimal
	String
	List
	AdapterBundle
	Map
	Replacement
	Function
	Lambda
	Node
	Rule
)

type Value struct {
	Kind            Kind
	Span            SourceSpan
	Bool            bool
	Text            string
	List            []Value
	Map             []Entry
	Base            *Value
	SourcePath      string
	SourceLocations []SourceDiagnosticLocation
	typing          *ValueTyping
	NodeID          *NodeIdentity
	output          *valueOutputMetadata
}

type valueOutputMetadata struct {
	Lifecycle *Lifecycle
}

type LambdaTyping struct {
	ParameterTypes []ResolvedType
	ReturnType     *ResolvedType
	TypeScope      map[string]TypeAnalysis
	InferenceBody  *typedExpression
}

type ValueTyping struct {
	TypeProvenance   []TypeProvenance
	SchemaProvenance *SchemaProvenance
}

type ModuleContext struct {
	//nolint:containedctx // Adapterへ評価Contextを伝播するため、実行状態に保持します。
	Context         context.Context
	ProjectRoot     string
	filesystem      *projectFilesystem
	Dependencies    *dependency.Lock
	HelmOperation   string
	Analysis        *TypeAnalysisContext
	ResourceCatalog KubernetesResourceCatalog
}

func valueStringField(value Value, key string) (string, bool) {
	field, exists := mapValue(nodeContents(value), key)

	return field.Text, exists && field.Kind == String
}

type TypeProvenance struct {
	Type  ResolvedType
	Types map[string]ResolvedType
}

type SchemaProvenance struct {
	ResourceType string
	Path         string
	Type         *ResolvedType
	Unknown      bool
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

type NodeIdentity struct {
	originPath string
	originSpan SourceSpan
}

type Entry struct {
	Key                string
	KeySpan            SourceSpan
	KeySourcePath      string
	KeySourceLocations []SourceDiagnosticLocation
	Value              Value
}

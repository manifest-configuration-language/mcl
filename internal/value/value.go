// Package value defines finalized values and the metadata that travels with them.
package value

import (
	"maps"
	"slices"

	"github.com/manifest-configuration-language/mcl/internal/source"
	semtypes "github.com/manifest-configuration-language/mcl/internal/types"
)

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
	Span            source.Span
	Bool            bool
	Text            string
	List            []Value
	Map             []Entry
	Base            *Value
	SourcePath      string
	SourceLocations []source.DiagnosticLocation
	typing          *ValueTyping
	NodeID          *NodeIdentity
	output          *outputMetadata
}

type outputMetadata struct {
	Lifecycle *Lifecycle
}

type Entry struct {
	Key                string
	KeySpan            source.Span
	KeySourcePath      string
	KeySourceLocations []source.DiagnosticLocation
	Value              Value
}

type Artifact struct {
	Value     Value
	Lifecycle Lifecycle
}

type Lifecycle struct {
	Events   []string
	Priority int
	Cleanup  []string
	Wait     bool
	Origin   string
}

type NodeIdentity struct {
	originPath string
	originSpan source.Span
}

func NewNodeIdentity(originPath string, originSpan source.Span) *NodeIdentity {
	return &NodeIdentity{originPath: originPath, originSpan: originSpan}
}

func (identity *NodeIdentity) Clone() *NodeIdentity {
	if identity == nil {
		return nil
	}

	return NewNodeIdentity(identity.originPath, identity.originSpan)
}

func (identity *NodeIdentity) Origin() (string, source.Span) {
	if identity == nil {
		return "", source.Span{}
	}

	return identity.originPath, identity.originSpan
}

type TypeProvenance struct {
	Type  semtypes.ResolvedType
	Types map[string]semtypes.ResolvedType
}

type SchemaProvenance struct {
	ResourceType string
	Path         string
	Type         *semtypes.ResolvedType
	Unknown      bool
}

type ValueTyping struct {
	TypeProvenance   []TypeProvenance
	SchemaProvenance *SchemaProvenance
}

func (value *Value) Typing() *ValueTyping {
	if value == nil {
		return nil
	}

	return value.typing
}

func (value *Value) SetTyping(typing *ValueTyping) {
	if value == nil {
		return
	}

	value.typing = typing
}

func (value *Value) EnsureTyping() *ValueTyping {
	if value.typing == nil {
		value.typing = &ValueTyping{}
	}

	return value.typing
}

func (value Value) LifecycleMetadata() *Lifecycle {
	if value.output == nil {
		return nil
	}

	return value.output.Lifecycle
}

func (value *Value) SetLifecycleMetadata(lifecycle *Lifecycle) {
	if value == nil {
		return
	}
	if lifecycle == nil {
		value.output = nil

		return
	}
	value.output = &outputMetadata{Lifecycle: lifecycle}
}

func CloneTyping(typing *ValueTyping) *ValueTyping {
	if typing == nil {
		return nil
	}

	cloned := *typing
	cloned.TypeProvenance = slices.Clone(typing.TypeProvenance)
	for index := range cloned.TypeProvenance {
		provenance := &cloned.TypeProvenance[index]
		provenance.Type = semtypes.Clone(provenance.Type)
		provenance.Types = cloneTypes(provenance.Types)
	}
	if typing.SchemaProvenance != nil {
		provenance := *typing.SchemaProvenance
		if provenance.Type != nil {
			clonedType := semtypes.Clone(*provenance.Type)
			provenance.Type = &clonedType
		}
		cloned.SchemaProvenance = &provenance
	}

	return &cloned
}

func cloneTypes(types map[string]semtypes.ResolvedType) map[string]semtypes.ResolvedType {
	if types == nil {
		return nil
	}

	cloned := maps.Clone(types)
	for name, resolvedType := range cloned {
		cloned[name] = semtypes.Clone(resolvedType)
	}

	return cloned
}

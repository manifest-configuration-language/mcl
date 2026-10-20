package engine

import (
	"maps"
	"slices"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type SchemaID string

type TypeScopeID string

type TypeAnalysis struct {
	Type  ResolvedType
	Facts TypeAnalysisFacts
}

type TypeAnalysisFacts struct {
	Replacement                *ResolvedType
	Fields                     map[string]TypeAnalysisFacts
	Elements                   []TypeAnalysisFacts
	ConstantString             string
	ConstantStringKnown        bool
	Bundle                     bool
	BundleKnown                bool
	FunctionInference          *FunctionInference
	ReturnBundleParameter      int
	ReturnBundleParameterKnown bool
}

func cloneTypeAnalysisFacts(facts TypeAnalysisFacts) TypeAnalysisFacts {
	if facts.Fields != nil {
		fields := make(map[string]TypeAnalysisFacts, len(facts.Fields))
		for name, fieldFacts := range facts.Fields {
			fields[name] = cloneTypeAnalysisFacts(fieldFacts)
		}
		facts.Fields = fields
	}
	facts.Elements = slices.Clone(facts.Elements)
	for index := range facts.Elements {
		facts.Elements[index] = cloneTypeAnalysisFacts(facts.Elements[index])
	}

	return facts
}

type FunctionInference struct {
	Body      Expr
	TypedBody *typedExpression
	Bindings  map[string]TypeAnalysis
}

// TypeAnalysisContextは型解析中に共有するSchemaと型スコープを管理します。
type TypeAnalysisContext struct {
	mu      sync.RWMutex
	schemas map[SchemaID]*jsonschema.Schema
	scopes  map[TypeScopeID]map[string]ResolvedType
}

func (context *TypeAnalysisContext) registerSchema(id SchemaID, schema *jsonschema.Schema) {
	if context == nil || id == "" || schema == nil {
		return
	}
	context.mu.Lock()
	defer context.mu.Unlock()
	if context.schemas == nil {
		context.schemas = make(map[SchemaID]*jsonschema.Schema)
	}
	context.schemas[id] = schema
}

func (context *TypeAnalysisContext) schema(id SchemaID) *jsonschema.Schema {
	if context == nil || id == "" {
		return nil
	}
	context.mu.RLock()
	defer context.mu.RUnlock()

	return context.schemas[id]
}

func (context *TypeAnalysisContext) registerScope(id TypeScopeID, types map[string]ResolvedType) {
	if context == nil || id == "" {
		return
	}
	context.mu.Lock()
	defer context.mu.Unlock()
	if context.scopes == nil {
		context.scopes = make(map[TypeScopeID]map[string]ResolvedType)
	}
	context.scopes[id] = maps.Clone(types)
}

func (context *TypeAnalysisContext) scope(id TypeScopeID) map[string]ResolvedType {
	if context == nil || id == "" {
		return nil
	}
	context.mu.RLock()
	defer context.mu.RUnlock()

	return context.scopes[id]
}

package engine

import (
	"strings"
	"testing"
)

func TestTypedAnalysisCacheIsScopedToStaticBindings(t *testing.T) {
	t.Parallel()

	module, err := ParseAST("let value = item\n")
	if err != nil {
		t.Fatal(err)
	}
	expression := module.Nodes[0].(*LetDeclaration).Expression
	context := typedEvaluationContext{
		staticTypes: map[string]TypeAnalysis{"item": {Type: ResolvedType{Name: typeString}}},
		analysis:    newTypedAnalysisCache(),
	}
	first, known, err := context.inferType(expression)
	if err != nil || !known || first.Name != typeString {
		t.Fatalf("first inferType() = (%#v, %t, %v), want String", first, known, err)
	}
	if _, known, err := context.inferType(expression); err != nil || !known || len(context.analysis.results) != 1 {
		t.Fatalf("cached inferType() = (known %t, error %v), cache entries = %d, want one cached result", known, err, len(context.analysis.results))
	}

	secondContext := context
	secondContext.staticTypes = map[string]TypeAnalysis{"item": {Type: ResolvedType{Name: typeBool}}}
	secondContext.analysis = newTypedAnalysisCache()
	second, known, err := secondContext.inferType(expression)
	if err != nil || !known || second.Name != typeBool {
		t.Fatalf("second inferType() = (%#v, %t, %v), want Bool", second, known, err)
	}
}

func BenchmarkTypedAnalysisCache(b *testing.B) {
	const elements = 128
	source := "let value = " + strings.Repeat("1 + ", elements) + "1\n"
	module, err := ParseAST(source)
	if err != nil {
		b.Fatal(err)
	}
	expression := module.Nodes[0].(*LetDeclaration).Expression
	state := &EvaluationState{
		Environment: make(map[string]RuntimeValue),
		StaticTypes: make(map[string]TypeAnalysis),
		Types:       make(map[string]ResolvedType),
		Stack:       make(map[string]bool),
	}

	b.Run("shared-analysis", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			analysis, known, err := inferStaticExprAnalysis(expression, state.StaticTypes, state.Types)
			if err != nil {
				b.Fatal(err)
			}
			if _, _, err := typedExpressionFromKnownAnalysis(expression, state, analysis, known); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("recomputed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := inferStaticExprAnalysis(expression, state.StaticTypes, state.Types); err != nil {
				b.Fatal(err)
			}
			if _, _, err := typedExpressionFromExpr(expression, state); err != nil {
				b.Fatal(err)
			}
		}
	})
}

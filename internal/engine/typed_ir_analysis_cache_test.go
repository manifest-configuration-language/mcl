package engine

import (
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

func TestTypedAnalysisCacheTypeHitDoesNotCopyOrExposeFacts(t *testing.T) {
	t.Parallel()

	module, err := ParseAST("let value =\n  name: \"api\"\n")
	if err != nil {
		t.Fatal(err)
	}
	expression := module.Nodes[0].(*LetDeclaration).Expression
	context := typedEvaluationContext{analysis: newTypedAnalysisCache()}
	if _, known, err := context.inferAnalysis(expression); err != nil || !known {
		t.Fatalf("inferAnalysis() = (known %t, error %v), want known analysis", known, err)
	}
	typeResult, known, err := context.inferType(expression)
	if err != nil || !known || len(typeResult.Fields) != 1 {
		t.Fatalf("cached inferType() = (%#v, %t, %v), want one-field type", typeResult, known, err)
	}
	typeResult.Fields[0].Name = "mutated"

	analysis, known, err := context.inferAnalysis(expression)
	if err != nil || !known {
		t.Fatalf("cached inferAnalysis() = (known %t, error %v), want known analysis", known, err)
	}
	if got := analysis.Type.Fields[0].Name; got != "name" {
		t.Errorf("cached analysis type field = %q, want name", got)
	}
	if got := analysis.Facts.Fields["name"].ConstantString; got != "api" {
		t.Errorf("cached field constant = %q, want api", got)
	}
}

func BenchmarkTypedAnalysisCache(b *testing.B) {
	const source = `let value =
  spec:
    endpoints:
      - name: "web"
        targetPort: 8080
      - name: "admin"
        targetPort: 8081
`
	module, err := ParseAST(source)
	if err != nil {
		b.Fatal(err)
	}
	expression := module.Nodes[0].(*LetDeclaration).Expression
	state := &EvaluationState{
		EvaluationSession: &EvaluationSession{},
		Environment:       make(map[string]RuntimeValue),
		StaticTypes:       make(map[string]TypeAnalysis),
		Types:             make(map[string]ResolvedType),
		Stack:             make(map[string]bool),
	}

	b.Run("cache-hit-type", func(b *testing.B) {
		b.ReportAllocs()
		context := typedEvaluationContextFromState(state)
		if _, _, err := context.inferAnalysis(expression); err != nil {
			b.Fatal(err)
		}
		for b.Loop() {
			if _, _, err := context.inferType(expression); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cache-hit-analysis", func(b *testing.B) {
		b.ReportAllocs()
		context := typedEvaluationContextFromState(state)
		if _, _, err := context.inferAnalysis(expression); err != nil {
			b.Fatal(err)
		}
		for b.Loop() {
			if _, _, err := context.inferAnalysis(expression); err != nil {
				b.Fatal(err)
			}
		}
	})

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

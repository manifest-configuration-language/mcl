package engine

type typedAnalysisResult struct {
	analysis TypeAnalysis
	known    bool
	err      error
}

type typedAnalysisCache struct {
	results map[Expr]typedAnalysisResult
}

func newTypedAnalysisCache() *typedAnalysisCache {
	return &typedAnalysisCache{results: make(map[Expr]typedAnalysisResult)}
}

func (context typedEvaluationContext) analysisCache() *typedAnalysisCache {
	if context.analysis != nil {
		return context.analysis
	}

	return newTypedAnalysisCache()
}

func (context typedEvaluationContext) inferAnalysis(expression Expr) (TypeAnalysis, bool, error) {
	cache := context.analysisCache()
	context.analysis = cache
	if result, exists := cache.results[expression]; exists {
		return cloneTypeAnalysis(result.analysis), result.known, result.err
	}

	analysis, known, err := inferStaticExprAnalysis(expression, context.staticTypes, context.types)
	cache.results[expression] = typedAnalysisResult{analysis: cloneTypeAnalysis(analysis), known: known, err: err}

	return analysis, known, err
}

func (context typedEvaluationContext) inferType(expression Expr) (ResolvedType, bool, error) {
	analysis, known, err := context.inferAnalysis(expression)

	return analysis.Type, known, err
}

func cloneTypeAnalysis(analysis TypeAnalysis) TypeAnalysis {
	analysis.Type = cloneResolvedType(analysis.Type)
	analysis.Facts = cloneTypeAnalysisFacts(analysis.Facts)

	return analysis
}

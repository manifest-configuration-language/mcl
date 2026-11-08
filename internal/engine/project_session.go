package engine

// ProjectSessionは一回のプロジェクト評価で共有するモジュールキャッシュと型解析状態を保持します。
type ProjectSession struct {
	runtimeCache map[string][]RuntimeEntry
	runtimeTypes map[string]map[string]ResolvedType
	analysis     *TypeAnalysisContext
}

func newProjectSession() *ProjectSession {
	return &ProjectSession{
		runtimeCache: make(map[string][]RuntimeEntry),
		runtimeTypes: make(map[string]map[string]ResolvedType),
		analysis:     &TypeAnalysisContext{},
	}
}

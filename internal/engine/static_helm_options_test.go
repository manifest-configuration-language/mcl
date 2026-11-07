package engine

import (
	"strings"
	"testing"
)

func TestHelmRenderStaticOptionsValidateKnownFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries []ExprMapEntry
		wantErr string
	}{
		{
			name: "supported options",
			entries: []ExprMapEntry{
				{Key: "values", Value: &MapExpr{}},
				{Key: "revision", Value: &IntegerExpr{Value: "2"}},
				{Key: "releaseName", Value: &StringExpr{Value: "release"}},
				{Key: "namespace", Value: &StringExpr{Value: "apps"}},
				{Key: "operation", Value: &StringExpr{Value: operationRollback}},
				{Key: "kubeVersion", Value: &StringExpr{Value: "1.30.0"}},
				{Key: helmOptionVersion, Value: &StringExpr{Value: "1.2.3"}},
				{Key: "now", Value: &StringExpr{Value: "2026-11-07T00:00:00Z"}},
				{Key: "apiVersions", Value: &ListExpr{Items: []Expr{&StringExpr{Value: "example.test/v1"}}}},
				{Key: "lookupResources", Value: &EmptyListExpr{}},
			},
		},
		{name: "unknown option", entries: []ExprMapEntry{{Key: "revison", Value: &IntegerExpr{Value: "1"}}}, wantErr: `no option named "revison"`},
		{name: "incorrect option type", entries: []ExprMapEntry{{Key: "revision", Value: &StringExpr{Value: "1"}}}, wantErr: "option revision must be Int"},
		{name: "unsupported operation", entries: []ExprMapEntry{{Key: "operation", Value: &StringExpr{Value: "install"}}}, wantErr: "operation must be create, update, delete, rollback, or verify"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			options := &MapExpr{Entries: test.entries}
			optionsType, known, err := inferStaticExprType(options, nil, nil)
			if err != nil || !known {
				t.Fatalf("infer options type = %#v, %t, %v", optionsType, known, err)
			}
			_, _, err = inferStaticAdapterBuiltin(builtin{name: fnHelmRender}, []Expr{&StringExpr{Value: "test-chart"}, options}, []ResolvedType{{Name: typeString}, optionsType}, []bool{true, true})
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("infer helm.render = %v", err)
				}

				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("infer helm.render error = %v, want text %q", err, test.wantErr)
			}
		})
	}
}

func TestHelmRenderStaticOptionsAllowDynamicMap(t *testing.T) {
	t.Parallel()

	anyType := ResolvedType{Name: typeAny}
	dynamicMap := ResolvedType{Name: typeMap, Element: &anyType}
	_, _, err := inferStaticAdapterBuiltin(builtin{name: fnHelmRender}, []Expr{&StringExpr{Value: "test-chart"}, &ReferenceExpr{Name: "options"}}, []ResolvedType{{Name: typeString}, dynamicMap}, []bool{true, true})
	if err != nil {
		t.Fatalf("infer helm.render with dynamic Map = %v", err)
	}

	_, _, err = inferStaticAdapterBuiltin(builtin{name: fnHelmRender}, []Expr{&StringExpr{Value: "test-chart"}, &ReferenceExpr{Name: "options"}}, []ResolvedType{{Name: typeString}, {Name: typeAny}}, []bool{true, true})
	if err != nil {
		t.Fatalf("infer helm.render with Any = %v", err)
	}
}

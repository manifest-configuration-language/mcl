package value

import (
	"testing"

	semtypes "github.com/manifest-configuration-language/mcl/internal/types"
)

func TestCloneTypingOwnsNestedTypeData(t *testing.T) {
	typeValue := semtypes.ResolvedType{
		Name: "Record",
		Fields: []semtypes.TypeField{{
			Name: "items",
			Type: semtypes.ResolvedType{Element: &semtypes.ResolvedType{Name: "String"}},
		}},
	}
	typing := &ValueTyping{
		TypeProvenance: []TypeProvenance{{
			Type:  typeValue,
			Types: map[string]semtypes.ResolvedType{"Record": typeValue},
		}},
		SchemaProvenance: &SchemaProvenance{Type: &typeValue},
	}

	cloned := CloneTyping(typing)
	cloned.TypeProvenance[0].Type.Fields[0].Type.Element.Name = "Int"
	cloned.TypeProvenance[0].Types["Record"].Fields[0].Name = "changed"
	cloned.SchemaProvenance.Type.Fields[0].Type.Element.Name = "Bool"

	if got := typing.TypeProvenance[0].Type.Fields[0].Type.Element.Name; got != "String" {
		t.Fatalf("original type provenance element = %q, want String", got)
	}
	if got := typing.TypeProvenance[0].Types["Record"].Fields[0].Name; got != "items" {
		t.Fatalf("original type environment field = %q, want items", got)
	}
	if got := typing.SchemaProvenance.Type.Fields[0].Type.Element.Name; got != "String" {
		t.Fatalf("original schema provenance element = %q, want String", got)
	}
}

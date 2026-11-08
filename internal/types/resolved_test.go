package types

import (
	"reflect"
	"testing"
)

func TestCloneCopiesNestedTypeData(t *testing.T) {
	t.Parallel()
	source := ResolvedType{
		Name: "Function", TypeParameters: []string{"T"},
		Arguments: []ResolvedType{{Name: "Argument", Options: []ResolvedType{{Name: "String"}}}},
		Fields:    []TypeField{{Name: "field", Type: ResolvedType{Name: "Field", Element: new(ResolvedType)}}},
		Element:   new(ResolvedType), AdditionalProperties: new(ResolvedType),
		Parameters: []FunctionTypeParameter{{Name: "parameter", Type: ResolvedType{Name: "Parameter"}}},
		ReturnType: new(ResolvedType),
	}
	*source.Fields[0].Type.Element = ResolvedType{Name: "FieldElement"}
	*source.Element = ResolvedType{Name: "Element"}
	*source.AdditionalProperties = ResolvedType{Name: "Additional"}
	*source.ReturnType = ResolvedType{Name: "Return"}

	cloned := Clone(source)
	cloned.TypeParameters[0] = "U"
	cloned.Arguments[0].Options[0].Name = "ChangedArgumentOption"
	cloned.Fields[0].Type.Element.Name = "ChangedFieldElement"
	cloned.Element.Name = "ChangedElement"
	cloned.AdditionalProperties.Name = "ChangedAdditional"
	cloned.Parameters[0].Type.Name = "ChangedParameter"
	cloned.ReturnType.Name = "ChangedReturn"

	if source.TypeParameters[0] != "T" || source.Arguments[0].Options[0].Name != "String" ||
		source.Fields[0].Type.Element.Name != "FieldElement" || source.Element.Name != "Element" ||
		source.AdditionalProperties.Name != "Additional" || source.Parameters[0].Type.Name != "Parameter" ||
		source.ReturnType.Name != "Return" {
		t.Fatalf("Clone() shares nested type data with its input: %#v", source)
	}
}

func TestSubstituteDoesNotMutateInputTypes(t *testing.T) {
	t.Parallel()
	source := ResolvedType{
		Name:      "Record",
		Options:   []ResolvedType{{Name: "T"}},
		Arguments: []ResolvedType{{Name: "List", Element: new(ResolvedType)}},
		Fields:    []TypeField{{Name: "field", Type: ResolvedType{Name: "T"}}},
		Element:   new(ResolvedType), AdditionalProperties: new(ResolvedType),
		Parameters: []FunctionTypeParameter{{Name: "parameter", Type: ResolvedType{Name: "T"}}},
		ReturnType: new(ResolvedType),
	}
	*source.Arguments[0].Element = ResolvedType{Name: "T"}
	*source.Element = ResolvedType{Name: "T"}
	*source.AdditionalProperties = ResolvedType{Name: "T"}
	*source.ReturnType = ResolvedType{Name: "T"}
	bindings := map[string]ResolvedType{"T": {Name: "String"}}
	sourceBefore := Clone(source)
	bindingBefore := Clone(bindings["T"])

	substituted := Substitute(source, bindings)

	if !reflect.DeepEqual(source, sourceBefore) {
		t.Fatalf("Substitute() mutated its source type: got %#v, want %#v", source, sourceBefore)
	}
	if !reflect.DeepEqual(bindings["T"], bindingBefore) {
		t.Fatalf("Substitute() mutated a binding: got %#v, want %#v", bindings["T"], bindingBefore)
	}
	if substituted.Options[0].Name != "String" || substituted.Arguments[0].Element.Name != "String" ||
		substituted.Fields[0].Type.Name != "String" || substituted.Element.Name != "String" ||
		substituted.AdditionalProperties.Name != "String" || substituted.Parameters[0].Type.Name != "String" ||
		substituted.ReturnType.Name != "String" {
		t.Fatalf("Substitute() did not replace nested type variables: %#v", substituted)
	}
}

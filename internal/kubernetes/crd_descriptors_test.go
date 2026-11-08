package kubernetes

import (
	"reflect"
	"testing"
)

func TestCanonicalizeCRDDescriptors(t *testing.T) {
	input := []Descriptor{
		{APIGroup: "z.example.com", Kind: "Widget", ResourceName: "widgets", Scope: ScopeNamespaced},
		{APIGroup: "a.example.com", Kind: "Gadget", ResourceName: "gadgets", Scope: ScopeCluster},
		{APIGroup: "z.example.com", Kind: "Widget", ResourceName: "widgets", Scope: ScopeNamespaced},
	}

	got, err := CanonicalizeCRDDescriptors(input)
	if err != nil {
		t.Fatalf("CanonicalizeCRDDescriptors() error = %v", err)
	}
	want := []Descriptor{
		{APIGroup: "a.example.com", Kind: "Gadget", ResourceName: "gadgets", Scope: ScopeCluster},
		{APIGroup: "z.example.com", Kind: "Widget", ResourceName: "widgets", Scope: ScopeNamespaced},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CanonicalizeCRDDescriptors() = %#v, want %#v", got, want)
	}
}

func TestCanonicalizeCRDDescriptorsRejectsConflicts(t *testing.T) {
	input := []Descriptor{
		{APIGroup: "example.com", Kind: "Widget", ResourceName: "widgets", Scope: ScopeNamespaced},
		{APIGroup: "example.com", Kind: "Widget", ResourceName: "other-widgets", Scope: ScopeNamespaced},
	}

	got, err := CanonicalizeCRDDescriptors(input)
	if got != nil {
		t.Fatalf("CanonicalizeCRDDescriptors() descriptors = %#v, want nil", got)
	}
	const wantError = "CRD versions have conflicting resource descriptors for example.com/Widget"
	if err == nil || err.Error() != wantError {
		t.Fatalf("CanonicalizeCRDDescriptors() error = %v, want %q", err, wantError)
	}
}

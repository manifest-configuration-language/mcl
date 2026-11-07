package languageservice

import (
	"strings"
	"testing"
)

const testHelmRenderReleaseName = "releaseName"

func TestCompletionOffersHelmRenderOptions(t *testing.T) {
	t.Parallel()
	source := `let first = helm.render("first", {releaseName: "sample"})
let second = helm.render("second", { })`
	lines := strings.Split(source, "\n")
	character := strings.Index(lines[1], "{") + 1
	items := CompletionForSource(source, 1, character, "")
	got := make(map[string]CompletionItem, len(items))
	for _, item := range items {
		got[item.Label] = item
	}
	for name, typeName := range map[string]string{testHelmRenderReleaseName: "String", "revision": "Int", "operation": "create | update | delete | rollback | verify"} {
		item, ok := got[name]
		if !ok {
			t.Errorf("completion omitted %q: %#v", name, items)

			continue
		}
		if item.Detail != typeName {
			t.Errorf("completion %q detail = %q, want %q", name, item.Detail, typeName)
		}
		if item.InsertText != name+": " {
			t.Errorf("completion %q insert text = %q, want %q", name, item.InsertText, name+": ")
		}
	}
}

func TestCompletionOmitsExistingHelmRenderOptions(t *testing.T) {
	t.Parallel()
	source := `let resources = helm.render("chart", {releaseName: "sample", })`
	character := strings.Index(source, ",") + 1
	items := CompletionForSource(source, 0, character, "")
	for _, item := range items {
		if item.Label == testHelmRenderReleaseName {
			t.Fatal("completion included the already-specified releaseName option")
		}
	}
}

func TestCompletionDoesNotRepeatExistingHelmRenderAssignment(t *testing.T) {
	t.Parallel()
	source := `let resources = helm.render("chart", {re: null})`
	character := strings.Index(source, "re: null") + 2
	items := CompletionForSource(source, 0, character, "")
	found := false
	for _, item := range items {
		if item.Label == testHelmRenderReleaseName {
			found = true
			if item.InsertText != testHelmRenderReleaseName {
				t.Fatalf("completion for existing assignment = %q, want key only", item.InsertText)
			}
		}
	}
	if !found {
		t.Fatal("completion omitted releaseName")
	}
}

func TestCompletionDoesNotOfferHelmRenderOptionsInsideValues(t *testing.T) {
	t.Parallel()
	source := `let resources = helm.render("chart", {values: {re: null}})`
	character := strings.Index(source, "re: null") + 2
	items := CompletionForSource(source, 0, character, "")
	for _, item := range items {
		if item.Label == "releaseName" || item.Label == "revision" {
			t.Fatalf("completion inside values included Helm render option %q", item.Label)
		}
	}
}

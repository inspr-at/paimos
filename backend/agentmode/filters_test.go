package agentmode

import (
	"net/url"
	"testing"
)

func TestFiltersCanonicalizeStatesExcludeSelectionAndRejectAmbiguity(t *testing.T) {
	first, err := ParseFilters(url.Values{
		"state": {"active", "pending", "active"}, "attention": {"required"},
		"selected_delivery": {"delivery:one"}, "q": {"  Ship  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseFilters(url.Values{
		"state": {"pending", "active"}, "attention": {"required"},
		"selected_delivery": {"delivery:two"}, "q": {"ship"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.CanonicalFingerprint() != second.CanonicalFingerprint() {
		t.Fatal("selector or state order changed the canonical filter fingerprint")
	}
	for _, values := range []url.Values{
		{"project_id": {"1", "2"}},
		{"lane_key": {"project:1/epic:0"}},
		{"q": {string([]byte{0xff})}},
		{"q": {"line\nbreak"}},
		{"unknown": {"value"}},
	} {
		if _, err := ParseFilters(values); err == nil {
			t.Fatalf("ambiguous/invalid filters accepted: %#v", values)
		}
	}
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeDraftEmptyLists(t *testing.T) {
	d := Draft{}
	normalizeDraft(&d)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, field := range []string{`"unresolved":null`, `"requirements":null`, `"constraints":null`, `"requirement_refs":null`, `"constraint_refs":null`} {
		if strings.Contains(body, field) {
			t.Fatalf("normalized draft still has %s in %s", field, body)
		}
	}
	if d.Unresolved == nil || d.Requirements == nil || d.Constraints == nil {
		t.Fatalf("normalize left nil slices: %+v", d)
	}
}

func TestNormalizeWorkflowEmptyLists(t *testing.T) {
	w := Workflow{Draft: &Draft{}}
	normalizeWorkflow(&w)
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if strings.Contains(body, `"unresolved":null`) || strings.Contains(body, `"batches":null`) || strings.Contains(body, `"runtimes":null`) {
		t.Fatalf("normalized workflow still has null lists: %s", body)
	}
}

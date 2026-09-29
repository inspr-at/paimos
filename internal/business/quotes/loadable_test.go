// SPDX-License-Identifier: AGPL-3.0-only

package quotes

import (
	"encoding/json"
	"strings"
	"testing"
)

// AEON-274: a nil list or map in a profile definition must never be stored as
// null, and stored JSON with null lists must be refused.
func TestProfileDefinitionNeverEncodesNullLists(t *testing.T) {
	raw, err := json.Marshal(profileDefinition{Schema: "inspr.document-profile.v1"}.normalized())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"fonts":[]`, `"colors":{}`, `"typography":{}`, `"cover":{}`, `"sections":{}`, `"labels":{}`, `"columns":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s in %s", want, raw)
		}
	}
	if strings.Contains(string(raw), "null") {
		t.Fatalf("null in %s", raw)
	}
	if err := checkProfileJSON("definition", raw); err != nil {
		t.Fatal(err)
	}
	// Also inside a document snapshot.
	snapshot, err := json.Marshal(normalizedSnapshot(&documentProfileSnapshot{ID: "x", Revision: 1}))
	if err != nil || !strings.Contains(string(snapshot), `"fonts":[]`) {
		t.Fatalf("snapshot: %s %v", snapshot, err)
	}
	var def map[string]any
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatal(err)
	}
	def["fonts"] = nil
	broken, _ := json.Marshal(def)
	if err := checkProfileJSON("definition", broken); err == nil || !strings.Contains(err.Error(), "fonts") {
		t.Fatalf("null fonts accepted: %v", err)
	}
	stored, err := marshalProfile(profileDefinition{})
	if err != nil || strings.Contains(string(stored), "null") {
		t.Fatalf("marshalProfile must normalise: %s %v", stored, err)
	}
}

func TestMarshalDraftWritesEmptyLists(t *testing.T) {
	doc := quoteDocument{SchemaVersion: 1, MinimumWriterVersion: 1, Sender: json.RawMessage(`{}`), Recipient: json.RawMessage(`{}`), Legal: json.RawMessage(`{}`), Layout: json.RawMessage(`{}`)}
	doc.Sections = []documentSection{{ID: "11111111-1111-4111-8111-111111111111", Heading: "Scope"}}
	doc.Profile = &documentProfileSnapshot{ID: "11111111-1111-4111-8111-111111111111", Revision: 1, Definition: profileDefinition{Schema: "inspr.document-profile.v1"}}
	// A frozen version is never rewritten (its digest covers it): refused.
	if _, err := marshalDocument(doc); err == nil {
		t.Fatal("marshalDocument accepted null lists")
	}
	raw, err := marshalDraft(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"positions":[]`, `"nodes":[]`, `"fonts":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s in %s", want, raw)
		}
	}
	if doc.Sections[0].Nodes != nil || doc.Profile.Definition.Fonts != nil {
		t.Fatal("marshalDraft changed the caller's sections")
	}
	if err := CheckLoadableDocument(raw); err != nil {
		t.Fatalf("normalised document not loadable: %v", err)
	}
	for _, broken := range []string{
		strings.Replace(string(raw), `"fonts":[]`, `"fonts":null`, 1),
		strings.Replace(string(raw), `"labels":{}`, `"labels":null`, 1),
		strings.Replace(string(raw), `"positions":[]`, `"positions":null`, 1),
		strings.Replace(string(raw), `"nodes":[]`, `"nodes":null`, 1),
	} {
		if err := CheckLoadableDocument([]byte(broken)); err == nil {
			t.Errorf("stored null reported loadable: %s", broken)
		}
	}
}

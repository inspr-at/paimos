// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestClassificationKeepsEmptyNoteArrays(t *testing.T) {
	live := map[string]TicketMeta{"AEON-1": {PublicBenefit: true}}
	empty := &Notes{
		Items:       []NoteItem{},
		Gaps:        []string{},
		PublicItems: []TicketNote{{Key: "AEON-1"}},
	}
	classified := applyClassification(empty, live)
	raw, err := json.Marshal(classified)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"items":[]`)) || bytes.Contains(raw, []byte(`"items":null`)) {
		t.Fatalf("items: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"gaps":[]`)) || bytes.Contains(raw, []byte(`"gaps":null`)) {
		t.Fatalf("gaps: %s", raw)
	}
	if classified == empty {
		t.Fatal("classification mutated the captured notes")
	}
	if empty.Items == nil || empty.Gaps == nil {
		t.Fatal("classification changed the captured empty arrays")
	}

	unset := &Notes{PublicItems: []TicketNote{{Key: "AEON-1"}}}
	kept := applyClassification(unset, live)
	raw, err = json.Marshal(kept)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"items":null`)) || !bytes.Contains(raw, []byte(`"gaps":null`)) {
		t.Fatalf("nil notes became arrays: %s", raw)
	}
}

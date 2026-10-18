// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNoteCorrectionsPreserveOriginalAndVisibility(t *testing.T) {
	r, raw := reservedFixture(t)
	b := EmptyProductNotes()
	if err := b.AddReserved(raw, r, historicTenant, historicProject); err != nil {
		t.Fatal(err)
	}
	h := withProductNotes(History{Product: b.Product, Repository: b.Repository, Releases: []Release{{Version: r.Version, State: StatePublished, Notes: MissingNotes()}}}, b)
	correction := NoteCorrection{Version: r.Version, Key: "AEON-1", SHA256: b.Releases[r.Version].SHA256, Group: GroupFixes, Reason: "Reviewed repair classification."}
	layer := func(c []NoteCorrection) []byte {
		raw, _ := json.Marshal(map[string]any{"schema": "aeon.product-note-corrections.v1", "corrections": c})
		return raw
	}
	got, err := withNoteCorrections(h, b, layer([]NoteCorrection{correction}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Releases[0].Notes.PublicItems[0].Group != GroupFixes || len(got.Releases[0].Notes.Corrections) != 1 || got.Releases[0].Notes.SHA256 != h.Releases[0].Notes.SHA256 {
		t.Fatal("missing correction/provenance")
	}
	if h.Releases[0].Notes.PublicItems[0].Group != GroupFeatures || b.Releases[r.Version].Items[0].Group != GroupFeatures {
		t.Fatal("rewrote original")
	}
	for _, key := range []string{"AEON-3", "AEON-999"} {
		bad := correction
		bad.Key = key
		if _, err := withNoteCorrections(h, b, layer([]NoteCorrection{bad})); err == nil {
			t.Fatal("made hidden/absent ticket public")
		}
	}
	bad := correction
	bad.SHA256 = strings.Repeat("0", 64)
	if _, err := withNoteCorrections(h, b, layer([]NoteCorrection{bad})); err == nil {
		t.Fatal("accepted different original digest")
	}
	if _, err := withNoteCorrections(h, b, layer([]NoteCorrection{correction, correction})); err == nil {
		t.Fatal("accepted duplicate correction")
	}
}

func TestReviewedAuditCoversFrozenOccurrences(t *testing.T) {
	raw, err := data.ReadFile("data/product-notes.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ReadProductNotes(raw)
	if err != nil {
		t.Fatal(err)
	}
	layer, err := data.ReadFile("data/product-note-corrections.json")
	if err != nil {
		t.Fatal(err)
	}
	h := History{Product: b.Product, Repository: b.Repository}
	for v := range b.Releases {
		h.Releases = append(h.Releases, Release{Version: v, State: StatePublished, Notes: MissingNotes()})
	}
	h, err = withNoteCorrections(withProductNotes(h, b), b, layer)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	repairs := map[string]string{
		"AEON-401": "260930094206.0.0",
		"AEON-439": "260930115354.0.0",
	}
	for _, r := range h.Releases {
		for _, c := range r.Notes.Corrections {
			count++
			if repairs[c.Key] == r.Version {
				if c.Group != GroupFixes || strings.TrimSpace(c.Reason) == "" || c.SHA256 != b.Releases[r.Version].SHA256 {
					t.Fatal("repair lost its reviewed binding", c.Key)
				}
				delete(repairs, c.Key)
			}
			for _, i := range r.Notes.PublicItems {
				if i.Key == c.Key && i.Group != GroupFixes {
					t.Fatal("audit fix remained a feature", i.Key)
				}
			}
		}
	}
	if count < 80 {
		t.Fatalf("audit layer only covers %d occurrences", count)
	}
	if len(repairs) != 0 {
		t.Fatalf("missing release 14/14.1 repair corrections: %v", repairs)
	}
}

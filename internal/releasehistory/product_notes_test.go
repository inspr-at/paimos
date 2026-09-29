// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductNotesProjectionAndImmutability(t *testing.T) {
	s := noteFixture()
	s.Frozen = true
	s.Tickets[0].Key = "AEON-7"
	s.Tickets[0].Group = GroupFixes
	s.Tickets[0].Fields = json.RawMessage(`{"pill_en":"Captured release notes","pill_de":"","benefit_en":"Preserve the release text.","benefit_de":"","private_comment":"NEVER EMBED INTERNAL COMMENTS"}`)
	hidden := s.Tickets[0]
	hidden.ID = "55555555-5555-4555-8555-555555555555"
	hidden.Key = "AEON-8"
	hidden.Fields = json.RawMessage(`{"hide_from_release_notes":true,"pill_en":"HIDDEN NOTE TEXT"}`)
	s.Tickets = append(s.Tickets, hidden)
	raw, _ := json.Marshal(s)
	notes, err := PublicNotesFromSnapshot(raw, notesVersion, s.TenantID, s.ProjectID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes.Items) != 1 || notes.Items[0].Group != GroupFixes || notes.Items[0].PillDE != "" {
		t.Fatalf("notes %+v", notes)
	}
	bundle := EmptyProductNotes()
	if err := bundle.Add(notesVersion, notes); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Add(notesVersion, notes); err != nil {
		t.Fatal("identical export", err)
	}
	encoded, _ := json.Marshal(bundle)
	for _, private := range []string{s.TenantID, s.ProjectID, s.ReleaseID, s.Tickets[0].ID, "AEON-8", "HIDDEN", "INTERNAL", "private_comment", "fields", "hide_from_release_notes"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private projection: %s", private)
		}
	}
	if _, err := ReadProductNotes(encoded); err != nil {
		t.Fatal(err)
	}
	changed := notes
	changed.Revision++
	if err := bundle.Add(notesVersion, changed); err == nil {
		t.Fatal("overwrote immutable version")
	}
	if _, err := PublicNotesFromSnapshot(raw, notesVersion, s.ProjectID, s.TenantID, false); err == nil {
		t.Fatal("accepted wrong ownership")
	}
	s.Frozen = false
	raw, _ = json.Marshal(s)
	if _, err := PublicNotesFromSnapshot(raw, notesVersion, s.TenantID, s.ProjectID, false); err == nil {
		t.Fatal("backfilled from live preview")
	}
	if _, err := PublicNotesFromSnapshot(raw, notesVersion, s.TenantID, s.ProjectID, true); err != nil {
		t.Fatal("explicit reservation", err)
	}
	s.Tickets[0].Fields = json.RawMessage(`{"hide_from_release_notes":"true","pill_en":"Must not become public"}`)
	raw, _ = json.Marshal(s)
	if _, err := PublicNotesFromSnapshot(raw, notesVersion, s.TenantID, s.ProjectID, true); err == nil {
		t.Fatal("invalid hide flag became public")
	}
	for _, bad := range []string{strings.Replace(string(encoded), `"items":`, `"internal_comment":"private","items":`, 1), strings.Replace(string(encoded), "AEON-7", "CLIENT-7", 1), string(encoded) + "{}"} {
		if _, err := ReadProductNotes([]byte(bad)); err == nil {
			t.Fatal("accepted invalid public bundle")
		}
	}
}

func TestProductNotesNeverReadLiveTickets(t *testing.T) {
	h := History{Product: "PAIMOS AEON", Repository: "inspr-at/aeon", Releases: []Release{{Version: notesVersion, Notes: &Notes{Source: ProductNotesSource, Items: []NoteItem{{Key: "AEON-7", Group: GroupFixes, PillEN: "Frozen fix", BenefitEN: "Captured benefit."}}}, Changes: []Change{{Commit: "a", Subject: "AEON-7: repair the release", Type: "other", Tickets: []string{"AEON-7"}}}}, {Version: "260927120000.0.0", Notes: MissingNotes(), Changes: []Change{{Commit: "b", Subject: "AEON-7: an older change", Type: "other", Tickets: []string{"AEON-7"}}}}}}
	mod := NewWith(h, notesVersion)
	mod.UseTickets(func(context.Context, string, []string) (map[string]TicketMeta, error) {
		t.Fatal("read live ticket fields")
		return nil, nil
	})
	got := mod.annotated(t.Context(), h)
	if got.Releases[0].Changes[0].Group != GroupFixes || got.Releases[0].Changes[0].Linked[0].PillEN != "Frozen fix" {
		t.Fatal(got)
	}
	if got.Releases[1].Changes[0].Group != GroupOther || len(got.Releases[1].Changes[0].Linked) != 0 {
		t.Fatal("invented historical notes")
	}
	if h.Releases[0].Changes[0].Group != "" {
		t.Fatal("mutated shared history")
	}
	// Empty and hidden-only captures must never be filled with bundle members.
	for _, notes := range []*Notes{{Source: "database-snapshot", Items: []NoteItem{}}, {Source: "database-snapshot", Hidden: 1, Items: []NoteItem{}}} {
		h.Releases[0].Notes = notes
		bundle := EmptyProductNotes()
		bundle.Releases[notesVersion] = PublicNotes{Items: []TicketNote{{Key: "AEON-7", PillEN: "Portable"}}}
		if got := withProductNotes(h, bundle); got.Releases[0].Notes != notes {
			t.Fatal("empty capture replaced")
		}
	}
	h.Releases[0].Notes = MissingNotes()
	bundle := EmptyProductNotes()
	bundle.Releases[notesVersion] = PublicNotes{Items: []TicketNote{{Key: "AEON-7", PillEN: "Portable"}}}
	h.Product = "Other product"
	if got := withProductNotes(h, bundle); got.Releases[0].Notes.Source != "unavailable" {
		t.Fatal("notes crossed product boundary")
	}
}

func TestHistoryExportIgnoresLiveAnnotations(t *testing.T) {
	s := noteFixture()
	s.Tickets[0].Key = "AEON-7"
	raw, _ := json.Marshal(s)
	notes, err := NotesFromSnapshot(raw, notesVersion, "database-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	h := History{Schema: Schema, Product: "PAIMOS AEON", Repository: "inspr-at/aeon", Releases: []Release{
		{Version: notesVersion, Notes: notes, Changes: []Change{{Linked: []TicketNote{{Key: "AEON-7", PillEN: "LIVE EDIT NEVER EMBED"}}}}},
		{Version: "260927120000.0.0", Notes: MissingNotes(), Changes: []Change{{Linked: []TicketNote{{Key: "AEON-9", PillEN: "UNFROZEN NEVER EMBED"}}}}},
	}}
	bundle := EmptyProductNotes()
	if err := bundle.AddHistory(h); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(bundle)
	if len(bundle.Releases) != 1 || strings.Contains(string(encoded), "NEVER EMBED") || strings.Contains(string(encoded), s.Tickets[0].ID) {
		t.Fatal("live data or tenant ID entered public bundle")
	}
	h.Repository = "another/product"
	if err := bundle.AddHistory(h); err == nil {
		t.Fatal("accepted another product")
	}
}

func TestPackNotesCommandReservesPublicProjection(t *testing.T) {
	dir := repo(t)
	s := noteFixture()
	s.Version = "260923143005.0.0" // fixture version.json
	s.Tickets[0].Key = "AEON-7"
	s.Tickets[0].Group = GroupFeatures
	raw, _ := json.Marshal(s)
	input := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "./packnotes", "-repo", dir, "-snapshot", input, "-reserve", s.Version, "-tenant", s.TenantID, "-project", s.ProjectID)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("packnotes: %v %s", err, out)
	}
	built, err := Build(t.Context(), Options{Repo: dir, Repository: "inspr-at/aeon"})
	if err != nil {
		t.Fatal(err)
	}
	if got := built.Releases[0].Notes; got.Source != ProductNotesSource || len(got.Items) != 0 || len(got.PublicItems) != 1 || got.PublicItems[0].Group != GroupFeatures {
		t.Fatal(got)
	}
}

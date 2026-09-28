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
	"time"
)

const notesVersion = "260928120000.0.0"

func noteFixture() NoteSnapshot {
	at := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	return NoteSnapshot{Schema: SnapshotSchema, TenantID: "11111111-1111-4111-8111-111111111111", ProjectID: "22222222-2222-4222-8222-222222222222", ReleaseID: "33333333-3333-4333-8333-333333333333", Version: notesVersion, VersionScheme: "inspr-calendar-v2", Revision: 7, CapturedAt: at, MembershipSource: MembershipSource, FieldSource: FieldSource, Tickets: []NoteTicket{{ID: "44444444-4444-4444-8444-444444444444", Key: "TEST-7", Position: 2, UpdatedAt: &at, Fields: json.RawMessage(`{"pill_en":"Clear release notes","pill_de":"Verständliche Release Notes","benefit_en":"Tickets explain what you gain.","benefit_de":"Tickets erklären den Nutzen."}`)}}}
}
func TestNotesSnapshotLanguagesHiddenGapsAndDuplicates(t *testing.T) {
	s := noteFixture()
	original := s.Tickets[0]
	hidden := original
	hidden.ID = "55555555-5555-4555-8555-555555555555"
	hidden.Key = "TEST-8"
	hidden.Fields = json.RawMessage(strings.Replace(string(hidden.Fields), `{`, `{"hide_from_release_notes":true,`, 1))
	missing := original
	missing.ID = "66666666-6666-4666-8666-666666666666"
	missing.Key = "TEST-9"
	missing.Fields = json.RawMessage(`{"pill_en":"No translation"}`)
	s.Tickets = append(s.Tickets, hidden, missing, original)
	raw, _ := json.Marshal(s)
	notes, err := NotesFromSnapshot(raw, notesVersion, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(notes.Items) != 1 || notes.Hidden != 1 || len(notes.Gaps) != 1 || len(notes.SHA256) != 64 || notes.Items[0].BenefitDE != "Tickets erklären den Nutzen." {
		t.Fatalf("notes: %+v", notes)
	}
	encoded, _ := json.Marshal(notes)
	if strings.Contains(string(encoded), "TEST-8") {
		t.Fatal("hidden ticket copied into notes")
	}
	unavailable := hidden
	unavailable.ID = "77777777-7777-4777-8777-777777777777"
	unavailable.Key = "TEST-10"
	unavailable.Unavailable = "Member was deleted before capture."
	s.Tickets = append(s.Tickets, unavailable)
	raw, _ = json.Marshal(s)
	notes, err = NotesFromSnapshot(raw, notesVersion, "fixture")
	if err != nil || notes.Hidden != 2 {
		t.Fatalf("hidden unavailable: %+v %v", notes, err)
	}
	encoded, _ = json.Marshal(notes)
	if strings.Contains(string(encoded), "TEST-10") {
		t.Fatal("hidden deleted key copied into notes")
	}
	s.Tickets = s.Tickets[:len(s.Tickets)-1]
	s.Tickets[len(s.Tickets)-1].Fields = json.RawMessage(`{}`)
	raw, _ = json.Marshal(s)
	if _, err := NotesFromSnapshot(raw, notesVersion, "fixture"); err == nil {
		t.Fatal("conflicting duplicate accepted")
	}
	s = noteFixture()
	s.Version = "260927120000.0.0"
	raw, _ = json.Marshal(s)
	if _, err := NotesFromSnapshot(raw, notesVersion, "fixture"); err == nil {
		t.Fatal("wrong release accepted")
	}
	s = noteFixture()
	s.Tickets = []NoteTicket{}
	raw, _ = json.Marshal(s)
	notes, err = NotesFromSnapshot(raw, notesVersion, "fixture")
	if err != nil || len(notes.Items) != 0 || len(notes.Gaps) != 0 {
		t.Fatal("known empty membership", notes, err)
	}
}
func TestBuildUsesOnlyTaggedSnapshotAndKeepsOfflineGaps(t *testing.T) {
	dir := repo(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	s := noteFixture()
	raw, _ := json.Marshal(s)
	if err := os.Mkdir(filepath.Join(dir, "release-notes"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "release-notes", notesVersion+".json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "release-notes")
	run("commit", "-qm", "Record linked ticket fields")
	run("tag", "-a", "v"+notesVersion, "-m", "Misleading git headline TEST-999")
	// Later edits and unrelated working-tree snapshots cannot rewrite history.
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := Build(context.Background(), Options{Repo: dir})
	if err != nil {
		t.Fatal(err)
	}
	if h.Releases[0].Notes.Items[0].Key != "TEST-7" || h.Releases[0].Notes.Items[0].BenefitEN != "Tickets explain what you gain." {
		t.Fatal(h.Releases[0])
	}
	for _, r := range h.Releases[1:] {
		if r.Notes == nil || len(r.Notes.Gaps) == 0 || len(r.Notes.Items) != 0 {
			t.Fatal("invented historical notes", r)
		}
	}
	// Older manifests retain the exact archived Git data without a fabricated extension.
	var archived Release
	if err := json.Unmarshal([]byte(`{"version":"260923143005.0.0","headline":"Archived text","changes":[]}`), &archived); err != nil || archived.Notes != nil || archived.Headline != "Archived text" {
		t.Fatal(archived, err)
	}
}

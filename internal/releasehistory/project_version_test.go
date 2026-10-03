// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectDecoderPreservesTaggedVersionsAndProductBoundary(t *testing.T) {
	for _, pair := range [][2]string{{"legacy", "1.2.3-rc.1"}, {"inspr-calendar-v1", "26.10.03"}, {"inspr-calendar-v1", "26.10.03.12.30.01"}, {"inspr-calendar-v2", notesVersion}, {"inspr-calver-3", notesVersion}} {
		s := noteFixture()
		s.MembershipSource = ShipsInMembershipSource
		s.Frozen = true
		s.VersionScheme, s.Version = pair[0], pair[1]
		s.Tickets[0].Key = "TSK-1"
		raw, _ := json.Marshal(s)
		binding := ProjectSnapshotBinding{s.TenantID, s.ProjectID, s.ReleaseID, s.VersionScheme, s.Version}
		notes, e := ProjectNotesFromSnapshot(raw, binding, "test")
		if e != nil || len(notes.Items) != 1 || notes.Items[0].Key != "TSK-1" {
			t.Fatalf("%v notes=%+v err=%v", pair, notes, e)
		}
		if pair[0] == "legacy" || pair[0] == "inspr-calendar-v1" {
			if _, e := NotesFromSnapshot(raw, s.Version, "product"); e == nil {
				t.Fatal("project scheme crossed product boundary")
			}
			if _, e := PublicNotesFromSnapshot(raw, s.Version, s.TenantID, s.ProjectID, false); e == nil {
				t.Fatal("project scheme packed as product")
			}
		}
		for _, mutate := range []func(*NoteSnapshot){func(s *NoteSnapshot) { s.TenantID = s.ProjectID }, func(s *NoteSnapshot) { s.ProjectID = s.ReleaseID }, func(s *NoteSnapshot) { s.ReleaseID = s.ProjectID }, func(s *NoteSnapshot) { s.Version += "x" }, func(s *NoteSnapshot) { s.VersionScheme = "unknown" }, func(s *NoteSnapshot) { s.Tickets[0].Key = "TSK-0" }, func(s *NoteSnapshot) { s.Tickets = append(s.Tickets, s.Tickets[0]) }} {
			var bad NoteSnapshot
			_ = json.Unmarshal(raw, &bad)
			mutate(&bad)
			b, _ := json.Marshal(bad)
			if _, e := ProjectNotesFromSnapshot(b, binding, "test"); e == nil {
				t.Fatal("accepted broken project capture")
			}
		}
		if _, e := ProjectNotesFromSnapshot([]byte(strings.Replace(string(raw), `"schema":`, `"unknown":1,"schema":`, 1)), binding, "test"); e == nil {
			t.Fatal("unknown field accepted")
		}
	}
	s := noteFixture()
	s.MembershipSource = ShipsInMembershipSource
	s.Version, s.VersionScheme = "", ""
	s.Tickets[0].Key = "TSK-1"
	raw, _ := json.Marshal(s)
	binding := ProjectSnapshotBinding{s.TenantID, s.ProjectID, s.ReleaseID, "", ""}
	if _, e := ProjectNotesFromSnapshot(raw, binding, "draft"); e != nil {
		t.Fatal(e)
	}
	s.Frozen = true
	raw, _ = json.Marshal(s)
	if _, e := ProjectNotesFromSnapshot(raw, binding, "test"); e == nil {
		t.Fatal("frozen versionless capture")
	}
}
func TestActualTaskKeySurvivesProductPacking(t *testing.T) {
	s := noteFixture()
	s.Frozen = true
	s.MembershipSource = ShipsInMembershipSource
	s.Tickets[0].Key = "TSK-1"
	raw, _ := json.Marshal(s)
	notes, e := PublicNotesFromSnapshot(raw, notesVersion, s.TenantID, s.ProjectID, false)
	if e != nil {
		t.Fatal(e)
	}
	bundle := EmptyProductNotes()
	if e = bundle.Add(notesVersion, notes); e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(bundle)
	decoded, e := ReadProductNotes(encoded)
	if e != nil || decoded.Releases[notesVersion].Items[0].Key != "TSK-1" {
		t.Fatalf("key projection=%+v %v", decoded, e)
	}
	for _, key := range []string{"a-1", "T-1", "TSK-0", "TSK-01", strings.Repeat("A", 11) + "-1", "TSK-" + strings.Repeat("1", 27)} {
		if ValidCapturedNoteKey(key) {
			t.Errorf("accepted key %q", key)
		}
	}
}

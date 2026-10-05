// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestFix2SnapshotCardinalityRespectsProvenance(t *testing.T) {
	for _, provenance := range []string{MembershipSource, ManifestMembershipSource, ShipsInMembershipSource} {
		t.Run(provenance, func(t *testing.T) {
			s := noteFixture()
			s.Frozen = true
			s.MembershipSource = provenance
			if provenance == ManifestMembershipSource {
				s.ReleaseID, s.ActorID = "", s.TenantID
				s.Backfilled, s.Label, s.ReleasedAt = true, BackfillLabel, &s.CapturedAt
			}
			template := s.Tickets[0]
			s.Tickets = make([]NoteTicket, 5001)
			for i := range s.Tickets {
				ticket := template
				ticket.ID, ticket.Key, ticket.Position = fmt.Sprintf("44444444-4444-4444-8444-%012d", i+1), fmt.Sprintf("TEST-%d", i+1), i
				s.Tickets[i] = ticket
			}
			raw, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) >= 16<<20 {
				t.Fatal("fixture exceeds retained historical byte bound")
			}
			notes, err := NotesFromSnapshot(raw, s.Version, "historical")
			if provenance == ShipsInMembershipSource {
				if err == nil || !strings.Contains(err.Error(), "identity, version or provenance is incomplete") {
					t.Fatalf("oversized new capture: %+v %v", notes, err)
				}
				binding := ProjectSnapshotBinding{s.TenantID, s.ProjectID, s.ReleaseID, s.VersionScheme, s.Version}
				if _, err := ProjectNotesFromSnapshot(raw, binding, "project"); err == nil {
					t.Fatal("project decoder accepted oversized new capture")
				}
				return
			}
			if err != nil {
				t.Fatalf("historical capture below 16 MiB rejected: %v", err)
			}
			if len(notes.Items) != 5001 || notes.Items[0].Key != "TEST-1" || notes.Items[5000].Key != "TEST-5001" || notes.Items[5000].BenefitEN != "Tickets explain what you gain." || notes.WrittenAfterRelease != (provenance == ManifestMembershipSource) {
				t.Fatalf("historical capture lost members or provenance: count=%d", len(notes.Items))
			}
		})
	}
}

func TestFix2BoundProjectDecoderRetainsHistoricalByteCeiling(t *testing.T) {
	s := noteFixture()
	s.Frozen = true
	s.Tickets[0].Fields = json.RawMessage(`{"benefit_en":"` + strings.Repeat("x", 12<<20) + `"}`)
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 12<<20 || len(raw) >= 16<<20 {
		t.Fatal("fixture does not distinguish the historical and new byte limits")
	}
	binding := ProjectSnapshotBinding{s.TenantID, s.ProjectID, s.ReleaseID, s.VersionScheme, s.Version}
	notes, err := ProjectNotesFromSnapshot(raw, binding, "historical")
	if err != nil || len(notes.Items) != 1 || len(notes.Items[0].BenefitEN) != 12<<20 {
		t.Fatalf("historical byte ceiling: %v", err)
	}
	s.MembershipSource = ShipsInMembershipSource
	raw, err = json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectNotesFromSnapshot(raw, binding, "new"); err == nil || !strings.Contains(err.Error(), "exceeds 12 MiB") {
		t.Fatalf("new byte ceiling: %v", err)
	}
}

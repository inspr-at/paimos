// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"errors"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

func TestAgentPromotionPolicy(t *testing.T) {
	at := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	release := Position{ProjectID: "project", ItemID: "item", ReleaseID: "r1", ReleaseRank: "A", ReleaseState: "planned", Rank: "V", CreatedAt: at}
	laterRelease := release
	laterRelease.ReleaseID, laterRelease.ReleaseRank, laterRelease.ReleaseState = "r2", "B", "building"
	backlog := release
	backlog.ReleaseID, backlog.ReleaseRank, backlog.ReleaseState = "", "", ""
	tail := backlog
	tail.Rank = ""
	laterRank := release
	laterRank.Rank = "W"
	frozen := release
	frozen.ReleaseState = "frozen"
	history := release
	history.ReleaseState = "released"
	abandoned := release
	abandoned.ReleaseState = "abandoned"
	for _, tc := range []struct {
		name          string
		before, after Position
		want          error
	}{
		{"same", release, release, nil}, {"later rank", release, laterRank, nil},
		{"undo later rank", laterRank, release, ErrPromotion},
		{"later release", release, laterRelease, nil}, {"earlier release", laterRelease, release, ErrPromotion},
		{"to backlog", laterRelease, backlog, nil}, {"to tail", backlog, tail, nil},
		{"rank tail", tail, backlog, ErrPromotion}, {"tail to release", tail, release, ErrPromotion},
		{"same tail", tail, tail, nil}, {"frozen removal", frozen, backlog, nil},
		{"out of history", history, tail, ErrHistoryCorrection}, {"into history", tail, history, ErrHistoryCorrection},
		{"abandoned has no position", abandoned, tail, ErrHistoryCorrection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckAgentMove(tc.before, tc.after); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
	bad := tail
	bad.ProjectID = "another"
	if err := CheckAgentMove(tail, bad); !errors.Is(err, ErrProjectChanged) {
		t.Fatal(err)
	}
	bad = tail
	bad.ItemID = "another"
	if err := CheckAgentMove(tail, bad); err == nil {
		t.Fatal("different record allowed")
	}
	bad = release
	bad.ReleaseState = "unknown"
	if err := CheckAgentMove(release, bad); err == nil {
		t.Fatal("unknown state allowed")
	}
}

func TestPlacementPrecondition(t *testing.T) {
	for _, tc := range []struct {
		project          string
		revision         int64
		expectedProject  string
		expectedRevision int64
		want             error
	}{
		{"p", 0, "p", 0, nil}, {"p", 1, "p", 1, nil},
		{"p", 2, "p", 1, ErrRevisionChanged}, {"p", 1, "p", 0, ErrRevisionChanged},
		{"q", 0, "p", 0, ErrProjectChanged},
	} {
		if err := CheckPrecondition(tc.project, tc.revision, tc.expectedProject, tc.expectedRevision); !errors.Is(err, tc.want) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	if err := CheckPrecondition("p", 0, "", 0); err == nil {
		t.Fatal("missing expected project accepted")
	}
	if err := CheckPrecondition("p", 1, "p", -1); err == nil {
		t.Fatal("negative revision accepted")
	}
}

func TestPublishedNumberedOrder(t *testing.T) {
	lower, higher := &NumberedRank{1, "B"}, &NumberedRank{3, "D"}
	for _, tc := range []struct {
		rank string
		want error
	}{
		{"C", nil}, {"B", ErrPublishedOrder}, {"A", ErrPublishedOrder}, {"D", ErrPublishedOrder}, {"E", ErrPublishedOrder},
	} {
		if err := CheckPublishedRank(2, tc.rank, lower, higher); !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", tc.rank, err)
		}
	}
	// Conversion allocates the next sequence, even if internal rank was earlier.
	if err := CheckPublishedRank(4, "A", higher, nil); !errors.Is(err, ErrPublishedOrder) {
		t.Fatal(err)
	}
	if err := CheckPublishedRank(4, "E", higher, nil); err != nil {
		t.Fatal(err)
	}
	// Seeds in final (possibly renumbered) sequence order pass the rank policy.
	keys, err := SeedRanks(3)
	if err != nil {
		t.Fatal(err)
	}
	for i, seq := range []int{1, 2, 121} {
		var lo, hi *NumberedRank
		if i > 0 {
			lo = &NumberedRank{[]int{1, 2, 121}[i-1], keys[i-1]}
		}
		if i < 2 {
			hi = &NumberedRank{[]int{1, 2, 121}[i+1], keys[i+1]}
		}
		if err := CheckPublishedRank(seq, keys[i], lo, hi); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEntryDeadlineBoundary(t *testing.T) {
	deadline := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		kind   tenant.PrincipalKind
		write  bool
		state  string
		now    time.Time
		adding bool
		want   error
	}{
		{"agent before", tenant.Agent, true, "planned", deadline.Add(-time.Nanosecond), true, nil},
		{"agent exactly at", tenant.Agent, true, "planned", deadline, true, ErrEntryClosed},
		{"agent creator is irrelevant", tenant.Agent, true, "building", deadline.Add(time.Hour), true, ErrEntryClosed},
		{"person allowed", tenant.Person, true, "building", deadline, true, nil},
		{"person without write", tenant.Person, false, "planned", deadline, true, ErrEntryClosed},
		{"frozen person", tenant.Person, true, "frozen", deadline, true, ErrFrozen},
		{"frozen agent before", tenant.Agent, true, "frozen", deadline.Add(-time.Hour), true, ErrFrozen},
		{"rerank after", tenant.Agent, true, "building", deadline, false, nil},
		{"remove frozen", tenant.Agent, true, "frozen", deadline, false, nil},
		{"abandoned", tenant.Person, true, "abandoned", deadline, true, ErrReleaseClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckAdmission(tc.kind, tc.write, tc.state, &deadline, tc.now, tc.adding); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
	if err := CheckAdmission(tenant.Agent, true, "building", nil, deadline, true); err != nil {
		t.Fatal(err)
	}
}

func TestCountCapsAndCompletion(t *testing.T) {
	if err := CheckCounts(1000, 50, 4000); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		rows, releases, pending int
		want                    error
	}{
		{1001, 50, 4000, ErrReleaseCapacity}, {1000, 51, 4000, ErrOpenReleaseCapacity}, {1000, 50, 4001, ErrPendingCapacity},
	} {
		if err := CheckCounts(tc.rows, tc.releases, tc.pending); !errors.Is(err, tc.want) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	for _, state := range []string{"done", "accepted", "delivered"} {
		if !Completed(state) {
			t.Errorf("%s not completed", state)
		}
	}
	for _, state := range []string{"open", "cancelled", "canceled", "archived", "closed", "custom-complete", "Done"} {
		if Completed(state) {
			t.Errorf("%s completed", state)
		}
	}
}

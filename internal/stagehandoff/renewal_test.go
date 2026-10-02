// SPDX-License-Identifier: AGPL-3.0-only
package stagehandoff

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// The Journey API tests exercise the person decision. These tests exercise the
// committed authority boundary against real handoff writes and dependency seals.
func recordGateRenewal(t *testing.T, m *Module, p tenant.Principal, project, release, gate string) int64 {
	t.Helper()
	var revision int64
	err := db.InTenant(dbtest.Seed(t.Context()), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `UPDATE journey_projects SET revision=revision+1 WHERE project_node_id=$1::uuid RETURNING revision`, project).Scan(&revision); err != nil {
			return err
		}
		_, err := events.Append(t.Context(), tx, p, events.Change{Type: "journey." + gate + "_renewed", NodeID: &project, After: map[string]any{"revision": revision, "current_release_id": release}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestRenewalRequiresFreshPrepareDependency(t *testing.T) {
	m, p, project, release, bearer := fixture(t)
	ctx := t.Context()
	prepare := func(revision int64) Handoff {
		t.Helper()
		var h Handoff
		err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
			var err error
			h, err = m.create(ctx, tx, p, RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "prepare", ExpectedJourneyRevision: revision, IdempotencyKey: fmt.Sprint("prepare-", revision)}, "janus", []string{"authorization", "credential_handoff"}, "")
			if err != nil {
				return err
			}
			yes := true
			for _, e := range []EvidenceWrite{
				{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now(), AuthorityEpoch: h.AuthorityEpoch, Authorized: &yes},
				{Sequence: 2, Kind: "credential_handoff", Outcome: "satisfied", ObservedAt: time.Now(), AuthorityEpoch: h.AuthorityEpoch, CredentialReady: &yes},
			} {
				if _, err := m.appendEvidence(ctx, tx, p, bearer, h.ID, e); err != nil {
					return err
				}
			}
			_, err = m.close(ctx, tx, p, bearer, h.ID, ResultWrite{Outcome: "succeeded", TerminalSequence: 2, AuthorityEpoch: h.AuthorityEpoch, PrerequisiteSealSHA256: h.PrerequisiteSealSHA256})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	first := prepare(1)
	dependent := Handoff{ProjectNodeID: project, ReleaseNodeID: release, Operation: "deploy", PlanDigest: first.PlanDigest}
	check := func(wantID string, blocked bool) string {
		t.Helper()
		var seal string
		err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
			deps, s, err := dependencySet(ctx, tx, dependent, true)
			if blocked {
				if err == nil || !strings.Contains(err.Error(), "predates gate renewal") {
					t.Fatalf("old prerequisite accepted: deps=%+v err=%v", deps, err)
				}
				return nil
			}
			if err != nil {
				return err
			}
			if len(deps) != 2 || deps[0].id != wantID {
				t.Fatalf("wrong dependencies %+v", deps)
			}
			seal = s
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return seal
	}
	oldSeal := check(first.ID, false)
	// A renewal event naming a different release must not contaminate this one.
	err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		_, err := events.Append(ctx, tx, p, events.Change{Type: "journey.candidate_renewed", NodeID: &project, After: map[string]any{"revision": 99, "current_release_id": project}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if check(first.ID, false) != oldSeal {
		t.Fatal("other release changed dependency seal")
	}
	rev := recordGateRenewal(t, m, p, project, release, "candidate")
	check("", true)
	second := prepare(rev)
	if check(second.ID, false) == oldSeal {
		t.Fatal("fresh prepare reused old seal")
	}
	rev = recordGateRenewal(t, m, p, project, release, "deploy")
	check("", true)
	third := prepare(rev)
	check(third.ID, false)
	rev = recordGateRenewal(t, m, p, project, release, "permit")
	check("", true)
	fourth := prepare(rev)
	check(fourth.ID, false)
}

func TestAccessRenewalFencesInflightReporterEvidence(t *testing.T) {
	m, p, project, release, bearer := fixture(t)
	var h Handoff
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		h, err = m.create(t.Context(), tx, p, RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "prepare", ExpectedJourneyRevision: 1, IdempotencyKey: "before-permit-renewal"}, "janus", []string{"authorization", "credential_handoff"}, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	recordGateRenewal(t, m, p, project, release, "permit")
	mux := http.NewServeMux()
	m.Mount(mux)
	yes := true
	e := EvidenceWrite{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now(), AuthorityEpoch: h.AuthorityEpoch, Authorized: &yes}
	w := routedTestRequest(t, mux, p, bearer, "/api/stage-handoffs/"+h.ID+"/evidence", e)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "stale") {
		t.Fatalf("stale Access reporter: %d %s", w.Code, w.Body.String())
	}
}

func TestRenewalClosesInflightHandoffAndUnconsumedAdmission(t *testing.T) {
	w := newLaunchWorld(t)
	w.storeArtifact(t, w.art.DigestSHA256)
	w.postReadiness(t, w.plan, true, true, true, time.Now(), w.h.AuthorityEpoch)
	body, _ := json.Marshal(w.art)
	rec := launchHTTP(t, w, w.p, w.bearer, "/launch/admit", "11111111-1111-4111-8111-111111111111", string(body))
	if rec.Code != 200 {
		t.Fatalf("admission: %d %s", rec.Code, rec.Body.String())
	}
	var admission LaunchAdmission
	if err := json.Unmarshal(rec.Body.Bytes(), &admission); err != nil {
		t.Fatal(err)
	}
	recordGateRenewal(t, w.m, w.p, w.project, w.release, "candidate")
	mux := http.NewServeMux()
	w.m.Mount(mux)
	blocker := "policy_refused"
	for _, tc := range []struct {
		path string
		body any
	}{
		{"evidence", w.readiness(w.seq+1, w.plan, true, true, true, time.Now(), w.h.AuthorityEpoch)},
		{"result", ResultWrite{Outcome: "failed", TerminalSequence: w.seq, AuthorityEpoch: w.h.AuthorityEpoch, PrerequisiteSealSHA256: w.h.PrerequisiteSealSHA256, BlockerCode: &blocker}},
	} {
		r := routedTestRequest(t, mux, w.p, w.bearer, "/api/stage-handoffs/"+w.h.ID+"/"+tc.path, tc.body)
		if r.Code != 409 || !strings.Contains(r.Body.String(), "stale") {
			t.Fatalf("old %s: %d %s", tc.path, r.Code, r.Body.String())
		}
	}
	rec = launchHTTP(t, w, w.p, w.bearer, "/launch/consume", "22222222-2222-4222-8222-222222222222", `{"admission_id":"`+admission.ID+`"}`)
	if rec.Code != 409 {
		t.Fatalf("old admission consumed: %d %s", rec.Code, rec.Body.String())
	}
}

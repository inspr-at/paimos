// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/workorders"
)

type handoffFaultAPI struct {
	*claimFaultAPI
	s       *Supervisor
	t       *testing.T
	pickups []WorkerPickup
}

func (a *handoffFaultAPI) WorkOrder(context.Context, string) (WorkOrder, error) {
	return WorkOrder{NodeID: "order", Status: "ready", Revision: 1}, nil
}

func (a *handoffFaultAPI) ClaimHandoff(ctx context.Context, id, daemon, generation string, ids []string, pickup WorkerPickup) error {
	records := a.s.journal.Snapshot()
	if len(records) != 1 || records[0].WorkerPickup == nil || !reflect.DeepEqual(*records[0].WorkerPickup, pickup) || records[0].LaunchState != launchPrepared {
		a.t.Fatal("assignment pickup not journaled before network claim")
	}
	a.pickups = append(a.pickups, pickup)
	return a.claimFaultAPI.Claim(ctx, id, daemon, generation, ids)
}

func assignedClaimFixture(t *testing.T) (*Supervisor, *handoffFaultAPI, *verificationFakeAdapter) {
	t.Helper()
	s, base, adapter := claimFixture(t)
	digest, err := workorders.BriefDigest("Do work", "Criteria", []string{})
	if err != nil {
		t.Fatal(err)
	}
	base.run.Trace, err = json.Marshal(map[string]any{"worker_assignment": map[string]any{
		"run_id": base.run.ID, "ticket_revision": time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), "work_order_revision": 1, "brief_sha256": digest,
	}})
	if err != nil {
		t.Fatal(err)
	}
	base.server = base.run
	api := &handoffFaultAPI{claimFaultAPI: base, s: s, t: t}
	s.api = api
	return s, api, adapter
}

func TestWorkerAssignmentClaimResponseLossUsesJournalProof(t *testing.T) {
	s, api, adapter := assignedClaimFixture(t)
	api.claimErr, api.reportErr = errors.New("lost claim response"), errors.New("lost report response")
	api.claimCommitted = true
	if err := s.StartRun(t.Context(), api.run); err == nil {
		t.Fatal("response loss hidden")
	}
	if adapter.starts != 0 || len(api.pickups) != 1 || api.accepted[1].ProcessState != "not_attempted" {
		t.Fatal("uncertain claim launched or failed to retain no-launch evidence")
	}
	records := s.journal.Snapshot()
	if records[0].WorkerPickup == nil || strings.Contains(records[0].WorkerPickup.WorktreeID, s.workspace) || len(records[0].WorkerPickup.WorktreeID) != 64 {
		t.Fatal("pickup lost opaque worktree binding")
	}
	api.reportErr = nil
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if adapter.starts != 0 || len(api.accepted) != 1 || api.accepted[1].ProcessState != "not_attempted" {
		t.Fatal("reconciliation changed proof or launched a second process")
	}
}

type handoffLaunchError struct {
	fakeAdapter
	starts int
}

func (a *handoffLaunchError) Start(context.Context, StartRequest, func(AdapterEvent)) (Process, error) {
	a.starts++
	return nil, errors.New("unknown fork outcome")
}

func TestWorkerAssignmentUnknownForkNeverRetries(t *testing.T) {
	s, api, _ := assignedClaimFixture(t)
	adapter := &handoffLaunchError{}
	s.adapters[Codex] = adapter
	if err := s.StartRun(t.Context(), api.run); err == nil {
		t.Fatal("unknown fork hidden")
	}
	if err := s.StartRun(t.Context(), api.run); err != nil {
		t.Fatal(err)
	}
	records := s.journal.Snapshot()
	if adapter.starts != 1 || records[0].LaunchState != launchAttempted || records[0].ExitObserved || records[0].WorkerPickup == nil {
		t.Fatal("unknown launch was retried or declared stopped")
	}
	for _, report := range api.accepted {
		if report.ProcessState == "exited" || report.ProcessState == "not_attempted" {
			t.Fatal("unknown fork fabricated exit evidence")
		}
	}
}

func TestWorkerAssignmentChangedBriefCannotClaim(t *testing.T) {
	s, api, adapter := assignedClaimFixture(t)
	api.run.Trace = json.RawMessage(`{"worker_assignment":{"run_id":"run","ticket_revision":"2026-10-06T00:00:00Z","work_order_revision":1,"brief_sha256":"` + strings.Repeat("a", 64) + `"}}`)
	if err := s.StartRun(t.Context(), api.run); err == nil || !strings.Contains(err.Error(), "assignment brief changed") {
		t.Fatalf("changed brief: %v", err)
	}
	if api.routes != 0 || len(api.pickups) != 0 || adapter.starts != 0 || len(s.journal.Snapshot()) != 0 {
		t.Fatal("changed brief reserved or started work")
	}
}

func TestWorkerAssignmentRemoteClaimCarriesExactPickup(t *testing.T) {
	pickup := WorkerPickup{TicketRevision: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), WorkOrderRevision: 3, BriefSHA256: strings.Repeat("a", 64), WorktreeID: strings.Repeat("b", 64)}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/runs/run/claim" {
			t.Error("wrong claim route")
		}
		var body struct {
			Daemon       string       `json:"daemon_id"`
			Generation   string       `json:"daemon_generation"`
			Reservations []string     `json:"reservation_ids"`
			Handoff      WorkerPickup `json:"handoff"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Daemon != "daemon" || body.Generation != "generation" || !reflect.DeepEqual(body.Reservations, []string{"reservation"}) || !reflect.DeepEqual(body.Handoff, pickup) {
			t.Error("pickup identity changed over HTTP")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	remote := NewRemote(server.URL, "test-key")
	if err := remote.ClaimHandoff(t.Context(), "run", "daemon", "generation", []string{"reservation"}, pickup); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("claim repeated")
	}
}

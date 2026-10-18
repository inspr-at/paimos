// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: queue demand or an injected adapter bypasses person consent, replaces
// an adopted coordinator, or lets an old daemon bootstrap automatic execution.
func TestLeadRuntimeConsentAndAdoptionRemainLaunchBarriers(t *testing.T) {
	f := projectLeadFixture(t, readyLeadChecks)
	var calls atomic.Int32
	f.mux = http.NewServeMux()
	harness.NewWithLeadAdmission(f.db.App, readyLeadChecks).(*harness.Module).WithLeadRuntime(harness.LeadRuntimeChecks{
		Prepare: func(context.Context, pgx.Tx, tenant.Principal, agentsetup.LeadPickup) (agentsetup.LeadLaunch, error) {
			calls.Add(1)
			t.Error("unconsented launch reached preparer")
			return agentsetup.LeadLaunch{}, nil
		},
		Validate: func(context.Context, pgx.Tx, tenant.Principal, agentsetup.LeadLaunch) error { return nil },
		Exit: func(context.Context, pgx.Tx, tenant.Principal, agentsetup.LeadLaunch, ownedprocess.Identity) error {
			return nil
		},
	}).Mount(f.mux)
	path := "/api/projects/" + f.project + "/lead-runtime"
	read := func() map[string]any {
		t.Helper()
		w := f.call(f.agent, "GET", path, nil, "")
		expect(t, w, 200)
		return decode(t, w)
	}
	if out := read(); out["reason"] != "lead_consent_missing" || out["demand"] != nil {
		t.Fatal(out)
	}
	startLead(t, f, 0)
	if out := read(); out["state"] != "waiting" || out["demand"] != nil || out["reason"] == "" {
		t.Fatal("default-off admission was bypassed", out)
	}
	// Old daemons cannot use the new surface even with otherwise valid ids.
	pickup := agentsetup.LeadPickup{Demand: agentsetup.LeadDemand{ID: uid(), ProjectID: f.project, RoutineRunID: uid(), OwnerID: f.person.ID, Revision: 1}, AccountID: uid(), ComputerID: uid(), LedgerGeneration: uid(), DaemonID: "fixture", DaemonGeneration: "fixture", DaemonDigest: strings.Repeat("a", 64), CapabilityDigest: strings.Repeat("b", 64), WorktreeID: strings.Repeat("c", 64), Ref: strings.Repeat("d", 32), Lease: strings.Repeat("e", 32)}
	expect(t, f.call(f.agent, "POST", path+"/claim", pickup, ""), 400)
	session, lease, _ := leadCandidate(t, f)
	adopted := decode(t, f.call(f.person, "POST", "/api/projects/"+f.project+"/lead/adopt", map[string]any{"expected_revision": 1, "session_id": session}, ""))
	claimLead(t, f, session, lease, adopted["revision"])
	if out := read(); out["state"] != "reused" || out["reason"] != "existing_lead" || out["demand"] != nil {
		t.Fatal("adopted lead was replaced", out)
	}
	if calls.Load() != 0 {
		t.Fatal("automatic preparer was invoked")
	}
}

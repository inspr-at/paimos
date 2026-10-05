// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func TestLeadQueueDispatchAndWorkerGenerationFence(t *testing.T) {
	f := setup(t)
	f.queueAccount(t, 1000000)
	project, session := uuid(), uuid()
	lease := "fixture-lead-lease-private-0000000000000"
	leaseDigest := sha256.Sum256([]byte("aeon.harness.lease\x00" + lease))
	refDigest := sha256.Sum256([]byte("aeon.harness.ref\x00fixture-lead-reference"))
	coordinator := f.other
	coordinator.Scopes = authz.CoordinatorKeyScopes
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'lead_queue_test','Lead') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
			return err
		}
		for _, permission := range authz.CoordinatorBaseScopes {
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, f.person.TenantID, role, permission); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, coordinator.ID, role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'LQ-1',id,'Lead project' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,phase,heartbeat_at,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,'codex','local','unmanaged','coordinator','working',clock_timestamp(),$6,$7)`, f.person.TenantID, session, project, coordinator.ID, f.person.ID, refDigest[:], leaseDigest[:]); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state) VALUES($1,$2,$3,$4,1,'working')`, f.person.TenantID, project, f.person.ID, session)
		return err
	})
	id := f.ticket(t, "open", "high", nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, project)
		return err
	})
	e := f.addQueue(t, id, nil)
	mode, calls := "ready", 0
	admission := func(ctx context.Context, tx pgx.Tx, _ tenant.Principal, _ string, _ harness.Session) (harness.LeadChecks, error) {
		calls++
		var now time.Time
		err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
		g := harness.LeadGate{State: "available", CheckedAt: now}
		checks := harness.LeadChecks{Dial: g, Harness: g, Account: g, Host: g}
		if mode == "stale" {
			checks.Host.CheckedAt = now.Add(-time.Minute)
		}
		return checks, err
	}
	f.mux = http.NewServeMux()
	workorders.New(f.d.App).Mount(f.mux)
	agentruns.NewWithLeadAdmission(f.d.App, admission).Mount(f.mux)
	next := func(generation int, proof string, want int) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/queue/next", strings.NewReader(`{"agent_principal_id":"`+f.agent.ID+`","model_profile_id":"`+f.profile+`"}`)).WithContext(tenant.WithPrincipal(t.Context(), coordinator))
		r.Header.Set("X-Aeon-Lead-Session", session)
		r.Header.Set("X-Aeon-Lead-Generation", strconv.Itoa(generation))
		r.Header.Set("X-Aeon-Worker-Lease", proof)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("next: got %d want %d: %s", w.Code, want, w.Body.String())
		}
		return w
	}
	w := next(2, lease, 409)
	if !strings.Contains(w.Body.String(), "current working lead generation required") {
		t.Fatal("stale lead rejected for wrong reason")
	}
	next(1, "invalid-worker-proof-with-32-characters", 403)
	mode = "stale"
	w = next(1, lease, 409)
	if !strings.Contains(w.Body.String(), "host_unavailable") {
		t.Fatal("stale host check bypassed")
	}
	mode = "ready"
	next(1, lease, 200)
	var trace struct {
		Lead struct {
			Session    string `json:"session_id"`
			Generation int    `json:"generation"`
		} `json:"project_lead"`
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(t.Context(), `SELECT trace FROM agent_runs WHERE id=$1`, e.Run.ID).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &trace)
	})
	if trace.Lead.Session != session || trace.Lead.Generation != 1 {
		t.Fatal("queue route lost accepting lead generation")
	}
	next(1, lease, 200)
	if calls != 3 {
		t.Fatal("repeated queue selection cached admission checks")
	}
	ids := f.reserve(t, e.Run)
	mode = "stale"
	body, _ := json.Marshal(claimBody(ids))
	w = f.request(f.agent, "POST", "/api/runs/"+e.Run.ID+"/claim", string(body), f.token)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "worker admission wait: host_unavailable") {
		t.Fatal("worker start reused routing-time host check")
	}
	mode = "ready"
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE project_leads SET state='paused',revision=revision+1 WHERE project_id=$1`, project)
		return err
	})
	body, _ = json.Marshal(claimBody(ids))
	w = f.request(f.agent, "POST", "/api/runs/"+e.Run.ID+"/claim", string(body), f.token)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "assignment lead generation is no longer current") {
		t.Fatalf("paused lead worker claim: %d %s", w.Code, w.Body.String())
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE project_leads SET state='working',generation=2,revision=revision+1 WHERE project_id=$1`, project)
		return err
	})
	w = f.request(f.agent, "POST", "/api/runs/"+e.Run.ID+"/claim", string(body), f.token)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "assignment lead generation is no longer current") {
		t.Fatal("successor inherited an old accepted assignment")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE project_leads SET generation=1 WHERE project_id=$1`, project)
		return err
	})
	f.call(t, f.agent, "POST", "/api/runs/"+e.Run.ID+"/claim", claimBody(ids), 200, nil)
}

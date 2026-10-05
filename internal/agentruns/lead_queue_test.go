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
	"github.com/jackc/pgx/v5/pgxpool"
)

func leadQueueFixture(t *testing.T) (*fixture, tenant.Principal, string, string, string, qEntry) {
	t.Helper()
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
	return f, coordinator, project, session, lease, e
}

func TestLeadQueueDispatchAndWorkerGenerationFence(t *testing.T) {
	f, coordinator, project, session, lease, e := leadQueueFixture(t)
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

func TestLegacyQueueSkipsOtherProjectDispatchAuthorities(t *testing.T) {
	for _, state := range []string{"open", "blocked", "unready"} {
		t.Run(state, func(t *testing.T) {
			f := setup(t)
			f.queueAccount(t, 1000000)
			configured, legacy := uuid(), uuid()
			f.tx(t, f.person, func(tx pgx.Tx) error {
				for i, id := range []string{configured, legacy} {
					if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,$3,id,'Queue project' FROM node_kinds WHERE slug='project'`, f.person.TenantID, id, "MQ-"+strconv.Itoa(i+1)); err != nil {
						return err
					}
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,state) VALUES($1,$2,$3,'waiting_for_room')`, f.person.TenantID, configured, f.person.ID)
				return err
			})
			first := f.ticket(t, "open", "high", nil)
			later := f.ticket(t, "open", "low", nil)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, first, configured); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, later, legacy)
				return err
			})
			e := f.addQueue(t, first, nil)
			f.addQueue(t, later, nil)
			if got := keys(f.queuePage(t)); len(got) != 2 || got[0] != first || got[1] != later {
				t.Fatalf("fixture order=%v", got)
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				switch state {
				case "blocked":
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='blocked' WHERE id=$1`, first)
					return err
				case "unready":
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{}'::jsonb WHERE id=$1`, first)
					return err
				}
				return nil
			})
			var result struct {
				Entry *qEntry `json:"entry"`
			}
			f.call(t, f.person, "POST", "/api/queue/next", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 200, &result)
			if result.Entry == nil || result.Entry.NodeID != later || result.Entry.Run.QueueRoutedAt == nil {
				t.Fatalf("legacy pickup=%+v", result.Entry)
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var routed bool
				if err := tx.QueryRow(t.Context(), `SELECT queue_routed_at IS NOT NULL FROM agent_runs WHERE id=$1`, e.Run.ID).Scan(&routed); err != nil {
					return err
				}
				if routed {
					t.Fatal("legacy caller routed lead-controlled work")
				}
				return nil
			})
		})
	}
}

func TestLeadQueueFairRoutingUsesLiveSecurityRoute(t *testing.T) {
	for _, change := range []string{"field", "title", "body"} {
		t.Run(change, func(t *testing.T) {
			f, coordinator, olderProject, olderSession, olderLease, older := leadQueueFixture(t)
			project, session := uuid(), uuid()
			lease := "live-route-fixture-lease-0000000000000"
			refDigest := sha256.Sum256([]byte("aeon.harness.ref\x00live-route-reference"))
			leaseDigest := sha256.Sum256([]byte("aeon.harness.lease\x00" + lease))
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build',1,$2)`, f.person.TenantID, f.profile); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'LQ-2',id,'Younger project' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,phase,heartbeat_at,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,'codex','local','unmanaged','coordinator','working',clock_timestamp(),$6,$7)`, f.person.TenantID, session, project, coordinator.ID, f.person.ID, refDigest[:], leaseDigest[:]); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state) VALUES($1,$2,$3,$4,1,'working')`, f.person.TenantID, project, f.person.ID, session)
				return err
			})
			id := f.ticket(t, "open", "low", nil)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, project)
				return err
			})
			younger := f.addQueue(t, id, nil)
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_at=$2 WHERE id=$1`, older.Run.ID, at); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_at=$2 WHERE id=$1`, younger.Run.ID, at.Add(time.Minute)); err != nil {
					return err
				}
				var savedSecurity, hardRoute bool
				if err := tx.QueryRow(t.Context(), `SELECT coalesce(queue_security_review_required,false),EXISTS(SELECT 1 FROM model_role_routes WHERE role='build-hard') FROM agent_runs WHERE id=$1`, older.Run.ID).Scan(&savedSecurity, &hardRoute); err != nil {
					return err
				}
				if savedSecurity || hardRoute {
					t.Fatal("fixture must enqueue ordinary work with no build-hard route")
				}
				switch change {
				case "field":
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||'{"security_review_required":true}'::jsonb WHERE id=$1`, older.NodeID)
					return err
				case "title":
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Authorization changes' WHERE id=$1`, older.NodeID)
					return err
				default:
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET body='Review permissions' WHERE id=$1`, older.NodeID)
					return err
				}
			})
			admission := func(ctx context.Context, tx pgx.Tx, _ tenant.Principal, _ string, _ harness.Session) (harness.LeadChecks, error) {
				var now time.Time
				err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
				g := harness.LeadGate{State: "available", CheckedAt: now}
				return harness.LeadChecks{Dial: g, Harness: g, Account: g, Host: g}, err
			}
			f.mux = http.NewServeMux()
			workorders.New(f.d.App).Mount(f.mux)
			agentruns.NewWithLeadAdmission(f.d.App, admission).Mount(f.mux)
			next := func(session, lease, want, reason string) {
				t.Helper()
				// Ordinary placement must use the live ticket, with no pickup override.
				r := httptest.NewRequest("POST", "/api/queue/next", strings.NewReader(`{}`)).WithContext(tenant.WithPrincipal(t.Context(), coordinator))
				r.Header.Set("X-Aeon-Lead-Session", session)
				r.Header.Set("X-Aeon-Lead-Generation", "1")
				r.Header.Set("X-Aeon-Worker-Lease", lease)
				w := httptest.NewRecorder()
				f.mux.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("next=%d %s", w.Code, w.Body.String())
				}
				var out struct {
					Entry  *qEntry `json:"entry"`
					Reason string  `json:"wait_reason"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if want == "" && out.Entry != nil || want != "" && (out.Entry == nil || out.Entry.Run.ID != want || out.Entry.Run.QueueRoutedAt == nil) || out.Reason != reason {
					t.Fatalf("next=%s want run=%s reason=%s", w.Body.String(), want, reason)
				}
			}
			next(olderSession, olderLease, "", "")   // Current security route is unavailable.
			next(session, lease, younger.Run.ID, "") // Older work cannot monopolize turns.
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var unchanged bool
				if err := tx.QueryRow(t.Context(), `SELECT queue_routed_at IS NULL AND model_profile_id IS NULL AND agent_principal_id=tenant_id AND NOT coalesce(queue_security_review_required,false) AND (SELECT last_turn_at IS NULL FROM project_leads WHERE project_id=$2) FROM agent_runs WHERE id=$1`, older.Run.ID, olderProject).Scan(&unchanged); err != nil {
					return err
				}
				if !unchanged {
					t.Fatal("qualification changed the older assignment, saved security flag or turn")
				}
				return nil
			})
		})
	}
}

func TestLeadQueueFairRoutingAndWorkerRestartPriority(t *testing.T) {
	f := setup(t)
	f.queueAccount(t, 1000000)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build',1,$2)`, f.person.TenantID, f.profile)
		return err
	})

	unrouteableWorker := uuid()
	coordinator := f.other
	coordinator.Scopes = authz.CoordinatorKeyScopes
	coordinatorToken := f.key(t, coordinator, authz.CoordinatorKeyScopes)
	projects := []string{uuid(), uuid()}
	sessions := []string{uuid(), uuid()}
	leases := []string{"fair-a-fixture-lease-00000000000000", "fair-b-fixture-lease-00000000000000"}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','Unrouteable worker')`, f.person.TenantID, unrouteableWorker); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE key='member'`, f.person.TenantID, unrouteableWorker); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'fair_coordinator','Lead') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
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
		for i, project := range projects {
			if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,$3,id,'Fair project' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project, "FAIR-"+strconv.Itoa(i+1)); err != nil {
				return err
			}
			ref := sha256.Sum256([]byte("aeon.harness.ref\x00fair-ref-" + project))
			lease := sha256.Sum256([]byte("aeon.harness.lease\x00" + leases[i]))
			if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,phase,heartbeat_at,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,'codex','local','unmanaged','coordinator','working',clock_timestamp(),$6,$7)`, f.person.TenantID, sessions[i], project, coordinator.ID, f.person.ID, ref[:], lease[:]); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state) VALUES($1,$2,$3,$4,1,'working')`, f.person.TenantID, project, f.person.ID, sessions[i]); err != nil {
				return err
			}
		}
		return nil
	})
	tickets := []string{f.ticket(t, "open", "low", nil), f.ticket(t, "open", "high", nil), f.ticket(t, "open", "urgent", nil)}
	runs := []string{}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range tickets {
		project := projects[0]
		if i == 2 {
			project = projects[1]
		}
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, project)
			return err
		})
		e := f.addQueue(t, id, nil)
		runs = append(runs, e.Run.ID)
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_at=$2,queue_rank=$3 WHERE id=$1`, e.Run.ID, at.Add(time.Duration(i)*time.Minute), i+1)
			return err
		})
	}
	calls := 0
	admission := func(ctx context.Context, tx pgx.Tx, _ tenant.Principal, _ string, s harness.Session) (harness.LeadChecks, error) {
		calls++
		var now time.Time
		err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
		g := harness.LeadGate{State: "available", CheckedAt: now}
		return harness.LeadChecks{Dial: g, Harness: g, Account: g, Host: g}, err
	}
	f.mux = http.NewServeMux()
	workorders.New(f.d.App).Mount(f.mux)
	agentruns.NewWithLeadAdmission(f.d.App, admission).Mount(f.mux)
	harness.NewWithLeadAdmission(f.d.App, admission).Mount(f.mux)
	next := func(i int, want, reason string) {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/queue/next", strings.NewReader(`{"agent_principal_id":"`+f.agent.ID+`","model_profile_id":"`+f.profile+`"}`)).WithContext(tenant.WithPrincipal(t.Context(), coordinator))
		r.Header.Set("X-Aeon-Lead-Session", sessions[i])
		r.Header.Set("X-Aeon-Lead-Generation", "1")
		r.Header.Set("X-Aeon-Worker-Lease", leases[i])
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("next=%d %s", w.Code, w.Body.String())
		}
		var out struct {
			Entry  *qEntry `json:"entry"`
			Reason string  `json:"wait_reason"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if want == "" && out.Entry != nil || want != "" && (out.Entry == nil || out.Entry.NodeID != want) || out.Reason != reason {
			t.Fatalf("next=%s want=%s reason=%s", w.Body.String(), want, reason)
		}
	}
	// The older project's targeted workers have no account route, while its
	// coordinator admission remains fully available. It cannot own every turn.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_target_agent_id=$2,agent_principal_id=$2,model_profile_id=$3 WHERE id=ANY($1::uuid[])`, runs[:2], unrouteableWorker, f.profile)
		return err
	})
	next(1, tickets[2], "")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var unchanged bool
		err := tx.QueryRow(t.Context(), `SELECT bool_and(agent_principal_id=$2 AND queue_routed_at IS NULL) AND (SELECT last_turn_at IS NULL FROM project_leads WHERE project_id=$3) FROM agent_runs WHERE id=ANY($1::uuid[])`, runs[:2], unrouteableWorker, projects[0]).Scan(&unchanged)
		if err == nil && !unchanged {
			t.Error("fairness changed a competing assignment or turn")
		}
		return err
	})

	resetRoutingFixture := func() {
		f.tx(t, f.person, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields-'route_role' WHERE id=ANY($1::uuid[])`, tickets[:2]); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_target_agent_id=NULL,agent_principal_id=tenant_id,model_profile_id=NULL WHERE id=ANY($1::uuid[])`, runs[:2]); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_routed_at=NULL,agent_principal_id=tenant_id,model_profile_id=NULL,trace=trace-'project_lead' WHERE id=$1`, runs[2]); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `UPDATE project_leads SET last_turn_at=NULL WHERE project_id=$1`, projects[1])
			return err
		})
	}
	resetRoutingFixture()
	// Shared demand with no model route also yields to a ready project.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||'{"route_role":"scout"}'::jsonb WHERE id=ANY($1::uuid[])`, tickets[:2])
		return err
	})
	next(1, tickets[2], "")
	resetRoutingFixture()
	next(1, "", "project_turn_wait")
	next(0, tickets[0], "") // Preserve the manual order despite ticket B's urgency.
	var turn time.Time
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT last_turn_at FROM project_leads WHERE project_id=$1`, projects[0]).Scan(&turn)
	})
	next(0, tickets[0], "") // Idempotent receipt must not consume another turn.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var again time.Time
		if err := tx.QueryRow(t.Context(), `SELECT last_turn_at FROM project_leads WHERE project_id=$1`, projects[0]).Scan(&again); err != nil {
			return err
		}
		if !again.Equal(turn) {
			t.Fatal("idempotent route consumed a turn")
		}
		return nil
	})
	// Pending work wins over a newly requested lead, even in another project.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE project_leads SET session_id=NULL,generation=0,state='waiting_for_room' WHERE project_id=$1`, projects[1])
		return err
	})
	r := httptest.NewRequest("POST", "/api/projects/"+projects[1]+"/lead/claim", strings.NewReader(`{"expected_revision":1,"session_id":"`+sessions[1]+`"}`)).WithContext(tenant.WithPrincipal(t.Context(), coordinator))
	r.Header.Set("X-Aeon-Worker-Lease", leases[1])
	r.Header.Set("Authorization", "Bearer "+coordinatorToken)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reason":"worker_priority"`) {
		t.Fatalf("restart did not yield to worker: %d %s", w.Code, w.Body.String())
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='cancelled' WHERE id=$1`, runs[0]); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE project_leads SET session_id=$2,generation=1,state='working' WHERE project_id=$1`, projects[1], sessions[1])
		return err
	})
	next(0, "", "project_turn_wait")
	next(1, tickets[2], "")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='cancelled' WHERE id=$1`, runs[2])
		return err
	})
	next(0, tickets[1], "")
	if calls < 6 {
		t.Fatal("live admission was not repeated")
	}
}

// Reject oversized snapshots before fields are decoded or per-entry projections
// start. The tracer observes the real query's returned row count and field cap.
type leadPickupQueryKey struct{}

type leadPickupQueries struct {
	queries []string
	counts  []int64
}

func (b *leadPickupQueries) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "SELECT queue_node_id::text") {
		b.queries = append(b.queries, q.SQL)
		return context.WithValue(ctx, leadPickupQueryKey{}, true)
	}
	return ctx
}
func (b *leadPickupQueries) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryEndData) {
	if ctx.Value(leadPickupQueryKey{}) == true && q.CommandTag.Select() {
		b.counts = append(b.counts, q.CommandTag.RowsAffected())
	}
}
func TestLeadQueuePickupBoundsBeforeMaterialization(t *testing.T) {
	for _, mode := range []string{"rows", "fields"} {
		t.Run(mode, func(t *testing.T) {
			f, coordinator, project, session, lease, e := leadQueueFixture(t)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if mode == "fields" {
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||jsonb_build_object('large',repeat('x',65537)) WHERE id=$1`, e.NodeID)
					return err
				}
				_, err := tx.Exec(t.Context(), `WITH added AS (
     INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields)
     SELECT $1,'PB-'||i,k.id,'Bounded pickup',$2,'{"estimate_hours":1,"acceptance_criteria":["B"]}'::jsonb
     FROM generate_series(1,205) i CROSS JOIN node_kinds k WHERE k.slug='work' RETURNING id
    ) INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,queue_node_id,queue_by_principal_id,queue_at,queue_security_review_required,trace)
    SELECT r.tenant_id,r.work_order_id,r.agent_principal_id,added.id,r.queue_by_principal_id,r.queue_at,false,'{}'::jsonb
    FROM added CROSS JOIN agent_runs r WHERE r.id=$3`, f.person.TenantID, project, e.Run.ID)
				return err
			})
			observer := &leadPickupQueries{}
			cfg := f.d.App.Config()
			cfg.ConnConfig.Tracer = observer
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			f.mux = http.NewServeMux()
			agentruns.NewWithLeadAdmission(pool, func(ctx context.Context, tx pgx.Tx, _ tenant.Principal, _ string, _ harness.Session) (harness.LeadChecks, error) {
				var now time.Time
				err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
				g := harness.LeadGate{State: "available", CheckedAt: now}
				return harness.LeadChecks{Dial: g, Harness: g, Account: g, Host: g}, err
			}).Mount(f.mux)
			r := httptest.NewRequest("POST", "/api/queue/next", strings.NewReader(`{"agent_principal_id":"`+f.agent.ID+`","model_profile_id":"`+f.profile+`"}`)).WithContext(tenant.WithPrincipal(t.Context(), coordinator))
			r.Header.Set("X-Aeon-Lead-Session", session)
			r.Header.Set("X-Aeon-Lead-Generation", "1")
			r.Header.Set("X-Aeon-Worker-Lease", lease)
			w := httptest.NewRecorder()
			f.mux.ServeHTTP(w, r)
			want := "lead scheduling snapshot exceeds bound"
			if mode == "fields" {
				want = "lead scheduling fields exceed bound"
			}
			if w.Code != 409 || !strings.Contains(w.Body.String(), want) {
				t.Fatalf("bound response=%d %s", w.Code, w.Body.String())
			}
			if len(observer.queries) != 1 || !strings.Contains(observer.queries[0], "LIMIT 201") || !strings.Contains(observer.queries[0], "octet_length") {
				t.Fatalf("pickup loaded an unbounded query: %v", observer.queries)
			}
			expectedRows := int64(201)
			if mode == "fields" {
				expectedRows = 1
			}
			if len(observer.counts) != 1 || observer.counts[0] != expectedRows {
				t.Fatalf("pickup query row sample=%v want=%d", observer.counts, expectedRows)
			}

			f.tx(t, f.person, func(tx pgx.Tx) error {
				var routed bool
				err := tx.QueryRow(t.Context(), `SELECT queue_routed_at IS NOT NULL FROM agent_runs WHERE id=$1`, e.Run.ID).Scan(&routed)
				if routed {
					t.Error("overflow routed work")
				}
				return err
			})
		})
	}
}

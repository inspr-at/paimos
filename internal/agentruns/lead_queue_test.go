// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"bytes"
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
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// bindLeadKey records token's key row as the project lead's dispatch
// credential, as a claim or proven pickup through that key would. Tokens are
// aeon_<prefix>_<secret>; only the prefix identifies the row.
func bindLeadKey(t *testing.T, f *fixture, project, token string) string {
	t.Helper()
	parts := strings.Split(token, "_")
	if len(parts) != 3 {
		t.Fatalf("unexpected key token shape")
	}
	var id string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `UPDATE project_leads SET dispatch_key_id=(SELECT id FROM agent_keys WHERE prefix=$2) WHERE project_id=$1 RETURNING dispatch_key_id::text`, project, parts[1]).Scan(&id)
	})
	return id
}

func leadQueueFixture(t *testing.T) (*fixture, tenant.Principal, string, string, string, qEntry) {
	t.Helper()
	f := setup(t)
	f.agent.Scopes = []string{"run.claim", "run.telemetry", "run.read"}
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
	var h agentruns.WorkerHandoff
	f.call(t, f.person, "GET", "/api/runs/"+e.Run.ID+"/handoff", nil, 200, &h)
	pickup := claimBody(ids)
	pickup["handoff"] = agentruns.WorkerPickup{TicketRevision: h.TicketRevision, WorkOrderRevision: h.WorkOrderRevision, BriefSHA256: h.BriefSHA256, WorktreeID: strings.Repeat("a", 64)}
	f.call(t, f.agent, "POST", "/api/runs/"+e.Run.ID+"/claim", pickup, 200, nil)
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
			coordinatorToken := f.key(t, coordinator, authz.CoordinatorKeyScopes)
			bindLeadKey(t, f, olderProject, coordinatorToken)
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
			bindLeadKey(t, f, project, coordinatorToken)
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

// A competing lead whose agent lost its live queue-coordinator grants is
// denied at actual pickup, so it must not hold the turn against ready work.
func TestLeadQueueFairRoutingSkipsLeadWithoutDispatchAuthority(t *testing.T) {
	f, older, olderProject, olderSession, olderLease, olderWork := leadQueueFixture(t)
	younger := tenant.Principal{ID: uuid(), TenantID: f.person.TenantID, Kind: tenant.Agent, Scopes: authz.CoordinatorKeyScopes}
	project, session := uuid(), uuid()
	lease := "dispatch-authority-lease-00000000000000"
	bindLeadKey(t, f, olderProject, f.key(t, older, authz.CoordinatorKeyScopes))
	refDigest := sha256.Sum256([]byte("aeon.harness.ref\x00dispatch-authority-reference"))
	leaseDigest := sha256.Sum256([]byte("aeon.harness.lease\x00" + lease))
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build',1,$2)`, f.person.TenantID, f.profile); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','Younger lead')`, f.person.TenantID, younger.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE key='lead_queue_test'`, f.person.TenantID, younger.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'LQ-3',id,'Younger project' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,phase,heartbeat_at,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,'codex','local','unmanaged','coordinator','working',clock_timestamp(),$6,$7)`, f.person.TenantID, session, project, younger.ID, f.person.ID, refDigest[:], leaseDigest[:]); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state) VALUES($1,$2,$3,$4,1,'working')`, f.person.TenantID, project, f.person.ID, session)
		return err
	})
	bindLeadKey(t, f, project, f.key(t, younger, authz.CoordinatorKeyScopes))
	id := f.ticket(t, "open", "low", nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, project)
		return err
	})
	youngerWork := f.addQueue(t, id, nil)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_at=$2 WHERE id=$1`, olderWork.Run.ID, at); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_at=$2 WHERE id=$1`, youngerWork.Run.ID, at.Add(time.Minute))
		return err
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
	next := func(lead tenant.Principal, session, lease string, code int) (string, string) {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/queue/next", strings.NewReader(`{}`)).WithContext(tenant.WithPrincipal(t.Context(), lead))
		r.Header.Set("X-Aeon-Lead-Session", session)
		r.Header.Set("X-Aeon-Lead-Generation", "1")
		r.Header.Set("X-Aeon-Worker-Lease", lease)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if w.Code != code {
			t.Fatalf("next=%d want %d: %s", w.Code, code, w.Body.String())
		}
		if code != 200 {
			return "", w.Body.String()
		}
		var out struct {
			Entry  *qEntry `json:"entry"`
			Reason string  `json:"wait_reason"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Entry == nil {
			return "", out.Reason
		}
		if out.Entry.Run.QueueRoutedAt == nil {
			t.Fatalf("next returned an unrouted entry: %s", w.Body.String())
		}
		return out.Entry.Run.ID, out.Reason
	}
	olderUnchanged := func() {
		t.Helper()
		f.tx(t, f.person, func(tx pgx.Tx) error {
			var unchanged bool
			if err := tx.QueryRow(t.Context(), `SELECT queue_routed_at IS NULL AND model_profile_id IS NULL AND agent_principal_id=tenant_id AND (SELECT last_turn_at IS NULL FROM project_leads WHERE project_id=$2) FROM agent_runs WHERE id=$1`, olderWork.Run.ID, olderProject).Scan(&unchanged); err != nil {
				return err
			}
			if !unchanged {
				t.Fatal("fairness changed the older assignment or turn")
			}
			return nil
		})
	}
	// Both leads can dispatch: the older ready work holds the turn.
	if run, reason := next(younger, session, lease, 200); run != "" || reason != "project_turn_wait" {
		t.Fatalf("younger turn with an authorized older lead: run=%q reason=%q", run, reason)
	}
	olderUnchanged()
	// Revoke only the older lead agent's grants. Its owner, reporting and the
	// worker route for its ticket stay valid, so only pickup authority changes.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, older.ID)
		return err
	})
	if _, body := next(older, olderSession, olderLease, 403); !strings.Contains(body, "forbidden") {
		t.Fatalf("older pickup denied for the wrong reason: %s", body)
	}
	olderUnchanged()
	if run, reason := next(younger, session, lease, 200); run != youngerWork.Run.ID || reason != "" {
		t.Fatalf("forbidden older lead still held the turn: run=%q reason=%q", run, reason)
	}
	olderUnchanged()
}

// Fairness qualifies a contender through the key it actually dispatches with.
// Claiming and reporting need only harness.worker; a lead whose live keys are
// below the coordinator ceiling, bound to a creator without run.create, or
// revoked is refused at pickup by production authentication and must not hold
// a turn against a ready competitor.
func TestLeadQueueFairRoutingThroughProductionAuthentication(t *testing.T) {
	f, scoped, scopedProject, scopedSession, scopedLease, scopedWork := leadQueueFixture(t)
	// The oldest lead reports with a key that lacks the coordinator ceiling.
	scopedToken := f.key(t, scoped, []string{"harness.read", "harness.write", "harness.worker", "nodes.read"})
	bindLeadKey(t, f, scopedProject, scopedToken)
	var creatorRole string
	creator := tenant.Principal{ID: uuid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build',1,$2)`, f.person.TenantID, f.profile); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Key creator')`, f.person.TenantID, creator.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'lead_key_creator','Key creator') RETURNING id::text`, f.person.TenantID).Scan(&creatorRole); err != nil {
			return err
		}
		// The creator keeps every reporting permission a lead needs, but not run.create.
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, f.person.TenantID, creatorRole, authz.CoordinatorBaseScopes); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE key='admin' AND tenant_id=$1`, f.person.TenantID, creator.ID)
		return err
	})
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_at=$2 WHERE id=$1`, scopedWork.Run.ID, at)
		return err
	})
	type contender struct {
		agent                   tenant.Principal
		project, session, lease string
		run                     string
		token                   string
	}
	lead := func(name, key string, minute int, keyCreator string) contender {
		t.Helper()
		c := contender{agent: tenant.Principal{ID: uuid(), TenantID: f.person.TenantID, Kind: tenant.Agent, Scopes: authz.CoordinatorKeyScopes}, project: uuid(), session: uuid(), lease: "prod-auth-" + key + "-lease-000000000000000000"}
		refDigest := sha256.Sum256([]byte("aeon.harness.ref\x00prod-auth-" + key))
		leaseDigest := sha256.Sum256([]byte("aeon.harness.lease\x00" + c.lease))
		f.tx(t, f.person, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent',$3)`, f.person.TenantID, c.agent.ID, name); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE key='lead_queue_test'`, f.person.TenantID, c.agent.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,$3,id,$4 FROM node_kinds WHERE slug='project'`, f.person.TenantID, c.project, "LQ-"+strconv.Itoa(3+minute), name); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,phase,heartbeat_at,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,'codex','local','unmanaged','coordinator','working',clock_timestamp(),$6,$7)`, f.person.TenantID, c.session, c.project, c.agent.ID, f.person.ID, refDigest[:], leaseDigest[:]); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state) VALUES($1,$2,$3,$4,1,'working')`, f.person.TenantID, c.project, f.person.ID, c.session)
			return err
		})
		c.token = f.key(t, c.agent, authz.CoordinatorKeyScopes)
		bindLeadKey(t, f, c.project, c.token)
		if keyCreator != "" {
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET created_by_principal_id=$2 WHERE principal_id=$1`, c.agent.ID, keyCreator)
				return err
			})
		}
		id := f.ticket(t, "open", "low", nil)
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, c.project)
			return err
		})
		c.run = f.addQueue(t, id, nil).Run.ID
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_at=$2 WHERE id=$1`, c.run, at.Add(time.Duration(minute)*time.Minute))
			return err
		})
		return c
	}
	bound := lead("Creator-bound lead", "creator", 1, creator.ID)
	revocable := lead("Revocable lead", "revocable", 2, "")
	youngest := lead("Youngest lead", "youngest", 3, "")
	admission := func(ctx context.Context, tx pgx.Tx, _ tenant.Principal, _ string, _ harness.Session) (harness.LeadChecks, error) {
		var now time.Time
		err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
		g := harness.LeadGate{State: "available", CheckedAt: now}
		return harness.LeadChecks{Dial: g, Harness: g, Account: g, Host: g}, err
	}
	f.mux = http.NewServeMux()
	workorders.New(f.d.App).Mount(f.mux)
	agentruns.NewWithLeadAdmission(f.d.App, admission).Mount(f.mux)
	harness.NewWithLeadAdmission(f.d.App, admission).Mount(f.mux)
	authn, err := auth.New(auth.Config{Env: "prod", SessionKey: bytes.Repeat([]byte{7}, 32)}, f.d.App)
	if err != nil {
		t.Fatal(err)
	}
	secured := authn.Middleware(f.mux)
	call := func(token, method, path string, body any, session, lease string, want int) string {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+token)
		if session != "" {
			r.Header.Set("X-Aeon-Lead-Session", session)
			r.Header.Set("X-Aeon-Lead-Generation", "1")
		}
		r.Header.Set("X-Aeon-Worker-Lease", lease)
		_, r.Pattern = f.mux.Handler(r)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	next := func(c contender, code int) (string, string) {
		t.Helper()
		body := call(c.token, "POST", "/api/queue/next", map[string]any{}, c.session, c.lease, code)
		if code != 200 {
			return "", body
		}
		var out struct {
			Entry  *qEntry `json:"entry"`
			Reason string  `json:"wait_reason"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		if out.Entry == nil {
			return "", out.Reason
		}
		if out.Entry.Run.QueueRoutedAt == nil {
			t.Fatalf("next returned an unrouted entry: %s", body)
		}
		return out.Entry.Run.ID, out.Reason
	}
	scopedLead := contender{agent: scoped, project: scopedProject, session: scopedSession, lease: scopedLease, run: scopedWork.Run.ID, token: scopedToken}
	heartbeat := map[string]any{"phase": "working", "activity": "idle", "activity_sequence": 1}
	unchanged := func(leads ...contender) {
		t.Helper()
		for _, c := range leads {
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var ok bool
				if err := tx.QueryRow(t.Context(), `SELECT queue_routed_at IS NULL AND model_profile_id IS NULL AND agent_principal_id=tenant_id AND (SELECT last_turn_at IS NULL FROM project_leads WHERE project_id=$2) FROM agent_runs WHERE id=$1`, c.run, c.project).Scan(&ok); err != nil {
					return err
				}
				if !ok {
					t.Fatalf("fairness changed the assignment or turn of %s", c.project)
				}
				return nil
			})
		}
	}
	// The scoped lead is a live reporting lead whose key cannot dispatch.
	call(scopedToken, "POST", "/api/projects/"+scopedProject+"/harness-sessions/"+scopedSession+"/heartbeat", heartbeat, "", scopedLease, 200)
	if _, body := next(scopedLead, 403); !strings.Contains(body, "forbidden") {
		t.Fatalf("scoped lead pickup denied for the wrong reason: %s", body)
	}
	// With the creator still an admin, the creator-bound lead holds the turn.
	if run, reason := next(youngest, 200); run != "" || reason != "project_turn_wait" {
		t.Fatalf("youngest turn with an authorized older lead: run=%q reason=%q", run, reason)
	}
	unchanged(scopedLead, bound, revocable, youngest)
	// The creator loses run.create; the key keeps its ceiling and still reports.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, creator.ID, creatorRole)
		return err
	})
	call(bound.token, "POST", "/api/projects/"+bound.project+"/harness-sessions/"+bound.session+"/heartbeat", heartbeat, "", bound.lease, 200)
	if _, body := next(bound, 403); !strings.Contains(body, "forbidden") {
		t.Fatalf("creator-bound lead pickup denied for the wrong reason: %s", body)
	}
	if run, reason := next(youngest, 200); run != "" || reason != "project_turn_wait" {
		t.Fatalf("youngest turn with a revocable older lead: run=%q reason=%q", run, reason)
	}
	unchanged(scopedLead, bound, revocable, youngest)
	// A revoked key no longer authenticates at all.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE principal_id=$1`, revocable.agent.ID)
		return err
	})
	next(revocable, 401)
	if run, reason := next(youngest, 200); run != youngest.run || reason != "" {
		t.Fatalf("ineligible older leads still held the turn: run=%q reason=%q", run, reason)
	}
	unchanged(scopedLead, bound, revocable)
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
	for _, project := range projects {
		bindLeadKey(t, f, project, coordinatorToken)
	}
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

// A lead's dispatch authority is the key bound to its generation, not the
// strongest key its principal owns. A lead that claims and reports with a
// restricted key holds no turn while a stronger unused key exists; claiming
// again with the stronger key binds it; revoking the bound key ends the turn
// even though the restricted key keeps reporting; a proven pickup through
// another live key rebinds the generation to that key.
func TestLeadQueueFairRoutingBindsLeadDispatchCredential(t *testing.T) {
	f, older, olderProject, olderSession, olderLease, olderWork := leadQueueFixture(t)
	restricted := f.key(t, older, []string{"harness.read", "harness.write", "harness.worker", "nodes.read"})
	strong := f.key(t, older, authz.CoordinatorKeyScopes)
	younger := tenant.Principal{ID: uuid(), TenantID: f.person.TenantID, Kind: tenant.Agent, Scopes: authz.CoordinatorKeyScopes}
	project, session := uuid(), uuid()
	lease := "bound-credential-lease-000000000000000"
	refDigest := sha256.Sum256([]byte("aeon.harness.ref\x00bound-credential-reference"))
	leaseDigest := sha256.Sum256([]byte("aeon.harness.lease\x00" + lease))
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build',1,$2)`, f.person.TenantID, f.profile); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','Younger lead')`, f.person.TenantID, younger.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE key='lead_queue_test'`, f.person.TenantID, younger.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'LQ-4',id,'Younger project' FROM node_kinds WHERE slug='project'`, f.person.TenantID, project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,phase,heartbeat_at,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,'codex','local','unmanaged','coordinator','working',clock_timestamp(),$6,$7)`, f.person.TenantID, session, project, younger.ID, f.person.ID, refDigest[:], leaseDigest[:]); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state) VALUES($1,$2,$3,$4,1,'working')`, f.person.TenantID, project, f.person.ID, session)
		return err
	})
	youngerToken := f.key(t, younger, authz.CoordinatorKeyScopes)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	youngerRuns := []string{}
	for i := 1; i <= 2; i++ {
		id := f.ticket(t, "open", "low", nil)
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, project)
			return err
		})
		run := f.addQueue(t, id, nil).Run.ID
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_at=$2 WHERE id=$1`, run, at.Add(time.Duration(i)*time.Minute))
			return err
		})
		youngerRuns = append(youngerRuns, run)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET queue_at=$2 WHERE id=$1`, olderWork.Run.ID, at)
		return err
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
	harness.NewWithLeadAdmission(f.d.App, admission).Mount(f.mux)
	authn, err := auth.New(auth.Config{Env: "prod", SessionKey: bytes.Repeat([]byte{7}, 32)}, f.d.App)
	if err != nil {
		t.Fatal(err)
	}
	secured := authn.Middleware(f.mux)
	call := func(token, method, path string, body any, session, lease string, want int) string {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+token)
		if session != "" {
			r.Header.Set("X-Aeon-Lead-Session", session)
			r.Header.Set("X-Aeon-Lead-Generation", "1")
		}
		r.Header.Set("X-Aeon-Worker-Lease", lease)
		_, r.Pattern = f.mux.Handler(r)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	next := func(token, session, lease string, code int) (string, string) {
		t.Helper()
		body := call(token, "POST", "/api/queue/next", map[string]any{}, session, lease, code)
		if code != 200 {
			return "", body
		}
		var out struct {
			Entry  *qEntry `json:"entry"`
			Reason string  `json:"wait_reason"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		if out.Entry == nil {
			return "", out.Reason
		}
		if out.Entry.Run.QueueRoutedAt == nil {
			t.Fatalf("next returned an unrouted entry: %s", body)
		}
		return out.Entry.Run.ID, out.Reason
	}
	claim := func(token string, revision int64) int64 {
		t.Helper()
		body := call(token, "POST", "/api/projects/"+olderProject+"/lead/claim", map[string]any{"expected_revision": revision, "session_id": olderSession}, "", olderLease, 200)
		var out struct {
			State    string `json:"state"`
			Revision int64  `json:"revision"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		if out.State != "working" {
			t.Fatalf("claim left the older lead %s: %s", out.State, body)
		}
		return out.Revision
	}
	olderUnchanged := func() {
		t.Helper()
		f.tx(t, f.person, func(tx pgx.Tx) error {
			var unchanged bool
			if err := tx.QueryRow(t.Context(), `SELECT queue_routed_at IS NULL AND model_profile_id IS NULL AND agent_principal_id=tenant_id AND (SELECT last_turn_at IS NULL FROM project_leads WHERE project_id=$2) FROM agent_runs WHERE id=$1`, olderWork.Run.ID, olderProject).Scan(&unchanged); err != nil {
				return err
			}
			if !unchanged {
				t.Fatal("fairness changed the older assignment or turn")
			}
			return nil
		})
	}
	heartbeat := map[string]any{"phase": "working", "activity": "idle", "activity_sequence": 1}
	// A routed assignment occupies its worker until the daemon starts it; the
	// shared worker route stays free for the next contender once it has.
	started := func(run string) {
		t.Helper()
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='running' WHERE id=$1`, run)
			return err
		})
	}
	// The older lead claims and reports with its restricted key. Production
	// refuses that key at pickup, so the lead holds no turn: the stronger key
	// of the same principal is not what this generation dispatches with.
	revision := claim(restricted, 1)
	call(restricted, "POST", "/api/projects/"+olderProject+"/harness-sessions/"+olderSession+"/heartbeat", heartbeat, "", olderLease, 200)
	if _, body := next(restricted, olderSession, olderLease, 403); !strings.Contains(body, "forbidden") {
		t.Fatalf("restricted pickup denied for the wrong reason: %s", body)
	}
	if run, reason := next(youngerToken, session, lease, 200); run != youngerRuns[0] || reason != "" {
		t.Fatalf("restricted lead held the turn through an unused stronger key: run=%q reason=%q", run, reason)
	}
	olderUnchanged()
	started(youngerRuns[0])
	// Claiming again with the stronger key binds it; the older work now holds the turn.
	revision = claim(strong, revision)
	if run, reason := next(youngerToken, session, lease, 200); run != "" || reason != "project_turn_wait" {
		t.Fatalf("younger turn against a lead bound to a live dispatch key: run=%q reason=%q", run, reason)
	}
	olderUnchanged()
	// Revoking the bound key ends the turn, although the restricted key still reports.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE prefix=$1`, strings.Split(strong, "_")[1])
		return err
	})
	next(strong, olderSession, olderLease, 401)
	call(restricted, "POST", "/api/projects/"+olderProject+"/harness-sessions/"+olderSession+"/heartbeat", heartbeat, "", olderLease, 200)
	if run, reason := next(youngerToken, session, lease, 200); run != youngerRuns[1] || reason != "" {
		t.Fatalf("lead bound to a revoked key still held the turn: run=%q reason=%q", run, reason)
	}
	olderUnchanged()
	started(youngerRuns[1])
	// A proven pickup through another live key of the lead rebinds the generation to it.
	rotated := f.key(t, older, authz.CoordinatorKeyScopes)
	if run, reason := next(rotated, olderSession, olderLease, 200); run != olderWork.Run.ID || reason != "" {
		t.Fatalf("rotated key pickup: run=%q reason=%q", run, reason)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var bound bool
		if err := tx.QueryRow(t.Context(), `SELECT l.dispatch_key_id=k.id FROM project_leads l JOIN agent_keys k ON k.prefix=$2 WHERE l.project_id=$1`, olderProject, strings.Split(rotated, "_")[1]).Scan(&bound); err != nil {
			return err
		}
		if !bound {
			t.Fatal("proven pickup did not rebind the generation to its dispatching key")
		}
		return nil
	})
	if _, body := next(restricted, olderSession, olderLease, 403); !strings.Contains(body, "forbidden") {
		t.Fatalf("rebinding widened the restricted key: %s", body)
	}
	_ = revision
}

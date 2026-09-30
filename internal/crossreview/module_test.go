// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

func testID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type fixture struct {
	d                               *dbtest.DB
	m                               *Module
	mux                             *http.ServeMux
	person, agent, other, foreign   tenant.Principal
	ticket, profile, account, token string
}

func (f *fixture) tx(t *testing.T, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{d: dbtest.Open(t), mux: http.NewServeMux()}
	tid := testID()
	f.person = tenant.Principal{ID: testID(), TenantID: tid, Kind: tenant.Person}
	f.agent = tenant.Principal{ID: testID(), TenantID: tid, Kind: tenant.Agent, Scopes: []string{"work_orders.read", "work_orders.write", "run.create", "run.read", "run.telemetry", "run.claim"}}
	f.other = tenant.Principal{ID: testID(), TenantID: tid, Kind: tenant.Agent}
	f.foreign = tenant.Principal{ID: testID(), TenantID: testID(), Kind: tenant.Person}
	for _, p := range []tenant.Principal{f.person, f.foreign} {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.Admin, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'Review tests')`, p.TenantID, "review-"+p.TenantID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []tenant.Principal{f.person, f.agent, f.other, f.foreign} {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,$3,'Review fixture')`, p.TenantID, p.ID, p.Kind)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, f.d, p.TenantID, p.ID, "admin")
	}
	f.profile = testID()
	f.account = testID()
	f.tx(t, func(tx pgx.Tx) error {
		for i, row := range []struct{ id, harness, family string }{{testID(), "codex", "openai"}, {testID(), "cursor", "xai"}, {f.profile, "claude", "anthropic"}} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,$3,'test-version',$4,$5,'review-model','xhigh','frontier')`, tid, row.id, "review-"+row.id, row.harness, row.family); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id,state) VALUES($1,'review-gate',$2,$3,'available')`, tid, i+1, row.id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner) VALUES($1,$2,'review-account','claude','review-daemon',$3,'Review account',clock_timestamp(),true,'review-generation',$4)`, tid, f.account, f.agent.ID, f.person.ID); err != nil {
			return err
		}
		s := capacity.DefaultSchedule()
		for i := range s.Week {
			s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
		}
		s.Reserve = capacity.ReserveOff
		s.Override = "sprint"
		raw, _ := json.Marshal(s)
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3,$4,$5)`, tid, f.person.ID, f.account, f.account, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance) VALUES($1,$2,now()-interval '1 hour',now()+interval '1 hour','tokens',10000000)`, tid, f.account); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,body,fields) SELECT $1,'REVIEW-1',id,'Guard tenant boundary','Every mutation checks tenant isolation.', '{"acceptance_criteria":"Cross-tenant tests pass"}' FROM node_kinds WHERE slug='ticket' RETURNING id::text`, tid).Scan(&f.ticket)
	})
	prefix, secret := strings.ReplaceAll(testID(), "-", ""), testID()
	sum := sha256.Sum256([]byte(secret))
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1,$2,'review fixture',$3,$4,$5)`, tid, f.agent.ID, prefix, hex.EncodeToString(sum[:]), f.agent.Scopes)
		return err
	})
	f.token = "aeon_" + prefix + "_" + secret
	f.m = New(f.d.App, nil)
	f.m.Mount(f.mux)
	workorders.New(f.d.App).Mount(f.mux)
	agentruns.NewWithReviews(f.d.App, nil, f.m.RequestForRun).Mount(f.mux)
	return f
}
func (f *fixture) call(t *testing.T, p tenant.Principal, method, path string, body any, want int, dst any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	if p.ID == f.agent.ID {
		r.Header.Set("Authorization", "Bearer "+f.token)
	}
	r.Header.Set(reviewgate.PolicyHeader, reviewgate.Policy)
	r.Header.Set(agentruns.DaemonHeader, "review-daemon")
	r.Header.Set(agentruns.GenerationHeader, "review-generation")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s returned %d, wanted %d: %s", method, path, w.Code, want, w.Body.String())
	}
	if dst != nil {
		if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
			t.Fatal(err)
		}
	}
}
func (f *fixture) input() CreateInput {
	return CreateInput{RequestID: testID(), Repository: "example/review-fixture", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), AuthorFamily: "openai"}
}

func TestReviewFlowIsolationReplayAndVerdicts(t *testing.T) {
	f := newFixture(t)
	path := "/api/nodes/" + f.ticket + "/reviews"
	in := f.input()
	var v Review
	f.call(t, f.person, "POST", path, in, 201, &v)
	if v.Status != "queued" || v.ReviewerFamily == nil || *v.ReviewerFamily != "anthropic" || v.GateOpen || v.RunID == nil {
		t.Fatalf("invalid queued review: %+v", v)
	}
	if len(v.Ladder) != 3 || !strings.Contains(strings.Join(v.Ladder[0].SkipReasons, ","), "author family") || len(v.Ladder[1].SkipReasons) == 0 {
		t.Fatal("fallback reasons were lost")
	}
	var replay Review
	f.call(t, f.person, "POST", path, in, 201, &replay)
	if replay.OrderID != v.OrderID {
		t.Fatal("replay started another review")
	}
	// Replaying a person's external request cannot bypass the agent author check.
	f.call(t, f.agent, "POST", path, in, 403, nil)
	different := in
	different.HeadSHA = strings.Repeat("c", 40)
	f.call(t, f.person, "POST", path, different, 409, nil)
	f.call(t, f.foreign, "GET", path, nil, 404, nil)
	f.call(t, f.foreign, "POST", path, in, 404, nil)
	var run agentruns.Run
	f.call(t, f.person, "GET", "/api/runs/"+*v.RunID, nil, 200, &run)
	if !run.ReadOnlyReview || run.RepositoryMutationAllowed {
		t.Fatal("review run allowed writes")
	}
	f.call(t, f.person, "POST", "/api/work-orders/"+v.OrderID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 409, nil)
	evidence := map[string]any{"kind": "text", "reference": "VERDICT: ok", "run_id": *v.RunID}
	f.call(t, f.person, "POST", "/api/work-orders/"+v.OrderID+"/evidence", evidence, 403, nil)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='running',account_id=$2,daemon_id='review-daemon',daemon_generation='review-generation',started_at=clock_timestamp(),effective_model='review-model',model_evidence='vendor_reported' WHERE id=$1`, *v.RunID, f.account)
		return err
	})
	f.call(t, f.agent, "POST", "/api/work-orders/"+v.OrderID+"/evidence", evidence, 201, nil)
	var rows []Review
	f.call(t, f.person, "GET", path, nil, 200, &rows)
	if rows[0].GateOpen {
		t.Fatal("running review opened gate")
	}
	evidence["reference"] = "FINDING: high main.go:1 Broken.\nVERDICT: changes"
	f.call(t, f.agent, "POST", "/api/work-orders/"+v.OrderID+"/evidence", evidence, 403, nil)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed',ended_at=clock_timestamp() WHERE id=$1`, *v.RunID)
		return err
	})
	f.call(t, f.person, "GET", path, nil, 200, &rows)
	if !rows[0].GateOpen || rows[0].Result.Verdict != "ok" {
		t.Fatal("completed verified review did not pass")
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET effective_model='weaker-model' WHERE id=$1`, *v.RunID)
		return err
	})
	f.call(t, f.person, "GET", path, nil, 200, &rows)
	if rows[0].GateOpen {
		t.Fatal("different effective model opened the gate")
	}
	// A person's work-order edits cannot manufacture a verdict or switch its reviewer.
	var o workorders.Order
	f.call(t, f.person, "GET", "/api/work-orders/"+v.OrderID, nil, 200, &o)
	f.call(t, f.person, "PATCH", "/api/work-orders/"+v.OrderID, map[string]any{"expected_revision": o.Revision, "assignee_principal_id": f.other.ID}, 409, nil)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_role_routes SET state='unavailable',valid_until=now()+interval '1 hour',reason='fixture unavailable' WHERE profile_id=$1`, f.profile)
		return err
	})
	unavailable := f.input()
	var blocked Review
	f.call(t, f.person, "POST", path, unavailable, 201, &blocked)
	if blocked.Status != "blocked" || blocked.GateOpen || blocked.RunID != nil {
		t.Fatal("unavailable review did not stay closed")
	}
	f.call(t, f.agent, "POST", path, f.input(), 403, nil)
}

func TestMalformedAndChangesRemainClosed(t *testing.T) {
	f := newFixture(t)
	path := "/api/nodes/" + f.ticket + "/reviews"
	for _, text := range []string{"Earlier VERDICT: ok\nTests are still running.", "FINDING: high internal/api.go:9 Tenant constraint missing.\nVERDICT: changes"} {
		var v Review
		f.call(t, f.person, "POST", path, f.input(), 201, &v)
		f.tx(t, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='running',account_id=$2,daemon_id='review-daemon',daemon_generation='review-generation',started_at=clock_timestamp(),model_evidence='vendor_reported',effective_model='review-model' WHERE id=$1`, *v.RunID, f.account)
			return err
		})
		f.call(t, f.agent, "POST", "/api/work-orders/"+v.OrderID+"/evidence", map[string]any{"kind": "text", "reference": text, "run_id": *v.RunID}, 201, nil)
		f.tx(t, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed',ended_at=clock_timestamp() WHERE id=$1`, *v.RunID)
			return err
		})
		var rows []Review
		f.call(t, f.person, "GET", path, nil, 200, &rows)
		if rows[0].GateOpen {
			t.Fatal("malformed or changes verdict passed")
		}
	}
}

func TestCompletedBuilderAutomaticallyRequestsIndependentReview(t *testing.T) {
	f := newFixture(t)
	var order workorders.Order
	f.call(t, f.agent, "POST", "/api/work-orders", map[string]any{"title": "Builder", "parent_id": f.ticket, "assignee_principal_id": f.agent.ID, "criteria": []string{"Pass isolation tests"}}, 201, &order)
	var source string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,status,account_id,daemon_id,daemon_generation,started_at)
       SELECT $1,$2,$3,p.id,p.model,'running',$4,'review-daemon','review-generation',clock_timestamp() FROM model_profiles p WHERE p.family='openai' RETURNING id::text`, f.person.TenantID, order.NodeID, f.agent.ID, f.account).Scan(&source)
	})
	report := agentruns.Telemetry{Sequence: 1, Kind: "finished", Status: "completed", ReviewRange: &reviewgate.CommitRange{Repository: "example/review-fixture", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}}
	f.call(t, f.agent, "POST", "/api/runs/"+source+"/telemetry", report, 200, nil)
	var rows []Review
	f.call(t, f.person, "GET", "/api/nodes/"+f.ticket+"/reviews", nil, 200, &rows)
	if len(rows) != 1 || rows[0].AuthorRunID == nil || *rows[0].AuthorRunID != source || rows[0].AuthorFamily != "openai" || rows[0].RunID == nil {
		t.Fatal("builder completion did not queue the exact independent review")
	}
	f.call(t, f.agent, "POST", "/api/runs/"+source+"/telemetry", report, 200, nil)
	f.call(t, f.person, "GET", "/api/nodes/"+f.ticket+"/reviews", nil, 200, &rows)
	if len(rows) != 1 {
		t.Fatal("completion replay queued another review")
	}
}

func TestReviewRespectsProjectVisibilityAndSealsSnapshot(t *testing.T) {
	f := newFixture(t)
	a, b := testID(), testID()
	viewer := tenant.Principal{ID: testID(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, func(tx pgx.Tx) error {
		for i, id := range []string{a, b} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2::uuid,$3,id,'Project' FROM node_kinds WHERE slug='project'`, f.person.TenantID, id, fmt.Sprintf("PRJ-%d", i+1)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticket, a); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Other project viewer')`, viewer.TenantID, viewer.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE key='guest'`, viewer.TenantID, viewer.ID, b)
		return err
	})
	var v Review
	path := "/api/nodes/" + f.ticket + "/reviews"
	f.call(t, f.person, "POST", path, f.input(), 201, &v)
	f.call(t, viewer, "GET", path, nil, 404, nil)
	f.call(t, viewer, "POST", path, f.input(), 404, nil)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET body='Changed after review was requested' WHERE id=$1`, v.OrderID)
		return err
	})
	var order workorders.Order
	f.call(t, f.person, "GET", "/api/work-orders/"+v.OrderID, nil, 200, &order)
	if order.Review == nil || !strings.Contains(order.Review.TicketSnapshot, "Cross-tenant tests pass") || strings.Contains(order.Review.TicketSnapshot, "Changed after") {
		t.Fatal("ticket snapshot changed with the editable node")
	}
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_order_reviews SET head_sha=$2 WHERE work_order_id=$1`, v.OrderID, strings.Repeat("c", 40))
		return err
	})
	if err == nil {
		t.Fatal("review commit binding was mutable")
	}
}

func TestLegacyDaemonCannotSeeOrClaimAReview(t *testing.T) {
	f := newFixture(t)
	var v Review
	f.call(t, f.person, "POST", "/api/nodes/"+f.ticket+"/reviews", f.input(), 201, &v)
	var modern []agentruns.Run
	f.call(t, f.agent, "GET", "/api/runs/queued", nil, 200, &modern)
	if len(modern) != 1 || !modern[0].ReadOnlyReview {
		t.Fatal("review-capable daemon did not receive review")
	}
	r := httptest.NewRequest("GET", "/api/runs/queued", nil)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), f.agent))
	r.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	var legacy []agentruns.Run
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &legacy) != nil || len(legacy) != 0 {
		t.Fatal("legacy daemon received review")
	}
	raw, _ := json.Marshal(map[string]any{"daemon_id": "review-daemon", "daemon_generation": "review-generation", "reservation_ids": []string{testID()}})
	r = httptest.NewRequest("POST", "/api/runs/"+*v.RunID+"/claim", strings.NewReader(string(raw)))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), f.agent))
	r.Header.Set("Authorization", "Bearer "+f.token)
	w = httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "review-capable") {
		t.Fatal("legacy daemon could claim a writable review")
	}
}

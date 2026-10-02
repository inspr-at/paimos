// SPDX-License-Identifier: AGPL-3.0-only
package autopilotlanes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type fixture struct {
	d                     *dbtest.DB
	m                     *Module
	mux                   *http.ServeMux
	owner                 tenant.Principal
	project, otherProject string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('lane-policy','Lane policy') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{d: d, m: New(d.App), mux: http.NewServeMux(), owner: tenant.Principal{TenantID: tid, Kind: tenant.Person}}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Lane owner') RETURNING id::text`, tid).Scan(&f.owner.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, f.owner.ID, "owner")
	f.tx(t, func(tx pgx.Tx) error {
		for i, out := range []*string{&f.project, &f.otherProject} {
			if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),$2 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='project' RETURNING id::text`, tid, fmt.Sprintf("Project %d", i)).Scan(out); err != nil {
				return err
			}
		}
		return nil
	})
	f.m.now = func() time.Time { return time.Date(2026, 10, 2, 22, 0, 0, 0, time.UTC) }
	f.m.Mount(f.mux)
	agentruns.New(d.App).Mount(f.mux)
	return f
}
func (f *fixture) tx(t *testing.T, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.owner.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) person(t *testing.T, projectID string, perms []string) tenant.Principal {
	t.Helper()
	p := tenant.Principal{TenantID: f.owner.TenantID, Kind: tenant.Person}
	ctx := t.Context()
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Project person') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'lane_'||replace($2::text,'-',''),'Lane role') RETURNING id::text`, p.TenantID, p.ID).Scan(&role); err != nil {
			return err
		}
		for _, perm := range perms {
			if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, p.TenantID, role, perm); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, p.TenantID, p.ID, role, projectID)
		return err
	})
	return p
}
func (f *fixture) call(t *testing.T, p tenant.Principal, method, path string, in any, status int, out any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	var err error
	if s, ok := in.(string); ok {
		raw = []byte(s)
	} else if in != nil {
		raw, err = json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req = req.WithContext(tenant.WithPrincipal(t.Context(), p))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	if w.Code != status {
		t.Fatalf("%s %s got %d: %s; want %d", method, path, w.Code, w.Body.String(), status)
	}
	if out != nil {
		if err = json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	return w
}
func (f *fixture) lane(t *testing.T, projectID string) Lane {
	t.Helper()
	var l Lane
	f.call(t, f.owner, "POST", "/api/projects/"+projectID+"/autopilot-lanes", createInput{Name: "Night fixes", Policy: testPolicy()}, 201, &l)
	return l
}
func (f *fixture) ticket(t *testing.T, projectID, kind string, fields map[string]any) (string, time.Time) {
	t.Helper()
	raw, _ := json.Marshal(fields)
	var id string
	var revision time.Time
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state,fields) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Ticket',$2,'open',$3 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug=$4 RETURNING id::text,updated_at`, f.owner.TenantID, projectID, raw, kind).Scan(&id, &revision)
	})
	return id, revision
}
func (f *fixture) eventCount(t *testing.T, id string) int {
	t.Helper()
	var n int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=$1 AND type LIKE 'node.autopilot_%'`, id).Scan(&n)
	})
	return n
}
func TestLanePolicyLifecycleAndTrustBoundaries(t *testing.T) {
	f := setup(t)
	l := f.lane(t, f.project)
	path := "/api/autopilot-lanes/" + l.ID
	if l.Revision != 1 || l.Enabled || l.Paused || l.OwnerPrincipalID != f.owner.ID || l.Scope.Kind != "queued_tickets" {
		t.Fatalf("unsafe defaults %+v", l)
	}
	manager := f.person(t, f.project, []string{"nodes.read", "autopilot.read", "autopilot.manage", "autopilot.pause"})
	reader := f.person(t, f.project, []string{"nodes.read", "autopilot.read"})
	outsider := f.person(t, f.otherProject, []string{"nodes.read", "autopilot.read", "autopilot.manage"})
	f.call(t, reader, "GET", path, nil, 200, nil)
	f.call(t, reader, "PATCH", path, patchInput{ExpectedRevision: 1, Enabled: ptr(true)}, 403, nil)
	f.call(t, outsider, "GET", path, nil, 404, nil)
	f.call(t, manager, "PATCH", path, patchInput{ExpectedRevision: 1, Enabled: ptr(true)}, 403, nil)
	if f.eventCount(t, l.ID) != 1 {
		t.Fatal("denied activation wrote an event")
	}
	f.call(t, f.owner, "PATCH", path, patchInput{ExpectedRevision: 1, Enabled: ptr(true)}, 200, &l)
	if !l.Enabled || l.Revision != 2 {
		t.Fatalf("activation %+v", l)
	}
	f.call(t, f.owner, "PATCH", path, patchInput{ExpectedRevision: 1, Name: ptr("stale")}, 409, nil)
	f.call(t, manager, "PATCH", path, patchInput{ExpectedRevision: 2, Policy: ptr(testPolicy())}, 403, nil)
	f.call(t, f.owner, "POST", path+"/pause", actionInput{ExpectedRevision: 2, Reason: "Coordinator review"}, 200, &l)
	if !l.Paused || l.Revision != 3 {
		t.Fatalf("pause %+v", l)
	}
	// Agent governance denial must hold even with forged broad key scopes.
	agent := tenant.Principal{TenantID: f.owner.TenantID, Kind: tenant.Agent, KeyCreatorID: f.owner.ID, Scopes: append(append([]string{}, authz.CoordinatorBaseScopes...), "autopilot.read", "autopilot.pause", "autopilot.manage")}
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Coordinator') RETURNING id::text`, agent.TenantID).Scan(&agent.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, agent.TenantID, agent.ID, "admin")
	f.call(t, agent, "POST", path+"/resume", actionInput{ExpectedRevision: 3}, 403, nil)
	f.call(t, agent, "PATCH", path, patchInput{ExpectedRevision: 3, Enabled: ptr(true)}, 403, nil)
	f.call(t, agent, "POST", "/api/projects/"+f.project+"/autopilot-lanes", createInput{Name: "Agent lane", Policy: testPolicy()}, 403, nil)
	f.call(t, f.owner, "POST", path+"/resume", actionInput{ExpectedRevision: 3}, 200, &l)
	f.call(t, agent, "POST", path+"/pause", actionInput{ExpectedRevision: l.Revision, Reason: "Capacity unavailable"}, 200, &l)
	worker := agent
	worker.Scopes = []string{"nodes.read", "autopilot.pause"}
	f.call(t, worker, "POST", path+"/pause", actionInput{ExpectedRevision: l.Revision, Reason: "Unauthorized worker"}, 403, nil)
	noPause := agent
	noPause.Scopes = authz.CoordinatorBaseScopes
	f.call(t, noPause, "POST", path+"/pause", actionInput{ExpectedRevision: l.Revision}, 403, nil)
	f.call(t, f.owner, "POST", path+"/pause", actionInput{ExpectedRevision: l.Revision, Reason: l.PauseReason}, 200, nil)
	if f.eventCount(t, l.ID) != int(l.Revision) {
		t.Fatal("one event per actual policy write")
	}
	// Policy-only management can disable; it cannot mint execution authority.
	f.call(t, manager, "PATCH", path, patchInput{ExpectedRevision: l.Revision, Enabled: ptr(false)}, 200, &l)
	f.call(t, manager, "PATCH", path, patchInput{ExpectedRevision: l.Revision, Name: ptr("Renamed while disabled")}, 200, &l)
	var history struct {
		Items []struct{ Type string }
		Next  *int64 `json:"next_after"`
	}
	f.call(t, reader, "GET", path+"/history?limit=2", nil, 200, &history)
	if len(history.Items) != 2 || history.Next == nil {
		t.Fatalf("history %+v", history)
	}
	f.call(t, reader, "GET", path+"/history?after="+fmt.Sprint(*history.Next), nil, 200, &history)
	if len(history.Items) != int(l.Revision)-2 {
		t.Fatalf("history page %+v", history)
	}
	second := f.lane(t, f.project)
	_ = second
	var page struct {
		Items []Lane
		Next  *string `json:"next_after"`
	}
	f.call(t, reader, "GET", "/api/projects/"+f.project+"/autopilot-lanes?limit=1", nil, 200, &page)
	if len(page.Items) != 1 || page.Next == nil {
		t.Fatal("missing bounded lane page")
	}
	first := page.Items[0].ID
	f.call(t, reader, "GET", "/api/projects/"+f.project+"/autopilot-lanes?limit=1&after="+*page.Next, nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].ID == first || page.Next != nil {
		t.Fatal("bad lane keyset")
	}
	// RLS on both new tables applies even without endpoint checks.
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), outsider), f.d.App, f.owner.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM autopilot_lanes`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatalf("project RLS leaked %d lanes", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var foreign string
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('other-lane-tenant','Other') RETURNING id::text`).Scan(&foreign)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, foreign, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM autopilot_lanes`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("tenant RLS leaked lanes")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Release scope is validated and stored without executing it.
	release, _ := f.ticket(t, f.project, "release", map[string]any{})
	var rel Lane
	f.call(t, f.owner, "POST", "/api/projects/"+f.project+"/autopilot-lanes", createInput{Name: "Build release", Scope: &Scope{Kind: "release", ReleaseNodeID: &release}, Policy: testPolicy()}, 201, &rel)
	f.call(t, f.owner, "POST", "/api/autopilot-lanes/"+rel.ID+"/resume", actionInput{ExpectedRevision: 1}, 409, nil)
	wrongRelease, _ := f.ticket(t, f.otherProject, "release", map[string]any{})
	f.call(t, f.owner, "PATCH", "/api/autopilot-lanes/"+rel.ID, patchInput{ExpectedRevision: 1, Scope: &Scope{Kind: "release", ReleaseNodeID: &wrongRelease}}, 404, nil)
	var preview Preview
	f.call(t, f.owner, "GET", "/api/autopilot-lanes/"+rel.ID+"/preview", nil, 200, &preview)
	if preview.ExecutionAvailable || len(preview.Items) != 0 || !includes(preview.MissingDecisions, "release_order_integration_pending") {
		t.Fatalf("release preview %+v", preview)
	}
}

func TestPrepareIntentRevisionsAndIsolation(t *testing.T) {
	f := setup(t)
	l := f.lane(t, f.project)
	path := "/api/autopilot-lanes/" + l.ID + "/prepare"
	missing, rev := f.ticket(t, f.project, "ticket", map[string]any{"acceptance_criteria": "Human criteria stay authoritative"})
	in := prepareInput{l.Revision, missing, rev}
	var first, again PreparationRequest
	f.call(t, f.owner, "POST", path, in, 202, &first)
	f.call(t, f.owner, "POST", path, in, 202, &again)
	if first.ID != again.ID || first.Launched || first.Status != "requested" || first.Preparation != "automatic_estimate" || f.eventCount(t, l.ID) != 2 {
		t.Fatalf("intent %+v / %+v", first, again)
	}
	criteria, rev := f.ticket(t, f.project, "ticket", map[string]any{"estimate_hours": 2})
	f.call(t, f.owner, "POST", path, prepareInput{l.Revision, criteria, rev}, 202, &again)
	if again.Preparation != "criteria_draft_requires_person" {
		t.Fatalf("criteria intent %+v", again)
	}
	f.call(t, f.owner, "POST", path, prepareInput{99, criteria, rev}, 409, nil)
	f.call(t, f.owner, "POST", path, prepareInput{l.Revision, criteria, rev.Add(time.Second)}, 409, nil)
	other, rev := f.ticket(t, f.otherProject, "ticket", map[string]any{})
	f.call(t, f.owner, "POST", path, prepareInput{l.Revision, other, rev}, 404, nil)
	ready, rev := f.ticket(t, f.project, "ticket", map[string]any{"estimate_hours": 2, "acceptance_criteria": "Ready"})
	f.call(t, f.owner, "POST", path, prepareInput{l.Revision, ready, rev}, 409, nil)
	outsider := f.person(t, f.otherProject, []string{"nodes.read", "autopilot.read"})
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), outsider), f.d.App, f.owner.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM autopilot_preparation_requests`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("preparation project RLS leak")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM agent_runs`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("Prepare launched or queued work")
		}
		var fields map[string]any
		var raw []byte
		if err := tx.QueryRow(t.Context(), `SELECT fields FROM nodes WHERE id=$1`, missing).Scan(&raw); err != nil {
			return err
		}
		_ = json.Unmarshal(raw, &fields)
		if fields["acceptance_criteria"] != "Human criteria stay authoritative" || fields["estimate_hours"] != nil {
			t.Fatal("Prepare changed criteria or estimate")
		}
		return nil
	})
}

func TestPreviewSharedQueueOrderingAndNoWrites(t *testing.T) {
	f := setup(t)
	l := f.lane(t, f.project)
	path := "/api/autopilot-lanes/" + l.ID
	fields := map[string]any{"estimate_hours": 2, "acceptance_criteria": "Ready", "route_role": "build", "area": "backend", "priority": "low"}
	low, _ := f.ticket(t, f.project, "ticket", fields)
	fields["priority"] = "high"
	high, _ := f.ticket(t, f.project, "ticket", fields)
	f.call(t, f.owner, "POST", "/api/queue", map[string]string{"node_id": low}, 200, nil)
	f.call(t, f.owner, "POST", "/api/queue", map[string]string{"node_id": high}, 200, nil)
	count := f.eventCount(t, l.ID)
	var preview Preview
	f.call(t, f.owner, "GET", path+"/preview?limit=1", nil, 200, &preview)
	if preview.ExecutionAvailable || len(preview.Items) != 1 || preview.Items[0].TicketNodeID != high || preview.Candidate == nil || preview.NextCursor == nil {
		t.Fatalf("preview %+v", preview)
	}
	cursor := *preview.NextCursor
	f.call(t, f.owner, "GET", path+"/preview?limit=1&cursor="+cursor, nil, 200, &preview)
	if len(preview.Items) != 1 || preview.Items[0].TicketNodeID != low || preview.NextCursor != nil {
		t.Fatalf("preview page %+v", preview)
	}
	f.call(t, f.owner, "POST", "/api/queue/"+low+"/move", map[string]int{"position": 1}, 200, nil)
	f.call(t, f.owner, "GET", path+"/preview", nil, 200, &preview)
	if len(preview.Items) != 2 || preview.Items[0].TicketNodeID != low {
		t.Fatalf("manual order %+v", preview)
	}
	if count != f.eventCount(t, l.ID) {
		t.Fatal("preview wrote events")
	}
	f.call(t, f.owner, "PATCH", path, patchInput{ExpectedRevision: 1, Name: ptr("New name")}, 200, &l)
	f.call(t, f.owner, "GET", path+"/preview?cursor="+cursor, nil, 409, nil)
	f.call(t, f.owner, "GET", path+"/preview?limit=101", nil, 400, nil)
	f.call(t, f.owner, "GET", path+"/history?after=-1", nil, 400, nil)
	f.call(t, f.owner, "GET", path+"/preview?cursor="+strings.Repeat("a", 1025), nil, 400, nil)
	// Turning Status autopilot on cannot enable dispatch policy.
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE status_autopilot_settings SET enabled=true`)
		return err
	})
	f.call(t, f.owner, "GET", path, nil, 200, &l)
	if l.Enabled {
		t.Fatal("Status autopilot activated a lane")
	}
}

func TestInvalidLaneBodiesFailBeforeStorage(t *testing.T) {
	mux := http.NewServeMux()
	New(nil).Mount(mux)
	p := tenant.Principal{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", TenantID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Kind: tenant.Person}
	for _, tc := range []struct {
		path, body string
		want       int
	}{
		{"/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/autopilot-lanes", `{"name":"bad","policy":null}`, 400},
		{"/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/autopilot-lanes", `{"name":"bad","enabled":true}`, 400},
		{"/api/autopilot-lanes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/pause", `{"expected_revision":1,"reason":"` + strings.Repeat("x", 17000) + `"}`, 413},
		{"/api/autopilot-lanes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/pause", `{"expected_revision":1} {}`, 400},
	} {
		r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		r = r.WithContext(tenant.WithPrincipal(context.Background(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d: %s", w.Code, w.Body.String())
		}
	}
}

func TestAuthorizationRevocationFencesFinalMutation(t *testing.T) {
	f := setup(t)
	l := f.lane(t, f.project)
	manager := f.person(t, f.project, []string{"nodes.read", "autopilot.read", "autopilot.manage"})
	ctx := tenant.WithPrincipal(t.Context(), manager)
	// The outer permission check succeeds before the revoker takes the fence.
	if err := authz.RequirePattern(authz.BindPool(ctx, f.d.App), "PATCH /api/autopilot-lanes/{laneId}", authz.Scope{ProjectID: f.project}); err != nil {
		t.Fatal(err)
	}
	conn, err := f.d.Admin.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.owner.TenantID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(patchInput{ExpectedRevision: l.Revision, Name: ptr("revoked write")})
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest("PATCH", "/api/autopilot-lanes/"+l.ID, bytes.NewReader(raw))
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, req)
		response <- w
	}()
	// Observe the actual database lock wait, not elapsed time or a sleep.
	waitCtx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err = f.d.Admin.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT id::text FROM tenants%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case w := <-response:
			t.Fatalf("mutation escaped the access fence: %d %s", w.Code, w.Body.String())
		default:
		}
	}
	if _, err = tx.Exec(t.Context(), `DELETE FROM role_permissions rp USING role_bindings b WHERE b.tenant_id=rp.tenant_id AND b.role_id=rp.role_id AND b.principal_id=$1 AND rp.permission='autopilot.manage'`, manager.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-response:
		if w.Code != 403 {
			t.Fatalf("revoked mutation got %d: %s", w.Code, w.Body.String())
		}
	case <-t.Context().Done():
		t.Fatal("mutation did not finish")
	}
	var after Lane
	f.call(t, f.owner, "GET", "/api/autopilot-lanes/"+l.ID, nil, 200, &after)
	if after.Revision != l.Revision || after.Name != l.Name || f.eventCount(t, l.ID) != 1 {
		t.Fatal("revoked mutation changed policy or audit")
	}
}

func TestLaneRouteMatrixAndRealMuxScope(t *testing.T) {
	f := setup(t)
	l := f.lane(t, f.project)
	viewer := f.person(t, f.project, []string{"nodes.read", "autopilot.read"})
	manager := f.person(t, f.project, []string{"nodes.read", "autopilot.read", "autopilot.manage", "autopilot.pause"})
	outsider := f.person(t, f.otherProject, []string{"nodes.read", "autopilot.read", "autopilot.manage"})
	for _, tc := range []struct{ method, suffix, key string }{
		{"GET", "", "autopilot.read"}, {"PATCH", "", "autopilot.manage"},
		{"GET", "/preview", "autopilot.read"}, {"GET", "/history", "autopilot.read"},
		{"POST", "/prepare", "autopilot.manage"}, {"POST", "/pause", "autopilot.pause"}, {"POST", "/resume", "autopilot.manage"},
	} {
		req := httptest.NewRequest(tc.method, "/api/autopilot-lanes/"+l.ID+tc.suffix, nil)
		_, pattern := f.mux.Handler(req)
		declared, ok := authz.PermissionForPattern(pattern)
		if !ok || declared != tc.key {
			t.Fatalf("undeclared real route %q", pattern)
		}
		for _, actor := range []tenant.Principal{viewer, manager, outsider} {
			ctx := tenant.WithPrincipal(t.Context(), actor)
			scope, ok, err := authz.ResolveRouteScope(ctx, f.d.App, pattern, req.URL.Path)
			if err != nil {
				t.Fatal(err)
			}
			if actor.ID == outsider.ID {
				if ok {
					t.Fatal("invisible lane acquired project scope")
				}
				continue
			}
			if !ok || scope.ProjectID != f.project {
				t.Fatalf("wrong scope %+v", scope)
			}
			err = authz.RequirePattern(authz.BindPool(ctx, f.d.App), pattern, scope)
			want := actor.ID == manager.ID || tc.key == "autopilot.read"
			if (err == nil) != want {
				t.Fatalf("route %s for %s: %v; allowed=%v", pattern, actor.ID, err, want)
			}
		}
	}
	perm, ok := authz.Lookup("autopilot.manage")
	if !ok || perm.AgentGrantable {
		t.Fatal("management grantable to agents")
	}
	for _, key := range []string{"autopilot.read", "autopilot.pause"} {
		perm, ok := authz.Lookup(key)
		if !ok || !perm.AgentGrantable || !authz.ProjectGrantable(key) {
			t.Fatalf("bad project permission %s", key)
		}
	}
}

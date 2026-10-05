// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"context"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type parentSnapshot struct {
	ID                    string
	State                 string
	ParentRevision        time.Time `json:"parent_revision"`
	TreeRevision          string    `json:"tree_revision"`
	Partial, Truncated    bool
	TreeChanged           bool `json:"tree_changed"`
	ContinuationAvailable bool `json:"continuation_available"`
	Items                 []struct {
		NodeID   string `json:"node_id"`
		Revision time.Time
		Outcome  string
		RunID    string `json:"run_id"`
	}
}

func (f *fixture) captureParent(t *testing.T, parent string) parentSnapshot {
	t.Helper()
	var n struct {
		UpdatedAt time.Time `json:"updated_at"`
	}
	f.call(t, f.person, "GET", "/api/nodes/"+parent, nil, 200, &n)
	var s parentSnapshot
	f.call(t, f.person, "POST", "/api/queue/"+parent+"/snapshots", map[string]any{"expected_revision": n.UpdatedAt}, 200, &s)
	if s.ID == "" || s.TreeRevision == "" || s.ParentRevision.IsZero() {
		t.Fatalf("missing snapshot identity/revisions: %+v", s)
	}
	return s
}
func (f *fixture) applyParent(t *testing.T, s parentSnapshot) parentSnapshot {
	t.Helper()
	var out parentSnapshot
	f.call(t, f.person, "POST", "/api/queue-snapshots/"+s.ID+"/apply", nil, 200, &out)
	return out
}
func snapshotOutcomes(s parentSnapshot) map[string]string {
	out := map[string]string{}
	for _, item := range s.Items {
		out[item.NodeID] = item.Outcome
	}
	return out
}
func TestParentQueueSnapshotChangedLeavesAndRetry(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	parent := f.apiQueueTicket(t, nil)
	good := f.apiQueueTicket(t, &parent)
	edited := f.apiQueueTicket(t, &parent)
	closed := f.apiQueueTicket(t, &parent)
	moved := f.apiQueueTicket(t, &parent)
	branch := f.apiQueueTicket(t, &parent)
	branchLeaf := f.apiQueueTicket(t, &branch)
	s := f.captureParent(t, parent)
	if len(s.Items) != 5 || s.Partial {
		t.Fatalf("capture %+v", s)
	}
	// New descendants do not enter the captured work. Moving an entire branch
	// leaves the leaf's own revision unchanged, so ancestry must be checked too.
	arrival := f.apiQueueTicket(t, &parent)
	other := f.apiQueueTicket(t, nil)
	f.call(t, f.person, "PATCH", "/api/nodes/"+edited, map[string]any{"title": "Revised"}, 200, nil)
	f.call(t, f.person, "PATCH", "/api/nodes/"+closed, map[string]any{"state": "done", "fields": map[string]any{"pill_en": "Closed work", "pill_de": "Arbeit abgeschlossen", "benefit_en": "Captured closure test.", "benefit_de": "Abschluss erfasst."}}, 200, nil)
	for _, id := range []string{moved, branch} {
		f.call(t, f.person, "POST", "/api/nodes/"+id+"/move", map[string]any{"parent_id": other}, 200, nil)
	}
	out := f.applyParent(t, s)
	got := snapshotOutcomes(out)
	if out.State != "applied" || !out.Partial || !out.TreeChanged || got[good] != "queued" || got[edited] != "changed" || got[closed] != "changed" || got[moved] != "changed" || got[branchLeaf] != "changed" || got[arrival] != "" {
		t.Fatalf("apply %+v", out)
	}
	if f.queuePage(t).Count != 1 {
		t.Fatal("unexpected queue membership")
	}
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1`, parent) != 0 {
		t.Fatal("parent has a run")
	}
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1 AND queue_routed_at IS NOT NULL`, good) != 0 {
		t.Fatal("snapshot routed or launched work")
	}
	f.call(t, f.person, "DELETE", "/api/queue/"+good, nil, 200, nil)
	replay := f.applyParent(t, s)
	if snapshotOutcomes(replay)[good] != "queued" || f.queuePage(t).Count != 0 {
		t.Fatal("retry re-added removed work or changed historical receipt")
	}
	if f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='queue.added'`, good) != 1 {
		t.Fatal("duplicate queue audit")
	}
}
func TestParentQueueSnapshotDuplicatesEligibilityAndCancellation(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	parent := f.apiQueueTicket(t, nil)
	first := f.apiQueueTicket(t, &parent)
	missing := f.apiQueueTicket(t, &parent)
	f.call(t, f.person, "PATCH", "/api/nodes/"+missing, map[string]any{"fields": map[string]any{"estimate_hours": nil}}, 200, nil)
	s := f.captureParent(t, parent)
	cancelled := f.captureParent(t, parent)
	f.call(t, f.person, "DELETE", "/api/queue-snapshots/"+cancelled.ID, nil, 200, nil)
	f.call(t, f.person, "DELETE", "/api/queue-snapshots/"+cancelled.ID, nil, 200, nil)
	f.call(t, f.person, "POST", "/api/queue-snapshots/"+cancelled.ID+"/apply", nil, 409, nil)
	if f.queuePage(t).Count != 0 {
		t.Fatal("cancelled snapshot queued work")
	}
	f.addQueue(t, first, nil)
	out := f.applyParent(t, s)
	got := snapshotOutcomes(out)
	if got[first] != "already_queued" || got[missing] != "not_ready" || !out.Partial || f.queuePage(t).Count != 1 {
		t.Fatalf("outcomes %+v", out)
	}
	f.call(t, f.person, "DELETE", "/api/queue-snapshots/"+s.ID, nil, 409, nil)
	if f.queuePage(t).Count != 1 {
		t.Fatal("late cancellation removed queued work")
	}
}
func TestParentQueueSnapshotBounds(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	parent := f.apiQueueTicket(t, nil)
	// Retain all 101 real leaves in the fixture; a traversal limit must not be
	// proved by deleting the data being bounded.
	for range 101 {
		f.apiQueueTicket(t, &parent)
	}
	s := f.captureParent(t, parent)
	if !s.Truncated || !s.Partial || len(s.Items) != 100 {
		t.Fatalf("bounds %+v", s)
	}
	out := f.applyParent(t, s)
	if !out.Partial || !out.Truncated || f.queuePage(t).Count != 100 {
		t.Fatalf("bounded apply %+v", out)
	}
	// A timeout before transaction entry must not create any snapshot or queue
	// write. This is cancellation evidence, with no elapsed-time assertion.
	r := httprequestCancelled(f.person, "/api/queue-snapshots/"+s.ID+"/apply")
	w := serveSnapshotRequest(f, r)
	if w.Code == 200 {
		t.Fatal("cancelled request reported success")
	}
}
func TestParentQueueSnapshotOwnerPermissionRevocationAndVisibility(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	parent := f.apiQueueTicket(t, nil)
	f.apiQueueTicket(t, &parent)
	s := f.captureParent(t, parent)
	other := tenant.Principal{ID: uuid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Other')`, other.TenantID, other.ID)
		return err
	})
	dbtest.BindRole(t, f.d, other.TenantID, other.ID, "admin")
	f.call(t, other, "GET", "/api/queue-snapshots/"+s.ID, nil, 404, nil)
	dbtest.BindRole(t, f.d, f.foreign.TenantID, f.foreign.ID, "admin")
	f.call(t, f.foreign, "GET", "/api/queue-snapshots/"+s.ID, nil, 404, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		// Keep nodes.read while revoking queue authority, so denial must come
		// from the final write check and not the outer read prerequisite.
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1;`, f.person.ID)
		return err
	})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH role AS (INSERT INTO roles(tenant_id,key,name) VALUES($1,'snapshot_reader','Snapshot reader') RETURNING tenant_id,id) INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT tenant_id,id,'nodes.read' FROM role`, f.person.TenantID)
		return err
	})
	dbtest.BindRole(t, f.d, f.person.TenantID, f.person.ID, "snapshot_reader")
	var rejection struct{ Error string }
	f.call(t, f.person, "POST", "/api/queue-snapshots/"+s.ID+"/apply", nil, 403, &rejection)
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id IS NOT NULL`) != 0 {
		t.Fatal("revoked permission queued work")
	}
	dbtest.BindRole(t, f.d, f.person.TenantID, f.person.ID, "admin")
	out := f.applyParent(t, s)
	if out.State != "applied" {
		t.Fatal("denial consumed snapshot")
	}
	// Lose all project visibility after application: stored leaf identifiers
	// must not be replayed to a now unauthorized owner.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
		return err
	})
	w := f.request(f.person, http.MethodGet, "/api/queue-snapshots/"+s.ID, "", "")
	if w.Code != 403 || strings.Contains(w.Body.String(), parent) {
		t.Fatalf("revoked read disclosure: %d", w.Code)
	}
}

// Helpers keep context cancellation independent of wall-clock timing.
func httprequestCancelled(p tenant.Principal, path string) *http.Request {
	ctx, cancel := context.WithCancel(tenant.WithPrincipal(context.Background(), p))
	cancel()
	return httptest.NewRequest(http.MethodPost, path, nil).WithContext(ctx)
}
func serveSnapshotRequest(f *fixture, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func TestParentQueueSnapshotHiddenMovedLeaf(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	var visible, hidden string
	owner := tenant.Principal{ID: uuid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Project owner')`, owner.TenantID, owner.ID); err != nil {
			return err
		}
		for _, target := range []*string{&visible, &hidden} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Project' FROM node_kinds WHERE slug='project' RETURNING id::text`, owner.TenantID).Scan(target); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, owner.TenantID, owner.ID, visible)
		return err
	})
	parent := f.apiQueueTicket(t, &visible)
	retained := f.apiQueueTicket(t, &parent)
	moved := f.apiQueueTicket(t, &parent)
	hiddenLeaf := f.apiQueueTicket(t, &hidden)
	var node struct {
		UpdatedAt time.Time `json:"updated_at"`
	}
	f.call(t, owner, "GET", "/api/nodes/"+parent, nil, 200, &node)
	var s parentSnapshot
	f.call(t, owner, "POST", "/api/queue/"+parent+"/snapshots", map[string]any{"expected_revision": node.UpdatedAt}, 200, &s)
	if len(s.Items) != 2 || snapshotOutcomes(s)[hiddenLeaf] != "" {
		t.Fatalf("capture disclosed hidden work: %+v", s)
	}
	f.call(t, f.person, "POST", "/api/nodes/"+moved+"/move", map[string]any{"parent_id": hidden}, 200, nil)
	var out parentSnapshot
	f.call(t, owner, "POST", "/api/queue-snapshots/"+s.ID+"/apply", nil, 200, &out)
	if len(out.Items) != 1 || out.Items[0].NodeID != retained || out.Items[0].Outcome != "queued" || !out.Partial {
		t.Fatalf("hidden leaf result %+v", out)
	}
	w := f.request(owner, http.MethodGet, "/api/queue-snapshots/"+s.ID, "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), moved) || strings.Contains(w.Body.String(), hiddenLeaf) || strings.Contains(w.Body.String(), "path") || strings.Contains(w.Body.String(), "_start") || strings.Contains(w.Body.String(), "_next") {
		t.Fatal("stored snapshot disclosed hidden identity or ancestry")
	}
}

type snapshotFencePause struct {
	first   atomic.Bool
	entered chan struct{}
	resume  chan struct{}
}

func (p *snapshotFencePause) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(q.SQL, "SELECT id FROM tenants WHERE id=current_setting('aeon.tenant_id')") && p.first.CompareAndSwap(false, true) {
		close(p.entered)
		select {
		case <-p.resume:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (p *snapshotFencePause) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestParentQueueSnapshotRevocationWhileApplyWaits(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	parent := f.apiQueueTicket(t, nil)
	leaf := f.apiQueueTicket(t, &parent)
	s := f.captureParent(t, parent)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH role AS (INSERT INTO roles(tenant_id,key,name) VALUES($1,'snapshot_reader','Snapshot reader') RETURNING tenant_id,id) INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT tenant_id,id,'nodes.read' FROM role`, f.person.TenantID)
		return err
	})
	pause := &snapshotFencePause{entered: make(chan struct{}), resume: make(chan struct{})}
	cfg := f.d.App.Config()
	cfg.ConnConfig.Tracer = pause
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	mux := http.NewServeMux()
	agentruns.New(pool).Mount(mux)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/queue-snapshots/"+s.ID+"/apply", nil).WithContext(tenant.WithPrincipal(ctx, f.person))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); mux.ServeHTTP(w, req); done <- w }()
	defer func() {
		select {
		case <-pause.resume:
		default:
			close(pause.resume)
		}
	}()
	select {
	case <-pause.entered:
	case <-ctx.Done():
		t.Fatal("apply did not reach access fence")
	}
	// The request has already passed its preliminary read check. Revocation
	// commits before it can acquire the tenant fence and perform the write.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := agentpairing.LockMutation(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='snapshot_reader'`, f.person.TenantID, f.person.ID)
		return err
	})
	close(pause.resume)
	select {
	case w := <-done:
		if w.Code != 403 {
			t.Fatalf("revocation failed for wrong reason: %d %s", w.Code, w.Body.String())
		}
	case <-ctx.Done():
		t.Fatal("apply hung after fence released")
	}
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1`, leaf) != 0 {
		t.Fatal("revoked apply created a run")
	}
	if f.count(t, f.person, `SELECT count(*) FROM parent_queue_snapshots WHERE id=$1 AND payload->>'state'='pending'`, s.ID) != 1 {
		t.Fatal("revoked apply consumed the snapshot")
	}
}

type snapshotWritePause struct {
	first   atomic.Bool
	written chan struct{}
	resume  chan struct{}
}
type snapshotWritePauseKey struct{}

func (p *snapshotWritePause) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(q.SQL, "UPDATE nodes SET state=$2,fields=$3,updated_at=clock_timestamp()") && p.first.CompareAndSwap(false, true) {
		return context.WithValue(ctx, snapshotWritePauseKey{}, true)
	}
	return ctx
}
func (p *snapshotWritePause) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryEndData) {
	if ctx.Value(snapshotWritePauseKey{}) == true && q.Err == nil {
		close(p.written)
		select {
		case <-p.resume:
		case <-ctx.Done():
		}
	}
}
func TestParentQueueSnapshotCancelledWriteRollsBack(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	parent := f.apiQueueTicket(t, nil)
	leaves := []string{f.apiQueueTicket(t, &parent), f.apiQueueTicket(t, &parent)}
	s := f.captureParent(t, parent)
	pause := &snapshotWritePause{written: make(chan struct{}), resume: make(chan struct{})}
	cfg := f.d.App.Config()
	cfg.ConnConfig.Tracer = pause
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	mux := http.NewServeMux()
	agentruns.New(pool).Mount(mux)
	guard, stop := context.WithTimeout(t.Context(), 8*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(guard)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/queue-snapshots/"+s.ID+"/apply", nil).WithContext(tenant.WithPrincipal(ctx, f.person))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); mux.ServeHTTP(w, req); done <- w }()
	defer func() {
		select {
		case <-pause.resume:
		default:
			close(pause.resume)
		}
	}()
	select {
	case <-pause.written:
	case <-guard.Done():
		t.Fatal("apply did not prepare first leaf write")
	}
	cancel()
	close(pause.resume)
	select {
	case w := <-done:
		if w.Code == 200 {
			t.Fatal("cancelled partial write reported success")
		}
	case <-guard.Done():
		t.Fatal("cancelled apply hung")
	}
	if f.queuePage(t).Count != 0 || f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=ANY($1::uuid[])`, leaves) != 0 {
		t.Fatal("cancelled apply committed partial membership")
	}
	if f.count(t, f.person, `SELECT count(*) FROM nodes WHERE parent_id=ANY($1::uuid[]) AND deleted_at IS NULL`, leaves) != 0 {
		t.Fatal("cancelled apply retained generated orders")
	}
	if f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=ANY($1::uuid[]) AND type='queue.added'`, leaves) != 0 {
		t.Fatal("cancelled apply retained queue audit")
	}
	if f.count(t, f.person, `SELECT count(*) FROM parent_queue_snapshots WHERE id=$1 AND payload->>'state'='pending'`, s.ID) != 1 {
		t.Fatal("cancelled apply consumed snapshot")
	}
	out := f.applyParent(t, s)
	if out.Partial || f.queuePage(t).Count != 2 {
		t.Fatalf("retry after cancelled write %+v", out)
	}
}

func TestParentQueueSnapshotTraversalBounds(t *testing.T) {
	for _, shape := range []string{"wide", "deep"} {
		t.Run(shape, func(t *testing.T) {
			f := setup(t)
			nodes.New(f.d.App, nil).Mount(f.mux)
			parent := f.apiQueueTicket(t, nil)
			if shape == "wide" {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id,position) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Closed leaf','done',$2,g*1024 FROM node_kinds k CROSS JOIN generate_series(1,257) g WHERE k.slug='work'`, f.person.TenantID, parent)
					return err
				})
			} else {
				branch := parent
				for range 35 {
					branch = f.apiQueueTicket(t, &branch)
				}
			}
			s := f.captureParent(t, parent)
			if !s.Truncated || !s.Partial || len(s.Items) != 0 || shape == "deep" && s.ContinuationAvailable {
				t.Fatalf("unbounded %s expansion: %+v", shape, s)
			}
			if f.queuePage(t).Count != 0 {
				t.Fatal("capture launched or queued work")
			}
		})
	}
}

func TestParentQueueSnapshotActiveBindings(t *testing.T) {
	for _, binding := range []string{"direct_session", "order_ticket_session", "order_session", "run_session", "order_run", "running_order"} {
		t.Run(binding, func(t *testing.T) {
			f := setup(t)
			nodes.New(f.d.App, nil).Mount(f.mux)
			parent := f.apiQueueTicket(t, nil)
			leaf := f.apiQueueTicket(t, &parent)
			var order struct {
				NodeID string `json:"node_id"`
			}
			if binding != "direct_session" {
				f.call(t, f.person, "POST", "/api/work-orders", map[string]any{"title": "Existing order", "parent_id": leaf, "criteria": []string{"Keep existing work"}}, 201, &order)
			}
			s := f.captureParent(t, parent)
			if len(s.Items) != 1 || s.Items[0].NodeID != leaf {
				t.Fatalf("missing captured leaf: %+v", s)
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var project string
				if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&project); err != nil {
					return err
				}
				switch binding {
				case "order_run":
					_, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,status) VALUES($1,$2,$3,$4,'running')`, f.person.TenantID, order.NodeID, f.agent.ID, f.profile)
					return err
				case "running_order":
					_, err := tx.Exec(t.Context(), `UPDATE work_orders SET status='running' WHERE node_id=$1`, order.NodeID)
					return err
				}
				var ticket, workOrder, run any
				switch binding {
				case "direct_session":
					ticket = leaf
				case "order_ticket_session":
					ticket = order.NodeID
				case "order_session":
					workOrder = order.NodeID
				case "run_session":
					var id string
					if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,status) VALUES($1,$2,$3,$4,'completed') RETURNING id::text`, f.person.TenantID, order.NodeID, f.agent.ID, f.profile).Scan(&id); err != nil {
						return err
					}
					run = id
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,work_order_id,run_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase) VALUES($1,$2,$3,$4,$5,$6,'codex','local','unmanaged','worker',CASE WHEN $4::uuid IS NULL THEN 'unknown' ELSE 'ship' END,decode(repeat('ab',32),'hex'),decode(repeat('cd',32),'hex'),'working')`, f.person.TenantID, project, f.agent.ID, ticket, workOrder, run)
				return err
			})
			var revision time.Time
			var busy bool
			f.tx(t, f.person, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT updated_at,aeon_work_busy(id) FROM nodes WHERE id=$1`, leaf).Scan(&revision, &busy)
			})
			if !busy || !revision.Equal(s.Items[0].Revision) {
				t.Fatal("fixture must make the unchanged captured leaf busy")
			}
			orders := f.count(t, f.person, `SELECT count(*) FROM work_orders`)
			runs := f.count(t, f.person, `SELECT count(*) FROM agent_runs`)
			out := f.applyParent(t, s)
			if snapshotOutcomes(out)[leaf] != "active" || !out.Partial || out.State != "applied" {
				t.Fatalf("busy leaf was not skipped: %+v", out)
			}
			if f.count(t, f.person, `SELECT count(*) FROM work_orders`) != orders || f.count(t, f.person, `SELECT count(*) FROM agent_runs`) != runs || f.count(t, f.person, `SELECT count(*) FROM events WHERE type='queue.added'`) != 0 {
				t.Fatal("active outcome created order, run or queue audit")
			}
			retry := f.applyParent(t, s)
			if snapshotOutcomes(retry)[leaf] != "active" || f.count(t, f.person, `SELECT count(*) FROM agent_runs`) != runs {
				t.Fatal("active replay changed receipt or created work")
			}
		})
	}
}

func (f *fixture) continueParent(t *testing.T, parent string, previous parentSnapshot) parentSnapshot {
	t.Helper()
	var n struct {
		UpdatedAt time.Time `json:"updated_at"`
	}
	f.call(t, f.person, "GET", "/api/nodes/"+parent, nil, 200, &n)
	var s parentSnapshot
	f.call(t, f.person, "POST", "/api/queue/"+parent+"/snapshots", map[string]any{"expected_revision": n.UpdatedAt, "continuation_of": previous.ID}, 200, &s)
	if s.ID == "" || s.ID == previous.ID {
		t.Fatal("continuation must create a fresh explicit snapshot")
	}
	return s
}

func TestParentQueueSnapshotLeafContinuation(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	parent := f.apiQueueTicket(t, nil)
	leaves := make(map[string]bool)
	for range 101 {
		leaves[f.apiQueueTicket(t, &parent)] = true
	}
	first := f.captureParent(t, parent)
	if len(first.Items) != 100 || !first.Truncated || !first.ContinuationAvailable {
		t.Fatalf("first page: %+v", first)
	}
	f.applyParent(t, first)
	second := f.continueParent(t, parent, first)
	if len(second.Items) != 1 || second.Truncated || second.Partial || second.ContinuationAvailable {
		t.Fatalf("remaining page: %+v", second)
	}
	for _, item := range first.Items {
		delete(leaves, item.NodeID)
	}
	if !leaves[second.Items[0].NodeID] {
		t.Fatal("continuation repeated a visited leaf")
	}
	// Work arriving after this page was captured stays outside its membership.
	arrival := f.apiQueueTicket(t, &parent)
	out := f.applyParent(t, second)
	if snapshotOutcomes(out)[second.Items[0].NodeID] != "queued" || snapshotOutcomes(out)[arrival] != "" || f.queuePage(t).Count != 101 {
		t.Fatalf("continued apply: %+v", out)
	}
	replay := f.continueParent(t, parent, first)
	if snapshotOutcomes(f.applyParent(t, replay))[second.Items[0].NodeID] != "already_queued" {
		t.Fatal("replayed continuation lost identical membership")
	}
}

func TestParentQueueSnapshotNodeContinuation(t *testing.T) {
	for _, shape := range []string{"siblings", "branches"} {
		t.Run(shape, func(t *testing.T) {
			f := setup(t)
			nodes.New(f.d.App, nil).Mount(f.mux)
			parent := f.apiQueueTicket(t, nil)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				// Equal fractional positions exercise the UUID tie-breaker without losing precision.
				if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id,position) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Preceding node','done',$2,0.000000000000001 FROM node_kinds k CROSS JOIN generate_series(1,257) g WHERE k.slug='work'`, f.person.TenantID, parent); err != nil {
					return err
				}
				if shape == "branches" {
					_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Closed child','done',n.id FROM nodes n CROSS JOIN node_kinds k WHERE n.parent_id=$2 AND k.slug='work'`, f.person.TenantID, parent)
					return err
				}
				return nil
			})
			leaf := f.apiQueueTicket(t, &parent)
			first := f.captureParent(t, parent)
			if !first.Truncated || !first.ContinuationAvailable || len(first.Items) != 0 {
				t.Fatalf("unbounded first traversal: %+v", first)
			}
			page := first
			found := false
			for range 3 {
				page = f.continueParent(t, parent, page)
				if snapshotOutcomes(page)[leaf] == "pending" {
					found = true
					break
				}
				if len(page.Items) != 0 || !page.Truncated {
					t.Fatalf("unexpected continuation: %+v", page)
				}
			}
			if !found || page.Truncated {
				t.Fatalf("continuation did not reach remaining work: %+v", page)
			}
			out := f.applyParent(t, page)
			if snapshotOutcomes(out)[leaf] != "queued" || out.TreeChanged || out.Partial || f.queuePage(t).Count != 1 {
				t.Fatalf("continued tree receipt: %+v", out)
			}
			if f.count(t, f.person, `SELECT count(*) FROM nodes WHERE title='Preceding node'`) != 257 {
				t.Fatal("fixture lost traversed nodes")
			}
		})
	}
}

func TestParentQueueSnapshotContinuationGuards(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	parent := f.apiQueueTicket(t, nil)
	branch := f.apiQueueTicket(t, &parent)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id,position,fields) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Open leaf','open',$2,g,'{"estimate_hours":2,"acceptance_criteria":"- [ ] Keep all leaves"}'::jsonb FROM node_kinds k CROSS JOIN generate_series(1,101) g WHERE k.slug='work'`, f.person.TenantID, branch)
		return err
	})
	s := f.captureParent(t, parent)
	if !s.ContinuationAvailable || len(s.Items) != 100 {
		t.Fatalf("missing bounded frontier: %+v", s)
	}
	w := f.request(f.person, http.MethodGet, "/api/queue-snapshots/"+s.ID, "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "path") || strings.Contains(w.Body.String(), "_next") || strings.Contains(w.Body.String(), "_start") {
		t.Fatal("cursor ancestry escaped server storage")
	}
	other := tenant.Principal{ID: uuid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Other owner')`, other.TenantID, other.ID)
		return err
	})
	dbtest.BindRole(t, f.d, other.TenantID, other.ID, "admin")
	body := map[string]any{"expected_revision": s.ParentRevision, "continuation_of": s.ID}
	reject := func(principal tenant.Principal, target string, request map[string]any, status int, message string) {
		t.Helper()
		var response struct{ Error string }
		f.call(t, principal, "POST", "/api/queue/"+target+"/snapshots", request, status, &response)
		if response.Error != message {
			t.Fatalf("wrong rejection: got %q want %q", response.Error, message)
		}
	}
	before := f.count(t, f.person, `SELECT count(*) FROM parent_queue_snapshots`)
	f.call(t, other, "POST", "/api/queue/"+parent+"/snapshots", body, 404, nil)
	dbtest.BindRole(t, f.d, f.foreign.TenantID, f.foreign.ID, "admin")
	foreignParent := ""
	f.tx(t, f.foreign, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Foreign project' FROM node_kinds k WHERE k.slug='project' RETURNING id::text`, f.foreign.TenantID).Scan(&foreignParent)
	})
	f.call(t, f.foreign, "POST", "/api/queue/"+foreignParent+"/snapshots", body, 404, nil)
	wrongParent := f.apiQueueTicket(t, nil)
	f.apiQueueTicket(t, &wrongParent)
	reject(f.person, wrongParent, body, 409, "continuation unavailable for this parent")
	stale := map[string]any{"expected_revision": s.ParentRevision.Add(-time.Second), "continuation_of": s.ID}
	reject(f.person, parent, stale, 409, "parent changed")
	invalid := map[string]any{"expected_revision": s.ParentRevision, "continuation_of": "invalid"}
	reject(f.person, parent, invalid, 400, "invalid continuation snapshot id")
	cancelled := f.captureParent(t, parent)
	f.call(t, f.person, "DELETE", "/api/queue-snapshots/"+cancelled.ID, nil, 200, nil)
	body["continuation_of"] = cancelled.ID
	reject(f.person, parent, body, 409, "continuation unavailable for this parent")
	exhausted := f.continueParent(t, parent, s)
	if exhausted.ContinuationAvailable {
		t.Fatalf("unexpected exhausted page: %+v", exhausted)
	}
	body["continuation_of"] = exhausted.ID
	reject(f.person, parent, body, 409, "continuation unavailable for this parent")
	// Moving an intermediate ancestor invalidates the server-held frontier.
	f.call(t, f.person, "POST", "/api/nodes/"+branch+"/move", map[string]any{"parent_id": wrongParent}, 200, nil)
	// Parent remains a parent by retaining another visible work child.
	f.apiQueueTicket(t, &parent)
	body["continuation_of"] = s.ID
	var current struct {
		UpdatedAt time.Time `json:"updated_at"`
	}
	f.call(t, f.person, "GET", "/api/nodes/"+parent, nil, 200, &current)
	body["expected_revision"] = current.UpdatedAt
	reject(f.person, parent, body, 409, "continuation ancestry changed; capture a fresh snapshot")
	if f.count(t, f.person, `SELECT count(*) FROM parent_queue_snapshots`) != before+2 || f.count(t, f.person, `SELECT count(*) FROM agent_runs`) != 0 {
		t.Fatal("rejected continuations changed snapshots or created work")
	}
}

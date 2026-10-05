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
	ID                 string
	State              string
	ParentRevision     time.Time `json:"parent_revision"`
	TreeRevision       string    `json:"tree_revision"`
	Partial, Truncated bool
	TreeChanged        bool `json:"tree_changed"`
	Items              []struct {
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
	if w.Code != 200 || strings.Contains(w.Body.String(), moved) || strings.Contains(w.Body.String(), hiddenLeaf) || strings.Contains(w.Body.String(), "path") {
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
			if !s.Truncated || !s.Partial || len(s.Items) != 0 {
				t.Fatalf("unbounded %s expansion: %+v", shape, s)
			}
			if f.queuePage(t).Count != 0 {
				t.Fatal("capture launched or queued work")
			}
		})
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package stagehandoff

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEvidenceAndCompletionShareLockOrderWithDerivationDisabled(t *testing.T) {
	m, p, project, release, bearer := fixture(t)
	var h Handoff
	yes := true
	first := EvidenceWrite{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now().UTC().Truncate(time.Microsecond), Authorized: &yes}
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		h, err = m.create(t.Context(), tx, p, RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "prepare", ExpectedJourneyRevision: 1, IdempotencyKey: "lock-order"}, "janus", []string{"authorization", "credential_handoff"}, "")
		if err != nil {
			return err
		}
		first.AuthorityEpoch = h.AuthorityEpoch
		_, err = m.appendEvidence(t.Context(), tx, p, bearer, h.ID, first)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var active bool
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT aeon_work_status_begin()`).Scan(&active)
	}); err != nil || active {
		t.Fatalf("requires disabled derivation: active=%v err=%v", active, err)
	}
	pool, barrier, ctx := dbtest.BarrierPool(t, m.pool, func(q string) bool {
		return strings.Contains(q, "FROM stage_handoffs WHERE id=$1::uuid") && strings.Contains(q, "FOR UPDATE")
	})
	evidence := *m
	evidence.pool = pool
	second := EvidenceWrite{Sequence: 2, Kind: "credential_handoff", Outcome: "satisfied", ObservedAt: time.Now().UTC().Truncate(time.Microsecond), AuthorityEpoch: h.AuthorityEpoch, CredentialReady: &yes}
	evidenceDone := make(chan error, 1)
	go func() {
		evidenceDone <- db.InTenant(dbtest.Seed(ctx), pool, p.TenantID, func(tx pgx.Tx) error { _, err := evidence.appendEvidence(ctx, tx, p, bearer, h.ID, second); return err })
	}()
	holder := barrier.Wait(t, ctx)
	completionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	completionDone := make(chan error, 1)
	completed := make(chan struct{})
	result := ResultWrite{Outcome: "succeeded", TerminalSequence: 2, AuthorityEpoch: h.AuthorityEpoch, PrerequisiteSealSHA256: h.PrerequisiteSealSHA256}
	go func() {
		completionDone <- db.InTenant(dbtest.Seed(completionCtx), m.pool, p.TenantID, func(tx pgx.Tx) error { _, err := m.close(completionCtx, tx, p, bearer, h.ID, result); return err })
		close(completed)
	}()
	// Clean up cancelled competitors before unpausing the lock holder on failure.
	defer func() { cancel(); dbtest.Await(t, ctx, completed); barrier.Release() }()
	if lock := dbtest.BlockedOrDone(t, ctx, m.pool, holder, completed); lock == "" {
		t.Fatal("completion finished before competing evidence committed")
	}
	var statement string
	if err := m.pool.QueryRow(ctx, `SELECT query FROM pg_stat_activity WHERE wait_event_type='Lock' AND $1::int=ANY(pg_blocking_pids(pid))`, holder).Scan(&statement); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statement, "FROM tenants") || !strings.Contains(statement, "FOR NO KEY UPDATE") {
		t.Fatalf("completion acquired project/tree before evidence's fence, creating inverse order: %s", statement)
	}
	barrier.Release()
	if err := dbtest.Await(t, ctx, evidenceDone); err != nil {
		t.Fatalf("concurrent evidence: %v", err)
	}
	if err := dbtest.Await(t, ctx, completionDone); err != nil {
		t.Fatalf("concurrent completion: %v", err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		got, err := loadHandoff(ctx, tx, h.ID, false)
		if err != nil {
			return err
		}
		if got.Result == nil || !sameResult(got.Result, result) || got.State != "succeeded" {
			t.Fatalf("completion lost: %+v", got)
		}
		e, err := loadEvidence(ctx, tx, h.ID, 2)
		if err != nil {
			return err
		}
		if e.Kind != second.Kind || e.CredentialReady == nil || !*e.CredentialReady {
			t.Fatalf("evidence lost: %+v", e)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='stage_handoff.completed' AND after->>'handoff_id'=$1`, h.ID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("completion events=%d", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type handoffProjectTrace struct{ ready chan uint32 }

func (b handoffProjectTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "FROM journey_projects") && strings.Contains(q.SQL, "FOR ") {
		select {
		case b.ready <- conn.PgConn().PID():
		default:
		}
	}
	return ctx
}
func (handoffProjectTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A blocked evidence write must leave the handoff free for the transaction
// already holding its project. The old handoff -> project order fails NOWAIT.
func TestEvidenceLocksProjectBeforeHandoffWithDerivationDisabled(t *testing.T) {
	m, p, project, release, bearer := fixture(t)
	var h Handoff
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		h, err = m.create(t.Context(), tx, p, RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "prepare", ExpectedJourneyRevision: 1, IdempotencyKey: "project-order"}, "janus", []string{"authorization", "credential_handoff"}, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	writer, err := m.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err = writer.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true)`, p.TenantID); err != nil {
		t.Fatal(err)
	}
	var active bool
	if err = writer.QueryRow(ctx, `SELECT aeon_work_status_begin()`).Scan(&active); err != nil || active {
		t.Fatalf("requires disabled derivation: active=%v err=%v", active, err)
	}
	var holder int
	if err = writer.QueryRow(ctx, `SELECT pg_backend_pid() FROM journey_projects WHERE project_node_id=$1 FOR NO KEY UPDATE`, project).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	ready := make(chan uint32, 2)
	cfg := m.pool.Config()
	cfg.ConnConfig.Tracer = handoffProjectTrace{ready: ready}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	yes := true
	input := EvidenceWrite{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now().UTC().Truncate(time.Microsecond), AuthorityEpoch: h.AuthorityEpoch, Authorized: &yes}
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(dbtest.Seed(ctx), pool, p.TenantID, func(tx pgx.Tx) error { _, err := m.appendEvidence(ctx, tx, p, bearer, h.ID, input); return err })
	}()
	completed := false
	defer func() {
		cancel()
		_ = writer.Rollback(context.Background())
		if !completed {
			<-done
		}
	}()
	waiter := dbtest.Await(t, ctx, ready)
	if err = dbtest.WaitForBlocked(ctx, m.pool, int(waiter), holder, "FROM journey_projects"); err != nil {
		t.Fatal(err)
	}
	var id string
	if err = writer.QueryRow(ctx, `SELECT id::text FROM stage_handoffs WHERE id=$1 FOR UPDATE NOWAIT`, h.ID).Scan(&id); err != nil {
		t.Fatalf("evidence took handoff before project, blocking project-first completion: %v", err)
	}
	if id != h.ID {
		t.Fatal("locked wrong handoff")
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = dbtest.Await(t, ctx, done); err != nil {
		t.Fatalf("evidence did not commit after project barrier: %v", err)
	}
	completed = true
	if err = db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		e, err := loadEvidence(ctx, tx, h.ID, 1)
		if err == nil && (e.Kind != input.Kind || e.Authorized == nil || !*e.Authorized) {
			t.Fatalf("evidence lost: %+v", e)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCompletionAndJourneyShareTenantPairingTreeOrder(t *testing.T) {
	m, p, project, release, bearer := fixture(t)
	person := fixturePerson(t, m, p.TenantID)
	var h Handoff
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var active bool
		if err := tx.QueryRow(t.Context(), `SELECT aeon_work_status_begin()`).Scan(&active); err != nil {
			return err
		}
		if active {
			t.Fatal("requires disabled derivation")
		}
		var err error
		h, err = m.create(t.Context(), tx, p, RequestWrite{ProjectNodeID: project, ReleaseNodeID: release, Stage: "access", Operation: "prepare", ExpectedJourneyRevision: 1, IdempotencyKey: "completion-tree-order"}, "janus", []string{"authorization", "credential_handoff"}, "")
		if err != nil {
			return err
		}
		yes := true
		for _, e := range []EvidenceWrite{
			{Sequence: 1, Kind: "authorization", Outcome: "satisfied", ObservedAt: time.Now().UTC(), AuthorityEpoch: h.AuthorityEpoch, Authorized: &yes},
			{Sequence: 2, Kind: "credential_handoff", Outcome: "satisfied", ObservedAt: time.Now().UTC(), AuthorityEpoch: h.AuthorityEpoch, CredentialReady: &yes},
		} {
			if _, err := m.appendEvidence(t.Context(), tx, p, bearer, h.ID, e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pool, barrier, ctx := dbtest.BarrierPool(t, m.pool, func(q string) bool {
		return strings.Contains(q, "pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'")
	})
	completion := *m
	completion.pool = pool
	result := ResultWrite{Outcome: "succeeded", TerminalSequence: 2, AuthorityEpoch: h.AuthorityEpoch, PrerequisiteSealSHA256: h.PrerequisiteSealSHA256}
	completionDone := make(chan error, 1)
	go func() {
		completionDone <- db.InTenant(dbtest.Seed(ctx), pool, p.TenantID, func(tx pgx.Tx) error { _, err := completion.close(ctx, tx, p, bearer, h.ID, result); return err })
	}()
	holder := barrier.Wait(t, ctx)
	journeyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	mux := http.NewServeMux()
	journey.New(m.pool).Mount(mux)
	journeyDone := make(chan *httptest.ResponseRecorder, 1)
	finished := make(chan struct{})
	go func() {
		r := httptest.NewRequest(http.MethodPut, "/api/projects/"+project+"/journey/profile", strings.NewReader(`{"profile":"personal","expected_revision":1}`)).WithContext(tenant.WithPrincipal(journeyCtx, person))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		journeyDone <- w
		close(finished)
	}()
	defer func() { cancel(); dbtest.Await(t, ctx, finished); barrier.Release() }()
	if lock := dbtest.BlockedOrDone(t, ctx, m.pool, holder, finished); lock == "" {
		t.Fatal("journey did not wait for completion fence")
	}
	var statement string
	if err := m.pool.QueryRow(ctx, `SELECT query FROM pg_stat_activity WHERE wait_event_type='Lock' AND $1::int=ANY(pg_blocking_pids(pid))`, holder).Scan(&statement); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statement, "FROM tenants") || !strings.Contains(statement, "FOR NO KEY UPDATE") {
		t.Fatalf("journey acquired pairing before waiting on completion's tree: %s", statement)
	}
	var advisory int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND granted`, holder).Scan(&advisory); err != nil {
		t.Fatal(err)
	}
	if advisory != 2 {
		t.Fatalf("completion must hold pairing and tree fences, got %d", advisory)
	}
	barrier.Release()
	if err := dbtest.Await(t, ctx, completionDone); err != nil {
		t.Fatalf("completion: %v", err)
	}
	w := dbtest.Await(t, ctx, journeyDone)
	if w.Code != http.StatusOK {
		t.Fatalf("journey after completion: %d %s", w.Code, w.Body.String())
	}
	if err := db.InTenant(dbtest.Seed(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		got, err := loadHandoff(ctx, tx, h.ID, false)
		if err == nil && (got.Result == nil || !sameResult(got.Result, result)) {
			t.Fatalf("completion lost: %+v", got)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

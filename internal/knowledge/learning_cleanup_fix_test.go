// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// Risk: a tenant created after the release has no legacy row, so the first
// cleanup invents "now" as its cutoff and dismisses learnings people just made.
func TestLegacyCleanupPostReleaseTenant(t *testing.T) {
	f := setup(t)
	tagged := addNode(t, f, "POST-1", "work", "Tagged after the release", &f.project)
	setFields(t, f, tagged, map[string]any{"tags": []any{"process-learning"}})
	nominated := addNode(t, f, "POST-2", "work", "Nominated after the release", &f.project)
	comment := addLegacyComment(t, f, f.ticket, "#process-learning A lesson filed after the release.")
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET updated_at='2000-01-01' WHERE tenant_id=$1 AND id=$2::uuid`, f.a.TenantID, tagged); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO method_learning_nominations(tenant_id,source_key,project_id,node_id,origin,excerpt,source_hash,nominated_at) VALUES($1,$2,$3,$4,'closed_ticket','Old title',$5,'2000-01-01')`, f.a.TenantID, nodeLearningID(nominated), f.project, nominated, sourceHash("Old title")); err != nil {
		t.Fatal(err)
	}
	var foreignID string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields,updated_at) SELECT $1,'FOREIGN-9',id,'Old foreign title',$2::uuid,'{"tags":["process-learning"]}',timestamptz '2000-01-01' FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING id::text`, f.foreign.TenantID, f.elsewhere).Scan(&foreignID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO method_learning_legacy_cleanup(tenant_id,cutoff) VALUES($1,'2020-01-01') ON CONFLICT(tenant_id) DO UPDATE SET cutoff=EXCLUDED.cutoff, completed_at=NULL`, f.foreign.TenantID); err != nil {
		t.Fatal(err)
	}

	n, err := CleanupLegacyLearnings(t.Context(), f.db.App)
	if err != nil || n != 1 {
		t.Fatalf("cleanup=%d err=%v", n, err)
	}
	for _, key := range []string{nodeLearningID(tagged), nodeLearningID(nominated), commentLearningID(f.ticket, comment)} {
		if decisionOf(t, f, key) != "" {
			t.Fatalf("post-release tenant dismissed %s as legacy", key)
		}
	}
	var completed bool
	var after string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT completed_at IS NOT NULL, coalesce(after_key,'') FROM method_learning_legacy_cleanup WHERE tenant_id=$1`, f.a.TenantID).Scan(&completed, &after); err != nil {
		t.Fatal(err)
	}
	if !completed || after != "" {
		t.Fatalf("post-release cleanup row completed=%v after=%q", completed, after)
	}
	var foreignDecisions int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM method_learning_decisions WHERE tenant_id=$1 AND source_key=$2`, f.foreign.TenantID, nodeLearningID(foreignID)).Scan(&foreignDecisions); err != nil {
		t.Fatal(err)
	}
	if foreignDecisions != 1 {
		t.Fatalf("release-era tenant decisions=%d", foreignDecisions)
	}
	again, err := CleanupLegacyLearnings(t.Context(), f.db.App)
	if err != nil || again != 0 || decisionOf(t, f, nodeLearningID(tagged)) != "" {
		t.Fatalf("second pass=%d err=%v", again, err)
	}
}

// Risk: each legacy item rechecks eligibility by scanning every comment and tag
// event in the tenant. The recheck has to use that one node or comment.
func TestLegacyCleanupItemRecheckIsKeyed(t *testing.T) {
	f := setup(t)
	tagged := addNode(t, f, "KEY-1", "work", "Old tagged title", &f.project)
	setFields(t, f, tagged, map[string]any{"tags": []any{"process-learning"}})
	nominated := addNode(t, f, "KEY-2", "work", "Nominated only", &f.project)
	fresh := addNode(t, f, "KEY-3", "work", "After the cutoff", &f.project)
	setFields(t, f, fresh, map[string]any{"tags": []any{"process-learning"}})
	comment := addLegacyComment(t, f, f.ticket, "#process-learning Old ticket comment.")
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET updated_at='2000-01-01' WHERE tenant_id=$1 AND id=$2::uuid`, f.a.TenantID, tagged); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO method_learning_nominations(tenant_id,source_key,project_id,node_id,origin,excerpt,source_hash,nominated_at) VALUES($1,$2,$3,$4,'closed_ticket','Old title',$5,'2000-01-01')`, f.a.TenantID, nodeLearningID(nominated), f.project, nominated, sourceHash("Old title")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO method_learning_legacy_cleanup(tenant_id,cutoff) VALUES($1,'2020-01-01') ON CONFLICT(tenant_id) DO UPDATE SET cutoff=EXCLUDED.cutoff, completed_at=NULL`, f.a.TenantID); err != nil {
		t.Fatal(err)
	}

	trace := &legacyQueryTrace{}
	cfg := f.db.App.Config().Copy()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	n, err := CleanupLegacyLearnings(t.Context(), pool)
	if err != nil || n != 3 {
		t.Fatalf("cleanup=%d err=%v", n, err)
	}
	for _, key := range []string{nodeLearningID(tagged), nodeLearningID(nominated), commentLearningID(f.ticket, comment)} {
		if decisionOf(t, f, key) != "dismissed" {
			t.Fatalf("missed legacy source %s", key)
		}
	}
	if decisionOf(t, f, nodeLearningID(fresh)) != "" {
		t.Fatal("recheck dismissed a source created after the cutoff")
	}
	calls := trace.snapshot()
	unionRechecks := 0
	var sawNomination, sawNode, sawComment bool
	var nodeQuery pgx.TraceQueryStartData
	for _, call := range calls {
		if strings.Contains(call.SQL, "UNION") && strings.Contains(call.SQL, "source_key=$3") {
			unionRechecks++
		}
		if strings.Contains(call.SQL, "m.tenant_id=$1 AND m.source_key=$2 AND m.nominated_at<$3") {
			sawNomination = true
		}
		if strings.Contains(call.SQL, "n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL AND n.project_id IS NOT NULL") {
			sawNode = true
			nodeQuery = call
		}
		if strings.Contains(call.SQL, "c.tenant_id=$1 AND c.node_id=$2::uuid AND c.id=$3::bigint AND c.type='comment.created'") {
			sawComment = true
		}
	}
	if unionRechecks != 0 || !sawNomination || !sawNode || !sawComment {
		t.Fatalf("per-item recheck union=%d nomination=%v node=%v comment=%v", unionRechecks, sawNomination, sawNode, sawComment)
	}
	if !legacyPlanUsesIndex(t, f, nodeQuery) {
		t.Fatal("node recheck has no index plan")
	}
}

// Risk: the daily tagger skips nomination whenever legacy cleanup returns an error.
func TestLegacyCleanupFailureStillTags(t *testing.T) {
	f := setup(t)
	incident := addNode(t, f, "INC-1330", "work", "INCIDENT: rollback failed", &f.project)
	for _, tid := range []string{f.a.TenantID, f.foreign.TenantID} {
		if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO method_learning_legacy_cleanup(tenant_id,cutoff) VALUES($1,'2020-01-01') ON CONFLICT(tenant_id) DO UPDATE SET cutoff=EXCLUDED.cutoff, completed_at=NULL`, tid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Admin.Exec(t.Context(), `ALTER TABLE method_learning_legacy_cleanup DROP COLUMN cutoff`); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		NewTagger(f.db.App).Run(ctx)
		close(stopped)
	}()
	deadline := time.Now().Add(20 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		if _, _, ok := nominationOfKey(t, f, nodeLearningID(incident)); ok {
			found = true
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("tagger did not stop")
	}
	if !found {
		t.Fatal("tagger did not nominate after legacy cleanup failed")
	}
	var completed bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT completed_at IS NOT NULL FROM method_learning_legacy_cleanup WHERE tenant_id=$1`, f.a.TenantID).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed {
		t.Fatal("cleanup finished despite the failed scan")
	}
}

type legacyQueryTrace struct {
	mu    sync.Mutex
	calls []pgx.TraceQueryStartData
}

func (tr *legacyQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tr.mu.Lock()
	tr.calls = append(tr.calls, data)
	tr.mu.Unlock()
	return ctx
}

func (*legacyQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (tr *legacyQueryTrace) snapshot() []pgx.TraceQueryStartData {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]pgx.TraceQueryStartData(nil), tr.calls...)
}

func legacyPlanUsesIndex(t *testing.T, f fixture, call pgx.TraceQueryStartData) bool {
	t.Helper()
	tx, err := f.db.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err := tx.Exec(t.Context(), `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(t.Context(), `EXPLAIN (FORMAT TEXT) `+call.SQL, call.Args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	text := plan.String()
	if strings.Contains(text, "Seq Scan") {
		t.Fatalf("keyed recheck seq scan:\n%s", text)
	}
	return strings.Contains(text, "Index Scan") || strings.Contains(text, "Index Only Scan") || strings.Contains(text, "Bitmap Index Scan")
}

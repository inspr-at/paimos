// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func bulkRequest(f *fixture, p tenant.Principal, in attentionBulkRequest, key string, want int) *httptest.ResponseRecorder {
	f.t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		f.t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/status-autopilot/attention/bulk", strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(f.t.Context(), p))
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != want {
		f.t.Fatalf("bulk %d: %s; want %d", w.Code, w.Body, want)
	}
	return w
}
func bulkPreview(f *fixture, p tenant.Principal, action string, scope attentionScope) (attentionBulkRequest, attentionPreview) {
	f.t.Helper()
	dry := true
	in := attentionBulkRequest{Action: action, Scope: scope, DryRun: &dry, Exclude: []string{}}
	var out attentionPreview
	if err := json.Unmarshal(bulkRequest(f, p, in, "", 200).Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	dry = false
	in.Through = out.Through
	in.PreviewToken = out.Token
	return in, out
}
func bulkRun(f *fixture, p tenant.Principal, in attentionBulkRequest, key string) attentionBulkResult {
	f.t.Helper()
	var out attentionBulkResult
	if err := json.Unmarshal(bulkRequest(f, p, in, key, 200).Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}
func bulkUndo(f *fixture, p tenant.Principal, id string, want int) attentionBulkResult {
	f.t.Helper()
	w := f.call(p, "POST", "/api/status-autopilot/attention/bulk/"+id+"/undo", "", want)
	var out attentionBulkResult
	if want == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			f.t.Fatal(err)
		}
	}
	return out
}
func bulkAdd(f *fixture, count int) {
	f.t.Helper()
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(f.t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id,created_at,updated_at)
 SELECT $1,k.id,'AUT-'||(i+1),'Triage item '||i,'new',$2,$3,$3 FROM node_kinds k CROSS JOIN generate_series(1,$4::int) i WHERE k.tenant_id=$1 AND k.slug='work'`, f.p.TenantID, f.project, f.now.Add(-8*24*time.Hour), count)
		return err
	})
}
func bulkSkipCount(out []attentionSkipped, reason string) int {
	for _, s := range out {
		if s.Reason == reason {
			return s.Count
		}
	}
	return 0
}

// Risk: group counts leak hidden projects or disagree with row eligibility;
// kind/time pagination duplicates or loses rows at a kind boundary.
func TestAttentionGroupsAndKindTimePagination(t *testing.T) {
	f := setup(t)
	bulkAdd(f, 53)
	f.add("AUT-100", "work", "blocked", 15, nil)
	f.add("AUT-101", "work", "backlog", 91, nil)
	primary := f.project
	f.project = f.add("AUT-200", "project", "open", 0, nil)
	f.add("AUT-201", "work", "new", 8, nil)
	f.project = primary
	f.run(f.now)
	member := attentionMember(f)
	var groups attentionGroupsPage
	if err := json.Unmarshal(f.call(member, "GET", "/api/status-autopilot/attention/groups?by=project", "", 200).Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	if groups.Total != 55 || len(groups.Groups) != 1 || groups.Groups[0].Total != 55 || groups.Groups[0].Editable != 55 || groups.Groups[0].Applicable != 55 || groups.Groups[0].ProjectID != primary || groups.Groups[0].CanManage || groups.Groups[0].OverrideMode != "inherit" {
		t.Fatalf("group scope/eligibility %+v", groups)
	}
	if err := json.Unmarshal(f.call(member, "GET", "/api/status-autopilot/attention/groups?by=kind", "", 200).Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups.Groups) != 3 || groups.Groups[0].Kind != "triage" || groups.Groups[1].Kind != "cancel" || groups.Groups[2].Kind != "blocked" {
		t.Fatalf("kind groups %+v", groups)
	}
	first := attentionRead(f, member, "")
	if first.Next == nil {
		t.Fatal("missing cursor")
	}
	second := attentionRead(f, member, "?after="+*first.Next)
	all := append(first.Items, second.Items...)
	seen := map[int64]bool{}
	for i, item := range all {
		if seen[item.EventID] {
			t.Fatal("duplicate cursor item")
		}
		seen[item.EventID] = true
		if i > 0 {
			before := all[i-1]
			if attentionKindOrder(item.Kind) < attentionKindOrder(before.Kind) || item.Kind == before.Kind && (item.At.After(before.At) || item.At.Equal(before.At) && item.EventID > before.EventID) {
				t.Fatal("not sorted by kind/newest event")
			}
		}
	}
	if len(all) != 55 || second.Next != nil {
		t.Fatalf("incomplete pages %d", len(all))
	}
	// In-flight cursors from the old node/event order retain an opaque position.
	last := first.Items[len(first.Items)-1]
	legacy := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s/%d", last.NodeID, last.EventID)))
	old := attentionRead(f, member, "?after="+legacy)
	if len(old.Items) != len(second.Items) || old.Items[0].EventID != second.Items[0].EventID {
		t.Fatal("legacy cursor position changed")
	}
	f.call(member, "GET", "/api/status-autopilot/attention/groups?by=bad", "", 400)
}

// Risk: large groups silently stop at the row-selection cap, double click
// repeats writes, audit loses batch attribution, or batch undo restores stale data.
func TestAttentionBulkBeyondSelectionCapReplayAuditAndPartialUndo(t *testing.T) {
	f := setup(t)
	bulkAdd(f, 120)
	f.run(f.now)
	member := attentionMember(f)
	in, preview := bulkPreview(f, member, "apply", attentionScope{ProjectID: f.project})
	if preview.Total != 120 || preview.Truncated || len(preview.Moves) != 1 || preview.Moves[0].Count != 120 || len(preview.Moves[0].SampleKeys) != 4 {
		t.Fatalf("preview %+v", preview)
	}
	result := bulkRun(f, member, in, "apply-120")
	if !result.Completed || result.Changed != 120 || len(result.Failed) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("run %+v", result)
	}
	again := bulkRun(f, member, in, "apply-120")
	if result.Changed != again.Changed || result.BatchID != again.BatchID {
		t.Fatalf("replay %+v", again)
	}
	f.tx(func(tx pgx.Tx) error {
		var summaries, items int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='status_autopilot.attention_bulk'),count(*) FILTER(WHERE type='status_autopilot.attention_apply') FROM events WHERE metadata->>'batch_id'=$1`, result.BatchID).Scan(&summaries, &items); err != nil {
			return err
		}
		if summaries != 1 || items != 120 {
			return fmt.Errorf("audit summaries=%d items=%d", summaries, items)
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Edited after bulk',updated_at=clock_timestamp() WHERE key='AUT-2'`)
		return err
	})
	undone := bulkUndo(f, member, result.BatchID, 200)
	if !undone.Completed || undone.Changed != 119 || len(undone.Failed) != 1 || undone.Failed[0].Key != "AUT-2" || !strings.Contains(undone.Failed[0].Error, "changed") {
		t.Fatalf("undo %+v", undone)
	}
	if got := bulkUndo(f, member, result.BatchID, 200); got.Changed != 119 || len(got.Failed) != 1 {
		t.Fatalf("undo replay %+v", got)
	}
	if got := attentionRead(f, member, ""); got.Total != 119 {
		t.Fatalf("undo queue %+v", got)
	}
}

// Risk: run expands a frozen preview, ignores exclusions or applies an item
// that changed since preview; actor/scope and idempotency binding fail open.
func TestAttentionBulkFrozenPreviewExclusionsAndOwnership(t *testing.T) {
	f := setup(t)
	changed := f.add("AUT-2", "work", "new", 8, nil)
	excluded := f.add("AUT-3", "work", "backlog", 91, nil)
	f.run(f.now)
	member := attentionMember(f)
	in, preview := bulkPreview(f, member, "apply", attentionScope{ProjectID: f.project})
	if preview.Total != 2 {
		t.Fatalf("preview %+v", preview)
	}
	bulkRequest(f, f.p, in, "stolen-preview", 404)
	altered := in
	altered.Action = "dismiss"
	bulkRequest(f, member, altered, "altered", 409)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET title='New revision',updated_at=clock_timestamp() WHERE id=$1`, changed)
		return err
	})
	fresh := f.add("AUT-4", "work", "new", 8, nil)
	f.run(f.now)
	in.Exclude = []string{"cancel"}
	out := bulkRun(f, member, in, "frozen")
	if out.Changed != 0 || len(out.Failed) != 0 || bulkSkipCount(out.Skipped, "Changed since the preview.") != 1 || bulkSkipCount(out.Skipped, "Excluded from the preview.") != 1 || !out.Completed {
		t.Fatalf("frozen run %+v", out)
	}
	if f.state(fresh).State != "new" || f.state(excluded).State != "backlog" || f.state(changed).State != "new" {
		t.Fatal("frozen/excluded item changed")
	}
	altered = in
	altered.Exclude = []string{}
	bulkRequest(f, member, altered, "frozen", 409)
	different, _ := bulkPreview(f, member, "dismiss", attentionScope{ProjectID: f.project})
	bulkRequest(f, member, different, "frozen", 409)
}

// Risk: over-1000 previews overclaim coverage or overflow bounded storage;
// expired batches and cross-tenant previews remain usable.
func TestAttentionBulkCapExpiryAndTenantIsolation(t *testing.T) {
	f := setup(t)
	bulkAdd(f, 1001)
	f.run(f.now)
	in, preview := bulkPreview(f, f.p, "dismiss", attentionScope{ProjectID: f.project})
	if preview.Total != 1001 || !preview.Truncated || preview.Limit != 1000 || len(preview.Moves) != 1 || preview.Moves[0].Count != 1000 {
		t.Fatalf("cap %+v", preview)
	}
	f.tx(func(tx pgx.Tx) error {
		var snapshot attentionSnapshot
		if err := tx.QueryRow(t.Context(), `SELECT snapshot FROM attention_batches WHERE id=$1`, in.PreviewToken).Scan(&snapshot); err != nil {
			return err
		}
		if len(snapshot.Items) != 1000 {
			return fmt.Errorf("unbounded snapshot %d", len(snapshot.Items))
		}
		_, err := tx.Exec(t.Context(), `UPDATE attention_batches SET created_at=clock_timestamp()-interval '25 hours' WHERE id=$1`, in.PreviewToken)
		return err
	})
	bulkRequest(f, f.p, in, "expired", 404)
	bulkUndo(f, f.p, in.PreviewToken, 404)
	other := "10000000-0000-4000-8000-000000000099"
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, other, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM attention_batches`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("cross tenant leak %d", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Risk: Apply all covers only triage or lets release membership and undo lose
// their revision guards when several items share the same planning release.
func TestAttentionBulkAllKindsAndReleaseUndo(t *testing.T) {
	f := setup(t)
	f.add("AUT-2", "work", "new", 8, nil)
	f.add("AUT-3", "work", "backlog", 91, nil)
	f.add("AUT-4", "work", "blocked", 15, nil)
	first := f.add("AUT-5", "work", "done", 15, nil)
	second := f.add("AUT-6", "work", "done", 15, nil)
	release := planningAttentionRelease(f)
	f.merge(first, 15)
	f.merge(second, 15)
	f.run(f.now)
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='planning',released_at=NULL,version=NULL,version_scheme=NULL WHERE release_node_id=$1`, release); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE journey_projects SET current_release_node_id=$1 WHERE project_node_id=$2`, release, f.project)
		return err
	})
	in, preview := bulkPreview(f, f.p, "apply", attentionScope{ProjectID: f.project})
	if preview.Total != 5 || len(preview.Moves) != 4 || len(preview.Skipped) != 0 {
		t.Fatalf("all kinds preview %+v", preview)
	}
	out := bulkRun(f, f.p, in, "all-kinds")
	if out.Changed != 5 || len(out.Skipped) != 0 || len(out.Failed) != 0 {
		t.Fatalf("all kinds %+v", out)
	}
	undo := bulkUndo(f, f.p, out.BatchID, 200)
	if undo.Changed != 5 || len(undo.Failed) != 0 {
		t.Fatalf("release undo %+v", undo)
	}
	if got := attentionRead(f, f.p, ""); got.Total != 5 {
		t.Fatalf("restored kinds %+v", got)
	}
}

// Risk: losing authority while the bulk queues still commits; concurrent
// requests in another Module bypass the scope fence. A query barrier proves
// the first request actually owns that fence while the second runs.
func TestAttentionBulkScopeConcurrencyAndFinalAuthorityFence(t *testing.T) {
	f := setup(t)
	id := f.add("AUT-2", "work", "new", 8, nil)
	f.run(f.now)
	member := attentionMember(f)
	in, _ := bulkPreview(f, member, "apply", attentionScope{ProjectID: f.project})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	blocker, err := f.d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true)`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(f.d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	probe := attentionFenceProbe{reached: make(chan struct{}, 1)}
	config.ConnConfig.Tracer = probe
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	raw, _ := json.Marshal(in)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/api/status-autopilot/attention/bulk", strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(ctx, member))
		r.Header.Set("Idempotency-Key", "queued")
		w := httptest.NewRecorder()
		New(pool).attentionBulk(w, r)
		done <- w
	}()
	select {
	case <-probe.reached:
	case <-ctx.Done():
		t.Fatal("bulk never reached final authority fence")
	}
	blocked := bulkRequest(f, member, in, "concurrent", 409)
	if !strings.Contains(blocked.Body.String(), attentionBulkConflict) {
		t.Fatalf("wrong conflict: %s", blocked.Body)
	}
	if _, err = blocker.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, member.ID); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != 403 || !strings.Contains(w.Body.String(), "permission denied") {
			t.Fatalf("revocation %d %s", w.Code, w.Body)
		}
	case <-ctx.Done():
		t.Fatal("bulk writer hung")
	}
	if f.state(id).State != "new" {
		t.Fatal("revoked writer committed")
	}
}

// Risk: another project editor can undo someone else's run by guessing a batch
// id, or persisted previews discard authentication admission guards.
func TestAttentionBulkUndoOwnershipAndAuthenticationGuard(t *testing.T) {
	f := setup(t)
	f.add("AUT-2", "work", "new", 8, nil)
	f.run(f.now)
	in, _ := bulkPreview(f, f.p, "dismiss", attentionScope{ProjectID: f.project})
	out := bulkRun(f, f.p, in, "owned")
	member := attentionMember(f)
	bulkUndo(f, member, out.BatchID, 404)
	// The caller's authentication guard must run on the first final transaction.
	called := false
	raw, _ := json.Marshal(in)
	ctx := db.WithTenantGuard(tenant.WithPrincipal(t.Context(), f.p), func(context.Context, pgx.Tx, string) error { called = true; return fmt.Errorf("admission denied") })
	r := httptest.NewRequest("POST", "/api/status-autopilot/attention/bulk", strings.NewReader(string(raw))).WithContext(ctx)
	r.Header.Set("Idempotency-Key", "owned")
	w := httptest.NewRecorder()
	f.m.attentionBulk(w, r)
	if !called || w.Code != 500 {
		t.Fatalf("authentication guard ignored: called=%v code=%d", called, w.Code)
	}
}

// Risk: paused proposals are applied anyway, groups overstate agent release
// rights, or dismiss is incorrectly blocked by apply-only eligibility checks.
func TestAttentionBulkUnavailableProposalsAndAgentReleaseRights(t *testing.T) {
	t.Setenv("AEON_STATUS_AUTOPILOT", "suggest")
	f := setup(t)
	id := f.add("AUT-2", "work", "in_progress", 4, nil)
	f.run(f.now)
	t.Setenv("AEON_STATUS_AUTOPILOT", "off")
	in, preview := bulkPreview(f, f.p, "apply", attentionScope{ProjectID: f.project})
	if len(preview.Moves) != 0 || bulkSkipCount(preview.Skipped, "The server operator has paused status autopilot.") != 1 {
		t.Fatalf("paused preview %+v", preview)
	}
	out := bulkRun(f, f.p, in, "paused")
	if out.Changed != 0 || len(out.Failed) != 0 || len(out.Skipped) != 1 || f.state(id).State != "in_progress" {
		t.Fatalf("paused run %+v", out)
	}
	in, preview = bulkPreview(f, f.p, "dismiss", attentionScope{ProjectID: f.project})
	if len(preview.Moves) != 1 || len(preview.Skipped) != 0 {
		t.Fatalf("paused dismiss preview %+v", preview)
	}
	out = bulkRun(f, f.p, in, "dismiss-paused")
	if out.Changed != 1 {
		t.Fatalf("paused dismiss %+v", out)
	}
	if undo := bulkUndo(f, f.p, out.BatchID, 200); undo.Changed != 1 || len(undo.Failed) != 0 {
		t.Fatalf("proposal undo %+v", undo)
	}
	t.Setenv("AEON_STATUS_AUTOPILOT", "on")
	missed := f.add("AUT-3", "work", "done", 15, nil)
	release := planningAttentionRelease(f)
	f.merge(missed, 15)
	f.run(f.now)
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='planning',released_at=NULL,version=NULL,version_scheme=NULL WHERE release_node_id=$1`, release); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE journey_projects SET current_release_node_id=$1 WHERE project_node_id=$2`, release, f.project)
		return err
	})
	agent := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Agent, Name: "Bulk agent", Scopes: []string{"nodes.read", "nodes.write", "events.undo"}}
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Bulk agent') RETURNING id::text`, f.p.TenantID).Scan(&agent.ID)
	})
	dbtest.BindRole(t, f.d, agent.TenantID, agent.ID, "owner")
	in, preview = bulkPreview(f, agent, "apply", attentionScope{ProjectID: f.project, Kind: "missed"})
	if preview.Total != 1 || len(preview.Moves) != 0 || bulkSkipCount(preview.Skipped, "Adding to a release needs permission to manage releases.") != 1 {
		t.Fatalf("agent release preview %+v", preview)
	}
	out = bulkRun(f, agent, in, "agent-release")
	if out.Changed != 0 || !f.state(missed).Marks["missed_release"] {
		t.Fatalf("agent release run %+v", out)
	}
	var groups attentionGroupsPage
	if err := json.Unmarshal(f.call(agent, "GET", "/api/status-autopilot/attention/groups?by=kind&kind=missed", "", 200).Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups.Groups) != 1 || groups.Groups[0].Applicable != 0 || groups.Groups[0].Editable != 1 {
		t.Fatalf("agent group rights %+v", groups)
	}
}

// Risk: a disconnect after an item commits loses its durable progress, so an
// idempotent retry either repeats the item or cannot finish the batch audit.
// The query-end barrier cancels only after PostgreSQL has accepted the item
// commit; it never relies on a delay or on elapsed time.
type attentionCancelAfterItemCommit struct {
	cancel  context.CancelFunc
	commits int
}

func (p *attentionCancelAfterItemCommit) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (p *attentionCancelAfterItemCommit) TraceQueryEnd(_ context.Context, _ *pgx.Conn, q pgx.TraceQueryEndData) {
	if q.Err == nil && q.CommandTag.String() == "COMMIT" {
		p.commits++
		if p.commits == 2 {
			p.cancel()
		}
	}
}
func TestAttentionBulkInterruptedCommitResumesWithoutRepeating(t *testing.T) {
	f := setup(t)
	id := f.add("AUT-2", "work", "new", 8, nil)
	f.run(f.now)
	in, _ := bulkPreview(f, f.p, "apply", attentionScope{ProjectID: f.project})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	config, err := pgxpool.ParseConfig(f.d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	probe := &attentionCancelAfterItemCommit{cancel: cancel}
	config.ConnConfig.Tracer = probe
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	raw, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "/api/status-autopilot/attention/bulk", strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(ctx, f.p))
	r.Header.Set("Idempotency-Key", "interrupted")
	w := httptest.NewRecorder()
	New(pool).attentionBulk(w, r)
	var partial attentionBulkResult
	if err = json.Unmarshal(w.Body.Bytes(), &partial); err != nil {
		t.Fatal(err)
	}
	if probe.commits != 2 || partial.Completed || len(partial.Failed) == 0 || f.state(id).State != "backlog" {
		t.Fatalf("commit barrier/progress: commits=%d response=%s", probe.commits, w.Body)
	}
	final := bulkRun(f, f.p, in, "interrupted")
	if !final.Completed || final.Changed != 1 || len(final.Failed) != 0 || len(final.Skipped) != 0 {
		t.Fatalf("recovered result %+v", final)
	}
	f.tx(func(tx pgx.Tx) error {
		var changes, summaries int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='status_autopilot.attention_apply'),count(*) FILTER(WHERE type='status_autopilot.attention_bulk') FROM events WHERE metadata->>'batch_id'=$1`, final.BatchID).Scan(&changes, &summaries); err != nil {
			return err
		}
		if changes != 1 || summaries != 1 {
			return fmt.Errorf("repeated commit or missing audit: changes=%d summaries=%d", changes, summaries)
		}
		return nil
	})
	if undone := bulkUndo(f, f.p, final.BatchID, 200); undone.Changed != 1 || len(undone.Failed) != 0 || f.state(id).State != "new" {
		t.Fatalf("recovered undo %+v", undone)
	}
}

// Risk: cleanup clears genuine release reminders, changes status/other fields,
// repeats writes, or cannot be undone through the normal bulk receipt.
func TestInvalidMissedReleaseCleanupReplayAndUndo(t *testing.T) {
	f := setup(t)
	classicID := f.add("AUT-1147", "work", "done", 100, nil)
	unmerged := f.add("AUT-1148", "work", "done", 100, nil)
	merged := f.add("AUT-1149", "work", "done", 100, nil)
	f.merge(merged, 20)
	for _, id := range []string{classicID, unmerged, merged} {
		f.tx(func(tx pgx.Tx) error {
			fields := `{"note":"preserved"}`
			if id == classicID {
				fields = `{"classic":{},"note":"preserved"}`
			}
			if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2,status_autopilot='{"blocked_reminder":true}' WHERE id=$1`, id, fields); err != nil {
				return err
			}
			c, _, err := loadCandidate(t.Context(), tx, id, f.now)
			if err != nil {
				return err
			}
			return apply(t.Context(), tx, f.p, c.Node, decision{Rule: "done", Flag: "missed_release", Anchor: "old-done-anchor", Reason: "Old rule without merge evidence"})
		})
	}
	in, preview := bulkPreview(f, f.p, "dismiss", attentionScope{Kind: "invalid_missed"})
	if preview.Total != 2 {
		t.Fatalf("cleanup preview: %+v", preview)
	}
	out := bulkRun(f, f.p, in, "missed-release-cleanup")
	if !out.Completed || out.Changed != 2 || len(out.Failed) != 0 {
		t.Fatalf("cleanup: %+v", out)
	}
	if replay := bulkRun(f, f.p, in, "missed-release-cleanup"); replay.Changed != 2 || replay.BatchID != out.BatchID {
		t.Fatalf("replay: %+v", replay)
	}
	for _, id := range []string{classicID, unmerged} {
		n := f.state(id)
		if n.State != "done" || n.Marks["missed_release"] || !n.Marks["blocked_reminder"] || fieldString(n, "note") != "preserved" {
			t.Fatalf("cleanup changed ticket: %+v", n)
		}
	}
	if !f.state(merged).Marks["missed_release"] {
		t.Fatal("genuine merged flag cleared")
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE metadata->>'batch_id'=$1 AND type='status_autopilot.attention_dismiss' AND metadata->>'reason' LIKE 'Cleared incorrect missed-release flag:%'`, out.BatchID).Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			t.Fatalf("per-ticket audit count: %d", count)
		}
		return nil
	})
	_, again := bulkPreview(f, f.p, "dismiss", attentionScope{Kind: "invalid_missed"})
	if again.Total != 0 {
		t.Fatalf("cleanup not idempotent: %+v", again)
	}
	undone := bulkUndo(f, f.p, out.BatchID, 200)
	if undone.Changed != 2 || !undone.Completed || len(undone.Failed) != 0 {
		t.Fatalf("cleanup undo: %+v", undone)
	}
	for _, id := range []string{classicID, unmerged} {
		if !f.state(id).Marks["missed_release"] {
			t.Fatal("undo did not restore flag")
		}
	}
	// A fresh merge between preview and mutation must preserve its flag even
	// when the fixture does not advance the ticket revision.
	fresh, _ := bulkPreview(f, f.p, "dismiss", attentionScope{Kind: "invalid_missed", Q: "AUT-1148"})
	f.merge(unmerged, 20)
	guarded := bulkRun(f, f.p, fresh, "cleanup-fresh-merge")
	if guarded.Changed != 0 || len(guarded.Failed) != 1 || !f.state(unmerged).Marks["missed_release"] {
		t.Fatalf("new merge was cleared: %+v", guarded)
	}
}

// Risk: a missing planning release strands the row, or Apply reports success
// without writing the content-work mark and preserving its reversible fields.
func TestAttentionWithoutPlanningReleaseMarksNoReleaseNeededAndUndoes(t *testing.T) {
	f := setup(t)
	id := f.add("AUT-1150", "work", "done", 20, nil)
	f.merge(id, 20)
	f.run(f.now)
	items := attentionRead(f, f.p, "").Items
	if len(items) != 1 || items[0].To != "no_release_needed" || !items[0].Applicable {
		t.Fatalf("no release action: %+v", items)
	}
	in, preview := bulkPreview(f, f.p, "apply", attentionScope{Kind: "missed"})
	if len(preview.Moves) != 1 || preview.Moves[0].To != "no_release_needed" || len(preview.Skipped) != 0 {
		t.Fatalf("fallback preview: %+v", preview)
	}
	out := bulkRun(f, f.p, in, "no-release-needed")
	if out.Changed != 1 || len(out.Failed) != 0 {
		t.Fatalf("fallback write: %+v", out)
	}
	if n := f.state(id); !noReleaseNeeded(n) || n.State != "done" || n.Marks["missed_release"] {
		t.Fatalf("false fallback result: %+v", n)
	}
	undone := bulkUndo(f, f.p, out.BatchID, 200)
	if undone.Changed != 1 || len(undone.Failed) != 0 {
		t.Fatalf("fallback undo: %+v", undone)
	}
	if n := f.state(id); noReleaseNeeded(n) || !n.Marks["missed_release"] || n.State != "done" {
		t.Fatalf("fallback undo fields: %+v", n)
	}
	planningAttentionRelease(f)
	captured := attentionRead(f, f.p, "").Items[0].attentionInput
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_projects SET current_release_node_id=NULL WHERE project_node_id=$1`, f.project)
		return err
	})
	stale := attentionWrite(f, f.p, "apply", captured)[0]
	if stale.OK || !strings.Contains(stale.Error, "changed") || noReleaseNeeded(f.state(id)) {
		t.Fatalf("release action silently became content marking: %+v", stale)
	}
}

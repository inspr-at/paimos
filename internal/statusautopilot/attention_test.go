// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func attentionRead(f *fixture, p tenant.Principal, path string) attentionPage {
	f.t.Helper()
	var out attentionPage
	if err := json.Unmarshal(f.call(p, "GET", "/api/status-autopilot/attention"+path, "", 200).Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}
func attentionWrite(f *fixture, p tenant.Principal, action string, items ...attentionInput) []attentionResult {
	f.t.Helper()
	body, err := json.Marshal(map[string]any{"action": action, "items": items})
	if err != nil {
		f.t.Fatal(err)
	}
	var out struct {
		Items []attentionResult `json:"items"`
	}
	if err = json.Unmarshal(f.call(p, "POST", "/api/status-autopilot/attention/actions", string(body), 200).Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	if len(out.Items) != len(items) {
		f.t.Fatalf("incomplete response: %+v", out)
	}
	return out.Items
}
func attentionMember(f *fixture) tenant.Principal {
	f.t.Helper()
	p := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Person, Name: "Project editor"}
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(f.t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Project editor') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		_, err := tx.Exec(f.t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, p.TenantID, p.ID, f.project)
		return err
	})
	return p
}
func TestAttentionPaginationFiltersAndProjectVisibility(t *testing.T) {
	f := setup(t)
	for i := 0; i < 53; i++ {
		f.add(fmt.Sprintf("AUT-%d", i+2), "work", "new", 8, nil)
	}
	second := f.add("AUT-100", "project", "open", 0, nil)
	primary := f.project
	f.project = second
	hidden := f.add("AUT-101", "work", "backlog", 91, nil)
	f.project = primary
	f.run(f.now)
	editor := attentionMember(f)
	first := attentionRead(f, editor, "")
	if first.Total != 53 || len(first.Items) != 50 || first.Next == nil || first.Counts["triage"] != 53 || len(first.Facets.Projects) != 1 {
		t.Fatalf("page/count/scope: %+v", first)
	}
	ids := map[string]bool{}
	for _, item := range first.Items {
		if !item.Editable || !item.Applicable || item.To != "backlog" || item.NodeID == hidden {
			t.Fatalf("wrong projection %+v", item)
		}
		ids[item.NodeID] = true
	}
	last := attentionRead(f, editor, "?after="+*first.Next)
	if len(last.Items) != 3 || last.Next != nil || last.Total != 53 {
		t.Fatalf("last page %+v", last)
	}
	for _, item := range last.Items {
		if ids[item.NodeID] {
			t.Fatal("duplicate keyset row")
		}
	}
	all := attentionRead(f, f.p, "")
	if all.Total != 54 || all.Counts["cancel"] != 1 {
		t.Fatalf("cross-project owner %+v", all)
	}
	filtered := attentionRead(f, f.p, "?kind=cancel&project_id="+second+"&q=AUT-101")
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].NodeID != hidden || filtered.Items[0].To != "cancelled" {
		t.Fatalf("filters %+v", filtered)
	}
	f.call(editor, "GET", "/api/status-autopilot/attention?after=bad", "", 400)
}
func TestAttentionApplyDismissUndoAndSuppression(t *testing.T) {
	f := setup(t)
	applyID := f.add("AUT-2", "work", "new", 8, nil)
	dismissID := f.add("AUT-3", "work", "backlog", 91, nil)
	f.run(f.now)
	editor := attentionMember(f)
	items := attentionRead(f, editor, "").Items
	for _, item := range items {
		action := "apply"
		if item.NodeID == dismissID {
			action = "dismiss"
		}
		result := attentionWrite(f, editor, action, item.attentionInput)[0]
		if !result.OK || result.Resolution == 0 || result.Revision == nil {
			t.Fatalf("resolution %+v", result)
		}
		state := f.state(item.NodeID)
		if item.NodeID == applyID && state.State != "backlog" || item.NodeID == dismissID && state.State != "backlog" || len(state.Marks) != 0 {
			t.Fatalf("wrong mutation %+v", state)
		}
		if again := attentionWrite(f, editor, action, item.attentionInput)[0]; again.OK || !strings.Contains(again.Error, "changed") {
			t.Fatalf("replay %+v", again)
		}
		undo := item.attentionInput
		undo.Resolution = result.Resolution
		undo.Revision = *result.Revision
		result = attentionWrite(f, editor, "undo", undo)[0]
		if !result.OK {
			t.Fatalf("undo %+v", result)
		}
		restored := f.state(item.NodeID)
		if restored.State != item.From || len(restored.Marks) != 1 {
			t.Fatalf("restore %+v", restored)
		}
	}
	if got := attentionRead(f, editor, ""); got.Total != 2 {
		t.Fatalf("restored queue %+v", got)
	}
	// Dismiss keeps the old inactivity episode suppressed even on future days.
	for _, item := range attentionRead(f, editor, "").Items {
		if result := attentionWrite(f, editor, "dismiss", item.attentionInput)[0]; !result.OK {
			t.Fatalf("dismiss %+v", result)
		}
	}
	f.run(f.now.Add(200 * 24 * time.Hour))
	if f.state(dismissID).Marks["cancel_suggested"] {
		t.Fatal("dismissed episode returned")
	}
}
func TestAttentionProposalsReappearAndApplyAfterUndo(t *testing.T) {
	t.Setenv("AEON_STATUS_AUTOPILOT", "suggest")
	f := setup(t)
	id := f.add("AUT-2", "work", "in_progress", 4, nil)
	f.run(f.now)
	editor := attentionMember(f)
	page := attentionRead(f, editor, "")
	if len(page.Items) != 1 || page.Items[0].Kind != "proposed" || !page.Items[0].Applicable {
		t.Fatalf("pending proposal %+v", page)
	}
	item := page.Items[0]
	for _, action := range []string{"dismiss", "apply"} {
		result := attentionWrite(f, editor, action, item.attentionInput)[0]
		if !result.OK {
			t.Fatalf("%s %+v", action, result)
		}
		if action == "apply" && f.state(id).State != "open" {
			t.Fatal("proposal did not move to Open")
		}
		item.Resolution = result.Resolution
		item.Revision = *result.Revision
		if result = attentionWrite(f, editor, "undo", item.attentionInput)[0]; !result.OK {
			t.Fatalf("undo %+v", result)
		}
		page = attentionRead(f, editor, "")
		if len(page.Items) != 1 || !page.Items[0].Applicable || f.state(id).State != "in_progress" {
			t.Fatalf("revived proposal %+v", page)
		}
		item = page.Items[0]
	}
}
func TestAttentionBulkReturnsPartialResultsAndGuardsUndo(t *testing.T) {
	f := setup(t)
	f.add("AUT-2", "work", "new", 8, nil)
	f.add("AUT-3", "work", "blocked", 15, nil)
	f.run(f.now)
	items := attentionRead(f, f.p, "").Items
	stale := items[1]
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Changed elsewhere',updated_at=clock_timestamp() WHERE id=$1`, stale.NodeID)
		return err
	})
	results := attentionWrite(f, f.p, "apply", items[0].attentionInput, stale.attentionInput)
	if !results[0].OK || results[1].OK || !strings.Contains(results[1].Error, "changed") {
		t.Fatalf("partial %+v", results)
	}
	in := items[0].attentionInput
	in.Resolution = results[0].Resolution
	in.Revision = *results[0].Revision
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET title='After Apply',updated_at=clock_timestamp() WHERE id=$1`, in.NodeID)
		return err
	})
	undo := attentionWrite(f, f.p, "undo", in)[0]
	if undo.OK || !strings.Contains(undo.Error, "changed") {
		t.Fatalf("stale undo %+v", undo)
	}
	if f.state(in.NodeID).State != items[0].To {
		t.Fatal("stale undo changed the ticket")
	}
}

// The barrier reports the exact authority-fence query, so revocation happens
// while the final write is queued behind it, with no timing assumptions.
type attentionFenceProbe struct{ reached chan struct{} }

func (p attentionFenceProbe) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if q.SQL == `SELECT id::text FROM tenants WHERE id=$1 FOR NO KEY UPDATE` {
		select {
		case p.reached <- struct{}{}:
		default:
		}
	}
	return ctx
}
func (attentionFenceProbe) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestAttentionChecksRevocationUnderFinalWriteFence(t *testing.T) {
	f := setup(t)
	f.add("AUT-2", "work", "new", 8, nil)
	f.run(f.now)
	editor := attentionMember(f)
	item := attentionRead(f, editor, "").Items[0]
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
	probe := attentionFenceProbe{make(chan struct{}, 1)}
	config.ConnConfig.Tracer = probe
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	body, _ := json.Marshal(map[string]any{"action": "apply", "items": []attentionInput{item.attentionInput}})
	done := make(chan []byte, 1)
	go func() {
		r := httptest.NewRequest("POST", "/api/status-autopilot/attention/actions", strings.NewReader(string(body)))
		r = r.WithContext(tenant.WithPrincipal(ctx, editor))
		w := httptest.NewRecorder()
		New(pool).attentionActions(w, r)
		done <- w.Body.Bytes()
	}()
	select {
	case <-probe.reached:
	case <-ctx.Done():
		t.Fatal("writer never reached tenant fence")
	}
	if _, err = blocker.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, editor.ID); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-done:
		var result struct {
			Items []attentionResult `json:"items"`
		}
		if err = json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Items) != 1 || result.Items[0].OK || !strings.Contains(result.Items[0].Error, "no longer available") {
			t.Fatalf("revocation result %s", raw)
		}
	case <-ctx.Done():
		t.Fatal("writer hung")
	}
	if f.state(item.NodeID).State != "new" {
		t.Fatal("revoked writer committed")
	}
}
func TestAdditionalAttentionFencePreservesAuthenticationGuard(t *testing.T) {
	f := setup(t)
	id := f.add("AUT-2", "work", "new", 8, nil)
	f.run(f.now)
	item := attentionRead(f, f.p, "").Items[0]
	body, _ := json.Marshal(map[string]any{"action": "apply", "items": []attentionInput{item.attentionInput}})
	called := false
	ctx := db.WithTenantGuard(tenant.WithPrincipal(t.Context(), f.p), func(context.Context, pgx.Tx, string) error { called = true; return events.ErrForbidden })
	r := httptest.NewRequest("POST", "/api/status-autopilot/attention/actions", strings.NewReader(string(body))).WithContext(ctx)
	w := httptest.NewRecorder()
	f.m.attentionActions(w, r)
	if !called || !strings.Contains(w.Body.String(), "no longer edit") || f.state(id).State != "new" {
		t.Fatalf("authentication guard lost: %s", w.Body)
	}
}

func TestAttentionMissedReleaseUsesPlanningMembershipAndUndo(t *testing.T) {
	f := setup(t)
	id := f.add("AUT-2", "work", "done", 15, nil)
	f.run(f.now)
	initial := attentionRead(f, f.p, "")
	if len(initial.Items) != 1 || initial.Items[0].Applicable || initial.Items[0].Unavailable == "" {
		t.Fatalf("missing planning target %+v", initial)
	}
	release := f.release(nil)
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='planning',released_at=NULL,version=NULL,version_scheme=NULL WHERE release_node_id=$1`, release); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE journey_projects SET current_release_node_id=$1 WHERE project_node_id=$2`, release, f.project)
		return err
	})
	item := attentionRead(f, f.p, "").Items[0]
	if !item.Applicable || item.ReleaseID != release || item.To != "release" {
		t.Fatalf("planning target %+v", item)
	}
	mismatched := item.attentionInput
	mismatched.ReleaseRevision++
	if result := attentionWrite(f, f.p, "apply", mismatched)[0]; result.OK || !strings.Contains(result.Error, "changed") {
		t.Fatalf("stale release %+v", result)
	}
	result := attentionWrite(f, f.p, "apply", item.attentionInput)[0]
	if !result.OK {
		t.Fatalf("membership %+v", result)
	}
	f.tx(func(tx pgx.Tx) error {
		var actual string
		err := tx.QueryRow(t.Context(), `SELECT release_node_id::text FROM journey_tickets WHERE ticket_node_id=$1`, id).Scan(&actual)
		if err == nil && actual != release {
			return fmt.Errorf("wrong release %s", actual)
		}
		return err
	})
	if f.state(id).State != "done" || f.state(id).Marks["missed_release"] {
		t.Fatal("membership changed status or retained flag")
	}
	item.Resolution = result.Resolution
	item.Revision = *result.Revision
	result = attentionWrite(f, f.p, "undo", item.attentionInput)[0]
	if !result.OK {
		t.Fatalf("membership undo %+v", result)
	}
	f.tx(func(tx pgx.Tx) error {
		var found bool
		err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM journey_tickets WHERE ticket_node_id=$1)`, id).Scan(&found)
		if err == nil && found {
			return fmt.Errorf("membership was not undone")
		}
		return err
	})
	if !f.state(id).Marks["missed_release"] {
		t.Fatal("undo did not restore the queue flag")
	}
}

func TestAttentionResolutionVisibilityPreservesHiddenReferences(t *testing.T) {
	f := setup(t)
	id := f.add("AUT-2", "work", "new", 8, nil)
	hidden := f.add("AUT-20", "project", "open", 0, nil)
	f.run(f.now)
	reader := attentionMember(f)
	item := attentionRead(f, f.p, "").Items[0]
	result := attentionWrite(f, f.p, "apply", item.attentionInput)[0]
	if !result.OK {
		t.Fatalf("apply %+v", result)
	}
	applied := result.Resolution
	item.Resolution, item.Revision = result.Resolution, *result.Revision
	if undone := attentionWrite(f, f.p, "undo", item.attentionInput)[0]; !undone.OK {
		t.Fatalf("undo %+v", undone)
	}
	f.tx(func(tx pgx.Tx) error {
		for _, change := range []events.Change{
			{NodeID: &id, Type: "status_autopilot.attention_private", After: map[string]any{"id": id}},
			{NodeID: &hidden, Type: "status_autopilot.attention_apply", After: map[string]any{"id": hidden}},
			{NodeID: &id, Type: "status_autopilot.attention_dismiss", After: map[string]any{"id": id, "fields": map[string]any{"dependency": hidden}}},
			{NodeID: &id, Type: "status_autopilot.attention_undone", After: map[string]any{"id": id, "fields": map[string]any{"dependency": hidden}}},
			{NodeID: &id, Type: "status_autopilot.proposed", After: map[string]any{"id": id, "fields": map[string]any{"dependency": hidden}}},
		} {
			if _, err := events.Append(t.Context(), tx, f.p, change); err != nil {
				return err
			}
		}
		return nil
	})
	var page struct {
		Items []events.Event `json:"items"`
	}
	if err := json.Unmarshal(f.call(reader, "GET", fmt.Sprintf("/api/events?after=%d", applied-1), "", 200).Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != applied || page.Items[0].Type != "status_autopilot.attention_apply" || page.Items[1].Type != "status_autopilot.attention_undone" {
		t.Fatalf("public resolutions or hidden reference boundaries: %+v", page)
	}
	for _, event := range page.Items {
		if event.ActorPrincipalID != f.p.ID || event.NodeID == nil || *event.NodeID != id || len(event.NodeChanges) != 1 || event.NodeChanges[0].ID != id {
			t.Fatalf("missing visible node projection: %+v", event)
		}
	}
}

func TestAttentionApplyAndUndoDeriveParentStatuses(t *testing.T) {
	f := setup(t)
	parent := f.add("AUT-2", "work", "blocked", 0, nil)
	leaf := f.add("AUT-3", "work", "blocked", 15, nil)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=$2`, parent, leaf)
		return err
	})
	dbtest.EnableWorkParentStatus(t, f.d, f.p.TenantID)
	f.run(f.now)
	page := attentionRead(f, f.p, "")
	if len(page.Items) != 1 || page.Items[0].NodeID != leaf || f.state(parent).State != "blocked" {
		t.Fatalf("leaf-only queue: %+v", page)
	}
	item := page.Items[0]
	result := attentionWrite(f, f.p, "apply", item.attentionInput)[0]
	if !result.OK || f.state(leaf).State != "open" || f.state(parent).State != "open" {
		t.Fatalf("Apply did not derive parent from leaf: %+v", result)
	}
	item.Resolution, item.Revision = result.Resolution, *result.Revision
	result = attentionWrite(f, f.p, "undo", item.attentionInput)[0]
	if !result.OK || f.state(leaf).State != "blocked" || f.state(parent).State != "blocked" {
		t.Fatalf("Undo did not derive parent from leaf: %+v", result)
	}
}

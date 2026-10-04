// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/activity"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func enableWorkStatusTest(t *testing.T, p tenant.Principal) {
	t.Helper()
	dbtest.EnableWorkParentStatus(t, testDB, p.TenantID)
}
func workNodeTest(t *testing.T, p tenant.Principal, parent string, state string) nodeJSON {
	t.Helper()
	k := kindBySlug(t, p, "work")
	body := fmt.Sprintf(`{"kind_id":%q,"title":"Work child","state":%q,"fields":%s}`, k.ID, state, benefitFields)
	if parent != "" {
		body = strings.TrimSuffix(body, "}") + fmt.Sprintf(`,"parent_id":%q}`, parent)
	}
	return mustNode(t, p, body)
}
func currentWorkTest(t *testing.T, p tenant.Principal, id string) nodeJSON {
	t.Helper()
	status, body := call(t, &p, "GET", "/api/nodes/"+id, "")
	return decode[nodeJSON](t, status, body, 200)
}
func lastDerivedTest(t *testing.T, p tenant.Principal, nodeID string) (int64, int64) {
	t.Helper()
	var id, cause int64
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id,(metadata->>'cause_event_id')::bigint FROM events WHERE node_id=$1 AND type='status_autopilot.derived' ORDER BY id DESC LIMIT 1`, nodeID).Scan(&id, &cause)
	}); err != nil {
		t.Fatal(err)
	}
	return id, cause
}
func causalCallTest(t *testing.T, p tenant.Principal, method string, id int64, body string) (int, []byte) {
	t.Helper()
	suffix := "undo"
	if method == "GET" {
		suffix = "undo-preview"
	}
	return callAs(t, events.New(appPool, events.WithUndoHandlers(UndoHandlers()), events.WithCausalUndoHandlers(CausalUndoHandlers())), &p, method, fmt.Sprintf("/api/events/%d/%s", id, suffix), body)
}
func patchWorkTest(t *testing.T, p tenant.Principal, id, state string) {
	t.Helper()
	status, body := call(t, &p, "PATCH", "/api/nodes/"+id, fmt.Sprintf(`{"state":%q}`, state))
	if status != 200 {
		t.Fatalf("patch %d %s", status, body)
	}
}
func TestWorkParentStatusCausalUndo(t *testing.T) {
	p := newPrincipal(t, "work-causal")
	root := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"Project"}`)
	top := workNodeTest(t, p, root.ID, "open")
	parent := workNodeTest(t, p, top.ID, "open")
	child := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	if currentWorkTest(t, p, parent.ID).State != "in_progress" || currentWorkTest(t, p, top.ID).State != "in_progress" {
		t.Fatal("cascade not derived")
	}
	id, cause := lastDerivedTest(t, p, parent.ID)
	other, _ := lastDerivedTest(t, p, top.ID)
	status, raw := causalCallTest(t, p, "GET", id, "")
	if status != 200 {
		t.Fatalf("preview %d %s", status, raw)
	}
	var preview struct {
		Cause int64 `json:"cause_event_id"`
		Nodes []struct {
			ID string `json:"id"`
			To string `json:"to"`
		} `json:"affected_nodes"`
	}
	if err := json.Unmarshal(raw, &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Cause != cause || len(preview.Nodes) != 1 || preview.Nodes[0].ID != child.ID || preview.Nodes[0].To != "open" {
		t.Fatalf("wrong cause preview %s", raw)
	}
	if currentWorkTest(t, p, child.ID).State != "in_progress" {
		t.Fatal("preview committed a write")
	}
	status, raw = causalCallTest(t, p, "POST", id, "")
	if status != 409 {
		t.Fatalf("missing confirmation %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 201 {
		t.Fatalf("undo %d %s", status, raw)
	}
	for _, n := range []nodeJSON{child, parent, top} {
		if currentWorkTest(t, p, n.ID).State != "open" {
			t.Fatal("causal undo did not rederive")
		}
	}
	status, raw = causalCallTest(t, p, "POST", other, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 409 {
		t.Fatalf("sibling parent repeated Undo %d %s", status, raw)
	}
	// A later unrelated child edit also invalidates the original preview.
	patchWorkTest(t, p, child.ID, "done")
	id, cause = lastDerivedTest(t, p, parent.ID)
	status, raw = call(t, &p, "PATCH", "/api/nodes/"+child.ID, `{"title":"Later edit"}`)
	if status != 200 {
		t.Fatalf("later edit %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "GET", id, "")
	if status != 409 {
		t.Fatalf("stale preview %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 409 {
		t.Fatalf("stale confirmation %d %s", status, raw)
	}
	if currentWorkTest(t, p, child.ID).State != "done" {
		t.Fatal("stale undo changed child")
	}
}
func TestWorkParentStatusPatchAndBulkBoundary(t *testing.T) {
	p := newPrincipal(t, "work-boundary")
	parent := workNodeTest(t, p, "", "open")
	child := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	for _, state := range []string{"open", "done"} {
		status, raw := call(t, &p, "PATCH", "/api/nodes/"+parent.ID, fmt.Sprintf(`{"state":%q}`, state))
		if status != 409 || !strings.Contains(string(raw), "parent_status_derived") {
			t.Fatalf("parent boundary %d %s", status, raw)
		}
	}
	status, raw := call(t, &p, "POST", "/api/nodes/bulk", fmt.Sprintf(`{"ids":[%q,%q],"state":"in_progress"}`, parent.ID, child.ID))
	if status != 200 {
		t.Fatalf("bulk %d %s", status, raw)
	}
	var result bulkResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].ID != parent.ID || result.Skipped[0].Code != "parent_status_derived" || len(result.Items) != 1 {
		t.Fatalf("bulk must honestly skip parent %s", raw)
	}
	if currentWorkTest(t, p, parent.ID).State != "in_progress" {
		t.Fatal("leaf bulk did not derive parent")
	}
	id, cause := lastDerivedTest(t, p, parent.ID)
	status, raw = causalCallTest(t, p, "GET", id, "")
	if status != 200 {
		t.Fatalf("bulk preview %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 201 {
		t.Fatalf("bulk causal undo %d %s", status, raw)
	}
	if currentWorkTest(t, p, parent.ID).State != "open" {
		t.Fatal("bulk undo did not derive parent")
	}
}
func TestWorkParentStatusDeleteCausalUndo(t *testing.T) {
	p := newPrincipal(t, "work-delete-undo")
	parent := workNodeTest(t, p, "", "open")
	_ = workNodeTest(t, p, parent.ID, "open")
	child := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	status, raw := call(t, &p, "DELETE", "/api/nodes/"+child.ID, "")
	if status != 204 {
		t.Fatalf("delete %d %s", status, raw)
	}
	if currentWorkTest(t, p, parent.ID).State != "open" {
		t.Fatal("delete did not derive")
	}
	id, cause := lastDerivedTest(t, p, parent.ID)
	status, raw = causalCallTest(t, p, "GET", id, "")
	if status != 200 {
		t.Fatalf("delete preview %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 201 {
		t.Fatalf("delete undo %d %s", status, raw)
	}
	if currentWorkTest(t, p, child.ID).State != "in_progress" || currentWorkTest(t, p, parent.ID).State != "in_progress" {
		t.Fatal("restore did not derive")
	}
}

func TestWorkParentStatusMoveAndCreateCausalUndo(t *testing.T) {
	p := newPrincipal(t, "work-move-undo")
	project := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"Project"}`)
	source := workNodeTest(t, p, project.ID, "open")
	target := workNodeTest(t, p, project.ID, "open")
	_ = workNodeTest(t, p, source.ID, "open")
	_ = workNodeTest(t, p, target.ID, "open")
	child := workNodeTest(t, p, source.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	status, raw := call(t, &p, "POST", "/api/nodes/"+child.ID+"/move", fmt.Sprintf(`{"parent_id":%q}`, target.ID))
	if status != 200 {
		t.Fatalf("move %d %s", status, raw)
	}
	if currentWorkTest(t, p, source.ID).State != "open" || currentWorkTest(t, p, target.ID).State != "in_progress" {
		t.Fatal("move missed ancestor chain")
	}
	id, cause := lastDerivedTest(t, p, target.ID)
	status, raw = causalCallTest(t, p, "GET", id, "")
	if status != 200 {
		t.Fatalf("move preview %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 201 {
		t.Fatalf("move undo %d %s", status, raw)
	}
	if currentWorkTest(t, p, source.ID).State != "in_progress" || currentWorkTest(t, p, target.ID).State != "open" {
		t.Fatal("move undo missed ancestor chain")
	}
	created := workNodeTest(t, p, target.ID, "in_progress")
	id, cause = lastDerivedTest(t, p, target.ID)
	status, raw = causalCallTest(t, p, "GET", id, "")
	if status != 200 {
		t.Fatalf("create preview %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 201 {
		t.Fatalf("create undo %d %s", status, raw)
	}
	if currentWorkTest(t, p, target.ID).State != "open" {
		t.Fatal("create undo did not derive")
	}
	if status, raw = call(t, &p, "GET", "/api/nodes/"+created.ID, ""); status != 404 {
		t.Fatalf("created child remains %d %s", status, raw)
	}
}

func TestWorkParentStatusHiddenCauseAndPermissionRevocation(t *testing.T) {
	p := newPrincipal(t, "work-hidden-undo")
	root := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"Project"}`)
	parent := workNodeTest(t, p, root.ID, "open")
	child := workNodeTest(t, p, parent.ID, "open")
	visibleChild := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	id, cause := lastDerivedTest(t, p, parent.ID)
	status, raw := causalCallTest(t, p, "GET", id, "")
	if status != 200 {
		t.Fatalf("preview %d %s", status, raw)
	}
	// Restrict to a project role so event visibility follows node visibility.
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, p.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
 SELECT $2,$1,id,'project',$3 FROM roles WHERE tenant_id=$2 AND key='admin'`, p.ID, p.TenantID, root.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appPool.Exec(t.Context(), `CREATE POLICY work_test_hidden ON nodes AS RESTRICTIVE USING(id<>'`+child.ID+`'::uuid OR aeon_visibility_system())`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := appPool.Exec(context.Background(), `DROP POLICY IF EXISTS work_test_hidden ON nodes`); err != nil {
			t.Error(err)
		}
	})
	if status, raw = call(t, &p, "GET", "/api/nodes/"+child.ID, ""); status != 404 {
		t.Fatalf("child visibility %d %s", status, raw)
	}
	patchWorkTest(t, p, visibleChild.ID, "done")
	if currentWorkTest(t, p, parent.ID).State != "in_progress" {
		t.Fatal("parent lost canonical hidden-child result")
	}
	status, raw = callAs(t, events.New(appPool), &p, "GET", "/api/events", "")
	if status != 200 {
		t.Fatalf("history %d %s", status, raw)
	}
	var history struct {
		Items []events.Event `json:"items"`
	}
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range history.Items {
		if e.ID == id {
			found = true
			if e.Derivation == nil || e.Derivation.CauseEventID != nil || len(e.Derivation.AffectedNodes) != 1 || e.Derivation.AffectedNodes[0] != parent.ID {
				t.Fatalf("unsafe derivation %+v", e.Derivation)
			}
		}
	}
	if !found {
		t.Fatal("visible parent event missing")
	}
	status, raw = causalCallTest(t, p, "GET", id, "")
	if status != 404 {
		t.Fatalf("hidden preview %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 404 {
		t.Fatalf("hidden undo %d %s", status, raw)
	}
	if _, err := appPool.Exec(t.Context(), `DROP POLICY work_test_hidden ON nodes`); err != nil {
		t.Fatal(err)
	}

	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'causal_reader','Causal reader')`, p.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,id,permission FROM roles, unnest(ARRAY['nodes.read','events.read','events.undo','events.undo_other']) permission WHERE tenant_id=$1 AND key='causal_reader'`, p.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='causal_reader') WHERE tenant_id=$1 AND principal_id=$2`, p.TenantID, p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 403 {
		t.Fatalf("revoked write %d %s", status, raw)
	}
}

func TestWorkParentStatusReusesAutopilotActivity(t *testing.T) {
	p := newPrincipal(t, "work-parent-activity")
	parent := workNodeTest(t, p, "", "open")
	child := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	id, _ := lastDerivedTest(t, p, parent.ID)
	status, raw := callAs(t, activity.New(appPool), &p, "GET", "/api/nodes/"+parent.ID+"/activity", "")
	if status != 200 {
		t.Fatalf("activity %d %s", status, raw)
	}
	var history activity.Page
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range history.Items {
		if c := item.AutomaticChange; c != nil && c.EventID == id {
			found = true
			if c.Rule != "work_parent" || c.From != "open" || c.To != "in_progress" || c.Reason == "" || !c.RequiresPreview || c.Undoable || !item.Author.Automatic || item.Author.Job != statusautopilot.Job {
				t.Fatalf("derived audit %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("derived status missing from autopilot Activity")
	}
}

// probeConfirmationBody pauses the body at an explicit barrier, before any
// confirmation bytes are available. Timeouts guard hangs, not interleavings.
type probeConfirmationBody struct {
	reader  io.Reader
	entered chan struct{}
	release chan struct{}
	ctx     context.Context
}

func (b *probeConfirmationBody) Read(p []byte) (int, error) {
	if b.entered != nil {
		close(b.entered)
		b.entered = nil
		select {
		case <-b.release:
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
	}
	return b.reader.Read(p)
}
func (b *probeConfirmationBody) Close() error { return nil }

func TestWorkParentStatusConfirmationReadsBeforeTenantLocks(t *testing.T) {
	p := newPrincipal(t, "work-body-fence")
	parent := workNodeTest(t, p, "", "open")
	child := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	id, cause := lastDerivedTest(t, p, parent.ID)
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(t.Context(), p), 15*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	mux := http.NewServeMux()
	events.New(appPool, events.WithCausalUndoHandlers(CausalUndoHandlers())).Mount(mux)
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/events/%d/undo", id), nil).WithContext(ctx)
	req.Body = &probeConfirmationBody{reader: strings.NewReader(fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause)), entered: entered, release: release, ctx: ctx}
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); mux.ServeHTTP(rec, req) }()
	select {
	case <-entered:
	case <-done:
		t.Fatalf("body never read: %d %s", rec.Code, rec.Body.String())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Acquire the same tree fence through an independent connection while the
	// request is stalled. A successful probe proves no tenant-wide lock is held.
	probe, err := adminPool.Begin(ctx)
	if err != nil {
		close(release)
		<-done
		t.Fatal(err)
	}
	var available bool
	err = probe.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, p.TenantID).Scan(&available)
	if err == nil && available {
		_, err = probe.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, p.TenantID)
	}
	_ = probe.Rollback(ctx)
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err != nil || !available {
		t.Fatalf("confirmation body holds tenant-wide locks: available=%v err=%v", available, err)
	}
	if rec.Code != 201 {
		t.Fatalf("confirmed Undo: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWorkParentStatusDisabledUndoFencesRevocation(t *testing.T) {
	p := newPrincipal(t, "work-disabled-revoke")
	parent := workNodeTest(t, p, "", "open")
	child := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	id, cause := lastDerivedTest(t, p, parent.ID)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE features SET enabled=false WHERE tenant_id=$1`, p.TenantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(dbtest.Seed(t.Context()), 15*time.Second)
	defer cancel()
	revoke, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke.Rollback(context.Background())
	if _, err := revoke.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`, p.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := authz.LockProjectMutation(ctx, revoke, p.TenantID); err != nil {
		t.Fatal(err)
	}
	var blocker uint32
	if err := revoke.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := revoke.Exec(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'disabled_reader','Disabled reader');`, p.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := revoke.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,id,permission FROM roles,unnest(ARRAY['nodes.read','events.read','events.undo','events.undo_other']) permission WHERE tenant_id=$1 AND key='disabled_reader'`, p.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := revoke.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='disabled_reader') WHERE tenant_id=$1 AND principal_id=$2`, p.TenantID, p.ID); err != nil {
		t.Fatal(err)
	}
	type response struct {
		status int
		body   []byte
	}
	done := make(chan response, 1)
	go func() {
		status, body := causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
		done <- response{status, body}
	}()
	// Commit revocation only once PostgreSQL proves Undo waits on its fence.
	for {
		select {
		case r := <-done:
			t.Fatalf("Undo escaped uncommitted revocation fence: %d %s", r.status, r.body)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
		var waiting bool
		if err := adminPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE NOT granted AND $1::int=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		runtime.Gosched()
	}
	// The per-event fence must still be free while Undo waits on the tree.
	// This is the same order whether transaction entry saw the flag ON or OFF.
	probe, err := adminPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var eventAvailable bool
	err = probe.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,12))`, p.TenantID+":"+fmt.Sprint(id)).Scan(&eventAvailable)
	_ = probe.Rollback(ctx)
	if err := revoke.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if err != nil || !eventAvailable {
			t.Fatalf("Undo locked the event before its tree fence: available=%v err=%v", eventAvailable, err)
		}
		if r.status != 403 || !strings.Contains(string(r.body), "forbidden") {
			t.Fatalf("revoked Undo: %d %s", r.status, r.body)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if currentWorkTest(t, p, child.ID).State != "in_progress" {
		t.Fatal("revoked Undo changed child")
	}
}

func TestWorkParentStatusCausalUndoAuditRedactsHiddenCause(t *testing.T) {
	p := newPrincipal(t, "work-undo-audit")
	project := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"Project"}`)
	parent := workNodeTest(t, p, project.ID, "open")
	child := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	id, cause := lastDerivedTest(t, p, parent.ID)
	status, raw := causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 201 {
		t.Fatalf("Undo: %d %s", status, raw)
	}
	var auditID int64
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE type='status_autopilot.causal_undo' AND undo_of=$1`, id).Scan(&auditID)
	}); err != nil {
		t.Fatal(err)
	}
	readAudit := func(hidden bool) {
		t.Helper()
		status, raw := callAs(t, events.New(appPool), &p, "GET", fmt.Sprintf("/api/events?node_id=%s", parent.ID), "")
		if status != 200 {
			t.Fatalf("history: %d %s", status, raw)
		}
		var page struct {
			Items []events.Event `json:"items"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Items {
			if e.ID == auditID {
				var snapshot map[string]json.RawMessage
				if err := json.Unmarshal(e.After, &snapshot); err != nil {
					t.Fatal(err)
				}
				if _, exists := snapshot["cause_event_id"]; exists {
					t.Fatalf("cause ID in public audit snapshot: %s", e.After)
				}
				if e.Derivation == nil {
					t.Fatal("causal audit lacks safe context")
				}
				if hidden {
					if e.Derivation.CauseEventID != nil {
						t.Fatal("hidden cause exposed")
					}
				} else if e.Derivation.CauseEventID == nil || *e.Derivation.CauseEventID != cause {
					t.Fatal("visible cause lost")
				}
				return
			}
		}
		t.Fatal("visible parent causal audit missing")
	}
	readAudit(false)
	// Model a historical audit snapshot as well: redaction must not depend on
	// rewriting append-only events that existed before this fix.
	if _, err := adminPool.Exec(t.Context(), `ALTER TABLE events DISABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	_, err := adminPool.Exec(t.Context(), `UPDATE events SET after=after||jsonb_build_object('cause_event_id',$1::bigint),metadata=NULL WHERE tenant_id=$2 AND id=$3`, cause, p.TenantID, auditID)
	_, enableErr := adminPool.Exec(t.Context(), `ALTER TABLE events ENABLE TRIGGER USER`)
	if err != nil || enableErr != nil {
		t.Fatalf("legacy fixture: %v %v", err, enableErr)
	}
	readAudit(false)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, p.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $2,$1,id,'project',$3 FROM roles WHERE tenant_id=$2 AND key='admin'`, p.ID, p.TenantID, project.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := appPool.Exec(t.Context(), `CREATE POLICY work_test_undo_hidden ON nodes AS RESTRICTIVE USING(id<>'`+child.ID+`'::uuid OR aeon_visibility_system())`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := appPool.Exec(context.Background(), `DROP POLICY IF EXISTS work_test_undo_hidden ON nodes`)
		if err != nil {
			t.Error(err)
		}
	})
	readAudit(true)
}

// The person-only decisions remain protected even when the event that changed
// them also caused a parent status derivation. Preview exercises the same guard.
func TestWorkCausalUndoProtectedTransitions(t *testing.T) {
	for _, tc := range []struct {
		name, initial, patch string
	}{
		{"unpublish", `{"fields":{"roadmap_public":true}}`, `{"fields":{"roadmap_public":false},"state":"in_progress"}`},
		{"publish", ``, `{"fields":{"roadmap_public":true},"state":"in_progress"}`},
		{"complete_check", `{"human_check":"Inspect the release"}`, `{"human_check":null,"state":"in_progress"}`},
		{"reopen_check", `{"human_check":"Inspect the release"}`, `{"human_check":"Inspect again","state":"in_progress"}`},
	} {
		for _, method := range []string{"GET", "POST"} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				p := newPrincipal(t, "causal-protected")
				parent := workNodeTest(t, p, "", "open")
				child := workNodeTest(t, p, parent.ID, "open")
				if tc.initial != "" {
					status, raw := call(t, &p, "PATCH", "/api/nodes/"+child.ID, tc.initial)
					child = decode[nodeJSON](t, status, raw, 200)
				}
				if tc.name == "reopen_check" {
					status, raw := call(t, &p, "PATCH", "/api/nodes/"+child.ID, `{"human_check":null}`)
					child = decode[nodeJSON](t, status, raw, 200)
				}
				enableWorkStatusTest(t, p)
				status, raw := call(t, &p, "PATCH", "/api/nodes/"+child.ID, tc.patch)
				after := decode[nodeJSON](t, status, raw, 200)
				id, cause := lastDerivedTest(t, p, parent.ID)
				agent := estimateAgent(t, p)
				agent.Scopes = append(agent.Scopes, "events.read", "events.undo", "events.undo_other")
				confirmation := fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause)
				status, raw = causalCallTest(t, agent, method, id, confirmation)
				if status != http.StatusForbidden || !strings.Contains(string(raw), "forbidden") {
					t.Fatalf("agent protected %s: %d %s", method, status, raw)
				}
				current := currentWorkTest(t, p, child.ID)
				if !current.UpdatedAt.Equal(after.UpdatedAt) || !sameString(current.HumanCheck, after.HumanCheck) || !reflectJSONEqual(estimateFieldsOf(t, current), estimateFieldsOf(t, after)) || current.State != after.State || currentWorkTest(t, p, parent.ID).State != "in_progress" {
					t.Fatal("denied protected undo changed child or parent")
				}
				status, raw = causalCallTest(t, p, "GET", id, "")
				if status != 200 {
					t.Fatalf("person preview: %d %s", status, raw)
				}
				status, raw = causalCallTest(t, p, "POST", id, confirmation)
				if status != 201 {
					t.Fatalf("person undo: %d %s", status, raw)
				}
				restored := currentWorkTest(t, p, child.ID)
				if !sameString(restored.HumanCheck, child.HumanCheck) || !reflectJSONEqual(estimateFieldsOf(t, restored), estimateFieldsOf(t, child)) || restored.State != child.State || currentWorkTest(t, p, parent.ID).State != "open" {
					t.Fatal("person undo did not faithfully restore child and rederive parent")
				}
			})
		}
	}
}

func TestWorkCausalUndoAgentOrdinaryEditPreservesProtectedFields(t *testing.T) {
	p := newPrincipal(t, "causal-ordinary")
	parent := workNodeTest(t, p, "", "open")
	child := workNodeTest(t, p, parent.ID, "open")
	status, raw := call(t, &p, "PATCH", "/api/nodes/"+child.ID, `{"fields":{"roadmap_public":true},"human_check":"Inspect the release"}`)
	child = decode[nodeJSON](t, status, raw, 200)
	enableWorkStatusTest(t, p)
	agent := estimateAgent(t, p)
	agent.Scopes = append(agent.Scopes, "events.read", "events.undo", "events.undo_other")
	patchWorkTest(t, agent, child.ID, "in_progress")
	id, cause := lastDerivedTest(t, p, parent.ID)
	status, raw = causalCallTest(t, agent, "GET", id, "")
	if status != 200 {
		t.Fatalf("agent ordinary preview: %d %s", status, raw)
	}
	status, raw = causalCallTest(t, agent, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 201 {
		t.Fatalf("agent ordinary undo: %d %s", status, raw)
	}
	restored := currentWorkTest(t, p, child.ID)
	if !sameString(restored.HumanCheck, child.HumanCheck) || !reflectJSONEqual(estimateFieldsOf(t, restored), estimateFieldsOf(t, child)) || restored.State != "open" {
		t.Fatal("ordinary undo changed protected fields")
	}
}

// Both requests target the same ordinary bulk event. The first enters while
// OFF, then activation commits; the second enters while ON and holds the tree.
// A database-observed wait proves overlap before probing the event fence.
func TestWorkParentStatusOrdinaryUndoOrdersAcrossActivation(t *testing.T) {
	p := newPrincipal(t, "work-ordinary-activation")
	child := workNodeTest(t, p, "", "open")
	enableWorkStatusTest(t, p)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE features SET enabled=false WHERE tenant_id=$1`, p.TenantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, raw := call(t, &p, "POST", "/api/nodes/bulk", fmt.Sprintf(`{"ids":[%q],"state":"done"}`, child.ID))
	if status != 200 {
		t.Fatalf("ordinary bulk: %d %s", status, raw)
	}
	var id int64
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE type='node.bulk_changed' ORDER BY id DESC LIMIT 1`).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	offPool, offBarrier, ctx := dbtest.BarrierPool(t, appPool, func(sql string) bool { return sql == "SELECT aeon_work_status_begin()" })
	type response struct {
		status int
		body   string
	}
	startUndo := func(pool *pgxpool.Pool) <-chan response {
		done := make(chan response, 1)
		mux := http.NewServeMux()
		events.New(pool, events.WithUndoHandlers(UndoHandlers())).Mount(mux)
		req := httptest.NewRequest("POST", fmt.Sprintf("/api/events/%d/undo", id), nil).WithContext(tenant.WithPrincipal(ctx, p))
		go func() {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			done <- response{rec.Code, rec.Body.String()}
		}()
		return done
	}
	first := startUndo(offPool)
	offPID := offBarrier.Wait(t, ctx)
	enableWorkStatusTest(t, p)
	onPool, onBarrier, _ := dbtest.BarrierPool(t, appPool, func(sql string) bool { return sql == "SELECT aeon_work_status_begin()" })
	second := startUndo(onPool)
	onPID := onBarrier.Wait(t, ctx)
	offBarrier.Release()
	for {
		select {
		case r := <-first:
			t.Fatalf("OFF Undo escaped ON tree fence: %d %s", r.status, r.body)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
		var waiting bool
		if err := adminPool.QueryRow(ctx, `SELECT $1::int=ANY(pg_blocking_pids($2::int))`, onPID, offPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		runtime.Gosched()
	}
	probe, err := adminPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var free bool
	err = probe.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,12))`, p.TenantID+":"+fmt.Sprint(id)).Scan(&free)
	_ = probe.Rollback(ctx)
	onBarrier.Release()
	// Drain both requests before assertions, including the failing implementation.
	a, b := dbtest.Await(t, ctx, first), dbtest.Await(t, ctx, second)
	if err != nil || !free {
		t.Fatalf("ordinary OFF Undo holds event while waiting for ON tree: free=%v err=%v; responses %d/%d", free, err, a.status, b.status)
	}
	if a.status != 409 || b.status != 201 {
		t.Fatalf("ordinary Undo results: OFF %d %s; ON %d %s", a.status, a.body, b.status, b.body)
	}
	if currentWorkTest(t, p, child.ID).State != "open" {
		t.Fatal("ordinary Undo did not restore child exactly once")
	}
}

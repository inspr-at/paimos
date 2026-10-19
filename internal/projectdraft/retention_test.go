// SPDX-License-Identifier: AGPL-3.0-only
package projectdraft

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: retention deletes an ordinary, engaged, young or foreign project,
// treats the import itself as engagement, reports a partial deletion as success,
// ignores dry-run, repeats its audit, or exceeds the bounded batch.
func TestStartImportRetention(t *testing.T) {
	f := setup(t)
	ctx := db.AllProjects(t.Context(), "retention fixture")
	// Only this person administers policy. Other members can still engage their
	// project, but agents and ordinary members cannot enable live deletion.
	if _, err := f.d.Admin.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='owner') WHERE tenant_id=$1 AND principal_id=$2`, f.tenant, f.a.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	old := now.AddDate(-1, 0, 0).Add(-time.Second)
	makeProject := func(key string, at time.Time) string {
		doc := handoffDoc()
		if key == "LIVE" {
			doc["transcript"].([]any)[0].(map[string]any)["text"] = "Unique live transcript"
		}
		capability := f.handoff(t, doc)
		body := createBody(f.a.ID, 0)
		body["key"] = key
		made := decodeBody[Created](t, expectStatus(t, f.call(t, f.a, http.MethodPost, "/api/import/project-draft/create", capability, body), http.StatusCreated))
		var project struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(made.Project, &project); err != nil {
			t.Fatal(err)
		}
		if got := f.scalar(t, `SELECT source||'|'||(engaged_at IS NULL)::text FROM import_project_retention WHERE project_id=$1`, project.ID); got != "start|true" {
			t.Fatalf("source marker %q", got)
		}
		if _, err := f.d.Admin.Exec(ctx, `UPDATE import_project_retention SET imported_at=$2 WHERE project_id=$1`, project.ID, at); err != nil {
			t.Fatal(err)
		}
		if _, err := f.d.Admin.Exec(ctx, `UPDATE nodes SET created_at=$2 WHERE id=$1`, project.ID, at); err != nil {
			t.Fatal(err)
		}
		return project.ID
	}
	live := makeProject("LIVE", old)
	young := makeProject("YOUNG", now.AddDate(-1, 0, 0).Add(time.Hour))
	explicit := makeProject("ENGAGE", old)
	commented := makeProject("COMMENT", old)
	edited := makeProject("EDIT", old)
	created := makeProject("CREATE", old)
	agentOnly := makeProject("AGENT", old)
	related := makeProject("RELATED", old)
	// Directly created projects have no source marker, even at the same age.
	var ordinary string
	if err := db.InTenant(ctx, f.d.App, f.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,created_at) SELECT $1,'ORDINARY-1',id,'Ordinary',$2 FROM node_kinds WHERE slug='project' RETURNING id::text`, f.tenant, old).Scan(&ordinary)
	}); err != nil {
		t.Fatal(err)
	}
	// A source marker in the other tenant must be invisible even with identical
	// eligibility, and the current tenant's job must never process it.
	var foreign string
	if err := db.InTenant(ctx, f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'FOREIGN-1',id,'Foreign' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.foreign.TenantID).Scan(&foreign); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO import_project_retention(tenant_id,project_id,source,imported_at) VALUES($1,$2,'start',$3)`, f.foreign.TenantID, foreign, old)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, f.d.App, f.tenant, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM import_project_retention WHERE project_id=$1`, foreign).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("foreign marker leaked through RLS")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/settings/import-retention"
	settings := decodeBody[RetentionSettings](t, expectOK(t, f.call(t, f.a, "GET", path, "", nil)))
	if !settings.Enabled || !settings.DryRun || settings.Months != 12 || settings.Revision != 0 {
		t.Fatalf("defaults %+v", settings)
	}
	expect(t, f.call(t, f.b, "PUT", path, "", map[string]any{"enabled": true, "dry_run": false, "expected_revision": 0}), 403)
	agent := f.a
	agent.Kind = tenant.Agent
	expect(t, f.call(t, agent, "PUT", path, "", map[string]any{"enabled": true, "dry_run": false, "expected_revision": 0}), 403)
	engagePath := "/api/projects/" + explicit + "/import-retention/engage"
	expect(t, f.call(t, agent, "POST", engagePath, "", nil), 403)
	expect(t, f.call(t, f.b, "POST", engagePath, "", nil), 200)
	expect(t, f.call(t, f.b, "POST", engagePath, "", nil), 200)
	if got := f.scalar(t, `SELECT count(*)::text FROM events WHERE type='import_retention.engaged' AND node_id=$1`, explicit); got != "1" {
		t.Fatalf("engagement audit count %s", got)
	}
	expect(t, f.call(t, f.b, "POST", "/api/projects/"+ordinary+"/import-retention/engage", "", nil), 404)
	// Use real durable event writes for all three engagement meanings. A
	// comment snapshot intentionally names no project: only node_id binds it.
	write := func(project, typ string, who tenant.Principal) {
		t.Helper()
		child := f.scalar(t, `SELECT id::text FROM nodes WHERE parent_id=$1 ORDER BY id LIMIT 1`, project)
		if err := db.InTenant(ctx, f.d.App, f.tenant, func(tx pgx.Tx) error {
			_, err := events.Append(ctx, tx, who, events.Change{NodeID: &child, Type: typ, After: map[string]any{"body_markdown": "A person's contribution"}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	// An inbound relation can name the other project's node as its event
	// node_id. Its referenced imported child must still engage this project.
	child := f.scalar(t, `SELECT id::text FROM nodes WHERE parent_id=$1 ORDER BY id LIMIT 1`, related)
	if err := db.InTenant(ctx, f.d.App, f.tenant, func(tx pgx.Tx) error {
		_, err := events.Append(ctx, tx, f.b, events.Change{NodeID: &ordinary, Type: "relation.created", After: map[string]any{"source_node_id": ordinary, "target_node_id": child}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A non-owning ordinary link is removed without deleting its other project.
	if _, err := f.d.Admin.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'blocks')`, f.tenant, live, ordinary); err != nil {
		t.Fatal(err)
	}
	write(commented, "comment.created", f.b)
	write(edited, "node.updated", f.b)
	write(created, "node.created", f.b)
	var botID string
	if err := f.d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Bot') RETURNING id::text`, f.tenant).Scan(&botID); err != nil {
		t.Fatal(err)
	}
	write(agentOnly, "node.updated", tenant.Principal{TenantID: f.tenant, ID: botID, Kind: tenant.Agent})
	// Simulate an import created by the previous release: backfill uses its
	// immutable positive source event after the draft has expired.
	if _, err := f.d.Admin.Exec(ctx, `DELETE FROM import_project_retention WHERE project_id=$1`, live); err != nil {
		t.Fatal(err)
	}
	result, err := f.m.RetainTenant(ctx, f.tenant, now)
	if err != nil || result.Deleted != 0 || len(result.Candidates) != 2 {
		t.Fatalf("dry run %+v, %v", result, err)
	}
	preview := f.call(t, f.a, "GET", path+"/candidates?limit=1", "", nil)
	expect(t, preview, 200)
	var page struct {
		Items []RetentionCandidate `json:"items"`
		Next  *string              `json:"next_cursor"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Next == nil {
		t.Fatalf("bounded preview %s, %v", preview.Body.String(), err)
	}
	expect(t, f.call(t, f.a, "GET", path+"/candidates?limit=101", "", nil), 400)
	if got := f.scalar(t, `SELECT count(*)::text FROM events WHERE type='import_retention.deleted'`); got != "0" {
		t.Fatal("dry run appended deletion audit")
	}
	setPolicy := func(enabled, dry bool) {
		t.Helper()
		settings = decodeBody[RetentionSettings](t, expectOK(t, f.call(t, f.a, "PUT", path, "", map[string]any{"enabled": enabled, "dry_run": dry, "expected_revision": settings.Revision})))
	}
	setPolicy(false, false)
	result, err = f.m.RetainTenant(ctx, f.tenant, now)
	if err != nil || result.Deleted != 0 || len(result.Candidates) != 0 {
		t.Fatalf("disabled %+v, %v", result, err)
	}
	if report, err := attachments.Verify(ctx, f.d.App, f.files, f.tenant); err != nil || report.Checked != 2 || len(report.Issues) != 0 {
		t.Fatalf("before retention file owners %+v, %v", report, err)
	}
	setPolicy(true, false)
	expect(t, f.call(t, f.a, "PUT", path, "", map[string]any{"enabled": true, "dry_run": true, "expected_revision": 0}), 409)
	result, err = f.m.RetainTenant(ctx, f.tenant, now)
	if err != nil || result.Deleted != 2 {
		t.Fatalf("live deletion %+v, %v", result, err)
	}
	for _, id := range []string{live, agentOnly} {
		for _, q := range []string{`SELECT count(*)::text FROM nodes WHERE id=$1 OR project_id=$1`, `SELECT count(*)::text FROM attachments WHERE node_id=$1`, `SELECT count(*)::text FROM import_project_drafts WHERE project_id=$1`} {
			if got := f.scalar(t, q, id); got != "0" {
				t.Fatalf("records retained for %s: %s", id, got)
			}
		}
		if got := f.scalar(t, `SELECT count(*)::text FROM events WHERE type='import_retention.deleted' AND after->>'project_id'=$1`, id); got != "1" {
			t.Fatalf("deletion audit %s", got)
		}
	}
	// Audit attachment snapshots no longer keep purged START bytes alive;
	// the other projects' shared transcript is still a durable file owner.
	if report, err := attachments.Verify(ctx, f.d.App, f.files, f.tenant); err != nil || report.Checked != 1 || len(report.Issues) != 0 {
		t.Fatalf("after retention file owners %+v, %v", report, err)
	}
	for _, id := range []string{young, explicit, commented, edited, created, related, ordinary, foreign} {
		if got := f.scalar(t, `SELECT count(*)::text FROM nodes WHERE id=$1`, id); got != "1" {
			t.Fatalf("protected project %s removed", id)
		}
	}
	result, err = f.m.RetainTenant(ctx, f.tenant, now)
	if err != nil || result.Deleted != 0 {
		t.Fatalf("replay %+v, %v", result, err)
	}
	// More than a batch: first pass deletes exactly the bound and the next
	// pass proves remaining candidates were preserved rather than truncated.
	for i := range retentionBatch + 1 {
		makeProject(fmt.Sprintf("BATCH%d", i), old)
	}
	result, err = f.m.RetainTenant(ctx, f.tenant, now)
	if err != nil || len(result.Candidates) != retentionBatch || result.Deleted != retentionBatch {
		t.Fatalf("batch %+v, %v", result, err)
	}
	result, err = f.m.RetainTenant(ctx, f.tenant, now)
	if err != nil || result.Deleted != 1 {
		t.Fatalf("remaining batch %+v, %v", result, err)
	}
	// A candidate can engage or the policy can turn off after discovery. The
	// final deletion transaction must recheck both, rather than trust the page.
	late := makeProject("LATE", old)
	setPolicy(true, true)
	result, err = f.m.RetainTenant(ctx, f.tenant, now)
	if err != nil || len(result.Candidates) != 1 {
		t.Fatalf("late preview %+v, %v", result, err)
	}
	setPolicy(false, false)
	if deleted, err := f.m.retainProject(ctx, f.tenant, late, now); deleted || err != nil {
		t.Fatalf("disabled after discovery: %v, %v", deleted, err)
	}
	expect(t, f.call(t, f.b, "POST", "/api/projects/"+late+"/import-retention/engage", "", nil), 200)
	setPolicy(true, false)
	if deleted, err := f.m.retainProject(ctx, f.tenant, late, now); deleted || err != nil {
		t.Fatalf("engaged after discovery: %v, %v", deleted, err)
	}
	// An audit write failure must undo the hard deletes and receipt too.
	fail := makeProject("FAIL", old)
	if _, err := f.d.Admin.Exec(ctx, `CREATE FUNCTION fail_retention_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='import_retention.deleted' THEN RAISE EXCEPTION 'retention audit failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER fail_retention_audit BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION fail_retention_audit()`); err != nil {
		t.Fatal(err)
	}
	result, err = f.m.RetainTenant(ctx, f.tenant, now)
	if err == nil || !strings.Contains(err.Error(), "retention audit failure") || result.Deleted != 0 {
		t.Fatalf("audit failure %+v, %v", result, err)
	}
	if got := f.scalar(t, `SELECT (deleted_at IS NULL)::text FROM import_project_retention WHERE project_id=$1`, fail); got != "true" {
		t.Fatal("failed delete retained receipt")
	}
	if got := f.scalar(t, `SELECT count(*)::text FROM nodes WHERE project_id=$1`, fail); got != "3" {
		t.Fatalf("audit failure lost records: %s", got)
	}
}

func expectStatus(t *testing.T, w *httptest.ResponseRecorder, status int) *httptest.ResponseRecorder {
	t.Helper()
	expect(t, w, status)
	return w
}

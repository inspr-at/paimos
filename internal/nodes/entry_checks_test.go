// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// Risk: an existing or new tenant loses its completion and queue guards when
// no definition has been saved. Exercise both HTTP writes on an unconfigured
// tenant, then prove that deliberate empty definitions still replace defaults.
func TestEntryChecksDefaultWithoutDefinition(t *testing.T) {
	p := newPrincipal(t, "entry-defaults")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Unconfigured project"}`, kindBySlug(t, p, "project").ID))
	n := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"title":"Unfinished work","fields":{"acceptance_criteria":"Tests pass"}}`, kindBySlug(t, p, "work").ID, project.ID))
	agent := estimateAgent(t, p)
	var definitions int
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE k.slug='definition'`).Scan(&definitions)
	}); err != nil || definitions != 0 {
		t.Fatalf("fixture has a saved definition: %d, %v", definitions, err)
	}
	t.Run("agent-done", func(t *testing.T) {
		code, raw := call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"state":"done"}`)
		var refusal struct {
			Error string `json:"error"`
			workqueue.EntryFailure
		}
		if json.Unmarshal(raw, &refusal) != nil || code != 409 || refusal.Error != "entry_checks_failed" || refusal.ToStatus != "done" || refusal.OverrideAllowed || len(refusal.Failed) != 1 || refusal.Failed[0].ID != "release_copy_set" {
			t.Fatalf("default Done guard lost: %d %s", code, raw)
		}
		if now := currentWorkTest(t, p, n.ID); now.State != n.State || !now.UpdatedAt.Equal(n.UpdatedAt) {
			t.Fatal("refusal changed the ticket", now)
		}
	})
	t.Run("queue-ready", func(t *testing.T) {
		// Use separate work so a broken Done guard cannot turn this refusal
		// into a status failure and conceal the missing estimate guard.
		n := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"title":"Unestimated queue work","fields":{"acceptance_criteria":"Tests pass"}}`, kindBySlug(t, p, "work").ID, project.ID))
		code, raw := callAs(t, agentruns.New(appPool), &p, "POST", "/api/queue", fmt.Sprintf(`{"node_id":%q}`, n.ID))
		var refusal struct {
			Code      string              `json:"code"`
			Readiness workqueue.Readiness `json:"readiness"`
		}
		if json.Unmarshal(raw, &refusal) != nil || code != 422 || refusal.Code != "queue_not_ready" || !refusal.Readiness.Queueable || refusal.Readiness.Ready || !slices.Equal(refusal.Readiness.Missing, []string{"estimate"}) {
			t.Fatalf("default Ready guard lost: %d %s", code, raw)
		}
		var runs int
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1`, n.ID).Scan(&runs)
		}); err != nil || runs != 0 {
			t.Fatalf("refused queue write persisted a run: %d, %v", runs, err)
		}
	})
	code, raw := call(t, &p, "GET", "/api/nodes/"+n.ID+"/entry-checks", "")
	checks := decode[workqueue.EntryChecks](t, code, raw, 200)
	if checks.Definition == nil || checks.Definition.Source != "workspace" || len(checks.Blocks) != 3 || checks.Blocks[0].Total != 2 || checks.Blocks[0].Passed != 1 || checks.Blocks[1].Total != 0 || checks.Blocks[2].Total != 1 || checks.Blocks[2].Passed != 0 {
		t.Fatalf("missing built-in workspace checks: %s", raw)
	}
	for _, tc := range []struct {
		path   string
		source string
	}{
		{"/api/settings/definitions", "workspace"},
		{"/api/projects/" + project.ID + "/definitions", "project"},
	} {
		code, raw = call(t, &p, "PUT", tc.path, `{"expected_revision":null,"checks":{}}`)
		d := decode[workqueue.EntryDefinition](t, code, raw, 200)
		code, raw = call(t, &p, "GET", "/api/nodes/"+n.ID+"/entry-checks", "")
		checks = decode[workqueue.EntryChecks](t, code, raw, 200)
		if checks.Definition == nil || checks.Definition.ID != d.ID || checks.Definition.Source != tc.source || checks.Blocks[0].Total != 0 || checks.Blocks[2].Total != 0 {
			t.Fatalf("explicit empty definition did not replace defaults: %s", raw)
		}
	}
	code, raw = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"state":"done"}`)
	if got := decode[nodeJSON](t, code, raw, 200); got.State != "done" {
		t.Fatal("explicit empty project definition blocked Done", got)
	}
}

// Risks: false Done, agent bypass, lost audit, and a manual tick by the wrong
// person. All assertions use the HTTP write boundary and persisted history.
func TestEntryChecksEnforcementOverrideAndOwnership(t *testing.T) {
	p := newPrincipal(t, "entry-checks")
	projectKind := kindBySlug(t, p, "project")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Definitions project"}`, projectKind.ID))
	work := kindBySlug(t, p, "work")
	n := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"title":"Needs Ready checks"}`, work.ID, project.ID))
	agent := estimateAgent(t, p)
	definition := `{"expected_revision":null,"checks":{"in_progress":[{"id":"estimate_set"},{"id":"acceptance_present"},{"id":"no_active_build"}],"qa":[{"id":"ci_green_latest_main"},{"id":"review_ok_cross_family"},{"id":"visual_ok"}],"done":[{"id":"release_copy_set"},{"id":"manual:release-reviewed","label":"Release notes reviewed"}]}}`
	code, raw := call(t, &p, "PUT", "/api/projects/"+project.ID+"/definitions", definition)
	d := decode[workqueue.EntryDefinition](t, code, raw, 200)
	t.Run("bulk-builder-fence-before-node-rows", func(t *testing.T) {
		// Risk: a bulk writer holding node rows while it waits for admission
		// deadlocks a direct builder that holds the admission lock first.
		pool, barrier, ctx := dbtest.BarrierPool(t, appPool, func(sql string) bool { return strings.Contains(sql, "aeon-builder:") })
		module := New(pool, SQLWriter{}).(*Module)
		type outcome struct {
			result bulkResult
			err    error
		}
		done := make(chan outcome, 1)
		state := "in_progress"
		go func() {
			result, err := module.applyBulk(tenant.WithPrincipal(ctx, p), p, bulkPlan{ids: []string{n.ID}, state: &state})
			done <- outcome{result, err}
		}()
		barrier.Wait(t, ctx)
		row, err := adminPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer row.Rollback(t.Context())
		if _, err := row.Exec(ctx, `SELECT id FROM nodes WHERE id=$1 FOR UPDATE NOWAIT`, n.ID); err != nil {
			t.Fatalf("bulk acquired the node row before its builder fence: %v", err)
		}
		if err := row.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		barrier.Release()
		got := dbtest.Await(t, ctx, done)
		if got.err != nil || len(got.result.Items) != 0 || len(got.result.Skipped) != 1 || got.result.Skipped[0].Code != "entry_checks_failed" {
			t.Fatalf("bulk did not retain the entry refusal: %+v, %v", got.result, got.err)
		}
	})
	code, raw = call(t, &agent, "PUT", "/api/projects/"+project.ID+"/definitions", definition)
	if code != 403 {
		t.Fatalf("agent changed governance: %d %s", code, raw)
	}
	for attempt := 0; attempt < 2; attempt++ {
		body := `{"state":"in_progress"}`
		if attempt == 1 {
			body = `{"state":"in_progress","override_reason":"Agent must not override"}`
		}
		code, raw = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, body)
		var failed struct {
			Error  string                  `json:"error"`
			Failed []workqueue.EntryResult `json:"failed"`
			Status string                  `json:"to_status"`
		}
		if json.Unmarshal(raw, &failed) != nil || code != 409 || failed.Error != "entry_checks_failed" || failed.Status != "in_progress" || len(failed.Failed) != 2 || failed.Failed[0].ID != "estimate_set" || failed.Failed[1].ID != "acceptance_present" {
			t.Fatalf("wrong refusal: %d %s", code, raw)
		}
		if now := currentWorkTest(t, p, n.ID); now.State != n.State || !now.UpdatedAt.Equal(n.UpdatedAt) {
			t.Fatal("refusal changed the ticket", now)
		}
	}
	code, raw = call(t, &p, "GET", "/api/nodes/"+n.ID+"/entry-checks", "")
	checks := decode[workqueue.EntryChecks](t, code, raw, 200)
	if checks.LatestRefusal == nil || checks.LatestRefusal.Count != 2 || checks.Blocks[0].Passed != 1 || checks.Blocks[0].Total != 3 {
		t.Fatalf("refusal/history: %s", raw)
	}
	for _, tc := range []struct {
		body    string
		status  int
		message string
	}{
		{`{"state":"in_progress"}`, 409, "entry_checks_failed"},
		{`{"state":"in_progress","override_reason":"   "}`, 400, "requires a reason"},
	} {
		code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, tc.body)
		if code != tc.status || !strings.Contains(string(raw), tc.message) {
			t.Fatalf("person skipped required reason: %d %s", code, raw)
		}
	}
	code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"state":"in_progress","override_reason":"Estimate follows the concept review"}`)
	n = decode[nodeJSON](t, code, raw, 200)
	code, raw = call(t, &p, "GET", "/api/nodes/"+n.ID+"/entry-checks", "")
	checks = decode[workqueue.EntryChecks](t, code, raw, 200)
	if len(checks.Entries) != 1 || checks.Entries[0].OverrideReason == nil || *checks.Entries[0].OverrideReason != "Estimate follows the concept review" || checks.Entries[0].Passed != 1 || checks.Entries[0].By.ID != p.ID || checks.Entries[0].DefinitionID != d.ID {
		t.Fatalf("override record lost: %s", raw)
	}
	var count int
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=$1 AND metadata->'entry_checks'->>'override_reason'=$2`, n.ID, "Estimate follows the concept review").Scan(&count)
	}); err != nil || count != 1 {
		t.Fatal("override must be one status/audit event", count, err)
	}
	// Self-authored booleans cannot stand in for CI and a cross-family verdict.
	code, raw = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"state":"qa","fields":{"ci_green_latest_main":true,"review_ok_cross_family":true,"visual_ok":true}}`)
	if code != 409 || !strings.Contains(string(raw), "ci_green_latest_main") || !strings.Contains(string(raw), "review_ok_cross_family") {
		t.Fatalf("agent forged delivery facts: %d %s", code, raw)
	}
	code, raw = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"state":"done"}`)
	if code != 409 || !strings.Contains(string(raw), "release_copy_set") || !strings.Contains(string(raw), "manual:release-reviewed") || !strings.Contains(string(raw), p.Name) {
		t.Fatalf("Done/manual refusal: %d %s", code, raw)
	}
	code, raw = call(t, &p, "GET", "/api/nodes/"+n.ID+"/entry-checks", "")
	checks = decode[workqueue.EntryChecks](t, code, raw, 200)
	manual := checks.Blocks[2].Checks[1]
	if manual.Owner == nil || manual.Owner.ID != p.ID || !manual.CanTick {
		t.Fatalf("no current person owner: %+v", manual)
	}
	body := fmt.Sprintf(`{"checked":true,"expected_revision":%q}`, checks.Revision.Format("2006-01-02T15:04:05.999999999Z07:00"))
	code, raw = call(t, &agent, "PUT", "/api/nodes/"+n.ID+"/entry-checks/manual:release-reviewed", body)
	if code != 403 {
		t.Fatalf("agent ticked human evidence: %d %s", code, raw)
	}
	// A current writer who is neither owner nor admin cannot tick a human step.
	member := tenant.Principal{ID: addPrincipalIn(t, p.TenantID, "Other editor").ID, TenantID: p.TenantID, Kind: tenant.Person, Name: "Other editor"}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, p.TenantID, member.ID, project.ID); err != nil {
		t.Fatal(err)
	}
	code, raw = call(t, &member, "GET", "/api/nodes/"+n.ID+"/entry-checks", "")
	otherChecks := decode[workqueue.EntryChecks](t, code, raw, 200)
	if otherChecks.Blocks[2].Checks[1].CanTick {
		t.Fatal("nonowner was offered a manual tick")
	}
	code, raw = call(t, &member, "PUT", "/api/nodes/"+n.ID+"/entry-checks/manual:release-reviewed", body)
	if code != 403 {
		t.Fatalf("nonowner ticked human evidence: %d %s", code, raw)
	}
	code, raw = call(t, &p, "PUT", "/api/nodes/"+n.ID+"/entry-checks/manual:release-reviewed", body)
	checks = decode[workqueue.EntryChecks](t, code, raw, 200)
	if !checks.Blocks[2].Checks[1].Passed || checks.Blocks[2].Checks[1].CheckedBy.ID != p.ID {
		t.Fatalf("tick provenance lost: %s", raw)
	}
	// Markus's Q1: hiding release notes makes copy inapplicable; no filler text.
	code, raw = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"state":"done","fields":{"hide_from_release_notes":true}}`)
	n = decode[nodeJSON](t, code, raw, 200)
	if n.State != "done" {
		t.Fatal("hidden checked work cannot enter Done")
	}
	// A project copy replaces, rather than merges with, the workspace default.
	code, raw = call(t, &p, "PUT", "/api/settings/definitions", `{"expected_revision":null,"checks":{"in_progress":[{"id":"estimate_set"}]}}`)
	decode[workqueue.EntryDefinition](t, code, raw, 200)
	code, raw = call(t, &p, "PUT", "/api/projects/"+project.ID+"/definitions", fmt.Sprintf(`{"expected_revision":%q,"checks":{}}`, d.Revision.Format("2006-01-02T15:04:05.999999999Z07:00")))
	decode[workqueue.EntryDefinition](t, code, raw, 200)
	code, raw = call(t, &p, "GET", "/api/nodes/"+n.ID+"/entry-checks", "")
	checks = decode[workqueue.EntryChecks](t, code, raw, 200)
	if checks.Definition.Source != "project" || checks.Blocks[0].Total != 0 || len(checks.Entries) != 2 {
		t.Fatalf("empty copy merged or erased entry history: %s", raw)
	}
}

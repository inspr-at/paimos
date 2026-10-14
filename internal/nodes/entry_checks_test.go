// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

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
	for _, body := range []string{`{"state":"in_progress"}`, `{"state":"in_progress","override_reason":"   "}`} {
		code, raw = call(t, &p, "PATCH", "/api/nodes/"+n.ID, body)
		if code != 409 && code != 400 {
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

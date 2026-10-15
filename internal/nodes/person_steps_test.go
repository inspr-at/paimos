// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/decisiondesk"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// Risks: a person request is lost or duplicated, an agent/nonowner forges a
// completion, a stale owner ticks after revocation, or a Desk count differs
// from the native ticket state. Exercise real HTTP writes and the real Desk
// projection; the revocation interleaving is established with a DB barrier.
func TestPersonStepsOwnershipRoutingAndAudit(t *testing.T) {
	admin := newPrincipal(t, "person-steps")
	project := mustNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Human steps"}`, kindBySlug(t, admin, "project").ID))
	member := func(name string) tenant.Principal {
		p := tenant.Principal{ID: addPrincipalIn(t, admin.TenantID, name).ID, TenantID: admin.TenantID, Kind: tenant.Person, Name: name}
		if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, admin.TenantID, p.ID, project.ID); err != nil {
			t.Fatal(err)
		}
		return p
	}
	owner, other := member("Mia Weber"), member("Alex Person")
	agent := estimateAgent(t, admin)
	code, raw := call(t, &admin, "PUT", "/api/projects/"+project.ID+"/definitions", fmt.Sprintf(`{"expected_revision":null,"checks":{"done":[{"id":"manual:release-reviewed","label":"Release notes reviewed","fallback_owner_id":%q}]}}`, other.ID))
	decode[workorders.EntryDefinition](t, code, raw, 200)
	node := mustNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"state":"qa","title":"Release verification","human_check":"Verify the deployed release","fields":{"assignee":%q,"hide_from_release_notes":true}}`, kindBySlug(t, admin, "work").ID, project.ID, owner.ID))
	read := func(p tenant.Principal) personStepsResponse {
		t.Helper()
		code, raw := call(t, &p, "GET", "/api/nodes/"+node.ID+"/person-steps", "")
		return decode[personStepsResponse](t, code, raw, 200)
	}
	find := func(out personStepsResponse, id string) workorders.PersonStep {
		t.Helper()
		for _, s := range out.Steps {
			if s.CheckID == id {
				return s
			}
		}
		t.Fatalf("missing %s in %+v", id, out)
		return workorders.PersonStep{}
	}
	desk := func(p tenant.Principal, want int) decisiondesk.Page {
		t.Helper()
		code, raw := callAs(t, decisiondesk.New(appPool), &p, "GET", "/api/decision-desk/projection?limit=1", "")
		if code != 200 {
			_, err := decisiondesk.New(appPool).Read(tenant.WithPrincipal(t.Context(), p), p, 1, nil)
			t.Fatalf("Desk read failed: HTTP %d %s; native error: %v", code, raw, err)
		}
		out := decode[decisiondesk.Page](t, code, raw, 200)
		if out.Counts.Open != want || len(out.Items) != min(1, want) {
			t.Fatalf("wrong Desk for %s: %s", p.Name, raw)
		}
		return out
	}
	write := func(p tenant.Principal, s workorders.PersonStep, body map[string]any, suffix string) personStepsResponse {
		t.Helper()
		body["expected_revision"] = s.Revision
		body["definition_revision"] = s.DefinitionRevision
		data, _ := json.Marshal(body)
		code, raw := call(t, &p, "PUT", "/api/nodes/"+node.ID+"/person-steps/"+s.CheckID+suffix, string(data))
		return decode[personStepsResponse](t, code, raw, 200)
	}
	manual := find(read(owner), "manual:release-reviewed")
	human := find(read(owner), "human")
	if manual.Owner.ID != owner.ID || !manual.CanTick || !manual.Active || human.Active || human.Owner.ID != owner.ID {
		t.Fatalf("wrong assignee-first routing: %+v %+v", manual, human)
	}
	initial := desk(owner, 1)
	desk(admin, 0)
	desk(other, 0)
	if initial.Items[0].PersonStep == nil || initial.Items[0].PersonStep.ID != manual.ID || !initial.Items[0].CanDecide {
		t.Fatal("missing native inline pointer", initial)
	}
	// Agents and other editors cannot tick through either native API or PATCH.
	for _, actor := range []tenant.Principal{agent, other} {
		for _, check := range []string{manual.CheckID, "human"} {
			payload, _ := json.Marshal(map[string]any{"checked": true, "expected_revision": manual.Revision, "definition_revision": manual.DefinitionRevision})
			code, raw = call(t, &actor, "PUT", "/api/nodes/"+node.ID+"/person-steps/"+check, string(payload))
			if code != 403 || !strings.Contains(string(raw), "owner") && !strings.Contains(string(raw), "permission") {
				t.Fatalf("wrong tick refusal: %d %s", code, raw)
			}
		}
		code, raw = call(t, &actor, "PATCH", "/api/nodes/"+node.ID, `{"human_check":null}`)
		if code != 403 {
			t.Fatalf("legacy PATCH bypass: %d %s", code, raw)
		}
	}
	code, raw = call(t, &agent, "PATCH", "/api/nodes/"+node.ID, `{"state":"done","override_reason":"agent bypass"}`)
	if code != 409 || !strings.Contains(string(raw), "entry_checks_failed") || !strings.Contains(string(raw), owner.Name) {
		t.Fatalf("refusal does not name owner: %d %s", code, raw)
	}

	completedManual := write(owner, manual, map[string]any{"checked": true}, "")
	manual = find(completedManual, manual.CheckID)
	desk(owner, 0)
	repeatedManual := write(owner, manual, map[string]any{"checked": true}, "")
	if !repeatedManual.Revision.Equal(completedManual.Revision) {
		t.Fatal("duplicate manual tick changed revision")
	}
	manual = find(write(owner, manual, map[string]any{"checked": false}, ""), manual.CheckID)
	desk(owner, 1)
	// Reassignment uses the same request identity; repeated current state writes
	// change neither revision nor audit count. The previous owner can undo only
	// the exact unchanged assignment event they just wrote.
	reassigned := write(owner, manual, map[string]any{"owner_id": other.ID}, "/owner")
	moved := find(reassigned, manual.CheckID)
	if moved.Owner.ID != other.ID || moved.ID != manual.ID || reassigned.UndoReassignment < 1 {
		t.Fatalf("reassignment lost identity/audit: %+v", reassigned)
	}
	desk(owner, 0)
	desk(other, 1)
	duplicate := write(other, moved, map[string]any{"owner_id": other.ID}, "/owner")
	if !duplicate.Revision.Equal(reassigned.Revision) {
		t.Fatal("duplicate reassignment changed revision")
	}
	restored := write(owner, moved, map[string]any{"owner_id": owner.ID, "undo_reassignment": reassigned.UndoReassignment}, "/owner")
	manual = find(restored, manual.CheckID)
	if manual.Owner.ID != owner.ID {
		t.Fatal("Undo did not restore owner", manual)
	}
	// A stale definition or ticket precondition cannot apply the action to a
	// changed step, even when the current owner is the same person.
	payload, _ := json.Marshal(map[string]any{"checked": true, "expected_revision": manual.Revision, "definition_revision": time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)})
	code, raw = call(t, &owner, "PUT", "/api/nodes/"+node.ID+"/person-steps/"+manual.CheckID, string(payload))
	if code != 412 {
		t.Fatalf("stale definition tick accepted: %d %s", code, raw)
	}
	// The list mark, Waiting for you and exact facet totals share the projection.
	code, raw = call(t, &owner, "GET", "/api/nodes?kind=work&person_waiting=mine&facets=person_waiting", "")
	page := decode[nodePage](t, code, raw, 200)
	if len(page.Items) != 1 || page.Items[0].ID != node.ID || len(page.Items[0].PersonWaiting) != 1 || page.Facets["person_waiting"]["mine"] != 1 {
		t.Fatalf("waiting filter/count drift: %s", raw)
	}
	// A person can explicitly override the failing manual check; audit retains
	// the missing check, its owner and the reason. Its request then stops mattering.
	code, raw = call(t, &admin, "PATCH", "/api/nodes/"+node.ID, `{"state":"done","override_reason":"Release verification follows deployment"}`)
	decode[nodeJSON](t, code, raw, 200)
	out := read(owner)
	human = find(out, "human")
	if !human.Active || find(out, manual.CheckID).Active {
		t.Fatal("wrong request activation after Done", out)
	}
	requests := desk(owner, 1)
	if requests.Items[0].PersonStep.CheckID != "human" {
		t.Fatal("Done did not replace manual request with human check", requests)
	}
	code, raw = call(t, &admin, "GET", "/api/nodes/"+node.ID+"/entry-checks", "")
	var checks workorders.EntryChecks
	if json.Unmarshal(raw, &checks) != nil || code != 200 || len(checks.Entries) != 1 || checks.Entries[0].OverrideReason == nil || *checks.Entries[0].OverrideReason != "Release verification follows deployment" || checks.Entries[0].Checks[0].Owner.ID != owner.ID {
		t.Fatalf("override audit missing: %d %s", code, raw)
	}
	complete := write(owner, human, map[string]any{"checked": true}, "")
	checked := find(complete, "human")
	if !checked.Checked || checked.Active {
		t.Fatal("human tick not confirmed", checked)
	}
	desk(owner, 0)
	repeated := write(owner, checked, map[string]any{"checked": true}, "")
	if !repeated.Revision.Equal(complete.Revision) {
		t.Fatal("duplicate tick wrote another completion")
	}
	code, raw = call(t, &other, "PATCH", "/api/nodes/"+node.ID, `{"human_check":"Verify the deployed release"}`)
	if code != 403 {
		t.Fatalf("nonowner undid a human check: %d %s", code, raw)
	}
	reopened := write(owner, checked, map[string]any{"checked": false}, "")
	human = find(reopened, "human")
	desk(owner, 1)
	// Hold a legitimate owner's write immediately before its tenant fence.
	// Revocation finishes first through the real access API. No elapsed-time
	// assumption or unrelated permission failure can satisfy the assertion.
	pool, barrier, ctx := dbtest.BarrierPool(t, appPool, func(sql string) bool {
		return strings.Contains(sql, "SELECT nullif(current_setting('aeon.tenant_id', true)")
	})
	type outcome struct {
		code int
		raw  []byte
	}
	done := make(chan outcome, 1)
	payload, _ = json.Marshal(map[string]any{"checked": true, "expected_revision": human.Revision, "definition_revision": nil})
	go func() {
		code, raw := callAs(t, New(pool, nil), &owner, "PUT", "/api/nodes/"+node.ID+"/person-steps/human", string(payload))
		done <- outcome{code, raw}
	}()
	barrier.Wait(t, ctx)
	code, raw = callAs(t, authz.New(appPool), &admin, "DELETE", "/api/projects/"+project.ID+"/members/"+owner.ID, "")
	if code != 204 {
		t.Fatalf("revocation failed: %d %s", code, raw)
	}
	barrier.Release()
	blocked := dbtest.Await(t, ctx, done)
	if blocked.code != 403 {
		t.Fatalf("revoked owner completed step: %d %s", blocked.code, blocked.raw)
	}
	fallback := find(read(admin), "human")
	if fallback.Owner.ID != admin.ID || fallback.Checked || !fallback.Active {
		t.Fatal("access loss did not immediately fall back to admin", fallback)
	}
	desk(admin, 1)
	code, raw = callAs(t, decisiondesk.New(appPool), &owner, "GET", "/api/decision-desk/projection", "")
	inaccessible := decode[decisiondesk.Page](t, code, raw, 200)
	if inaccessible.Counts.Open != 0 || len(inaccessible.Items) != 0 {
		t.Fatal("revoked owner retained a Desk request", inaccessible)
	}
	// Existing definition fallback is named, while a revoked per-ticket owner
	// is replaced by an admin immediately. Agent assignees never become owners.
	second := mustNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"state":"qa","title":"Agent-owned work","fields":{"assignee":%q}}`, kindBySlug(t, admin, "work").ID, project.ID, agent.ID))
	code, raw = call(t, &admin, "GET", "/api/nodes/"+second.ID+"/person-steps", "")
	secondOut := decode[personStepsResponse](t, code, raw, 200)
	if find(secondOut, manual.CheckID).Owner.ID != other.ID {
		t.Fatal("named fallback not used for agent assignee", secondOut)
	}
	var assignmentCount, completionCount, refusals int
	if err := testDB.Admin.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='node.person_step_reassigned'),count(*) FILTER(WHERE type='entry_check.ticked'),count(*) FILTER(WHERE type='entry_checks.refused') FROM events WHERE tenant_id=$1 AND node_id=$2`, admin.TenantID, node.ID).Scan(&assignmentCount, &completionCount, &refusals); err != nil {
		t.Fatal(err)
	}
	if assignmentCount != 2 || completionCount != 2 || refusals != 1 {
		t.Fatalf("idempotent audit counts: assignments=%d manual completions=%d refusals=%d", assignmentCount, completionCount, refusals)
	}
}

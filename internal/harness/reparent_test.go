// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestManualReparentPermissionsAndUndo(t *testing.T) {
	f := fixture(t)
	events.New(f.db.App, events.WithUndoHandlers(harness.UndoHandlers())).Mount(f.mux)
	base := "/api/projects/" + f.project + "/harness-sessions"
	moveRight := func(id any) bool {
		t.Helper()
		w := f.call(f.person, "GET", base, nil, "")
		expect(t, w, 200)
		var sessions []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &sessions); err != nil {
			t.Fatal(err)
		}
		for _, s := range sessions {
			if s["id"] == id {
				return s["can_reparent"] == true
			}
		}
		t.Fatal("session missing from project list")
		return false
	}
	register := func(role string, parent any) map[string]any {
		t.Helper()
		w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": role, "harness_session_ref": "manual-ref-" + uid(), "worker_lease": "manual-lease-0000000000000000000000", "parent_harness_session_id": parent}, "")
		expect(t, w, 201)
		return decode(t, w)
	}
	old := register("coordinator", nil)
	next := register("coordinator", nil)
	child := register("worker", old["id"])
	path := base + "/" + child["id"].(string)
	input := map[string]any{"expected_revision": child["revision"], "parent_harness_session_id": next["id"]}
	// A member who registered both sessions owns them; a different member doesn't.
	other := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, e := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Other member')`, other.TenantID, other.ID)
		return e
	})
	dbtest.BindRole(t, f.db, other.TenantID, other.ID, "member")
	expect(t, f.call(other, "POST", path+"/reparent", input, ""), 403)
	expect(t, f.call(f.agent, "POST", path+"/reparent", input, ""), 403)
	expect(t, f.call(f.foreign, "POST", path+"/reparent", input, ""), 404)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "member")
	// Ownership of the child alone is insufficient.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, e := tx.Exec(t.Context(), `UPDATE harness_sessions SET owner_principal_id=$2 WHERE id=$1`, next["id"], other.ID)
		return e
	})
	expect(t, f.call(f.person, "POST", path+"/reparent", input, ""), 403)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, e := tx.Exec(t.Context(), `UPDATE harness_sessions SET owner_principal_id=$2 WHERE id=$1`, next["id"], f.person.ID)
		return e
	})
	w := f.call(f.person, "POST", path+"/reparent", input, "")
	expect(t, w, 200)
	result := decode(t, w)
	event := fmt.Sprint(result["event_id"])
	if result["undoable"] != true || result["session"].(map[string]any)["parent_harness_session_id"] != next["id"] {
		t.Fatal("move or undo permission missing")
	}
	expect(t, f.call(f.person, "POST", path+"/reparent", input, ""), 409)
	// A heartbeat increments the generation revision but does not invalidate undo.
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}, "manual-lease-0000000000000000000000"), 200)
	expect(t, f.call(f.person, "POST", "/api/events/"+event+"/undo", nil, ""), 201)
	got := decode(t, f.call(f.person, "GET", path, nil, ""))
	if got["parent_harness_session_id"] != old["id"] || !moveRight(child["id"]) {
		t.Fatal("undo lost original lead or rights")
	}
	// Stopped targets are not selectable; the write also denies them.
	expect(t, f.call(f.agent, "POST", base+"/"+next["id"].(string)+"/stop", map[string]any{"reason": "process_exited"}, "manual-lease-0000000000000000000000"), 200)
	input["expected_revision"] = got["revision"]
	expect(t, f.call(f.person, "POST", path+"/reparent", input, ""), 409)
	if moveRight(next["id"]) {
		t.Fatal("stopped target granted")
	}
	// Project path cannot move a foreign session even to a valid lead.
	expect(t, f.call(f.person, "POST", "/api/projects/"+uid()+"/harness-sessions/"+child["id"].(string)+"/reparent", input, ""), 404)
	// Legacy registrations have no inferred owner. An admin may move them,
	// but undo rechecks a role revoked since the original move.
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	third := register("coordinator", nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, e := tx.Exec(t.Context(), `UPDATE harness_sessions SET owner_principal_id=NULL WHERE id=ANY($1::uuid[])`, []string{child["id"].(string), third["id"].(string)})
		return e
	})
	input["parent_harness_session_id"] = third["id"]
	w = f.call(f.person, "POST", path+"/reparent", input, "")
	expect(t, w, 200)
	event = fmt.Sprint(decode(t, w)["event_id"])
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "member")
	expect(t, f.call(f.person, "POST", "/api/events/"+event+"/undo", nil, ""), 403)
	if decode(t, f.call(f.person, "GET", path, nil, ""))["parent_harness_session_id"] != third["id"] {
		t.Fatal("denied undo changed hierarchy")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func crossProjectFixture(t *testing.T) (*harnessFixture, string, string, string, map[string]any) {
	t.Helper()
	f := fixture(t)
	other, ticket := uid(), uid()
	f.addNode(t, other, "OTHER-1", "project", "", "Other project")
	f.addNode(t, ticket, "OTHER-2", "work", other, "Child work stays here")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET person_id=$1 WHERE id=$2`, f.person.ID, f.agent.AuthKeyID)
		return err
	})
	f.agent.KeyPersonID = f.person.ID
	f.agent.Scopes = []string{"harness.worker", "harness.read"}
	parent := f.registerSession(t, f.agent.ID, "coordinator", "", "lineage-parent-reference-"+uid(), "lineage-parent-lease-"+uid())
	body := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "management_mode": "unmanaged", "role": "worker", "ticket_node_id": ticket, "work_shape": "scout", "parent_harness_session_id": parent, "harness_session_ref": "lineage-child-reference-" + uid(), "worker_lease": "lineage-child-lease-" + uid()}
	return f, other, ticket, parent, body
}

// Risks: tenant/person leakage, wrong work attribution and accidental control
// authority from a display-only relationship. Exercise the migrated API, not a
// mocked graph, including legacy readers, history, replay and mixed cycles.
func TestCrossProjectChildrenKeepWorkAndPrivacy(t *testing.T) {
	f, other, ticket, parent, body := crossProjectFixture(t)
	base := "/api/projects/" + other + "/harness-sessions"
	w := f.call(f.agent, "POST", base, body, "")
	expect(t, w, 201)
	child := decode(t, w)
	id := child["id"].(string)
	assertChild := func(got map[string]any) {
		t.Helper()
		if got["project_id"] != other || got["ticket_node_id"] != ticket || got["work_shape"] != "scout" || got["parent_harness_session_id"] != nil || got["cross_project_parent_harness_session_id"] != parent || got["owner_principal_id"] != f.person.ID {
			t.Fatalf("child changed work/person/legacy-parent binding: %#v", got)
		}
	}
	assertChild(child)
	w = f.call(f.agent, "POST", base, body, "")
	expect(t, w, 201)
	assertChild(decode(t, w))
	if decode(t, w)["id"] != id {
		t.Fatal("replay duplicated the child")
	}
	// UUID spelling does not turn a legacy same-project writer into a
	// cross-project writer or cause it to take different hierarchy fences.
	legacyBody := map[string]any{}
	for k, v := range body {
		legacyBody[k] = v
	}
	legacyBody["ticket_node_id"] = f.ticket
	legacyBody["harness_session_ref"], legacyBody["worker_lease"] = "legacy-lineage-reference-"+uid(), "legacy-lineage-lease-"+uid()
	w = f.call(f.agent, "POST", "/api/projects/"+strings.ToUpper(f.project)+"/harness-sessions", legacyBody, "")
	expect(t, w, 201)
	legacyChild := decode(t, w)
	if legacyChild["parent_harness_session_id"] != parent || legacyChild["cross_project_parent_harness_session_id"] != nil || legacyChild["project_id"] != f.project {
		t.Fatal("same-project UUID spelling changed legacy lineage")
	}
	path := base + "/" + id
	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}, body["worker_lease"].(string))
	expect(t, w, 200)
	assertChild(decode(t, w))
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	assertChild(decode(t, w))
	for _, view := range []string{"current", "all"} {
		w = f.call(f.person, "GET", "/api/harness-sessions?view="+view+"&project="+other+"&limit=1", nil, "")
		expect(t, w, 200)
		page := decode(t, w)
		items := page["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["id"] != id || page["next_cursor"] != nil {
			t.Fatal("child project page duplicated or lost its child")
		}
		assertChild(items[0].(map[string]any))
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var legacy *string
		var owner string
		var events int
		if err := tx.QueryRow(t.Context(), `SELECT h.parent_id::text,l.owner_person_id::text FROM harness_sessions h JOIN harness_session_lineage l ON l.child_session_id=h.id AND l.tenant_id=h.tenant_id WHERE h.id=$1`, id).Scan(&legacy, &owner); err != nil {
			return err
		}
		if legacy != nil || owner != f.person.ID {
			t.Fatal("additive storage changed legacy parent or verified owner")
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.registered' AND after->>'id'=$1 AND NOT(after ? 'cross_project_parent_harness_session_id')`, id).Scan(&events); err != nil {
			return err
		}
		if events != 1 {
			t.Fatal("replay duplicated audit or audit exposed cross-project context")
		}
		return nil
	})
	reader := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Child project reader')`, reader.TenantID, reader.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member' AND builtin`, reader.TenantID, reader.ID, other)
		return err
	})
	w = f.call(reader, "GET", path, nil, "")
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), parent) || decode(t, w)["cross_project_parent_harness_session_id"] != nil {
		t.Fatal("inaccessible ancestor leaked into child detail")
	}
	w = f.call(reader, "POST", base, body, "")
	expect(t, w, 409) // RLS refuses even discovering the inaccessible parent.
	if !strings.Contains(w.Body.String(), "active visible parent") {
		t.Fatal("inaccessible parent failed for a different reason: " + w.Body.String())
	}
	w = f.call(f.foreign, "GET", "/api/harness-sessions", nil, "")
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), id) || strings.Contains(w.Body.String(), parent) {
		t.Fatal("lineage leaked across tenants")
	}
	var foreignParent string
	f.tx(t, f.foreign, func(tx pgx.Tx) error {
		var project, agent string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'FOREIGN-1','Foreign project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.foreign.TenantID).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Foreign agent') RETURNING id::text`, f.foreign.TenantID).Scan(&agent); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest) VALUES($1,$2,$3,'codex','fixture','unmanaged','coordinator',$4,$4) RETURNING id::text`, f.foreign.TenantID, project, agent, []byte("foreign-lineage-fixture")).Scan(&foreignParent)
	})
	body["parent_harness_session_id"] = foreignParent
	w = f.call(f.agent, "POST", base, body, "")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "active visible parent") {
		t.Fatal("foreign tenant parent failed for a different reason")
	}
	body["parent_harness_session_id"] = parent

	// Move only the legacy same-project parent: the mixed path still contains
	// A -> B -> A, and must be rejected before any parent mutation.
	grand := map[string]any{}
	for k, v := range body {
		grand[k] = v
	}
	grand["parent_harness_session_id"], grand["ticket_node_id"], grand["work_shape"], grand["role"] = id, nil, "unknown", "coordinator"
	grand["harness_session_ref"], grand["worker_lease"] = "lineage-grand-reference-"+uid(), "lineage-grand-lease-"+uid()
	w = f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", grand, "")
	expect(t, w, 201)
	grandID := decode(t, w)["id"]
	w = f.call(f.agent, "PATCH", "/api/projects/"+f.project+"/harness-sessions/"+parent+"/binding", map[string]any{"expected_revision": 1, "parent_harness_session_id": grandID, "work_shape": "unknown"}, "")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "hierarchy cycle") {
		t.Fatal("mixed cycle failed for a different reason: " + w.Body.String())
	}
	w = f.call(f.agent, "PATCH", path+"/binding", map[string]any{"expected_revision": 1, "parent_harness_session_id": id, "ticket_node_id": ticket, "work_shape": "scout"}, "")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "cannot be reparented") {
		t.Fatal("cross-project self-parent was not fenced")
	}

	refuse := func(want string) {
		t.Helper()
		body["harness_session_ref"], body["worker_lease"] = "refused-lineage-reference-"+uid(), "refused-lineage-lease-"+uid()
		w := f.call(f.agent, "POST", base, body, "")
		expect(t, w, 409)
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("refused for wrong reason: %s", w.Body.String())
		}
	}
	// An unresolved child must not borrow the key issuer or parent's person.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET person_id=NULL WHERE id=$1`, f.agent.AuthKeyID)
		return err
	})
	f.agent.KeyPersonID = ""
	refuse("verified canonical person")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET person_id=$1 WHERE id=$2`, f.person.ID, f.agent.AuthKeyID)
		return err
	})
	f.agent.KeyPersonID = f.person.ID
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET owner_principal_id=$2 WHERE id=$1`, parent, reader.ID)
		return err
	})
	refuse("run_person_conflict")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET owner_principal_id=NULL WHERE id=$1`, parent)
		return err
	})
	refuse("verified canonical person")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET owner_principal_id=$2,heartbeat_at=clock_timestamp()-interval '1 day' WHERE id=$1`, parent, f.person.ID)
		return err
	})
	refuse("active visible parent")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var sessions, links int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions WHERE project_id=$1`, other).Scan(&sessions); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_session_lineage`).Scan(&links); err != nil {
			return err
		}
		if sessions != 1 || links != 2 {
			t.Fatal("refusal left a partial generation or lineage")
		}
		return nil
	})
	// A run's starting person is independent of both parent and key issuer.
	// A missing run person is not repaired from either source, and success
	// must leave its immutable binding untouched.
	order, run := stateRun(t, f, other, "running", "LINEAGE-10")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=clock_timestamp() WHERE id=$1`, parent)
		return err
	})
	runBody := map[string]any{}
	for k, v := range body {
		runBody[k] = v
	}
	runBody["run_id"], runBody["work_order_id"], runBody["ticket_node_id"], runBody["work_shape"] = run, order, order, "ship"
	runBody["harness_session_ref"], runBody["worker_lease"] = "run-lineage-reference-"+uid(), "run-lineage-lease-"+uid()
	w = f.call(f.agent, "POST", base, runBody, "")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "verified canonical person") {
		t.Fatal("unresolved run used a fallback person: " + w.Body.String())
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET acting_person_id=$2 WHERE id=$1`, run, f.person.ID)
		return err
	})
	w = f.call(f.agent, "POST", base, runBody, "")
	expect(t, w, 201)
	runChild := decode(t, w)
	if runChild["run_id"] != run || runChild["owner_principal_id"] != f.person.ID || runChild["cross_project_parent_harness_session_id"] != parent {
		t.Fatal("cross-project registration changed run person or lineage")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var person string
		if err := tx.QueryRow(t.Context(), `SELECT acting_person_id::text FROM agent_runs WHERE id=$1`, run).Scan(&person); err != nil {
			return err
		}
		if person != f.person.ID {
			t.Fatal("registration rewrote the immutable run person")
		}
		return nil
	})
	// A mixed-project chain at the existing depth limit cannot grow again.
	deep := parent
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=clock_timestamp() WHERE id=$1`, parent); err != nil {
			return err
		}
		for level := 2; level <= 16; level++ {
			project := f.project
			if level%2 == 0 {
				project = other
			}
			var next string
			if err := tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,owner_principal_id) VALUES($1,$2,$3,'codex','fixture','unmanaged','coordinator',$4,$4,$5) RETURNING id::text`, f.person.TenantID, project, f.agent.ID, []byte(uid()), f.person.ID).Scan(&next); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO harness_session_lineage(tenant_id,child_session_id,parent_session_id,owner_person_id) VALUES($1,$2,$3,$4)`, f.person.TenantID, next, deep, f.person.ID); err != nil {
				return err
			}
			deep = next
		}
		return nil
	})
	body["parent_harness_session_id"], body["ticket_node_id"] = deep, f.ticket
	body["harness_session_ref"], body["worker_lease"] = "deep-lineage-reference-"+uid(), "deep-lineage-lease-"+uid()
	w = f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions", body, "")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "exceeds 16 ancestors") {
		t.Fatal("depth limit failed for a different reason: " + w.Body.String())
	}
}

// Risk: registration commits after access revocation. The database lock is the
// barrier: prove the request is waiting at the tenant fence, commit revocation,
// then require a permission refusal and no partial link. No elapsed-time proof.
func TestCrossProjectRegistrationRechecksRevokedAccess(t *testing.T) {
	f, other, _, _, body := crossProjectFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	blocker, err := f.db.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	var blockerPID uint32
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- f.call(f.agent, "POST", "/api/projects/"+other+"/harness-sessions", body, "")
		close(done)
	}()
	if lock := dbtest.BlockedOrDone(t, ctx, f.db.Admin, blockerPID, done); lock != "transactionid" {
		t.Fatalf("registration did not reach tenant fence: %s", lock)
	}
	if _, err := blocker.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.person.TenantID, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-response:
		expect(t, w, 403)
		if !strings.Contains(w.Body.String(), "forbidden") {
			t.Fatal("revocation failed for a different reason: " + w.Body.String())
		}
	case <-ctx.Done():
		t.Fatal("registration did not finish after revocation")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM harness_session_lineage)+(SELECT count(*) FROM harness_sessions WHERE project_id=$1)`, other).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("revoked registration left a partial child/link")
		}
		return nil
	})
}

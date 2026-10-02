// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func TestRegisteredPlacementUsesStarterAndFreezesAcrossReplay(t *testing.T) {
	f := fixture(t)
	var profile, alias string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := modelprefs.SeedKinds(t.Context(), tx, f.person.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,linked_to) VALUES($1,'person','Linked starter',$2) RETURNING id::text`, f.person.TenantID, f.person.ID).Scan(&alias); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'placement-pin','1','claude','anthropic','opus','high','strong') RETURNING id::text`, f.person.TenantID).Scan(&profile); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"area":"backend","complexity":"M","route_role":"build","assignee":"`+f.agent.ID+`"}' WHERE id=$1`, f.ticket); err != nil {
			return err
		}
		scope, err := modelprefs.SaveScope(t.Context(), tx, f.person, modelprefs.Scope{Level: "person", PersonID: &f.person.ID})
		if err != nil {
			return err
		}
		kind, _, err := modelprefs.LookupKind(t.Context(), tx, "backend", f.project)
		if err != nil {
			return err
		}
		return modelprefs.PutRow(t.Context(), tx, f.person, scope, kind.ID, modelprefs.Row{Cells: map[string]modelprefs.Cell{"normal": {Mode: "pinned", ProfileID: profile}}})
	})
	f.agent.KeyCreatorID = alias
	base := "/api/projects/" + f.project + "/harness-sessions"
	registration := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "placement-ref-" + uid(), "worker_lease": "placement-lease-" + uid(), "ticket_node_id": f.ticket, "work_shape": "ship", "model": "actual-unreported-model"}
	w := f.call(f.agent, "POST", base, registration, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	read := func(id string) json.RawMessage {
		var raw json.RawMessage
		f.tx(t, f.person, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT work_placement FROM harness_sessions WHERE id=$1`, id).Scan(&raw)
		})
		return raw
	}
	first := read(id)
	var placement modelregistry.WorkPlacement
	if err := json.Unmarshal(first, &placement); err != nil {
		t.Fatal(err)
	}
	if placement.PersonID == nil || *placement.PersonID != f.person.ID || placement.Kind != "backend" || placement.Bucket != "normal" || placement.ProjectID != f.project || placement.SetBy != "person" || placement.PlannedProfileID == nil || *placement.PlannedProfileID != profile {
		t.Fatal("registered placement lost canonical starter", placement)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_pref_cells SET mode='auto',profile_id=NULL WHERE mode='pinned'`)
		return err
	})
	expect(t, f.call(f.agent, "POST", base, registration, ""), 201)
	if string(read(id)) != string(first) {
		t.Fatal("registration replay recomputed placement")
	}
	// An operator key has no You setting even when another person binds it.
	f.agent.KeyCreatorID = ""
	w = f.call(f.agent, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "unbound-placement-ref-" + uid(), "worker_lease": "unbound-placement-lease-" + uid()}, "")
	expect(t, w, 201)
	path := base + "/" + decode(t, w)["id"].(string)
	current := decode(t, f.call(f.person, "GET", path, nil, ""))
	expect(t, f.call(f.person, "PATCH", path+"/binding", map[string]any{"expected_revision": current["revision"], "ticket_node_id": f.ticket, "work_shape": "ship"}, ""), 200)
	var bound modelregistry.WorkPlacement
	if err := json.Unmarshal(read(current["id"].(string)), &bound); err != nil {
		t.Fatal(err)
	}
	if bound.PersonID != nil || bound.Kind != "backend" {
		t.Fatal("binding borrowed the editor's You setting", bound)
	}
}

func TestManagedRegistrationCopiesDispatchPlacementAfterPreferencesChange(t *testing.T) {
	f := fixture(t)
	workorders.New(f.db.App).Mount(f.mux)
	agentruns.New(f.db.App).Mount(f.mux)
	var profile string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := modelprefs.SeedKinds(t.Context(), tx, f.person.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"area":"security","complexity":"L","route_role":"build-hard"}' WHERE id=$1`, f.ticket); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'dispatch-pin','1','codex','openai','gpt-6-astra','xhigh','frontier') RETURNING id::text`, f.person.TenantID).Scan(&profile); err != nil {
			return err
		}
		scope, err := modelprefs.SaveScope(t.Context(), tx, f.person, modelprefs.Scope{Level: "person", PersonID: &f.person.ID})
		if err != nil {
			return err
		}
		kind, _, err := modelprefs.LookupKind(t.Context(), tx, "security", f.project)
		if err != nil {
			return err
		}
		return modelprefs.PutRow(t.Context(), tx, f.person, scope, kind.ID, modelprefs.Row{Cells: map[string]modelprefs.Cell{"complex": {Mode: "pinned", ProfileID: profile}}})
	})
	w := f.call(f.person, "POST", "/api/work-orders", map[string]any{"title": "Placement dispatch", "parent_id": f.ticket, "criteria": []string{"Tests pass"}, "assignee_principal_id": f.agent.ID}, "")
	expect(t, w, 201)
	order := decode(t, w)
	orderID := order["node_id"].(string)
	w = f.call(f.person, "PATCH", "/api/work-orders/"+orderID, map[string]any{"expected_revision": order["revision"], "status": "ready"}, "")
	expect(t, w, 200)
	w = f.call(f.person, "POST", "/api/work-orders/"+orderID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": profile}, "")
	expect(t, w, 201)
	run := decode(t, w)
	var trace struct {
		Placement modelregistry.WorkPlacement `json:"work_placement"`
	}
	raw, _ := json.Marshal(run["trace"])
	if err := json.Unmarshal(raw, &trace); err != nil {
		t.Fatal(err)
	}
	if trace.Placement.Kind != "security" || trace.Placement.Bucket != "complex" || trace.Placement.PersonID == nil || *trace.Placement.PersonID != f.person.ID || trace.Placement.PlannedProfileID == nil || *trace.Placement.PlannedProfileID != profile {
		t.Fatal("dispatch placement", trace.Placement)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_pref_cells SET mode='auto',profile_id=NULL WHERE mode='pinned'`)
		return err
	})
	// A different creator starts the daemon; the saved run decision must win.
	f.agent.KeyCreatorID = ""
	w = f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions", map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "managed", "role": "worker", "harness_session_ref": "managed-placement-ref-" + uid(), "worker_lease": "managed-placement-lease-" + uid(), "ticket_node_id": f.ticket, "work_shape": "ship", "work_order_id": orderID, "run_id": run["id"]}, "")
	expect(t, w, 201)
	session := decode(t, w)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var same bool
		if err := tx.QueryRow(t.Context(), `SELECT s.work_placement=r.trace->'work_placement' FROM harness_sessions s JOIN agent_runs r ON r.id=s.run_id WHERE s.id=$1`, session["id"]).Scan(&same); err != nil {
			return err
		}
		if !same {
			t.Fatal("managed registration replaced dispatch placement")
		}
		return nil
	})
}

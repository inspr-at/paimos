// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workerlease"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// Risk: preferences/requester provenance invents an actor, or a retry rewrites
// the person a run acts for after admission.
func TestRunActingPersonIsExplicitAndWriteOnce(t *testing.T) {
	f := setup(t)
	run := f.run(t, f.order(t, nil))
	if run.ActingPersonID == nil || *run.ActingPersonID != f.person.ID {
		t.Fatal("personal start lost the signed-in person")
	}
	var otherPerson string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other actor') RETURNING id::text`, f.person.TenantID).Scan(&otherPerson)
	})
	bind := func(person string) error {
		return db.InTenant(tenant.WithPrincipal(t.Context(), f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
			if err := db.LockTenant(t.Context(), tx, f.person.TenantID); err != nil {
				return err
			}
			return agentruns.SetActingPersonTx(t.Context(), tx, f.person.TenantID, run.ID, person)
		})
	}
	for _, person := range []string{f.agent.ID, f.foreign.ID} {
		if err := bind(person); !errors.Is(err, agentruns.ErrActingPersonBinding) {
			t.Fatalf("invalid actor: %v", err)
		}
	}
	if err := bind(f.person.ID); err != nil {
		t.Fatal(err)
	}
	if err := bind(f.person.ID); err != nil {
		t.Fatalf("same actor replay: %v", err)
	}
	if err := bind(otherPerson); !errors.Is(err, agentruns.ErrActingPersonBinding) {
		t.Fatalf("changed actor: %v", err)
	}
	var got agentruns.Run
	f.call(t, f.person, "GET", "/api/runs/"+run.ID, nil, 200, &got)
	if got.ActingPersonID == nil || *got.ActingPersonID != f.person.ID {
		t.Fatalf("actor response lost binding: %+v", got.ActingPersonID)
	}
}

// Risk: a creation path substitutes an issuer, approver or preference person
// for the authoritative source; repeated admission emits duplicate bindings.
func TestRunPersonStartPathSourcesAndUnresolvedRefusal(t *testing.T) {
	f := setup(t)
	order := f.order(t, nil)
	original := f.run(t, order)
	project, recurrence, intent := uuid(), uuid(), uuid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		statements := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO nodes(tenant_id,id,kind_id,key,title) SELECT $1,$2,id,'PERSON-1','Person sources' FROM node_kinds WHERE tenant_id=$1 AND slug='project'`, []any{f.person.TenantID, project}},
			{`INSERT INTO recurrences(tenant_id,id,project_id,parent_id,template,trigger,created_by_principal_id,definition_scope) VALUES($1,$2,$3,$3,'{}','{"kind":"event"}',$4,'project')`, []any{f.person.TenantID, recurrence, project, f.person.ID}},
			{`INSERT INTO recurrence_definitions(tenant_id,recurrence_id,scope_type,scope_project_id,owner_principal_id,output_project_id,output_parent_id) VALUES($1,$2,'project',$3,$4,$3,$3)`, []any{f.person.TenantID, recurrence, project, f.person.ID}},
			{`INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,state) VALUES($1,$2,$3,'waiting_for_room')`, []any{f.person.TenantID, project, f.person.ID}},
			{`INSERT INTO native_session_intents(tenant_id,id,owner_person_id,project_id) VALUES($1,$2,$3,$4)`, []any{f.person.TenantID, intent, f.person.ID, project}},
		}
		for _, q := range statements {
			if _, err := tx.Exec(t.Context(), q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
	for _, tc := range []struct{ rule, source string }{
		{"personal", ""}, {"routine", recurrence}, {"lead", project}, {"review", original.ID}, {"fix", original.ID}, {"child", original.ID}, {"recovery", original.ID}, {"vendor_handoff", original.ID}, {"native_intent", intent},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			caller := f.other
			caller.KeyCreatorID = f.person.ID // available provenance must never become a fallback
			if tc.rule == "personal" {
				caller = f.person
			}
			var run string
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
					return err
				}
				person, err := workqueue.ResolveRunPersonTx(t.Context(), tx, caller, tc.rule, tc.source)
				if err != nil {
					return err
				}
				if person != f.person.ID {
					t.Fatalf("%s picked %s", tc.rule, person)
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,prefs_person_id) VALUES($1,$2,$3,$3) RETURNING id::text`, f.person.TenantID, order.NodeID, f.agent.ID).Scan(&run); err != nil {
					return err
				}
				change, err := workqueue.SetActingPersonTx(t.Context(), tx, f.person.TenantID, run, person, tc.rule)
				if err != nil {
					return err
				}
				if change == nil {
					t.Fatal("first binding has no audit")
				}
				replay, err := workqueue.SetActingPersonTx(t.Context(), tx, f.person.TenantID, run, person, tc.rule)
				if err != nil {
					return err
				}
				if replay != nil {
					t.Fatal("replay duplicated binding")
				}
				_, err = events.Append(t.Context(), tx, f.person, *change)
				return err
			})
			if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND acting_person_id=$2`, run, f.person.ID); n != 1 {
				t.Fatal("person was not recorded")
			}
			if n := f.count(t, f.person, `SELECT count(*) FROM events WHERE type='run.person_bound' AND after->>'run_id'=$1 AND after->>'person_id'=$2 AND after->>'source_rule'=$3`, run, f.person.ID, tc.rule); n != 1 {
				t.Fatal("binding audit not exact")
			}
			err := db.InTenant(tenant.WithPrincipal(t.Context(), f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
				caller = f.other
				caller.KeyCreatorID = f.person.ID
				_, err := workqueue.ResolveRunPersonTx(t.Context(), tx, caller, tc.rule, uuid())
				return err
			})
			var refusal *workqueue.PersonAdmissionError
			if !errors.As(err, &refusal) || refusal.Code != "run_person_unresolved" {
				t.Fatalf("missing source fell back: %v", err)
			}
		})
	}
	// An ordinary unbound agent key has an issuer, but no chosen person.
	path := "/api/work-orders/" + order.NodeID + "/runs"
	raw, _ := json.Marshal(map[string]string{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile})
	w := f.request(f.agent, "POST", path, string(raw), f.token)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"run_person_unresolved"`) {
		t.Fatalf("unresolved start: %d %s", w.Code, w.Body.String())
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM events WHERE type='run.admission_refused' AND after->>'reason'='run_person_unresolved'`); n != 1 {
		t.Fatal("refusal audit rolled back")
	}
}

// Risk: a person requesting a restart replaces the original person, or a shared
// computer picks another run's person for a child without its worker proof.
func TestRunPersonRecoveryAndChildInheritServerRun(t *testing.T) {
	f := setup(t)
	project := uuid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title) SELECT $1,$2,id,'PERSON-2','Child proof' FROM node_kinds WHERE tenant_id=$1 AND slug='project'`, f.person.TenantID, project)
		return err
	})
	order := f.order(t, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, order.NodeID, project)
		return err
	})
	original := f.run(t, order)
	requester := tenant.Principal{TenantID: f.person.TenantID, ID: uuid(), Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Restart requester')`, requester.TenantID, requester.ID)
		return err
	})
	// The second requester holds real permission, but remains provenance only.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE key='admin'`, requester.TenantID, requester.ID)
		return err
	})
	var recovered agentruns.Run
	f.call(t, requester, "POST", "/api/work-orders/"+order.NodeID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "retry_of_run_id": original.ID}, 201, &recovered)
	if recovered.ActingPersonID == nil || *recovered.ActingPersonID != f.person.ID {
		t.Fatal("recovery replaced original person")
	}
	lease := strings.Repeat("person-child-proof", 3)
	sum := sha256.Sum256([]byte("aeon.harness.lease\x00" + lease))
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='running' WHERE id=$1`, original.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,run_id,harness,host,management,role,ref_digest,lease_digest,owner_principal_id) VALUES($1,$2,$3,$4,'codex','local','managed','coordinator',$5,$6,$7)`, f.person.TenantID, project, f.agent.ID, original.ID, []byte("child-parent-ref"), sum[:], f.person.ID)
		return err
	})
	childOrder := f.order(t, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, childOrder.NodeID, project)
		return err
	})
	raw, _ := json.Marshal(map[string]string{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "parent_run_id": original.ID})
	call := func(proof string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/work-orders/"+childOrder.NodeID+"/runs", strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(context.Background(), f.agent))
		if proof != "" {
			workerlease.Install(r.Header, f.token, proof)
		} else {
			r.Header.Set("Authorization", "Bearer "+f.token)
		}
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w
	}
	for _, proof := range []string{"", strings.Repeat("wrong-child-proof", 3)} {
		if w := call(proof); w.Code != 403 {
			t.Fatalf("unproven parent accepted: %d %s", w.Code, w.Body.String())
		}
	}
	w := call(lease)
	if w.Code != 201 {
		t.Fatalf("child: %d %s", w.Code, w.Body.String())
	}
	var child agentruns.Run
	if err := json.Unmarshal(w.Body.Bytes(), &child); err != nil {
		t.Fatal(err)
	}
	if child.ActingPersonID == nil || *child.ActingPersonID != f.person.ID {
		t.Fatal("child person not inherited")
	}
}

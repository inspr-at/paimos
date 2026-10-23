// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/agentverification"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: a lost response creates duplicate leaves/runs, changed options reuse an
// old run, unsupported read-only becomes writable, or revoked start authority
// survives final claim. Every request uses the real composite and run handlers.
func TestRunOnceBindsContextOptionsAndCurrentAuthority(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	f.pairedRunComputer(t)
	project := uuid()
	var revision time.Time
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title,fields) SELECT $1,$2,id,'ONCE-1','One-off project','{"project_key":"ONCE"}' FROM node_kinds WHERE slug='project' RETURNING updated_at`, f.person.TenantID, project).Scan(&revision)
	})
	path := "/api/nodes/" + project + "/run-once"
	in := map[string]any{"request_id": uuid(), "expected_updated_at": revision, "task": "Inspect supplied context", "instruction": "  Preserve this exact instruction.\n", "agent_principal_id": f.agent.ID, "model_profile_id": f.profile}
	call := func(input map[string]any, status int) agentruns.Run {
		t.Helper()
		raw, _ := json.Marshal(input)
		w := f.request(f.person, "POST", path, string(raw), "")
		if w.Code != status {
			t.Fatalf("run once: %d, want %d: %s", w.Code, status, w.Body.String())
		}
		if status == 409 && !strings.Contains(w.Body.String(), "Read-only is not available") && !strings.Contains(w.Body.String(), "already bound") && !strings.Contains(w.Body.String(), "active run") && !strings.Contains(w.Body.String(), "context changed") {
			t.Fatalf("wrong refusal: %s", w.Body.String())
		}
		var run agentruns.Run
		if status == 201 {
			if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
				t.Fatal(err)
			}
		}
		return run
	}
	call(in, 409) // Codex's sandbox is not a qualified no-tools boundary.
	if f.count(t, f.person, `SELECT count(*) FROM nodes WHERE parent_id=$1`, project) != 0 {
		t.Fatal("refused read-only start committed a leaf")
	}
	in["repository_mutation_allowed"] = true
	run := call(in, 201)
	if run.ID == in["request_id"] || !run.RunOnce || !run.RepositoryMutationAllowed || run.ReadOnlyReview || run.Purpose != "managed" || run.Instruction != in["instruction"] || run.ActingPersonID == nil || *run.ActingPersonID != f.person.ID {
		t.Fatalf("lost one-off binding: %+v", run)
	}
	if again := call(in, 201); again.ID != run.ID {
		t.Fatal("retry created another run")
	}
	if f.count(t, f.person, `SELECT count(*) FROM nodes WHERE parent_id=$1 AND title='Run once: Inspect supplied context' AND fields->>'hide_from_release_notes'='true'`, project) != 1 || f.count(t, f.person, `SELECT count(*) FROM agent_runs`) != 1 {
		t.Fatal("retry duplicated its leaf or run")
	}
	in["instruction"] = "A different instruction"
	call(in, 409)
	in["instruction"] = run.Instruction
	in["repository_mutation_allowed"] = false
	call(in, 409)
	in["repository_mutation_allowed"] = true
	// A still-authorized person passes the final synthetic-person recheck;
	// the ordinary account, reservation and claim fences must also succeed.
	claim := func(run agentruns.Run, ids []string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(claimBody(ids))
		r := httptest.NewRequest("POST", "/api/runs/"+run.ID+"/claim", strings.NewReader(string(raw)))
		r = r.WithContext(tenant.WithPrincipal(r.Context(), f.agent))
		r.Header.Set("Authorization", "Bearer "+f.token)
		r.Header.Set(agentverification.RunOncePolicyHeader, agentverification.RunOncePolicy)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w
	}
	w := claim(run, f.reserve(t, run))
	var accepted agentruns.Run
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || w.Code != 200 || accepted.Status != "starting" || accepted.ID != run.ID || accepted.ActingPersonID == nil || *accepted.ActingPersonID != f.person.ID || !accepted.RunOnce || !accepted.RepositoryMutationAllowed {
		t.Fatalf("authorized one-off claim: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='starting'`, run.ID) != 1 {
		t.Fatal("authorized claim did not persist")
	}
	// A selected leaf uses its task body when the optional instruction is empty.
	var leaf string
	var leafRevision time.Time
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT t.id::text,t.updated_at FROM nodes o JOIN nodes t ON t.id=o.parent_id WHERE o.id=$1`, run.OrderID).Scan(&leaf, &leafRevision)
	})
	path = "/api/nodes/" + leaf + "/run-once"
	in["request_id"], in["expected_updated_at"], in["instruction"] = uuid(), leafRevision, ""
	delete(in, "task")
	call(in, 409) // A new instruction/restriction cannot reuse an active run.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='cancelled',ended_at=clock_timestamp() WHERE id=$1`, run.ID)
		return err
	})
	// Qualify a real read-only profile without changing account permissions.
	claudeProfile := uuid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'one-off-reader','1','claude','anthropic','fixture-reader','high','strong')`, f.person.TenantID, claudeProfile)
		return err
	})
	in["model_profile_id"] = claudeProfile
	in["repository_mutation_allowed"] = false
	readOnly := call(in, 201)
	if readOnly.RepositoryMutationAllowed || !readOnly.RunOnce || readOnly.ReadOnlyReview || readOnly.Instruction != "Inspect supplied context" {
		t.Fatalf("read-only/fallback lost: %+v", readOnly)
	}
	// The ordinary reservation fixture suffices: claim must refuse the
	// person's revoked run.create BEFORE adapter/account validation.
	ids := f.reserve(t, readOnly)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'once_observer','One-off observer') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read'),($1,$2,'work_orders.write')`, f.person.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1 AND scope_type='workspace'`, f.person.ID, role)
		return err
	})
	w = claim(readOnly, ids)
	if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"permission_revoked"`) {
		t.Fatalf("wrong final-claim refusal: %d %s", w.Code, w.Body.String())
	}
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='queued'`, readOnly.ID) != 1 {
		t.Fatal("revoked claim mutated the run")
	}
	if f.count(t, f.person, `SELECT count(*) FROM events WHERE type='run.admission_refused' AND after->>'reason'='permission_revoked'`) != 1 {
		t.Fatal("revoked one-off claim lost its refusal audit")
	}
	t.Run("settlement", exerciseRunOnceSettlement)
}

// Risks: false Done, duplicate settlement, overwriting a person's decision or
// retaining revoked write authority. Extend the already-classified Run once
// behaviour guard through the real launch, claim, telemetry and cancel routes.
func exerciseRunOnceSettlement(t *testing.T) {
	for _, tc := range []struct {
		name, status, errorCode, state, reason string
	}{
		{"success", "completed", "", "done", ""},
		{"selected-leaf", "completed", "", "done", ""},
		{"failed", "failed", "turn_failed", "open", "turn_failed"},
		{"cancelled", "cancelled", "decision_refused", "open", "decision_refused"},
		{"queued-cancel", "cancelled", "", "open", "cancelled_by_person"},
		{"person-status", "completed", "", "blocked", "ticket_changed"},
		{"person-away-and-back", "completed", "", "open", "ticket_changed"},
		{"person-edit", "completed", "", "open", "ticket_changed"},
		{"permission-revoked", "completed", "", "open", "permission_revoked"},
		{"done-check", "completed", "", "open", "entry_checks_failed"},
		{"unconfirmed-exit", "completed", "", "open", "process_exit_unconfirmed"},
		{"completed-with-error", "completed", "turn_failed", "open", "turn_failed"},
		{"not-started", "completed", "", "open", "process_not_started"},
		{"historical", "completed", "", "open", "launch_revision_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			nodes.New(f.d.App, nil).Mount(f.mux)
			f.pairedRunComputer(t)
			var contextID string
			var revision time.Time
			if tc.name == "selected-leaf" {
				contextID = f.ticket(t, "open", "medium", nil)
				f.tx(t, f.person, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT updated_at FROM nodes WHERE id=$1`, contextID).Scan(&revision)
				})
			} else {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,'SETTLE-1','Run once settlement','{"project_key":"SETTLE"}' FROM node_kinds WHERE slug='project' RETURNING id::text,updated_at`, f.person.TenantID).Scan(&contextID, &revision)
				})
			}
			if tc.name == "done-check" {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=jsonb_set(fields,'{checks,done}','[{"id":"manual:signoff","label":"Person signs off"}]') WHERE kind_id=(SELECT id FROM node_kinds WHERE slug='definition')`)
					return err
				})
			}
			input := map[string]any{"request_id": uuid(), "expected_updated_at": revision, "agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "repository_mutation_allowed": true}
			if tc.name != "selected-leaf" {
				input["task"] = "Inspect supplied context"
			}
			launchPath := "/api/nodes/" + contextID + "/run-once"
			var run agentruns.Run
			f.call(t, f.person, "POST", launchPath, input, 201, &run)
			var ticket string
			f.tx(t, f.person, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT parent_id::text FROM nodes WHERE id=$1`, run.OrderID).Scan(&ticket)
			})
			path := "/api/runs/" + run.ID
			resultURL := "/api/runs/" + run.ID
			var report map[string]any
			if tc.name == "queued-cancel" {
				f.reserve(t, run)
				f.call(t, f.person, "POST", path+"/cancel", nil, 200, &run)
				f.call(t, f.person, "POST", path+"/cancel", nil, 200, nil)
				if f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, run.ID) != 0 {
					t.Fatal("queued cancellation retained a reservation")
				}
			} else {
				ids := f.reserve(t, run)
				raw, _ := json.Marshal(claimBody(ids))
				r := httptest.NewRequest("POST", path+"/claim", strings.NewReader(string(raw)))
				r = r.WithContext(tenant.WithPrincipal(r.Context(), f.agent))
				r.Header.Set("Authorization", "Bearer "+f.token)
				r.Header.Set(agentverification.RunOncePolicyHeader, agentverification.RunOncePolicy)
				w := httptest.NewRecorder()
				f.mux.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("claim: %d %s", w.Code, w.Body.String())
				}
				if tc.name == "success" {
					sessionID := uuid()
					f.tx(t, f.person, func(tx pgx.Tx) error {
						_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,run_id,harness,host,management,role,work_shape,capabilities,ref_digest,lease_digest,phase,stopped_at,stop_reason) VALUES($1,$2,$3,$4,$5,'codex','test','unmanaged','worker','unknown','{}',decode(repeat('ab',32),'hex'),decode(repeat('cd',32),'hex'),'stopped',clock_timestamp(),'completed')`, f.person.TenantID, sessionID, contextID, f.agent.ID, run.ID)
						return err
					})
					resultURL = "/agents/" + sessionID
				}
				switch tc.name {
				case "person-status", "person-away-and-back":
					f.call(t, f.person, "PATCH", "/api/nodes/"+ticket, map[string]any{"state": "blocked"}, 200, nil)
					if tc.name == "person-away-and-back" {
						f.call(t, f.person, "PATCH", "/api/nodes/"+ticket, map[string]any{"state": "open"}, 200, nil)
					}
				case "person-edit":
					f.call(t, f.person, "PATCH", "/api/nodes/"+ticket, map[string]any{"title": "Person updated this task"}, 200, nil)
				case "permission-revoked":
					permissions, ok := authz.BuiltinPermissions("admin")
					if !ok {
						t.Fatal("admin role unavailable")
					}
					f.tx(t, f.person, func(tx pgx.Tx) error {
						var role string
						if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'settlement_observer','Settlement observer') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
							return err
						}
						// Revoke only ticket writes. Keep the creator's live telemetry
						// ceiling so the exact run can still report its outcome.
						if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,permission FROM unnest($3::text[]) permission WHERE permission<>'nodes.write'`, f.person.TenantID, role, permissions); err != nil {
							return err
						}
						_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1 AND scope_type='workspace'`, f.person.ID, role)
						return err
					})
					f.call(t, f.person, "GET", "/api/nodes/"+ticket, nil, 200, nil)
					f.call(t, f.person, "PATCH", "/api/nodes/"+ticket, map[string]any{"state": "done"}, 403, nil)
				case "historical":
					f.tx(t, f.person, func(tx pgx.Tx) error {
						_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET trace=trace#-'{run_once,ticket}' WHERE id=$1`, run.ID)
						return err
					})
				}
				report = map[string]any{"sequence": 1, "kind": "finished", "status": tc.status, "error_code": tc.errorCode, "process_state": "exited"}
				if tc.name == "unconfirmed-exit" {
					report["process_state"] = "unconfirmed"
				}
				if tc.name == "not-started" {
					report["process_state"] = "not_attempted"
				}
				f.call(t, f.agent, "POST", path+"/telemetry", report, 200, &run)
				f.call(t, f.agent, "POST", path+"/telemetry", report, 200, nil)
			}
			if run.Status != tc.status || run.EndedAt == nil {
				t.Fatalf("run did not settle: %+v", run)
			}
			if f.count(t, f.person, `SELECT count(*) FROM nodes WHERE id=$1 AND state=$2 AND deleted_at IS NULL`, ticket, tc.state) != 1 {
				t.Fatalf("ticket did not retain expected state %s", tc.state)
			}
			if f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='run_once.settled' AND after->>'run_id'=$2 AND after->>'status'=$3 AND after->>'reason'=$4 AND (after->>'ticket_closed')::bool=$5 AND after->>'result_url'=$6`, ticket, run.ID, tc.status, tc.reason, tc.state == "done", resultURL) != 1 {
				t.Fatal("missing, divergent or duplicated settlement receipt")
			}
			if f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='comment.created' AND after->>'run_id'=$2 AND position($3 in after->>'body_markdown')>0 AND position($4 in after->>'body_markdown')>0`, ticket, run.ID, resultURL, tc.reason) != 1 {
				t.Fatal("missing, divergent or duplicated result comment")
			}
			closed := int64(0)
			if tc.state == "done" {
				closed = 1
			}
			if got := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='node.updated' AND after->>'state'='done' AND metadata->>'run_id'=$2`, ticket, run.ID); got != closed {
				t.Fatalf("status transition audit count %d, want %d", got, closed)
			}
			if tc.name == "success" {
				var again agentruns.Run
				f.call(t, f.person, "POST", launchPath, input, 201, &again)
				if again.ID != run.ID {
					t.Fatal("settlement invalidated the launch retry")
				}
				f.call(t, f.person, "PATCH", "/api/nodes/"+ticket, map[string]any{"state": "open"}, 200, nil)
				f.call(t, f.agent, "POST", path+"/telemetry", report, 200, nil)
				if f.count(t, f.person, `SELECT count(*) FROM nodes WHERE id=$1 AND state='open'`, ticket) != 1 || f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='run_once.settled'`, ticket) != 1 {
					t.Fatal("replay overrode a later person decision")
				}
			}
		})
	}
}

// Risk: a malformed persisted binding poisons every list page (or hides its
// next page), while a direct read or claim must continue to fail closed.
func TestRunOnceMalformedBindingDoesNotPoisonLists(t *testing.T) {
	f := setup(t)
	order := f.order(t, nil)
	good := f.run(t, order)
	bad := uuid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,id,work_order_id,agent_principal_id,model_profile_id,requested_model,trace,created_at)
 SELECT tenant_id,$2,work_order_id,agent_principal_id,model_profile_id,requested_model,'{"run_once":{"repository_mutation_allowed":"broken"}}',created_at+interval '1 second' FROM agent_runs WHERE id=$1`, good.ID, bad)
		return err
	})
	w := f.request(f.person, "GET", "/api/runs?limit=1", "", "")
	var page struct {
		Items []agentruns.Run `json:"items"`
		Next  *string         `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Items) != 0 || page.Next == nil {
		t.Fatalf("malformed first page: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	w = f.request(f.person, "GET", "/api/runs?limit=1&cursor="+*page.Next, "", "")
	page.Next = nil
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Items) != 1 || page.Items[0].ID != good.ID || page.Next != nil {
		t.Fatalf("valid following page lost: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	// Polling advertises the policy explicitly; use the real request handler.
	r := httptest.NewRequest("GET", "/api/runs/queued", nil)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), f.agent))
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set(agentverification.RunOncePolicyHeader, agentverification.RunOncePolicy)
	w = httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	var queued []agentruns.Run
	if err := json.Unmarshal(w.Body.Bytes(), &queued); err != nil || w.Code != 200 || len(queued) != 1 || queued[0].ID != good.ID {
		t.Fatalf("queued list poisoned: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	w = f.request(f.person, "GET", "/api/runs/"+bad, "", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "invalid one-off run binding") {
		t.Fatalf("malformed direct read did not fail closed: %d %s", w.Code, w.Body.String())
	}
}

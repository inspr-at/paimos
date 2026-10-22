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
	if run.ID != in["request_id"] || !run.RunOnce || !run.RepositoryMutationAllowed || run.ReadOnlyReview || run.Purpose != "managed" || run.Instruction != in["instruction"] || run.ActingPersonID == nil || *run.ActingPersonID != f.person.ID {
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
	raw, _ := json.Marshal(claimBody(ids))
	r := httptest.NewRequest("POST", "/api/runs/"+readOnly.ID+"/claim", strings.NewReader(string(raw)))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), f.agent))
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set(agentverification.RunOncePolicyHeader, agentverification.RunOncePolicy)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"permission_revoked"`) {
		t.Fatalf("wrong final-claim refusal: %d %s", w.Code, w.Body.String())
	}
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='queued'`, readOnly.ID) != 1 {
		t.Fatal("revoked claim mutated the run")
	}
	if f.count(t, f.person, `SELECT count(*) FROM events WHERE type='run.admission_refused' AND after->>'reason'='permission_revoked'`) != 1 {
		t.Fatal("revoked one-off claim lost its refusal audit")
	}
}

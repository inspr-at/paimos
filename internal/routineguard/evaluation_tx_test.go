// SPDX-License-Identifier: AGPL-3.0-only
package routineguard_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risks R1/R7/R15: a verdict request not surviving before dispatch, replay
// clearing an exact hold, changed actions reusing evidence, and owner revocation
// authorizing a final write. Real RLS/fences are exercised; no model is launched.
func TestEvaluationRequestPersistsBeforeDispatchAndRechecksOwner(t *testing.T) {
	d := dbtest.Open(t)
	ctx := dbtest.Seed(t.Context())
	p := tenant.Principal{TenantID: "10000000-0000-4000-8000-000000000001", Kind: tenant.Person}
	agent := tenant.Principal{TenantID: p.TenantID, Kind: tenant.Agent, Scopes: []string{"nodes.write", "harness.control", "models.read"}}
	var project, parent, workKind string
	in := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, d.App, p.TenantID, fn); err != nil {
			t.Fatal(err)
		}
	}
	in(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'evaluation','Evaluation')`, p.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Coordinator') RETURNING id::text`, p.TenantID).Scan(&agent.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'EVAL','Evaluation' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,project_id) SELECT $1,id,'EVAL-1','Assigned work',$2,$2 FROM node_kinds WHERE slug='work' RETURNING id::text`, p.TenantID, project).Scan(&parent); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `SELECT aeon_seed_work_kinds($1)`, p.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='backend' AND project_id IS NULL AND archived_at IS NULL`).Scan(&workKind)
	})
	dbtest.BindRole(t, d, p.TenantID, p.ID, "owner")
	dbtest.BindRole(t, d, p.TenantID, agent.ID, "admin")
	mux := http.NewServeMux()
	recurrences.New(d.App).Mount(mux)
	modelregistry.New(d.App).Mount(mux)
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		if out.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, out.Code, out.Body.String())
		}
		return out.Body.Bytes()
	}
	call("GET", "/api/models", nil, 200)
	input := recurrences.Input{ProjectID: project, ParentID: parent, Template: recurrences.Template{Title: "Inspect assigned source", Description: "Read code", Criteria: []string{"Report findings"}, EstimateHours: 1, Priority: "normal", Type: "work"}, Trigger: recurrences.Trigger{Kind: "time", RRULE: "FREQ=WEEKLY;BYDAY=MO", TimeOfDay: "09:00", Timezone: "Europe/Vienna"}, QueueEach: true, OverlapPolicy: "create", CatchUpPolicy: "one", Definition: &recurrences.Definition{Scope: recurrences.DefinitionScope{Kind: "project", ProjectID: project}, OwnerPrincipalID: p.ID, Assignment: &recurrences.Assignment{Goal: "Review assigned source", Role: "build", WorkKindID: workKind, Sources: []recurrences.SourceReference{}, AllowedActions: []string{"pr.open"}, RuntimeRequirements: recurrences.RuntimeRequirements{NeedsNativeHost: true, RuntimeClass: "native_coding"}, Budget: recurrences.DefinitionBudget{Mode: "off"}}}}
	var recurrence recurrences.Recurrence
	if err := json.Unmarshal(call("POST", "/api/recurrences", input, 201), &recurrence); err != nil {
		t.Fatal(err)
	}
	var occurrence recurrences.Occurrence
	if err := json.Unmarshal(call("POST", "/api/recurrences/"+recurrence.ID+"/run-now", map[string]string{"idempotency_key": "evaluation"}, 200), &occurrence); err != nil {
		t.Fatal(err)
	}
	if occurrence.Run == nil || occurrence.Run.AgentRunID == nil || occurrence.NodeID == nil {
		t.Fatal("missing persisted run fixture")
	}
	b := routineguard.ActionBinding{TenantID: p.TenantID, RunID: occurrence.Run.ID, AuthorRunID: *occurrence.Run.AgentRunID, ProjectID: project, TargetID: *occurrence.NodeID, TargetRevision: 1, Kind: "pr.open", PayloadDigest: strings.Repeat("a", 64), HeadSHA: strings.Repeat("b", 40)}
	in(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET model_profile_id=(SELECT id FROM model_profiles WHERE harness='codex' AND enabled ORDER BY created_at,id LIMIT 1),requested_model=(SELECT model FROM model_profiles WHERE harness='codex' AND enabled ORDER BY created_at,id LIMIT 1),effective_model=(SELECT model FROM model_profiles WHERE harness='codex' AND enabled ORDER BY created_at,id LIMIT 1),model_evidence='vendor_reported' WHERE id=$1`, b.AuthorRunID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO routine_actions(tenant_id,run_id,action_key,kind,request_digest,target_node_id,target_revision) VALUES($1,$2,'evaluate','pr.open',$3,$4,1) RETURNING id::text`, p.TenantID, b.RunID, b.PayloadDigest, b.TargetID).Scan(&b.ActionID)
	})
	c := routineguard.Context{Checkpoint: "action", Action: b.Kind, Text: "Inspect assigned source", PayloadBytes: 100}
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	prepare := func(at time.Time) (routineguard.EvaluationRequest, error) {
		var request routineguard.EvaluationRequest
		err := db.InTenant(ctx, d.App, p.TenantID, func(tx pgx.Tx) error {
			if err := db.LockTree(t.Context(), tx, p.TenantID); err != nil {
				return err
			}
			var err error
			request, err = routineguard.PrepareEvaluationTx(t.Context(), tx, agent, b, c, at)
			return err
		})
		return request, err
	}
	first, err := prepare(now)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := prepare(now.Add(time.Second))
	if err != nil || replayed.Digest != first.Digest {
		t.Fatalf("request replay changed: %+v %v", replayed, err)
	}
	in(func(tx pgx.Tx) error {
		var count int
		var pending bool
		err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM routine_guard_evaluations WHERE checkpoint='action'),a.state='pending' AND a.approval_id IS NULL AND e.decision#>>'{request,digest}'=$2 AND e.decision->>'wait_reason'='evaluator_unavailable' FROM routine_actions a JOIN routine_guard_evaluations e ON e.id=a.evaluation_id AND e.tenant_id=a.tenant_id WHERE a.id=$1`, b.ActionID, first.Digest).Scan(&count, &pending)
		if err == nil && (count != 1 || !pending) {
			t.Fatal("request not durable/default-off or replay duplicated")
		}
		return err
	})
	// Changed payload/head requires a new request and keeps old audit evidence.
	in(func(tx pgx.Tx) error {
		b.PayloadDigest, b.HeadSHA = strings.Repeat("c", 64), strings.Repeat("d", 40)
		_, err := tx.Exec(t.Context(), `UPDATE routine_actions SET request_digest=$2 WHERE id=$1`, b.ActionID, b.PayloadDigest)
		return err
	})
	changed, err := prepare(now.Add(2 * time.Second))
	if err != nil || changed.Digest == first.Digest {
		t.Fatalf("changed action reused request: %v", err)
	}
	in(func(tx pgx.Tx) error {
		if err := db.LockTree(t.Context(), tx, p.TenantID); err != nil {
			return err
		}
		pending := []events.Change{}
		decision, err := routineguard.RecordEvaluationTx(t.Context(), tx, agent, b, c, b.AuthorRunID, []byte("completed without an explicit verdict"), now.Add(3*time.Second), &pending)
		if err == nil && (decision.CanExecute() || len(pending) != 0) {
			t.Fatal("missing result created execution or person approval")
		}
		return err
	})
	in(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, p.ID)
		return err
	})
	if _, err = prepare(now.Add(4 * time.Second)); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("revoked owner wrong failure: %v", err)
	}
	in(func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM routine_guard_evaluations WHERE checkpoint='action'`).Scan(&count)
		if err == nil && count != 3 {
			t.Fatal("revoked owner left evaluation write")
		}
		return err
	})
}

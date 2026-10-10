// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risks R1/R2/R3/R7/R15: person-only rule writes, tenant/private scope leakage,
// stale administrator edits, unbound evaluation evidence and accidental starts.
func TestRoutineGuardrailsPersonRevisionScopeAndStoredDecisions(t *testing.T) {
	f := setup(t)
	in := f.input()
	in.Definition = &Definition{Scope: DefinitionScope{Kind: "project", ProjectID: f.project}, OwnerPrincipalID: f.p.ID}
	item := f.create(in)
	route := "/api/recurrences/" + item.ID + "/guardrails"
	var baseline routineguard.Policy
	if err := json.Unmarshal(f.call(f.p, "GET", route, nil, 200), &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.HardVersion != routineguard.HardVersion || baseline.PresetVersion != routineguard.PresetVersion || len(baseline.Rules) != 6 {
		t.Fatal("missing compiled policy")
	}
	rule := routineguard.Rule{ID: "custom.large", Checkpoint: "both", Method: "size", Limit: 128, Result: routineguard.Block}
	request := func(scope string, revision int64, rules []routineguard.Rule, reason string) any {
		return map[string]any{"scope": scope, "expected_revision": revision, "rules": rules, "reason": reason}
	}
	f.call(f.p, "PUT", route, request("project", 0, []routineguard.Rule{rule}, ""), 200)
	f.call(f.p, "PUT", route, request("project", 0, nil, "stale"), 409)
	member := projectPrincipal(f, "member")
	f.call(member, "PUT", route, request("project", 1, nil, "claimed admin"), 403)
	agent := tenant.Principal{ID: f.p.ID, TenantID: f.p.TenantID, Kind: tenant.Agent}
	f.call(agent, "PUT", route, request("project", 1, nil, "claimed admin"), 403)
	hard := routineguard.Rule{ID: "hard.protected_paths_v1", Checkpoint: "both", Method: "size", Limit: 1, Result: routineguard.Allow}
	f.call(f.p, "PUT", route, request("project", 1, []routineguard.Rule{hard}, "override hard"), 400)
	f.call(f.p, "PUT", route, request("project", 1, nil, ""), 403)
	f.call(f.p, "PUT", route, request("project", 1, nil, "Approved removal of optional size rule"), 200)
	f.call(f.p, "PUT", route, request("tenant", 0, []routineguard.Rule{rule}, ""), 200)
	weak := rule
	weak.Result = routineguard.Allow
	f.call(f.p, "PUT", route, request("project", 2, []routineguard.Rule{weak}, ""), 403)
	var source routineguard.Source
	if err := json.Unmarshal(f.call(f.p, "PUT", route, request("project", 2, []routineguard.Rule{weak}, "Approved narrow exception"), 200), &source); err != nil {
		t.Fatal(err)
	}
	if source.Revision != 3 || len(source.Loosenings) != 1 || source.Loosenings[0].ActorID != f.p.ID || source.Loosenings[0].ExpectedRevision != 2 {
		t.Fatal("loosening evidence lost")
	}
	f.tx(func(tx pgx.Tx) error {
		// The final broker transaction owns the same access fence. A deterministic
		// allow must remain held for S08; protected artifact changes stay blocked.
		if _, err := lock(t.Context(), tx, f.p.TenantID, false); err != nil {
			return err
		}
		for _, c := range []routineguard.Context{{Checkpoint: "action", Action: "work.update", Text: "Ordinary work"}, {Checkpoint: "action", Action: "pr.open", Paths: []string{"internal/routineguard/policy.go"}}} {
			d, err := EvaluateGuardrailsTx(t.Context(), tx, f.p, item, c)
			if err != nil {
				return err
			}
			if d.CanExecute() || d.RequiredEvaluation == nil {
				t.Fatal("missing required evaluator allowed effect")
			}
			if len(c.Paths) > 0 && d.Result != routineguard.Block {
				t.Fatal("administrator bypassed hard floor")
			}
		}
		var honest bool
		err := tx.QueryRow(t.Context(), `SELECT count(*)=2 AND bool_and(required_evaluation AND hard_version=$2 AND decision->>'policy_digest'=policy_digest AND jsonb_array_length(decision->'findings')>0) FROM routine_guard_evaluations WHERE recurrence_id=$1`, item.ID, routineguard.HardVersion).Scan(&honest)
		if err == nil && !honest {
			t.Fatal("evaluation did not retain rule/version/result/reason")
		}
		return err
	})
	// Personal policies remain owner-private and cannot affect another user's
	// definition or expose its evaluation bindings through application RLS.
	privateInput := f.input()
	privateInput.Definition = &Definition{Scope: DefinitionScope{Kind: "personal"}, OwnerPrincipalID: f.p.ID}
	private := f.create(privateInput)
	privateRoute := "/api/recurrences/" + private.ID + "/guardrails"
	f.call(f.p, "PUT", privateRoute, request("user", 0, []routineguard.Rule{{ID: "custom.private", Checkpoint: "both", Method: "words", Values: []string{"private marker"}, Result: routineguard.Block}}, ""), 200)
	f.tx(func(tx pgx.Tx) error {
		if _, err := lock(t.Context(), tx, f.p.TenantID, false); err != nil {
			return err
		}
		d, err := EvaluateGuardrailsTx(t.Context(), tx, f.p, private, routineguard.Context{Checkpoint: "save", Text: "private marker"})
		if err == nil && d.Result != routineguard.Block {
			t.Fatal("private tightening did not block")
		}
		return err
	})
	f.call(member, "GET", privateRoute, nil, 404)
	ctx := tenant.WithPrincipal(t.Context(), member)
	if err := db.InTenant(ctx, f.d.App, member.TenantID, func(tx pgx.Tx) error {
		var hidden bool
		err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM routine_guard_policies WHERE scope_kind='user') AND NOT EXISTS(SELECT 1 FROM routine_guard_evaluations WHERE recurrence_id=$1)`, private.ID).Scan(&hidden)
		if err == nil && !hidden {
			t.Fatal("private policy or evaluation leaked")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var foreign tenant.Principal
	foreign.Kind = tenant.Person
	foreign.TenantID = "20000000-0000-4000-8000-000000000001"
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, foreign.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'guard-foreign','Guard foreign')`, foreign.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Foreign') RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID)
	}); err != nil {
		t.Fatal(err)
	}
	f.call(foreign, "GET", route, nil, 404)
	ctx = tenant.WithPrincipal(t.Context(), foreign)
	if err := db.InTenant(ctx, f.d.App, foreign.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM routine_guard_policies)+(SELECT count(*) FROM routine_guard_evaluations)`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("cross-tenant policy/evaluation leakage")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.tx(func(tx pgx.Tx) error {
		var inert bool
		err := tx.QueryRow(t.Context(), `SELECT NOT EXISTS(SELECT 1 FROM agent_runs) AND NOT EXISTS(SELECT 1 FROM recurrence_definitions WHERE execute_consent)`).Scan(&inert)
		if err == nil && !inert {
			t.Fatal("policy edit started execution")
		}
		return err
	})
	// Assignment saves share the policy boundary and retain revision-bound
	// evidence, while a denied save changes neither definition nor revision.
	var workKind string
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT aeon_seed_work_kinds($1)`, f.p.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='backend' AND project_id IS NULL`).Scan(&workKind)
	})
	current := f.get(item.ID)
	current.Definition.Assignment = &Assignment{Goal: "Review ordinary code", Role: "build", WorkKindID: workKind, RuntimeRequirements: RuntimeRequirements{NeedsNativeHost: true, RuntimeClass: "native_coding"}, Budget: DefinitionBudget{Mode: "off"}}
	edit := func(input Input, revision int64, status int) {
		f.call(f.p, "PUT", "/api/recurrences/"+item.ID, struct {
			Input
			ExpectedRevision int64 `json:"expected_revision"`
		}{input, revision}, status)
	}
	edit(current.Input, current.Revision, 200)
	current = f.get(item.ID)
	f.tx(func(tx pgx.Tx) error {
		var saved bool
		err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM routine_guard_evaluations WHERE recurrence_id=$1 AND definition_revision=$2 AND checkpoint='save' AND NOT required_evaluation)`, item.ID, current.Revision).Scan(&saved)
		if err == nil && !saved {
			t.Fatal("save evaluation missing")
		}
		return err
	})
	current.Definition.Assignment.Goal = "steal credentials"
	edit(current.Input, current.Revision, 400)
	f.tx(func(tx pgx.Tx) error {
		var denial bool
		err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM routine_guard_evaluations WHERE recurrence_id IS NULL AND result='block' AND checkpoint='save' AND owner_principal_id=$1 AND decision->>'hard_version'=$2)`, f.p.ID, routineguard.HardVersion).Scan(&denial)
		if err == nil && !denial {
			t.Fatal("rejected save lost audited rule results")
		}
		return err
	})
	if got := f.get(item.ID); got.Revision != current.Revision || got.Definition.Assignment.Goal == "steal credentials" {
		t.Fatal("hard-blocked save changed definition")
	}
	// JSON callers cannot invent actor evidence or replace script hashes.
	f.call(f.p, "PUT", route, map[string]any{"scope": "project", "expected_revision": 3, "rules": []any{}, "actor_id": f.p.ID}, 400)
	bad := routineguard.Rule{ID: "custom.script", Checkpoint: "action", Method: "template", Template: "protected_paths_v1", ScriptHash: strings.Repeat("0", 64), Result: routineguard.Allow}
	f.call(f.p, "PUT", route, request("project", 3, []routineguard.Rule{bad}, ""), 400)
}

// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Project routines inherit tenant/project rules; personal routines inherit
// tenant/user rules; workspace routines inherit tenant rules. A private user
// policy is never silently omitted from a project administrator's evaluation.
func guardScopes(p tenant.Principal, r Recurrence) []routineguard.Scope {
	out := []routineguard.Scope{{Kind: "tenant", ID: p.TenantID}}
	if r.Definition.Scope.Kind == "project" {
		out = append(out, routineguard.Scope{Kind: "project", ID: r.Definition.Scope.ProjectID})
	}
	if r.Definition.Scope.Kind == "personal" {
		out = append(out, routineguard.Scope{Kind: "user", ID: r.Definition.OwnerPrincipalID})
	}
	return out
}
func guardSources(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Recurrence) ([]routineguard.Source, error) {
	out := []routineguard.Source{}
	if r.Definition == nil {
		return out, workorders.Fail(400, "guardrails require a scoped routine definition")
	}
	for _, scope := range guardScopes(p, r) {
		var raw, evidence []byte
		s := routineguard.Source{Scope: scope, Rules: []routineguard.Rule{}, Loosenings: []routineguard.Loosening{}}
		err := tx.QueryRow(ctx, `SELECT revision,rules,loosenings FROM routine_guard_policies WHERE scope_kind=$1 AND scope_id=$2 ORDER BY revision DESC LIMIT 1`, scope.Kind, scope.ID).Scan(&s.Revision, &raw, &evidence)
		if err == pgx.ErrNoRows {
			continue
		}
		if err != nil {
			return out, err
		}
		if len(raw) > routineguard.MaxPolicyBytes || len(evidence) > 2*routineguard.MaxPolicyBytes {
			return out, workorders.Fail(400, "guardrail policy exceeds limits")
		}
		if err = json.Unmarshal(raw, &s.Rules); err != nil {
			return out, err
		}
		if err = json.Unmarshal(evidence, &s.Loosenings); err != nil {
			return out, err
		}
		out = append(out, s)
	}
	return out, nil
}
func (m *Module) getGuardrails(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var out routineguard.Policy
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(r.Context(), tx, 5*time.Second); err != nil {
			return err
		}
		item, err := load(r.Context(), tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err = readDefinition(r.Context(), tx, p, item.Input); err != nil {
			return err
		}
		sources, err := guardSources(r.Context(), tx, p, item)
		if err != nil {
			return err
		}
		out, err = routineguard.Resolve(sources)
		if err != nil {
			return workorders.Fail(409, "guardrail policy needs revision or administrator evidence")
		}
		return nil
	})
	reply(w, 200, out, err)
}
func guardAdmin(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) (bool, error) {
	effective, err := authz.EffectiveTx(ctx, tx, p, project)
	if err != nil {
		return false, err
	}
	admin := func(r *authz.RoleRef) bool { return r != nil && (r.Key == "owner" || r.Key == "admin") }
	return admin(effective.Workspace.Role) || effective.Project != nil && admin(effective.Project.Role), nil
}
func (m *Module) putGuardrails(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person || p.KeyID != "" {
		httpapi.WriteError(w, 403, "only a person session may edit guardrails")
		return
	}
	var in struct {
		Scope            string              `json:"scope"`
		ExpectedRevision int64               `json:"expected_revision"`
		Reason           string              `json:"reason"`
		Rules            []routineguard.Rule `json:"rules"`
	}
	// Policy inputs are bounded before decoding or database work.
	r.Body = http.MaxBytesReader(w, r.Body, routineguard.MaxPolicyBytes)
	if !decode(w, r, &in) {
		return
	}
	if len(in.Reason) > 2048 || in.ExpectedRevision < 0 || in.ExpectedRevision == 9223372036854775807 {
		httpapi.WriteError(w, 400, "policy revision or reason exceeds limits")
		return
	}
	if err := routineguard.ValidateRules(in.Rules); err != nil {
		httpapi.WriteError(w, 400, err.Error())
		return
	}
	var out routineguard.Source
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(r.Context(), tx, 5*time.Second); err != nil {
			return err
		}
		if _, err := lock(r.Context(), tx, p.TenantID, false); err != nil {
			return err
		}
		item, err := load(r.Context(), tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err = manageDefinition(r.Context(), tx, p, item.Input); err != nil {
			return err
		}
		if item.Definition == nil {
			return workorders.Fail(400, "guardrails require a scoped routine definition")
		}
		person, err := canonicalPerson(r.Context(), tx, p)
		if err != nil {
			return err
		}
		var selected routineguard.Scope
		for _, s := range guardScopes(p, item) {
			if s.Kind == in.Scope {
				selected = s
			}
		}
		if selected.Kind == "" {
			return workorders.Fail(400, "policy scope is not applicable to this routine")
		}
		project := ""
		if selected.Kind == "project" {
			project = selected.ID
		}
		if err = authz.RequireTx(r.Context(), tx, p, Permission, authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		admin, err := guardAdmin(r.Context(), tx, p, project)
		if err != nil {
			return err
		}
		if selected.Kind == "user" {
			if person != selected.ID {
				return authz.ErrForbidden
			}
		} else if !admin {
			return authz.ErrForbidden
		}
		sources, err := guardSources(r.Context(), tx, p, item)
		if err != nil {
			return err
		}
		inherited := []routineguard.Source{}
		before := routineguard.Source{Scope: selected, Rules: []routineguard.Rule{}, Loosenings: []routineguard.Loosening{}}
		for _, source := range sources {
			if source.Scope.Kind == selected.Kind {
				before = source
				break
			}
		}
		// No source at this scope: discard any lower-scope sources from inheritance.
		order := map[string]int{"tenant": 0, "project": 1, "user": 2}
		inherited = nil
		for _, source := range sources {
			if order[source.Scope.Kind] < order[selected.Kind] {
				inherited = append(inherited, source)
			}
		}
		if before.Revision != in.ExpectedRevision {
			return workorders.Fail(409, "guardrail revision changed")
		}
		out, err = routineguard.Change(inherited, before, in.Rules, in.ExpectedRevision, routineguard.Actor{ID: person, Person: true, Administrator: admin, Reason: in.Reason})
		if err != nil {
			return workorders.Fail(403, err.Error())
		}
		rules, _ := json.Marshal(out.Rules)
		evidence, _ := json.Marshal(out.Loosenings)
		if len(rules) > routineguard.MaxPolicyBytes || len(evidence) > 2*routineguard.MaxPolicyBytes {
			return workorders.Fail(400, "guardrail policy exceeds limits")
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO routine_guard_policies(tenant_id,scope_kind,scope_id,revision,rules,loosenings,created_by,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.TenantID, selected.Kind, selected.ID, out.Revision, rules, evidence, person, strings.TrimSpace(in.Reason))
		return err
	})
	reply(w, 200, out, err)
}

// EvaluateGuardrailsTx is the action broker seam. The caller must hold the
// tenant/tree access fence and separately RequireTx the actual action target in
// this same final transaction. Persisting a decision grants no execution.
func EvaluateGuardrailsTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Recurrence, c routineguard.Context) (routineguard.Decision, error) {
	if err := routineguard.ValidateContext(c); err != nil {
		return routineguard.Decision{}, workorders.Fail(400, err.Error())
	}
	current, err := load(ctx, tx, r.ID, false)
	if err != nil {
		return routineguard.Decision{}, err
	}
	if current.Revision != r.Revision || current.Definition == nil || routineguard.Digest(current.Definition) != routineguard.Digest(r.Definition) {
		return routineguard.Decision{}, workorders.Fail(409, "routine definition changed")
	}
	if err = readDefinition(ctx, tx, p, current.Input); err != nil {
		return routineguard.Decision{}, err
	}
	sources, err := guardSources(ctx, tx, p, current)
	if err != nil {
		return routineguard.Decision{}, err
	}
	decision, err := routineguard.Evaluate(sources, c)
	if err != nil {
		return decision, workorders.Fail(400, err.Error())
	}
	return decision, persistGuardrailDecision(ctx, tx, p, r, c.Checkpoint, decision)
}
func persistGuardrailDecision(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Recurrence, checkpoint string, decision routineguard.Decision) error {
	raw, _ := json.Marshal(decision)
	if len(raw) > 262144 {
		return workorders.Fail(400, "evaluation exceeds limits")
	}
	_, err := tx.Exec(ctx, `INSERT INTO routine_guard_evaluations(tenant_id,recurrence_id,definition_revision,checkpoint,policy_digest,context_digest,hard_version,preset_version,result,required_evaluation,decision,scope_kind,scope_project_id,owner_principal_id,output_project_id) VALUES($1,nullif($2,'')::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,nullif($13,'')::uuid,$14,$15)`, p.TenantID, r.ID, r.Revision, checkpoint, decision.PolicyDigest, decision.ContextDigest, decision.HardVersion, decision.PresetVersion, decision.Result, decision.RequiredEvaluation != nil, raw, r.Definition.Scope.Kind, r.Definition.Scope.ProjectID, r.Definition.OwnerPrincipalID, r.ProjectID)
	return err
}
func saveGuardrails(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Recurrence) error {
	if r.Definition == nil || r.Definition.Assignment == nil {
		return nil
	}
	raw, err := json.Marshal(r.Input)
	if err != nil {
		return err
	}
	if len(raw) > routineguard.MaxTextBytes {
		return workorders.Fail(400, "saved guardrail context exceeds limits")
	}
	decision, err := EvaluateGuardrailsTx(ctx, tx, p, r, routineguard.Context{Checkpoint: "save", Text: string(raw), PayloadBytes: int64(len(raw))})
	if err != nil {
		return err
	}
	if decision.Result == routineguard.Block {
		return &blockedGuardrailSave{item: r, decision: decision}
	}
	return nil
}

// A rejected mutation rolls back its provisional evaluation. Record its bounded
// denial separately, under a fresh authority fence, without a recurrence FK to
// a definition that was never committed. No raw assignment is persisted.
type blockedGuardrailSave struct {
	item     Recurrence
	decision routineguard.Decision
}

func (e *blockedGuardrailSave) Error() string {
	return "compiled or scoped guardrail blocks this assignment"
}
func (e *blockedGuardrailSave) Unwrap() error { return workorders.Fail(400, e.Error()) }
func (m *Module) recordGuardrailDenial(ctx context.Context, p tenant.Principal, err error) error {
	var blocked *blockedGuardrailSave
	if !errors.As(err, &blocked) {
		return err
	}
	auditErr := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		if _, err := lock(ctx, tx, p.TenantID, false); err != nil {
			return err
		}
		if err := authorizeDefinition(ctx, tx, p, blocked.item.Input); err != nil {
			return err
		}
		attempted := blocked.item
		attempted.ID = ""
		return persistGuardrailDecision(ctx, tx, p, attempted, "save", blocked.decision)
	})
	if auditErr != nil {
		return auditErr
	}
	return err
}

// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// ActionBinding carries server-loaded frozen lineage to the consent seam.
type ActionBinding struct {
	RunID, RecurrenceID, OwnerID, ExecutionPrincipalID, ProjectID, ParentID string
	ScopeType, ScopeProjectID                                               string
	DefinitionRevision, ConsentRevision                                     int64
	Assignment                                                              json.RawMessage
	AssignmentDigest                                                        string
}

// ActionEvaluatorTx is S08's verified persisted-verdict seam. It reads already
// recorded evidence only; evaluator/model/network calls happen before this
// transaction. Without it, even a deterministic allow cannot execute.
type ActionEvaluatorTx func(context.Context, pgx.Tx, tenant.Principal, Recurrence, routineguard.Decision) (routineguard.Decision, string, error)

func (m *Module) WithActionEvaluator(evaluate ActionEvaluatorTx) *Module {
	m.actionEvaluator = evaluate
	return m
}

func (m *Module) AuthorizeActionTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, b ActionBinding, c routineguard.Context) (tenant.Principal, routineguard.Decision, string, error) {
	empty := tenant.Principal{}
	actor, current, err := m.authorizeActionBindingTx(ctx, tx, p, b)
	if err != nil {
		return empty, routineguard.Decision{}, "", err
	}
	d := current.Definition

	if !slices.Contains(d.Assignment.AllowedActions, c.Action) {
		return empty, routineguard.Decision{}, "", authz.ErrForbidden
	}
	owner := tenant.Principal{ID: d.OwnerPrincipalID, TenantID: p.TenantID, Kind: tenant.Person}
	decision, err := EvaluateGuardrailsTx(ctx, tx, owner, current, c)
	if err != nil {
		return empty, decision, "", err
	}
	if decision.DeterministicResult != routineguard.Allow {
		return empty, decision, "", workorders.Fail(409, "guardrail_action_denied")
	}
	if m.actionEvaluator == nil {
		return empty, decision, "", workorders.Fail(409, "guardrail_evaluation_required")
	}
	verified, evaluationID, err := m.actionEvaluator(ctx, tx, owner, current, decision)
	if err != nil {
		return empty, decision, "", err
	}
	if verified.PolicyDigest != decision.PolicyDigest || verified.ContextDigest != decision.ContextDigest || verified.HardVersion != decision.HardVersion || verified.PresetVersion != decision.PresetVersion || verified.DeterministicResult != routineguard.Allow || !verified.CanExecute() {
		return empty, decision, "", workorders.Fail(409, "guardrail_evaluation_required")
	}
	return actor, verified, evaluationID, nil
}
func (m *Module) authorizeActionBindingTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, b ActionBinding) (tenant.Principal, Recurrence, error) {
	empty := tenant.Principal{}
	identity, err := RequireExecutionConsentTx(ctx, tx, p.TenantID, b.RecurrenceID, b.DefinitionRevision, m.executionRuntime)
	if err != nil {
		return empty, Recurrence{}, err
	}
	current, err := load(ctx, tx, b.RecurrenceID, false)
	if err != nil {
		return empty, Recurrence{}, err
	}
	var frozen Assignment
	if json.Unmarshal(b.Assignment, &frozen) != nil || routineguard.Digest(&frozen) != b.AssignmentDigest {
		return empty, Recurrence{}, workorders.Fail(409, "routine_assignment_changed")
	}
	d := current.Definition
	if identity != b.ExecutionPrincipalID || d == nil || d.Assignment == nil || d.OwnerPrincipalID != b.OwnerID || current.ProjectID != b.ProjectID || current.ParentID != b.ParentID || d.Scope.Kind != b.ScopeType || d.Scope.ProjectID != b.ScopeProjectID || routineguard.Digest(d.Assignment) != b.AssignmentDigest || b.ConsentRevision != current.Revision {
		return empty, Recurrence{}, workorders.Fail(409, "routine_grant_changed")
	}

	actor := tenant.Principal{ID: identity, TenantID: p.TenantID, Scopes: executionPermissions(d.Assignment)}
	var kind string
	if err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1 AND status='active' AND linked_to IS NULL`, identity).Scan(&kind); err != nil {
		return empty, Recurrence{}, err
	}
	actor.Kind = tenant.PrincipalKind(kind)
	return actor, current, nil
}

// AuthorizeObservationTx rechecks the current frozen assignment, owning person,
// execution identity, consent and exact qualification for observing an already
// committed merge-to-Done receipt. It grants no outward action authority.
func (m *Module) AuthorizeObservationTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, b ActionBinding) (tenant.Principal, error) {
	actor, _, err := m.authorizeActionBindingTx(ctx, tx, p, b)
	return actor, err
}

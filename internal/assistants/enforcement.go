// SPDX-License-Identifier: AGPL-3.0-only

package assistants

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Check is an additional ceiling, never a grant. Callers hold the tenant access
// fence, recheck ordinary authority, finish all writes, then append Changes.
// Charge reserves a person-granted maximum. An unknown cost occupies the whole
// remaining allowance; an assistant's own reported cost never releases it.
type Check struct {
	TenantID, PrincipalID, ProjectID, Operation, Key string
	Scopes                                           []string
	Charge, RequireBudget, RequireAssistant          bool
	MessageCost                                      bool // use the current policy's per-message amount
	Ceiling                                          *Budget
	Now                                              time.Time
	Refusal                                          string // a server-selected denial from the surrounding person grant
}

type Decision struct {
	Applicable     bool            `json:"-"`
	Charged        bool            `json:"-"` // this transaction created the hold
	Allowed        bool            `json:"allowed"`
	Reason         string          `json:"reason"`
	PrincipalID    string          `json:"principal_id"`
	PolicyRevision int64           `json:"policy_revision"`
	Changes        []events.Change `json:"-"`
}

// Week is the policy's UTC calendar week. Omitted reset day means Monday.
func Week(now time.Time, p Policy) (time.Time, time.Time) {
	day := 1
	if p.WeeklyResetDay != nil {
		day = *p.WeeklyResetDay
	}
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(int(now.Weekday())-day+7)%7)
	return start, start.AddDate(0, 0, 7)
}

func micro(amount string) (int64, error) {
	if !decimal.MatchString(amount) {
		return 0, errInput
	}
	whole, fraction, _ := strings.Cut(amount, ".")
	a, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, err
	}
	b, err := strconv.ParseInt(fraction+strings.Repeat("0", 6-len(fraction)), 10, 64)
	return a*1_000_000 + b, err
}

// EvaluateTx is side-effect free for ordinary builder principals. Hosted and
// legacy messaging assistants always use their own current policy, including
// when called by a person, lead or background dispatcher on their behalf.
func EvaluateTx(ctx context.Context, tx pgx.Tx, in Check) (Decision, error) {
	d := Decision{Allowed: true, PrincipalID: in.PrincipalID}
	if !uuid.MatchString(in.TenantID) || !uuid.MatchString(in.PrincipalID) || len(in.Scopes) > 200 || in.Key == "" || len(in.Key) > 256 || len(in.Operation) > 64 {
		return d, errInput
	}
	var visibleTenant string
	if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id',true)`).Scan(&visibleTenant); err != nil {
		return d, err
	}
	if !strings.EqualFold(visibleTenant, in.TenantID) {
		return d, authz.ErrForbidden
	}
	if p, ok := tenant.PrincipalFrom(ctx); ok && p.TenantID != in.TenantID {
		return d, authz.ErrForbidden
	}
	var assistant bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid AND kind='agent' AND (agent_class='assistant' OR messaging_only IS TRUE))`, in.TenantID, in.PrincipalID).Scan(&assistant); err != nil {
		return d, err
	}
	if !assistant && !in.RequireAssistant {
		return d, nil
	}
	d.Applicable = true
	a, err := getTx(ctx, tx, in.TenantID, in.PrincipalID)
	if errors.Is(err, ErrNotFound) {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid)`, in.TenantID, in.PrincipalID).Scan(&exists); err != nil {
			return d, err
		}
		if !exists {
			return d, ErrNotFound
		}
		d.Allowed, d.Reason = false, "assistant_policy_unavailable"
		return recordDecisionTx(ctx, tx, in, d)
	}
	if err != nil {
		return d, err
	}
	d.PolicyRevision = a.Revision
	deny := func(reason string) (Decision, error) {
		d.Allowed, d.Reason = false, reason
		return recordDecisionTx(ctx, tx, in, d)
	}
	if a.Status != "active" {
		return deny("assistant_disabled")
	}
	var ownerActive bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid AND kind='person' AND status='active' AND linked_to IS NULL)`, in.TenantID, a.OwnerPersonID).Scan(&ownerActive); err != nil {
		return d, err
	}
	if !ownerActive {
		return deny("assistant_owner_unavailable")
	}
	if in.ProjectID != "" && !slices.Contains(a.Policy.AllowedProjects, in.ProjectID) {
		return deny("assistant_project_denied")
	}
	for _, scope := range in.Scopes {
		if !slices.Contains(a.Policy.AllowedScopes, scope) {
			return deny("assistant_scope_denied: " + scope)
		}
	}
	if in.Refusal != "" {
		return deny(in.Refusal)
	}
	if a.Policy.OutwardActionRule != "approval_required" || !a.Policy.NeverAskPersonForSecretsOrManualSteps {
		return deny("assistant_policy_invalid")
	}
	if in.Charge || in.RequireBudget {
		if in.MessageCost {
			in.Ceiling = &Budget{Amount: a.Policy.MessageCost, Unit: a.Policy.WeeklyBudget.Unit}
		}
		if in.Now.IsZero() {
			if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&in.Now); err != nil {
				return d, err
			}
		}
		limit, _ := micro(a.Policy.WeeklyBudget.Amount)
		if limit <= 0 {
			return deny("assistant_weekly_budget_exhausted")
		}
		start, end := Week(in.Now, a.Policy)
		var used int64
		// Count reservation timestamps against the current calendar too, so
		// changing the reset day cannot discard already admitted work.
		var incompatible bool
		if err = tx.QueryRow(ctx, `SELECT coalesce(sum(amount_micro) FILTER(WHERE unit=$4),0)::bigint,coalesce(bool_or(unit<>$4),false) FROM assistant_budget_reservations WHERE tenant_id=$1::uuid AND principal_id=$2::uuid AND created_at >= $3 AND created_at < $5`, in.TenantID, in.PrincipalID, start, a.Policy.WeeklyBudget.Unit, end).Scan(&used, &incompatible); err != nil {
			return d, err
		}
		if incompatible {
			return deny("assistant_budget_unit_changed")
		}
		var reserved int64
		var unit string
		err = tx.QueryRow(ctx, `SELECT amount_micro,unit FROM assistant_budget_reservations WHERE tenant_id=$1::uuid AND principal_id=$2::uuid AND operation_key=$3`, in.TenantID, in.PrincipalID, in.Key).Scan(&reserved, &unit)
		if err == nil {
			if unit != a.Policy.WeeklyBudget.Unit {
				return deny("assistant_budget_unit_changed")
			}
			if used > limit {
				return deny("assistant_weekly_budget_exhausted")
			}
			if in.MessageCost {
				amount, _ := micro(a.Policy.MessageCost)
				if reserved < amount {
					return deny("assistant_message_cost_exceeds_hold")
				}
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return d, err
		} else {
			amount, unknown := limit-used, in.Ceiling == nil
			if in.Ceiling != nil {
				if in.Ceiling.Unit != a.Policy.WeeklyBudget.Unit {
					return deny("assistant_budget_unit_mismatch")
				}
				amount, err = micro(in.Ceiling.Amount)
				if err != nil {
					return d, err
				}
			}
			if amount <= 0 || used >= limit || amount > limit-used {
				return deny("assistant_weekly_budget_exhausted")
			}
			if in.Charge {
				_, err = tx.Exec(ctx, `INSERT INTO assistant_budget_reservations(tenant_id,principal_id,operation_key,week_start,unit,amount_micro,unknown_cost,policy_revision,created_at) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9)`, in.TenantID, in.PrincipalID, in.Key, start, a.Policy.WeeklyBudget.Unit, amount, unknown, a.Revision, in.Now)
				if err != nil {
					return d, err
				}
				d.Charged = true
			}
		}
	}
	return recordDecisionTx(ctx, tx, in, d)
}

func recordDecisionTx(ctx context.Context, tx pgx.Tx, in Check, d Decision) (Decision, error) {
	project := in.ProjectID
	if !d.Allowed && project != "" {
		// A revoked project is invisible to this caller. Keep the refusal on
		// the assistant without naming a hidden project in its FK or event.
		var visible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1::uuid)`, project).Scan(&visible); err != nil {
			return d, err
		}
		if !visible {
			project = ""
		}
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO assistant_policy_receipts(tenant_id,principal_id,project_id,operation_key,operation,policy_revision,allowed,reason) VALUES($1::uuid,$2::uuid,nullif($3,'')::uuid,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING RETURNING id::text`, in.TenantID, in.PrincipalID, project, in.Key, in.Operation, d.PolicyRevision, d.Allowed, d.Reason).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	after := map[string]any{"id": id, "principal_id": in.PrincipalID, "operation": in.Operation, "operation_key": in.Key, "policy_revision": d.PolicyRevision, "allowed": d.Allowed, "reason": d.Reason}
	change := events.Change{Type: "assistant.policy_evaluated", After: after}
	if project != "" {
		change.NodeID = &project
	}
	d.Changes = []events.Change{change}
	return d, nil
}

func AppendDecisionsTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, decisions ...Decision) error {
	for _, d := range decisions {
		for _, c := range d.Changes {
			if _, err := events.Append(ctx, tx, p, c); err != nil {
				return err
			}
		}
	}
	return nil
}

// CheckApproverTx is read-only so the Desk and phone challenge can use the
// same current approver ceiling as the final decision transaction.
func CheckApproverTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, assistantID string) error {
	a, err := getTx(ctx, tx, p.TenantID, assistantID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var person string
	if err = tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid AND kind='person' AND status='active'`, p.TenantID, p.ID).Scan(&person); err != nil {
		return authz.ErrForbidden
	}
	if len(a.Policy.ApproverPersonIDs) == 0 {
		if person != a.OwnerPersonID {
			return authz.ErrForbidden
		}
	} else if !slices.Contains(a.Policy.ApproverPersonIDs, person) {
		return authz.ErrForbidden
	}
	return nil
}

// ValidateCeiling checks a person-supplied run maximum without converting units.
func ValidateCeiling(b *Budget) bool {
	if b == nil {
		return true
	}
	value, err := micro(b.Amount)
	return err == nil && value > 0 && slices.Contains([]string{"USD", "EUR", "tokens", "credits"}, b.Unit)
}

// ReleaseUnexposedMessageTx is only for a trusted hosted dispatcher that still
// owns the exact delivery lease and knows no request was sent. It must never be
// called for a reused routine hold or a provider response/timeout. Callers hold
// the tenant fence and append the returned audit after all resource writes.
func ReleaseUnexposedMessageTx(ctx context.Context, tx pgx.Tx, tenantID, principalID, key string) ([]events.Change, error) {
	var amount int64
	var unit string
	err := tx.QueryRow(ctx, `DELETE FROM assistant_budget_reservations WHERE tenant_id=$1::uuid AND principal_id=$2::uuid AND operation_key=$3 RETURNING amount_micro,unit`, tenantID, principalID, key).Scan(&amount, &unit)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []events.Change{{Type: "assistant.message_hold_released", After: map[string]any{"principal_id": principalID, "operation_key": key, "amount_micro": amount, "unit": unit, "reason": "not_exposed"}}}, nil
}

// RoutineGrantRefusalTx checks owner consent separately from ordinary project
// authority. Callers hold the tenant fence and still check live person/agent
// permissions. Older bindings and a withdrawn delegator fail closed too.
func RoutineGrantRefusalTx(ctx context.Context, tx pgx.Tx, tenantID, principalID, personID string) (string, error) {
	var assistant bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid AND kind='agent' AND (agent_class='assistant' OR messaging_only IS TRUE))`, tenantID, principalID).Scan(&assistant); err != nil {
		return "", err
	}
	if !assistant {
		return "", nil
	}
	a, err := getTx(ctx, tx, tenantID, principalID)
	if errors.Is(err, ErrNotFound) {
		return "assistant_policy_unavailable", nil
	}
	if err != nil {
		return "", err
	}
	if personID != a.OwnerPersonID && !slices.Contains(a.Policy.DelegatorPersonIDs, personID) {
		return "assistant_owner_consent_required", nil
	}
	return "", nil
}

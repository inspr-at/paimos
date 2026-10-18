// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type RoutineQualificationSummary struct {
	ID           string    `json:"id"`
	AcceptedAt   time.Time `json:"accepted_at"`
	HostLabel    *string   `json:"host_label"`
	Capabilities []string  `json:"capabilities"`
	BudgetModes  []string  `json:"budget_modes"`
}

// LatestRoutineQualificationTx reads accepted evidence even while the project
// switch is off. It returns only the display projection, never the private
// account/host selectors or acceptance/artifact pins. The caller authorizes
// harness.read for this project; final consent still runs qualifiedTx.
func LatestRoutineQualificationTx(ctx context.Context, tx pgx.Tx, tenantID, project string) (*RoutineQualificationSummary, error) {
	var id string
	var accepted time.Time
	err := tx.QueryRow(ctx, `SELECT id::text,recorded_at FROM routine_execution_qualifications
 WHERE project_id=$1 AND revoked_at IS NULL
 AND coordinator_acceptance ~ '^[a-f0-9]{64}$' AND ops_attestation ~ '^[a-f0-9]{64}$'
 ORDER BY recorded_at DESC,id DESC LIMIT 1`, project).Scan(&id, &accepted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	q, err := LoadRoutineQualificationTx(ctx, tx, project, id)
	if err != nil {
		return nil, err
	}
	if !validRuntime(q.Runtime) {
		return nil, workorders.Fail(409, "qualification_unavailable")
	}
	out := &RoutineQualificationSummary{ID: q.ID, AcceptedAt: accepted.UTC(), Capabilities: q.Runtime.Capabilities, BudgetModes: q.Runtime.BudgetModes}
	policy, err := LoadRoutineLeadPolicyTx(ctx, tx, tenantID, project)
	if errors.Is(err, authz.ErrForbidden) || errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	pin, err := RoutinePolicyDigest(policy)
	if err != nil {
		return nil, err
	}
	hosts := policy.Effective.AllowedHostIDs
	if policy.OwnerPersonID == nil || *policy.OwnerPersonID != q.OwnerPersonID || pin != q.PolicyDigest || hosts == nil || len(*hosts) != 1 {
		return out, nil
	}
	// A digest cannot be decoded into a computer name. Only a single named
	// computer in the matching policy is a truthful label; otherwise use null.
	var label string
	err = tx.QueryRow(ctx, `SELECT c.display_name FROM agent_pairing_computers c
 JOIN agent_pairing_requests r ON r.tenant_id=c.tenant_id AND r.id=c.request_id
 WHERE c.id=$1 AND c.state<>'revoked' AND r.approved_by=$2
 AND char_length(c.display_name) BETWEEN 1 AND 128 AND octet_length(c.display_name)<=512
 AND c.display_name !~ '[[:cntrl:]]'`, (*hosts)[0], q.OwnerPersonID).Scan(&label)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.HostLabel = &label
	return out, nil
}

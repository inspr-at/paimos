// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// PrepareSessionRecovery uses ordinary admission, residency and account checks.
// The caller holds the tenant/tree fence and flushes events after its last lock.
func PrepareSessionRecovery(ctx context.Context, tx pgx.Tx, actor tenant.Principal, project, oldID, model, effort string) (Run, func() error, error) {
	if actor.Kind != tenant.Person {
		return Run{}, nil, workorders.Fail(403, "person required for restart")
	}
	if err := authz.RequireTx(ctx, tx, actor, "run.create", authz.Scope{ProjectID: project}); err != nil {
		return Run{}, nil, err
	}
	old, order, err := lockRun(ctx, tx, oldID)
	if err != nil {
		return Run{}, nil, err
	}
	if !terminal(old.Status) || old.Purpose != "managed" || old.ReadOnlyReview || old.ProfileID == nil || old.AccountID == nil {
		return Run{}, nil, workorders.Fail(409, "settled managed run with account and model required")
	}
	profile := *old.ProfileID
	if model != "" {
		// Applied model/effort must still be an enabled catalog tuple. Never invent
		// a profile or override its allowed-account ceiling during recovery.
		if err := tx.QueryRow(ctx, `SELECT id::text FROM model_profiles WHERE enabled AND model=$1 AND effort=$2 AND harness=(SELECT harness FROM model_profiles WHERE id=$3) ORDER BY (id=$3) DESC,id LIMIT 1`, model, effort, profile).Scan(&profile); err != nil {
			return Run{}, nil, workorders.Fail(409, "applied model settings no longer available")
		}
	}
	body, _ := json.Marshal(map[string]any{"agent_principal_id": old.AgentID, "model_profile_id": profile, "requested_account_id": *old.AccountID, "retry_of_run_id": old.ID})
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", bytes.NewReader(body))
	r.SetPathValue("workOrderId", order.NodeID)
	var events []func() error
	result, err := createRun(r, tx, actor, &events)
	if err != nil {
		return Run{}, nil, err
	}
	return result.(Run), func() error {
		for _, event := range events {
			if err := event(); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

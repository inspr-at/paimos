// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type RouteTarget struct {
	Agent   string  `json:"agent_principal_id,omitempty"`
	Profile string  `json:"model_profile_id,omitempty"`
	Account *string `json:"requested_account_id,omitempty"`
}

// RouteCandidatesTx shares placement and account discovery between pickup and
// fairness. A targeted run keeps its exact requested worker/profile/account.
func RouteCandidatesTx(ctx context.Context, tx pgx.Tx, run string, fields []byte, project string, security bool, target RouteTarget) ([]RouteTarget, json.RawMessage, error) {
	f := modelprefs.PlacementFields(fields)
	role := f.RouteRole
	if role == "" {
		role = "build"
	}
	if security {
		role = "build-hard"
	}
	var starter, ticket *string
	if err := tx.QueryRow(ctx, `SELECT prefs_person_id::text,queue_node_id::text FROM agent_runs WHERE id=$1`, run).Scan(&starter, &ticket); err != nil {
		return nil, nil, err
	}
	ticketID := ""
	if ticket != nil {
		ticketID = *ticket
	}
	placement, err := modelregistry.PlacementFor(ctx, tx, tenant.Principal{}, modelregistry.WorkQuery{
		TicketID: ticketID, Role: role, Queued: true, TicketRole: f.RouteRole, Area: f.Area, Complexity: f.Complexity,
		ComplexitySource: f.ComplexitySource, TicketResidency: f.Residency, ProjectID: project, PersonID: starter,
	}, time.Now().UTC())
	if err != nil {
		return nil, nil, err
	}
	raw, err := placement.JSON()
	if err != nil {
		return nil, nil, err
	}
	if target.Profile == "" {
		if placement.PlannedProfileID == nil {
			return nil, raw, nil
		}
		target.Profile = *placement.PlannedProfileID
	}
	if target.Agent != "" {
		return []RouteTarget{target}, raw, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT a.registered_by_principal_id::text FROM agent_accounts a JOIN model_profiles m ON m.tenant_id=a.tenant_id AND m.harness=a.harness
 WHERE m.id=$1 AND m.enabled AND a.state='available' AND a.last_probe_ok AND a.last_probe_at>clock_timestamp()-interval '2 minutes'
 AND aeon_account_allows_profile(a.harness,a.allowed_model_profile_ids,m.id) ORDER BY 1 LIMIT 201`, target.Profile)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	candidates := []RouteTarget{}
	for rows.Next() {
		var agent string
		if err := rows.Scan(&agent); err != nil {
			return nil, nil, err
		}
		if len(candidates) == 200 {
			return nil, nil, workorders.Fail(409, "queue routing candidates exceed bound")
		}
		candidates = append(candidates, RouteTarget{Agent: agent, Profile: target.Profile})
	}
	return candidates, raw, rows.Err()
}

// RouteReadyTx is read-only; it proves the same live role, pending assignment,
// enabled model and account admission predicates used by the final route.
// It neither assigns work nor reserves capacity or advances a project turn.
func RouteReadyTx(ctx context.Context, tx pgx.Tx, tenantID, project, run string, target RouteTarget) (bool, error) {
	agent := tenant.Principal{TenantID: tenantID, ID: target.Agent, Kind: tenant.Agent, Scopes: []string{"run.claim"}}
	if err := authz.RequireTx(ctx, tx, agent, "run.claim", authz.Scope{ProjectID: project}); errors.Is(err, authz.ErrForbidden) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var eligible bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE id=$3 AND enabled) AND NOT EXISTS(SELECT 1 FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.queue_node_id WHERE r.id<>$1 AND r.agent_principal_id=$2 AND r.status='queued' AND r.queue_routed_at IS NOT NULL AND n.state='open' AND n.deleted_at IS NULL)`, run, target.Agent, target.Profile).Scan(&eligible)
	if err != nil || !eligible {
		return false, err
	}
	wait, err := agentaccounts.WaitForQueueRoute(ctx, tx, run, target.Agent, target.Profile, target.Account)
	return wait == nil, err
}

// RunRouteReadyTx qualifies another project's demand using its saved targets
// or ordinary placement and live ticket security requirements, without
// inheriting the caller's pickup overrides.
func RunRouteReadyTx(ctx context.Context, tx pgx.Tx, tenantID, project, run string, fields []byte) (bool, error) {
	var agent, profile *string
	var target RouteTarget
	var title, body string
	if err := tx.QueryRow(ctx, `SELECT r.queue_target_agent_id::text,r.model_profile_id::text,r.requested_account_id::text,n.title,n.body
 FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.queue_node_id WHERE r.id=$1`, run).Scan(&agent, &profile, &target.Account, &title, &body); err != nil {
		return false, err
	}
	if agent != nil {
		target.Agent = *agent
		if profile != nil {
			target.Profile = *profile
		}
	}
	candidates, _, err := RouteCandidatesTx(ctx, tx, run, fields, project, Security(title, body, Fields(fields)), target)
	if err != nil {
		return false, err
	}
	for _, candidate := range candidates {
		ready, err := RouteReadyTx(ctx, tx, tenantID, project, run, candidate)
		if err != nil || ready {
			return ready, err
		}
	}
	return false, nil
}

// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/jackc/pgx/v5"
)

// WithAssistantPolicy installs A's current policy as D's atomic admission hook.
// The instance is trusted server configuration, never the request Host header.
func (m *Module) WithAssistantPolicy(instance string) *Module {
	m.policyInstance = strings.TrimRight(instance, "/")
	m.agentBudget = m.assistantBudgetTx
	return m
}

func agentScopes(c AgentOwner) []string {
	return []string{"nodes.read", Permission, agentOwnerPermission(c.Destination), "inbox.read"}
}

func (m *Module) assistantBudgetTx(ctx context.Context, tx pgx.Tx, r AgentBudgetRequest) (AgentBudgetDecision, error) {
	var raw, delegation []byte
	var project string
	if err := tx.QueryRow(ctx, `SELECT output_project_id::text,agent_owner_config,agent_delegation FROM routine_runs WHERE tenant_id=$1::uuid AND id=$2::uuid AND owner_principal_id=$3::uuid`, r.TenantID, r.RunID, r.OwnerPrincipalID).Scan(&project, &raw, &delegation); err != nil {
		return AgentBudgetDecision{}, err
	}
	var owner AgentOwner
	if len(raw) > 8192 || json.Unmarshal(raw, &owner) != nil {
		return AgentBudgetDecision{}, errInvalidAssistantGrant()
	}
	key, scopes := "routine-run/"+r.RunID, agentScopes(owner)
	if len(delegation) > 0 {
		var d AgentDelegation
		if len(delegation) > 32768 || json.Unmarshal(delegation, &d) != nil || d.Instance != m.policyInstance {
			return AgentBudgetDecision{}, errInvalidAssistantGrant()
		}
		key, scopes = d.BudgetKey, d.Scopes
		if err := validateDelegatingLeadTx(ctx, tx, r.TenantID, project, d.LeadPrincipalID, d.SessionID, d.OwnerPersonID); err != nil {
			return AgentBudgetDecision{}, err
		}
	}
	now, err := m.clock(ctx, tx)
	if err != nil {
		return AgentBudgetDecision{}, err
	}
	d, err := assistants.EvaluateTx(ctx, tx, assistants.Check{TenantID: r.TenantID, PrincipalID: r.OwnerPrincipalID, ProjectID: project, Operation: "routine.run", Key: key, Scopes: scopes, Charge: true, Ceiling: owner.BudgetCeiling, Now: now, RequireAssistant: true})
	return AgentBudgetDecision{Allowed: d.Allowed, Reason: d.Reason, Changes: d.Changes}, err
}

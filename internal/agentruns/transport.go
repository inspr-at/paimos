// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"net/http"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// The transport query never selects trace, outcome prose or recovery content.
// Typed NULL placeholders keep the shared scanner's positions explicit.
const metadataColumns = `id::text,work_order_id::text,agent_principal_id::text,model_profile_id::text,account_id::text,status,
 requested_model,effective_model,model_evidence,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,cost_micros,started_at,ended_at,created_at,daemon_id,daemon_generation,requested_account_id::text,purpose,active_ms,NULL::text,retry_of_run_id::text,capacity_override,(retry_account_id IS NOT NULL),verification_unavailable_reason,
 EXISTS(SELECT 1 FROM work_orders review_order WHERE review_order.node_id=agent_runs.work_order_id AND review_order.kind='review'),row_version,queue_node_id::text,queue_target_agent_id::text,queue_routed_at,NULL::jsonb,waiting_ms,
 NULL::jsonb,''::text,NULL::text,routine_launch_id::text,acting_person_id::text,
 coalesce(trace ? 'run_once',false),CASE WHEN trace ? 'run_once' THEN coalesce(trace->'run_once'->'repository_mutation_allowed'='true'::jsonb,false) ELSE true END`

func readColumns(p tenant.Principal) string {
	if p.TransportComputerID != "" && p.RunID == "" {
		return metadataColumns
	}
	return columns
}

// Construct the allowlist instead of removing today's sensitive fields.
// A future Run field therefore cannot accidentally become transport content.
func transportRun(r *http.Request, v Run) Run {
	out := Run{ID: v.ID, OrderID: v.OrderID, AgentID: v.AgentID, ProfileID: v.ProfileID, AccountID: v.AccountID,
		RunOnce:            v.RunOnce,
		RequestedAccountID: v.RequestedAccountID, Status: v.Status, Purpose: v.Purpose, ActingPersonID: v.ActingPersonID,
		RoutineLaunchID: v.RoutineLaunchID, ReservationsSettled: v.ReservationsSettled, ReadOnlyReview: v.ReadOnlyReview,
		RetryOfRunID: v.RetryOfRunID, CapacityHandoff: v.CapacityHandoff, CapacityOverride: v.CapacityOverride,
		RepositoryMutationAllowed: v.RepositoryMutationAllowed, VerificationTask: v.VerificationTask,
		MaxDurationSeconds: v.MaxDurationSeconds, VerificationPolicy: v.VerificationPolicy,
		VerificationError: v.VerificationError, VerificationReason: v.VerificationReason, RowVersion: v.RowVersion,
		QueueNodeID: v.QueueNodeID, QueueTargetAgentID: v.QueueTargetAgentID, QueueRoutedAt: v.QueueRoutedAt,
		RequestedModel: v.RequestedModel, EffectiveModel: v.EffectiveModel, ModelEvidence: v.ModelEvidence,
		InputTokens: v.InputTokens, OutputTokens: v.OutputTokens, CachedInputTokens: v.CachedInputTokens,
		ReasoningTokens: v.ReasoningTokens, Cost: v.Cost, StartedAt: v.StartedAt, EndedAt: v.EndedAt, CreatedAt: v.CreatedAt,
		DurationMS: v.DurationMS, WaitingMS: v.WaitingMS, ActiveMS: v.ActiveMS, Outcome: v.Outcome,
		RunCapabilityRequired: true}
	if r.Header.Get(db.RunCapabilityPolicyHeader) != db.RunCapabilityPolicy {
		out.UpgradeReason = "run_capability_upgrade_required"
	}
	return out
}

func transportResult(r *http.Request, p tenant.Principal, result any) any {
	if p.TransportComputerID == "" || p.RunID != "" {
		return result
	}
	switch v := result.(type) {
	case Run:
		return transportRun(r, v)
	case []Run:
		for i := range v {
			v[i] = transportRun(r, v[i])
		}
		return v
	case runPage:
		for i := range v.Items {
			v.Items[i] = transportRun(r, v.Items[i])
		}
		return v
	}
	return result
}

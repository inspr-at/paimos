// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// AgentResultWriteTx uses the ordinary result writers. It must return audit
// without appending events, so the receipt is stored before the event counter.
type AgentResultWriteTx func(context.Context, pgx.Tx, tenant.Principal, string, string, string, AgentDestination, AgentResultInput) (string, []events.Change, error)

func (m *Module) WithAgentResultWriter(write AgentResultWriteTx) *Module {
	m.agentResultWriter = write
	return m
}

type AgentResultInput struct {
	Status       string `json:"status"`
	Title        string `json:"title,omitempty"`
	Body         string `json:"body"`
	CostMicroUSD *int64 `json:"cost_microusd,omitempty"`
}
type AgentResult struct {
	RunID         string `json:"run_id"`
	Status        string `json:"status"`
	RequestDigest string `json:"request_digest"`
	NodeID        string `json:"node_id,omitempty"`
	CostMicroUSD  *int64 `json:"cost_microusd,omitempty"`
	LeadMessageID string `json:"lead_message_id,omitempty"`
}

func (v AgentResultInput) validate() error {
	if v.Status != "succeeded" && v.Status != "failed" || strings.TrimSpace(v.Body) == "" || len(v.Body) > 65536 || len(v.Title) > 512 || !utf8.ValidString(v.Body+v.Title) || strings.ContainsRune(v.Body+v.Title, 0) || v.CostMicroUSD != nil && (*v.CostMicroUSD < 0 || *v.CostMicroUSD > 1000000000000) {
		return workorders.Fail(400, "invalid agent result")
	}
	return nil
}
func (m *Module) getAgentResult(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if !workorders.UUID(r.PathValue("runId")) {
		reply(w, 200, nil, workorders.Fail(400, "invalid run id"))
		return
	}
	var out *AgentResult
	err := db.InTenantReadSnapshot(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := historyDefinition(r.Context(), tx, p, r.PathValue("recurrenceId")); err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(r.Context(), `SELECT agent_result FROM routine_runs WHERE id=$1 AND recurrence_id=$2 AND agent_owner_config IS NOT NULL`, r.PathValue("runId"), r.PathValue("recurrenceId")).Scan(&raw); err != nil {
			return err
		}
		if len(raw) > 0 {
			return json.Unmarshal(raw, &out)
		}
		return nil
	})
	reply(w, 200, out, err)
}
func (m *Module) completeAgentResult(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Agent {
		reply(w, 200, nil, authz.ErrForbidden)
		return
	}
	id := r.PathValue("runId")
	if !workorders.UUID(id) {
		reply(w, 200, nil, workorders.Fail(400, "invalid run id"))
		return
	}
	var in AgentResultInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.validate(); err != nil {
		reply(w, 200, nil, err)
		return
	}
	var out AgentResult
	var policyErr error
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		if _, err := lock(ctx, tx, p.TenantID, false); err != nil {
			return err
		}
		item, err := load(ctx, tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		var raw, prior []byte
		var owner string
		if err = tx.QueryRow(ctx, `SELECT owner_principal_id::text,agent_owner_config FROM routine_runs WHERE id=$1 AND recurrence_id=$2 AND agent_owner_config IS NOT NULL`, id, item.ID).Scan(&owner, &raw); err != nil {
			return err
		}
		if owner != p.ID {
			return authz.ErrForbidden
		}
		var c AgentOwner
		if json.Unmarshal(raw, &c) != nil {
			return workorders.Fail(409, "invalid_agent_owner")
		}
		current, err := loadAgentOwner(ctx, tx, item.ID)
		if err != nil {
			return err
		}
		if current == nil || digestJSON(current) != digestJSON(c) {
			return workorders.Fail(409, "agent_owner_grant_changed")
		}
		if _, _, err = agentOwnerAuthority(ctx, tx, p.TenantID, item, c); err != nil {
			if reason, _ := authz.AssistantPolicyDenial(err); reason != "" {
				d, err := assistants.EvaluateTx(ctx, tx, assistants.Check{TenantID: p.TenantID, PrincipalID: p.ID, ProjectID: item.ProjectID, Operation: "routine.result", Key: "routine-run/" + id, Scopes: agentScopes(c)})
				if err != nil {
					return err
				}
				policyErr = workorders.Fail(409, d.Reason)
				return assistants.AppendDecisionsTx(ctx, tx, p, d)
			}
			return err
		}
		for _, permission := range []string{"nodes.read", Permission, agentOwnerPermission(c.Destination)} {
			if err = authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: item.ProjectID}); err != nil {
				return err
			}
		}
		// Lock all current destination rows before run/effect rows. The same tree
		// fence serializes target moves, edits and permission revocation.
		ids := []string{item.ProjectID, item.ParentID}
		if c.Destination.TicketID != "" {
			ids = append(ids, c.Destination.TicketID)
		}
		if _, err = tx.Exec(ctx, `SELECT id FROM nodes WHERE id=ANY($1::uuid[]) ORDER BY id FOR NO KEY UPDATE`, ids); err != nil {
			return err
		}
		if err = validateTarget(ctx, tx, item.Input, false); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `SELECT agent_result FROM routine_runs WHERE id=$1 FOR NO KEY UPDATE`, id).Scan(&prior); err != nil {
			return err
		}
		if len(prior) > 0 {
			if err = json.Unmarshal(prior, &out); err != nil {
				return err
			}
			if out.RequestDigest != digestJSON(in) {
				return workorders.Fail(409, "agent_result_conflict")
			}
			return nil
		}
		var dispatchState string
		if err = tx.QueryRow(ctx, `SELECT state FROM routine_effect_outbox WHERE run_id=$1 AND effect_key='agent-dispatch' FOR NO KEY UPDATE`, id).Scan(&dispatchState); err != nil {
			return err
		}
		if dispatchState != "succeeded" {
			return workorders.Fail(409, "agent_run_not_dispatched")
		}
		policyKey := "routine-run/" + id
		var delegationRaw []byte
		var delegation *AgentDelegation
		leadFailure := ""
		if err = tx.QueryRow(ctx, `SELECT agent_delegation FROM routine_runs WHERE id=$1`, id).Scan(&delegationRaw); err != nil {
			return err
		}
		if len(delegationRaw) > 0 {
			if len(delegationRaw) > 32768 || json.Unmarshal(delegationRaw, &delegation) != nil || delegation == nil || delegation.Instance != m.policyInstance {
				return errInvalidAssistantGrant()
			}
			policyKey = delegation.BudgetKey
			if err = validateDelegatingLeadTx(ctx, tx, p.TenantID, item.ProjectID, delegation.LeadPrincipalID, delegation.SessionID, delegation.OwnerPersonID, delegation.LeadKeyID); err != nil {
				if !errors.Is(err, authz.ErrForbidden) {
					return err
				}
				// The person grant and assistant policy authorize the stored
				// output. Lost lead authority only fails its result pointer.
				leadFailure = "assistant_delegating_lead_unavailable"
			}
		}
		policy, err := assistants.EvaluateTx(ctx, tx, assistants.Check{TenantID: p.TenantID, PrincipalID: p.ID, ProjectID: item.ProjectID, Operation: "routine.result", Key: policyKey, Scopes: agentScopes(c), RequireBudget: true})
		if err != nil {
			return err
		}
		if !policy.Allowed {
			policyErr = workorders.Fail(409, policy.Reason)
			return assistants.AppendDecisionsTx(ctx, tx, p, policy)
		}
		out = AgentResult{RunID: id, Status: in.Status, RequestDigest: digestJSON(in), CostMicroUSD: in.CostMicroUSD}
		var changes []events.Change
		if in.Status == "succeeded" {
			if c.Destination.Kind != "ticket_comment" && strings.TrimSpace(in.Title) == "" {
				return workorders.Fail(400, "result title required")
			}
			if m.agentResultWriter == nil {
				return workorders.Fail(409, "agent_result_writer_unavailable")
			}
			out.NodeID, changes, err = m.agentResultWriter(ctx, tx, p, item.ProjectID, item.ParentID, id, c.Destination, in)
			if err != nil {
				return err
			}
		}
		var leadChanges []events.Change
		if delegation != nil {
			sender := tenant.Principal{ID: c.GrantedByPrincipalID, TenantID: p.TenantID, Kind: tenant.Person}
			body := "Assistant routine " + id + ": " + in.Status + "\nResult node: " + out.NodeID + "\nResult receipt: /api/recurrences/" + item.ID + "/runs/" + id + "/agent-result\nThis is a stored result, not approval or execution authority."
			out.LeadMessageID, leadChanges, err = inbox.QueueAgentLeadResultTx(ctx, tx, sender, delegation.LeadPrincipalID, delegation.SessionID, item.ProjectID, id, body, leadFailure)
			if err != nil {
				return err
			}
		}

		raw, _ = json.Marshal(out)
		state := "done"
		if in.Status == "failed" {
			state = "failed"
		}
		if _, err = tx.Exec(ctx, `UPDATE routine_runs SET state=$2,agent_result=$3,updated_at=clock_timestamp() WHERE id=$1`, id, state, raw); err != nil {
			return err
		}
		// The existing effect ledger is the idempotent delivery receipt, including
		// failure evidence. Only a bounded diagnostic is retained for failed runs.
		detail := ""
		if in.Status == "failed" {
			detail = boundedText(in.Body, 2048)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO routine_effect_outbox(tenant_id,run_id,effect_key,kind,state,payload,result) VALUES($1,$2,'agent-result','routine.agent_result','succeeded',jsonb_build_object('failure_detail',$3::text),$4)`, p.TenantID, id, detail, raw); err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]string{"routine_run_id": id, "recurrence_id": item.ID, "on_behalf_of": c.GrantedByPrincipalID, "cost_source": "agent_reported"})
		// No existing-row lock after the event counter.
		for _, change := range changes {
			change.Metadata = meta
			if _, err = events.Append(ctx, tx, p, change); err != nil {
				return err
			}
		}
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &item.ProjectID, Type: "recurrence.agent_result", After: out, Metadata: meta})
		if err != nil {
			return err
		}
		if err = assistants.AppendDecisionsTx(ctx, tx, p, policy); err != nil {
			return err
		}
		if out.LeadMessageID != "" {
			return inbox.AppendAgentRoutineTx(ctx, tx, p, out.LeadMessageID, leadChanges)
		}
		return err
	})
	if err == nil {
		err = policyErr
	}
	reply(w, 200, out, err)
}

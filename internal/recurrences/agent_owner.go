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
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type AgentDestination struct {
	Kind     string `json:"kind"`
	TicketID string `json:"ticket_id,omitempty"`
}

// AgentOwner is an explicit person grant, separate from the accountable
// canonical person in Definition. It never changes a principal, key or role.
type AgentOwner struct {
	OwnerPrincipalID     string           `json:"owner_principal_id"`
	Destination          AgentDestination `json:"destination"`
	GrantedByPrincipalID string           `json:"granted_by_principal_id"`
	DefinitionDigest     string           `json:"definition_digest"`
}

// AgentBudgetRequest binds each admission to a tenant, owner, run and UTC week.
// The hook runs under the tenant/tree fence before any inbox acceptance. It
// must reserve/check the owner's weekly allowance atomically using this tx,
// including other admitted runs and unknown costs; no network work is allowed.
type AgentBudgetRequest struct {
	TenantID, OwnerPrincipalID, RunID string
	WeekStart, WeekEnd                time.Time
}
type AgentBudgetDecision struct{ Allowed bool }
type AgentBudgetTx func(context.Context, pgx.Tx, AgentBudgetRequest) (AgentBudgetDecision, error)

// WithAgentBudget supplies the policy-owned admission hook. Missing policy is
// closed, so this slice cannot enable unattended spending before integration.
func (m *Module) WithAgentBudget(check AgentBudgetTx) *Module { m.agentBudget = check; return m }

func agentDefinitionDigest(r Recurrence) string { raw, _ := json.Marshal(r.Input); return digest(raw) }
func loadAgentOwner(ctx context.Context, tx pgx.Tx, id string) (*AgentOwner, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT agent_owner_config FROM recurrence_definitions WHERE recurrence_id=$1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	var out AgentOwner
	if len(raw) > 8192 || json.Unmarshal(raw, &out) != nil {
		return nil, workorders.Fail(409, "invalid_agent_owner")
	}
	return &out, nil
}

func (d AgentDestination) validate() error {
	switch d.Kind {
	case "ticket_comment":
		if !workorders.UUID(d.TicketID) {
			return workorders.Fail(400, "ticket_comment requires ticket_id")
		}
	case "new_ticket", "knowledge":
		if d.TicketID != "" {
			return workorders.Fail(400, "ticket_id is only valid for ticket_comment")
		}
	default:
		return workorders.Fail(400, "invalid agent result destination")
	}
	return nil
}
func agentOwnerPermission(d AgentDestination) string {
	switch d.Kind {
	case "knowledge":
		return "knowledge.write"
	case "ticket_comment":
		return "comments.write"
	default:
		return "nodes.write"
	}
}

// Re-check the same live role grants used by ordinary writes. The scheduler's
// system visibility is not authority for either the person or the agent.
func agentOwnerAuthority(ctx context.Context, tx pgx.Tx, tid string, r Recurrence, c AgentOwner) (tenant.Principal, tenant.Principal, error) {
	person := tenant.Principal{ID: c.GrantedByPrincipalID, TenantID: tid, Kind: tenant.Person}
	owner := tenant.Principal{ID: c.OwnerPrincipalID, TenantID: tid, Kind: tenant.Agent, Scopes: []string{"nodes.read", Permission, agentOwnerPermission(c.Destination), "inbox.read"}}
	canonical, err := canonicalPerson(ctx, tx, person)
	if err != nil {
		return person, owner, err
	}
	if r.Definition == nil || canonical != person.ID || person.ID != r.Definition.OwnerPrincipalID || c.DefinitionDigest != agentDefinitionDigest(r) {
		return person, owner, workorders.Fail(409, "agent_owner_grant_changed")
	}
	if err = tx.QueryRow(ctx, `SELECT name FROM principals WHERE id=$1 AND kind='agent' AND status='active' AND linked_to IS NULL`, owner.ID).Scan(&owner.Name); err != nil {
		return person, owner, err
	}
	if err = validateDefinition(ctx, tx, person, r.Input); err != nil {
		return person, owner, err
	}
	for _, p := range []tenant.Principal{person, owner} {
		for _, permission := range []string{"nodes.read", Permission, agentOwnerPermission(c.Destination)} {
			if err = authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: r.ProjectID}); err != nil {
				return person, owner, err
			}
		}
	}
	if err = authz.RequireTx(ctx, tx, person, "inbox.send", authz.Scope{ProjectID: r.ProjectID}); err != nil {
		return person, owner, err
	}
	if err = authz.RequireTx(ctx, tx, owner, "inbox.read", authz.Scope{ProjectID: r.ProjectID}); err != nil {
		return person, owner, err
	}
	if c.Destination.Kind == "ticket_comment" {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL AND k.slug='work')`, c.Destination.TicketID, r.ProjectID).Scan(&exists); err != nil {
			return person, owner, err
		}
		if !exists {
			return person, owner, pgx.ErrNoRows
		}
	}
	return person, owner, nil
}

func (m *Module) getAgentOwner(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var out *AgentOwner
	err := db.InTenantReadSnapshot(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		item, err := load(r.Context(), tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err = readDefinition(r.Context(), tx, p, item.Input); err != nil {
			return err
		}
		out, err = loadAgentOwner(r.Context(), tx, item.ID)
		return err
	})
	reply(w, 200, out, err)
}
func (m *Module) setAgentOwner(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if !modelregistry.InteractivePerson(p) || r.Header.Get("Authorization") != "" {
		reply(w, 200, nil, authz.ErrForbidden)
		return
	}
	var in struct {
		ExpectedRevision int64            `json:"expected_revision"`
		OwnerPrincipalID string           `json:"owner_principal_id"`
		Destination      AgentDestination `json:"destination"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !workorders.UUID(in.OwnerPrincipalID) {
		reply(w, 200, nil, workorders.Fail(400, "invalid agent owner"))
		return
	}
	if err := in.Destination.validate(); err != nil {
		reply(w, 200, nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var out AgentOwner
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := lock(ctx, tx, p.TenantID, false); err != nil {
			return err
		}
		item, err := load(ctx, tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err = authorizeDefinition(ctx, tx, p, item.Input); err != nil {
			return err
		}
		if in.ExpectedRevision != item.Revision {
			return workorders.Fail(409, "recurrence revision changed")
		}
		if item.Definition == nil || item.Definition.Scope.Kind != "project" || item.Definition.Scope.ProjectID != item.ProjectID || item.Definition.Assignment != nil || item.QueueEach {
			return workorders.Fail(409, "agent ownership requires a project routine without native assignment or queue_each")
		}
		canonical, err := canonicalPerson(ctx, tx, p)
		if err != nil {
			return err
		}
		if canonical != item.Definition.OwnerPrincipalID {
			return authz.ErrForbidden
		}
		out = AgentOwner{OwnerPrincipalID: strings.ToLower(in.OwnerPrincipalID), Destination: in.Destination, GrantedByPrincipalID: canonical, DefinitionDigest: agentDefinitionDigest(item)}
		if _, _, err = agentOwnerAuthority(ctx, tx, p.TenantID, item, out); err != nil {
			return err
		}
		ids := append([]string{item.ProjectID, item.ParentID}, item.Template.Tags...)
		if out.Destination.TicketID != "" {
			ids = append(ids, out.Destination.TicketID)
		}
		if _, err = tx.Exec(ctx, `SELECT id FROM nodes WHERE id=ANY($1::uuid[]) ORDER BY id FOR NO KEY UPDATE`, ids); err != nil {
			return err
		}
		if _, err = load(ctx, tx, item.ID, true); err != nil {
			return err
		}
		raw, _ := json.Marshal(out)
		if _, err = tx.Exec(ctx, `UPDATE recurrence_definitions SET agent_owner_config=$2 WHERE recurrence_id=$1`, item.ID, raw); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE recurrences SET paused=true,revision=revision+1,updated_at=clock_timestamp() WHERE id=$1`, item.ID); err != nil {
			return err
		}
		return record(ctx, tx, p, item.ProjectID, "recurrence.updated", nil, map[string]any{"id": item.ID, "revision": item.Revision + 1, "agent_owner": out, "paused": true})
	})
	reply(w, 200, out, err)
}

// Ordinary editor authorization is unchanged. Agent grants bind the complete
// input digest, so any edit becomes inert until an interactive person renews it.
func persistAgentRun(ctx context.Context, tx pgx.Tx, actor tenant.Principal, r Recurrence, o *Occurrence, c *AgentOwner) (bool, error) {
	if c == nil {
		return false, nil
	}
	if r.Definition == nil || o.NodeID == nil {
		return true, workorders.Fail(409, "agent routine definition unavailable")
	}
	assignment, err := json.Marshal(r.Input)
	if err != nil {
		return true, err
	}
	policy, _ := json.Marshal(c)
	run := RunReceipt{State: RunCreated, AssignmentDigest: digest(assignment), PolicyDigest: digest(policy)}
	err = tx.QueryRow(ctx, `INSERT INTO routine_runs(tenant_id,recurrence_id,occurrence_key,definition_revision,scope_type,scope_project_id,owner_principal_id,output_project_id,output_parent_id,assignment,assignment_digest,policy_snapshot,policy_digest,execution_principal_id,work_node_id,source_receipt,state,agent_owner_config)
 VALUES($1,$2,$3,$4,'project',$5,$6,$5,$7,$8,$9,$10,$11,$6,$12,'{}','created',$10) RETURNING id::text`, actor.TenantID, r.ID, o.Key, r.Revision, r.ProjectID, c.OwnerPrincipalID, r.ParentID, assignment, run.AssignmentDigest, policy, run.PolicyDigest, *o.NodeID).Scan(&run.ID)
	if err != nil {
		return true, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO routine_effect_outbox(tenant_id,run_id,effect_key,kind,payload) VALUES($1,$2,'agent-dispatch','routine.agent_dispatch',jsonb_build_object('run_id',$2::uuid::text))`, actor.TenantID, run.ID)
	if err == nil {
		o.Run = &run
	}
	return true, err
}

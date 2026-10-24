// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type delegationContextKey struct{}
type AgentDelegation struct {
	SessionID       string   `json:"session_id"`
	LeadPrincipalID string   `json:"lead_principal_id"`
	LeadKeyID       string   `json:"lead_key_id"`
	OwnerPersonID   string   `json:"owner_person_id"`
	Instance        string   `json:"instance"`
	Scopes          []string `json:"scopes"`
	BudgetKey       string   `json:"budget_key"`
	RequestDigest   string   `json:"request_digest"`
}

func errInvalidAssistantGrant() error { return workorders.Fail(409, "invalid_assistant_grant") }

func validateDelegatingLeadTx(ctx context.Context, tx pgx.Tx, tid, project, lead, session, owner, key string) error {
	if err := authz.RequireQueueDispatcherTx(ctx, tx, tid, lead, key, project); err != nil {
		return err
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions s JOIN principals p ON p.tenant_id=s.tenant_id AND p.id=s.agent_principal_id
 JOIN principals o ON o.tenant_id=s.tenant_id AND o.id=s.owner_principal_id
 WHERE s.tenant_id=$1::uuid AND s.id=$2::uuid AND s.project_id=$3::uuid AND s.agent_principal_id=$4::uuid
 AND coalesce(o.linked_to,o.id)=$5::uuid AND o.kind='person' AND o.status='active' AND p.kind='agent' AND p.status='active'
 AND s.role='coordinator' AND s.phase IN ('working','yielded') AND s.stopped_at IS NULL
 AND EXISTS(SELECT 1 FROM agent_keys k JOIN principals kp ON kp.tenant_id=k.tenant_id AND kp.id=k.person_id WHERE k.tenant_id=s.tenant_id AND k.id=$6::uuid AND k.principal_id=s.agent_principal_id AND coalesce(kp.linked_to,kp.id)=$5::uuid AND kp.kind='person' AND kp.status='active'))`, tid, session, project, lead, owner, key).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return authz.ErrForbidden
	}
	return nil
}

func (m *Module) delegateAssistant(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Agent || p.KeyPersonID == "" || p.AuthKeyID == "" {
		reply(w, 200, nil, authz.ErrForbidden)
		return
	}
	var in struct {
		SessionID string   `json:"session_id"`
		Instance  string   `json:"instance"`
		Scopes    []string `json:"scopes"`
		Key       string   `json:"idempotency_key"`
		Revision  int64    `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !workorders.UUID(in.SessionID) || len(in.Instance) > 2048 || in.Instance == "" || len(in.Scopes) == 0 || len(in.Scopes) > 200 || strings.TrimSpace(in.Key) == "" || len(in.Key) > 128 || in.Revision < 1 {
		reply(w, 200, nil, workorders.Fail(400, "invalid delegation"))
		return
	}
	slices.Sort(in.Scopes)
	for i, s := range in.Scopes {
		if len(s) > 100 || i > 0 && in.Scopes[i-1] == s {
			reply(w, 200, nil, workorders.Fail(400, "invalid delegation scopes"))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	// Read-only admission before provisioning the existing keyless routine actor.
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		item, err := load(ctx, tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		return authz.RequireQueueCoordinatorTx(ctx, tx, p, item.ProjectID)
	})
	if err != nil {
		reply(w, 200, nil, err)
		return
	}
	actor, err := ensureActor(ctx, m, p.TenantID)
	if err != nil {
		reply(w, 200, nil, err)
		return
	}
	var out Occurrence
	var refused error
	err = db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := lock(ctx, tx, p.TenantID, false); err != nil {
			return err
		}
		item, err := load(ctx, tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err = authz.RequireQueueCoordinatorTx(ctx, tx, p, item.ProjectID); err != nil {
			return err
		}
		for _, scope := range []string{"nodes.read", "inbox.send", "harness.read"} {
			if err = authz.RequireTx(ctx, tx, p, scope, authz.Scope{ProjectID: item.ProjectID}); err != nil {
				return err
			}
		}
		c, err := loadAgentOwner(ctx, tx, item.ID)
		if err != nil {
			return err
		}
		if c == nil {
			return workorders.Fail(403, "assistant_person_grant_required")
		}
		person, _, err := agentOwnerAuthority(ctx, tx, p.TenantID, item, *c)
		if err != nil {
			return err
		}
		var personID string
		if err = tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE id=$1::uuid AND kind='person' AND status='active'`, p.KeyPersonID).Scan(&personID); err != nil {
			return authz.ErrForbidden
		}
		if personID != person.ID {
			return authz.ErrForbidden
		}
		snapshot, err := assistants.New(m.pool).ReadPolicyTx(ctx, tx, p.TenantID, c.OwnerPrincipalID)
		if err != nil {
			return err
		}
		if snapshot.OwnerPersonID != personID {
			return authz.ErrForbidden
		}
		if err = validateDelegatingLeadTx(ctx, tx, p.TenantID, item.ProjectID, p.ID, in.SessionID, personID, p.AuthKeyID); err != nil {
			return err
		}
		if in.Revision != item.Revision {
			return workorders.Fail(409, "recurrence revision changed")
		}
		d := AgentDelegation{SessionID: in.SessionID, LeadPrincipalID: p.ID, LeadKeyID: p.AuthKeyID, OwnerPersonID: personID, Instance: in.Instance, Scopes: in.Scopes, RequestDigest: digestJSON(in), BudgetKey: "delegation/" + in.SessionID + "/" + digest([]byte(in.Key))}
		key := "manual:delegation/" + in.SessionID + "/" + digest([]byte(in.Key))
		var prior []byte
		err = tx.QueryRow(ctx, `SELECT r.agent_delegation FROM routine_runs r WHERE r.recurrence_id=$1 AND r.occurrence_key=$2`, item.ID, key).Scan(&prior)
		if err == nil {
			var old AgentDelegation
			if json.Unmarshal(prior, &old) != nil || old.RequestDigest != d.RequestDigest {
				return workorders.Fail(409, "delegation_idempotency_conflict")
			}
			out, err = scanOccurrence(tx.QueryRow(ctx, `SELECT `+occurrenceColumns+` FROM recurrence_occurrences WHERE recurrence_id=$1 AND occurrence_key=$2`, item.ID, key))
			if err == nil {
				err = runReceipt(ctx, tx, &out)
			}
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		refusal := ""
		if m.policyInstance == "" || in.Instance != m.policyInstance {
			refusal = "assistant_instance_mismatch"
		}
		granted := agentScopes(*c)
		for _, scope := range in.Scopes {
			if !slices.Contains(granted, scope) {
				refusal = "assistant_delegation_scope_exceeds_grant"
			}
		}
		for _, scope := range granted {
			if !slices.Contains(in.Scopes, scope) {
				refusal = "assistant_delegation_scope_missing"
			}
		}
		now, err := m.clock(ctx, tx)
		if err != nil {
			return err
		}
		policy, err := assistants.EvaluateTx(ctx, tx, assistants.Check{TenantID: p.TenantID, PrincipalID: c.OwnerPrincipalID, ProjectID: item.ProjectID, Operation: "lead.delegate", Key: d.BudgetKey, Scopes: in.Scopes, Charge: true, Ceiling: c.BudgetCeiling, RequireAssistant: true, Now: now, Refusal: refusal})
		if err != nil {
			return err
		}
		if !policy.Allowed {
			refused = workorders.Fail(409, policy.Reason)
			return assistants.AppendDecisionsTx(ctx, tx, p, policy)
		}
		delegateCtx := context.WithValue(ctx, delegationContextKey{}, &d)
		out, err = occurSource(delegateCtx, tx, actor, item, key, now, nil, "", "", nil)
		if err != nil {
			return err
		}
		if out.Run == nil {
			return workorders.Fail(409, "assistant_delegation_not_created: "+out.Reason)
		}
		if err = assistants.AppendDecisionsTx(ctx, tx, p, policy); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &item.ProjectID, Type: "assistant.delegated", After: map[string]any{"principal_id": c.OwnerPrincipalID, "run_id": out.Run.ID, "delegation": d}})
		return err
	})
	if err == nil {
		err = refused
	}
	reply(w, 200, out, err)
}

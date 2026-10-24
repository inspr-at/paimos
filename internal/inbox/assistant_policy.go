// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Callers hold the inbox access fence. Execution spend belongs to the receiving
// assistant. Acceptance checks affordability; the final exposure reserves it.
func assistantDeliveryPolicyTx(ctx context.Context, tx pgx.Tx, sender tenant.Principal, recipient, project, message, key, body string, collect func(events.Change) error) (string, error) {
	if key == "" {
		key = "message/" + message
	}
	checks := []assistants.Check{{TenantID: sender.TenantID, PrincipalID: sender.ID, ProjectID: project, Operation: "message.send", Key: "send/" + message, Scopes: []string{"inbox.send"}}}
	var person bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid AND kind='person')`, sender.TenantID, recipient).Scan(&person); err != nil {
		return "", err
	}
	if person {
		checks[0].Refusal = assistants.HumanInputRefusal("none", body)
	}
	var push bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_message_targets t JOIN assistant_adapters a ON a.tenant_id=t.tenant_id AND a.target_id=t.id WHERE t.tenant_id=$1::uuid AND t.principal_id=$2::uuid AND t.project_id=nullif($3,'')::uuid AND t.enabled AND t.role='primary' AND t.target_kind='https_webhook')`, sender.TenantID, recipient, project).Scan(&push); err != nil {
		return "", err
	}
	checks = append(checks, assistants.Check{TenantID: sender.TenantID, PrincipalID: recipient, ProjectID: project, Operation: "adapter.accept", Key: key, Scopes: []string{"inbox.read"}, RequireBudget: push, MessageCost: true})
	for _, check := range checks {
		d, err := assistants.EvaluateTx(ctx, tx, check)
		if err != nil {
			return "", err
		}
		for _, change := range d.Changes {
			if err = collect(change); err != nil {
				return "", err
			}
		}
		if !d.Allowed {
			return d.Reason, nil
		}
		if d.Applicable && check.PrincipalID == recipient {
			p := tenant.Principal{ID: recipient, TenantID: sender.TenantID, Kind: tenant.Agent, Scopes: []string{"inbox.read"}}
			if err = authz.RequireTx(ctx, tx, p, "inbox.read", authz.Scope{ProjectID: project}); err != nil {
				return "", err
			}
		}
	}
	return "", nil
}

// Pickup is another admission boundary for sleeping pull hosts. In particular,
// revoking project or result scopes after queueing must not leak queued work.
func (m *module) assistantPickupPolicyTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, items []Message) ([]Message, []assistants.Decision, error) {
	if p.Kind != tenant.Agent || len(items) == 0 {
		return items, nil, nil
	}
	var assistant bool
	if err := tx.QueryRow(ctx, `SELECT coalesce(messaging_only,false) OR coalesce(agent_class,'')='assistant' FROM principals WHERE id=$1::uuid`, p.ID).Scan(&assistant); err != nil {
		return nil, nil, err
	}
	if !assistant {
		return items, nil, nil
	}
	ids := make([]string, len(items))
	for i, msg := range items {
		ids[i] = msg.ID
	}
	// Fence the whole bounded page in one stable order before receipt writes.
	if _, err := tx.Exec(ctx, `SELECT id FROM inbox_messages WHERE id=ANY($1::uuid[]) ORDER BY id FOR NO KEY UPDATE`, ids); err != nil {
		return nil, nil, err
	}
	decisions := []assistants.Decision{}
	kept := make([]Message, 0, len(items))
	for _, msg := range items {
		var project, key string
		var raw, current []byte
		run := ""
		if candidate, ok := strings.CutPrefix(msg.IdempotencyKey, "routine-run/"); ok {
			if _, valid := parseUUID(candidate); valid {
				run = candidate
			}
		}
		if err := tx.QueryRow(ctx, `SELECT CASE WHEN m.assistant_policy_key='implicit-test/'||m.id::text THEN '' ELSE coalesce(m.assistant_policy_project_id::text,(SELECT project_id::text FROM inbox_compat_messages WHERE inbox_message_id=m.id),(SELECT output_project_id::text FROM routine_runs WHERE id=nullif($2,'')::uuid),'') END,coalesce(m.assistant_policy_key,'message/'||m.id::text),
 (SELECT agent_owner_config FROM routine_runs WHERE id=nullif($2,'')::uuid),
 (SELECT d.agent_owner_config FROM recurrence_definitions d JOIN routine_runs r ON r.recurrence_id=d.recurrence_id WHERE r.id=nullif($2,'')::uuid)
 FROM inbox_messages m WHERE m.id=$1::uuid`, msg.ID, run).Scan(&project, &key, &raw, &current); err != nil {
			return nil, nil, err
		}
		scopes := []string{"inbox.read"}
		grantRefusal := ""
		if err := authz.RequireTx(ctx, tx, p, "inbox.read", authz.Scope{ProjectID: project}); err != nil {
			if !errors.Is(err, authz.ErrForbidden) {
				return nil, nil, err
			}
			// Policy ceilings get their precise reason from EvaluateTx below;
			// ordinary role revocation must also refuse before charging/pickup.
			if reason, _ := authz.AssistantPolicyDenial(err); reason == "" {
				grantRefusal = "assistant_inbox_authority_revoked"
			}
		}
		if run != "" {
			var owner struct {
				GrantedBy   string `json:"granted_by_principal_id"`
				Destination struct {
					Kind string `json:"kind"`
				} `json:"destination"`
			}
			if len(raw) > 8192 || json.Unmarshal(raw, &owner) != nil || string(raw) != string(current) {
				grantRefusal = "assistant_routine_grant_changed"
			} else {
				consent, err := assistants.RoutineGrantRefusalTx(ctx, tx, p.TenantID, p.ID, owner.GrantedBy)
				if err != nil {
					return nil, nil, err
				}
				if consent != "" {
					grantRefusal = consent
				}
				permission := "nodes.write"
				if owner.Destination.Kind == "knowledge" {
					permission = "knowledge.write"
				} else if owner.Destination.Kind == "ticket_comment" {
					permission = "comments.write"
				}
				scopes = []string{"nodes.read", "recurrences.manage", permission, "inbox.read"}
				for _, actor := range []tenant.Principal{p, {ID: owner.GrantedBy, TenantID: p.TenantID, Kind: tenant.Person}} {
					for _, scope := range scopes[:3] {
						if err := authz.RequireTx(ctx, tx, actor, scope, authz.Scope{ProjectID: project}); err != nil {
							if !errors.Is(err, authz.ErrForbidden) {
								return nil, nil, err
							}
							grantRefusal = "assistant_routine_authority_revoked"
						}
					}
				}
			}
		}
		if run == "" {
			key += "/pull"
		}
		d, err := assistants.EvaluateTx(ctx, tx, assistants.Check{TenantID: p.TenantID, PrincipalID: p.ID, ProjectID: project, Operation: "adapter.pull", Key: key, Scopes: scopes, Charge: true, MessageCost: true, Refusal: grantRefusal})
		if err != nil {
			return nil, nil, err
		}
		if !d.Allowed {
			if _, err := FailMessage(ctx, tx, msg.ID, d.Reason); err != nil {
				return nil, nil, err
			}
		} else {
			kept = append(kept, msg)
		}
		decisions = append(decisions, d)
	}
	return kept, decisions, nil
}

func assistantReceiptErrorTx(ctx context.Context, tx pgx.Tx, id string) error {
	var reason string
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT failure_reason FROM inbox_receipts WHERE message_id=$1::uuid AND state='failed'),'')`, id).Scan(&reason); err != nil {
		return err
	}
	if strings.HasPrefix(reason, "assistant_") {
		return &httpError{409, "assistant_policy_denied", reason + "; message receipt: " + id}
	}
	return nil
}

// A deadline can elapse while a page is being prepared. If handOver drops an
// item after a new hold, abort this unexposed page so its holds and audit roll
// back together. The next poll omits the expired item and admits the survivors.
func assistantPickupUnchanged(items []Message, alive map[string]bool, decisions []assistants.Decision) error {
	if len(alive) != len(items) {
		for _, d := range decisions {
			if d.Charged {
				return &httpError{409, "pickup_changed", "message availability changed; retry pickup"}
			}
		}
	}
	return nil
}

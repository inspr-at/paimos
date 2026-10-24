// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"
	"strings"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Callers hold the inbox access fence. Execution spend belongs to the receiving
// assistant. Pure pull transport costs nothing; routines reserve separately.
func assistantDeliveryPolicyTx(ctx context.Context, tx pgx.Tx, sender tenant.Principal, recipient, project, message, key string, collect func(events.Change) error) (string, error) {
	if key == "" {
		key = "message/" + message
	}
	checks := []assistants.Check{{TenantID: sender.TenantID, PrincipalID: sender.ID, ProjectID: project, Operation: "message.send", Key: "send/" + message, Scopes: []string{"inbox.send"}}}
	var push bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_message_targets t JOIN assistant_adapters a ON a.tenant_id=t.tenant_id AND a.target_id=t.id WHERE t.tenant_id=$1::uuid AND t.principal_id=$2::uuid AND t.project_id=nullif($3,'')::uuid AND t.enabled AND t.role='primary' AND t.target_kind='https_webhook')`, sender.TenantID, recipient, project).Scan(&push); err != nil {
		return "", err
	}
	checks = append(checks, assistants.Check{TenantID: sender.TenantID, PrincipalID: recipient, ProjectID: project, Operation: "adapter.accept", Key: key, Scopes: []string{"inbox.read"}, Charge: push})
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

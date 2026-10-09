// SPDX-License-Identifier: AGPL-3.0-only
package stepup

import (
	"context"
	"strings"

	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func resultLine(r ApprovalRequest) string {
	actor := nullableValue(r.DecidedByName)
	if actor == "" {
		actor = nullableValue(r.DecidedBy)
	}
	// Keep a principal label on one line; provider-specific biometrics are not
	// inferred from a platform authenticator (Face ID and Touch ID look alike).
	actor = strings.Join(strings.Fields(actor), " ")
	switch r.State {
	case "applied":
		method := "passkey"
		switch nullableValue(r.Method) {
		case "passkey_platform":
			method = "device passkey"
		case "oidc_reauth":
			method = "fresh sign-in"
		}
		return "Approved by " + actor + " with " + method + " · applied"
	case "declined":
		return "Declined by " + actor
	case "expired":
		return "Expired after 15 min · ask again"
	case "stale":
		return "Not applied · changed meanwhile"
	case "failed":
		return "Not applied · the change could not be saved"
	case "withdrawn":
		return "Withdrawn"
	}
	return ""
}

// Called exactly once by the conditional pending -> terminal write, before
// the audit counter. No arbitrary session successor or process is selected.
func recordResult(ctx context.Context, tx pgx.Tx, p tenant.Principal, r ApprovalRequest) error {
	project := nullableValue(r.ProjectID)
	if r.SessionID != nil {
		// Workspace requests keep workspace authority, but their result belongs
		// in the requesting session's project chat, never an unrelated project.
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM harness_sessions WHERE tenant_id=$1 AND id=$2 AND agent_principal_id=$3`, p.TenantID, *r.SessionID, r.RequestedBy).Scan(&project); err != nil {
			return err
		}
	}
	return inbox.RecordResult(ctx, tx, p, inbox.Result{ID: r.ID, ProjectID: project, RecipientID: r.RequestedBy, SessionID: nullableValue(r.SessionID), Body: resultLine(r)})
}

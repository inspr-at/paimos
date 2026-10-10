// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// A paired principal has a permanent resource ceiling even if somebody later
// changes its workspace role or creates another key. Only its own execution
// resources are reachable; pairing never grants tenant administration.
func (m *Module) pairingBoundary(r *http.Request, p tenant.Principal) error {
	return db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if authz.OwnerWorkstation(p) {
			_, _, _, err := authz.WorkstationKeyTx(r.Context(), tx, p)
			return err
		}
		paired, err := agentpairing.PairedPrincipal(r.Context(), tx, p.ID)
		if err != nil || !paired {
			return err
		}
		var live bool
		if err = tx.QueryRow(r.Context(), `SELECT c.state<>'revoked' AND q.state='redeemed' FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id WHERE c.principal_id=$1`, p.ID).Scan(&live); err != nil {
			return err
		}
		if !live {
			return &agentpairing.Error{Status: 410, Code: "pairing_revoked", Message: "pairing revoked or setup not redeemed"}
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		deny := &agentpairing.Error{Status: 403, Code: "forbidden", Message: "outside this computer's runtime authority"}
		if len(parts) < 2 {
			return deny
		}
		switch parts[1] {
		case "status":
			// A live paired computer may read the same tenant status metadata;
			// this does not admit other paths or bypass pairing revocation.
			if len(parts) == 3 && parts[2] == "help" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				return nil
			}
		case "me":
			if r.Method == "GET" && len(parts) == 2 {
				return nil
			}
		case "agentd":
			if r.Pattern == "GET /api/agentd/step-ups/{challenge_id}" {
				return nil // Handler requires the runtime key plus the computer lifecycle proof.
			}
		case "harness-recoveries":
			if r.Method == "POST" && len(parts) == 3 && parts[2] == "claim" {
				return nil // The claim handler selects only this principal's exact daemon generation.
			}
			if r.Method == "POST" && len(parts) == 4 && validRouteUUID(parts[2]) && parts[3] == "complete" {
				var own bool
				if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM harness_recoveries q JOIN harness_sessions s ON s.tenant_id=q.tenant_id AND s.id=q.session_id WHERE q.id=$1 AND s.agent_principal_id=$2)`, parts[2], p.ID).Scan(&own); err != nil {
					return err
				}
				if own {
					return nil
				}
			}
		case "agent-pairing":
			if r.Method == "POST" && r.URL.Path == "/api/agent-pairing/account-link" {
				return nil // Handler binds the account and installation proof to this principal.
			}
			if r.Method == "POST" && r.URL.Path == "/api/agent-pairing/attach" {
				return nil
			}
			if r.URL.Path == "/api/agent-pairing/self" && r.Method == "GET" || (r.URL.Path == "/api/agent-pairing/self/disconnect" || r.URL.Path == "/api/agent-pairing/self/capacity" || r.URL.Path == "/api/agent-pairing/self/ledger") && r.Method == "POST" {
				return nil
			}
		case "models":
			if r.Method == "GET" && len(parts) == 2 {
				return nil
			}
		case "agent-accounts":
			if r.Method == "GET" && (r.URL.Path == "/api/agent-accounts/capacity/next" || r.URL.Path == "/api/agent-accounts/use") {
				return nil
			} // Handler restricts paired principals to their own accounts.
			// Quota signals, the status-line read, and the tenant quota key are
			// the registering computer's own account, same as a readings read.
			// Opting the status line in stays with a person.
			if len(parts) == 4 && validRouteUUID(parts[2]) && (r.Method == "GET" && (parts[3] == "readings" || parts[3] == "statusline") || r.Method == "PUT" && parts[3] == "signals" || r.Method == "POST" && parts[3] == "quota-key") {
				var own bool
				if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_pairing_enrollments e JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id WHERE e.account_id=$1 AND c.principal_id=$2 AND a.registered_by_principal_id=$2 AND e.state<>'revoked')`, parts[2], p.ID).Scan(&own); err != nil {
					return err
				}
				if own {
					return nil
				}
				return deny
			}
			if r.Method == "GET" && len(parts) == 2 {
				return nil
			}
			if r.Method == "POST" && (len(parts) == 3 && parts[2] == "route" || len(parts) == 4 && (parts[3] == "probe" || parts[3] == "readings")) {
				return nil
			}
		case "runs":
			if r.Method == "GET" {
				return nil
			}
			if r.Method == "POST" && len(parts) == 4 && (parts[3] == "claim" || parts[3] == "telemetry") {
				return nil
			}
		case "work-orders":
			if len(parts) >= 3 && validRouteUUID(parts[2]) {
				var own bool
				if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE work_order_id=$1 AND agent_principal_id=$2)`, parts[2], p.ID).Scan(&own); err != nil {
					return err
				}
				if own && (r.Method == "GET" && len(parts) == 3 || r.Method == "PATCH" && len(parts) == 3 || r.Method == "POST" && len(parts) == 4 && parts[3] == "evidence" || r.Method == "POST" && len(parts) == 6 && parts[3] == "criteria" && parts[5] == "check") {
					return nil
				}
			}
		case "nodes":
			if r.Method != "GET" || len(parts) != 3 {
				return deny
			}
			if parts[2] == "lookup" {
				keys := strings.Split(r.URL.Query().Get("keys"), ",")
				if len(keys) == 0 || len(keys) > 4 {
					return deny
				}
				for _, key := range keys {
					var own bool
					if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM nodes n JOIN agent_runs r ON r.tenant_id=n.tenant_id AND r.work_order_id=n.id WHERE n.key=$1 AND r.agent_principal_id=$2)`, key, p.ID).Scan(&own); err != nil {
						return err
					}
					if !own {
						return deny
					}
				}
				return nil
			}
			if validRouteUUID(parts[2]) {
				var own bool
				if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE work_order_id=$1 AND agent_principal_id=$2)`, parts[2], p.ID).Scan(&own); err != nil {
					return err
				}
				if own {
					return nil
				}
			}
		case "projects":
			if len(parts) >= 4 && validRouteUUID(parts[2]) && parts[3] == "harness-sessions" {
				var own bool
				if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM nodes n JOIN agent_runs r ON r.tenant_id=n.tenant_id AND r.work_order_id=n.id WHERE n.project_id=$1 AND r.agent_principal_id=$2)`, parts[2], p.ID).Scan(&own); err != nil {
					return err
				}
				if own {
					return nil
				} // Harness module independently checks run/session owner and worker lease; key has no control/recovery scopes.
			}
		}
		return deny
	})
}
func pairedIdentity(ctx context.Context, tx pgx.Tx, principal string) error {
	if principal == "" {
		return nil
	}
	paired, err := agentpairing.PairedPrincipal(ctx, tx, principal)
	if err != nil {
		return err
	}
	if paired {
		return errServicePrincipal
	}
	return nil
}

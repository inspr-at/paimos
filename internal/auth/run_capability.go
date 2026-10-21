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
	"github.com/inspr-at/paimos/internal/workerlease"
	"github.com/jackc/pgx/v5"
)

// Bare computer credentials carry transport authority only. This is an exact
// route allowlist, not a resource-prefix exemption. Each handler still checks
// its own computer/account/lease and must project only metadata.
func transportMetadataRoute(r *http.Request) bool {
	if workerlease.SafetyRoute(r) && workerlease.Read(r) != "" {
		return true
	}
	switch r.Pattern {
	case "GET /api/me", "GET /api/status/help", "GET /api/models",
		"GET /api/runs", "GET /api/runs/queued", "GET /api/runs/{runId}",
		"GET /api/runs/queued/notifications", "GET /api/runs/daemon-waits", "GET /api/runs/{runId}/daemon-wait",
		"PUT /api/runs/{runId}/daemon-wait", "POST /api/runs/{runId}/telemetry", "POST /api/runs/{runId}/capability", "POST /api/runs/{runId}/claim",
		"GET /api/agent-pairing/self", "POST /api/agent-pairing/self/disconnect", "POST /api/agent-pairing/self/capacity", "POST /api/agent-pairing/self/ledger",
		"POST /api/agent-pairing/account-link", "POST /api/agent-pairing/attach",
		"GET /api/agent-accounts", "GET /api/agent-accounts/use", "GET /api/agent-accounts/capacity/next",
		"POST /api/agent-accounts/{accountId}/probe", "POST /api/agent-accounts/probes",
		"GET /api/agent-accounts/{accountId}/readings", "POST /api/agent-accounts/{accountId}/readings",
		"GET /api/agent-accounts/{accountId}/quota-series", "GET /api/agent-accounts/{accountId}/statusline",
		"PUT /api/agent-accounts/{accountId}/signals", "POST /api/agent-accounts/{accountId}/quota-key",
		"POST /api/models/harness-probes/claim", "POST /api/models/harness-probes/{probeId}",
		"GET /api/agentd/step-ups/{challenge_id}",
		"POST /api/harness-recoveries/claim", "POST /api/harness-recoveries/{requestId}/complete",
		// Native sessions have their own server-issued actor grants and exact
		// operation/generation broker. These are not work-order content readers.
		"POST /api/agentd/native-sessions/claim", "POST /api/agentd/native-sessions/receipts",
		"POST /api/agentd/native-sessions/events", "POST /api/agentd/native-sessions/finals", "GET /api/agentd/native-sessions/commands":
		return true
	}
	return false
}

func (m *Module) runContentBoundary(r *http.Request, p tenant.Principal) (*http.Request, tenant.Principal, error) {
	values := r.Header.Values(db.RunCapabilityHeader)
	if len(values) == 0 {
		if p.TransportComputerID != "" && !transportMetadataRoute(r) {
			return r, p, &agentpairing.Error{Status: 403, Code: "run_capability_upgrade_required", Message: "Upgrade agentd: run_content_v1 capability required for content and launch"}
		}
		return r, p, nil
	}
	if len(values) != 1 || len(values[0]) != 64 || p.TransportComputerID == "" {
		return r, p, authz.ErrForbidden
	}
	token := values[0]
	err := db.InTenant(tenant.WithPrincipal(r.Context(), p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(r.Context(), tx); err != nil {
			return err
		}
		var err error
		p, err = db.RunCapabilityPrincipalTx(r.Context(), tx, p, token)
		return err
	})
	if err != nil {
		return r, p, authz.ErrForbidden
	}
	if !runCapabilityTarget(r, p) {
		return r, p, authz.ErrForbidden
	}
	ctx := tenant.WithPrincipal(r.Context(), p)
	ctx = db.WithAdditionalTenantGuard(ctx, func(ctx context.Context, tx pgx.Tx, tid string) error {
		if tid != p.TenantID {
			return authz.ErrForbidden
		}
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		current, err := db.RunCapabilityPrincipalTx(ctx, tx, p, token)
		if err != nil || current.KeyPersonID != p.KeyPersonID || current.RunComputerID != p.TransportComputerID {
			return authz.ErrForbidden
		}
		// Access is live, including for reads after an external wait. The
		// shared tenant/tree fence serializes revocation with final writes.
		if err := authz.RequireTx(ctx, tx, current, "nodes.read", authz.Scope{ProjectID: current.RunProjectID}); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, current, "run.read", authz.Scope{ProjectID: current.RunProjectID}); err != nil {
			return err
		}
		if strings.Contains(r.Pattern, "{sessionId}") {
			var own bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE tenant_id=$1 AND id=$2 AND run_id=$3 AND agent_principal_id=$4)`, tid, strings.Split(strings.Trim(r.URL.Path, "/"), "/")[4], p.RunID, p.ID).Scan(&own); err != nil {
				return err
			}
			if !own {
				return authz.ErrForbidden
			}
		}
		return nil
	})
	return r.WithContext(ctx), p, nil
}

func runCapabilityTarget(r *http.Request, p tenant.Principal) bool {
	// Middleware can run before PathValue is available on the outer server;
	// use the matched declaration plus bounded URL components as elsewhere.
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		return false
	}
	switch parts[1] {
	case "runs", "nodes", "work-orders":
	case "agent-accounts":
		if r.Pattern != "POST /api/agent-accounts/route" {
			return false
		}
	case "projects":
		if len(parts) < 4 || parts[3] != "harness-sessions" {
			return false
		}
		if len(parts) == 4 && r.Method != http.MethodPost {
			return false
		}
	default:
		return false
	}
	if len(parts) >= 3 && parts[1] == "runs" && parts[2] != p.RunID {
		return false
	}
	if len(parts) >= 3 && parts[1] == "work-orders" && parts[2] != p.RunWorkOrderID {
		return false
	}
	if len(parts) >= 3 && parts[1] == "nodes" && parts[2] != "lookup" && parts[2] != p.RunWorkOrderID {
		return false
	}
	if len(parts) >= 3 && parts[1] == "projects" && parts[2] != p.RunProjectID {
		return false
	}
	return true
}

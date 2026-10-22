// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"errors"
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
	if r.Pattern == "POST /api/models/{collection}/{accountId}" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		return len(parts) == 4 && parts[2] == "harness-probes" && validRouteUUID(parts[3])
	}
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
		"GET /api/agent-accounts", "GET /api/agent-accounts/use", "GET /api/agent-accounts/capacity/next", "POST /api/agent-accounts/route",
		"POST /api/agent-accounts/{accountId}/probe", "POST /api/agent-accounts/probes",
		"GET /api/agent-accounts/{accountId}/readings", "POST /api/agent-accounts/{accountId}/readings",
		"GET /api/agent-accounts/{accountId}/quota-series", "GET /api/agent-accounts/{accountId}/statusline",
		"PUT /api/agent-accounts/{accountId}/signals", "POST /api/agent-accounts/{accountId}/quota-key",
		"POST /api/models/harness-probes/claim",
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

// Person session controls are not content reads or launch claims. A bare
// computer credential must meet the pairing ceiling on these routes. An
// upgrade demand would hide "outside this computer's runtime authority" and
// invite a content credential onto a control the computer does not hold.
func transportControlCeilingRoute(r *http.Request) bool {
	switch r.Pattern {
	case "POST /api/projects/{projectId}/harness-sessions/{sessionId}/controls/interrupt",
		"POST /api/projects/{projectId}/harness-sessions/{sessionId}/controls/stop":
		return true
	}
	return false
}

// Attached hooks have no run. Only these exact session operations can use the
// existing worker lease instead; neither registration nor content reads qualify.
func transportAttachedHookRoute(r *http.Request) bool {
	switch r.Pattern {
	case "POST /api/projects/{projectId}/harness-sessions/{sessionId}/heartbeat",
		"POST /api/projects/{projectId}/harness-sessions/{sessionId}/drain",
		"POST /api/projects/{projectId}/harness-sessions/{sessionId}/complete-delivery",
		"GET /api/projects/{projectId}/harness-sessions/{sessionId}/notifications":
		return true
	}
	return false
}

// The tenant/tree/pairing fence covers the final write and every stream recheck.
// The exact S1-03 generation proof excludes person-created registrations that
// merely name the paired principal. No issuer or pairing approver supplies a
// person: RequireTx retains the key's existing live person/creator ceilings.
func attachedHookAuthorityTx(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal) error {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	lease := workerlease.Read(r)
	if !transportAttachedHookRoute(r) || p.TransportComputerID == "" || p.RunID != "" || len(parts) != 6 || !validRouteUUID(parts[2]) || !validRouteUUID(parts[4]) || lease == "" {
		return authz.ErrForbidden
	}
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return err
	}
	if _, err := db.WorkerSessionPrincipalTx(ctx, tx, p, parts[2], parts[4], lease); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return authz.ErrForbidden
		}
		return err
	}
	var attached bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM harness_sessions s
 JOIN agent_pairing_computers c ON c.tenant_id=s.tenant_id AND c.principal_id=s.agent_principal_id
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 JOIN agent_keys k ON k.tenant_id=c.tenant_id AND k.id=c.key_id
 WHERE s.tenant_id=$1 AND s.project_id=$2 AND s.id=$3 AND s.agent_principal_id=$4
 AND s.run_id IS NULL AND s.management='unmanaged' AND 'inbox'=ANY(s.capabilities)
 AND s.archived_at IS NULL AND s.stopped_at IS NULL AND s.handed_over_to_id IS NULL
 AND c.id=$5 AND c.key_id=$6 AND c.state='connected' AND q.state='redeemed'
 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp())
 AND ($7 OR ('attached_reconnect_v1'=ANY(s.capabilities) AND s.process_ownership->>'daemon_id'=c.daemon_id)))`,
		p.TenantID, parts[2], parts[4], p.ID, p.TransportComputerID, p.KeyID,
		r.Pattern == "POST /api/projects/{projectId}/harness-sessions/{sessionId}/heartbeat").Scan(&attached)
	if err != nil {
		return err
	}
	if !attached {
		return authz.ErrForbidden
	}
	return authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: parts[2]})
}

func (m *Module) runContentBoundary(r *http.Request, p tenant.Principal) (*http.Request, tenant.Principal, error) {
	values := r.Header.Values(db.RunCapabilityHeader)
	if len(values) == 0 {
		if p.TransportComputerID != "" && transportAttachedHookRoute(r) && workerlease.Read(r) != "" {
			ctx := tenant.WithPrincipal(r.Context(), p)
			guard := func(ctx context.Context, tx pgx.Tx, tid string) error {
				if tid != p.TenantID {
					return authz.ErrForbidden
				}
				return attachedHookAuthorityTx(ctx, tx, r, p)
			}
			if err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error { return guard(ctx, tx, p.TenantID) }); err != nil {
				return r, p, err
			}
			return r.WithContext(db.WithAdditionalTenantGuard(ctx, guard)), p, nil
		}
		if p.TransportComputerID != "" && !transportMetadataRoute(r) && !transportControlCeilingRoute(r) {
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
		if errors.Is(err, db.ErrRunCapabilityEnrollmentRevoked) {
			return r, p, revokedRunEnrollment()
		}
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
		if errors.Is(err, db.ErrRunCapabilityEnrollmentRevoked) {
			return revokedRunEnrollment()
		}
		if err != nil || current.KeyPersonID != p.KeyPersonID || current.RunComputerID != p.TransportComputerID || current.RunProjectID != p.RunProjectID || current.RunWorkOrderID != p.RunWorkOrderID || current.RunGeneration != p.RunGeneration {
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
		if transportControlCeilingRoute(r) {
			return false
		}
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

func revokedRunEnrollment() error {
	// Run clients historically receive this code in error as well as the
	// status. Keep the cleanup signal identical when a proof is refused before
	// the run handler's pairingRunError adapter can run.
	return &agentpairing.Error{Status: 410, Code: "enrollment_revoked", Message: "enrollment_revoked"}
}

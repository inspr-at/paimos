// SPDX-License-Identifier: AGPL-3.0-only

package approvals

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	eventProposed = "approval.proposed"
	eventApproved = "approval.approved"
	eventDenied   = "approval.denied"
	eventRevoked  = "approval.revoked"
)

// approvalFrom carries the project's ID for the authorization decision. The
// principal name is never serialized until the caller's effective permissions
// have been checked at that scope.
const approvalFrom = `
	SELECT r.id::text, r.agent_principal_id::text, p.name, COALESCE(n.project_id, wn.project_id)::text, r.scope, r.resource_kind,
	       r.resource_id::text, r.run_id::text, r.rationale, r.expires_at, r.proposed_at, r.target, r.target_digest_sha256,
	       d.decision, d.decided_by_principal_id::text
	FROM approval_requests r
	LEFT JOIN principals p
	  ON p.tenant_id = r.tenant_id AND p.id = r.agent_principal_id
	LEFT JOIN nodes n ON n.tenant_id = r.tenant_id AND n.id = r.resource_id AND r.resource_kind = 'node'
	LEFT JOIN agent_runs ar ON ar.tenant_id = r.tenant_id AND ar.id = r.resource_id AND r.resource_kind = 'run'
	LEFT JOIN nodes wn ON wn.tenant_id = ar.tenant_id AND wn.id = ar.work_order_id
	LEFT JOIN approval_decisions d
	  ON d.tenant_id = r.tenant_id AND d.request_id = r.id`

// approvalSnapshot is the event body. revoked_at is audit detail; the HTTP
// Approval object does not include it.
type approvalSnapshot struct {
	Approval
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

func (m *Module) list(ctx context.Context, p tenant.Principal, limit int, pendingOnly ...bool) ([]Approval, error) {
	pending := len(pendingOnly) > 0 && pendingOnly[0]
	if p.Kind != tenant.Person && p.Kind != tenant.Agent {
		return nil, fail(http.StatusForbidden, "forbidden")
	}
	var agentID *string
	if p.Kind == tenant.Agent {
		id := p.ID
		agentID = &id
	}
	var items []Approval
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		items = []Approval{}
		workspaceErr := authz.RequireTx(ctx, tx, p, "approvals.read", authz.Scope{})
		if workspaceErr != nil && !errors.Is(workspaceErr, authz.ErrForbidden) {
			return workspaceErr
		}
		workspaceReader := workspaceErr == nil
		access := map[string]struct{ approval, name bool }{}
		for offset := 0; len(items) < limit; offset += 200 {
			rows, err := tx.Query(ctx, approvalFrom+`
			WHERE ($1::uuid IS NULL OR r.agent_principal_id = $1::uuid)
			  AND ($3::bool OR aeon_visible_all() OR COALESCE(n.project_id, wn.project_id) = ANY (aeon_visible_projects()))
			  AND (NOT $4::bool OR d.request_id IS NULL AND r.expires_at > now())
			ORDER BY r.proposed_at DESC, r.id DESC
			LIMIT 200 OFFSET $2`, agentID, offset, workspaceReader, pending)
			if err != nil {
				return err
			}
			batch := []Approval{}
			for rows.Next() {
				item, err := scanApproval(rows)
				if err != nil {
					rows.Close()
					return err
				}
				batch = append(batch, item)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, item := range batch {
				project := ""
				if item.projectID != nil {
					project = *item.projectID
				}
				policy, ok := access[project]
				if !ok {
					err := approvalVisible(ctx, tx, p, item)
					if err != nil && !errors.Is(err, authz.ErrForbidden) {
						return err
					}
					policy.approval = err == nil
					if policy.approval {
						policy.name, err = canSeeAgentName(ctx, tx, p, item)
						if err != nil {
							return err
						}
					}
					access[project] = policy
				}
				if !policy.approval {
					continue
				}
				if !policy.name {
					item.AgentName = nil
				}
				items = append(items, item)
				if len(items) == limit {
					break
				}
			}
			if len(batch) < 200 {
				break
			}
		}
		return nil
	})
	return items, err
}

func (m *Module) propose(ctx context.Context, p tenant.Principal, authorization string, in proposal) (Approval, error) {
	var out Approval
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		scopes, err := lookupKeyScopes(ctx, tx, authorization, p.ID)
		if err != nil {
			return err
		}
		if !ScopeWithinKey(in.Scope, scopes) {
			return fail(http.StatusForbidden, "scope exceeds the API key")
		}
		if err := verifyResource(ctx, tx, p.ID, in); err != nil {
			return err
		}
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO approval_requests (
				tenant_id, proposed_by_principal_id, agent_principal_id, run_id,
				scope, resource_kind, resource_id, rationale, expires_at, target, target_digest_sha256)
			VALUES ($1::uuid, $2::uuid, $2::uuid, $3::uuid, $4, $5, $6::uuid, $7, $8, $9, $10)
			RETURNING id::text`,
			p.TenantID, p.ID, in.RunID, in.Scope, in.ResourceKind, in.ResourceID, in.Rationale, in.ExpiresAt, in.Target, nullableDigest(in.TargetDigestSHA256),
		).Scan(&id)
		if err != nil {
			return err
		}
		out, err = loadApproval(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := exposeAgentName(ctx, tx, p, &out); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{
			Type:   eventProposed,
			After:  withoutAgentName(out),
			NodeID: nodeRef(out),
		})
		return err
	})
	return out, err
}

func (m *Module) decide(ctx context.Context, p tenant.Principal, id, decision, reason string) (Approval, error) {
	return m.decideVerified(ctx, p, id, decision, reason, nil)
}

// DecideVerified preserves the person-only permission checks, lock, grant and audit
// transaction. The verifier must consume a fresh proof in that same transaction.
func DecideVerified(ctx context.Context, pool *pgxpool.Pool, p tenant.Principal, id, decision, reason string, verify func(pgx.Tx, Approval) error) (Approval, error) {
	if verify == nil {
		return Approval{}, fail(http.StatusForbidden, "fresh verification required")
	}
	if _, err := validateDecision(decisionWrite{Decision: decision, Reason: &reason}); err != nil {
		return Approval{}, err
	}
	return (&Module{pool: pool, inTenant: db.InTenant}).decideVerified(ctx, p, id, decision, reason, verify)
}

func (m *Module) decideVerified(ctx context.Context, p tenant.Principal, id, decision, reason string, verify func(pgx.Tx, Approval) error) (Approval, error) {
	var out Approval
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, p.TenantID); err != nil {
			return err
		}
		before, expired, err := lockRequest(ctx, tx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(http.StatusNotFound, "approval not found")
		}
		if err != nil {
			return err
		}
		if err := CanDecide(ctx, tx, p, before); err != nil {
			return err
		}
		var existing string
		err = tx.QueryRow(ctx, `
			SELECT decision FROM approval_decisions WHERE request_id = $1::uuid`, id).Scan(&existing)
		if err == nil {
			return fail(http.StatusConflict, "approval is already decided")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if expired {
			return fail(http.StatusConflict, "approval has expired")
		}
		if verify != nil {
			if err := verify(tx, before); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO approval_decisions (
				tenant_id, request_id, decided_by_principal_id, decision, reason)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5)`,
			p.TenantID, id, p.ID, decision, reason); err != nil {
			return err
		}
		if decision == "approved" {
			tag, err := tx.Exec(ctx, `
				INSERT INTO agent_permission_grants (
					tenant_id, approval_request_id, agent_principal_id,
					scope, resource_kind, resource_id, valid_until)
				SELECT r.tenant_id, r.id, r.agent_principal_id,
				       r.scope, r.resource_kind, r.resource_id, r.expires_at
				FROM approval_requests r
				JOIN approval_decisions d
				  ON d.tenant_id = r.tenant_id AND d.request_id = r.id
				WHERE r.id = $1::uuid AND d.decision = 'approved'`, id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errors.New("approved grant was not inserted")
			}
		}
		out, err = loadApproval(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := exposeAgentName(ctx, tx, p, &out); err != nil {
			return err
		}
		eventType := eventDenied
		if decision == "approved" {
			eventType = eventApproved
		}
		_, err = events.Append(ctx, tx, p, events.Change{
			Type:   eventType,
			Before: withoutAgentName(before),
			After:  withoutAgentName(out),
			NodeID: nodeRef(out),
		})
		return err
	})
	return out, err
}

func (m *Module) revoke(ctx context.Context, p tenant.Principal, id string) (Approval, error) {
	var out Approval
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		if _, _, err = lockRequest(ctx, tx, id); errors.Is(err, pgx.ErrNoRows) {
			return fail(http.StatusNotFound, "approval not found")
		} else if err != nil {
			return err
		}
		if err := requirePerson(ctx, tx, p, "only a person may revoke a grant"); err != nil {
			return err
		}
		var revokedAt *time.Time
		err = tx.QueryRow(ctx, `
			SELECT revoked_at FROM agent_permission_grants
			WHERE approval_request_id = $1::uuid
			FOR UPDATE`, id).Scan(&revokedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(http.StatusConflict, "only an approved grant can be revoked")
		}
		if err != nil {
			return err
		}
		out, err = loadApproval(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := exposeAgentName(ctx, tx, p, &out); err != nil {
			return err
		}
		if revokedAt != nil {
			return nil
		}
		var at time.Time
		err = tx.QueryRow(ctx, `
			UPDATE agent_permission_grants
			SET revoked_at = now()
			WHERE approval_request_id = $1::uuid AND revoked_at IS NULL
			RETURNING revoked_at`, id).Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{
			Type:   eventRevoked,
			Before: withoutAgentName(out),
			After:  approvalSnapshot{Approval: withoutAgentName(out), RevokedAt: &at},
			NodeID: nodeRef(out),
		})
		return err
	})
	return out, err
}

func requirePerson(ctx context.Context, tx pgx.Tx, p tenant.Principal, msg string) error {
	if p.Kind != tenant.Person {
		return fail(http.StatusForbidden, msg)
	}
	var kind string
	err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id = $1::uuid`, p.ID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind != string(tenant.Person)) {
		return fail(http.StatusForbidden, msg)
	}
	return err
}

func approvalPermission(scope string) string {
	if scope == "release.deploy" || strings.HasPrefix(scope, "release.deploy.") {
		return "releases.deploy"
	}
	if strings.HasPrefix(scope, "journey.") {
		return "journey.act"
	}
	for scope != "" {
		if _, ok := authz.Lookup(scope); ok {
			return scope
		}
		last := strings.LastIndexByte(scope, '.')
		if last < 0 {
			break
		}
		scope = scope[:last]
	}
	return ""
}

// A project-scoped approval is readable only with approvals.read at that
// project. Tenant and unassigned resources require workspace access.
func approvalVisible(ctx context.Context, tx pgx.Tx, p tenant.Principal, a Approval) error {
	scope := authz.Scope{}
	if a.projectID != nil {
		scope.ProjectID = *a.projectID
	}
	return authz.RequireTx(ctx, tx, p, "approvals.read", scope)
}

func exposeAgentName(ctx context.Context, tx pgx.Tx, p tenant.Principal, a *Approval) error {
	if a.AgentName == nil {
		return nil
	}
	visible, err := canSeeAgentName(ctx, tx, p, *a)
	if err != nil {
		return err
	}
	if !visible {
		a.AgentName = nil
	}
	return nil
}

func canSeeAgentName(ctx context.Context, tx pgx.Tx, p tenant.Principal, a Approval) (bool, error) {
	scope := authz.Scope{}
	if a.projectID != nil {
		scope.ProjectID = *a.projectID
	}
	for _, permission := range []string{"members.read", "harness.read"} {
		err := authz.RequireTx(ctx, tx, p, permission, scope)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, authz.ErrForbidden) {
			return false, err
		}
	}
	return false, nil
}

func withoutAgentName(a Approval) Approval {
	a.AgentName = nil
	return a
}

func verifyResource(ctx context.Context, tx pgx.Tx, agentID string, in proposal) error {
	switch in.ResourceKind {
	case "node":
		if err := liveNode(ctx, tx, *in.ResourceID); err != nil {
			return err
		}
	case "run":
		if err := ownRun(ctx, tx, agentID, *in.ResourceID); err != nil {
			return err
		}
	}
	if in.RunID != nil && in.ResourceKind != "run" {
		return ownRun(ctx, tx, agentID, *in.RunID)
	}
	return nil
}

func liveNode(ctx context.Context, tx pgx.Tx, id string) error {
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT true FROM nodes WHERE id = $1::uuid AND deleted_at IS NULL`, id).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(http.StatusForbidden, "resource is not in this tenant")
	}
	return err
}

func ownRun(ctx context.Context, tx pgx.Tx, agentID, runID string) error {
	var owner string
	err := tx.QueryRow(ctx, `
		SELECT agent_principal_id::text FROM agent_runs WHERE id = $1::uuid`, runID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(http.StatusForbidden, "resource is not in this tenant")
	}
	if err != nil {
		return err
	}
	if owner != agentID {
		return fail(http.StatusForbidden, "run belongs to another agent")
	}
	return nil
}

func lockRequest(ctx context.Context, tx pgx.Tx, id string) (Approval, bool, error) {
	var a Approval
	var resourceID, runID, targetDigest *string
	var expired bool
	err := tx.QueryRow(ctx, `
		SELECT r.id::text, r.agent_principal_id::text, p.name, COALESCE(n.project_id, wn.project_id)::text, r.scope, r.resource_kind,
		       r.resource_id::text, r.run_id::text, r.rationale, r.expires_at, r.proposed_at, r.target, r.target_digest_sha256,
		       r.expires_at <= now()
		FROM approval_requests r
		LEFT JOIN principals p
		  ON p.tenant_id = r.tenant_id AND p.id = r.agent_principal_id
		LEFT JOIN nodes n ON n.tenant_id = r.tenant_id AND n.id = r.resource_id AND r.resource_kind = 'node'
		LEFT JOIN agent_runs ar ON ar.tenant_id = r.tenant_id AND ar.id = r.resource_id AND r.resource_kind = 'run'
		LEFT JOIN nodes wn ON wn.tenant_id = ar.tenant_id AND wn.id = ar.work_order_id
		WHERE r.id = $1::uuid
		FOR UPDATE OF r`, id).Scan(
		&a.ID, &a.AgentPrincipalID, &a.AgentName, &a.projectID, &a.Scope, &a.ResourceKind,
		&resourceID, &runID, &a.Rationale, &a.ExpiresAt, &a.ProposedAt, &a.Target, &targetDigest, &expired)
	a.ResourceID = resourceID
	a.RunID = runID
	if targetDigest != nil {
		a.TargetDigestSHA256 = *targetDigest
	}
	a.Risk = Risk(a.Scope, a.ResourceKind)
	return a, expired, err
}

func loadApproval(ctx context.Context, tx pgx.Tx, id string) (Approval, error) {
	return scanApproval(tx.QueryRow(ctx, approvalFrom+` WHERE r.id = $1::uuid`, id))
}

func scanApproval(row pgx.Row) (Approval, error) {
	var a Approval
	var resourceID, runID, decision, decidedBy, targetDigest *string
	err := row.Scan(
		&a.ID, &a.AgentPrincipalID, &a.AgentName, &a.projectID, &a.Scope, &a.ResourceKind,
		&resourceID, &runID, &a.Rationale, &a.ExpiresAt, &a.ProposedAt, &a.Target, &targetDigest,
		&decision, &decidedBy)
	a.ResourceID = resourceID
	a.RunID = runID
	if targetDigest != nil {
		a.TargetDigestSHA256 = *targetDigest
	}
	a.Risk = Risk(a.Scope, a.ResourceKind)
	a.Decision = decision
	a.DecidedByPrincipalID = decidedBy
	return a, err
}

func nullableDigest(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nodeRef(a Approval) *string {
	if a.ResourceKind != "node" || a.ResourceID == nil {
		return nil
	}
	id := *a.ResourceID
	return &id
}

// Review returns one approval under its existing project/workspace read policy.
func Review(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) (Approval, error) {
	a, err := loadApproval(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, fail(404, "approval not found")
	}
	if err != nil {
		return a, err
	}
	if err = approvalVisible(ctx, tx, p, a); err != nil {
		return Approval{}, fail(403, "approval unavailable")
	}
	if err = exposeAgentName(ctx, tx, p, &a); err != nil {
		return Approval{}, err
	}
	return a, nil
}

// CanDecide applies the same live actor and permission policy used by decision writes.
func CanDecide(ctx context.Context, tx pgx.Tx, p tenant.Principal, a Approval) error {
	if authz.OwnerWorkstation(p) {
		if a.AgentPrincipalID == p.ID {
			return fail(http.StatusForbidden, "agents cannot decide their own requests")
		}
	} else {
		if err := requirePerson(ctx, tx, p, "only a person may decide a live approval"); err != nil {
			return err
		}
	}
	if err := authz.RequireTx(ctx, tx, p, "approvals.decide", authz.Scope{}); err != nil {
		return fail(http.StatusForbidden, "approval decision requires an authorized person")
	}
	if a.Risk == "high" {
		if err := authz.RequireTx(ctx, tx, p, "approvals.decide_high", authz.Scope{}); err != nil {
			return fail(http.StatusForbidden, "high-risk approval requires workspace administration")
		}
	}
	permission := approvalPermission(a.Scope)
	if permission == "" || authz.RequireTx(ctx, tx, p, permission, authz.Scope{}) != nil {
		return fail(http.StatusForbidden, "approval requires the permission being granted")
	}

	return nil
}

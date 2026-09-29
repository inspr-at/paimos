// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Why a person may not preview one named agent's session file. The allow
// decision stays the rules package's active-key check; these codes only
// explain a denial. creator_name is never part of that denial.
const (
	PreviewNotKeyCreator = "not_key_creator"
	PreviewKeyRevoked    = "key_revoked"
	PreviewKeyExpired    = "key_expired"
	PreviewAgentInactive = "agent_inactive"
)

// AgentPreview tells one viewer whether they may preview a named agent.
// CreatorName is set only for a workspace owner or admin, and only when the
// denial is that someone else holds a current key.
type AgentPreview struct {
	Allowed     bool   `json:"allowed"`
	Reason      string `json:"reason,omitempty"`
	CreatorName string `json:"creator_name,omitempty"`
}

// PreviewDenialMessage is the plain sentence for a named-agent denial code.
// It never names another person; that name belongs only on the member list.
func PreviewDenialMessage(reason string) string {
	switch reason {
	case PreviewNotKeyCreator:
		return "You didn't create a key for this agent."
	case PreviewKeyRevoked:
		return "Your key for this agent was revoked."
	case PreviewKeyExpired:
		return "Your key for this agent has expired."
	case PreviewAgentInactive:
		return "This agent is deactivated."
	default:
		return "permission or scoped ownership denied"
	}
}

func previewFromFlags(status string, valid, expired, anyKey bool) (bool, string) {
	if status == "active" && valid {
		return true, ""
	}
	if status != "active" {
		return false, PreviewAgentInactive
	}
	if expired {
		return false, PreviewKeyExpired
	}
	if anyKey {
		return false, PreviewKeyRevoked
	}
	return false, PreviewNotKeyCreator
}

// ClassifyAgentControl reports whether viewerID (the canonical person) controls
// agentID, and a reason code when they do not. A missing or non-agent principal
// returns ok=false and an empty reason so the caller keeps its generic denial.
func ClassifyAgentControl(ctx context.Context, tx pgx.Tx, tenantID, viewerID, agentID string) (bool, string, error) {
	found, err := agentPreviews(ctx, tx, tenantID, viewerID, agentID, false)
	if err != nil {
		return false, "", err
	}
	item, ok := found[agentID]
	if !ok {
		return false, "", nil
	}
	return item.Allowed, item.Reason, nil
}

func canonicalPerson(ctx context.Context, tx pgx.Tx, tenantID, id string) (string, error) {
	var owner string
	err := tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE tenant_id=$1 AND id=$2 AND kind='person' AND status='active'`, tenantID, id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return owner, err
}

func workspaceOwnerOrAdmin(ctx context.Context, tx pgx.Tx, tenantID, principalID string) (bool, error) {
	var admin bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM role_bindings b
		JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
		WHERE b.tenant_id=$1::uuid AND b.principal_id=$2::uuid AND b.scope_type='workspace' AND r.key IN ('owner','admin')
	)`, tenantID, principalID).Scan(&admin)
	return admin, err
}

// agentPreviews classifies every named agent, or only one when onlyID is set.
// revealCreator adds the earliest current key holder's name.
func agentPreviews(ctx context.Context, tx pgx.Tx, tenantID, viewerID, onlyID string, revealCreator bool) (map[string]AgentPreview, error) {
	var viewer, only any
	if viewerID != "" {
		viewer = viewerID
	}
	if onlyID != "" {
		only = onlyID
	}
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, a.status,
		  COALESCE(bool_or(k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > clock_timestamp())) FILTER (WHERE k.created_by_principal_id = $2::uuid), false),
		  COALESCE(bool_or(k.revoked_at IS NULL AND k.expires_at IS NOT NULL AND k.expires_at <= clock_timestamp()) FILTER (WHERE k.created_by_principal_id = $2::uuid), false),
		  COALESCE(bool_or(true) FILTER (WHERE k.created_by_principal_id = $2::uuid), false),
		  (ARRAY_AGG(c.name ORDER BY k.created_at, c.id) FILTER (
		    WHERE k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > clock_timestamp()) AND c.kind = 'person'
		  ))[1]
		FROM principals a
		LEFT JOIN agent_keys k ON k.tenant_id = a.tenant_id AND k.principal_id = a.id
		LEFT JOIN principals c ON c.tenant_id = k.tenant_id AND c.id = k.created_by_principal_id
		WHERE a.tenant_id = $1::uuid AND a.kind = 'agent' AND ($3::uuid IS NULL OR a.id = $3::uuid)
		GROUP BY a.id, a.status`, tenantID, viewer, only)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AgentPreview{}
	for rows.Next() {
		var id, status string
		var valid, expired, anyKey bool
		var creator *string
		if err := rows.Scan(&id, &status, &valid, &expired, &anyKey, &creator); err != nil {
			return nil, err
		}
		allowed, reason := previewFromFlags(status, valid, expired, anyKey)
		item := AgentPreview{Allowed: allowed, Reason: reason}
		if revealCreator && !allowed && reason == PreviewNotKeyCreator && creator != nil && *creator != "" {
			item.CreatorName = *creator
		}
		out[id] = item
	}
	return out, rows.Err()
}

// fillAgentPreviews sets Preview on each agent for the signed-in person.
// A caller who is not an active person sees every agent as not theirs, with no names.
func fillAgentPreviews(ctx context.Context, tx pgx.Tx, p tenant.Principal, agents []AgentMember) error {
	viewer, err := canonicalPerson(ctx, tx, p.TenantID, p.ID)
	if err != nil {
		return err
	}
	reveal := false
	if viewer != "" {
		reveal, err = workspaceOwnerOrAdmin(ctx, tx, p.TenantID, viewer)
		if err != nil {
			return err
		}
	}
	found, err := agentPreviews(ctx, tx, p.TenantID, viewer, "", reveal)
	if err != nil {
		return err
	}
	for i := range agents {
		item, ok := found[agents[i].PrincipalID]
		if !ok {
			item = AgentPreview{Reason: PreviewNotKeyCreator}
		}
		agents[i].Preview = item
	}
	return nil
}

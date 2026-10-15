// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"strings"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func MessagingAssistantScopes() []string {
	return []string{"inbox.read", "inbox.send", "inbox.receipt"}
}

func MessagingAssistantOperation(pattern string) bool {
	switch pattern {
	case "POST /api/agents/assistants", "POST /api/agents/assistants/{principalId}/rotate", "DELETE /api/agents/assistants/{principalId}":
		return true
	}
	return false
}

// MessagingAssistantInboxRoute is an exact matched-pattern allowlist. Shared
// inbox scopes on project messaging, native sessions or chat are insufficient.
func MessagingAssistantInboxRoute(pattern string) bool {
	switch pattern {
	case "GET /api/inbox/messages", "POST /api/inbox/messages", "POST /api/inbox/messages/{messageId}/ack",
		"GET /api/inbox/messages/{messageId}/receipt", "GET /api/inbox/message-status", "GET /api/inbox/stream":
		return true
	}
	return false
}

// RequireMessagingAssistantTx returns the canonical person owner. Callers hold
// the tenant access-change fence in the final write transaction. This predicate
// deliberately does not grant agents.create or keys.manage to ordinary agents.
func RequireMessagingAssistantTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) (string, error) {
	var marked bool
	var owner string
	if err := tx.QueryRow(ctx, `SELECT coalesce(messaging_only,false),coalesce(linked_to,id)::text
	 FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid AND status='active' AND kind=$3`, p.TenantID, p.ID, string(p.Kind)).Scan(&marked, &owner); err != nil {
		if err == pgx.ErrNoRows {
			return "", ErrForbidden
		}
		return "", err
	}
	if marked || p.MessagingOnly {
		return "", ErrForbidden
	}
	if p.Kind == tenant.Agent {
		if p.KeyCreatorID == "" || p.KeyID == "" || !p.FullAccess && !IsCoordinatorKey(p.Scopes) {
			return "", ErrForbidden
		}
		g, err := readGrants(ctx, tx, p)
		if err != nil {
			return "", err
		}
		if g.workspaceRole == nil {
			return "", ErrForbidden
		}
		for _, permission := range CoordinatorBaseScopes {
			if !g.allows(permission, "") {
				return "", ErrForbidden
			}
			if err := RequireTx(ctx, tx, p, permission, Scope{}); err != nil {
				return "", err
			}
		}
		owner = p.KeyCreatorID
	} else if p.Kind != tenant.Person {
		return "", ErrForbidden
	}
	person := tenant.Principal{ID: owner, TenantID: p.TenantID, Kind: tenant.Person}
	for _, permission := range []string{"agents.create", "keys.manage"} {
		if err := RequireTx(ctx, tx, person, permission, Scope{}); err != nil {
			return "", err
		}
	}
	return strings.ToLower(owner), nil
}

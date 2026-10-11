// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"
	"errors"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// RoutineAdministratorNoticeTx is an internal system-evidence path, not an
// outward notify action. Caller holds tenant/tree and durable intent locks.
// The existing inbox receipt/wake protocol owns delivery after acceptance.
// Missing current administrator returns pending, never fabricated success.
func RoutineAdministratorNoticeTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, project, scope, key, body string) (bool, error) {
	var system bool
	if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')=$2 AND EXISTS(SELECT 1 FROM principals WHERE id=$1 AND kind='agent' AND name='System' AND roles @> ARRAY['system'] AND status='active')`, actor.ID, actor.TenantID).Scan(&system); err != nil {
		return false, err
	}
	if !system || actor.TenantID == "" || scope != "workspace" && scope != "project" && scope != "personal" || !strings.HasPrefix(key, "routine-pause:") && !strings.HasPrefix(key, "routine-recovery:") {
		return false, authz.ErrForbidden
	}
	var previous string
	err := tx.QueryRow(ctx, `SELECT body FROM inbox_messages WHERE sender_principal_id=$1 AND idempotency_key=$2`, actor.ID, key).Scan(&previous)
	if err == nil {
		if previous != body {
			return false, errConflict
		}
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	// Scope and live authority are both rechecked; a revoked owner/admin cannot
	// receive project details merely because an earlier scan selected them.
	rows, err := tx.Query(ctx, `SELECT DISTINCT p.id::text FROM principals p JOIN role_bindings b ON b.principal_id=p.id JOIN roles r ON r.id=b.role_id
  WHERE p.kind='person' AND p.status='active' AND p.linked_to IS NULL AND r.key IN ('owner','admin')
  AND (b.scope_type='workspace' OR $2<>'workspace' AND b.scope_type='project' AND b.scope_id=$1::uuid) ORDER BY p.id::text LIMIT 33`, project, scope)
	if err != nil {
		return false, err
	}
	var candidates []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		candidates = append(candidates, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	// Bound candidates before effective-role checks, including ineligible rows.
	if len(candidates) > 32 {
		candidates = candidates[:32]
	}
	recipient := ""
	for _, id := range candidates {
		p := tenant.Principal{ID: id, TenantID: actor.TenantID, Kind: tenant.Person}
		allowed := true
		for _, permission := range []string{"recurrences.manage", "nodes.read", "inbox.read"} {
			err = authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project})
			if errors.Is(err, authz.ErrForbidden) {
				allowed = false
				break
			}
			if err != nil {
				return false, err
			}
		}
		if allowed {
			recipient = id
			break
		}
	}
	if recipient == "" {
		return false, nil
	}
	// Every existing FK parent precedes the first event in AcceptMessageTx.
	if _, err = tx.Exec(ctx, `SELECT id FROM nodes WHERE id=$1 FOR KEY SHARE`, project); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM principals WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, []string{actor.ID, recipient}); err != nil {
		return false, err
	}
	_, err = AcceptMessageTx(ctx, tx, actor, Acceptance{RecipientPrincipalID: recipient, SenderLabel: "Routines", ProjectID: &project, IdempotencyKey: key, Body: body})
	return err == nil, err
}

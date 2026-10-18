// SPDX-License-Identifier: AGPL-3.0-only
package approvals

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// CanNotify reuses the native decision authority without granting or deciding
// anything. Readable approval pointers alone do not qualify a push recipient.
func CanNotify(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) (bool, error) {
	if p.Kind != tenant.Person || p.KeyCreatorID != "" {
		return false, nil
	}
	a, err := loadApproval(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = approvalVisible(ctx, tx, p, a); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return false, nil
		}
		return false, err
	}
	permission := approvalPermission(a.Scope)
	if permission == "" {
		return false, nil
	}
	permissions := []string{"approvals.decide", permission}
	if a.Risk == "high" {
		permissions = append(permissions, "approvals.decide_high")
	}
	for _, permission := range permissions {
		if err = authz.RequireTx(ctx, tx, p, permission, authz.Scope{}); err != nil {
			if errors.Is(err, authz.ErrForbidden) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

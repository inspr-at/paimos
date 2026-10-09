// SPDX-License-Identifier: AGPL-3.0-only
package stepup

import (
	"context"
	"encoding/json"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/phoneapprovals"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Push carries only a pointer. Read under RLS and the same live target authority
// as the desk. Scanning never settles expiry or acquires the event counter.
func (m *Module) phoneReviewTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) (phoneapprovals.Review, error) {
	if p.Kind != tenant.Person || p.KeyCreatorID != "" {
		return phoneapprovals.Review{}, authz.ErrForbidden
	}
	r, err := read(ctx, tx, id, false)
	if err != nil {
		return phoneapprovals.Review{}, err
	}
	if err = allowed(ctx, tx, p, r); err != nil {
		return phoneapprovals.Review{}, err
	}
	if err = decorate(ctx, tx, &r); err != nil {
		return phoneapprovals.Review{}, err
	}
	raw, err := json.Marshal(r)
	return phoneapprovals.Review{Kind: "stepup", ID: r.ID, Hash: r.Digest, Pending: r.State == "pending" && r.ExpiresAt.After(m.now()), StepUp: raw}, err
}

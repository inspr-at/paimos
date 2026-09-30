// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// quotaAccounts is tenant-scoped by RLS. Only a person's explicit confirmation
// joins enrollments; a matching self-reported fingerprint grants no authority.
const quotaAccounts = `SELECT sibling.id FROM agent_accounts own
 JOIN agent_accounts sibling ON sibling.tenant_id=own.tenant_id AND
 (sibling.id=own.id OR (own.quota_pool_fingerprint<>'' AND sibling.quota_pool_fingerprint=own.quota_pool_fingerprint AND sibling.harness=own.harness))
 WHERE own.id=$1`

type quotaPoolWrite struct {
	AccountIDs       []string `json:"account_ids"`
	QuotaFingerprint string   `json:"quota_fingerprint"`
	Confirmed        *bool    `json:"confirmed"`
}

func (m *Module) quotaPool(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.manage"); err != nil {
		writeErr(w, err)
		return
	}
	var in quotaPoolWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		return confirmQuotaPool(r.Context(), tx, p, in)
	}); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func confirmQuotaPool(ctx context.Context, tx pgx.Tx, p tenant.Principal, in quotaPoolWrite) error {
	if p.Kind != tenant.Person || authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}) != nil {
		return fail(http.StatusForbidden, "person account.manage required")
	}
	ids, err := idList(&in.AccountIDs)
	if err != nil {
		return err
	}
	if in.Confirmed == nil || len(ids) == 0 || len(ids) > 256 || len(ids) != len(in.AccountIDs) ||
		!fingerprintRE.MatchString(in.QuotaFingerprint) || *in.Confirmed && len(ids) < 2 {
		return fail(http.StatusBadRequest, "select accounts and confirm the same login")
	}
	// The module holds the pairing lock before account rows. Signals and
	// reservations use that lock too, so the reviewed identities cannot race.
	harness := ""
	for _, id := range ids {
		a, err := lockAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		if a.QuotaFingerprint != in.QuotaFingerprint || harness != "" && harness != a.Harness {
			return fail(http.StatusConflict, "accounts no longer match the selected login")
		}
		harness = a.Harness
	}
	fingerprint := ""
	if *in.Confirmed {
		fingerprint = in.QuotaFingerprint
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET quota_pool_fingerprint=$2 WHERE id::text=ANY($1::text[])`, ids, fingerprint); err != nil {
		return err
	}
	return writeEvent(ctx, tx, p, "account.quota_pool_confirmed", nil, in)
}

// sharedQuotaWindows projects one reservation ledger across every door. Mutating
// callers hold agentpairing.Lock before reading this view through reservation or
// settlement, so a sibling cannot concurrently spend the same remaining quota.
// Reservations keep their original window IDs for replay, release and settlement.
func sharedQuotaWindows(ctx context.Context, tx pgx.Tx, a Account, own []Window, now time.Time) ([]Window, error) {
	if a.QuotaPoolFingerprint == "" {
		return own, nil
	}
	rows, err := tx.Query(ctx, quotaAccounts, a.ID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	all, err := readAccountWindows(ctx, tx, ids, false)
	if err != nil {
		return nil, err
	}
	var history, out []Window
	latest := map[string]Window{}
	for _, id := range ids {
		for _, w := range all[id] {
			if w.pairingVerification {
				continue
			}
			history = append(history, w)
			// Manual limits remain independent bounds, shared by all doors.
			// Synthetic grants stay in history to prevent repeated refreshes.
			if w.capacityReadAt == nil || synthetic(w) {
				out = append(out, w)
				continue
			}
			key := w.capacityKind + "/" + w.capacityBucket
			prev, exists := latest[key]
			freshMeasured := func(v Window) bool {
				return v.capacitySource != "estimate" && now.Sub(*v.capacityReadAt) <= 10*time.Minute && v.EndsAt.After(now)
			}
			prefer := !exists
			if exists {
				prefer = freshMeasured(w) && !freshMeasured(prev) || freshMeasured(w) == freshMeasured(prev) &&
					(w.capacityReadAt.After(*prev.capacityReadAt) || w.capacityReadAt.Equal(*prev.capacityReadAt) &&
						(w.capacitySource == "harness" && prev.capacitySource != "harness" || w.capacitySource == prev.capacitySource && w.ID < prev.ID))
			}
			if prefer {
				latest[key] = w
			}
		}
	}
	for _, w := range latest {
		// A new observation/door must not strand outstanding holds on an old
		// ledger row, including one retired when the vendor replaced a window.
		w.Reserved = 0
		for _, held := range history {
			if held.capacityKind == w.capacityKind && held.capacityBucket == w.capacityBucket &&
				held.StartsAt.Before(w.EndsAt) && w.StartsAt.Before(held.EndsAt) {
				w.Reserved += held.Reserved
			}
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

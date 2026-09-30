// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"net/http"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// An attach request that never became a session stays visible to its owner this
// long, so /agents can say it expired or was cancelled instead of silently
// dropping it. Active watches are sessions and are listed there.
const attachRecentWindow = "15 minutes"

// attachPending lists the signed-in owner's attach requests that have not become
// a session yet: waiting for approval, approved and connecting, and recently
// expired or cancelled. It grants nothing the nine-digit lookup does not: the
// same owner filter, the same immutable snapshot and digests, and approval still
// needs the same-origin POST with both digests. It never returns the code or the
// Touch ID challenge, and it never changes a row (expiry is reported, not applied;
// the daemon's next poll or the owner's next decision applies it). The page polls
// this, so it reads in the tenant transaction without the pairing lock.
func (m *Module) attachPending(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	out := []attachwatch.View{}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT id::text,digest,snapshot,state,expires_at,consent_mode,expires_at<=clock_timestamp()
 FROM harness_attach_requests
 WHERE owner_id=$1 AND session_id IS NULL AND state IN ('pending','approved','detached','unreachable')
 AND created_at>clock_timestamp()-interval '`+attachRecentWindow+`'
 ORDER BY created_at DESC, id LIMIT 8`, p.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v attachwatch.View
			var expired bool
			if err = rows.Scan(&v.RequestID, &v.Digest, &v.Snapshot, &v.State, &v.ExpiresAt, &v.ConsentMode, &expired); err != nil {
				return err
			}
			if expired && (v.State == "pending" || v.State == "approved") {
				// The state attachExpired would record for a request nobody polled in time.
				v.State = "unreachable"
			}
			out = append(out, v)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		rows.Close()
		for i := range out {
			// Same rule as loadAttach: a waiting request shows the policy that
			// approval would pin now; every later state keeps the stored one.
			if out[i].State == "pending" {
				if out[i].ConsentMode, err = computerWatchConsentMode(r.Context(), tx, p.ID, out[i].Snapshot.Platform, out[i].Snapshot.ComputerID); err != nil {
					return err
				}
			}
			out[i].ConsentDigest = attachwatch.ConsentDigest(out[i].RequestID, out[i].Digest, out[i].ConsentMode)
		}
		return nil
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, map[string]any{"requests": out})
}

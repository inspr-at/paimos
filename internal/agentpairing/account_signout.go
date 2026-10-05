// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"net/http"
	"sort"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type signoutTarget struct {
	ComputerID string `json:"computer_id"`
	AccountID  string `json:"account_id"`
	Revision   int64  `json:"expected_revision"`
}

// A canonical quota identity may have several computer-bound account records.
// The complete reviewed set is fenced and revoked atomically, with one event
// last. Labels and email addresses never establish account identity.
func (m *Module) signOutEverywhere(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var in struct {
		Targets []signoutTarget `json:"targets"`
	}
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	if len(in.Targets) < 1 || len(in.Targets) > 100 {
		WriteError(w, fail(400, "invalid_request", "1–100 reviewed sign-ins required"))
		return
	}
	expected := map[string]signoutTarget{}
	for _, t := range in.Targets {
		if !uuidRE.MatchString(t.ComputerID) || !uuidRE.MatchString(t.AccountID) || t.Revision < 1 {
			WriteError(w, fail(400, "invalid_request", "valid reviewed sign-ins required"))
			return
		}
		if _, dup := expected[t.AccountID]; dup {
			WriteError(w, fail(400, "invalid_request", "duplicate sign-in"))
			return
		}
		expected[t.AccountID] = t
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(r.Context(), tx, p, "account.manage", authz.Scope{}) != nil {
			return fail(403, "forbidden", "account management required")
		}
		rows, err := tx.Query(r.Context(), `SELECT e.computer_id::text,e.account_id::text,c.revision,coalesce(a.owner_person_id,q.approved_by)::text
   FROM agent_pairing_enrollments e JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id
   JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
   JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
   JOIN agent_accounts seed ON seed.id=$1 AND seed.tenant_id=a.tenant_id
   WHERE e.state<>'revoked' AND a.archived_at IS NULL AND c.archived_at IS NULL
   AND a.harness=seed.harness AND (a.id=seed.id OR (seed.quota_pool_fingerprint<>'' AND a.quota_pool_fingerprint=seed.quota_pool_fingerprint))
   ORDER BY e.computer_id,e.account_id LIMIT 101`, in.Targets[0].AccountID)
		if err != nil {
			return err
		}
		var actual []signoutTarget
		for rows.Next() {
			var t signoutTarget
			var owner string
			if err = rows.Scan(&t.ComputerID, &t.AccountID, &t.Revision, &owner); err != nil {
				rows.Close()
				return err
			}
			if owner != p.ID {
				rows.Close()
				return fail(403, "forbidden", "only the owner may sign out this account everywhere")
			}
			actual = append(actual, t)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(actual) != len(expected) {
			return fail(409, "conflict", "sign-ins changed; review the complete account again")
		}
		for _, t := range actual {
			if expected[t.AccountID] != t {
				return fail(409, "conflict", "sign-ins changed; review the account again")
			}
		}
		sort.Slice(actual, func(i, j int) bool {
			if actual[i].ComputerID != actual[j].ComputerID {
				return actual[i].ComputerID < actual[j].ComputerID
			}
			return actual[i].AccountID < actual[j].AccountID
		})
		// Revisions were checked as one set before any mutation; later sibling
		// sign-ins on the same computer use the current serialized revision.
		for _, t := range actual {
			if err = disconnectTx(r.Context(), tx, p, t.ComputerID, disconnectInput{Mode: "revoke_now", AccountID: t.AccountID, SuppressAudit: true}); err != nil {
				return err
			}
		}
		return audit(r.Context(), tx, p, "agent_pairing.signed_out_everywhere", map[string]any{"targets": actual, "local_cleanup": "pending"})
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, map[string]any{"signed_out": len(in.Targets), "local_cleanup": "pending"})
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type verificationRetry struct {
	AccountID   string    `json:"account_id"`
	RunID       string    `json:"run_id"`
	ExpiresAt   time.Time `json:"expires_at"`
	WorkOrderID string    `json:"-"`
}

func (v verificationRetry) audit() map[string]any {
	return map[string]any{"account_id": v.AccountID, "run_id": v.RunID, "work_order_id": v.WorkOrderID, "expires_at": v.ExpiresAt, "allowance": 1, "unit": "requests", "max_duration_seconds": VerificationSeconds}
}

func (m *Module) verifyAgain(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var in struct {
		Revision *int64          `json:"expected_revision"`
		RunID    json.RawMessage `json:"expected_verification_run_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	// Null explicitly reviews a connect-only enrollment; omission does not.
	var expectedRunID *string
	if len(in.RunID) == 0 || json.Unmarshal(in.RunID, &expectedRunID) != nil {
		WriteError(w, fail(400, "invalid_request", "reviewed verification run required; use null when no prior run exists"))
		return
	}
	computer, account := r.PathValue("computerId"), r.PathValue("accountId")
	if !uuidRE.MatchString(computer) || !uuidRE.MatchString(account) || in.Revision == nil || *in.Revision < 1 || expectedRunID != nil && !uuidRE.MatchString(*expectedRunID) {
		WriteError(w, fail(400, "invalid_request", "reviewed computer revision and verification run required"))
		return
	}
	var out verificationRetry
	// Verification work orders are internal account-management work. Both the
	// old run transition and new node creation need service project visibility,
	// independent of the owner's nodes.read grants. Tenant RLS remains enforced;
	// RequireTx and the current ownership check precede every mutation below.
	// This transaction returns only the bound verification, never project data.
	err := m.in(db.AllProjects(ctx, "owner-authorized account verification"), p.TenantID, func(tx pgx.Tx) error {
		if p.Kind != tenant.Person || authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}) != nil {
			return fail(403, "forbidden", "person account management required")
		}
		var owner, principal, request, platform, arch string
		var revision int64
		var a Choice
		var run *string
		var claimed bool
		var expires time.Time
		err := tx.QueryRow(ctx, `SELECT coalesce(a.owner_person_id,q.approved_by)::text,c.principal_id::text,e.request_id::text,c.revision,
		 q.details->>'platform',q.details->>'arch',a.harness,a.account_key,a.label,e.model_profile_id::text,
		 e.verification_run_id::text,e.verification_claimed_at IS NOT NULL,e.verification_expires_at
		 FROM agent_pairing_enrollments e JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id
		 JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
		 JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id
		 WHERE e.account_id=$1 AND c.id=$2 AND e.state='connected' AND c.state='connected' AND q.state='redeemed'
		 AND c.archived_at IS NULL AND a.archived_at IS NULL FOR UPDATE OF c,e,a`, account, computer).
			Scan(&owner, &principal, &request, &revision, &platform, &arch, &a.Harness, &a.AccountKey, &a.Label, &a.ProfileID, &run, &claimed, &expires)
		if err != nil {
			return notFound(err)
		}
		if owner != p.ID {
			return fail(403, "forbidden", "account owner required")
		}
		if revision != *in.Revision || (run == nil) != (expectedRunID == nil) || run != nil && *run != *expectedRunID {
			return fail(409, "conflict", "account verification changed; refresh before verifying")
		}
		if !verificationCapabilities(platform, arch)[a.Harness].Supported {
			return fail(409, "verification_unavailable", "this harness cannot enforce safe verification")
		}
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE id=$1 AND enabled AND harness=$2)`, a.ProfileID, a.Harness).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return fail(409, "conflict", "approved model profile is unavailable")
		}
		if run != nil {
			var status string
			if err := tx.QueryRow(ctx, `SELECT status FROM agent_runs WHERE id=$1 AND purpose='pairing_verification' FOR UPDATE`, *run).Scan(&status); err != nil {
				return err
			}
			terminal := status == "completed" || status == "failed" || !claimed && status == "cancelled"
			if !terminal && (claimed || status != "queued") {
				return fail(409, "verification_active", "reconcile the existing verification before trying again")
			}
			if claimed {
				var held bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_reservations WHERE run_id=$1 AND state='active')`, *run).Scan(&held); err != nil {
					return err
				}
				if held {
					return fail(409, "verification_active", "settle the existing verification before trying again")
				}
			}
			if !claimed && status == "queued" {
				if err := cancelQueuedRun(ctx, tx, *run); err != nil {
					return err
				}
			}
		}
		// Retire only this enrollment's old check budget, preserving ordinary
		// budgets, settlement history, cleanup and every sibling enrollment.
		if _, err := tx.Exec(ctx, `UPDATE account_allowance_windows SET ends_at=LEAST(ends_at,clock_timestamp()) WHERE account_id=$1 AND pairing_verification AND ends_at>clock_timestamp()`, account); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `UPDATE agent_pairing_enrollments SET verification_claimed_at=NULL,verification_expired_at=NULL,verification_expires_at=clock_timestamp()+interval '30 minutes' WHERE account_id=$1 RETURNING verification_expires_at`, account).Scan(&expires); err != nil {
			return err
		}
		out, err = createVerificationJob(ctx, tx, p, computer, principal, account, a, expires)
		if err != nil {
			return err
		}
		// Event counter is last. No projection sweep or row lock follows it.
		data := out.audit()
		data["previous_run_id"], data["computer_id"], data["request_id"] = run, computer, request
		return audit(ctx, tx, p, "agent_pairing.verification_created", data)
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}

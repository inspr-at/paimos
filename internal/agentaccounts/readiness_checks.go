// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const CheckGap = time.Minute

// Pending captures have a bounded lifetime independent of coalesced retries.
const CheckTTL = 5 * time.Minute

type AccountCheck struct {
	ID                     string    `json:"id"`
	AccountID              string    `json:"account_id"`
	ActorPrincipalID       string    `json:"actor_principal_id"`
	BindingRevision        int64     `json:"binding_revision"`
	DaemonGeneration       *string   `json:"daemon_generation"`
	State                  string    `json:"state"`
	RequestedAt            time.Time `json:"requested_at"`
	ExpiresAt              time.Time `json:"expires_at"`
	RetryAt                time.Time `json:"retry_at"`
	EarlyRecoveryRequested bool      `json:"early_recovery_requested"`
	Result                 *string   `json:"result,omitempty"`
}

type checkWrite struct {
	BindingRevision *int64 `json:"binding_revision"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type checkGapError struct {
	retryAt time.Time
	now     time.Time
}

func (e *checkGapError) Error() string { return "check minimum gap" }

// inReadinessWrite takes the tenant authorization fence, pairing, then tree
// before account/resource locks. Every write rechecks RequireTx in this transaction.
func (m *Module) inReadinessWrite(ctx context.Context, p tenant.Principal, fn func(pgx.Tx) error) error {
	return m.in(ctx, p.TenantID, fn)
}

func requireReadinessOwner(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, revision int64) (Account, error) {
	if p.Kind != tenant.Person || authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}) != nil {
		return Account{}, fail(403, "person account management required")
	}
	a, err := lockAccount(ctx, tx, id)
	if err != nil {
		return a, err
	}
	if a.OwnerPersonID == nil || *a.OwnerPersonID != p.ID {
		return a, fail(403, "account owner required")
	}
	if a.LinkRevision != revision {
		return a, &httpError{status: 409, code: "stale_binding", msg: "account binding changed"}
	}
	return a, nil
}

func requireCheckOwner(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, revision int64) (Account, error) {
	a, err := requireReadinessOwner(ctx, tx, p, id, revision)
	if err != nil {
		return a, err
	}

	if err := agentpairing.AccountFence(ctx, tx, id, false); err != nil {
		return a, err
	}
	var current bool
	if err := tx.QueryRow(ctx, `SELECT NOT `+retiredSQL+` AND NOT EXISTS (
        SELECT 1 FROM agent_pairing_enrollments e JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
        JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id
        WHERE e.account_id=agent_accounts.id AND (e.state<>'connected' OR c.state<>'connected' OR q.state<>'redeemed' OR e.ongoing_approved_at IS NULL))
        FROM agent_accounts WHERE id=$1`, id).Scan(&current); err != nil {
		return a, err
	}
	if !current {
		return a, fail(409, "account binding is unavailable")
	}
	return a, nil
}

const checkColumns = `id::text,account_id::text,actor_principal_id::text,binding_revision,daemon_generation,state,requested_at,early_recovery_requested,result`

func scanCheck(row scanner) (AccountCheck, error) {
	var c AccountCheck
	err := row.Scan(&c.ID, &c.AccountID, &c.ActorPrincipalID, &c.BindingRevision, &c.DaemonGeneration, &c.State, &c.RequestedAt, &c.EarlyRecoveryRequested, &c.Result)
	c.RetryAt = c.RequestedAt.Add(CheckGap)
	c.ExpiresAt = c.RequestedAt.Add(CheckTTL)
	return c, err
}

func requestCheck(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, in checkWrite) (AccountCheck, error) {
	a, err := requireCheckOwner(ctx, tx, p, id, *in.BindingRevision)
	if err != nil {
		return AccountCheck{}, err
	}
	if err := ensureLocalReadinessResource(ctx, tx, p, a); err != nil {
		return AccountCheck{}, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return AccountCheck{}, err
	}
	c, err := scanCheck(tx.QueryRow(ctx, `SELECT `+checkColumns+` FROM account_readiness_checks WHERE id=(SELECT check_id FROM account_readiness_check_keys WHERE account_id=$1 AND idempotency_key=$2)`, id, in.IdempotencyKey))
	if err == nil {
		if c.BindingRevision != a.LinkRevision || c.State == "invalidated" || c.State == "pending" && !now.Before(c.ExpiresAt) {
			return c, &httpError{status: 409, code: "stale_binding", msg: "check binding changed"}
		}
		return c, nil
	}
	if !isNoRows(err) {
		return c, err
	}
	if _, err := tx.Exec(ctx, `UPDATE account_readiness_checks SET state='invalidated' WHERE account_id=$1 AND state='pending' AND requested_at<=$2`, id, now.Add(-CheckTTL)); err != nil {
		return c, err
	}
	// Coalesce before the gap check. A distinct key never creates a new
	// capture (or a new recovery intent) while the current one is pending.
	c, err = scanCheck(tx.QueryRow(ctx, `SELECT `+checkColumns+` FROM account_readiness_checks WHERE account_id=$1 AND state='pending'`, id))
	fresh := isNoRows(err)
	if err != nil && !fresh {
		return c, err
	}
	if fresh {
		var last *time.Time
		if err := tx.QueryRow(ctx, `SELECT max(requested_at) FROM account_readiness_checks WHERE account_id=$1`, id).Scan(&last); err != nil {
			return c, err
		}
		if last != nil && now.Before(last.Add(CheckGap)) {
			return c, &checkGapError{last.Add(CheckGap), now}
		}
		c, err = scanCheck(tx.QueryRow(ctx, `INSERT INTO account_readiness_checks(tenant_id,account_id,actor_principal_id,binding_revision,daemon_generation,requested_at) VALUES($1,$2,$3,$4,$5,$6) RETURNING `+checkColumns, p.TenantID, id, p.ID, a.LinkRevision, a.daemonGeneration, now))
		if err != nil {
			return c, err
		}
	}
	if c.BindingRevision != a.LinkRevision {
		return c, fail(409, "pending check binding changed")
	}
	var aliases int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_readiness_check_keys WHERE check_id=$1`, c.ID).Scan(&aliases); err != nil {
		return c, err
	}
	if aliases >= 256 {
		return c, &checkGapError{now.Add(CheckGap), now}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO account_readiness_check_keys(tenant_id,account_id,idempotency_key,binding_revision,check_id,actor_principal_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.TenantID, id, in.IdempotencyKey, a.LinkRevision, c.ID, p.ID, now); err != nil {
		return c, err
	}
	if fresh {
		// Freeze a telemetry-only denial into its original canonical wait too;
		// the owner's check can then request the same bounded early recovery.
		if err := reconcileVendorStop(ctx, tx, a, now); err != nil {
			return c, err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO account_readiness_check_waits(tenant_id,check_id,resource_id,window_key,wait_id)
            SELECT f.tenant_id,$1,f.resource_id,f.window_key,f.wait_id FROM account_readiness_facts f
            JOIN account_readiness_memberships m ON m.tenant_id=f.tenant_id AND m.resource_id=f.resource_id
            WHERE m.account_id=$2 AND m.binding_revision=$3 AND f.wait_id IS NOT NULL AND NOT f.early_recovery_used
            AND (f.stop_kind='money_402' OR (f.stop_kind='unnamed' AND f.denial_reason IN ('','vendor_denied') AND (f.used_percent IS NULL OR f.used_percent<100) AND (f.remaining IS NULL OR f.remaining>0) AND f.credit_state<>'exhausted'))
            ORDER BY f.resource_id,f.window_key LIMIT 33`, c.ID, id, a.LinkRevision)
		if err != nil {
			return c, err
		}
		if tag.RowsAffected() > 32 {
			return c, fail(503, "too many recovery waits")
		}
		c.EarlyRecoveryRequested = tag.RowsAffected() > 0
		if _, err := tx.Exec(ctx, `UPDATE account_readiness_checks SET early_recovery_requested=$2 WHERE id=$1`, c.ID, c.EarlyRecoveryRequested); err != nil {
			return c, err
		}
	}
	if fresh {
		// Event counter is last; no later writes acquire resource locks.
		err = writeEvent(ctx, tx, p, "account.check_requested", nil, map[string]any{"account_id": id, "binding_revision": a.LinkRevision, "check_id": c.ID, "actor_principal_id": p.ID})
	}
	return c, err
}

func (m *Module) check(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := strings.ToLower(r.PathValue("accountId"))
	if !uuidRE.MatchString(id) {
		writeErr(w, fail(404, "account not found"))
		return
	}
	var in checkWrite
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.BindingRevision == nil || *in.BindingRevision < 0 || len(in.IdempotencyKey) > 128 || !accountKeyRE.MatchString(in.IdempotencyKey) || looksLikeCredential(in.IdempotencyKey) {
		writeErr(w, fail(400, "binding_revision and opaque idempotency_key required"))
		return
	}
	var out AccountCheck
	err := m.inReadinessWrite(r.Context(), p, func(tx pgx.Tx) error { var err error; out, err = requestCheck(r.Context(), tx, p, id, in); return err })
	if gap, ok := err.(*checkGapError); ok {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(gap.retryAt.Sub(gap.now).Seconds())))))
		httpapi.WriteJSON(w, 429, map[string]any{"error": "wait before checking again", "code": "check_gap", "retry_at": gap.retryAt})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 202, out)
}

func (m *Module) sharing(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := strings.ToLower(r.PathValue("accountId"))
	if !uuidRE.MatchString(id) {
		writeErr(w, fail(404, "account not found"))
		return
	}
	var in struct {
		BindingRevision *int64 `json:"binding_revision"`
		ShareUsage      *bool  `json:"share_usage"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.BindingRevision == nil || *in.BindingRevision < 0 || in.ShareUsage == nil {
		writeErr(w, fail(400, "binding_revision and share_usage required"))
		return
	}
	err := m.inReadinessWrite(r.Context(), p, func(tx pgx.Tx) error {
		if _, err := requireReadinessOwner(r.Context(), tx, p, id, *in.BindingRevision); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `UPDATE agent_accounts SET share_usage=$2 WHERE id=$1 AND share_usage<>$2`, id, *in.ShareUsage)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return writeEvent(r.Context(), tx, p, "account.sharing_changed", nil, map[string]any{"account_id": id, "binding_revision": *in.BindingRevision, "share_usage": *in.ShareUsage})
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/openrouter"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	evRegistered = "account.registered"
	evUpdated    = "account.updated"
	evProbed     = "account.probed"
	evWindow     = "account.window_created"
	evReserved   = "account.reserved"
	evSettled    = "account.settled"
	evReleased   = "account.released"
	evArchived   = "account.archived"
)

func writeEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, eventType string, before, after any) error {
	_, err := events.Append(ctx, tx, p, events.Change{Type: eventType, Before: before, After: after})
	return err
}

// clockKey permits an in-process clock override; production uses transaction
// time. It cannot be supplied through the HTTP contract.
type clockKey struct{}

func dbNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	if now, ok := ctx.Value(clockKey{}).(time.Time); ok {
		return now, nil
	}
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now)
	return now, err
}

// retiredSQL matches an account that left the workspace's lists (AEON-402): a
// person removed it, or its only computer binding is revoked for good. Its
// runs, readings and events stay; it never routes again.
const retiredSQL = `(agent_accounts.archived_at IS NOT NULL OR EXISTS (SELECT 1 FROM agent_pairing_enrollments e
		WHERE e.tenant_id = agent_accounts.tenant_id AND e.account_id = agent_accounts.id AND e.state = 'revoked'))`

// listAccounts lists the workspace's current accounts, without retired ones.
func listAccounts(ctx context.Context, tx pgx.Tx) ([]Account, error) {
	return queryAccounts(ctx, tx, false)
}

// queryAccounts with retired lists every account, for a daemon reconciling
// its own registrations.
func queryAccounts(ctx context.Context, tx pgx.Tx, retired bool) ([]Account, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, account_key, harness, daemon_id, label, max_parallel_runs,
		       registered_by_principal_id::text, state, last_probe_at, last_probe_ok,
		       last_daemon_generation, created_at, plan, host_label, allowed_model_profile_ids::text[], reading_support, quota_fingerprint, statusline_enabled, provider, model, model_status, model_data_note, openrouter_credits, COALESCE(group_id::text,''), quota_pool_fingerprint, billing_mode
		FROM agent_accounts
		WHERE $1 OR NOT `+retiredSQL+`
		ORDER BY created_at, id`, retired)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, account)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return attachWindows(ctx, tx, out)
}

func getAccount(ctx context.Context, tx pgx.Tx, id string) (Account, error) {
	row := tx.QueryRow(ctx, `
		SELECT id::text, account_key, harness, daemon_id, label, max_parallel_runs,
		       registered_by_principal_id::text, state, last_probe_at, last_probe_ok,
		       last_daemon_generation, created_at, plan, host_label, allowed_model_profile_ids::text[], reading_support, quota_fingerprint, statusline_enabled, provider, model, model_status, model_data_note, openrouter_credits, COALESCE(group_id::text,''), quota_pool_fingerprint, billing_mode
		FROM agent_accounts WHERE id = $1::uuid`, id)
	account, err := scanAccount(row)
	if err != nil {
		return Account{}, err
	}
	with, err := attachWindows(ctx, tx, []Account{account})
	if err != nil {
		return Account{}, err
	}
	return with[0], nil
}

type scanner interface{ Scan(...any) error }

func scanAccount(row scanner) (Account, error) {
	var account Account
	err := row.Scan(&account.ID, &account.AccountKey, &account.Harness, &account.DaemonID, &account.Label,
		&account.MaxParallel, &account.RegisteredBy, &account.State, &account.LastProbeAt, &account.LastProbeOK,
		&account.daemonGeneration, &account.CreatedAt, &account.Plan, &account.HostLabel, &account.AllowedProfileIDs, &account.ReadingSupport, &account.QuotaFingerprint, &account.StatuslineEnabled, &account.Provider, &account.Model, &account.ModelStatus, &account.ModelDataNote, &account.OpenRouterCredits, &account.GroupID, &account.QuotaPoolFingerprint, &account.BillingMode)
	account.Windows = []Window{}
	return account, err
}

func attachWindows(ctx context.Context, tx pgx.Tx, accounts []Account) ([]Account, error) {
	if len(accounts) == 0 {
		return accounts, nil
	}
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	rows, err := tx.Query(ctx, `
		SELECT w.id::text, w.account_id::text, w.starts_at, w.ends_at, w.unit,
		       w.allowance, w.used, w.reserved, w.pace_model, w.burst_ratio::float8,
		       NOT EXISTS (
		           SELECT 1 FROM account_reservations r
		           WHERE r.tenant_id = w.tenant_id AND r.window_id = w.id AND r.state = 'settled'
		       ) OR EXISTS (
		           SELECT 1 FROM account_reservations r
		           WHERE r.tenant_id = w.tenant_id AND r.window_id = w.id
		             AND r.state = 'settled' AND r.actual_units = 0
		       ) AS provisional, w.capacity_read_at, w.capacity_allowed, COALESCE(w.capacity_kind,''), w.capacity_bucket, w.capacity_retired, w.capacity_refresh_run::text, COALESCE(w.capacity_source,'')
		FROM account_allowance_windows w
		WHERE w.account_id=ANY($1::uuid[]) AND NOT w.pairing_verification AND NOT w.capacity_retired AND w.removed_at IS NULL
		ORDER BY w.account_id, w.starts_at, w.id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byAccount := map[string][]Window{}
	for rows.Next() {
		var w Window
		if err := rows.Scan(&w.ID, &w.AccountID, &w.StartsAt, &w.EndsAt, &w.Unit, &w.Allowance, &w.Used, &w.Reserved, &w.PaceModel, &w.BurstRatio, &w.Provisional, &w.capacityReadAt, &w.capacityAllowed, &w.capacityKind, &w.capacityBucket, &w.capacityRetired, &w.capacityRefreshRun, &w.capacitySource); err != nil {
			return nil, err
		}
		if w.capacityReadAt != nil {
			w.Provisional = synthetic(w)
		} else {
			w.SetByYou = true
		} // Vendor percentage is measured; token settlement is irrelevant.
		byAccount[w.AccountID] = append(byAccount[w.AccountID], w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range accounts {
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM agent_pairing_enrollments WHERE account_id=$1 AND (ongoing_approved_at IS NULL OR state<>'connected')),a.owner_person_id::text,COALESCE(p.name,''),a.linked_at,a.link_revision,a.share_usage FROM agent_accounts a LEFT JOIN principals p ON p.tenant_id=a.tenant_id AND p.id=a.owner_person_id WHERE a.id=$1`, accounts[i].ID).Scan(&accounts[i].OngoingUseApproved, &accounts[i].OwnerPersonID, &accounts[i].OwnerPersonName, &accounts[i].LinkedAt, &accounts[i].LinkRevision, &accounts[i].ShareUsage); err != nil {
			return nil, err
		}
		if ws := byAccount[accounts[i].ID]; ws != nil {
			accounts[i].Windows = ws
		} else {
			accounts[i].Windows = []Window{}
		}
	}
	if err := fillGroupNames(ctx, tx, accounts); err != nil {
		return nil, err
	}
	return accounts, nil
}

func occupancy(ctx context.Context, tx pgx.Tx) (map[string]int, error) {
	rows, err := tx.Query(ctx, `
		SELECT account_id::text, count(*)
		FROM agent_runs
		WHERE account_id IS NOT NULL
		  AND status IN ('queued', 'starting', 'running', 'waiting')
		GROUP BY account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

type accountWrite struct {
	AccountKey  string `json:"account_key"`
	Harness     string `json:"harness"`
	DaemonID    string `json:"daemon_id"`
	Label       string `json:"label"`
	MaxParallel *int   `json:"max_parallel_runs"`
}

func registerAccount(ctx context.Context, tx pgx.Tx, p tenant.Principal, in accountWrite) (Account, error) {
	if paired, err := agentpairing.PairedPrincipal(ctx, tx, p.ID); err != nil {
		return Account{}, err
	} else if paired {
		return Account{}, fail(403, "paired accounts require fresh person approval")
	}
	key, err := cleanText(in.AccountKey, 128)
	if err != nil || !accountKeyRE.MatchString(key) {
		return Account{}, fail(http.StatusBadRequest, "invalid account key")
	}
	daemonID, err := cleanText(in.DaemonID, 128)
	if err != nil {
		return Account{}, fail(http.StatusBadRequest, "invalid daemon id")
	}
	label, err := metadataText(in.Label, false)
	if err != nil {
		return Account{}, fail(http.StatusBadRequest, "invalid label")
	}
	if !validHarness(in.Harness) {
		return Account{}, fail(http.StatusBadRequest, "invalid harness")
	}
	if looksLikeCredential(key) || looksLikeCredential(label) || looksLikeCredential(daemonID) {
		return Account{}, fail(http.StatusBadRequest, "credential material is not accepted")
	}
	parallel := 1
	if in.MaxParallel != nil {
		if *in.MaxParallel < 1 {
			return Account{}, fail(http.StatusBadRequest, "max_parallel_runs must be positive")
		}
		parallel = *in.MaxParallel
	}
	existing, err := findAccount(ctx, tx, daemonID, in.Harness, key)
	if err == nil {
		if existing.RegisteredBy == p.ID && existing.Label == label && existing.MaxParallel == parallel {
			return existing, nil
		}
		return Account{}, fail(http.StatusConflict, "account already exists")
	}
	if !isNoRows(err) {
		return Account{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO agent_accounts
			(tenant_id, account_key, harness, daemon_id, registered_by_principal_id, label, max_parallel_runs)
		VALUES ($1::uuid, $2, $3, $4, $5::uuid, $6, $7)
		RETURNING id::text`,
		p.TenantID, key, in.Harness, daemonID, p.ID, label, parallel).Scan(&id)
	if err != nil {
		return Account{}, err
	}
	account, err := getAccount(ctx, tx, id)
	if err != nil {
		return Account{}, err
	}
	if err := writeEvent(ctx, tx, p, evRegistered, nil, account); err != nil {
		return Account{}, err
	}
	return account, nil
}

func findAccount(ctx context.Context, tx pgx.Tx, daemonID, harness, key string) (Account, error) {
	row := tx.QueryRow(ctx, `
		SELECT id::text, account_key, harness, daemon_id, label, max_parallel_runs,
		       registered_by_principal_id::text, state, last_probe_at, last_probe_ok,
		       last_daemon_generation, created_at, plan, host_label, allowed_model_profile_ids::text[], reading_support, quota_fingerprint, statusline_enabled, provider, model, model_status, model_data_note, openrouter_credits, COALESCE(group_id::text,''), quota_pool_fingerprint, billing_mode
		FROM agent_accounts
		WHERE daemon_id = $1 AND harness = $2 AND account_key = $3`, daemonID, harness, key)
	account, err := scanAccount(row)
	if err != nil {
		return Account{}, err
	}
	with, err := attachWindows(ctx, tx, []Account{account})
	if err != nil {
		return Account{}, err
	}
	return with[0], nil
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

type probeWrite struct {
	Readiness         *ReadinessReport    `json:"readiness,omitempty"`
	OpenRouterCredits *openrouter.Credits `json:"openrouter_credits"`
	DaemonID          string              `json:"daemon_id"`
	DaemonGeneration  string              `json:"daemon_generation"`
	Available         bool                `json:"available"`
	HostLabel         *string             `json:"host_label"`
	// Failure says why a probe failed: auth_failed only when the vendor status
	// command confirmed a sign-out for this account, else unavailable.
	Failure string `json:"failure,omitempty"`
}

func reportProbe(ctx context.Context, tx pgx.Tx, p tenant.Principal, accountID string, in probeWrite) (Account, error) {
	if !uuidRE.MatchString(accountID) {
		return Account{}, fail(http.StatusNotFound, "account not found")
	}
	daemonID, err := cleanText(in.DaemonID, 128)
	if err != nil {
		return Account{}, fail(http.StatusBadRequest, "invalid daemon id")
	}
	generation, err := cleanText(in.DaemonGeneration, 128)
	if err != nil {
		return Account{}, fail(http.StatusBadRequest, "invalid daemon generation")
	}
	if in.HostLabel != nil {
		label, err := metadataText(*in.HostLabel, true)
		if err != nil {
			return Account{}, err
		}
		in.HostLabel = &label
	}
	failure := ""
	switch {
	case in.Available && in.Failure != "":
		return Account{}, fail(http.StatusBadRequest, "a successful probe has no failure")
	case in.Available:
	case in.Failure == "" || in.Failure == "unavailable":
		failure = "unavailable"
	case in.Failure == "auth_failed":
		failure = "auth_failed"
	default:
		return Account{}, fail(http.StatusBadRequest, "invalid probe failure")
	}
	if err := agentpairing.AccountFence(ctx, tx, accountID, false); err != nil {
		return Account{}, err
	}
	before, err := lockAccount(ctx, tx, accountID)
	if err != nil {
		return Account{}, err
	}
	if before.RegisteredBy != p.ID {
		return Account{}, fail(http.StatusForbidden, "only the registering agent can probe")
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return Account{}, err
	}
	if in.OpenRouterCredits != nil && (before.Provider != "openrouter" || !in.OpenRouterCredits.Valid() || in.OpenRouterCredits.ObservedAt.After(now.Add(time.Minute))) {
		return Account{}, fail(400, "invalid OpenRouter credits")
	}
	if before.DaemonID != daemonID {
		return Account{}, fail(http.StatusForbidden, "daemon does not match account")
	}
	if err := ensureLocalReadinessResource(ctx, tx, p, before); err != nil {
		return Account{}, err
	}
	if c := before.OpenRouterCredits; c != nil && c.Remaining != nil {
		resource, err := localReadinessResource(ctx, tx, before)
		if err != nil {
			return Account{}, err
		}
		if err := storeReadinessFact(tenant.WithPrincipal(ctx, p), tx, before, ReadinessFactWrite{ResourceID: resource, WindowKey: "key_cap", Source: "provider", ObservedAt: c.ObservedAt, ReadingAt: &c.ObservedAt, Remaining: c.Remaining, CreditState: "unknown"}, now); err != nil {
			return Account{}, err
		}
	}
	if c := in.OpenRouterCredits; c != nil && c.Remaining != nil {
		resource, err := localReadinessResource(ctx, tx, before)
		if err != nil {
			return Account{}, err
		}
		if err := storeReadinessFact(tenant.WithPrincipal(ctx, p), tx, before, ReadinessFactWrite{ResourceID: resource, WindowKey: "key_cap", Source: "provider", ObservedAt: c.ObservedAt, ReadingAt: &c.ObservedAt, Remaining: c.Remaining, CreditState: "unknown"}, now); err != nil {
			return Account{}, err
		}
	}
	if in.Readiness != nil {
		if in.Readiness.CheckID != "" && before.daemonGeneration != nil && *before.daemonGeneration != generation {
			return Account{}, fail(409, "readiness daemon generation changed")
		}
		if err := completeReadinessReport(tenant.WithPrincipal(ctx, p), tx, before, generation, *in.Readiness, now); err != nil {
			return Account{}, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agent_accounts
		SET last_probe_at = $7, last_probe_ok = $2, last_daemon_generation = $3, host_label = CASE WHEN host_label = '' THEN COALESCE($4, '') ELSE host_label END,
		    last_probe_failure = $5, openrouter_credits=$6
		WHERE id = $1::uuid`, accountID, in.Available, generation, in.HostLabel, failure, in.OpenRouterCredits, now); err != nil {
		return Account{}, err
	}
	after, err := getAccount(ctx, tx, accountID)
	if err != nil {
		return Account{}, err
	}
	if after.LastProbeAt != nil {
		if err := learnOnline(ctx, tx, after, *after.LastProbeAt); err != nil {
			return Account{}, err
		}
	}
	if err := writeEvent(ctx, tx, p, evProbed, before, after); err != nil {
		return Account{}, err
	}
	return after, nil
}

func lockAccount(ctx context.Context, tx pgx.Tx, id string) (Account, error) {
	row := tx.QueryRow(ctx, `
		SELECT id::text, account_key, harness, daemon_id, label, max_parallel_runs,
		       registered_by_principal_id::text, state, last_probe_at, last_probe_ok,
		       last_daemon_generation, created_at, plan, host_label, allowed_model_profile_ids::text[], reading_support, quota_fingerprint, statusline_enabled, provider, model, model_status, model_data_note, openrouter_credits, COALESCE(group_id::text,''), quota_pool_fingerprint, billing_mode
		FROM agent_accounts WHERE id = $1::uuid FOR UPDATE`, id)
	account, err := scanAccount(row)
	if isNoRows(err) {
		return Account{}, fail(http.StatusNotFound, "account not found")
	}
	if err != nil {
		return Account{}, err
	}
	with, err := attachWindows(ctx, tx, []Account{account})
	if err != nil {
		return Account{}, err
	}
	return with[0], nil
}

type stateWrite struct {
	State string `json:"state"`
}

func updateState(ctx context.Context, tx pgx.Tx, p tenant.Principal, accountID string, state string) (Account, error) {
	if !uuidRE.MatchString(accountID) {
		return Account{}, fail(http.StatusNotFound, "account not found")
	}
	if !validState(state) {
		return Account{}, fail(http.StatusBadRequest, "invalid state")
	}
	var archived bool
	if err := tx.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM agent_accounts WHERE id = $1::uuid`, accountID).Scan(&archived); err == nil && archived {
		return Account{}, &httpError{status: http.StatusConflict, msg: "this account was removed", code: "account_removed"}
	}
	if err := agentpairing.AccountFence(ctx, tx, accountID, false); err != nil {
		return Account{}, err
	}
	before, err := lockAccount(ctx, tx, accountID)
	if err != nil {
		return Account{}, err
	}
	if before.State == state {
		return before, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET state = $2 WHERE id = $1::uuid`, accountID, state); err != nil {
		return Account{}, err
	}
	after, err := getAccount(ctx, tx, accountID)
	if err != nil {
		return Account{}, err
	}
	if err := writeEvent(ctx, tx, p, evUpdated, before, after); err != nil {
		return Account{}, err
	}
	return after, nil
}

type windowWrite struct {
	StartsAt   time.Time `json:"starts_at"`
	EndsAt     time.Time `json:"ends_at"`
	Unit       string    `json:"unit"`
	Allowance  int64     `json:"allowance"`
	PaceModel  string    `json:"pace_model"`
	BurstRatio *float64  `json:"burst_ratio"`
}

func createWindow(ctx context.Context, tx pgx.Tx, p tenant.Principal, accountID string, in windowWrite) (Window, error) {
	if !uuidRE.MatchString(accountID) {
		return Window{}, fail(http.StatusNotFound, "account not found")
	}
	if !validUnit(in.Unit) || !validPace(in.PaceModel) {
		return Window{}, fail(http.StatusBadRequest, "invalid allowance window")
	}
	if in.Allowance < 1 || in.StartsAt.IsZero() || !in.EndsAt.After(in.StartsAt) {
		return Window{}, fail(http.StatusBadRequest, "invalid allowance window")
	}
	burst := 0.1
	if in.BurstRatio != nil {
		burst = *in.BurstRatio
	}
	if math.IsNaN(burst) || math.IsInf(burst, 0) || burst < 0 || burst > 1 {
		return Window{}, fail(http.StatusBadRequest, "invalid burst ratio")
	}
	burst = math.Round(burst*10000) / 10000
	if err := agentpairing.AccountFence(ctx, tx, accountID, false); err != nil {
		return Window{}, err
	}
	if _, err := lockAccount(ctx, tx, accountID); err != nil {
		return Window{}, err
	}
	var overlap bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM account_allowance_windows
			WHERE account_id = $1::uuid AND unit = $2 AND NOT pairing_verification AND removed_at IS NULL
			  AND starts_at < $4 AND ends_at > $3
		)`, accountID, in.Unit, in.StartsAt, in.EndsAt).Scan(&overlap); err != nil {
		return Window{}, err
	}
	if overlap {
		return Window{}, fail(http.StatusConflict, "allowance windows overlap")
	}
	var w Window
	err := tx.QueryRow(ctx, `
		INSERT INTO account_allowance_windows
			(tenant_id, account_id, starts_at, ends_at, unit, allowance, pace_model, burst_ratio)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8)
		RETURNING id::text, account_id::text, starts_at, ends_at, unit, allowance, used, reserved,
		          pace_model, burst_ratio::float8`,
		p.TenantID, accountID, in.StartsAt, in.EndsAt, in.Unit, in.Allowance, in.PaceModel, burst).
		Scan(&w.ID, &w.AccountID, &w.StartsAt, &w.EndsAt, &w.Unit, &w.Allowance, &w.Used, &w.Reserved, &w.PaceModel, &w.BurstRatio)
	if err != nil {
		return Window{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_pairing_enrollments SET ongoing_approved_at=clock_timestamp() WHERE account_id=$1 AND state='connected'`, accountID); err != nil {
		return Window{}, err
	}
	w.Provisional = true // No reservation has measured usage in a new window.
	w.SetByYou = true
	if err := writeEvent(ctx, tx, p, evWindow, nil, w); err != nil {
		return Window{}, err
	}
	return w, nil
}

// archiveAccount removes an account from the workspace's lists (AEON-402). A
// paired binding is disconnected first; queued runs bound to it are cancelled
// and release their holds. It refuses while a run works on the account, so its
// accounting stays known. Nothing is deleted: runs, readings and events stay.
func archiveAccount(ctx context.Context, tx pgx.Tx, p tenant.Principal, accountID string) (Account, error) {
	if !uuidRE.MatchString(accountID) {
		return Account{}, fail(http.StatusNotFound, "account not found")
	}
	before, err := lockAccount(ctx, tx, accountID)
	if err != nil {
		return Account{}, err
	}
	var archived bool
	if err := tx.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM agent_accounts WHERE id = $1::uuid`, accountID).Scan(&archived); err != nil {
		return Account{}, err
	}
	if archived {
		return before, nil
	}
	if err := agentpairing.DisconnectAccount(ctx, tx, p, accountID); errors.Is(err, agentpairing.ErrActiveRuns) {
		return Account{}, &httpError{status: http.StatusConflict, msg: "a run is still working on this account", code: "account_busy"}
	} else if err != nil {
		return Account{}, err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE account_id = $1::uuid AND status IN ('starting','running','waiting'))`, accountID).Scan(&active); err != nil {
		return Account{}, err
	}
	if active {
		return Account{}, &httpError{status: http.StatusConflict, msg: "a run is still working on this account", code: "account_busy"}
	}
	// A vendor-handoff retry has no pin of its own; retry_account_id is its target.
	rows, err := tx.Query(ctx, `SELECT id::text FROM agent_runs WHERE status = 'queued'
		AND (account_id = $1::uuid OR COALESCE(requested_account_id, retry_account_id) = $1::uuid) ORDER BY id FOR UPDATE`, accountID)
	if err != nil {
		return Account{}, err
	}
	var queued []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return Account{}, err
		}
		queued = append(queued, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Account{}, err
	}
	cancelled := []string{}
	for _, id := range queued {
		ok, err := agentpairing.CancelQueuedRun(ctx, tx, id)
		if err != nil {
			return Account{}, err
		}
		if ok {
			cancelled = append(cancelled, id)
		}
	}
	var at time.Time
	if err := tx.QueryRow(ctx, `UPDATE agent_accounts SET state = 'unavailable', archived_at = clock_timestamp(), archived_by_principal_id = $2::uuid
		WHERE id = $1::uuid RETURNING archived_at`, accountID, p.ID).Scan(&at); err != nil {
		return Account{}, err
	}
	after, err := getAccount(ctx, tx, accountID)
	if err != nil {
		return Account{}, err
	}
	if err := writeEvent(ctx, tx, p, evArchived, before, map[string]any{"account": after, "archived_at": at, "cancelled_run_ids": cancelled}); err != nil {
		return Account{}, err
	}
	return after, nil
}

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
)

func writeEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, eventType string, before, after any) error {
	_, err := events.Append(ctx, tx, p, events.Change{Type: eventType, Before: before, After: after})
	return err
}

func dbNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now)
	return now, err
}

func listAccounts(ctx context.Context, tx pgx.Tx) ([]Account, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, account_key, harness, daemon_id, label, max_parallel_runs,
		       registered_by_principal_id::text, state, last_probe_at, last_probe_ok,
		       last_daemon_generation, created_at, plan, host_label, allowed_model_profile_ids::text[]
		FROM agent_accounts
		ORDER BY created_at, id`)
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
		       last_daemon_generation, created_at, plan, host_label, allowed_model_profile_ids::text[]
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
		&account.daemonGeneration, &account.CreatedAt, &account.Plan, &account.HostLabel, &account.AllowedProfileIDs)
	account.Windows = []Window{}
	return account, err
}

func attachWindows(ctx context.Context, tx pgx.Tx, accounts []Account) ([]Account, error) {
	if len(accounts) == 0 {
		return accounts, nil
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
		       ) AS provisional
		FROM account_allowance_windows w
		ORDER BY w.account_id, w.starts_at, w.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byAccount := map[string][]Window{}
	for rows.Next() {
		var w Window
		if err := rows.Scan(&w.ID, &w.AccountID, &w.StartsAt, &w.EndsAt, &w.Unit, &w.Allowance, &w.Used, &w.Reserved, &w.PaceModel, &w.BurstRatio, &w.Provisional); err != nil {
			return nil, err
		}
		byAccount[w.AccountID] = append(byAccount[w.AccountID], w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range accounts {
		if ws := byAccount[accounts[i].ID]; ws != nil {
			accounts[i].Windows = ws
		} else {
			accounts[i].Windows = []Window{}
		}
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
		       last_daemon_generation, created_at, plan, host_label, allowed_model_profile_ids::text[]
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
	DaemonID         string  `json:"daemon_id"`
	DaemonGeneration string  `json:"daemon_generation"`
	Available        bool    `json:"available"`
	HostLabel        *string `json:"host_label"`
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
	if before.DaemonID != daemonID {
		return Account{}, fail(http.StatusForbidden, "daemon does not match account")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agent_accounts
		SET last_probe_at = now(), last_probe_ok = $2, last_daemon_generation = $3, host_label = CASE WHEN host_label = '' THEN COALESCE($4, '') ELSE host_label END
		WHERE id = $1::uuid`, accountID, in.Available, generation, in.HostLabel); err != nil {
		return Account{}, err
	}
	after, err := getAccount(ctx, tx, accountID)
	if err != nil {
		return Account{}, err
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
		       last_daemon_generation, created_at, plan, host_label, allowed_model_profile_ids::text[]
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
			WHERE account_id = $1::uuid AND unit = $2
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
	if err := writeEvent(ctx, tx, p, evWindow, nil, w); err != nil {
		return Window{}, err
	}
	return w, nil
}

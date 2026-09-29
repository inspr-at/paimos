// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

type usageSums struct {
	requests int64
	tokens   int64
	cost     int64
}

type reservationRow struct {
	ID       string
	WindowID string
	Reserved int64
	Actual   *int64
	State    string
	Unit     string
}

// Settle applies monotonic telemetry sums to the run's reservations.
// The caller must already be inside db.InTenant and rolls back on error.
// A second call with the same telemetry is a no-op. A later call may only
// increase settled usage.
func Settle(ctx context.Context, tx pgx.Tx, actor tenant.Principal, runID string) error {
	run, err := lockRun(ctx, tx, runID)
	if err != nil {
		return err
	}
	if err := actorMayUseRun(ctx, tx, actor, run.AgentID, run.ID); err != nil {
		return err
	}
	sums, err := telemetrySums(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	rows, err := lockReservations(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fail(http.StatusNotFound, "run has no reservation")
	}
	windows, err := lockWindows(ctx, tx, windowIDs(rows))
	if err != nil {
		return err
	}
	changed := false
	for _, row := range rows {
		if row.State == "released" {
			continue
		}
		actual, err := actualUnits(row.Unit, sums)
		if err != nil {
			return err
		}
		window, ok := windows[row.WindowID]
		if !ok {
			return fail(http.StatusConflict, "allowance window is missing")
		}
		switch row.State {
		case "active":
			if err := settleActive(ctx, tx, window, row, actual); err != nil {
				return err
			}
			changed = true
		case "settled":
			if row.Actual == nil {
				return fail(http.StatusConflict, "settled reservation has no usage")
			}
			if actual < *row.Actual {
				return fail(http.StatusConflict, "usage is not monotonic")
			}
			if actual == *row.Actual {
				continue
			}
			if err := settleIncrease(ctx, tx, window, row, actual); err != nil {
				return err
			}
			changed = true
		default:
			return fail(http.StatusConflict, "unknown reservation state")
		}
	}
	if !changed {
		return nil
	}
	return writeEvent(ctx, tx, actor, evSettled, nil, map[string]any{"run_id": run.ID, "requests": sums.requests, "tokens": sums.tokens, "cost_micros": sums.cost})
}

// Release returns unused reserved units after a queued cancel or a fenced
// terminal transition. A starting, running or waiting run is left untouched.
// When the run recorded a daemon, daemonID and generation must match it.
func Release(ctx context.Context, tx pgx.Tx, actor tenant.Principal, runID, daemonID, generation string) error {
	run, err := lockRun(ctx, tx, runID)
	if err != nil {
		return err
	}
	if err := actorMayUseRun(ctx, tx, actor, run.AgentID, run.ID); err != nil {
		return err
	}
	if run.DaemonID != nil && *run.DaemonID != "" {
		gen := ""
		if run.Generation != nil {
			gen = *run.Generation
		}
		if daemonID != *run.DaemonID || generation != gen {
			return fail(http.StatusConflict, "daemon fence does not match")
		}
	}
	switch run.Status {
	case "queued", "completed", "failed", "cancelled", "ownership_lost":
	default:
		return fail(http.StatusConflict, "live run keeps its reservation")
	}
	rows, err := lockReservations(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	if _, err := lockWindows(ctx, tx, windowIDs(rows)); err != nil {
		return err
	}
	released := 0
	for _, row := range rows {
		if row.State != "active" {
			continue
		}
		tag, err := tx.Exec(ctx, `
			UPDATE account_allowance_windows
			SET reserved = reserved - $2
			WHERE id = $1::uuid AND reserved >= $2`, row.WindowID, row.Reserved)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fail(http.StatusConflict, "reservation could not be released")
		}
		tag, err = tx.Exec(ctx, `
			UPDATE account_reservations
			SET state = 'released', settled_at = now()
			WHERE id = $1::uuid AND state = 'active'`, row.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fail(http.StatusConflict, "reservation could not be released")
		}
		released++
	}
	cleared := false
	if run.Status == "queued" && run.AccountID != nil {
		tag, err := tx.Exec(ctx, `
			UPDATE agent_runs SET account_id = NULL
			WHERE id = $1::uuid AND status = 'queued'`, run.ID)
		if err != nil {
			return err
		}
		cleared = tag.RowsAffected() == 1
	}
	if released == 0 && !cleared {
		return nil
	}
	return writeEvent(ctx, tx, actor, evReleased, nil, map[string]any{"run_id": run.ID, "released": released})
}

func telemetrySums(ctx context.Context, tx pgx.Tx, runID string) (usageSums, error) {
	var sums usageSums
	err := tx.QueryRow(ctx, `
		SELECT coalesce(sum(turn_count_delta), 0)::bigint,
		       coalesce(sum(input_tokens_delta + output_tokens_delta), 0)::bigint,
		       coalesce(sum(cost_micros_delta), 0)::bigint
		FROM run_telemetry WHERE run_id = $1::uuid`, runID).Scan(&sums.requests, &sums.tokens, &sums.cost)
	return sums, err
}

func lockReservations(ctx context.Context, tx pgx.Tx, runID string) ([]reservationRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.id::text, r.window_id::text, r.reserved_units, r.actual_units, r.state, w.unit
		FROM account_reservations r
		JOIN account_allowance_windows w ON w.tenant_id = r.tenant_id AND w.id = r.window_id
		WHERE r.run_id = $1::uuid
		ORDER BY r.id
		FOR UPDATE OF r`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []reservationRow
	for rows.Next() {
		var row reservationRow
		if err := rows.Scan(&row.ID, &row.WindowID, &row.Reserved, &row.Actual, &row.State, &row.Unit); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func windowIDs(rows []reservationRow) []string {
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, ok := seen[row.WindowID]; ok {
			continue
		}
		seen[row.WindowID] = struct{}{}
		ids = append(ids, row.WindowID)
	}
	sort.Strings(ids)
	return ids
}

func lockWindows(ctx context.Context, tx pgx.Tx, ids []string) (map[string]Window, error) {
	out := map[string]Window{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, account_id::text, starts_at, ends_at, unit, allowance, used, reserved,
		       pace_model, burst_ratio::float8
		FROM account_allowance_windows
		WHERE id::text = ANY($1::text[])
		ORDER BY id
		FOR UPDATE`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var w Window
		if err := rows.Scan(&w.ID, &w.AccountID, &w.StartsAt, &w.EndsAt, &w.Unit, &w.Allowance, &w.Used, &w.Reserved, &w.PaceModel, &w.BurstRatio); err != nil {
			return nil, err
		}
		out[w.ID] = w
	}
	return out, rows.Err()
}

func actualUnits(unit string, sums usageSums) (int64, error) {
	switch unit {
	case "requests":
		return sums.requests, nil
	case "tokens":
		return sums.tokens, nil
	case "cost_micros":
		return sums.cost, nil
	case "percent":
		// Authoritative readings already contain usage. Telemetry cannot convert
		// tokens to vendor quota; settle only releases the held percentage.
		return 0, nil
	default:
		return 0, fail(http.StatusConflict, "unknown allowance")
	}
}

func settleActive(ctx context.Context, tx pgx.Tx, window Window, row reservationRow, actual int64) error {
	tag, err := tx.Exec(ctx, `
		UPDATE account_allowance_windows
		SET used = used + $2, reserved = reserved - $3
		WHERE id = $1::uuid AND reserved >= $3
		  AND (unit = 'percent' OR used + $2 + reserved - $3 <= allowance)`, window.ID, actual, row.Reserved)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(http.StatusConflict, "allowance exceeded")
	}
	tag, err = tx.Exec(ctx, `
		UPDATE account_reservations
		SET state = 'settled', actual_units = $2, settled_at = now()
		WHERE id = $1::uuid AND state = 'active'`, row.ID, actual)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(http.StatusConflict, "reservation could not be settled")
	}
	return nil
}

func settleIncrease(ctx context.Context, tx pgx.Tx, window Window, row reservationRow, actual int64) error {
	diff := actual - *row.Actual
	tag, err := tx.Exec(ctx, `
		UPDATE account_allowance_windows
		SET used = used + $2
		WHERE id = $1::uuid AND used + $2 + reserved <= allowance`, window.ID, diff)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(http.StatusConflict, "allowance exceeded")
	}
	tag, err = tx.Exec(ctx, `
		UPDATE account_reservations
		SET actual_units = $2, settled_at = now()
		WHERE id = $1::uuid AND state = 'settled'`, row.ID, actual)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(http.StatusConflict, "reservation could not be settled")
	}
	return nil
}

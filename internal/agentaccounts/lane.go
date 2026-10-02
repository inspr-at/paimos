// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"math"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/jackc/pgx/v5"
)

// ValidateLaneCapacity narrows ordinary admission: measured subscription quota
// only, no blind/bootstrap budget or Run now override. Keep for you remains a
// floor even at night and just before reset (AEON-451 decision 4 A).
// Caller already owns pairing/account locks and validates the exact reservation.
func ValidateLaneCapacity(ctx context.Context, tx pgx.Tx, runID, accountID string, now time.Time) error {
	a, err := lockAccount(ctx, tx, accountID)
	if err != nil {
		return err
	}
	if a.BillingMode != "subscription" {
		return fail(409, "lane requires known subscription billing")
	}
	var override string
	if err = tx.QueryRow(ctx, `SELECT capacity_override FROM agent_runs WHERE id=$1`, runID).Scan(&override); err != nil {
		return err
	}
	if override != "" {
		return fail(409, "lane cannot bypass quota reserve")
	}
	s, err := routingSchedule(ctx, tx, a)
	if err != nil {
		return err
	}
	learned, err := loadLearning(ctx, tx, a.ID)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT w.capacity_kind,w.capacity_bucket,w.starts_at,w.ends_at,w.capacity_read_at,w.capacity_source,w.allowance,w.used,w.reserved,w.capacity_allowed,w.capacity_retired
 FROM account_reservations r JOIN account_allowance_windows w ON w.id=r.window_id WHERE r.run_id=$1 AND r.state='active' ORDER BY w.id`, runID)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var kind, source, bucket *string
		var start, end time.Time
		var read *time.Time
		var allowance, used, reserved int64
		var allowed, retired bool
		if err = rows.Scan(&kind, &bucket, &start, &end, &read, &source, &allowance, &used, &reserved, &allowed, &retired); err != nil {
			return err
		}
		// Manual windows may further narrow quota, but cannot establish vendor truth.
		if read == nil {
			continue
		}
		if kind == nil || source == nil || bucket == nil || *kind == "refresh" || *kind == "blind" || *source == "estimate" || !allowed || retired || read.After(now) || now.Sub(*read) > 10*time.Minute || now.Before(start) || !now.Before(end) {
			return fail(409, "lane quota reading unknown or stale")
		}
		metric := learned.metric(capacity.Reading{WindowKind: *kind, Bucket: *bucket, WindowMinutes: int(end.Sub(start) / time.Minute)}, now, s, "")
		// An off reserve cannot authorize the lane to consume the person's reserve.
		floor := s.ReserveLevel(metric.AutoReserve)
		if floor <= 0 {
			floor = capacity.AutoReserve
		}
		reserve := int64(math.Ceil(float64(allowance) * floor / 100))
		if allowance-used-reserved < reserve {
			return fail(409, "lane must retain quota reserve")
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count == 0 {
		return fail(409, "lane requires measured quota")
	}
	return nil
}

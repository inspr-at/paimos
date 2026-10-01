// SPDX-License-Identifier: AGPL-3.0-only

package agentactivity

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// InvalidReport carries only a fixed public validation message.
type InvalidReport struct{ Message string }

func (e *InvalidReport) Error() string { return e.Message }

func LoadMode(ctx context.Context, tx pgx.Tx) (string, error) {
	var mode string
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT agent_activity_mode FROM harness_settings),'agent_summary')`).Scan(&mode)
	return mode, err
}

func LockPolicy(ctx context.Context, tx pgx.Tx, shared bool) error {
	lock := "pg_advisory_xact_lock"
	if shared {
		lock += "_shared"
	}
	_, err := tx.Exec(ctx, `SELECT `+lock+`(hashtextextended(current_setting('aeon.tenant_id')||':agent_activity',0))`)
	return err
}

// The worker row is locked by the caller. Policy is tenant-scoped through RLS;
// timestamps and validation use the same database clock as session liveness.
func Report(ctx context.Context, tx pgx.Tx, id, mode string, doing *string, at *time.Time, tool *Activity) error {
	if mode == Off {
		return nil
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	validTime := func(at time.Time) bool { return !at.IsZero() && !at.After(now.Add(30*time.Second)) }
	if doing != nil && mode == Summary {
		clean, valid := CleanSummary(*doing)
		if !valid {
			return &InvalidReport{Message: "doing must be a public summary of at most 60 characters"}
		}
		when := now
		if at != nil {
			when = *at
		}
		if !validTime(when) {
			return &InvalidReport{Message: "invalid activity time"}
		}
		if when.After(now) {
			when = now
		}
		if now.Sub(when) < Fresh {
			if _, err := tx.Exec(ctx, `UPDATE harness_sessions SET doing=$2,doing_at=$3 WHERE id=$1 AND (doing_at IS NULL OR doing_at <= $3)`, id, clean, when); err != nil {
				return err
			}
		}
	}
	if tool != nil {
		if tool.Source != "auto" || !ValidAuto(tool.Text) || !validTime(tool.At) {
			return &InvalidReport{Message: "invalid sanitized tool activity"}
		}
		if tool.At.After(now) {
			tool.At = now
		}
		if now.Sub(tool.At) < Fresh {
			if _, err := tx.Exec(ctx, `UPDATE harness_sessions SET tool_activity=$2,tool_activity_at=$3 WHERE id=$1 AND (tool_activity_at IS NULL OR tool_activity_at <= $3)`, id, tool.Text, tool.At); err != nil {
				return err
			}
		}
	}
	return nil
}

func Record(ctx context.Context, tx pgx.Tx, tenantID, id string, a *Activity) error {
	if a == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO harness_current_activity(tenant_id,session_id,text,source,at)
		SELECT $1,$2,$3,$4,clock_timestamp() WHERE NOT EXISTS (
		SELECT 1 FROM (SELECT text,source FROM harness_current_activity WHERE session_id=$2 ORDER BY id DESC LIMIT 1) last WHERE text=$3 AND source=$4)`, tenantID, id, a.Text, a.Source)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM harness_current_activity WHERE session_id=$1 AND id NOT IN (SELECT id FROM harness_current_activity WHERE session_id=$1 ORDER BY id DESC LIMIT 20)`, id)
	return err
}

// ReportAttached is called after approval, process, daemon and sequence checks.
// Recheck the attributed principal and live generation under its row lock.
func ReportAttached(ctx context.Context, tx pgx.Tx, tenantID, principalID, id string, doing *string, tool *Activity) error {
	if err := LockPolicy(ctx, tx, true); err != nil {
		return err
	}
	var mode string
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT agent_activity_mode FROM harness_settings),'agent_summary') FROM harness_sessions WHERE id=$1 AND agent_principal_id=$2 AND stopped_at IS NULL AND archived_at IS NULL FOR UPDATE`, id, principalID).Scan(&mode); err != nil {
		return err
	}
	if err := Report(ctx, tx, id, mode, doing, nil, tool); err != nil {
		return err
	}
	var summary, observed *string
	var summaryAt, observedAt *time.Time
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT doing,doing_at,tool_activity,tool_activity_at,clock_timestamp() FROM harness_sessions WHERE id=$1`, id).Scan(&summary, &summaryAt, &observed, &observedAt, &now); err != nil {
		return err
	}
	return Record(ctx, tx, tenantID, id, Current(summary, summaryAt, observed, observedAt, mode, now))
}

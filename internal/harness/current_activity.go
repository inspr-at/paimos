// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/agentactivity"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func activityMode(ctx context.Context, tx pgx.Tx) (string, error) {
	var mode string
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT agent_activity_mode FROM harness_settings),'agent_summary')`).Scan(&mode)
	return mode, err
}

func lockActivityPolicy(ctx context.Context, tx pgx.Tx, shared bool) error {
	lock := "pg_advisory_xact_lock"
	if shared {
		lock += "_shared"
	}
	_, err := tx.Exec(ctx, `SELECT `+lock+`(hashtextextended(current_setting('aeon.tenant_id')||':agent_activity',0))`)
	return err
}

func (m *Module) getActivityMode(r *http.Request, tx pgx.Tx, _ tenant.Principal) (any, error) {
	mode, err := activityMode(r.Context(), tx)
	return map[string]string{"mode": mode}, err
}

func (m *Module) putActivityMode(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Mode string `json:"mode"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !agentactivity.Mode(in.Mode) {
		return nil, workorders.Fail(400, "invalid agent activity mode")
	}
	if err := lockActivityPolicy(r.Context(), tx, false); err != nil {
		return nil, err
	}
	_, err := tx.Exec(r.Context(), `INSERT INTO harness_settings(tenant_id,agent_activity_mode) VALUES($1,$2)
		ON CONFLICT (tenant_id) DO UPDATE SET agent_activity_mode=EXCLUDED.agent_activity_mode,updated_at=now()`, p.TenantID, in.Mode)
	return map[string]string{"mode": in.Mode}, err
}

func projectActivity(s *Session, now time.Time) {
	s.CurrentActivity = nil
	if s.AgentActivityMode != agentactivity.Summary {
		s.ActivityNote = nil
	}
	if s.AgentActivityMode == agentactivity.Off {
		return
	}
	if s.AgentActivityMode == agentactivity.Summary && s.doing != nil && s.doingAt != nil && now.Sub(*s.doingAt) < agentactivity.Fresh {
		s.CurrentActivity = &agentactivity.Activity{Text: *s.doing, Source: "agent", At: *s.doingAt}
	} else if s.toolActivity != nil && s.toolActivityAt != nil {
		s.CurrentActivity = &agentactivity.Activity{Text: *s.toolActivity, Source: "auto", At: *s.toolActivityAt}
	}
}

// The worker row is locked by the caller. Policy is tenant-scoped through RLS;
// timestamps and validation use the same database clock as session liveness.
func reportActivity(ctx context.Context, tx pgx.Tx, s Session, doing *string, at *time.Time, tool *agentactivity.Activity) error {
	if s.AgentActivityMode == agentactivity.Off {
		return nil
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	validTime := func(at time.Time) bool { return !at.IsZero() && !at.After(now.Add(30*time.Second)) }
	if doing != nil && s.AgentActivityMode == agentactivity.Summary {
		clean, valid := agentactivity.CleanSummary(*doing)
		if !valid {
			return workorders.Fail(400, "doing must be a public summary of at most 60 characters")
		}
		when := now
		if at != nil {
			when = *at
		}
		if !validTime(when) {
			return workorders.Fail(400, "invalid activity time")
		}
		if when.After(now) {
			when = now
		}
		if now.Sub(when) < agentactivity.Fresh {
			if _, err := tx.Exec(ctx, `UPDATE harness_sessions SET doing=$2,doing_at=$3 WHERE id=$1 AND (doing_at IS NULL OR doing_at <= $3)`, s.ID, clean, when); err != nil {
				return err
			}
		}
	}
	if tool != nil {
		if tool.Source != "auto" || !agentactivity.ValidAuto(tool.Text) || !validTime(tool.At) {
			return workorders.Fail(400, "invalid sanitized tool activity")
		}
		if tool.At.After(now) {
			tool.At = now
		}
		if now.Sub(tool.At) < agentactivity.Fresh {
			if _, err := tx.Exec(ctx, `UPDATE harness_sessions SET tool_activity=$2,tool_activity_at=$3 WHERE id=$1 AND (tool_activity_at IS NULL OR tool_activity_at <= $3)`, s.ID, tool.Text, tool.At); err != nil {
				return err
			}
		}
	}
	return nil
}

func recordCurrentActivity(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session) error {
	a := s.CurrentActivity
	if a == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO harness_current_activity(tenant_id,session_id,text,source,at)
		SELECT $1,$2,$3,$4,clock_timestamp() WHERE NOT EXISTS (
		SELECT 1 FROM (SELECT text,source FROM harness_current_activity WHERE session_id=$2 ORDER BY id DESC LIMIT 1) last WHERE text=$3 AND source=$4)`, p.TenantID, s.ID, a.Text, a.Source)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM harness_current_activity WHERE session_id=$1 AND id NOT IN (SELECT id FROM harness_current_activity WHERE session_id=$1 ORDER BY id DESC LIMIT 20)`, s.ID)
	return err
}

func currentActivityHistory(ctx context.Context, tx pgx.Tx, s *Session) error {
	if s.AgentActivityMode == agentactivity.Off {
		s.ActivityHistory = nil
		return nil
	}
	if s.AgentActivityMode == agentactivity.Tool {
		s.ActivityHistory = nil
	}
	rows, err := tx.Query(ctx, `SELECT text,source,at FROM harness_current_activity WHERE session_id=$1 AND ($2='agent_summary' OR source='auto') ORDER BY id DESC LIMIT 20`, s.ID, s.AgentActivityMode)
	if err != nil {
		return err
	}
	defer rows.Close()
	s.CurrentActivityHistory = []agentactivity.Activity{}
	for rows.Next() {
		var a agentactivity.Activity
		if err := rows.Scan(&a.Text, &a.Source, &a.At); err != nil {
			return err
		}
		s.CurrentActivityHistory = append(s.CurrentActivityHistory, a)
	}
	return rows.Err()
}

// ReportAttachedActivity is used only after the pairing handler has verified
// the active approval, process binding, daemon capability and poll sequence.
// It rechecks the attributed principal and live generation under a row lock.
func ReportAttachedActivity(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, doing *string, tool *agentactivity.Activity) error {
	if err := lockActivityPolicy(ctx, tx, true); err != nil {
		return err
	}
	s, err := scanSession(tx.QueryRow(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=$1 AND agent_principal_id=$2 AND stopped_at IS NULL AND archived_at IS NULL FOR UPDATE`, id, p.ID))
	if err != nil {
		return err
	}
	if err = reportActivity(ctx, tx, s, doing, nil, tool); err != nil {
		return err
	}
	s, err = scanSession(tx.QueryRow(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=$1`, id))
	if err != nil {
		return err
	}
	return recordCurrentActivity(ctx, tx, p, s)
}

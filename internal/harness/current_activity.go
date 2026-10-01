// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/agentactivity"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func activityMode(ctx context.Context, tx pgx.Tx) (string, error) {
	return agentactivity.LoadMode(ctx, tx)
}
func lockActivityPolicy(ctx context.Context, tx pgx.Tx, shared bool) error {
	return agentactivity.LockPolicy(ctx, tx, shared)
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
	s.CurrentActivity = agentactivity.Current(s.doing, s.doingAt, s.toolActivity, s.toolActivityAt, s.AgentActivityMode, now)
	if s.AgentActivityMode != agentactivity.Summary {
		s.ActivityNote = nil
	}
}

func reportActivity(ctx context.Context, tx pgx.Tx, s Session, doing *string, at *time.Time, tool *agentactivity.Activity) error {
	err := agentactivity.Report(ctx, tx, s.ID, s.AgentActivityMode, doing, at, tool)
	var invalid *agentactivity.InvalidReport
	if errors.As(err, &invalid) {
		return workorders.Fail(400, invalid.Message)
	}
	return err
}

func recordCurrentActivity(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session) error {
	return agentactivity.Record(ctx, tx, p.TenantID, s.ID, s.CurrentActivity)
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

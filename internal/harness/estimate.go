// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

func parseETA(raw json.RawMessage) (*time.Time, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil || text == "" {
		return nil, true, workorders.Fail(400, "ETA must be an RFC3339 timestamp")
	}
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		at, err = time.Parse(time.RFC3339Nano, text)
	}
	if err != nil {
		return nil, true, workorders.Fail(400, "ETA must be an RFC3339 timestamp")
	}
	return &at, true, nil
}

func parseProgress(raw json.RawMessage) (*int, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var number json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&number); err != nil || dec.More() {
		return nil, true, workorders.Fail(400, "progress must be an integer from 0 to 100")
	}
	value, err := number.Int64()
	if err != nil || value < 0 || value > 100 {
		return nil, true, workorders.Fail(400, "progress must be an integer from 0 to 100")
	}
	out := int(value)
	return &out, true, nil
}

func applyEstimate(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, readyRaw, liveRaw, progressRaw json.RawMessage) (Session, error) {
	if readyRaw == nil && liveRaw == nil && progressRaw == nil {
		return s, nil
	}
	readyAt, readySet, err := parseETA(readyRaw)
	if err != nil {
		return s, err
	}
	liveAt, liveSet, err := parseETA(liveRaw)
	if err != nil {
		return s, err
	}
	progress, progressSet, err := parseProgress(progressRaw)
	if err != nil {
		return s, err
	}
	if s.TicketNodeID == nil {
		return s, workorders.Fail(400, "ETA needs a bound ticket")
	}
	switch s.Role {
	case "worker":
		if liveSet {
			return s, workorders.Fail(400, "workers report the ready ETA and percent done")
		}
	case "coordinator":
		if readySet || progressSet {
			return s, workorders.Fail(400, "coordinators report the live ETA")
		}
		if err := writeTicketLive(ctx, tx, p.TenantID, *s.TicketNodeID, p.ID, liveAt); err != nil {
			return s, err
		}
	default:
		return s, workorders.Fail(400, "ETA needs a worker or coordinator session")
	}
	updated, err := scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET
		eta_ready_at = CASE WHEN $2::bool THEN $3::timestamptz ELSE eta_ready_at END,
		eta_live_at = CASE WHEN $4::bool THEN $5::timestamptz ELSE eta_live_at END,
		progress_pct = CASE WHEN $6::bool THEN $7::smallint ELSE progress_pct END,
		eta_reported_at = CASE
			WHEN (CASE WHEN $2::bool THEN $3::timestamptz ELSE eta_ready_at END) IS NULL
			 AND (CASE WHEN $4::bool THEN $5::timestamptz ELSE eta_live_at END) IS NULL
			 AND (CASE WHEN $6::bool THEN $7::smallint ELSE progress_pct END) IS NULL
			THEN NULL ELSE clock_timestamp() END
		WHERE id=$1 RETURNING `+sessionColumns, s.ID, readySet, readyAt, liveSet, liveAt, progressSet, progress))
	if err != nil {
		return s, err
	}
	updated.StateEvidence = s.StateEvidence
	return updated, nil
}

func writeTicketLive(ctx context.Context, tx pgx.Tx, tenantID, nodeID, by string, at *time.Time) error {
	if at == nil {
		_, err := tx.Exec(ctx, `DELETE FROM ticket_live_eta WHERE node_id=$1`, nodeID)
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO ticket_live_eta(tenant_id,node_id,eta_live_at,reported_at,reported_by)
		VALUES($1,$2,$3,clock_timestamp(),$4)
		ON CONFLICT (tenant_id, node_id) DO UPDATE
		SET eta_live_at=EXCLUDED.eta_live_at, reported_at=EXCLUDED.reported_at, reported_by=EXCLUDED.reported_by`,
		tenantID, nodeID, at, by)
	return err
}

func etaInterval(ctx context.Context, tx pgx.Tx) (time.Duration, error) {
	var mins int
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT interval_minutes FROM eta_settings), 10)`).Scan(&mins); err != nil {
		return 0, err
	}
	if mins < 1 {
		mins = 10
	}
	return time.Duration(mins) * time.Minute, nil
}

func stampSessions(ctx context.Context, tx pgx.Tx, sessions []*Session) error {
	if len(sessions) == 0 {
		return nil
	}
	interval, err := etaInterval(ctx, tx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, s := range sessions {
		stampSessionEta(s, interval, now)
	}
	return nil
}

func stampSessionEta(s *Session, interval time.Duration, now time.Time) {
	if s == nil {
		return
	}
	s.EtaStale = false
	if s.StoppedAt != nil || s.EtaReportedAt == nil {
		return
	}
	if now.Sub(*s.EtaReportedAt) > 2*interval {
		s.EtaStale = true
	}
}

func (m *Module) getEtaInterval(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	mins, err := etaInterval(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	return map[string]int{"interval_minutes": int(mins / time.Minute)}, nil
}

func (m *Module) putEtaInterval(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Minutes int `json:"interval_minutes"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Minutes < 1 || in.Minutes > 240 {
		return nil, workorders.Fail(400, "interval must be from 1 to 240 minutes")
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO eta_settings(tenant_id,interval_minutes) VALUES($1,$2)
		ON CONFLICT (tenant_id) DO UPDATE SET interval_minutes=EXCLUDED.interval_minutes, updated_at=now()`, p.TenantID, in.Minutes); err != nil {
		return nil, err
	}
	return map[string]int{"interval_minutes": in.Minutes}, nil
}

func (m *Module) setLiveEta(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	nodeID := r.PathValue("nodeId")
	if !workorders.UUID(nodeID) {
		return nil, workorders.Fail(400, "invalid node id")
	}
	var in struct {
		Live json.RawMessage `json:"eta_live_at"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Live == nil {
		return nil, workorders.Fail(400, "eta_live_at is required")
	}
	at, _, err := parseETA(in.Live)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	var slug, projectID string
	err = tx.QueryRow(ctx, `SELECT k.slug, coalesce(n.project_id::text,'') FROM nodes n
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.id=$1 AND n.deleted_at IS NULL`, nodeID).Scan(&slug, &projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, workorders.Fail(404, "node not found")
	}
	if err != nil {
		return nil, err
	}
	if slug != "ticket" && slug != "task" {
		return nil, workorders.Fail(400, "live ETA is set on a ticket or task")
	}
	if !workorders.UUID(projectID) {
		return nil, workorders.Fail(400, "node has no project")
	}
	var sessionID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM harness_sessions
		WHERE agent_principal_id=$1 AND role='coordinator' AND stopped_at IS NULL AND project_id=$2
		ORDER BY (ticket_node_id IS NOT DISTINCT FROM $3::uuid) DESC, heartbeat_at DESC NULLS LAST
		LIMIT 1`, p.ID, projectID, nodeID).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, workorders.Fail(403, "live ETA is reported by the coordinator")
	}
	if err != nil {
		return nil, err
	}
	s, err := load(ctx, tx, projectID, sessionID, true)
	if err != nil {
		return nil, err
	}
	if err := proof(s, r, p); err != nil {
		return nil, err
	}
	if err := writeTicketLive(ctx, tx, p.TenantID, nodeID, p.ID, at); err != nil {
		return nil, err
	}
	if at != nil {
		if _, err := tx.Exec(ctx, `UPDATE harness_sessions SET eta_reported_at=clock_timestamp(),
			eta_live_at=CASE WHEN ticket_node_id=$2 THEN $3 ELSE eta_live_at END
			WHERE id=$1`, s.ID, nodeID, at); err != nil {
			return nil, err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE harness_sessions SET
			eta_live_at=CASE WHEN ticket_node_id=$2 THEN NULL ELSE eta_live_at END,
			eta_reported_at=CASE WHEN ticket_node_id=$2 AND eta_ready_at IS NULL AND progress_pct IS NULL THEN NULL ELSE eta_reported_at END
			WHERE id=$1`, s.ID, nodeID); err != nil {
		return nil, err
	}
	return eta.One(ctx, tx, nodeID)
}

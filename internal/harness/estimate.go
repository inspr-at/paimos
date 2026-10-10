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

	"github.com/inspr-at/paimos/internal/authz"
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
	if !eta.Allowed(at, time.Now()) {
		return nil, true, workorders.Fail(400, "ETA must be within 30 days overdue and 365 days ahead")
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
			return s, workorders.Fail(400, "coordinator beats may carry only --eta-live; --progress and --eta-ready are not accepted: progress is derived from the coordinator's workers")
		}
		if err := writeTicketLive(ctx, tx, p, *s.TicketNodeID, s.ProjectID, liveAt); err != nil {
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

func assertTicketInProject(ctx context.Context, tx pgx.Tx, nodeID, projectID string) error {
	var current string
	// A project move locks this row FOR UPDATE before it changes parent_id.
	// FOR SHARE waits for that lock, then reads the committed project, and stays
	// held through the live ETA write so the move cannot commit in between.
	err := tx.QueryRow(ctx, `SELECT coalesce(n.project_id::text,'')
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug IN ('work','ticket','task')
		  AND (k.slug<>'work' OR aeon_work_leaf(n.id))
		FOR SHARE OF n`, nodeID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && current != projectID) {
		return workorders.Fail(403, "live ETA stays with the ticket's current project")
	}
	return err
}

func writeTicketLive(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodeID, projectID string, at *time.Time) error {
	if err := assertTicketInProject(ctx, tx, nodeID, projectID); err != nil {
		return err
	}
	// Both coordinator heartbeats and direct writes hold the tree fence.
	// Recheck the current grant against the locked target before mutating ETA.
	if err := authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: projectID}); err != nil {
		return err
	}
	if at == nil {
		_, err := tx.Exec(ctx, `DELETE FROM ticket_live_eta WHERE node_id=$1`, nodeID)
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO ticket_live_eta(tenant_id,node_id,eta_live_at,reported_at,reported_by)
		VALUES($1,$2,$3,clock_timestamp(),$4)
		ON CONFLICT (tenant_id, node_id) DO UPDATE
		SET eta_live_at=EXCLUDED.eta_live_at, reported_at=EXCLUDED.reported_at, reported_by=EXCLUDED.reported_by`,
		p.TenantID, nodeID, at, p.ID)
	return err
}

// syncBoundLive copies a live estimate onto the session only when that session
// is bound to the same ticket. Another ticket keeps its own reported_at, and
// clearing the estimate clears the session timestamp when nothing remains.
func syncBoundLive(ctx context.Context, tx pgx.Tx, sessionID, nodeID string, at *time.Time) error {
	if at != nil {
		_, err := tx.Exec(ctx, `UPDATE harness_sessions SET eta_live_at=$3, eta_reported_at=clock_timestamp()
			WHERE id=$1 AND ticket_node_id=$2`, sessionID, nodeID, at)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE harness_sessions SET eta_live_at=NULL,
		eta_reported_at=CASE WHEN eta_ready_at IS NULL AND progress_pct IS NULL THEN NULL ELSE clock_timestamp() END
		WHERE id=$1 AND ticket_node_id=$2`, sessionID, nodeID)
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

func (m *Module) stampSessions(ctx context.Context, tx pgx.Tx, sessions []*Session) error {
	if len(sessions) == 0 {
		return nil
	}
	interval, err := etaInterval(ctx, tx)
	if err != nil {
		return err
	}
	// eta_reported_at is clock_timestamp(). The same database clock decides
	// staleness; the API host's time.Now() does not.
	now, err := m.ownershipNow(ctx, tx)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		stampSessionEta(s, interval, now)
		*s = stampPause(*s, now)
	}
	if err := stampCoordinatorProgress(ctx, tx, sessions); err != nil {
		return err
	}
	return stampMoveRights(ctx, tx, sessions)
}

// heartbeatReceipt preserves authoritative write fields, including finished
// and process_observed_at. Missing optional projections are unknown, never a
// fresh estimate or a cached coordinator percent (which may predate its workers).
// The unknown-projection warning is declared in the pinned reporter contract.
func heartbeatReceipt(s Session) heartbeatResponse {
	if s.Role == "coordinator" && s.StoppedAt == nil && s.ArchivedAt == nil {
		s.ProgressPct = nil
	}
	// The reporting interval is constrained to at most 240 minutes. Beyond
	// two maximum intervals the estimate is certainly stale; anything newer
	// has unknown freshness until a read loads the setting. Use the committed
	// beat's database timestamp, never the API host's clock.
	s.EtaStale = s.StoppedAt == nil && s.EtaReportedAt != nil && s.HeartbeatAt != nil &&
		s.HeartbeatAt.Sub(*s.EtaReportedAt) > 480*time.Minute
	return heartbeatResponse{reporterSession(s), []EstimateWarning{{
		Code: "projection_unavailable",
		Hint: "Heartbeat accepted; ETA freshness, coordinator progress and estimate guidance are unknown on this receipt. Read session status separately; a failed read is not a failed heartbeat.",
	}}}
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
		Minutes  int  `json:"interval_minutes"`
		Expected *int `json:"expected_interval_minutes,omitempty"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Minutes < 1 || in.Minutes > 240 {
		return nil, workorders.Fail(400, "interval must be from 1 to 240 minutes")
	}
	if in.Expected != nil && (*in.Expected < 1 || *in.Expected > 240) {
		return nil, workorders.Fail(400, "expected value outside allowed range")
	}
	if err := lockAgentWorkSettings(r.Context(), tx, p); err != nil {
		return nil, err
	}
	before, err := etaInterval(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	if in.Expected != nil && *in.Expected != int(before/time.Minute) {
		return nil, workorders.Fail(409, "agent settings changed; reload before undoing")
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO eta_settings(tenant_id,interval_minutes) VALUES($1,$2)
		ON CONFLICT (tenant_id) DO UPDATE SET interval_minutes=EXCLUDED.interval_minutes, updated_at=now()`, p.TenantID, in.Minutes); err != nil {
		return nil, err
	}
	err = recordAgentWorkSetting(r.Context(), tx, p, "interval_minutes", int(before/time.Minute), in.Minutes)
	return map[string]int{"interval_minutes": in.Minutes}, err
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
	var leaf bool
	err = tx.QueryRow(ctx, `SELECT k.slug, coalesce(n.project_id::text,''),
		(k.slug<>'work' OR aeon_work_leaf(n.id)) FROM nodes n
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.id=$1 AND n.deleted_at IS NULL`, nodeID).Scan(&slug, &projectID, &leaf)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, workorders.Fail(404, "node not found")
	}
	if err != nil {
		return nil, err
	}
	if (slug != "work" && slug != "ticket" && slug != "task") || !leaf {
		return nil, workorders.Fail(400, "live ETA is set on a work leaf or legacy ticket or task")
	}
	if !workorders.UUID(projectID) {
		return nil, workorders.Fail(400, "node has no project")
	}
	var sessionID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM harness_sessions
		WHERE agent_principal_id=$1 AND role='coordinator' AND stopped_at IS NULL AND project_id=$2
		  AND lease_digest=$3
		ORDER BY (ticket_node_id IS NOT DISTINCT FROM $4::uuid) DESC, heartbeat_at DESC NULLS LAST
		LIMIT 1`, p.ID, projectID, digest("lease", r.Header.Get("X-Aeon-Worker-Lease")), nodeID).Scan(&sessionID)
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
	if err := writeTicketLive(ctx, tx, p, nodeID, s.ProjectID, at); err != nil {
		return nil, err
	}
	if err := syncBoundLive(ctx, tx, s.ID, nodeID, at); err != nil {
		return nil, err
	}
	return eta.One(ctx, tx, nodeID)
}

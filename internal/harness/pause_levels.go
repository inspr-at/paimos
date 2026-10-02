// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func ValidPauseLevel(level string) bool {
	return level == "stop_now" || level == "pause_quickly" || level == "pause" || level == "wrap_up"
}

func cooperativePause(s Session) bool {
	return has(s, "inbox") || s.Management == "unmanaged" && has(s, "pause")
}

func supportedPauseLevels(s Session) []string {
	if !cooperativePause(s) {
		return []string{"stop_now"}
	}
	return []string{"stop_now", "pause_quickly", "pause", "wrap_up"}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// A missing estimate never implies that finishing will fit. The caller supplies
// only a fresh estimate, and both scheduling clocks belong to the database.
func leavingLevel(now, deadline time.Time, remaining *time.Duration, inbox bool) (string, time.Time) {
	budget := deadline.Sub(now)
	if budget <= 0 {
		return "stop_now", now
	}
	if !inbox {
		return "stop_now", deadline
	}
	if budget <= 2*time.Minute {
		return "pause_quickly", now
	}
	if remaining != nil && *remaining >= 0 && *remaining < 10*time.Minute && *remaining <= budget {
		return "wrap_up", now
	}
	start := deadline.Add(-10 * time.Minute)
	if start.Before(now) {
		start = now
	}
	return "pause", start
}

func stampPause(s Session, now time.Time) Session {
	if s.Pause != nil {
		next := *s.Pause
		next.Deliver = (next.State == "requested" || next.State == "planned" || next.StopRequested) && (next.StartsAt == nil || !next.StartsAt.After(now))
		next.WakeInMS = 0
		if (next.State == "requested" || next.State == "planned") && ValidPauseLevel(next.Level) && !next.StopRequested {
			wake := next.DeadlineAt
			if next.StartsAt != nil && next.StartsAt.After(now) {
				wake = minTime(wake, *next.StartsAt)
			}
			quick := next.DeadlineAt.Add(-2 * time.Minute)
			if next.Level != "stop_now" && next.Level != "pause_quickly" && quick.After(now) {
				wake = minTime(wake, quick)
			}
			if wake.After(now) {
				next.WakeInMS = (wake.Sub(now) + time.Millisecond - 1).Milliseconds()
			}
		}
		s.Pause = &next
	}
	return s
}

func canonicalPausePerson(ctx context.Context, tx pgx.Tx, p *tenant.Principal) error {
	return tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE id=$1 AND kind='person'`, p.ID).Scan(&p.ID)
}

func resolvePauseDefault(ctx context.Context, tx pgx.Tx, p tenant.Principal, in *pauseRequest) error {
	if in.Level != "" {
		return nil
	}
	in.Level = "pause"
	if p.Kind != tenant.Person {
		return nil
	}
	if err := canonicalPausePerson(ctx, tx, &p); err != nil {
		return err
	}
	return tx.QueryRow(ctx, `SELECT coalesce((SELECT default_level FROM person_pause_settings WHERE person_id=$1),'pause')`, p.ID).Scan(&in.Level)
}

// The caller holds the session lock. An already delivered/accepted request never
// retreats to a weaker level or a later start. Escalation changes the durable
// request, and adapters use the level in their input receipt identity.
func advancePause(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session) (Session, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return s, err
	}
	if s.Pause == nil || (s.Pause.State != "requested" && s.Pause.State != "planned") || s.Pause.StopRequested || !s.Pause.DeadlineAt.After(now) {
		return stampPause(s, now), nil
	}
	next := *s.Pause
	if next.LeavingID != "" && next.State == "requested" && next.StartsAt != nil && next.StartsAt.After(now) {
		var remaining *time.Duration
		interval, err := etaInterval(ctx, tx)
		if err != nil {
			return s, err
		}
		if s.EtaReadyAt != nil && s.EtaReportedAt != nil && now.Sub(*s.EtaReportedAt) >= 0 && now.Sub(*s.EtaReportedAt) <= 2*interval {
			d := max(0, s.EtaReadyAt.Sub(now))
			remaining = &d
		}
		level, start := leavingLevel(now, next.DeadlineAt, remaining, cooperativePause(s))
		next.Level, next.StartsAt = level, &start
	}
	if (next.Level == "pause" || next.Level == "wrap_up") && next.DeadlineAt.Sub(now) <= 2*time.Minute {
		next.Level = "pause_quickly"
		next.StartsAt = &now
	}
	changed := next.Level != s.Pause.Level || !sameTime(next.StartsAt, s.Pause.StartsAt)
	if changed {
		var err error
		s, err = savePause(ctx, tx, p, s, next, "pause_level_changed")
		if err != nil {
			return s, err
		}
		payload, _ := json.Marshal(SessionRequestPayload{Pause: true, Level: next.Level, Note: next.Note})
		if _, err = tx.Exec(ctx, `UPDATE harness_controls SET request_payload=$2::jsonb WHERE id=$1 AND state<>'completed'`, next.ControlID, string(payload)); err != nil {
			return s, err
		}
	}
	if next.Level == "pause_quickly" && next.InterruptControlID == "" && s.Management == "managed" && has(s, "interrupt") {
		payload, _ := json.Marshal(SessionRequestPayload{Level: "pause_quickly"})
		var ownership any
		if has(s, managedControlCapability) {
			ownership = s.ProcessOwnership
		}
		raw, _ := json.Marshal(ownership)
		c, err := scanControl(tx.QueryRow(ctx, `INSERT INTO harness_controls(tenant_id,session_id,kind,request_payload,sequence,requested_by_principal_id,expected_generation,expected_ownership)
 SELECT $1,$2,'interrupt',$3::jsonb,coalesce(max(sequence),0)+1,$4,$2,nullif($5::jsonb,'null'::jsonb) FROM harness_controls WHERE session_id=$2 RETURNING `+controlColumns, p.TenantID, s.ID, string(payload), next.RequestedBy, string(raw)))
		if err != nil {
			return s, err
		}
		if err = record(ctx, tx, p, s, "control_requested", nil, c); err != nil {
			return s, err
		}
		next.InterruptControlID = c.ID
		s, err = savePause(ctx, tx, p, s, next, "pause_interrupt_requested")
		if err != nil {
			return s, err
		}
	}
	return stampPause(s, now), nil
}

func sameTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

// Stop now remains an ordinary stop control. Its executor must own the exact
// generation and process lifetime; the API never signals a supplied PID and
// never reports a stop request as confirmed process exit.
func stopPausedWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, reason string) (Session, error) {
	if s.Pause == nil || s.Pause.StopRequested || s.StoppedAt != nil || s.ArchivedAt != nil {
		return s, nil
	}
	next := *s.Pause
	if next.State == "requested" || next.State == "planned" {
		var err error
		s, err = cancelPause(ctx, tx, p, s, reason)
		if err != nil {
			return s, err
		}
		next = *s.Pause
	}
	payload, _ := json.Marshal(SessionRequestPayload{StopNow: true, Level: "stop_now"})
	ownership, _ := json.Marshal(s.ProcessOwnership)
	c, err := scanControl(tx.QueryRow(ctx, `INSERT INTO harness_controls(tenant_id,session_id,kind,request_payload,sequence,requested_by_principal_id,expected_generation,expected_ownership)
 SELECT $1,$2,'stop',$3::jsonb,coalesce(max(sequence),0)+1,$4,$2,nullif($5::jsonb,'null'::jsonb) FROM harness_controls WHERE session_id=$2 RETURNING `+controlColumns,
		p.TenantID, s.ID, string(payload), next.RequestedBy, string(ownership)))
	if err != nil {
		return s, err
	}
	if err = record(ctx, tx, p, s, "control_requested", nil, c); err != nil {
		return s, err
	}
	next.Level, next.StopControlID, next.StopRequested, next.Deliver = "stop_now", c.ID, true, true
	return savePause(ctx, tx, p, s, next, "pause_stop_requested")
}

func cancelPause(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, reason string) (Session, error) {
	if s.Pause == nil || s.Pause.State == "paused" || s.Pause.State == "resume_requested" || s.Pause.State == "resumed" {
		return s, nil
	}
	next := *s.Pause
	ids := []string{next.ControlID}
	if next.InterruptControlID != "" {
		ids = append(ids, next.InterruptControlID)
	}
	if next.StopControlID != "" {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM harness_controls WHERE id=$1`, next.StopControlID).Scan(&state); err != nil {
			return s, err
		}
		if state == "claimed" {
			return s, workorders.Fail(409, "stop already in flight")
		}
		ids = append(ids, next.StopControlID)
	}
	for _, id := range ids {
		before, err := scanControl(tx.QueryRow(ctx, `SELECT `+controlColumns+` FROM harness_controls WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return s, err
		}
		if before.State == "completed" {
			continue
		}
		after, err := scanControl(tx.QueryRow(ctx, `UPDATE harness_controls SET state='completed',outcome='rejected',reason=$2,claimed_at=coalesce(claimed_at,clock_timestamp()),completed_at=clock_timestamp() WHERE id=$1 RETURNING `+controlColumns, id, reason))
		if err != nil {
			return s, err
		}
		if err = record(ctx, tx, p, s, "control_completed", before, after); err != nil {
			return s, err
		}
	}
	next.State, next.StopRequested, next.Deliver = "cancelled", false, false
	return savePause(ctx, tx, p, s, next, "pause_cancelled")
}

func (m *Module) getPauseSettings(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "person required")
	}
	if err := canonicalPausePerson(r.Context(), tx, &p); err != nil {
		return nil, err
	}
	in := pauseRequest{}
	if err := resolvePauseDefault(r.Context(), tx, p, &in); err != nil {
		return nil, err
	}
	return map[string]string{"default_level": in.Level}, nil
}

func (m *Module) putPauseSettings(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "person required")
	}
	if err := canonicalPausePerson(r.Context(), tx, &p); err != nil {
		return nil, err
	}
	var in struct {
		Level string `json:"default_level"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !ValidPauseLevel(in.Level) {
		return nil, workorders.Fail(400, "invalid default pause level")
	}
	before, err := m.getPauseSettings(r, tx, p)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO person_pause_settings(tenant_id,person_id,default_level) VALUES($1,$2,$3) ON CONFLICT(tenant_id,person_id) DO UPDATE SET default_level=EXCLUDED.default_level`, p.TenantID, p.ID, in.Level); err != nil {
		return nil, err
	}
	after := map[string]string{"default_level": in.Level}
	if before.(map[string]string)["default_level"] != in.Level {
		err = pausePreferenceEvent(r.Context(), tx, p, "pause_default_changed", before, after)
	}
	return after, err
}

func pausePreferenceEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind string, before, after any) error {
	_, err := events.Append(ctx, tx, p, events.Change{Type: "harness." + kind, Before: before, After: after})
	return err
}

// The advisory lock serializes enable, replace, and cancel even before a
// preference row exists. Session locks then serialize against heartbeat/exit.
func lockPausePerson(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,524))`, p.TenantID+":"+p.ID)
	return err
}

func (m *Module) getLeavingAt(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "person required")
	}
	if err := canonicalPausePerson(r.Context(), tx, &p); err != nil {
		return nil, err
	}
	var id *string
	var deadline *time.Time
	err := tx.QueryRow(r.Context(), `SELECT leaving_request_id::text,leaving_at FROM person_pause_settings WHERE person_id=$1`, p.ID).Scan(&id, &deadline)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	items := []Session{}
	if id != nil {
		rows, err := tx.Query(r.Context(), `SELECT `+sessionColumns+` FROM harness_sessions WHERE pause_record->>'leaving_request_id'=$1 AND owner_principal_id=$2 ORDER BY id`, *id, p.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			s, err := scanSession(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			items = append(items, s)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	ptrs := make([]*Session, len(items))
	for i := range items {
		ptrs[i] = &items[i]
	}
	if len(ptrs) > 0 {
		if err := m.stampSessions(r.Context(), tx, ptrs); err != nil {
			return nil, err
		}
	}
	return map[string]any{"deadline_at": deadline, "request_id": id, "items": items, "stop_in_flight": false}, nil
}

func (m *Module) putLeavingAt(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "person required")
	}
	if err := canonicalPausePerson(r.Context(), tx, &p); err != nil {
		return nil, err
	}
	var in struct {
		Deadline time.Time `json:"deadline_at"`
		Reason   string    `json:"reason"`
		Note     string    `json:"note"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	// Match Postgres timestamp precision before retry comparison and scheduling;
	// otherwise the persisted deadline differs from the same nanosecond input.
	in.Deadline = in.Deadline.UTC().Truncate(time.Microsecond)
	request := pauseRequest{Reason: in.Reason, Note: in.Note}
	if err := request.validate(); err != nil {
		return nil, err
	}
	ctx := r.Context()
	if err := lockPausePerson(ctx, tx, p); err != nil {
		return nil, err
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if !in.Deadline.After(now) || in.Deadline.Sub(now) > 24*time.Hour {
		return nil, workorders.Fail(400, "leaving deadline must be in the next 24 hours")
	}
	old, err := m.getLeavingAt(r, tx, p)
	if err != nil {
		return nil, err
	}
	if deadline := old.(map[string]any)["deadline_at"].(*time.Time); deadline != nil && deadline.Equal(in.Deadline) {
		return old, nil
	}
	cancelled, err := m.cancelLeavingAt(r, tx, p)
	if err != nil {
		return nil, err
	}
	if cancelled.(map[string]any)["stop_in_flight"].(bool) {
		return nil, workorders.Fail(409, "stop already in flight")
	}
	// This is all *my* running work, irrespective of admin privileges. Project
	// permissions are checked again in the transaction; RLS hides other projects.
	allowed, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE owner_principal_id=$1 AND stopped_at IS NULL AND archived_at IS NULL ORDER BY id FOR UPDATE`, p.ID)
	if err != nil {
		return nil, err
	}
	items := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if allowed("harness.control", s.ProjectID) {
			items = append(items, s)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(items) > 200 {
		return nil, workorders.Fail(409, "leaving mode supports at most 200 running sessions")
	}
	var id string
	if err = tx.QueryRow(ctx, `INSERT INTO person_pause_settings(tenant_id,person_id,leaving_request_id,leaving_at) VALUES($1,$2,gen_random_uuid(),$3) ON CONFLICT(tenant_id,person_id) DO UPDATE SET leaving_request_id=gen_random_uuid(),leaving_at=EXCLUDED.leaving_at RETURNING leaving_request_id::text`, p.TenantID, p.ID, in.Deadline).Scan(&id); err != nil {
		return nil, err
	}
	interval, err := etaInterval(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, s := range items {
		if s.Pause != nil {
			if s, err = cancelPause(ctx, tx, p, s, "leaving_replaced_pause"); err != nil {
				return nil, err
			}
		}
		var remaining *time.Duration
		if s.EtaReadyAt != nil && s.EtaReportedAt != nil && now.Sub(*s.EtaReportedAt) >= 0 && now.Sub(*s.EtaReportedAt) <= 2*interval {
			d := max(0, s.EtaReadyAt.Sub(now))
			remaining = &d
		}
		level, start := leavingLevel(now, in.Deadline, remaining, cooperativePause(s))
		plan := request
		plan.Level, plan.startsAt, plan.deadlineAt, plan.leavingID = level, &start, &in.Deadline, id
		if _, err = requestPause(ctx, tx, p, s, plan); err != nil {
			return nil, err
		}
	}
	if err = pausePreferenceEvent(ctx, tx, p, "leaving_requested", nil, map[string]any{"request_id": id, "deadline_at": in.Deadline}); err != nil {
		return nil, err
	}
	return m.getLeavingAt(r, tx, p)
}

func (m *Module) cancelLeavingAt(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "person required")
	}
	if err := canonicalPausePerson(r.Context(), tx, &p); err != nil {
		return nil, err
	}
	ctx := r.Context()
	if err := lockPausePerson(ctx, tx, p); err != nil {
		return nil, err
	}
	report, err := m.getLeavingAt(r, tx, p)
	if err != nil {
		return nil, err
	}
	out := report.(map[string]any)
	if out["request_id"].(*string) == nil {
		return out, nil
	}
	items := out["items"].([]Session)
	for i, item := range items {
		s, err := load(ctx, tx, item.ProjectID, item.ID, true)
		if err != nil {
			return nil, err
		}
		next, err := cancelPause(ctx, tx, p, s, "leaving_cancelled")
		var conflict *workorders.Error
		if errors.As(err, &conflict) && conflict.Status == http.StatusConflict {
			out["stop_in_flight"] = true
			continue
		}
		if err != nil {
			return nil, err
		}
		items[i] = next
	}
	if _, err = tx.Exec(ctx, `UPDATE person_pause_settings SET leaving_at=NULL,leaving_request_id=NULL WHERE person_id=$1`, p.ID); err != nil {
		return nil, err
	}
	if err = pausePreferenceEvent(ctx, tx, p, "leaving_cancelled", map[string]any{"request_id": out["request_id"], "deadline_at": out["deadline_at"]}, nil); err != nil {
		return nil, err
	}
	out["deadline_at"], out["request_id"], out["items"] = nil, nil, items
	return out, nil
}

// Only a launcher advertising owned_stop_v1 may claim this via heartbeat. The plain
// run-heartbeat PID observer advertises no stop: a PID is never signal authority.
func (m *Module) preparePauseStopHeartbeat(r *http.Request, tx pgx.Tx, p tenant.Principal, s Session) (Session, error) {
	if s.Management != "unmanaged" || !has(s, "owned_stop_v1") || s.Pause == nil || !s.Pause.StopRequested || s.Pause.StopControlID == "" {
		return s, nil
	}
	c, err := scanControl(tx.QueryRow(r.Context(), `SELECT `+controlColumns+` FROM harness_controls WHERE id=$1 AND session_id=$2 FOR UPDATE`, s.Pause.StopControlID, s.ID))
	if err != nil {
		return s, err
	}
	if c.State == "completed" || c.ExpectedGeneration == nil || *c.ExpectedGeneration != s.ID || c.RequestPayload == nil || !c.RequestPayload.StopNow {
		return s, nil
	}
	allowed, err := controlRequesterAuthorized(r, tx, p, s, c)
	if err != nil {
		return s, err
	}
	if !allowed {
		after, err := scanControl(tx.QueryRow(r.Context(), `UPDATE harness_controls SET state='completed',outcome='rejected',reason='authorization_revoked',claimed_at=coalesce(claimed_at,clock_timestamp()),completed_at=clock_timestamp() WHERE id=$1 RETURNING `+controlColumns, c.ID))
		if err != nil {
			return s, err
		}
		return s, record(r.Context(), tx, p, s, "control_completed", c, after)
	}
	if c.State == "pending" {
		before := c
		c, err = scanControl(tx.QueryRow(r.Context(), `UPDATE harness_controls SET state='claimed',claimed_at=clock_timestamp(),expires_at=clock_timestamp()+interval '45 seconds' WHERE id=$1 RETURNING `+controlColumns, c.ID))
		if err != nil {
			return s, err
		}
		if err = record(r.Context(), tx, p, s, "control_claimed", before, c); err != nil {
			return s, err
		}
	}
	var now time.Time
	if err = tx.QueryRow(r.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return s, err
	}
	next := *s.Pause
	next.StopExpiresInMS = controlTTL(c.ExpiresAt, now).Milliseconds()
	s.Pause = &next
	return s, nil
}

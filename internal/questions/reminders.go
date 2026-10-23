// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const maxReminderSeconds = 30 * 24 * 60 * 60

type Reminder struct {
	AfterSeconds int        `json:"after_seconds"`
	Count        int64      `json:"count"`
	NextAt       *time.Time `json:"next_at,omitempty"`
	LastAt       *time.Time `json:"last_at,omitempty"`
}

type ReminderSettings struct {
	DefaultAfterSeconds int    `json:"default_after_seconds"`
	TimeZone            string `json:"time_zone"`
	QuietStart          int    `json:"quiet_start"`
	QuietEnd            int    `json:"quiet_end"`
	Revision            int64  `json:"revision"`
}

func reminderSettingsTx(ctx context.Context, tx pgx.Tx) (ReminderSettings, error) {
	s := ReminderSettings{DefaultAfterSeconds: 7200, TimeZone: "UTC", QuietStart: 22 * 60, QuietEnd: 8 * 60}
	err := tx.QueryRow(ctx, `SELECT default_after_seconds,time_zone,quiet_start,quiet_end,revision FROM desk_reminder_settings WHERE tenant_id=current_setting('aeon.tenant_id')::uuid`).Scan(&s.DefaultAfterSeconds, &s.TimeZone, &s.QuietStart, &s.QuietEnd, &s.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return s, err
}

func (m *Module) handleReminderSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person || p.KeyCreatorID != "" || p.AuthKeyID != "" || r.Header.Get("Authorization") != "" {
		result(w, 0, nil, fail(403, "person_required", "a signed-in person must manage reminder defaults"))
		return
	}
	var in struct {
		DefaultAfterSeconds *int    `json:"default_after_seconds"`
		TimeZone            *string `json:"time_zone"`
		QuietStart          *int    `json:"quiet_start"`
		QuietEnd            *int    `json:"quiet_end"`
		ExpectedRevision    *int64  `json:"expected_revision"`
	}
	if r.Method == http.MethodPut {
		if err := decode(w, r, &in); err != nil {
			result(w, 0, nil, err)
			return
		}
		if in.TimeZone == nil || in.DefaultAfterSeconds == nil || in.QuietStart == nil || in.QuietEnd == nil || in.ExpectedRevision == nil || *in.ExpectedRevision < 0 || len(*in.TimeZone) > 100 || *in.TimeZone == "Local" || *in.DefaultAfterSeconds < 0 || *in.DefaultAfterSeconds > maxReminderSeconds || *in.QuietStart < 0 || *in.QuietStart > 1439 || *in.QuietEnd < 0 || *in.QuietEnd > 1439 {
			result(w, 0, nil, fail(400, "invalid_request", "invalid reminder defaults, quiet hours or expected_revision"))
			return
		}
		if _, err := time.LoadLocation(*in.TimeZone); err != nil {
			result(w, 0, nil, fail(400, "invalid_request", "invalid reminder timezone"))
			return
		}
	}
	var out ReminderSettings
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if r.Method == http.MethodPut {
			if err := treeLock(r.Context(), tx, p.TenantID); err != nil {
				return err
			}
		}
		permission := "settings.read"
		if r.Method == http.MethodPut {
			permission = "settings.manage"
		}
		if err := authz.RequireTx(r.Context(), tx, p, permission, authz.Scope{}); err != nil {
			if !errors.Is(err, authz.ErrForbidden) {
				return err
			}
			if r.Method != http.MethodGet {
				return fail(403, "forbidden", "reminder settings permission required")
			}
			if err := authz.RequireTx(r.Context(), tx, p, "settings.manage", authz.Scope{}); err != nil {
				return fail(403, "forbidden", "reminder settings permission required")
			}
		}
		var err error
		out, err = reminderSettingsTx(r.Context(), tx)
		if err != nil || r.Method == http.MethodGet {
			return err
		}
		if out.Revision != *in.ExpectedRevision {
			return fail(409, "revision_conflict", "reminder defaults changed; reload")
		}
		out = ReminderSettings{DefaultAfterSeconds: *in.DefaultAfterSeconds, TimeZone: *in.TimeZone, QuietStart: *in.QuietStart, QuietEnd: *in.QuietEnd, Revision: out.Revision + 1}
		_, err = tx.Exec(r.Context(), `INSERT INTO desk_reminder_settings(tenant_id,default_after_seconds,time_zone,quiet_start,quiet_end,revision) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(tenant_id) DO UPDATE SET default_after_seconds=excluded.default_after_seconds,time_zone=excluded.time_zone,quiet_start=excluded.quiet_start,quiet_end=excluded.quiet_end,revision=excluded.revision`, p.TenantID, out.DefaultAfterSeconds, out.TimeZone, out.QuietStart, out.QuietEnd, out.Revision)
		if err != nil {
			return err
		}
		_, err = events.Append(r.Context(), tx, p, events.Change{Type: "question.reminder_settings_changed", After: out})
		return err
	})
	result(w, 200, out, err)
}

func (m *Module) scheduleReminderTx(ctx context.Context, tx pgx.Tx, tid, project, id string, in Input) error {
	s, err := reminderSettingsTx(ctx, tx)
	if err != nil {
		return err
	}
	seconds := s.DefaultAfterSeconds
	if in.RemindAfterSeconds != nil {
		seconds = *in.RemindAfterSeconds
	}
	now, err := m.now(ctx, tx)
	if err != nil {
		return err
	}
	var due *time.Time
	if seconds > 0 {
		at := now.Add(time.Duration(seconds) * time.Second)
		due = &at
	}
	_, err = tx.Exec(ctx, `INSERT INTO desk_question_reminders(tenant_id,project_id,question_id,after_seconds,next_at) VALUES($1,$2,$3,$4,$5)`, tid, project, id, seconds, due)
	return err
}

// Quiet windows use elapsed minute steps so folds, gaps and overnight windows
// resolve to the first permitted instant, without trusting time.Date's DST choice.
func reminderNotBefore(now time.Time, zone string, start, end int) (time.Time, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "Local" || start < 0 || start > 1439 || end < 0 || end > 1439 {
		return time.Time{}, fmt.Errorf("invalid reminder quiet hours")
	}
	quiet := func(at time.Time) bool {
		local := at.In(loc)
		minute := local.Hour()*60 + local.Minute()
		return start < end && minute >= start && minute < end || start > end && (minute >= start || minute < end)
	}
	if !quiet(now) {
		return now, nil
	}
	for at, n := now.Truncate(time.Minute).Add(time.Minute), 0; n < 48*60; at, n = at.Add(time.Minute), n+1 {
		if !quiet(at) {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("reminder quiet window exceeds two days")
}

// Each due candidate is rechecked under the final mutation fence. Accepting the
// notice and advancing its interval commit together, including across replicas.
func (m *Module) remindOne(ctx context.Context, tid, id string) error {
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := db.InTenant(claimCtx, m.pool, tid, func(tx pgx.Tx) error { return m.remindTx(claimCtx, tx, tid, id) })
	cancel()
	if err == nil {
		return nil
	}
	// A failed acceptance retains its interval identity. Back off only its next
	// check, so persistently failing rows cannot starve healthy questions behind
	// a bounded page. The original failure remains visible to the dispatcher.
	retryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	retryErr := db.InTenant(retryCtx, m.pool, tid, func(tx pgx.Tx) error {
		if err := treeLock(retryCtx, tx, tid); err != nil {
			return err
		}
		now, err := m.now(retryCtx, tx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(retryCtx, `UPDATE desk_question_reminders r SET next_at=$4 FROM desk_questions q
 WHERE r.tenant_id=$1 AND r.question_id=$2 AND r.next_at<=$3
 AND q.tenant_id=r.tenant_id AND q.node_id=r.question_id AND q.state='open'`, tid, id, now, now.Add(deliveryReconcile))
		return err
	})
	return errors.Join(err, retryErr)
}

func (m *Module) remindTx(ctx context.Context, tx pgx.Tx, tid, id string) error {
	if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
		return err
	}
	if err := treeLock(ctx, tx, tid); err != nil {
		return err
	}
	var project, state string
	var revision int64
	err := tx.QueryRow(ctx, `SELECT q.project_id::text,q.state,q.revision FROM desk_questions q
 JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.node_id AND n.deleted_at IS NULL
 JOIN nodes project ON project.tenant_id=q.tenant_id AND project.id=q.project_id AND project.deleted_at IS NULL
 WHERE q.tenant_id=$1 AND q.node_id=$2 FOR KEY SHARE OF n,project FOR NO KEY UPDATE OF q`, tid, id).Scan(&project, &state, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || state != "open" {
		return err
	}
	var r Reminder
	if err := tx.QueryRow(ctx, `SELECT after_seconds,next_at,last_at,reminder_count FROM desk_question_reminders WHERE tenant_id=$1 AND question_id=$2 FOR UPDATE`, tid, id).Scan(&r.AfterSeconds, &r.NextAt, &r.LastAt, &r.Count); err != nil {
		return err
	}
	now, err := m.now(ctx, tx)
	if err != nil || r.AfterSeconds == 0 || r.NextAt == nil || r.NextAt.After(now) {
		return err
	}
	deferUntil := func(at time.Time) error {
		_, err := tx.Exec(ctx, `UPDATE desk_question_reminders SET next_at=$3 WHERE tenant_id=$1 AND question_id=$2`, tid, id, at)
		return err
	}
	// Same canonical person routing as the Desk browser notification source.
	var recipient *string
	err = tx.QueryRow(ctx, `SELECT CASE WHEN EXISTS(SELECT 1 FROM desk_askers a WHERE a.tenant_id=q.tenant_id AND a.question_id=q.node_id AND a.source_request_id IS NOT NULL)
 THEN (SELECT m.recipient_principal_id FROM desk_askers a JOIN inbox_compat_messages m ON m.tenant_id=a.tenant_id AND m.id=a.source_request_id WHERE a.tenant_id=q.tenant_id AND a.question_id=q.node_id ORDER BY a.id LIMIT 1)
 ELSE aeon_person_step_owner(q.project_id,(SELECT owner_principal_id FROM project_leads WHERE tenant_id=q.tenant_id AND project_id=q.project_id),NULL) END::text
 FROM desk_questions q WHERE q.tenant_id=$1 AND q.node_id=$2`, tid, id).Scan(&recipient)
	if err != nil {
		return err
	}
	if recipient == nil {
		return deferUntil(now.Add(time.Minute))
	}
	p := tenant.Principal{ID: *recipient, TenantID: tid, Kind: tenant.Person}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE tenant_id=$1 AND id=$2 AND kind='person' AND status='active' AND linked_to IS NULL)`, tid, p.ID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return deferUntil(now.Add(time.Minute))
	}
	for _, permission := range []string{"questions.read", "questions.decide", "inbox.read"} {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
			if errors.Is(err, authz.ErrForbidden) {
				return deferUntil(now.Add(time.Minute))
			}
			return err
		}
	}
	s, err := reminderSettingsTx(ctx, tx)
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `SELECT time_zone,quiet_start,quiet_end FROM phone_approval_preferences WHERE tenant_id=$1 AND person_id=$2`, tid, p.ID).Scan(&s.TimeZone, &s.QuietStart, &s.QuietEnd)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	allowedAt, err := reminderNotBefore(now, s.TimeZone, s.QuietStart, s.QuietEnd)
	if err != nil {
		return err
	}
	if allowedAt.After(now) {
		return deferUntil(allowedAt)
	}
	var systemID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE tenant_id=$1 AND kind='agent' AND name='System' AND roles @> ARRAY['system'] AND status='active'`, tid).Scan(&systemID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE desk_question_reminders SET reminder_count=reminder_count+1,last_at=$3,next_at=$4 WHERE tenant_id=$1 AND question_id=$2`, tid, id, now, now.Add(time.Duration(r.AfterSeconds)*time.Second)); err != nil {
		return err
	}
	key := fmt.Sprintf("%s/%d", id, r.Count+1)
	body := "An unanswered Decision Desk question is waiting: /decision-desk?item=q:" + id
	if err := inbox.QuestionReminderNoticeTx(ctx, tx, tid, p.ID, key, body); err != nil {
		return err
	}
	return event(ctx, tx, tenant.Principal{ID: systemID, TenantID: tid, Kind: tenant.Agent}, id, "question.reminded", revision)
}

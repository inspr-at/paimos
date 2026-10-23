// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Risks: early or duplicate nags, stale recipients, lost retries and answered
// questions waking again. One fixture database clock drives all deadlines.
func reminderLifecycle(t *testing.T) {
	f := deliveryFixtureFor(t)
	ctx := t.Context()
	path := "/api/settings/question-reminders"
	var defaults ReminderSettings
	w := request(ctx, f.mux, f.person, "GET", path, nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &defaults) != nil || defaults.DefaultAfterSeconds != 7200 || defaults.Revision != 0 {
		t.Fatalf("reminder defaults: %d %s", w.Code, w.Body)
	}
	settings := map[string]any{"default_after_seconds": 21600, "time_zone": "UTC", "quiet_start": 0, "quiet_end": 0, "expected_revision": 0}
	if w := request(ctx, f.mux, f.person, "PUT", path, map[string]any{"time_zone": "UTC", "expected_revision": 0}); w.Code != 400 {
		t.Fatalf("incomplete settings disabled reminders: %d %s", w.Code, w.Body)
	}
	for _, p := range []tenant.Principal{f.agent, f.otherAgent} {
		if w := request(ctx, f.mux, p, "PUT", path, settings); w.Code != 403 || !strings.Contains(w.Body.String(), "person_required") {
			t.Fatalf("agent changed defaults: %d %s", w.Code, w.Body)
		}
	}
	if w := request(ctx, f.mux, f.person, "PUT", path, settings); w.Code != 200 {
		t.Fatalf("save reminder defaults: %d %s", w.Code, w.Body)
	}
	if w := request(ctx, f.mux, f.person, "PUT", path, settings); w.Code != 409 {
		t.Fatalf("stale defaults overwrote a revision: %d %s", w.Code, w.Body)
	}
	seconds := 7200
	in := input()
	in.RemindAfterSeconds = &seconds
	q := f.ask(t, in)
	now := time.Unix(0, f.now.Load()).UTC()
	wantDue := now.Add(2 * time.Hour)
	if q.Reminder == nil || q.Reminder.NextAt == nil || !q.Reminder.NextAt.Equal(wantDue) || q.Reminder.Count != 0 || q.Reminder.AfterSeconds != seconds {
		t.Fatalf("explicit schedule: %+v", q.Reminder)
	}
	joined := in
	joined.RequestID = uid()
	if got := f.ask(t, joined); got.ID != q.ID || !got.Reminder.NextAt.Equal(wantDue) {
		t.Fatal("matching asker reset the canonical reminder")
	}
	defaultIn := input()
	defaultIn.Question = "Use the workspace default"
	defaultQ := f.ask(t, defaultIn)
	if defaultQ.Reminder == nil || defaultQ.Reminder.AfterSeconds != 21600 || !defaultQ.Reminder.NextAt.Equal(now.Add(6*time.Hour)) {
		t.Fatal("new question ignored saved defaults")
	}
	disabledIn := input()
	zero := 0
	disabledIn.RemindAfterSeconds = &zero
	disabledQ := f.ask(t, disabledIn)
	if disabledQ.ID == q.ID || disabledQ.Reminder.NextAt != nil || disabledQ.Reminder.AfterSeconds != 0 {
		t.Fatal("explicit disabled reminder merged into the enabled question")
	}
	for _, invalid := range []int{-1, maxReminderSeconds + 1} {
		bad := input()
		bad.RemindAfterSeconds = &invalid
		if w := request(ctx, f.mux, f.agent, "POST", "/api/projects/"+f.project+"/questions", bad); w.Code != 400 || !strings.Contains(w.Body.String(), "remind_after_seconds") {
			t.Fatalf("invalid reminder accepted: %d %s", w.Code, w.Body)
		}
	}
	count := func() int {
		return f.count(t, `SELECT count(*) FROM inbox_messages WHERE recipient_principal_id=$1 AND idempotency_key LIKE $2`, f.person.ID, "question-reminder/"+q.ID+"/%")
	}
	f.advance(2*time.Hour - time.Nanosecond)
	f.dispatch(t)
	if count() != 0 {
		t.Fatal("reminder preceded its deadline")
	}
	f.advance(time.Nanosecond)
	// First claim holds tenant before the tree barrier. A real competing claim
	// must fail NOWAIT specifically on that fence, then replay without a duplicate.
	raceCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	reached, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(db.AllProjects(raceCtx, "reminder claim barrier"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
			return f.m.remindTx(raceCtx, deskTreeBarrier{tx, reached, release}, f.person.TenantID, q.ID)
		})
	}()
	select {
	case <-reached:
	case <-raceCtx.Done():
		t.Fatal("reminder never acquired its tenant fence")
	}
	replica := New(f.d.App)
	replica.clock = f.m.clock
	err := db.InTenant(db.AllProjects(raceCtx, "competing reminder claim"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		return replica.remindTx(raceCtx, tenantNowait{tx}, f.person.TenantID, q.ID)
	})
	close(release)
	var conflict *pgconn.PgError
	if !errors.As(err, &conflict) || conflict.Code != "55P03" {
		t.Fatalf("competing claim did not serialize on tenant: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := replica.remindOne(db.AllProjects(ctx, "reminder retry"), f.person.TenantID, q.ID); err != nil {
		t.Fatal(err)
	}
	f.dispatch(t)
	if count() != 1 || f.status(t, q.ID).Reminder.Count != 1 {
		t.Fatal("replica/retry duplicated or lost the reminder")
	}
	// A failed acceptance rolls back both interval and count; it never claims
	// success. Trigger only the reminder notice, leaving fixture setup intact.
	if _, err := f.d.Admin.Exec(ctx, `CREATE FUNCTION reject_reminder_notice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.idempotency_key LIKE 'question-reminder/%' THEN RAISE EXCEPTION 'injected reminder acceptance failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_reminder_notice BEFORE INSERT ON inbox_messages FOR EACH ROW EXECUTE FUNCTION reject_reminder_notice()`); err != nil {
		t.Fatal(err)
	}
	f.advance(2 * time.Hour)
	if _, err := f.m.DispatchTenant(ctx, f.person.TenantID); err == nil {
		t.Fatal("failed acceptance reported success")
	}
	if count() != 1 || f.status(t, q.ID).Reminder.Count != 1 {
		t.Fatal("failed acceptance advanced the durable interval")
	}
	if _, err := f.d.Admin.Exec(ctx, `DROP TRIGGER reject_reminder_notice ON inbox_messages`); err != nil {
		t.Fatal(err)
	}
	f.advance(deliveryReconcile)
	f.dispatch(t)
	if count() != 2 {
		t.Fatal("retry did not accept the retained interval")
	}
	// Quiet hours are per recipient and are independent of phone push enablement.
	local := time.Unix(0, f.now.Load()).UTC()
	start := (local.Hour()*60 + local.Minute())
	end := (start + 180) % 1440
	if _, err := f.d.Admin.Exec(ctx, `INSERT INTO phone_approval_preferences(tenant_id,person_id,enabled,time_zone,quiet_start,quiet_end,escalation_minutes) VALUES($1,$2,false,'UTC',$3,$4,15)`, f.person.TenantID, f.person.ID, start, end); err != nil {
		t.Fatal(err)
	}
	f.advance(2 * time.Hour)
	f.dispatch(t)
	quietQ := f.status(t, q.ID)
	if count() != 2 || quietQ.Reminder.Count != 2 || quietQ.Reminder.NextAt == nil || !quietQ.Reminder.NextAt.After(time.Unix(0, f.now.Load())) {
		t.Fatal("quiet hours consumed or delivered a reminder")
	}
	f.now.Store(quietQ.Reminder.NextAt.UnixNano())
	f.dispatch(t)
	if count() != 3 {
		t.Fatal("quiet-end reminder was not accepted once")
	}
	f.m = replica // Restart loses no receipts or schedule.
	f.advance(36 * time.Hour)
	if _, err := f.d.Admin.Exec(ctx, `UPDATE phone_approval_preferences SET quiet_start=0,quiet_end=0 WHERE person_id=$1`, f.person.ID); err != nil {
		t.Fatal(err)
	}
	f.dispatch(t)
	f.dispatch(t)
	if count() != 4 {
		t.Fatal("downtime caused a reminder burst or lost its due interval")
	}
	current := f.status(t, q.ID)
	answered := f.answer(t, current, "Use local storage")
	if answered.Reminder.NextAt != nil {
		t.Fatal("answer did not stop the reminder")
	}
	f.advance(48 * time.Hour)
	f.dispatch(t)
	if count() != 4 || f.status(t, q.ID).Reminder.Count != 4 {
		t.Fatal("answered question was reminded again")
	}
	if w := request(ctx, f.mux, f.foreign, "GET", "/api/questions/"+q.ID, nil); w.Code != 404 {
		t.Fatalf("foreign tenant read reminder: %d", w.Code)
	}
}

func reminderQuietBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, at, want, zone string
		start, end           int
	}{
		{"overnight start inclusive", "2026-10-23T20:00:00Z", "2026-10-24T06:00:00Z", "Europe/Vienna", 1320, 480},
		{"overnight end exclusive", "2026-10-24T06:00:00Z", "2026-10-24T06:00:00Z", "Europe/Vienna", 1320, 480},
		{"spring gap", "2026-03-29T00:00:00Z", "2026-03-29T01:00:00Z", "Europe/Vienna", 60, 150},
		{"fold second hour", "2026-10-25T01:15:00Z", "2026-10-25T01:30:00Z", "Europe/Vienna", 60, 150},
		{"daytime window", "2026-10-23T10:30:00Z", "2026-10-23T12:00:00Z", "UTC", 600, 720},
		{"equal disables", "2026-10-23T10:30:00Z", "2026-10-23T10:30:00Z", "UTC", 600, 600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reminderNotBefore(at, tc.zone, tc.start, tc.end)
			if err != nil || got.UTC().Format(time.RFC3339) != tc.want {
				t.Fatalf("quiet boundary: %s %v, want %s", got, err, tc.want)
			}
		})
	}
}

func reminderCurrentAuthority(t *testing.T) {
	f := deliveryFixtureFor(t)
	ctx := t.Context()
	if _, err := f.d.Admin.Exec(ctx, `INSERT INTO desk_reminder_settings(tenant_id,default_after_seconds,time_zone,quiet_start,quiet_end,revision) VALUES($1,7200,'UTC',0,0,1)`, f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	viewer := tenant.Principal{TenantID: f.person.TenantID, Kind: tenant.Person}
	if err := f.d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Reminder reader') RETURNING id::text`, viewer.TenantID).Scan(&viewer.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, viewer.TenantID, viewer.ID, "viewer")
	if w := request(ctx, f.mux, viewer, "PUT", "/api/settings/question-reminders", map[string]any{"default_after_seconds": 0, "time_zone": "UTC", "quiet_start": 0, "quiet_end": 0, "expected_revision": 1}); w.Code != 403 || !strings.Contains(w.Body.String(), "forbidden") {
		t.Fatalf("reader changed reminder defaults: %d %s", w.Code, w.Body)
	}
	// Workspace Owner has an intrinsic permission ceiling. Use a real custom
	// project role and the current preferred person to test live revocation.
	var role string
	if err := f.d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'reminder_recipient','Reminder recipient') RETURNING id::text`, viewer.TenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, viewer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest(ARRAY['nodes.read','questions.read','questions.decide','inbox.read'])`, viewer.TenantID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, viewer.TenantID, viewer.ID, role, f.project); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(ctx, `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,state) VALUES($1,$2,$3,'paused')`, viewer.TenantID, f.project, viewer.ID); err != nil {
		t.Fatal(err)
	}
	q := f.ask(t, input())
	for _, permission := range []string{"questions.decide", "questions.read", "inbox.read"} {
		if _, err := f.d.Admin.Exec(ctx, `DELETE FROM role_permissions WHERE role_id=$1 AND permission=$2`, role, permission); err != nil {
			t.Fatal(err)
		}
		f.advance(2 * time.Hour)
		f.dispatch(t)
		var accepted int
		var last *time.Time
		if err := f.d.Admin.QueryRow(ctx, `SELECT reminder_count,last_at FROM desk_question_reminders WHERE question_id=$1`, q.ID).Scan(&accepted, &last); err != nil {
			t.Fatal(err)
		}
		if accepted != 0 || last != nil || f.count(t, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'question-reminder/%'`) != 0 {
			t.Fatalf("revoked %s still received a reminder", permission)
		}
		if _, err := f.d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, viewer.TenantID, role, permission); err != nil {
			t.Fatal(err)
		}
	}
	f.advance(time.Minute)
	f.dispatch(t)
	if f.count(t, `SELECT count(*) FROM inbox_messages WHERE recipient_principal_id=$1 AND idempotency_key LIKE 'question-reminder/%'`, viewer.ID) != 1 {
		t.Fatal("restored authority did not recover the single retained interval")
	}
}

func reminderFailureFairness(t *testing.T) {
	f := deliveryFixtureFor(t)
	ctx := t.Context()
	if _, err := f.d.Admin.Exec(ctx, `INSERT INTO desk_reminder_settings(tenant_id,default_after_seconds,time_zone,quiet_start,quiet_end,revision) VALUES($1,1,'UTC',0,0,1)`, f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < dispatchBatch; i++ {
		in := input()
		in.Question += uid()
		f.ask(t, in)
	}
	f.advance(2 * time.Second)
	good := f.ask(t, input())
	f.advance(time.Second)
	if _, err := f.d.Admin.Exec(ctx, `CREATE FUNCTION reject_poison_reminder() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.idempotency_key LIKE 'question-reminder/%' AND NEW.idempotency_key NOT LIKE 'question-reminder/`+good.ID+`/%' THEN RAISE EXCEPTION 'injected poison reminder'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_poison_reminder BEFORE INSERT ON inbox_messages FOR EACH ROW EXECUTE FUNCTION reject_poison_reminder()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.DispatchTenant(ctx, f.person.TenantID); err == nil || !strings.Contains(err.Error(), "injected poison reminder") {
		t.Fatalf("poison reminders did not return their actual failure: %v", err)
	}
	// No clock advance: backoff has yielded the next page to the healthy row.
	f.dispatch(t)
	if f.count(t, `SELECT count(*) FROM inbox_messages WHERE idempotency_key=$1`, "question-reminder/"+good.ID+"/1") != 1 {
		t.Fatal("persistent acceptance failures starved the later healthy reminder")
	}
	if f.count(t, `SELECT count(*) FROM desk_question_reminders WHERE question_id<>$1 AND (reminder_count<>0 OR last_at IS NOT NULL)`, good.ID) != 0 {
		t.Fatal("failed reminders consumed an interval")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard_test

import (
	"bytes"
	"crypto/md5"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

type workSession struct {
	project        string
	ticket         *string
	harness, model string
	at             time.Time
	beat, stop     *time.Time
	reason         string
	worktree       bool
	commits        int
}

func (w *world) workSession(t *testing.T, p tenant.Principal, s workSession, digest byte) string {
	t.Helper()
	id := uid()
	phase, activity := "working", "busy"
	if s.stop != nil {
		phase, activity = "stopped", "idle"
	}
	var reason, worktree any
	if s.reason != "" {
		reason = s.reason
	}
	if s.worktree {
		worktree = "/tmp/wt-" + id[:8]
	}
	commits := "[]"
	if s.commits > 0 {
		commits = `[{"sha":"0123456789abcdef0123456789abcdef01234567","subject":"work"}]`
	}
	w.tx(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,model,created_at,heartbeat_at,phase,activity,stopped_at,stop_reason,worktree,commits)
			VALUES($1,$2,$3,$4,$5,$6,'builder','unmanaged','worker','ship',$7,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb)`,
			p.TenantID, id, s.project, w.agent, s.ticket, s.harness, []byte{digest, 0x30}, s.model, s.at, s.beat, phase, activity, s.stop, reason, worktree, commits)
		return err
	})
	return id
}

func (w *world) outcome(t *testing.T, p tenant.Principal, kind, project, ticket string, session *string, at time.Time, release *string) {
	t.Helper()
	key := "test:" + kind + ":" + uid()
	sum := md5.Sum([]byte(key))
	w.tx(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO outcome_events(tenant_id,kind,project_id,ticket_node_id,session_id,release_node_id,idempotency_key,actor_principal_id,source,payload,request_digest,recorded_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'recorded','{}'::jsonb,$9,$10)`,
			p.TenantID, kind, project, ticket, session, release, key, p.ID, sum[:], at)
		return err
	})
}

func (w *world) setState(t *testing.T, p tenant.Principal, node, state string) {
	t.Helper()
	w.tx(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state=$2 WHERE id=$1`, node, state)
		return err
	})
}

func tm(v time.Time) *time.Time { return &v }

func TestDashboardWorkFromSessionsAndOutcomes(t *testing.T) {
	w := newWorld(t)
	visible := w.project(t, w.home, "WRK-1", "Work project")
	hidden := w.project(t, w.home, "HID-1", "Hidden beacon")
	shipped := w.ticket(t, w.home, visible, "WRK-2", "Shipped ticket")
	stalled := w.ticket(t, w.home, visible, "WRK-3", "Stalled ticket")
	human := w.ticket(t, w.home, visible, "WRK-4", "Done by a person")
	secret := w.ticket(t, w.home, hidden, "HID-2", "Hidden beacon ticket")
	release := uid()
	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) SELECT $1,$2,'WRK-9',id,'Release one',$3 FROM node_kinds WHERE slug='release'`, w.home.TenantID, release, visible)
		return err
	})
	w.setState(t, w.home, stalled, "in_progress")

	day := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	done := w.workSession(t, w.home, workSession{project: visible, ticket: &shipped, harness: "claude", model: "claude-opus-5-5", at: day,
		beat: tm(day.Add(time.Hour)), stop: tm(day.Add(time.Hour + 2*time.Minute)), reason: "completed", worktree: true, commits: 1}, 1)
	w.workSession(t, w.home, workSession{project: visible, ticket: &stalled, harness: "codex", model: "gpt-6-sol", at: day.Add(2 * time.Hour),
		beat: tm(day.Add(150 * time.Minute)), stop: tm(day.Add(150 * time.Minute)), reason: "completed", worktree: true}, 2)
	w.workSession(t, w.home, workSession{project: visible, ticket: &stalled, harness: "codex", model: "gpt-6-sol", at: day.Add(22 * time.Hour),
		stop: tm(day.Add(34 * time.Hour)), reason: "heartbeat_lost", worktree: true}, 3)
	hiddenSession := w.workSession(t, w.home, workSession{project: hidden, ticket: &secret, harness: "grok", model: "grok-4.7", at: day,
		beat: tm(day.Add(time.Hour)), stop: tm(day.Add(time.Hour)), worktree: true, commits: 1}, 4)
	w.setState(t, w.home, shipped, "done")
	w.setState(t, w.home, secret, "done")
	w.outcome(t, w.home, "ticket_done", visible, shipped, &done, day.Add(23*time.Hour), nil)
	// A second record of the same completion counts the ticket once.
	w.outcome(t, w.home, "ticket_done", visible, shipped, &done, day.Add(23*time.Hour+time.Minute), nil)
	w.outcome(t, w.home, "released", visible, shipped, &done, day.Add(23*time.Hour+5*time.Minute), &release)
	w.outcome(t, w.home, "ticket_done", visible, human, nil, day.Add(23*time.Hour), nil)
	w.outcome(t, w.home, "ticket_done", hidden, secret, &hiddenSession, day.Add(23*time.Hour), nil)
	w.outcome(t, w.home, "ticket_done", visible, stalled, &done, day.Add(10*24*time.Hour), nil)
	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
			SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, w.home.TenantID, w.member.ID, visible)
		return err
	})
	dbtest.BindRole(t, w.db, w.home.TenantID, w.admin.ID, "admin")

	const window = "/api/usage/dashboard?from=2026-09-10&to=2026-09-12"
	code, page, body := w.get(t, w.member, window)
	if code != 200 {
		t.Fatalf("member status %d %s", code, body)
	}
	work := page.Work
	if work.Basis != "sessions_started_in_range" || work.Sessions != 3 || work.WorkerSessions != 3 || work.TimedSessions != 2 || work.AgentSeconds != 3720+1800 {
		t.Fatalf("work sessions %+v", work)
	}
	if work.Done != 1 || work.Released != 1 || work.DoneAttributed != 1 || work.TicketsWorked != 2 || work.TicketsDone != 1 || work.ModelSessions != 3 || work.ReportedSessions != 0 {
		t.Fatalf("work outcomes %+v", work)
	}
	if len(work.Days) != 2 || work.Days[0].Day != "2026-09-10" || work.Days[0].Sessions != 2 || work.Days[0].Done != 0 || work.Days[1].Done != 1 || work.Days[1].Sessions != 1 || work.Days[0].AgentSeconds != 3720+1800 {
		t.Fatalf("days %+v", work.Days)
	}
	if work.Waste.Total != 2 || work.Waste.NoResult != 1 || work.Waste.Lost != 1 || work.Waste.Stuck != 0 || len(work.Waste.Rows) != 2 {
		t.Fatalf("waste %+v", work.Waste)
	}
	if first := work.Waste.Rows[0]; first.Kind != "no_result" || first.TicketKey == nil || *first.TicketKey != "WRK-3" || first.AgentSeconds == nil || *first.AgentSeconds != 1800 {
		t.Fatalf("first waste %+v", first)
	}
	if lost := work.Waste.Rows[1]; lost.Kind != "lost" || lost.AgentSeconds != nil {
		t.Fatalf("lost waste %+v", lost)
	}
	if len(work.Tickets) != 2 || work.Tickets[0].Key != "WRK-2" || !work.Tickets[0].DoneInRange || !work.Tickets[0].Released || work.Tickets[0].State != "done" || work.Tickets[0].Tokens != nil {
		t.Fatalf("tickets %+v", work.Tickets)
	}
	if work.Tickets[1].Key != "WRK-3" || work.Tickets[1].Sessions != 2 || work.Tickets[1].TimedSessions != 1 || work.Tickets[1].State != "in_progress" {
		t.Fatalf("stalled ticket %+v", work.Tickets[1])
	}
	if len(work.ByHarness) != 2 || work.ByHarness[0].Key != "claude" || work.ByHarness[0].Done != 1 || work.ByHarness[1].Sessions != 2 {
		t.Fatalf("harnesses %+v", work.ByHarness)
	}
	if len(work.ByModel) != 2 || work.ByModel[0].Label != "claude-opus-5-5" || len(work.ByProject) != 1 || work.ByProject[0].Done != 1 {
		t.Fatalf("groups %+v %+v", work.ByModel, work.ByProject)
	}
	for _, forbidden := range []string{"Hidden beacon", "HID-2", "grok-4.7", "Done by a person", "WRK-4"} {
		if bytes.Contains([]byte(body), []byte(forbidden)) {
			t.Fatalf("member work leaked %s", forbidden)
		}
	}

	code, filtered, _ := w.get(t, w.member, window+"&project="+hidden)
	if code != 200 || filtered.Work.Sessions != 0 || filtered.Work.Done != 0 || len(filtered.Work.Days) != 2 {
		t.Fatalf("hidden filter %d %+v", code, filtered.Work)
	}

	code, admin, _ := w.get(t, w.admin, window)
	if code != 200 || admin.Work.Sessions != 4 || admin.Work.Done != 2 || admin.Work.DoneAttributed != 2 || admin.Work.TicketsWorked != 3 {
		t.Fatalf("admin work %d %+v", code, admin.Work)
	}

	code, empty, body := w.get(t, w.member, "/api/usage/dashboard?from=2026-08-01&to=2026-08-03")
	if code != 200 || empty.Work.Sessions != 0 || empty.Work.Done != 0 || len(empty.Work.Days) != 2 || empty.Work.Tickets == nil || empty.Work.Waste.Rows == nil {
		t.Fatalf("empty range %d %s", code, body)
	}
}

// A provisional report with no token components is not token coverage: the
// page must say "reported by 1 of 2 sessions", never "by all 2".
func TestDashboardWorkTokenCoverageCountsOnlyReportedTokens(t *testing.T) {
	w := newWorld(t)
	project := w.project(t, w.home, "TOK-1", "Token project")
	ticket := w.ticket(t, w.home, project, "TOK-2", "Token ticket")
	day := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	full := w.workSession(t, w.home, workSession{project: project, ticket: &ticket, harness: "claude", model: "claude-opus-5-5", at: day,
		beat: tm(day.Add(time.Hour)), stop: tm(day.Add(time.Hour)), worktree: true, commits: 1}, 7)
	empty := w.workSession(t, w.home, workSession{project: project, ticket: &ticket, harness: "claude", model: "claude-opus-5-5", at: day.Add(2 * time.Hour),
		beat: tm(day.Add(3 * time.Hour)), stop: tm(day.Add(3 * time.Hour)), worktree: true, commits: 1}, 8)
	w.tx(t, w.home, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO harness_session_usage(tenant_id,session_id,model,sequence,input_tokens,output_tokens,cached_input_tokens,provisional,billing_mode,reported_at)
			VALUES($1,$2,'claude-opus-5-5',1,60,40,0,false,'unknown',$3)`, w.home.TenantID, full, day); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_session_usage(tenant_id,session_id,model,sequence,input_tokens,output_tokens,cached_input_tokens,provisional,billing_mode,reported_at)
			VALUES($1,$2,'claude-opus-5-5',1,NULL,NULL,NULL,true,'unknown',$3)`, w.home.TenantID, empty, day)
		return err
	})
	dbtest.BindRole(t, w.db, w.home.TenantID, w.admin.ID, "admin")

	code, page, body := w.get(t, w.admin, "/api/usage/dashboard?from=2026-09-14&to=2026-09-15")
	if code != 200 {
		t.Fatalf("status %d %s", code, body)
	}
	work := page.Work
	if work.Sessions != 2 || work.ReportedSessions != 1 {
		t.Fatalf("coverage sessions %d reported %d", work.Sessions, work.ReportedSessions)
	}
	if len(work.Tickets) != 1 || work.Tickets[0].Tokens == nil || *work.Tickets[0].Tokens != "100" || work.Tickets[0].ReportedSessions != 1 || work.Tickets[0].Sessions != 2 {
		t.Fatalf("ticket %+v", work.Tickets)
	}
	if len(work.ByHarness) != 1 || work.ByHarness[0].ReportedSessions != 1 || work.ByHarness[0].Tokens == nil || *work.ByHarness[0].Tokens != "100" {
		t.Fatalf("harness %+v", work.ByHarness)
	}
}

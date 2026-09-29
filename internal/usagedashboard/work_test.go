// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"testing"
	"time"
)

func tp(v time.Time) *time.Time { return &v }
func sp(v string) *string       { return &v }

var (
	wStart = time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	wEnd   = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	wNow   = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
)

func TestAgentSecondsCountsToTheLastSignOfLife(t *testing.T) {
	at := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		row  workRow
		want int64
		ok   bool
	}{
		{"open with a heartbeat", workRow{created: at, heartbeat: tp(at.Add(90 * time.Minute))}, 5400, true},
		{"stopped soon after the last heartbeat", workRow{created: at, heartbeat: tp(at.Add(time.Hour)), stopped: tp(at.Add(time.Hour + 2*time.Minute))}, 3720, true},
		{"closed long after going silent", workRow{created: at, heartbeat: tp(at.Add(time.Hour)), stopped: tp(at.Add(9 * time.Hour)), stopReason: "heartbeat_lost"}, 3600, true},
		{"never beat, stopped at once", workRow{created: at, stopped: tp(at.Add(4 * time.Minute))}, 240, true},
		{"never beat, stopped a day later", workRow{created: at, stopped: tp(at.Add(24 * time.Hour))}, 0, false},
		{"never beat and still open", workRow{created: at}, 0, false},
		{"clipped to the range end", workRow{created: wEnd.Add(-time.Hour), heartbeat: tp(wEnd.Add(3 * time.Hour))}, 3600, true},
	}
	for _, c := range cases {
		got, ok := agentSeconds(c.row, wEnd)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %d,%v want %d,%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestWasteKindNamesOnlyRealWaste(t *testing.T) {
	at := wNow.Add(-3 * time.Hour)
	ticket := sp("t-1")
	open := sp("in_progress")
	worker := func(mod func(*workRow)) workRow {
		r := workRow{id: "s", ticketID: ticket, ticketState: open, role: "worker", shape: "ship", phase: "stopped", activity: "idle",
			created: at, heartbeat: tp(at.Add(time.Hour)), stopped: tp(at.Add(time.Hour)), worktree: true}
		mod(&r)
		return r
	}
	cases := []struct {
		name string
		row  workRow
		want string
	}{
		{"working, silent 20 min", workRow{role: "coordinator", phase: "working", activity: "busy", created: at, heartbeat: tp(wNow.Add(-20 * time.Minute))}, "stuck"},
		{"working, beat a minute ago", workRow{role: "worker", phase: "working", activity: "busy", created: at, heartbeat: tp(wNow.Add(-time.Minute))}, ""},
		{"idle and silent is not stuck", workRow{role: "worker", phase: "working", activity: "idle", created: at, heartbeat: tp(wNow.Add(-time.Hour))}, ""},
		{"finished with nothing", worker(func(*workRow) {}), "no_result"},
		{"finished with a commit", worker(func(r *workRow) { r.commits = 2 }), ""},
		{"ticket is done", worker(func(r *workRow) { r.ticketState = sp("done") }), ""},
		{"scout work leaves no commit by design", worker(func(r *workRow) { r.shape = "scout" }), ""},
		{"no worktree, so no commit evidence", worker(func(r *workRow) { r.worktree = false }), ""},
		{"managed run with no commit", worker(func(r *workRow) { r.worktree = false; r.runOutcome = sp("no_commit") }), "no_result"},
		{"lost contact without a commit", worker(func(r *workRow) { r.stopReason = "heartbeat_lost" }), "lost"},
		{"lost contact after a commit", worker(func(r *workRow) { r.stopReason = "heartbeat_lost"; r.commits = 1 }), ""},
		{"failed run", worker(func(r *workRow) { r.runStatus = sp("ownership_lost"); r.commits = 3 }), "failed"},
		{"crash stop reason", worker(func(r *workRow) { r.stopReason = "harness_crashed" }), "failed"},
		{"a normal stop is not a failure", worker(func(r *workRow) { r.stopReason = "user requested"; r.commits = 1 }), ""},
		{"coordinators are not waste when stopped", worker(func(r *workRow) { r.role = "coordinator" }), ""},
	}
	for _, c := range cases {
		if got := wasteKind(c.row, wNow); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestBuildWorkFoldsRetriesAndAttributesDone(t *testing.T) {
	day := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	retry, shipped := sp("t-retry"), sp("t-ship")
	row := func(id string, ticket *string, key, state string, at time.Time, mod func(*workRow)) workRow {
		r := workRow{id: id, projectID: "p-1", projectKey: "AEON", projectTitle: "Aeon", ticketID: ticket, ticketKey: sp(key), ticketTitle: sp(key + " title"), ticketState: sp(state),
			harness: "codex", model: sp("gpt-6-sol"), role: "worker", shape: "ship", phase: "stopped", activity: "idle",
			created: at, heartbeat: tp(at.Add(time.Hour)), stopped: tp(at.Add(time.Hour)), worktree: true}
		if mod != nil {
			mod(&r)
		}
		return r
	}
	rows := []workRow{
		row("s1", retry, "AEON-1", "in_progress", day, nil),
		row("s2", retry, "AEON-1", "in_progress", day.Add(2*time.Hour), func(r *workRow) { r.stopReason = "heartbeat_lost" }),
		row("s3", retry, "AEON-1", "in_progress", day.Add(4*time.Hour), func(r *workRow) { r.commits = 1 }),
		row("s4", shipped, "AEON-2", "done", day.Add(24*time.Hour), func(r *workRow) {
			r.harness, r.model, r.commits, r.usageRows, r.tokens = "claude", sp("claude-opus-5-5"), 2, 1, sp("1200")
		}),
		row("s5", nil, "", "", day, func(r *workRow) {
			r.ticketKey, r.ticketTitle, r.ticketState, r.role, r.model, r.shape = nil, nil, nil, "coordinator", nil, "unknown"
		}),
	}
	done := []doneRow{{ticketID: "t-ship", projectID: "p-1", projectTitle: "Aeon", at: day.Add(26 * time.Hour), harness: sp("claude"), model: sp("claude-opus-5-5")}}
	w := buildWork(rows, done, map[string]bool{"t-ship": true}, wStart, wEnd, wNow)

	if w.Sessions != 5 || w.WorkerSessions != 4 || w.TimedSessions != 5 || w.AgentSeconds != 5*3600 {
		t.Fatalf("sessions %d workers %d timed %d seconds %d", w.Sessions, w.WorkerSessions, w.TimedSessions, w.AgentSeconds)
	}
	if w.ReportedSessions != 1 || w.ModelSessions != 4 {
		t.Fatalf("reported %d models %d", w.ReportedSessions, w.ModelSessions)
	}
	if w.Done != 1 || w.DoneAttributed != 1 || w.TicketsWorked != 2 || w.TicketsDone != 1 {
		t.Fatalf("done %d attributed %d worked %d ticketsDone %d", w.Done, w.DoneAttributed, w.TicketsWorked, w.TicketsDone)
	}
	if len(w.Days) != 7 || w.Days[0].Day != "2026-09-20" || w.Days[2].Sessions != 4 || w.Days[3].Done != 1 || w.Days[3].Sessions != 1 || w.Days[6].Sessions != 0 {
		t.Fatalf("days %+v", w.Days)
	}
	// Three worker sessions on a ticket that is still open fold into one retried row.
	if w.Waste.Total != 1 || w.Waste.Retried != 1 || w.Waste.NoResult != 0 || w.Waste.Lost != 0 || len(w.Waste.Rows) != 1 {
		t.Fatalf("waste %+v", w.Waste)
	}
	if got := w.Waste.Rows[0]; got.Kind != "retried" || got.Sessions != 3 || got.SessionID != "s3" || got.AgentSeconds == nil || *got.AgentSeconds != 3*3600 {
		t.Fatalf("retried row %+v", got)
	}
	if w.Tickets[0].Key != "AEON-1" || w.Tickets[0].Sessions != 3 || w.Tickets[0].Tokens != nil {
		t.Fatalf("first ticket %+v", w.Tickets[0])
	}
	if s := w.Tickets[1]; s.Key != "AEON-2" || !s.DoneInRange || !s.Released || s.Tokens == nil || *s.Tokens != "1200" || s.ReportedSessions != 1 {
		t.Fatalf("shipped ticket %+v", s)
	}
	if h := w.ByHarness[0]; h.Key != "claude" || h.Done != 1 || h.Sessions != 1 {
		t.Fatalf("harness order %+v", w.ByHarness)
	}
	if len(w.ByModel) != 2 || w.ByModel[0].Label != "claude-opus-5-5" || w.ByModel[0].Done != 1 {
		t.Fatalf("models %+v", w.ByModel)
	}
	if len(w.ByProject) != 1 || w.ByProject[0].Done != 1 || w.ByProject[0].Sessions != 5 {
		t.Fatalf("projects %+v", w.ByProject)
	}
}

func TestBuildWorkKeepsTwoOrFewerWorkersAsSessionWaste(t *testing.T) {
	at := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	base := workRow{projectID: "p", projectKey: "P", ticketID: sp("t"), ticketKey: sp("P-1"), ticketState: sp("in_progress"), harness: "grok", role: "worker", shape: "ship", phase: "stopped", worktree: true}
	a, b := base, base
	a.id, a.created, a.heartbeat, a.stopped = "a", at, tp(at.Add(30*time.Minute)), tp(at.Add(30*time.Minute))
	b.id, b.created, b.stopped, b.stopReason = "b", at.Add(time.Hour), tp(at.Add(26*time.Hour)), "heartbeat_lost"
	w := buildWork([]workRow{a, b}, nil, nil, wStart, wEnd, wNow)
	if w.Waste.Total != 2 || w.Waste.NoResult != 1 || w.Waste.Lost != 1 || w.Waste.Retried != 0 {
		t.Fatalf("waste %+v", w.Waste)
	}
	// A row with a known time sorts before one whose time is unknown.
	if w.Waste.Rows[0].SessionID != "a" || w.Waste.Rows[1].AgentSeconds != nil {
		t.Fatalf("rows %+v", w.Waste.Rows)
	}
	if w.TimedSessions != 1 || w.Sessions != 2 {
		t.Fatalf("timed %d of %d", w.TimedSessions, w.Sessions)
	}
}

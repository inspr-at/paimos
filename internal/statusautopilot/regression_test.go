// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/jackc/pgx/v5"
)

func TestProgressUsesPersistedWorkEvidence(t *testing.T) {
	f := setup(t)
	comment := f.add("AUT-2", "ticket", " In--Progress ", 10, nil)
	branch := f.add("AUT-3", "ticket", "inprogress", 10, nil)
	staleBranch := f.add("AUT-4", "ticket", "inprogress", 10, nil)
	pr := f.add("AUT-5", "ticket", "inprogress", 10, nil)
	stalePR := f.add("AUT-6", "ticket", "inprogress", 10, nil)
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Recent title edit',updated_at=clock_timestamp() WHERE id=$1`, comment); err != nil {
			return err
		}
		if _, err := events.Append(t.Context(), tx, f.p, events.Change{NodeID: &comment, Type: "comment.created", After: map[string]string{"body_markdown": "A recent comment is not work evidence."}}); err != nil {
			return err
		}
		for i, id := range []string{branch, staleBranch} {
			at := f.now.Add(-24 * time.Hour)
			if i == 1 {
				at = f.now.Add(-10 * 24 * time.Hour)
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,branch,phase,created_at,heartbeat_at,stopped_at)
 VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',$5,$5,'work/real-branch','stopped',$6,$6,$6)`, f.p.TenantID, f.project, f.p.ID, id, []byte(id), at)
			if err != nil {
				return err
			}
		}
		return nil
	})
	for i, id := range []string{pr, stalePR} {
		order := f.add(fmt.Sprintf("AUT-%d", i+10), "work_order", "open", 1, nil)
		at := f.now.Add(-24 * time.Hour)
		if i == 1 {
			at = f.now.Add(-10 * 24 * time.Hour)
		}
		f.tx(func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, f.p.TenantID, order, f.p.ID); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO work_order_reviews(tenant_id,work_order_id,ticket_node_id,request_id,request,ticket_snapshot,repository,base_sha,head_sha,author_family,pull_request,created_at)
 VALUES($1,$2,$3,gen_random_uuid(),'{}','snapshot','example/repo',$4,$5,'openai',118,$6)`, f.p.TenantID, order, id, strings.Repeat("a", 40), strings.Repeat("b", 40), at)
			return err
		})
	}
	f.run(f.now)
	for _, id := range []string{comment, staleBranch, stalePR} {
		if f.state(id).State != "open" {
			t.Fatalf("stalled work stayed in progress: %s", id)
		}
	}
	for _, id := range []string{branch, pr} {
		if f.state(id).State != "inprogress" {
			t.Fatalf("recent persisted work reopened: %s", id)
		}
	}
}

func TestPausedSessionsOccupyTicketUntilResumedOrArchived(t *testing.T) {
	f := setup(t)
	for i, tc := range []struct {
		pause, stopReason  string
		archived, occupied bool
	}{
		{"paused", "paused", false, true},
		{"resume_requested", "paused", false, true},
		{"paused", "paused", true, false},
		{"resume_requested", "paused", true, false},
		{"resumed", "paused", false, false},
		{"cancelled", "paused", false, false},
		{"paused", "process_exited", false, false},
	} {
		id := f.add(fmt.Sprintf("AUT-%d", 800+i), "ticket", "in_progress", 20, nil)
		at := f.now.Add(-10 * 24 * time.Hour)
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase,created_at,heartbeat_at,stopped_at,stop_reason,pause_record,archived_at)
 VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',$5,$5,'stopped',$6,$6,$6,$7,jsonb_build_object('state',$8::text,'deadline_at',$6::timestamptz),CASE WHEN $9 THEN $6::timestamptz ELSE NULL END)`, f.p.TenantID, f.project, f.p.ID, id, []byte(id), at, tc.stopReason, tc.pause, tc.archived)
			return err
		})
		f.tx(func(tx pgx.Tx) error {
			c, _, err := loadCandidate(t.Context(), tx, id, f.now)
			if err != nil {
				return err
			}
			if c.Work != tc.occupied {
				t.Errorf("pause=%s reason=%s archived=%t: occupied=%t", tc.pause, tc.stopReason, tc.archived, c.Work)
			}
			return nil
		})
	}
	f.run(f.now)
	for i := range 7 {
		var id string
		f.tx(func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT id::text FROM nodes WHERE key=$1`, fmt.Sprintf("AUT-%d", 800+i)).Scan(&id)
		})
		want := "open"
		if i < 2 {
			want = "in_progress"
		}
		if got := f.state(id).State; got != want {
			t.Errorf("case %d: ticket state=%s, want %s", i, got, want)
		}
	}
}

func TestHumanChecksPermitNonDeliveryDailyRules(t *testing.T) {
	f := setup(t)
	human := "Touch ID"
	for i, tc := range []struct {
		state, flag string
		days        int
	}{{"new", "triage_list", 7}, {"backlog", "cancel_suggested", 90}, {"blocked", "blocked_reminder", 14}, {"in_progress", "", 3}, {"done", "missed_release", 14}} {
		id := f.add(fmt.Sprintf("AUT-%d", i+2), "ticket", tc.state, tc.days, &human)
		f.run(f.now.Add(time.Duration(i) * 24 * time.Hour))
		n := f.state(id)
		if tc.flag == "" {
			if n.State != "open" {
				t.Fatal("human check suppressed reopening")
			}
		} else if !n.Marks[tc.flag] {
			t.Fatalf("human check suppressed %s", tc.flag)
		}
		if !pending(n) {
			t.Fatal("human check was cleared")
		}
	}
}

func TestProjectOffRunsNoRulesButWorkspaceOffListsSuggestions(t *testing.T) {
	f := setup(t)
	f.call(f.p, "PUT", "/api/projects/"+f.project+"/status-autopilot", `{"mode":"off","expected_revision":0}`, 200)
	ids := []string{}
	for i, state := range []string{"new", "backlog", "blocked", "in_progress", "done", "delivered"} {
		ids = append(ids, f.add(fmt.Sprintf("AUT-%d", i+2), "ticket", state, 100, nil))
	}
	release := f.release([]string{ids[4]})
	f.tx(func(tx pgx.Tx) error { return PublishTx(t.Context(), tx, f.p.TenantID, release) })
	f.run(f.now)
	for _, id := range ids {
		if len(f.changes(id)) != 0 || len(f.state(id).Marks) != 0 {
			t.Fatal("project Off ran a rule")
		}
	}
	s := Defaults()
	s.Enabled = false
	raw, _ := json.Marshal(map[string]any{"enabled": false, "rules": s.Rules, "expected_revision": 0})
	f.call(f.p, "PUT", "/api/settings/status-autopilot", string(raw), 200)
	f.call(f.p, "PUT", "/api/projects/"+f.project+"/status-autopilot", `{"mode":"inherit","expected_revision":1}`, 200)
	f.run(f.now.Add(24 * time.Hour))
	if !f.state(ids[0]).Marks["triage_list"] || !f.state(ids[1]).Marks["cancel_suggested"] {
		t.Fatal("workspace Off lost suggestions")
	}
	for _, id := range ids[2:] {
		if len(f.changes(id)) != 0 {
			t.Fatal("workspace Off moved/reminded a ticket")
		}
	}
}

func TestDailyBatchCommitsAndResumesAfterFailure(t *testing.T) {
	f := setup(t)
	for i := range batchSize + 3 {
		f.add(fmt.Sprintf("AUT-%d", i+2), "ticket", "in_progress", 10, nil)
	}
	var failID string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='ticket' ORDER BY n.id OFFSET $1 LIMIT 1`, batchSize).Scan(&failID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `CREATE FUNCTION reject_second_batch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id='`+failID+`'::uuid THEN RAISE EXCEPTION 'test interruption'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_second_batch BEFORE UPDATE ON nodes FOR EACH ROW EXECUTE FUNCTION reject_second_batch()`)
		return err
	})
	if err := f.m.RunTenant(t.Context(), f.p.TenantID, f.now); err == nil {
		t.Fatal("expected second-batch interruption")
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		var cursor *string
		var day time.Time
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM status_autopilot_receipts WHERE rule='progress'`).Scan(&count); err != nil {
			return err
		}
		if count != batchSize {
			return fmt.Errorf("first batch did not commit: %d", count)
		}
		if err := tx.QueryRow(t.Context(), `SELECT day,after_node_id::text FROM status_autopilot_days`).Scan(&day, &cursor); err != nil {
			return err
		}
		if cursor == nil || !day.Before(f.now) {
			return fmt.Errorf("cursor did not retain incomplete day")
		}
		_, err := tx.Exec(t.Context(), `DROP TRIGGER reject_second_batch ON nodes; DROP FUNCTION reject_second_batch()`)
		return err
	})
	f.run(f.now)
	f.tx(func(tx pgx.Tx) error {
		var count, batches int
		if err := tx.QueryRow(t.Context(), `SELECT count(*),count(DISTINCT xmin::text) FROM events WHERE type='status_autopilot.changed'`).Scan(&count, &batches); err != nil {
			return err
		}
		if count != batchSize+3 || batches != 2 {
			return fmt.Errorf("resumed events=%d transactions=%d", count, batches)
		}
		_, err := tx.Exec(t.Context(), `SET LOCAL enable_seqscan=off`)
		if err != nil {
			return err
		}
		rows, err := tx.Query(t.Context(), `EXPLAIN SELECT n.id FROM nodes n WHERE n.deleted_at IS NULL AND `+candidateStateSQL+` IN ('new','backlog','blocked','in_progress','inprogress','progress','active','done','delivered') ORDER BY n.id LIMIT 50`)
		if err != nil {
			return err
		}
		defer rows.Close()
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err = rows.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line)
		}
		if !strings.Contains(plan.String(), "nodes_status_autopilot_candidates") {
			return fmt.Errorf("candidate index unused: %s", plan.String())
		}
		return rows.Err()
	})
}

func TestMissingReleaseDoesNotPoisonDailyCursor(t *testing.T) {
	f := setup(t)
	id := f.add("AUT-2", "ticket", "done", 1, nil)
	release := f.release([]string{id})
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, release)
		return err
	})
	f.tx(func(tx pgx.Tx) error { return PublishTx(t.Context(), tx, f.p.TenantID, release) })
	f.run(f.now)
	f.run(f.now.Add(24 * time.Hour))
	f.tx(func(tx pgx.Tx) error {
		var day time.Time
		var considered int
		if err := tx.QueryRow(t.Context(), `SELECT day FROM status_autopilot_days`).Scan(&day); err != nil {
			return err
		}
		if !day.Equal(f.now.Add(24 * time.Hour)) {
			return fmt.Errorf("cursor stuck: %s", day)
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM status_autopilot_releases`).Scan(&considered); err != nil {
			return err
		}
		if considered != 1 {
			return fmt.Errorf("release was not remembered")
		}
		return nil
	})
}

// Inject a real SQL error while reading one ticket, including nested savepoints.
type unreadableTicketTx struct {
	pgx.Tx
	id string
}

func (tx unreadableTicketTx) Begin(ctx context.Context) (pgx.Tx, error) {
	nested, err := tx.Tx.Begin(ctx)
	return unreadableTicketTx{nested, tx.id}, err
}
func (tx unreadableTicketTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "SELECT to_jsonb(n)") && len(args) > 0 {
		if ids, ok := args[0].([]string); ok && len(ids) == 1 && ids[0] == tx.id {
			_, err := tx.Tx.Exec(ctx, `SELECT 1/0`)
			return nil, err
		}
	}
	return tx.Tx.Query(ctx, sql, args...)
}

func TestPublishIsBoundedAndIsolatesUnreadableTicket(t *testing.T) {
	f := setup(t)
	ids := []string{}
	for i := range batchSize + 3 {
		ids = append(ids, f.add(fmt.Sprintf("AUT-%d", i+2), "ticket", "done", 1, nil))
	}
	release := f.release(ids)
	var bad string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT min(node_id::text) FROM (SELECT ticket_node_id node_id FROM journey_tickets WHERE release_node_id=$1) members`, release).Scan(&bad); err != nil {
			return err
		}
		if err := PublishTx(t.Context(), unreadableTicketTx{tx, bad}, f.p.TenantID, release); err != nil {
			return err
		}
		// A caller's own settlement write still commits despite the read error.
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Settlement committed' WHERE id=$1`, release)
		return err
	})
	if f.state(bad).State != "done" {
		t.Fatal("unreadable ticket changed")
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM status_autopilot_receipts WHERE rule='publish'`).Scan(&count); err != nil {
			return err
		}
		if count != batchSize-1 {
			return fmt.Errorf("publish batch unbounded or rolled back: %d", count)
		}
		return nil
	})
	f.run(f.now.Add(2 * 24 * time.Hour))
	for _, id := range ids {
		if f.state(id).State != "delivered" {
			t.Fatal("deferred delivery did not drain")
		}
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM status_autopilot_deliveries`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("completed deliveries retained")
		}
		return nil
	})
}

func TestCurrentFlagsOutliveRecentChangesAndDisappearOnResolution(t *testing.T) {
	f := setup(t)
	ids := []string{}
	for i := range 53 {
		ids = append(ids, f.add(fmt.Sprintf("AUT-%d", i+2), "ticket", "new", 10, nil))
	}
	for i, tc := range []struct {
		state string
		days  int
	}{{"backlog", 100}, {"blocked", 20}, {"done", 20}} {
		ids = append(ids, f.add(fmt.Sprintf("AUT-%d", i+100), "ticket", tc.state, tc.days, nil))
	}
	f.run(f.now)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Edited since flag',updated_at=clock_timestamp() WHERE id=$1`, ids[0])
		return err
	})
	var recent, current struct {
		Items []Change `json:"items"`
	}
	if err := json.Unmarshal(f.call(f.p, "GET", "/api/status-autopilot/changes", "", 200).Body.Bytes(), &recent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(f.call(f.p, "GET", "/api/status-autopilot/changes?suggestions=true", "", 200).Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if len(recent.Items) != 50 || len(current.Items) != 56 {
		t.Fatalf("history=%d current=%d", len(recent.Items), len(current.Items))
	}
	seen := map[string]bool{}
	for _, c := range current.Items {
		seen[c.To] = true
		if c.NodeID == ids[0] && c.Undoable {
			t.Fatal("later edit remained undoable")
		}
	}
	for _, flag := range []string{"triage_list", "cancel_suggested", "blocked_reminder", "missed_release"} {
		if !seen[flag] {
			t.Fatalf("missing list %s", flag)
		}
	}
	change := f.changes(ids[1])[0]
	f.call(f.p, "POST", fmt.Sprintf("/api/events/%d/undo", change.EventID), "", 201)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='open' WHERE id=$1`, ids[0])
		return err
	})
	if err := json.Unmarshal(f.call(f.p, "GET", "/api/status-autopilot/changes?suggestions=true", "", 200).Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Items) != 54 {
		t.Fatalf("resolved flags stayed listed: %d", len(current.Items))
	}
}

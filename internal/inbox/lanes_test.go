// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Called as a subtest of the registered session isolation behaviour test.
// Risk: chat/heartbeats or exact retries conceal a silent lane; structured
// status loses its goal, blocker owner or ETA, leaks across scopes, or commits
// a projection after the underlying inbox write failed.
func testLaneStatusInboxDay(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	w.agent.Scopes = []string{"inbox.read", "inbox.send", "inbox.receipt", "nodes.read", "harness.read"}
	token := insertKey(t, w.db, w.agent, w.agent.Scopes, "lane-day-synthetic")
	first := messageTestSession(t, w, project, w.agent, "JoeDesk")
	quiet := messageTestSession(t, w, project, w.agent, "Never reported")
	foreignOwner := insertPrincipal(t, w.db, w.agent.TenantID, tenant.Agent, "other-lane-agent", nil)
	foreign := messageTestSession(t, w, project, foreignOwner, "Another principal")
	var target, goal string
	err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id)
 SELECT $1,id,'MSG-3','Lane work',$2 FROM node_kinds WHERE slug='work' RETURNING id::text`, w.agent.TenantID, project).Scan(&target); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,'goal','Goal','GOAL','target')`, w.agent.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title)
 SELECT $1,id,'GOAL-1','JoeDesk goal' FROM node_kinds WHERE slug='goal' RETURNING id::text`, w.agent.TenantID).Scan(&goal); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET ticket_node_id=$2,work_shape='ship' WHERE id=ANY($1::uuid[])`, []string{first, quiet}, target)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var now time.Time
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	// Every instant is derived from the DB fixture clock. Both writes and reads
	// use the injected clock; no fixed date competes with real-clock rows.
	start := now
	m.base.laneClock = func(context.Context, pgx.Tx) (time.Time, error) { return now, nil }
	ps := map[string]tenant.Principal{}
	for _, p := range []tenant.Principal{w.agent, w.admin, w.sender, w.other, w.outsider} {
		ps[p.ID] = p
	}
	mux := http.NewServeMux()
	m.base.Mount(mux)
	m.Mount(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if p, ok := ps[r.Header.Get("X-Principal")]; ok {
			r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		}
		mux.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	read := func(p tenant.Principal, query string) LanePage {
		t.Helper()
		status, body := do(t, srv, p.ID, "GET", "/api/inbox/lanes?"+query, "", nil)
		if status != 200 {
			t.Fatalf("lanes %d: %s", status, body)
		}
		return mustJSON[LanePage](t, body)
	}
	report := func(path, key, session string, s LaneStatus) (int, []byte) {
		t.Helper()
		in := map[string]any{"kind": "status", "lane_status": s, "sender_session_id": session, "idempotency_key": key, "body": "client body is rendered from the structured fields"}
		if path == "/api/inbox/messages" {
			in["recipient_principal_id"] = w.sender.ID
		} else {
			in["to"] = w.sender.ID
		}
		return compatPostAuth(t, srv, w.agent, path, in, token)
	}
	initial := read(w.admin, "target="+target)
	if len(initial.Items) != 2 || initial.Items[0].Status != nil || initial.Items[0].AgeSeconds != nil {
		t.Fatalf("unreported ticket lanes missing: %+v", initial)
	}
	eta := start.Add(8 * time.Hour)
	var latest Message
	var lastStatus LaneStatus
	for i, done := range []string{"scope read", "implementation ready", "blocker resolved", "day complete"} {
		now = start.Add(time.Duration(i*2) * time.Hour)
		s := LaneStatus{TargetNodeID: target, Done: done, Next: "next verified step", ETA: &eta}
		if i == 1 {
			s.Blocker, s.BlockerOwner = "waiting for API decision", "LEAD"
		}
		status, body := report("/api/inbox/messages", fmt.Sprintf("day-%d", i), first, s)
		if status != 201 {
			t.Fatalf("day report %d: %d %s", i, status, body)
		}
		latest, lastStatus = mustJSON[Message](t, body), s
		if latest.Kind != "status" || latest.LaneStatus == nil || latest.LaneStatus.Done != done || latest.Body != s.Line() {
			t.Fatalf("status reduced to chat: %s", body)
		}
		page := read(w.admin, "target="+target+"&session="+first)
		if len(page.Items) != 1 || page.Items[0].MessageID == nil || *page.Items[0].MessageID != latest.ID || page.Items[0].Status == nil || page.Items[0].Status.BlockerOwner != s.BlockerOwner || page.Items[0].Status.ETA == nil || !page.Items[0].Status.ETA.Equal(eta) || *page.Items[0].AgeSeconds != 0 {
			t.Fatalf("latest status/ETA missing at day step %d: %+v", i, page)
		}
	}
	// The recipient reads the full day directly from the ordinary inbox.
	status, body := do(t, srv, w.sender.ID, "GET", "/api/inbox/messages?wait_ms=0", "", nil)
	if status != 200 {
		t.Fatalf("day inbox %d %s", status, body)
	}
	day := mustJSON[Page](t, body)
	if len(day.Items) != 4 || day.Items[0].LaneStatus.Done != "scope read" || day.Items[1].LaneStatus.BlockerOwner != "LEAD" || day.Items[3].ID != latest.ID {
		t.Fatalf("inbox lost the full day: %s", body)
	}
	if status, body := do(t, srv, w.sender.ID, "POST", "/api/inbox/messages/"+latest.ID+"/ack", "", nil); status != 200 {
		t.Fatalf("ack %d %s", status, body)
	}
	now = now.Add(30 * time.Minute)
	page := read(w.admin, "silent_minutes=30&session="+first)
	if len(page.Items) != 0 {
		t.Fatal("silence threshold must be strictly greater")
	}
	now = now.Add(time.Second)
	status, body = report("/api/inbox/messages", "day-3", first, lastStatus)
	if status != 201 || mustJSON[Message](t, body).ID != latest.ID {
		t.Fatalf("exact retry changed identity: %d %s", status, body)
	}
	// An ordinary message and heartbeat do not reset the structured clock.
	in := sendInput{Recipient: w.sender.ID, Body: "plain chat", Key: "plain-chat", SenderSessionID: &first}
	if _, err := m.base.send(t.Context(), w.agent, in); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=clock_timestamp() WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	page = read(w.admin, "silent_minutes=30&session="+first)
	if len(page.Items) != 1 || page.Items[0].AgeSeconds == nil || *page.Items[0].AgeSeconds != 1801 || page.Items[0].SilentSeconds != 1801 || page.Items[0].MessageID == nil || *page.Items[0].MessageID != latest.ID {
		t.Fatalf("chat, heartbeat, ack or retry reset silence: %+v", page)
	}
	lastStatus.Next = "changed retry"
	if status, body = report("/api/inbox/messages", "day-3", first, lastStatus); status != 409 {
		t.Fatalf("divergent retry %d %s", status, body)
	}
	// A project send publishes a second lane for the same session, against a
	// workspace goal. The ticket lane remains independently silent.
	goalStatus := LaneStatus{TargetNodeID: goal, Done: "goal outcome", Next: "next outcome"}
	path := "/api/projects/" + project + "/messages"
	status, body = report(path, "goal-status", first, goalStatus)
	if status != 201 {
		t.Fatalf("project goal status %d %s", status, body)
	}
	goalMessage := mustJSON[CompatMessage](t, body)
	if goalMessage.Kind != "status" || goalMessage.LaneStatus == nil || goalMessage.LaneStatus.TargetNodeID != goal || goalMessage.LaneStatus.ETA != nil {
		t.Fatalf("project status fields lost: %s", body)
	}
	goalPage := read(w.admin, "target="+goal)
	if len(goalPage.Items) != 1 || *goalPage.Items[0].MessageID != goalMessage.ID || goalPage.Items[0].Status.ETA != nil {
		t.Fatalf("goal lane missing: %+v", goalPage)
	}
	// Pagination retains each session/target pair exactly once, including the
	// lane that has never reported. No result is silently truncated.
	seen := map[string]bool{}
	after := ""
	for range 4 {
		query := "limit=1"
		if after != "" {
			query += "&after=" + url.QueryEscape(after)
		}
		page := read(w.admin, query)
		for _, item := range page.Items {
			key := item.SessionID + "/" + item.TargetNodeID
			if seen[key] {
				t.Fatal("duplicate lane after cursor")
			}
			seen[key] = true
		}
		after = page.NextAfter
		if after == "" {
			break
		}
	}
	if len(seen) != 3 {
		t.Fatalf("pagination lost lanes: %v", seen)
	}
	if got := read(w.outsider, ""); len(got.Items) != 0 {
		t.Fatal("foreign tenant saw lane status")
	}
	// Invalid status must fail for its intended reason and never create a lane.
	if status, body = report("/api/inbox/messages", "foreign-session", foreign, goalStatus); status != 404 {
		t.Fatalf("foreign-owned sender session %d %s", status, body)
	}
	bad := goalStatus
	bad.Blocker = "unowned blocker"
	if status, body = report("/api/inbox/messages", "unowned-blocker", first, bad); status != 400 || !strings.Contains(string(body), "owner") {
		t.Fatalf("unowned blocker wrong failure: %d %s", status, body)
	}
	bad = goalStatus
	bad.Done = strings.Repeat("x", 2001)
	if status, body = report(path, "too-large", first, bad); status != 400 || !strings.Contains(string(body), "lane_status.done") {
		t.Fatalf("oversized report wrong failure: %d %s", status, body)
	}
	for _, query := range []string{"limit=101", "limit=0", "silent_minutes=0", "silent_minutes=525601", "after=bad", "target=bad", "session=bad"} {
		if status, body := do(t, srv, w.admin.ID, "GET", "/api/inbox/lanes?"+query, "", nil); status != 400 {
			t.Fatalf("invalid lane filter %s: %d %s", query, status, body)
		}
	}
	// Force failure after the projection update, proving both writes roll back.
	if _, err := w.db.Admin.Exec(t.Context(), `CREATE FUNCTION lane_reject_message() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.lane_status IS NOT NULL THEN RAISE EXCEPTION 'injected message failure' USING ERRCODE='XX000'; END IF; RETURN NEW; END $$; CREATE TRIGGER lane_reject BEFORE INSERT ON inbox_messages FOR EACH ROW EXECUTE FUNCTION lane_reject_message()`); err != nil {
		t.Fatal(err)
	}
	if status, body = report("/api/inbox/messages", "rollback", first, goalStatus); status != 500 {
		t.Fatalf("failed status reported success: %d %s", status, body)
	}
	goalPage = read(w.admin, "target="+goal)
	if len(goalPage.Items) != 1 || *goalPage.Items[0].MessageID != goalMessage.ID {
		t.Fatal("failed inbox write changed latest lane")
	}
	if _, err := w.db.Admin.Exec(t.Context(), `DROP TRIGGER lane_reject ON inbox_messages; DROP FUNCTION lane_reject_message()`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	if status, body = report("/api/inbox/messages", "ended", first, goalStatus); status != 409 || !strings.Contains(string(body), "session_ended") {
		t.Fatalf("ended sender session wrong failure: %d %s", status, body)
	}
	if page := read(w.admin, "session="+first); len(page.Items) != 0 {
		t.Fatal("stopped lane still needs a check-in")
	}
	// Reports never set target work or parent state, and never copy content
	// into tenant events. The day remains entirely in the durable inbox.
	var leaked bool
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM events WHERE after::text LIKE '%waiting for API decision%')`).Scan(&leaked); err != nil || leaked {
		t.Fatalf("private status content in audit event: %v", err)
	}
	var payload []byte
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT lane_status FROM inbox_messages WHERE id=$1`, latest.ID).Scan(&payload); err != nil || !json.Valid(payload) {
		t.Fatal("durable status history missing", err)
	}
}

// Admission completes before the barrier. The final write must consult the
// current role permissions under the access-change fence after revocation.
func testLaneStatusFinalWriteAuthority(t *testing.T) {
	for _, permission := range []string{"inbox.send", "nodes.read"} {
		t.Run(permission, func(t *testing.T) {
			w, m, project, _ := messagingWorld(t)
			w.agent.Scopes = []string{"inbox.send", "nodes.read"}
			token := insertKey(t, w.db, w.agent, w.agent.Scopes, "revoked-status-synthetic")
			session := messageTestSession(t, w, project, w.agent, "Revoked lane")
			var target string
			if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id)
 SELECT $1,id,'MSG-3','Revoked target',$2 FROM node_kinds WHERE slug='work' RETURNING id::text`, w.agent.TenantID, project).Scan(&target); err != nil {
					return err
				}
				var role string
				if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'lane_reporter','Lane reporter') RETURNING id::text`, w.agent.TenantID).Scan(&role); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, w.agent.TenantID, role, w.agent.Scopes); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1 AND scope_type='workspace'`, w.agent.ID, role)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var pause atomic.Bool
			admitted, resume := make(chan struct{}), make(chan struct{})
			var release sync.Once
			defer release.Do(func() { close(resume) })
			mux := http.NewServeMux()
			m.base.Mount(mux)
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				r = r.WithContext(tenant.WithPrincipal(r.Context(), w.agent))
				if pause.Load() && r.Method == "POST" {
					r.Body = &messagingAdmissionBody{ReadCloser: r.Body, pause: func() {
						close(admitted)
						select {
						case <-resume:
						case <-r.Context().Done():
						}
					}}
				}
				mux.ServeHTTP(rw, r)
			}))
			t.Cleanup(srv.Close)
			in := map[string]any{"kind": "status", "sender_session_id": session, "recipient_principal_id": w.sender.ID, "idempotency_key": "baseline", "lane_status": LaneStatus{TargetNodeID: target, Done: "valid control", Next: "next"}}
			status, body := compatPostAuth(t, srv, w.agent, "/api/inbox/messages", in, token)
			if status != 201 {
				t.Fatalf("valid report before revocation %d %s", status, body)
			}
			baseline := mustJSON[Message](t, body)
			in["idempotency_key"] = "revoked"
			pause.Store(true)
			type result struct {
				status int
				body   []byte
			}
			done := make(chan result, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			go func() {
				status, body := compatPostAuth(t, srv, w.agent, "/api/inbox/messages", in, token)
				done <- result{status, body}
			}()
			select {
			case <-admitted:
			case <-ctx.Done():
				t.Fatal("send did not complete admission")
			}
			if err := db.InTenant(dbtest.Seed(ctx), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, db.TenantFenceSQL); err != nil {
					return err
				}
				tag, err := tx.Exec(ctx, `DELETE FROM role_permissions rp USING roles r
 WHERE rp.tenant_id=r.tenant_id AND rp.role_id=r.id AND r.key='lane_reporter' AND rp.permission=$1`, permission)
				if err == nil && tag.RowsAffected() != 1 {
					return fmt.Errorf("revocation changed %d rows", tag.RowsAffected())
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			release.Do(func() { close(resume) })
			got := dbtest.Await(t, ctx, done)
			want := 403
			if permission == "nodes.read" {
				want = 404 // A now-hidden target is indistinguishable from absent.
			}
			if got.status != want {
				t.Fatalf("revoked %s returned %d: %s", permission, got.status, got.body)
			}
			var latest string
			var count int
			if err := w.db.Admin.QueryRow(t.Context(), `SELECT message_id::text FROM inbox_lane_statuses WHERE session_id=$1 AND target_node_id=$2`, session, target).Scan(&latest); err != nil || latest != baseline.ID {
				t.Fatalf("revoked report changed latest status: %s %v", latest, err)
			}
			if err := w.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM inbox_messages WHERE sender_principal_id=$1 AND idempotency_key='revoked'`, w.agent.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("revoked status committed: %d %v", count, err)
			}
		})
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func watchFixture(t *testing.T) (*fixture, string, attachwatch.DeviceRequest) {
	t.Helper()
	f := newFixture(t)
	p := f.propose("codex")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	project, ticket := uuid(t, f.db), uuid(t, f.db)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title) SELECT $1,$2,id,'WATCH-1','Watch' FROM node_kinds WHERE slug='project'`, f.tenantID, project)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title,parent_id) SELECT $1,$2,id,'WATCH-2','Watch ticket',$3 FROM node_kinds WHERE slug='ticket'`, f.tenantID, ticket, project)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	in := attachwatch.DeviceRequest{Operation: "request", RequestID: uuid(t, f.db), ComputerID: *v.ComputerID, DeviceProof: p.lifecycle, Snapshot: attachwatch.Snapshot{ComputerID: *v.ComputerID, ProjectID: project, TicketID: ticket, Host: "Test workstation", Harness: "codex", Process: attachwatch.Process{PID: 123, UID: 501, Started: "fixture-start", Executable: "/usr/local/bin/codex", CWD: "/tmp/pairing-fixture/repo"}, Transcript: "/tmp/fixture/transcript.jsonl", FileID: "1:234"}}
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	in.PollKey = nonce()
	in.Digest = in.Snapshot.Digest()
	f.call("POST", "/api/agent-pairing/attach", attachwatch.DeviceRequest{Operation: "register", ComputerID: in.ComputerID, DeviceProof: p.lifecycle, PollKey: in.PollKey}, false, key, 200)
	return f, key, in
}
func requestWatch(t *testing.T, f *fixture, key string, in attachwatch.DeviceRequest) attachwatch.View {
	t.Helper()
	var v attachwatch.View
	in.Digest = in.Snapshot.Digest()
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &v)
	if len(v.UserCode) != 9 || v.Digest != in.Snapshot.Digest() {
		t.Fatal("invalid code or digest")
	}
	return v
}
func activateWatch(t *testing.T, f *fixture, key string, in *attachwatch.DeviceRequest) attachwatch.View {
	t.Helper()
	v := requestWatch(t, f, key, *in)
	body := map[string]string{"request_digest": v.Digest}
	if in.Snapshot.Mode == attachwatch.ModeLease {
		body["consent_digest"] = v.ConsentDigest
		in.ConsentDigest = v.ConsentDigest
	}
	f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", body, true, "", 200)
	in.Operation = "poll"
	in.Digest = v.Digest
	in.Sequence = 1
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &v)
	if v.State != "active" || v.SessionID == nil || v.LeaseUntil == nil {
		t.Fatal("watch failed to activate")
	}
	return v
}
func TestAttachApprovalIdentityAndLease(t *testing.T) {
	f, key, in := watchFixture(t)
	v := requestWatch(t, f, key, in)
	path := "/api/agent-pairing/attach/" + in.RequestID + "/approve"
	body := map[string]string{"request_digest": v.Digest}
	f.call("POST", path, body, false, key, 403)
	r := f.request("POST", path, body, true, "")
	r.Header.Set("Origin", "https://other.test")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin approval")
	}
	f.call("POST", path, map[string]string{"request_digest": strings.Repeat("0", 64)}, true, "", 409)
	f.call("POST", path, body, true, "", 200)
	in.Operation = "poll"
	in.Digest = v.Digest
	in.Sequence = 1
	in.Snapshot.Process.Started = "reused-pid"
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 409)
	in.Snapshot.Process.Started = "fixture-start"
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
	// Fresh approval, atomic activation, then expiry cannot be renewed.
	in.RequestID = uuid(t, f.db)
	in.Operation = "request"
	in.Digest = ""
	v = activateWatch(t, f, key, &in)
	var sessions int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions WHERE id=$1`, *v.SessionID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatal("single session activation failed")
	}
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 409)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, in.RequestID); err != nil {
		t.Fatal(err)
	}
	in.Sequence++
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
	var reason string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT stop_reason FROM harness_sessions WHERE id=$1 AND stopped_at IS NOT NULL`, *v.SessionID).Scan(&reason); err != nil || !strings.Contains(reason, "unconfirmed") {
		t.Fatal("lease expiry falsely claimed process exit")
	}
}
func TestAttachProofScopeAndDefaultOff(t *testing.T) {
	f, key, in := watchFixture(t)
	bad := in
	bad.PollKey = nonce()
	f.call("POST", "/api/agent-pairing/attach", bad, false, key, 403)
	bad = in
	bad.Snapshot.Process.CWD = "/tmp/pairing-fixture-other"
	bad.Digest = bad.Snapshot.Digest()
	f.call("POST", "/api/agent-pairing/attach", bad, false, key, 400)
	v := activateWatch(t, f, key, &in)
	watchPath := "/api/projects/" + in.Snapshot.ProjectID + "/harness-sessions/" + *v.SessionID + "/watch"
	f.call("GET", watchPath, nil, true, "", 403)
	f.call("GET", watchPath, nil, false, key, 403)
	for _, role := range []string{"owner", "admin", "member", "viewer", "guest", "customer"} {
		perms, _ := authz.BuiltinPermissions(role)
		for _, p := range perms {
			if p == "harness.watch" {
				t.Fatalf("watch default on for %s", role)
			}
		}
	}
	p, _ := authz.Lookup("harness.watch")
	if p.AgentGrantable || !authz.ProjectGrantable("harness.watch") {
		t.Fatal("wrong view grant policy")
	}
	// A session is unmanaged and never gains inbox/control through attach.
	f.call("POST", "/api/projects/"+in.Snapshot.ProjectID+"/harness-sessions/"+*v.SessionID+"/drain", map[string]any{}, false, key, 403)
	// Tenant RLS does not reveal requests or session grants to another tenant.
	foreign := uuid(t, f.db)
	if err := db.InTenant(dbtest.Seed(context.Background()), f.db.App, foreign, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_attach_requests`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("cross tenant request exposure")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/revoke", nil, true, "", 200)
	in.Sequence++
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
}

func TestAttachAttemptCap(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 10; i++ {
		f.call("POST", "/api/agent-pairing/attach/lookup", map[string]string{"user_code": "000000000"}, true, "", 404)
	}
	f.call("POST", "/api/agent-pairing/attach/lookup", map[string]string{"user_code": "000000000"}, true, "", 429)
}

func TestAttachLiveStreamRevokesAndNeverStoresText(t *testing.T) {
	f, key, in := watchFixture(t)
	v := activateWatch(t, f, key, &in)
	second := in
	second.Operation = "request"
	second.RequestID = uuid(t, f.db)
	second.Snapshot.Process.PID++
	secondView := activateWatch(t, f, key, &second)
	if *secondView.SessionID == *v.SessionID {
		t.Fatal("two sessions on a principal collided")
	}
	role := uuid(t, f.db)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO roles(tenant_id,id,key,name) VALUES($1,$2,'explicit_watch','Explicit watch')`, f.tenantID, role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'harness.watch')`, f.tenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, f.tenantID, f.person, role, in.Snapshot.ProjectID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.h)
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/projects/"+in.Snapshot.ProjectID+"/harness-sessions/"+*v.SessionID+"/watch", nil)
	req.AddCookie(f.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("stream status %d", res.StatusCode)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		s := bufio.NewScanner(res.Body)
		for s.Scan() {
			select {
			case lines <- s.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	next := func(want string) {
		t.Helper()
		deadline := time.After(4 * time.Second)
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatal("stream ended early")
				}
				if strings.Contains(line, want) {
					return
				}
				if strings.Contains(line, "other-session-text") {
					t.Fatal("cross-session text")
				}
			case <-deadline:
				t.Fatal("stream event missing")
			}
		}
	}
	next("keepalive")
	if _, err = f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET last_poll=clock_timestamp()-interval '2 seconds' WHERE id=ANY($1::uuid[])`, []string{in.RequestID, second.RequestID}); err != nil {
		t.Fatal(err)
	}
	second.Sequence++
	second.Text = "other-session-text"
	f.call("POST", "/api/agent-pairing/attach", second, false, key, 200)
	in.Sequence++
	in.Text = "live-only-fixture-258"
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 200)
	next("live-only-fixture-258")
	var stored int
	err = f.db.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM events WHERE row_to_json(events)::text LIKE '%live-only-fixture-258%')+(SELECT count(*) FROM harness_sessions WHERE row_to_json(harness_sessions)::text LIKE '%live-only-fixture-258%')+(SELECT count(*) FROM harness_attach_requests WHERE row_to_json(harness_attach_requests)::text LIKE '%live-only-fixture-258%')`).Scan(&stored)
	if err != nil || stored != 0 {
		t.Fatal("conversation persisted")
	}
	if _, err = f.db.Admin.Exec(t.Context(), `DELETE FROM role_permissions WHERE tenant_id=$1 AND role_id=$2 AND permission='harness.watch'`, f.tenantID, role); err != nil {
		t.Fatal(err)
	}
	next("event: end")
}

func TestAttachCannotRenewChangedSession(t *testing.T) {
	for _, change := range []string{"archived_at=clock_timestamp(),phase='stopped',stopped_at=clock_timestamp(),recovery_process_state='unknown',recovery_request_id=gen_random_uuid(),recovery_request_digest='fixture',recovery_actor_id=(SELECT owner_id FROM harness_attach_requests WHERE session_id=harness_sessions.id),recovery_reason='fixture'", "phase='stopped',stopped_at=clock_timestamp()", "ticket_node_id=NULL,work_shape='unknown'"} {
		t.Run(change, func(t *testing.T) {
			f, key, in := watchFixture(t)
			v := activateWatch(t, f, key, &in)
			if _, err := f.db.Admin.Exec(t.Context(), "UPDATE harness_sessions SET "+change+" WHERE id=$1", *v.SessionID); err != nil {
				t.Fatal(err)
			}
			in.Sequence++
			f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
			var state string
			if err := f.db.Admin.QueryRow(t.Context(), "SELECT state FROM harness_attach_requests WHERE id=$1", in.RequestID).Scan(&state); err != nil || state != "detached" {
				t.Fatal("changed session retained approval")
			}
		})
	}
}

func TestAttachEnrollmentRemovalEndsApproval(t *testing.T) {
	f, key, in := watchFixture(t)
	activateWatch(t, f, key, &in)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_enrollments SET state='revoked' WHERE computer_id=$1`, in.ComputerID); err != nil {
		t.Fatal(err)
	}
	in.Sequence++
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 409)
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
}

func TestAttachPairingFileCannotRequestActivateRenewOrUpload(t *testing.T) {
	f, key, in := watchFixture(t)
	attack := func(original attachwatch.DeviceRequest) {
		t.Helper()
		// Pairing.json yields the runtime bearer and lifecycle proof, never the
		// daemon's memory-only poll key. Neither omission nor proof substitution works.
		for _, stolen := range []string{"", original.DeviceProof, nonce()} {
			bad := original
			bad.PollKey = stolen
			f.call("POST", "/api/agent-pairing/attach", bad, false, key, 403)
		}
	}
	attack(in)
	v := requestWatch(t, f, key, in)
	f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", map[string]string{"request_digest": v.Digest}, true, "", 200)
	in.Operation, in.Sequence = "poll", 1
	attack(in) // Cannot activate an owner-approved snapshot.
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &v)
	if v.State != "active" {
		t.Fatal("registered daemon did not activate")
	}
	oldLease := *v.LeaseUntil
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET last_poll=clock_timestamp()-interval '2 seconds' WHERE id=$1`, in.RequestID); err != nil {
		t.Fatal(err)
	}
	in.Sequence++
	attack(in) // Cannot renew without text either.
	in.Text = "forged transcript fixture"
	attack(in)
	var lease time.Time
	var sequence int64
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT lease_until,sequence FROM harness_attach_requests WHERE id=$1`, in.RequestID).Scan(&lease, &sequence); err != nil || !lease.Equal(oldLease) || sequence != 1 {
		t.Fatal("pairing-file attacker modified lease or upload sequence")
	}
	// The lifecycle proof is no longer necessary after registration.
	in.DeviceProof = ""
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 200)
}

func TestAttachDaemonRestartInvalidatesEveryApproval(t *testing.T) {
	for _, state := range []string{"pending", "approved", "active"} {
		t.Run(state, func(t *testing.T) {
			f, key, in := watchFixture(t)
			v := requestWatch(t, f, key, in)
			approval := "/api/agent-pairing/attach/" + in.RequestID + "/approve"
			if state != "pending" {
				f.call("POST", approval, map[string]string{"request_digest": v.Digest}, true, "", 200)
			}
			if state == "active" {
				in.Operation, in.Sequence = "poll", 1
				decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &v)
			}
			newKey := nonce()
			// A registration with a wrong lifecycle proof must not evict the daemon.
			registration := attachwatch.DeviceRequest{Operation: "register", ComputerID: in.ComputerID, DeviceProof: nonce(), PollKey: newKey}
			f.call("POST", "/api/agent-pairing/attach", registration, false, key, 403)
			registration.DeviceProof = in.DeviceProof
			f.call("POST", "/api/agent-pairing/attach", registration, false, key, 200)
			in.Operation, in.Sequence, in.Text = "poll", 2, ""
			f.call("POST", "/api/agent-pairing/attach", in, false, key, 403)
			in.PollKey = newKey
			f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
			f.call("POST", approval, map[string]string{"request_digest": v.Digest}, true, "", 410)
			if v.SessionID != nil {
				var stopped bool
				if err := f.db.Admin.QueryRow(t.Context(), `SELECT stopped_at IS NOT NULL FROM harness_sessions WHERE id=$1`, *v.SessionID).Scan(&stopped); err != nil || !stopped {
					t.Fatal("restart left the earlier session active")
				}
			}
			// Registering a key, including with stolen pairing credentials, never
			// transfers consent: a fresh request must wait for a fresh owner decision.
			in.Operation, in.RequestID = "request", uuid(t, f.db)
			fresh := requestWatch(t, f, key, in)
			in.Operation, in.Sequence = "poll", 1
			var waiting attachwatch.View
			decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &waiting)
			if fresh.State != "pending" || waiting.State != "pending" || waiting.SessionID != nil {
				t.Fatal("registration bypassed new owner approval")
			}
		})
	}
}

func TestAttachRegisteredKeyStillRequiresSnapshotDigest(t *testing.T) {
	f, key, in := watchFixture(t)
	in.Digest = ""
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 409)
	in.Digest = in.Snapshot.Digest()
	activateWatch(t, f, key, &in)
	in.Sequence++
	in.Digest = nonce()
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 409)
	in.Digest = in.Snapshot.Digest()
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
}

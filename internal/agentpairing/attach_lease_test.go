// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
	"github.com/jackc/pgx/v5"
)

func leaseFixture(t *testing.T) (*fixture, string, attachwatch.DeviceRequest) {
	t.Helper()
	f, key, in := watchFixture(t)
	in.Snapshot.Mode = attachwatch.ModeLease
	in.Snapshot.Transcript = ""
	in.Snapshot.FileID = ""
	in.Digest = in.Snapshot.Digest()
	return f, key, in
}
func TestAttachLeaseApprovalSingleUseAndActivationAtomic(t *testing.T) {
	f, key, in := leaseFixture(t)
	v := requestWatch(t, f, key, in)
	if delta := time.Until(v.ExpiresAt); delta < 9*time.Minute || delta > 10*time.Minute {
		t.Fatal("wrong code lifetime")
	}
	approval := "/api/agent-pairing/attach/" + in.RequestID + "/approve"
	body := map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}
	f.call("POST", approval, map[string]string{"request_digest": v.Digest}, true, "", 409)
	f.call("POST", approval, map[string]string{"request_digest": strings.Repeat("0", 64), "consent_digest": v.ConsentDigest}, true, "", 409)
	foreignOrigin := f.request("POST", approval, body, true, "")
	foreignOrigin.Header.Set("Origin", "https://elsewhere.test")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, foreignOrigin)
	if w.Code != 403 {
		t.Fatal("cross-origin approval")
	}
	f.call("POST", approval, body, false, key, 403)
	race := func(path string, body any, person bool, key string) {
		t.Helper()
		codes := make(chan int, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			req := f.request("POST", path, body, person, key)
			wg.Add(1)
			go func() { defer wg.Done(); w := httptest.NewRecorder(); f.h.ServeHTTP(w, req); codes <- w.Code }()
		}
		wg.Wait()
		close(codes)
		counts := map[int]int{}
		for code := range codes {
			counts[code]++
		}
		if counts[200] != 1 || counts[409] != 1 {
			t.Fatalf("atomic single use: %v", counts)
		}
	}
	race(approval, body, true, "")
	in.Operation = "poll"
	in.Sequence = 1
	var discovery attachwatch.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &discovery)
	if discovery.State != "approved" || discovery.SessionID != nil {
		t.Fatal("approval activated without consent pin")
	}
	in.ConsentDigest = v.ConsentDigest
	race("/api/agent-pairing/attach", in, false, key)
	f.call("POST", approval, body, true, "", 409)
	var count int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions WHERE id=(SELECT session_id FROM harness_attach_requests WHERE id=$1)`, in.RequestID).Scan(&count); err != nil || count != 1 {
		t.Fatal("activation duplicated session", err)
	}
	in.Sequence++
	in.Snapshot.Process.Started = "pid-reused"
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 409)
	in.Snapshot.Process.Started = "fixture-start"
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
}

func TestAttachLeaseSessionsRevokeIndependentlyAndExpireOffline(t *testing.T) {
	f, key, in := leaseFixture(t)
	first := activateWatch(t, f, key, &in)
	second := in
	second.RequestID = uuid(t, f.db)
	second.Operation = "request"
	second.ConsentDigest = ""
	second.Sequence = 0
	second.Snapshot.Process.PID++
	secondView := activateWatch(t, f, key, &second)
	if *first.SessionID == *secondView.SessionID {
		t.Fatal("sessions on same principal collided")
	}
	f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/revoke", nil, true, "", 200)
	var state string
	var stopped bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT a.state,s.stopped_at IS NOT NULL FROM harness_attach_requests a JOIN harness_sessions s ON s.id=a.session_id WHERE a.id=$1`, in.RequestID).Scan(&state, &stopped); err != nil || state != "detached" || !stopped {
		t.Fatal("offline revocation did not end row", err)
	}
	in.Sequence++
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET last_poll=clock_timestamp()-interval '2 seconds' WHERE id=$1`, second.RequestID); err != nil {
		t.Fatal(err)
	}
	second.Sequence++
	f.call("POST", "/api/agent-pairing/attach", second, false, key, 200)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, second.RequestID); err != nil {
		t.Fatal(err)
	}
	second.Sequence++
	f.call("POST", "/api/agent-pairing/attach", second, false, key, 410)
	var reason string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT a.state,s.stop_reason FROM harness_attach_requests a JOIN harness_sessions s ON s.id=a.session_id WHERE a.id=$1`, second.RequestID).Scan(&state, &reason); err != nil || state != "unreachable" || !strings.Contains(reason, "unconfirmed") {
		t.Fatal("expiry reported process exit", err)
	}
	f.call("POST", "/api/agent-pairing/attach/"+second.RequestID+"/approve", map[string]string{"request_digest": secondView.Digest, "consent_digest": secondView.ConsentDigest}, true, "", 410)
	// A late kernel report must not resurrect or rewrite an expired lease.
	second.Operation = "exited"
	f.call("POST", "/api/agent-pairing/attach", second, false, key, 410)
}

func TestAttachLeaseRejectsTextViewerAndChangedScope(t *testing.T) {
	f, key, in := leaseFixture(t)
	bad := in
	bad.Snapshot.Process.CWD = "/tmp/pairing-fixture-other"
	bad.Digest = bad.Snapshot.Digest()
	f.call("POST", "/api/agent-pairing/attach", bad, false, key, 400)
	bad = in
	bad.PollKey = nonce()
	f.call("POST", "/api/agent-pairing/attach", bad, false, key, 403)
	bad = in
	bad.Snapshot.Transcript = "/tmp/never-read"
	bad.Digest = bad.Snapshot.Digest()
	f.call("POST", "/api/agent-pairing/attach", bad, false, key, 400)
	v := activateWatch(t, f, key, &in)
	// Even an explicit viewing grant cannot turn status-only consent into a stream.
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT tenant_id,id,'harness.watch' FROM roles WHERE key='owner'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.call("GET", "/api/projects/"+in.Snapshot.ProjectID+"/harness-sessions/"+*v.SessionID+"/watch", nil, true, "", 410)
	in.Sequence++
	in.Text = "AEON352_FORBIDDEN_CONVERSATION"
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 400)
	var stored int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM events WHERE row_to_json(events)::text LIKE '%AEON352_FORBIDDEN_CONVERSATION%')+(SELECT count(*) FROM harness_sessions WHERE row_to_json(harness_sessions)::text LIKE '%AEON352_FORBIDDEN_CONVERSATION%')+(SELECT count(*) FROM harness_attach_requests WHERE row_to_json(harness_attach_requests)::text LIKE '%AEON352_FORBIDDEN_CONVERSATION%')`).Scan(&stored); err != nil || stored != 0 {
		t.Fatal("text persisted", err)
	}
	in.Text = ""
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
	// Approval is not authority after the tenant/computer allowlist changes.
	in.Operation = "request"
	in.RequestID = uuid(t, f.db)
	in.Sequence = 0
	in.ConsentDigest = ""
	v = requestWatch(t, f, key, in)
	f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 200)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_requests SET details=jsonb_set(details,'{workspace_path}','"/other"') WHERE id=(SELECT request_id FROM agent_pairing_computers WHERE id=$1)`, in.ComputerID); err != nil {
		t.Fatal(err)
	}
	in.Operation = "poll"
	in.Sequence = 1
	in.ConsentDigest = v.ConsentDigest
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 403)
}

func TestAttachLeaseTenantIsolationAndConfirmedExit(t *testing.T) {
	f, key, in := leaseFixture(t)
	v := activateWatch(t, f, key, &in)
	foreign, err := tenantbootstrap.Create(t.Context(), f.db.App, "lease-foreign", "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	am, err := auth.New(auth.Config{Env: "dev", PublicURL: origin, SessionKey: []byte(nonce()), BootstrapTenantSlug: "lease-foreign", BootstrapAdminEmail: "foreign@example.test"}, f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	api := &httpapi.Server{Pool: f.db.App, Modules: []httpapi.Module{am, agentpairing.New(f.db.App, origin, "lease-foreign")}, Middleware: []func(http.Handler) http.Handler{am.Middleware}}
	other := &fixture{t: t, db: f.db, h: api.Handler(), tenantID: foreign}
	login := other.call("POST", "/api/auth/dev-login", map[string]string{"email": "foreign@example.test"}, false, "", 200)
	other.cookie = login.Result().Cookies()[0]
	var code string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT user_code FROM harness_attach_requests WHERE id=$1`, in.RequestID).Scan(&code); err != nil {
		t.Fatal(err)
	}
	other.call("POST", "/api/agent-pairing/attach/lookup", map[string]string{"user_code": code}, true, "", 404)
	other.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 404)
	other.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/revoke", nil, true, "", 404)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, foreign, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM harness_attach_requests)+(SELECT count(*) FROM harness_attach_limits)+(SELECT count(*) FROM harness_sessions)`).Scan(&count); err != nil {
			return err
		}
		// Foreign lookup creates its own attempt bucket; no foreign session/request is visible.
		if count != 1 {
			t.Fatalf("RLS visible count %d", count)
		}
		tag, err := tx.Exec(t.Context(), `UPDATE harness_attach_requests SET state='detached' WHERE id=$1`, in.RequestID)
		if err == nil && tag.RowsAffected() != 0 {
			t.Fatal("cross-tenant mutation")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	in.Operation = "exited"
	var ended attachwatch.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &ended)
	if ended.State != "confirmed_exited" || ended.LeaseUntil != nil {
		t.Fatal("exit not terminal")
	}
	var detail struct {
		Watch struct {
			State string `json:"state"`
			Mode  string `json:"mode"`
		} `json:"watch"`
	}
	decodeResult(t, f.call("GET", "/api/projects/"+in.Snapshot.ProjectID+"/harness-sessions/"+*v.SessionID, nil, true, "", 200), &detail)
	if detail.Watch.State != "confirmed_exited" || detail.Watch.Mode != "lease" {
		t.Fatal("truthful metadata status not projected")
	}
	in.Operation = "poll"
	in.Sequence++
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
}

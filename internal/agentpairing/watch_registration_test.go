// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type registrationClock struct {
	now   time.Time
	waits int
}

func (c *registrationClock) Now() time.Time { return c.now }
func (c *registrationClock) Wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.now = c.now.Add(d)
	c.waits++
	return nil
}

type registrationRoundTrip func(*http.Request) (*http.Response, error)

func (f registrationRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the real HTTP decoder, authentication, full module set and daemon
// recovery transport, retaining this same transport across the server restart.
func fixtureAttachTransport(f *fixture, key string, in attachwatch.DeviceRequest, clock *registrationClock, registrations *int) *agentd.AttachTransport {
	c := client.New(origin, key)
	c.HTTP.Transport = registrationRoundTrip(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		return w.Result(), nil
	})
	return agentd.NewAttachTransport(func(ctx context.Context, request attachwatch.DeviceRequest) (attachwatch.View, error) {
		request.PollKey = in.PollKey
		request.DeviceProof = "" // ordinary daemon exchanges carry no lifecycle proof
		var v attachwatch.View
		err := c.Do(ctx, "POST", "/api/agent-pairing/attach", request, &v)
		return v, err
	}, func(ctx context.Context) error {
		*registrations++
		var v attachwatch.View
		err := c.Do(ctx, "POST", "/api/agent-pairing/attach", attachwatch.DeviceRequest{Operation: "register", ComputerID: in.ComputerID, DeviceProof: in.DeviceProof, PollKey: in.PollKey, AttachProtocol: attachwatch.Protocol, LocalConsentProofVersion: attachwatch.LocalConsentProofVersion}, &v)
		if err == nil && (v.State != "registered" || v.LocalConsentProofVersion != attachwatch.LocalConsentProofVersion) {
			return errors.New("invalid registration acknowledgement")
		}
		return err
	}, clock)
}

func TestAttachTransportRecoversAfterFullServerRestart(t *testing.T) {
	for _, mode := range []string{"watch", "lease"} {
		t.Run(mode, func(t *testing.T) {
			f, key, in := watchFixture(t)
			if mode == "lease" {
				in.Snapshot.Mode, in.Snapshot.Transcript, in.Snapshot.FileID = attachwatch.ModeLease, "", ""
				in.Digest = in.Snapshot.Digest()
			}
			old := in
			active := activateWatch(t, f, key, &old)
			registrations := 0
			clock := &registrationClock{}
			transport := fixtureAttachTransport(f, key, in, clock, &registrations)
			f.rebuildHandler()
			// A new attach gets recovery and still waits for explicit approval.
			in.RequestID = uuid(t, f.db)
			v, err := transport.Exchange(t.Context(), in)
			if err != nil || v.State != "pending" || v.SessionID != nil || len(v.UserCode) != 9 || registrations != 1 || clock.waits != 1 {
				t.Fatal("new request did not recover registration and wait for consent", err)
			}
			var state, phase string
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.state,s.phase FROM harness_attach_requests r JOIN harness_sessions s ON s.id=r.session_id WHERE r.id=$1 AND s.id=$2`, old.RequestID, *active.SessionID).Scan(&state, &phase); err != nil || state != "detached" || phase != "stopped" {
				t.Fatal("recovery preserved previous approval", err)
			}
			// Old polls never regain their lease, even with the recovered key.
			if _, err := transport.Exchange(t.Context(), old); err == nil {
				t.Fatal("old watch resumed without approval")
			}
			f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 200)
			in.Operation, in.ConsentDigest, in.Sequence = "poll", v.ConsentDigest, 1
			v, err = transport.Exchange(t.Context(), in)
			if err != nil || v.State != "active" || v.SessionID == nil || *v.SessionID == *active.SessionID {
				t.Fatal("freshly approved attach did not activate", err)
			}
			if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET last_poll=clock_timestamp()-interval '2 seconds' WHERE id=$1`, in.RequestID); err != nil {
				t.Fatal(err)
			}
			in.Sequence++
			if mode == "watch" {
				in.Text = "A new fixture turn after fresh approval."
			}
			v, err = transport.Exchange(t.Context(), in)
			if err != nil || v.State != "active" || registrations != 1 {
				t.Fatal("recovered watch/lease poll failed", err)
			}
		})
	}
}

func TestRecoveryRegistrationRechecksPermissionInsideWrite(t *testing.T) {
	f, key, in := watchFixture(t)
	active := activateWatch(t, f, key, &in)
	var principal string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT principal_id::text FROM agent_pairing_computers WHERE id=$1`, in.ComputerID).Scan(&principal); err != nil {
		t.Fatal(err)
	}
	// Pause acquisition of the final write transaction, after the route's
	// preliminary Require has succeeded. No elapsed-time assumption is needed.
	entered, release := make(chan struct{}), make(chan struct{})
	var acquisitions atomic.Int32
	config, err := pgxpool.ParseConfig(f.db.AppURL)
	if err != nil {
		t.Fatal("fixture pool configuration unavailable")
	}
	config.BeforeAcquire = func(ctx context.Context, _ *pgx.Conn) bool {
		if acquisitions.Add(1) == 2 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return false
			}
		}
		return true
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("fixture pool unavailable")
	}
	defer pool.Close()
	defer cancel() // unblock acquisition before closing the pool on a failure
	handler := (&httpapi.Server{Pool: pool, Modules: []httpapi.Module{agentpairing.New(pool, origin, f.tenantSlug)}}).Handler()
	registration := attachwatch.DeviceRequest{Operation: "register", ComputerID: in.ComputerID, DeviceProof: in.DeviceProof, PollKey: nonce(), AttachProtocol: attachwatch.Protocol, LocalConsentProofVersion: attachwatch.LocalConsentProofVersion}
	r := f.request("POST", "/api/agent-pairing/attach", registration, false, key)
	r = r.WithContext(tenant.WithPrincipal(ctx, tenant.Principal{ID: principal, TenantID: f.tenantID, Kind: tenant.Agent, Scopes: agentpairing.RuntimePermissions, KeyCreatorID: f.person}))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		done <- w
	}()
	select {
	case <-entered:
	case w := <-done:
		t.Fatalf("registration returned before the write barrier: %d", w.Code)
	case <-ctx.Done():
		t.Fatal("registration did not reach the write barrier")
	}
	if err := db.InTenant(ctx, f.db.Admin, f.tenantID, func(tx pgx.Tx) error {
		if err := agentpairing.LockMutation(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.tenantID, principal)
		return err
	}); err != nil {
		t.Fatal("fixture permission revocation failed")
	}
	close(release)
	select {
	case w := <-done:
		var body struct{ Code, Error string }
		decodeResult(t, w, &body)
		if w.Code != http.StatusForbidden || body.Code != "forbidden" || body.Error != "paired daemon required" {
			t.Fatalf("registration bypassed current permissions: status=%d code=%q", w.Code, body.Code)
		}
	case <-ctx.Done():
		t.Fatal("registration did not finish after permission revocation")
	}
	var state, phase string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.state,s.phase FROM harness_attach_requests r JOIN harness_sessions s ON s.id=r.session_id WHERE r.id=$1 AND s.id=$2`, in.RequestID, *active.SessionID).Scan(&state, &phase); err != nil || state != "active" || phase != "working" {
		t.Fatal("refused registration mutated the retained watch", err)
	}
}

func TestMissingRegistrationDoesNotOverrideRefusals(t *testing.T) {
	for _, cause := range []string{"wrong key", "other computer", "wrong tenant", "revoked bearer", "revoked computer", "draining computer", "draining harness", "removed harness", "revoked watch", "invalid snapshot", "missing ticket"} {
		t.Run(cause, func(t *testing.T) {
			f, key, in := watchFixture(t)
			if cause == "other computer" {
				key, _ = extraWatchComputer(t, f, in)
			}
			if cause == "revoked watch" {
				activateWatch(t, f, key, &in)
				f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/revoke", nil, true, "", 200)
			}
			if cause == "wrong key" {
				in.PollKey = nonce()
			} else {
				f.rebuildHandler()
			}
			sql := ""
			switch cause {
			case "revoked bearer":
				sql = `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE principal_id=(SELECT principal_id FROM agent_pairing_computers WHERE id=$1)`
			case "revoked computer":
				sql = `UPDATE agent_pairing_computers SET state='revoked' WHERE id=$1`
			case "draining computer":
				sql = `UPDATE agent_pairing_computers SET state='draining' WHERE id=$1`
			case "draining harness":
				sql = `UPDATE agent_pairing_enrollments SET state='draining' WHERE computer_id=$1`
			case "removed harness":
				sql = `UPDATE agent_pairing_enrollments SET state='revoked' WHERE computer_id=$1`
			case "invalid snapshot":
				in.Snapshot.ComputerID = uuid(t, f.db)
				in.Digest = in.Snapshot.Digest()
			case "missing ticket":
				in.Snapshot.TicketID = uuid(t, f.db)
				in.Digest = in.Snapshot.Digest()
			case "wrong tenant":
				// Keep the target row in the same database: RLS and the pairing
				// fence, not a missing fixture row, must prevent recovery.
				other := newFixtureInTenant(t, f.db, "recovery-foreign")
				key, _ = extraWatchComputer(t, other, in)
			}
			if sql != "" {
				if _, err := f.db.Admin.Exec(t.Context(), sql, in.ComputerID); err != nil {
					t.Fatal(err)
				}
			}
			registrations := 0
			clock := &registrationClock{}
			transport := fixtureAttachTransport(f, key, in, clock, &registrations)
			_, err := transport.Exchange(t.Context(), in)
			var status *client.StatusError
			if !errors.As(err, &status) || status.AttachRefusal == attachwatch.RefusalPollKeyUnknown || registrations != 0 || clock.waits != 0 {
				t.Fatal("refusal triggered recovery or lacked a real HTTP refusal")
			}
			wantStatus := http.StatusForbidden
			if cause == "revoked bearer" || cause == "revoked computer" {
				wantStatus = http.StatusUnauthorized
			}
			if status.Status != wantStatus {
				t.Fatalf("expected refusal status %d, got %d", wantStatus, status.Status)
			}
		})
	}
}

func TestMissingRegistrationUsesRecoveryBudgetBeforeEligibilityReads(t *testing.T) {
	for _, operation := range []string{"request", "poll", "detach", "exited"} {
		t.Run(operation, func(t *testing.T) {
			f, key, in := watchFixture(t)
			// Keep the retained watch live for non-request diagnostics.
			if operation != "request" {
				requestWatch(t, f, key, in)
			}
			f.rebuildHandler()
			in.Operation = operation
			// Exhaust all but the final recovery slot without touching request quotas.
			for attempt := 0; attempt < 29; attempt++ {
				f.call("POST", "/api/agent-pairing/attach", in, false, key, 403)
			}
			w := f.call("POST", "/api/agent-pairing/attach", in, false, key, 403)
			var refusal struct {
				AttachRefusal string `json:"attach_refusal"`
			}
			decodeResult(t, w, &refusal)
			if refusal.AttachRefusal != attachwatch.RefusalPollKeyUnknown {
				t.Fatal("eligible call did not get the recovery signal")
			}
			// An unavailable scope would normally produce an opaque 403. The
			// full bucket must instead refuse before those eligibility reads.
			in.Snapshot.TicketID = uuid(t, f.db)
			in.Digest = in.Snapshot.Digest()
			w = f.call("POST", "/api/agent-pairing/attach", in, false, key, 429)
			var capped struct {
				Error         string
				Code          string
				AttachRefusal string `json:"attach_refusal"`
			}
			decodeResult(t, w, &capped)
			retry, parseErr := strconv.Atoi(w.Header().Get("Retry-After"))
			if capped.Code != "attach_recovery_limited" || capped.Error == "" || capped.AttachRefusal != "" || parseErr != nil || retry < 1 || retry > 60 {
				t.Fatal("full recovery bucket allowed diagnostics, recovery or lacked a retry delay")
			}
			wantAttempts := 0
			if operation != "request" {
				wantAttempts = 1 // retained request created before the restart
			}
			var attempts int
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT coalesce((SELECT attempts FROM harness_attach_limits WHERE tenant_id=$1 AND bucket='request'),0)`, f.tenantID).Scan(&attempts); err != nil || attempts != wantAttempts {
				t.Fatal("unknown-key calls spent the tenant request budget")
			}
		})
	}
}

func TestMissingRegistrationRecoveryDoesNotCoupleComputersOrNewAttaches(t *testing.T) {
	f, key, in := watchFixture(t)
	type computer struct {
		key string
		in  attachwatch.DeviceRequest
	}
	computers := []computer{{key, in}}
	for i := 1; i < 32; i++ {
		// Provision the retained computers across two pairing lookup windows;
		// their later recovery still happens together after one server restart.
		if i == 29 {
			if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_limits SET starts_at=clock_timestamp()-interval '11 minutes' WHERE tenant_id=$1 AND bucket='lookup'`, f.tenantID); err != nil {
				t.Fatal(err)
			}
		}
		k, next := extraWatchComputer(t, f, in)
		next.RequestID = uuid(t, f.db)
		next.Digest = next.Snapshot.Digest()
		computers = append(computers, computer{k, next})
	}
	// Create retained live requests across two explicit tenant request windows.
	for i, c := range computers {
		if i == 29 {
			if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_limits SET starts_at=clock_timestamp()-interval '11 minutes' WHERE tenant_id=$1 AND bucket='request'`, f.tenantID); err != nil {
				t.Fatal(err)
			}
		}
		requestWatch(t, f, c.key, c.in)
	}
	var before int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT attempts FROM harness_attach_limits WHERE tenant_id=$1 AND bucket='request'`, f.tenantID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	f.rebuildHandler()
	// Repeated calls by one authenticated principal cannot exhaust another
	// computer's budget, even when it rotates the submitted computer ID.
	hostile := computers[0]
	hostile.in.Operation = "poll"
	for i := 0; i < 30; i++ {
		f.call("POST", "/api/agent-pairing/attach", hostile.in, false, hostile.key, 403)
	}
	spoof := computers[1].in
	spoof.Operation = "poll"
	w := f.call("POST", "/api/agent-pairing/attach", spoof, false, hostile.key, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("hostile principal did not get bounded retry guidance")
	}
	for i, c := range computers[1:] {
		c.in.Operation = []string{"poll", "detach", "exited"}[i%3]
		w := f.call("POST", "/api/agent-pairing/attach", c.in, false, c.key, 403)
		var refusal struct {
			AttachRefusal string `json:"attach_refusal"`
		}
		decodeResult(t, w, &refusal)
		if refusal.AttachRefusal != attachwatch.RefusalPollKeyUnknown {
			t.Fatal("one computer prevented another from recovering")
		}
	}
	var after int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT attempts FROM harness_attach_limits WHERE tenant_id=$1 AND bucket='request'`, f.tenantID).Scan(&after); err != nil || after != before {
		t.Fatal("recovery spent the tenant request budget")
	}
	// Normal registration and a new attach still work after 31 recoveries.
	c := computers[1]
	f.call("POST", "/api/agent-pairing/attach", attachwatch.DeviceRequest{Operation: "register", ComputerID: c.in.ComputerID, DeviceProof: c.in.DeviceProof, PollKey: c.in.PollKey, AttachProtocol: attachwatch.Protocol, LocalConsentProofVersion: attachwatch.LocalConsentProofVersion}, false, c.key, 200)
	c.in.RequestID = uuid(t, f.db)
	requestWatch(t, f, c.key, c.in)
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT attempts FROM harness_attach_limits WHERE tenant_id=$1 AND bucket='request'`, f.tenantID).Scan(&after); err != nil || after != before+1 {
		t.Fatal("new attach did not charge exactly one normal request attempt")
	}
}

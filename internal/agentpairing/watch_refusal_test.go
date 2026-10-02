// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

func refusalCause(t *testing.T, w *httptest.ResponseRecorder, cause string) {
	t.Helper()
	var body struct {
		Code, Error string
		Cause       string `json:"attach_refusal"`
	}
	decodeResult(t, w, &body)
	if body.Cause != cause || body.Code == "" || body.Error == "" {
		t.Fatalf("wrong refusal: code=%q cause=%q", body.Code, body.Cause)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("refusal was cacheable")
	}
}

func TestAttachRefusalAuthenticatesProofAndPrincipalBeforeVersion(t *testing.T) {
	f, key, in := watchFixture(t)
	otherKey, _ := extraWatchComputer(t, f, in)
	registration := attachwatch.DeviceRequest{Operation: "register", AttachProtocol: attachwatch.Protocol, LocalConsentProofVersion: 1, ComputerID: in.ComputerID, DeviceProof: nonce(), PollKey: nonce()}
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", registration, false, key, 403), "")
	registration.DeviceProof = in.DeviceProof
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", registration, false, otherKey, 403), "")
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", registration, false, key, 409), attachwatch.RefusalVersion)
	// A failed upgrade did not replace the old memory-only poll authority.
	requestWatch(t, f, key, in)
	registration.AttachProtocol = 1
	registration.LocalConsentProofVersion = 0
	f.call("POST", "/api/agent-pairing/attach", registration, false, key, 200)
	in.PollKey = registration.PollKey
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, otherKey, 403), "")
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 409), attachwatch.RefusalVersion)
}

func TestAttachTicketRefusalIsOpaqueWithoutOwningDaemon(t *testing.T) {
	f, key, in := watchFixture(t)
	otherKey, _ := extraWatchComputer(t, f, in)
	actualTicket := in.Snapshot.TicketID
	in.Snapshot.TicketID = uuid(t, f.db)
	in.Digest = in.Snapshot.Digest()
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 409), attachwatch.RefusalTicket)
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, otherKey, 403), "")
	bad := in
	bad.PollKey = nonce()
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", bad, false, key, 403), "")
	// A missing and an existing ticket yield identical unauthenticated refusals.
	var prior *httptest.ResponseRecorder
	for _, ticket := range []string{in.Snapshot.TicketID, actualTicket} {
		in.Snapshot.TicketID = ticket
		in.Digest = in.Snapshot.Digest()
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, f.request("POST", "/api/agent-pairing/attach", in, false, ""))
		if w.Code != 401 && w.Code != 403 {
			t.Fatal("unauthenticated attach accepted")
		}
		var body map[string]any
		decodeResult(t, w, &body)
		if _, exists := body["attach_refusal"]; exists {
			t.Fatal("unauthenticated caller received an owner cause")
		}
		if prior != nil && (w.Code != prior.Code || w.Body.String() != prior.Body.String()) {
			t.Fatal("unauthenticated ticket probe changed the refusal")
		}
		prior = w
	}
	// An enrollment failure does not falsely blame ticket visibility.
	in.Snapshot.TicketID = actualTicket
	in.Snapshot.Harness = "claude"
	in.Digest = in.Snapshot.Digest()
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 409), attachwatch.RefusalEnrollment)
}

func TestAttachStatusOnlyClaudeDrainRefusal(t *testing.T) {
	for _, state := range []string{"draining", "revoked"} {
		t.Run(state, func(t *testing.T) {
			f, key, in := watchFixtureWithHarness(t, "", "claude")
			in.Snapshot.Mode, in.Snapshot.Transcript, in.Snapshot.FileID = attachwatch.ModeLease, "", ""
			in.Digest = in.Snapshot.Digest()
			otherKey, _ := extraWatchComputer(t, f, in)
			// A running external process is represented by the same pinned snapshot
			// throughout. Only its server enrollment state changes.
			if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_enrollments SET state=$2 WHERE computer_id=$1`, in.ComputerID, state); err != nil {
				t.Fatal(err)
			}
			cause := attachwatch.RefusalDraining
			if state == "revoked" {
				cause = attachwatch.RefusalEnrollment
			}
			w := f.call("POST", "/api/agent-pairing/attach", in, false, key, 409)
			refusalCause(t, w, cause)
			var body struct{ Code, Error string }
			decodeResult(t, w, &body)
			if body.Code != "conflict" || body.Error != "project, ticket or harness enrollment changed" {
				t.Fatal("legacy refusal contract changed")
			}
			refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, otherKey, 403), "")
			bad := in
			bad.PollKey = nonce()
			refusalCause(t, f.call("POST", "/api/agent-pairing/attach", bad, false, key, 403), "")
			var count int
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM harness_attach_requests WHERE id=$1`, in.RequestID).Scan(&count); err != nil || count != 0 {
				t.Fatal("refused attach created a request", err)
			}
			// Model fresh authorized enrollment, not a daemon-side fence bypass.
			if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_enrollments SET state='connected' WHERE computer_id=$1`, in.ComputerID); err != nil {
				t.Fatal(err)
			}
			v := activateWatch(t, f, key, &in)
			if v.State != "active" || v.SessionID == nil || v.Snapshot.Mode != attachwatch.ModeLease {
				t.Fatal("fresh approval did not activate status-only attach")
			}
		})
	}
}

func TestAttachServerRestartLosesRegistrationBeforeEnrollmentCheck(t *testing.T) {
	f, key, in := leaseFixture(t)
	// Rebuild the HTTP server/module over the same database, preserving the
	// ordinary bearer/cookie authentication while discarding only process memory.
	am, err := auth.New(auth.Config{Env: "dev", PublicURL: origin, SessionKey: f.sessionKey, BootstrapTenantSlug: "pairtest", BootstrapAdminEmail: "pairing@example.test"}, f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	f.pairing = agentpairing.New(f.db.App, origin, "pairtest")
	f.h = (&httpapi.Server{Pool: f.db.App, Modules: []httpapi.Module{am, f.pairing}, Middleware: []func(http.Handler) http.Handler{am.Middleware}}).Handler()
	// Even with a connected enrollment, the first guard is the lost poll key.
	for _, state := range []string{"connected", "draining"} {
		if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_enrollments SET state=$2 WHERE computer_id=$1`, in.ComputerID, state); err != nil {
			t.Fatal(err)
		}
		w := f.call("POST", "/api/agent-pairing/attach", in, false, key, 403)
		refusalCause(t, w, "")
		var body struct{ Code, Error string }
		decodeResult(t, w, &body)
		if body.Code != "forbidden" || body.Error != "daemon poll key rejected" {
			t.Fatal("server restart did not reproduce the missing-registration refusal")
		}
	}
	// A daemon restart's registration restores only its poll authority. It
	// cannot override drain or revive any approval.
	in.PollKey = nonce()
	f.call("POST", "/api/agent-pairing/attach", attachwatch.DeviceRequest{Operation: "register", ComputerID: in.ComputerID, DeviceProof: in.DeviceProof, PollKey: in.PollKey, AttachProtocol: attachwatch.Protocol, LocalConsentProofVersion: attachwatch.LocalConsentProofVersion}, false, key, 200)
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 409), attachwatch.RefusalDraining)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_enrollments SET state='connected' WHERE computer_id=$1`, in.ComputerID); err != nil {
		t.Fatal(err)
	}
	v := requestWatch(t, f, key, in)
	if v.State != "pending" || v.SessionID != nil {
		t.Fatal("restart reused approval or failed to request fresh consent")
	}
}

func TestAttachComputerDrainCauseRequiresOwningPollKey(t *testing.T) {
	f, key, in := leaseFixture(t)
	otherKey, _ := extraWatchComputer(t, f, in)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_computers SET state='draining' WHERE id=$1`, in.ComputerID); err != nil {
		t.Fatal(err)
	}
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 403), attachwatch.RefusalDraining)
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, otherKey, 403), "")
	in.PollKey = nonce()
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 403), "")
}

func TestAttachCodeExpiryCausePreservesTheEndedRequest(t *testing.T) {
	f, key, in := watchFixture(t)
	requestWatch(t, f, key, in)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, in.RequestID); err != nil {
		t.Fatal(err)
	}
	in.Operation = "poll"
	in.Sequence = 1
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 410), attachwatch.RefusalExpired)
	var state string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT state FROM harness_attach_requests WHERE id=$1 AND session_id IS NULL`, in.RequestID).Scan(&state); err != nil || state != "unreachable" {
		t.Fatal("expiry was not durable or created a session")
	}
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 410), "")
}

func TestAttachRevokedPairingCauseRequiresRetainedAuthentication(t *testing.T) {
	f, key, in := watchFixture(t)
	otherKey, _ := extraWatchComputer(t, f, in)
	var principal string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT principal_id::text FROM agent_pairing_computers WHERE id=$1`, in.ComputerID).Scan(&principal); err != nil {
		t.Fatal(err)
	}
	// Model the race after the computer state commits but before auth middleware
	// rejects its credential. No revoked credential is made usable by attach.
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_computers SET state='revoked' WHERE id=$1`, in.ComputerID); err != nil {
		t.Fatal(err)
	}
	// The complete HTTP path refuses a revoked computer even before its key
	// itself is marked revoked, and never discloses a cause.
	w := f.call("POST", "/api/agent-pairing/attach", in, false, key, 401)
	var body map[string]any
	decodeResult(t, w, &body)
	if _, exists := body["attach_refusal"]; exists {
		t.Fatal("revoked computer passed HTTP authentication")
	}
	refusalCause(t, f.call("POST", "/api/agent-pairing/attach", in, false, otherKey, 403), "")
	// Model an in-flight request authenticated before revocation committed.
	// Enter the module with that prior principal and its real registered key;
	// the production auth middleware above stays unchanged.
	raceHandler := (&httpapi.Server{Pool: f.db.App, Modules: []httpapi.Module{f.pairing}}).Handler()
	r := f.request("POST", "/api/agent-pairing/attach", in, false, key)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), tenant.Principal{ID: principal, TenantID: f.tenantID, Kind: tenant.Agent, Scopes: agentpairing.RuntimePermissions, KeyCreatorID: f.person}))
	race := httptest.NewRecorder()
	raceHandler.ServeHTTP(race, r)
	if race.Code != 403 {
		t.Fatalf("in-flight revoked request status %d", race.Code)
	}
	refusalCause(t, race, attachwatch.RefusalPairing)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE principal_id=(SELECT principal_id FROM agent_pairing_computers WHERE id=$1)`, in.ComputerID); err != nil {
		t.Fatal(err)
	}
	w = f.call("POST", "/api/agent-pairing/attach", in, false, key, 401)
	decodeResult(t, w, &body)
	if _, exists := body["attach_refusal"]; exists {
		t.Fatal("unauthenticated revoked credential got an owner cause")
	}
}

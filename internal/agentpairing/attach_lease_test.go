// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"crypto/ecdsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
	return attachModeFixture(t, attachwatch.ModeLease)
}
func attachModeFixture(t *testing.T, mode string) (*fixture, string, attachwatch.DeviceRequest) {
	t.Helper()
	f, key, in := watchFixture(t)
	in.Snapshot.Mode = mode
	if mode == attachwatch.ModeLease {
		in.Snapshot.Transcript = ""
		in.Snapshot.FileID = ""
	}
	in.Digest = in.Snapshot.Digest()
	return f, key, in
}
func TestAttachLeaseApprovalSingleUseAndActivationAtomic(t *testing.T) {
	for _, mode := range []string{"", attachwatch.ModeLease} {
		t.Run("mode="+mode, func(t *testing.T) {
			f, key, in := attachModeFixture(t, mode)
			v := requestWatch(t, f, key, in)
			var lifetime float64
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT extract(epoch FROM expires_at-created_at)::double precision FROM harness_attach_requests WHERE id=$1`, in.RequestID).Scan(&lifetime); err != nil || lifetime < 599 || lifetime > 601 {
				t.Fatal("wrong server code lifetime", err)
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

		})
	}
}

func TestAttachLeaseSessionsRevokeIndependentlyAndExpireOffline(t *testing.T) {
	for _, mode := range []string{"", attachwatch.ModeLease} {
		t.Run("mode="+mode, func(t *testing.T) {
			f, key, in := attachModeFixture(t, mode)
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

		})
	}
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
	role := uuid(t, f.db)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO roles(tenant_id,id,key,name) VALUES($1,$2,'lease_watch','Explicit watch')`, f.tenantID, role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'harness.watch')`, f.tenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, f.tenantID, f.person, role, in.Snapshot.ProjectID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.call("GET", "/api/projects/"+in.Snapshot.ProjectID+"/harness-sessions/"+*v.SessionID+"/watch", nil, true, "", 410)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET last_poll=clock_timestamp()-interval '2 seconds' WHERE id=$1`, in.RequestID); err != nil {
		t.Fatal(err)
	}
	published, unsubscribe := agentpairing.SubscribeAttachForTest(f.pairing, f.tenantID+"/"+*v.SessionID)
	defer unsubscribe()
	in.Sequence++
	in.Text = "AEON352_FORBIDDEN_CONVERSATION"
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 400)
	select {
	case <-published:
		t.Fatal("status-only text published to relay")
	default:
	}
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
	var state string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT state FROM harness_attach_requests WHERE id=$1`, in.RequestID).Scan(&state); err != nil || state != "detached" {
		t.Fatal("changed allowlist retained approval", err)
	}
}

func TestAttachLeaseTenantIsolationAndConfirmedExit(t *testing.T) {
	for _, mode := range []string{"", attachwatch.ModeLease} {
		t.Run("mode="+mode, func(t *testing.T) {
			f, key, in := attachModeFixture(t, mode)
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
					State        string `json:"state"`
					Mode         string `json:"mode"`
					ProcessState string `json:"process_state"`
				} `json:"watch"`
			}
			decodeResult(t, f.call("GET", "/api/projects/"+in.Snapshot.ProjectID+"/harness-sessions/"+*v.SessionID, nil, true, "", 200), &detail)
			if detail.Watch.State != "detached" || detail.Watch.ProcessState != "confirmed_exited" || detail.Watch.Mode != mode {
				t.Fatal("truthful metadata status not projected")
			}
			in.Operation = "poll"
			in.Sequence++
			f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)

		})
	}
}

func TestAttachModesProtectionMatrix(t *testing.T) {
	for _, mode := range []string{"", attachwatch.ModeLease} {
		for _, protection := range []string{"missing active consent", "changed active consent", "changed activation consent", "premature local confirmation", "strict missing digest", "strict changed digest", "discovery text", "changed cwd", "changed mode", "consumed request", "stopped session"} {
			t.Run("mode="+mode+"/"+protection, func(t *testing.T) {
				var f *fixture
				var key string
				var in attachwatch.DeviceRequest
				var signer *ecdsa.PrivateKey
				if strings.HasPrefix(protection, "strict ") {
					f, key, in, signer = upgradedWatchFixture(t)
					setWatchMode(f, attachwatch.ConsentLocalAuth)
					in.Snapshot.Platform = "darwin"
					in.Snapshot.Mode = mode
					if mode == attachwatch.ModeLease {
						in.Snapshot.Transcript, in.Snapshot.FileID = "", ""
					}
					in.Digest = in.Snapshot.Digest()
				} else {
					f, key, in = attachModeFixture(t, mode)
				}
				v := requestWatch(t, f, key, in)
				decodeResult(t, f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 200), &v)
				in.Operation, in.Sequence = "poll", 1
				if protection == "premature local confirmation" {
					in.LocalConfirmed = true
					f.call("POST", "/api/agent-pairing/attach", in, false, key, 403)
					var sessions int
					if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions`).Scan(&sessions); err != nil || sessions != 0 {
						t.Fatal("boolean confirmation granted authority", err)
					}
					return
				}
				if protection == "discovery text" {
					in.Text = "must never publish"
					f.call("POST", "/api/agent-pairing/attach", in, false, key, 400)
				} else if protection == "changed activation consent" || strings.HasPrefix(protection, "strict ") {
					if mode == "" {
						in.Text = "must never publish"
					}
					if protection == "changed activation consent" || protection == "strict changed digest" {
						in.ConsentDigest = strings.Repeat("0", 64)
					}
					if strings.HasPrefix(protection, "strict ") {
						in.LocalAuthNonce = v.LocalAuthNonce
						in.LocalAuthSignature = signWatchConsent(t, signer, v.ConsentDigest, v.LocalAuthNonce, in.Snapshot)
					}
					f.call("POST", "/api/agent-pairing/attach", in, false, key, 409)
				} else {
					in.ConsentDigest = v.ConsentDigest
					decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &v)
					if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET last_poll=clock_timestamp()-interval '2 seconds' WHERE id=$1`, in.RequestID); err != nil {
						t.Fatal(err)
					}
					published, unsubscribe := agentpairing.SubscribeAttachForTest(f.pairing, f.tenantID+"/"+*v.SessionID)
					defer unsubscribe()
					in.Sequence++
					switch protection {
					case "missing active consent":
						in.ConsentDigest = ""
					case "changed active consent":
						in.ConsentDigest = strings.Repeat("0", 64)
					case "changed cwd":
						in.Snapshot.Process.CWD += "/changed"
					case "changed mode":
						if mode == "" {
							in.Snapshot.Mode = attachwatch.ModeLease
							in.Snapshot.Transcript, in.Snapshot.FileID = "", ""
						} else {
							in.Snapshot.Mode = ""
							in.Snapshot.Transcript, in.Snapshot.FileID = "/tmp/transcript", "1:2"
						}
					case "consumed request":
						in.Operation, in.ConsentDigest = "request", ""
					case "stopped session":
						if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`, *v.SessionID); err != nil {
							t.Fatal(err)
						}
						var detail struct {
							Watch struct {
								State string `json:"state"`
							} `json:"watch"`
						}
						decodeResult(t, f.call("GET", "/api/projects/"+in.Snapshot.ProjectID+"/harness-sessions/"+*v.SessionID, nil, true, "", 200), &detail)
						if detail.Watch.State != "detached" {
							t.Fatal("stopped session must be detached")
						}
					}
					code := 409
					if protection == "stopped session" {
						code = 410
					}
					if mode == "" && in.Operation == "poll" {
						in.Text = "must never publish"
					}
					f.call("POST", "/api/agent-pairing/attach", in, false, key, code)
					select {
					case <-published:
						t.Fatal("rejected poll published text")
					default:
					}
					if protection == "consumed request" {
						return
					}
				}
				var state string
				if err := f.db.Admin.QueryRow(t.Context(), `SELECT state FROM harness_attach_requests WHERE id=$1`, in.RequestID).Scan(&state); err != nil || state != "detached" {
					t.Fatal("unsafe request retained authority", err)
				}
				in.Text, in.ConsentDigest, in.LocalConfirmed = "", v.ConsentDigest, false
				in.LocalAuthNonce, in.LocalAuthSignature = "", ""
				f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
			})
		}
	}
}

func TestLegacyAttachRegistrationKeepsDaemonIdentityButGrantsNoAttach(t *testing.T) {
	for _, mode := range []string{"", attachwatch.ModeLease} {
		for _, protocol := range []int{0, 1} {
			t.Run(fmt.Sprintf("mode=%s/protocol=%d", mode, protocol), func(t *testing.T) {
				f, key, in := attachModeFixture(t, mode)
				oldPollKey := in.PollKey
				registration := attachwatch.DeviceRequest{Operation: "register", AttachProtocol: protocol, ComputerID: in.ComputerID, DeviceProof: in.DeviceProof, PollKey: nonce()}
				var registered attachwatch.View
				decodeResult(t, f.call("POST", "/api/agent-pairing/attach", registration, false, key, 200), &registered)
				if registered.State != "registered" {
					t.Fatal("legacy daemon would stop at registration")
				}
				// The long-lived daemon identity remains usable for ordinary work.
				f.call("GET", "/api/me", nil, false, key, 200)
				in.PollKey = registration.PollKey
				for _, operation := range []string{"request", "poll", "detach", "exited"} {
					in.Operation = operation
					for _, claim := range []int{0, attachwatch.Protocol} {
						in.AttachProtocol = claim // A later claim cannot upgrade the key.
						var refusal struct{ Code, Error string }
						decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 409), &refusal)
						if refusal.Code != "update_agentd" || !strings.Contains(refusal.Error, "update agentd") {
							t.Fatal("legacy attach lacks update guidance")
						}
					}
				}
				var count int
				if err := f.db.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM harness_attach_requests)+(SELECT count(*) FROM harness_sessions)`).Scan(&count); err != nil || count != 0 {
					t.Fatal("legacy daemon created attach authority", err)
				}
				in.PollKey = oldPollKey
				f.call("POST", "/api/agent-pairing/attach", in, false, key, 403)
				// A fresh protocol-2 registration is understood by this server and
				// still needs the complete approval flow before it activates.
				registration.AttachProtocol, registration.PollKey = attachwatch.Protocol, nonce()
				registration.LocalConsentProofVersion = attachwatch.LocalConsentProofVersion
				f.call("POST", "/api/agent-pairing/attach", registration, false, key, 200)
				in.PollKey, in.Operation = registration.PollKey, "request"
				activateWatch(t, f, key, &in)
			})
		}
	}
}

func TestAttachEarlyTextAlwaysDetaches(t *testing.T) {
	for _, mode := range []string{"", attachwatch.ModeLease} {
		for _, stage := range []string{"pending", "discovery", "local confirmation", "activation", "strict activation"} {
			t.Run("mode="+mode+"/"+stage, func(t *testing.T) {
				var f *fixture
				var key string
				var in attachwatch.DeviceRequest
				var signer *ecdsa.PrivateKey
				if stage == "local confirmation" || stage == "strict activation" {
					f, key, in, signer = upgradedWatchFixture(t)
					setWatchMode(f, attachwatch.ConsentLocalAuth)
					in.Snapshot.Platform = "darwin"
					in.Snapshot.Mode = mode
					if mode == attachwatch.ModeLease {
						in.Snapshot.Transcript, in.Snapshot.FileID = "", ""
					}
					in.Digest = in.Snapshot.Digest()
				} else {
					f, key, in = attachModeFixture(t, mode)
				}
				v := requestWatch(t, f, key, in)
				if stage != "pending" {
					decodeResult(t, f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 200), &v)
					if stage != "discovery" {
						in.ConsentDigest = v.ConsentDigest
					}
				}
				in.Operation, in.Sequence, in.Text = "poll", 1, "AEON352_EARLY_TEXT_MUST_NOT_PUBLISH"
				if stage == "strict activation" {
					in.LocalAuthNonce = v.LocalAuthNonce
					in.LocalAuthSignature = signWatchConsent(t, signer, v.ConsentDigest, v.LocalAuthNonce, in.Snapshot)
				}
				f.call("POST", "/api/agent-pairing/attach", in, false, key, 400)
				var state string
				var sessions int
				if err := f.db.Admin.QueryRow(t.Context(), `SELECT state,(SELECT count(*) FROM harness_sessions) FROM harness_attach_requests WHERE id=$1`, in.RequestID).Scan(&state, &sessions); err != nil || state != "detached" || sessions != 0 {
					t.Fatal("early text retained authority or activated a session", err)
				}
				in.Text, in.ConsentDigest = "", v.ConsentDigest
				in.LocalAuthNonce, in.LocalAuthSignature = "", ""
				f.call("POST", "/api/agent-pairing/attach", in, false, key, 410)
			})
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

type watchSetting struct {
	ConsentMode  string `json:"consent_mode"`
	ConsentSaved bool   `json:"consent_saved"`
	Computers    []struct {
		ComputerID      string `json:"computer_id"`
		Name            string `json:"name"`
		Capability      string `json:"capability"`
		PairingUpgraded bool   `json:"pairing_upgraded"`
	} `json:"local_auth_computers"`
}

const watchSecurityPath = "/api/me/security/session-watching"

func setWatchMode(f *fixture, mode string) {
	f.call("PUT", watchSecurityPath, map[string]string{"consent_mode": mode}, true, "", 200)
}
func TestWatchSecurityIsPersonOnlySameOriginAndTenantScoped(t *testing.T) {
	f, key, in := watchFixture(t)
	var setting watchSetting
	loaded := f.call("GET", watchSecurityPath, nil, true, "", 200)
	if bytes.Contains(loaded.Body.Bytes(), []byte(`"local_auth_computers":null`)) {
		t.Fatal("null computer list")
	}
	decodeResult(t, loaded, &setting)
	if setting.ConsentMode != attachwatch.ConsentAeon || len(setting.Computers) != 1 || setting.Computers[0].ComputerID != in.ComputerID || setting.Computers[0].Capability != attachwatch.LocalAuthUnreported {
		t.Fatalf("default view %+v", setting)
	}
	f.call("GET", watchSecurityPath, nil, false, key, 403)
	f.call("PUT", watchSecurityPath, map[string]string{"consent_mode": "local_auth"}, false, key, 403)
	request := f.request("PUT", watchSecurityPath, map[string]string{"consent_mode": "local_auth"}, true, "")
	request.Header.Set("Origin", "https://foreign.test")
	response := httptest.NewRecorder()
	f.h.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("cross-origin security write accepted")
	}
	f.call("PUT", watchSecurityPath, map[string]string{"consent_mode": "off"}, true, "", 400)
	f.call("PUT", watchSecurityPath, map[string]string{"consent_mode": "aeon", "person_id": f.person}, true, "", 400)
	f.call("PUT", watchSecurityPath, map[string]any{"consent_mode": "aeon", "local_auth_computers": []any{}}, true, "", 400)
	setWatchMode(f, attachwatch.ConsentLocalAuth)
	decodeResult(t, f.call("GET", watchSecurityPath, nil, true, "", 200), &setting)
	if setting.ConsentMode != attachwatch.ConsentLocalAuth {
		t.Fatal("setting not persisted")
	}
	// A second person in the same tenant neither inherits nor edits this policy.
	second := uuid(t, f.db)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH i AS (INSERT INTO identities(issuer,subject,email) VALUES('fixture','second','second@example.test') RETURNING id) INSERT INTO principals(tenant_id,id,kind,identity_id,name,roles) SELECT $1,$2,'person',i.id,'Second person',ARRAY['member'] FROM i`, f.tenantID, second)
		if err != nil {
			return err
		}
		return dbtest.BindLegacyTx(t.Context(), tx, f.tenantID, second)
	}); err != nil {
		t.Fatal(err)
	}
	ownerCookie := f.cookie
	login := f.call("POST", "/api/auth/dev-login", map[string]string{"email": "second@example.test"}, false, "", 200)
	f.cookie = login.Result().Cookies()[0]
	decodeResult(t, f.call("GET", watchSecurityPath, nil, true, "", 200), &setting)
	if setting.ConsentMode != attachwatch.ConsentAeon {
		t.Fatal("inherited another person's setting")
	}
	setWatchMode(f, attachwatch.ConsentAeon)
	f.cookie = ownerCookie
	decodeResult(t, f.call("GET", watchSecurityPath, nil, true, "", 200), &setting)
	if setting.ConsentMode != attachwatch.ConsentLocalAuth {
		t.Fatal("another person overwrote owner's setting")
	}
	other := uuid(t, f.db)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, other, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM person_watch_security`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("cross-tenant policy exposed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestStrictWatchRequiresBoundDaemonConfirmation(t *testing.T) {
	f, key, in, signer := upgradedWatchFixture(t)
	setWatchMode(f, attachwatch.ConsentLocalAuth)
	in.Snapshot.Platform = "darwin"
	in.Digest = in.Snapshot.Digest()
	v := requestWatch(t, f, key, in)
	if v.ConsentMode != attachwatch.ConsentLocalAuth || v.ConsentDigest != attachwatch.ConsentDigest(v.RequestID, v.Digest, v.ConsentMode) {
		t.Fatal("server mode not bound")
	}
	path := "/api/agent-pairing/attach/" + in.RequestID + "/approve"
	f.call("POST", path, map[string]string{"request_digest": v.Digest}, true, "", 409)
	f.call("POST", path, map[string]string{"request_digest": v.Digest, "consent_digest": attachwatch.ConsentDigest(v.RequestID, v.Digest, attachwatch.ConsentAeon)}, true, "", 409)
	f.call("POST", path, map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 200)
	// Changing the setting cannot downgrade an already-approved request.
	setWatchMode(f, attachwatch.ConsentAeon)
	in.Operation, in.Sequence = "poll", 1
	var waiting attachwatch.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &waiting)
	if waiting.State != "approved" || waiting.SessionID != nil || waiting.LeaseUntil != nil || waiting.ConsentMode != attachwatch.ConsentLocalAuth {
		t.Fatal("activated without local confirmation")
	}
	in.LocalConfirmed = true
	in.ConsentDigest = v.ConsentDigest
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 403)
	in.LocalConfirmed = false
	in.LocalAuthNonce = waiting.LocalAuthNonce
	in.LocalAuthSignature = signWatchConsent(t, signer, v.ConsentDigest, waiting.LocalAuthNonce)
	stolen := in
	stolen.PollKey = ""
	f.call("POST", "/api/agent-pairing/attach", stolen, false, key, 403)
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &waiting)
	if waiting.State != "active" || waiting.SessionID == nil {
		t.Fatal("confirmed watch did not activate")
	}
	setWatchMode(f, attachwatch.ConsentLocalAuth)
	// Policy changes do not rewrite the active pin or session.
	var mode string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT consent_mode FROM harness_attach_requests WHERE id=$1`, in.RequestID).Scan(&mode); err != nil || mode != attachwatch.ConsentLocalAuth {
		t.Fatal("active pin changed")
	}
}
func TestWatchSecurityCannotBeDowngradedByRequestOrStaleReview(t *testing.T) {
	f, key, in, _ := upgradedWatchFixture(t)
	in.Snapshot.Platform = "darwin"
	in.Digest = in.Snapshot.Digest()
	in.ConsentDigest = attachwatch.ConsentDigest(in.RequestID, in.Digest, attachwatch.ConsentAeon)
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 400)
	in.ConsentDigest = ""
	v := requestWatch(t, f, key, in)
	setWatchMode(f, attachwatch.ConsentLocalAuth)
	path := "/api/agent-pairing/attach/" + in.RequestID + "/approve"
	f.call("POST", path, map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 409)
	var fresh attachwatch.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach/lookup", map[string]string{"user_code": v.UserCode}, true, "", 200), &fresh)
	if fresh.ConsentMode != attachwatch.ConsentLocalAuth || fresh.ConsentDigest == v.ConsentDigest {
		t.Fatal("stale policy retained")
	}
	// A client cannot submit a mode override at all.
	f.call("POST", path, map[string]string{"request_digest": v.Digest, "consent_digest": fresh.ConsentDigest, "consent_mode": "aeon"}, true, "", 400)
}
func TestStrictWatchUnavailableOnLinuxAndLegacyDaemon(t *testing.T) {
	for _, platform := range []string{"linux", ""} {
		t.Run(platform, func(t *testing.T) {
			f, key, in, _ := upgradedWatchFixture(t)
			setWatchMode(f, attachwatch.ConsentLocalAuth)
			in.Snapshot.Platform = platform
			in.Digest = in.Snapshot.Digest()
			v := requestWatch(t, f, key, in)
			f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 409)
		})
	}
}
func TestWatchSettingChangeDoesNotAffectActiveModeA(t *testing.T) {
	f, key, in := watchFixture(t)
	v := activateWatch(t, f, key, &in)
	setWatchMode(f, attachwatch.ConsentLocalAuth)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET last_poll=clock_timestamp()-interval '2 seconds' WHERE id=$1`, in.RequestID); err != nil {
		t.Fatal(err)
	}
	in.Sequence++
	var next attachwatch.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &next)
	if next.State != "active" || next.ConsentMode != attachwatch.ConsentAeon || *next.SessionID != *v.SessionID {
		t.Fatal("active watch policy changed")
	}
}

func readWatchSetting(t *testing.T, f *fixture) watchSetting {
	t.Helper()
	var setting watchSetting
	body := f.call("GET", watchSecurityPath, nil, true, "", 200)
	if bytes.Contains(body.Body.Bytes(), []byte(`"local_auth_computers":null`)) {
		t.Fatal("null computer list")
	}
	decodeResult(t, body, &setting)
	return setting
}

func TestLocalAuthCapabilityFollowsDaemonRegistration(t *testing.T) {
	f, key, in := watchFixture(t)
	setting := readWatchSetting(t, f)
	if len(setting.Computers) != 1 || setting.Computers[0].Name != "Test workstation" || setting.Computers[0].Capability != attachwatch.LocalAuthUnreported || setting.Computers[0].ComputerID != in.ComputerID {
		t.Fatalf("omitted registration %+v", setting.Computers)
	}
	in.LocalAuthCapability = attachwatch.LocalAuthUnsigned
	requestWatch(t, f, key, in)
	in.Operation = "poll"
	in.Sequence = 1
	in.LocalAuthCapability = attachwatch.LocalAuthPolicy
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 200)
	if got := readWatchSetting(t, f); len(got.Computers) != 1 || got.Computers[0].Capability != attachwatch.LocalAuthUnreported {
		t.Fatalf("poll changed capability %+v", got.Computers)
	}
	f.call("POST", "/api/agent-pairing/attach", attachwatch.DeviceRequest{AttachProtocol: attachwatch.Protocol, Operation: "register", ComputerID: in.ComputerID, DeviceProof: in.DeviceProof, PollKey: nonce(), LocalAuthCapability: "browser"}, false, key, 400)
	f.call("POST", "/api/agent-pairing/attach", attachwatch.DeviceRequest{AttachProtocol: attachwatch.Protocol, Operation: "register", ComputerID: in.ComputerID, DeviceProof: in.DeviceProof, PollKey: nonce(), LocalAuthCapability: attachwatch.LocalAuthUnreported}, false, key, 400)
	if got := readWatchSetting(t, f); got.Computers[0].Capability != attachwatch.LocalAuthUnreported {
		t.Fatal("rejected registration changed capability")
	}
	keyPoll := nonce()
	f.call("POST", "/api/agent-pairing/attach", attachwatch.DeviceRequest{AttachProtocol: attachwatch.Protocol, Operation: "register", ComputerID: in.ComputerID, DeviceProof: in.DeviceProof, PollKey: keyPoll, LocalAuthCapability: attachwatch.LocalAuthAvailable}, false, key, 200)
	if got := readWatchSetting(t, f); got.Computers[0].Capability != attachwatch.LocalAuthAvailable {
		t.Fatalf("available report %+v", got.Computers)
	}
	again := in
	again.Operation = "request"
	again.RequestID = uuid(t, f.db)
	again.PollKey = keyPoll
	again.LocalAuthCapability = attachwatch.LocalAuthUnsupported
	again.Sequence = 0
	requestWatch(t, f, key, again)
	if got := readWatchSetting(t, f); got.Computers[0].Capability != attachwatch.LocalAuthAvailable {
		t.Fatal("request changed capability")
	}
	f.call("POST", "/api/agent-pairing/attach", attachwatch.DeviceRequest{AttachProtocol: attachwatch.Protocol, Operation: "register", ComputerID: in.ComputerID, DeviceProof: in.DeviceProof, PollKey: nonce()}, false, key, 200)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_requests q SET details=details-'computer_name' FROM agent_pairing_computers c WHERE c.id=$1 AND q.tenant_id=c.tenant_id AND q.id=c.request_id`, in.ComputerID); err != nil {
		t.Fatal(err)
	}
	if got := readWatchSetting(t, f); got.Computers[0].Name != "Paired computer" || got.Computers[0].Capability != attachwatch.LocalAuthUnreported {
		t.Fatalf("cleared name %+v", got.Computers)
	}
	second := uuid(t, f.db)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH i AS (INSERT INTO identities(issuer,subject,email) VALUES('fixture','capability-second','capability-second@example.test') RETURNING id) INSERT INTO principals(tenant_id,id,kind,identity_id,name,roles) SELECT $1,$2,'person',i.id,'Capability second',ARRAY['member'] FROM i`, f.tenantID, second)
		if err != nil {
			return err
		}
		return dbtest.BindLegacyTx(t.Context(), tx, f.tenantID, second)
	}); err != nil {
		t.Fatal(err)
	}
	login := f.call("POST", "/api/auth/dev-login", map[string]string{"email": "capability-second@example.test"}, false, "", 200)
	f.cookie = login.Result().Cookies()[0]
	if got := readWatchSetting(t, f); got.ConsentMode != attachwatch.ConsentAeon || len(got.Computers) != 0 {
		t.Fatalf("other person saw %+v", got)
	}
}

func registerCapability(t *testing.T, f *fixture, key string, in *attachwatch.DeviceRequest, capability string) {
	t.Helper()
	poll := nonce()
	f.call("POST", "/api/agent-pairing/attach", attachwatch.DeviceRequest{AttachProtocol: attachwatch.Protocol, Operation: "register", ComputerID: in.ComputerID, DeviceProof: in.DeviceProof, PollKey: poll, LocalAuthCapability: capability}, false, key, 200)
	in.PollKey = poll
}

func finishAeonWatch(t *testing.T, f *fixture, key string, in *attachwatch.DeviceRequest, v attachwatch.View) {
	t.Helper()
	f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 200)
	in.Operation = "poll"
	in.Digest = v.Digest
	in.ConsentDigest = v.ConsentDigest
	in.Sequence = 1
	var active attachwatch.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &active)
	if active.State != "active" || active.SessionID == nil {
		t.Fatal("aeon watch did not activate")
	}
}

func TestTouchIDDefaultIsMacOnlyUntilSaved(t *testing.T) {
	t.Run("darwin available", func(t *testing.T) {
		f, key, in, signer := upgradedWatchFixture(t)
		registerCapability(t, f, key, &in, attachwatch.LocalAuthAvailable)
		in.Snapshot.Platform = "darwin"
		v := requestWatch(t, f, key, in)
		if v.ConsentMode != attachwatch.ConsentLocalAuth {
			t.Fatalf("unsaved mac default %s", v.ConsentMode)
		}
		if got := readWatchSetting(t, f); got.ConsentMode != attachwatch.ConsentLocalAuth || got.ConsentSaved || !got.Computers[0].PairingUpgraded {
			t.Fatalf("settings %+v", got)
		}
		f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 200)
		in.Operation, in.Sequence = "poll", 1
		in.Digest = v.Digest
		var waiting attachwatch.View
		decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &waiting)
		if waiting.State != "approved" || waiting.SessionID != nil || waiting.LeaseUntil != nil || waiting.ConsentMode != attachwatch.ConsentLocalAuth {
			t.Fatal("activated without local confirmation")
		}
		in.LocalConfirmed = true
		in.ConsentDigest = v.ConsentDigest
		f.call("POST", "/api/agent-pairing/attach", in, false, key, 403)
		in.LocalConfirmed = false
		in.LocalAuthNonce = waiting.LocalAuthNonce
		in.LocalAuthSignature = signWatchConsent(t, signer, in.ConsentDigest, in.LocalAuthNonce)
		decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, key, 200), &waiting)
		if waiting.State != "active" || waiting.SessionID == nil {
			t.Fatal("confirmed watch did not activate")
		}
	})
	for _, capability := range []string{attachwatch.LocalAuthNoGUI, attachwatch.LocalAuthPolicy, attachwatch.LocalAuthUnsigned} {
		t.Run(capability, func(t *testing.T) {
			f, key, in, _ := upgradedWatchFixture(t)
			registerCapability(t, f, key, &in, capability)
			in.Snapshot.Platform = "darwin"
			v := requestWatch(t, f, key, in)
			if v.ConsentMode != attachwatch.ConsentAeon {
				t.Fatalf("headless or incapable mac left aeon: %s", v.ConsentMode)
			}
			if got := readWatchSetting(t, f); got.ConsentMode != attachwatch.ConsentAeon || got.ConsentSaved {
				t.Fatalf("settings %+v", got)
			}
			finishAeonWatch(t, f, key, &in, v)
		})
	}
	t.Run("linux available", func(t *testing.T) {
		f, key, in, _ := upgradedWatchFixture(t)
		registerCapability(t, f, key, &in, attachwatch.LocalAuthAvailable)
		in.Snapshot.Platform = "linux"
		v := requestWatch(t, f, key, in)
		if v.ConsentMode != attachwatch.ConsentAeon {
			t.Fatalf("linux request used %s", v.ConsentMode)
		}
		if got := readWatchSetting(t, f); got.ConsentMode != attachwatch.ConsentLocalAuth || got.ConsentSaved {
			t.Fatalf("settings display %+v", got)
		}
		finishAeonWatch(t, f, key, &in, v)
	})
	t.Run("saved aeon opt-out", func(t *testing.T) {
		f, key, in, _ := upgradedWatchFixture(t)
		registerCapability(t, f, key, &in, attachwatch.LocalAuthAvailable)
		setWatchMode(f, attachwatch.ConsentAeon)
		in.Snapshot.Platform = "darwin"
		v := requestWatch(t, f, key, in)
		if v.ConsentMode != attachwatch.ConsentAeon {
			t.Fatalf("saved opt-out used %s", v.ConsentMode)
		}
		if got := readWatchSetting(t, f); got.ConsentMode != attachwatch.ConsentAeon || !got.ConsentSaved {
			t.Fatalf("settings %+v", got)
		}
		finishAeonWatch(t, f, key, &in, v)
	})
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

const watchSecurityPath = "/api/me/security/session-watching"

func setWatchMode(f *fixture, mode string) {
	f.call("PUT", watchSecurityPath, map[string]string{"consent_mode": mode}, true, "", 200)
}
func TestWatchSecurityIsPersonOnlySameOriginAndTenantScoped(t *testing.T) {
	f, key, _ := watchFixture(t)
	var setting map[string]string
	decodeResult(t, f.call("GET", watchSecurityPath, nil, true, "", 200), &setting)
	if setting["consent_mode"] != attachwatch.ConsentAeon {
		t.Fatal("default is not A")
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
	setWatchMode(f, attachwatch.ConsentLocalAuth)
	decodeResult(t, f.call("GET", watchSecurityPath, nil, true, "", 200), &setting)
	if setting["consent_mode"] != attachwatch.ConsentLocalAuth {
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
	if setting["consent_mode"] != attachwatch.ConsentAeon {
		t.Fatal("inherited another person's setting")
	}
	setWatchMode(f, attachwatch.ConsentAeon)
	f.cookie = ownerCookie
	decodeResult(t, f.call("GET", watchSecurityPath, nil, true, "", 200), &setting)
	if setting["consent_mode"] != attachwatch.ConsentLocalAuth {
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
	f, key, in := watchFixture(t)
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
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 409)
	in.ConsentDigest = attachwatch.ConsentDigest(v.RequestID, v.Digest, attachwatch.ConsentAeon)
	f.call("POST", "/api/agent-pairing/attach", in, false, key, 409)
	in.ConsentDigest = v.ConsentDigest
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
	f, key, in := watchFixture(t)
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
			f, key, in := watchFixture(t)
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

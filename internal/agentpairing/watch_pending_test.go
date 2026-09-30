// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

const pendingPath = "/api/agent-pairing/attach/pending"

type pendingList struct {
	Requests []attachwatch.View `json:"requests"`
}

func listPending(t *testing.T, f *fixture) (pendingList, string) {
	t.Helper()
	w := f.call("GET", pendingPath, nil, true, "", 200)
	var out pendingList
	decodeResult(t, w, &out)
	return out, w.Body.String()
}

// The owner sees a waiting request without the code, reviews it from the list and
// approves it with the same digests the lookup path uses.
func TestAttachPendingListsWaitingRequestWithoutCodeAndApprovesByID(t *testing.T) {
	f, key, in := watchFixture(t)
	if got, _ := listPending(t, f); len(got.Requests) != 0 {
		t.Fatal("nothing is waiting yet")
	}
	v := requestWatch(t, f, key, in)
	got, raw := listPending(t, f)
	if len(got.Requests) != 1 {
		t.Fatalf("want one waiting request, got %d", len(got.Requests))
	}
	item := got.Requests[0]
	if item.RequestID != in.RequestID || item.State != "pending" || item.Digest != v.Digest || item.ConsentMode != attachwatch.ConsentAeon ||
		item.ConsentDigest != v.ConsentDigest || item.Snapshot != in.Snapshot || item.ExpiresAt.IsZero() {
		t.Fatalf("list must carry exactly what lookup carries: %+v", item)
	}
	// The code and the Touch ID challenge never travel to the browser through the list.
	for _, secret := range []string{v.UserCode, "user_code", "local_auth_nonce", "session_id\":\"", "poll_key"} {
		if secret != "" && strings.Contains(raw, secret) {
			t.Fatalf("list leaks %q: %s", secret, raw)
		}
	}
	approval := "/api/agent-pairing/attach/" + item.RequestID + "/approve"
	f.call("POST", approval, map[string]string{"request_digest": item.Digest}, true, "", 409)
	f.call("POST", approval, map[string]string{"request_digest": item.Digest, "consent_digest": item.ConsentDigest}, true, "", 200)
	got, _ = listPending(t, f)
	if len(got.Requests) != 1 || got.Requests[0].State != "approved" {
		t.Fatalf("approved request keeps showing until the daemon connects: %+v", got.Requests)
	}
}

// Declining and expiry are visible, expiry is reported without being applied, and
// a request that became a session or is older than the window is not listed.
func TestAttachPendingShowsCancelledExpiredAndDropsOldOrActive(t *testing.T) {
	f, key, in := watchFixture(t)
	requestWatch(t, f, key, in)
	f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/revoke", nil, true, "", 200)
	got, _ := listPending(t, f)
	if len(got.Requests) != 1 || got.Requests[0].State != "detached" {
		t.Fatalf("a declined request reads as cancelled: %+v", got.Requests)
	}

	second := in
	second.RequestID = uuid(t, f.db)
	requestWatch(t, f, key, second)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, second.RequestID); err != nil {
		t.Fatal(err)
	}
	got, _ = listPending(t, f)
	states := map[string]string{}
	for _, item := range got.Requests {
		states[item.RequestID] = item.State
	}
	if len(got.Requests) != 2 || states[second.RequestID] != "unreachable" || states[in.RequestID] != "detached" {
		t.Fatalf("an unpolled expired request reads as expired: %v", states)
	}
	var stored string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT state FROM harness_attach_requests WHERE id=$1`, second.RequestID).Scan(&stored); err != nil || stored != "pending" {
		t.Fatal("listing must not mutate the request", stored, err)
	}

	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET created_at=clock_timestamp()-interval '16 minutes' WHERE id=$1`, in.RequestID); err != nil {
		t.Fatal(err)
	}
	got, _ = listPending(t, f)
	if len(got.Requests) != 1 || got.Requests[0].RequestID != second.RequestID {
		t.Fatalf("an old request drops out of the list: %+v", got.Requests)
	}

	active := in
	active.RequestID = uuid(t, f.db)
	v := activateWatch(t, f, key, &active)
	if v.State != "active" {
		t.Fatal("fixture must activate")
	}
	got, _ = listPending(t, f)
	for _, item := range got.Requests {
		if item.RequestID == active.RequestID {
			t.Fatal("an active watch is a session, not a pending request")
		}
	}
}

// Only the computer's owner lists its requests: not another tenant's person, not a
// paired daemon, not an anonymous caller. The list is read-only and needs no origin.
func TestAttachPendingIsOwnerOnly(t *testing.T) {
	f, key, in := watchFixture(t)
	requestWatch(t, f, key, in)
	f.call("GET", pendingPath, nil, false, key, 403)
	f.call("GET", pendingPath, nil, false, "", 401)

	foreign, err := tenantbootstrap.Create(t.Context(), f.db.App, "pending-foreign", "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	am, err := auth.New(auth.Config{Env: "dev", PublicURL: origin, SessionKey: []byte(nonce()), BootstrapTenantSlug: "pending-foreign", BootstrapAdminEmail: "foreign@example.test"}, f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	api := &httpapi.Server{Pool: f.db.App, Modules: []httpapi.Module{am, agentpairing.New(f.db.App, origin, "pending-foreign")}, Middleware: []func(http.Handler) http.Handler{am.Middleware}}
	other := &fixture{t: t, db: f.db, h: api.Handler(), tenantID: foreign}
	login := other.call("POST", "/api/auth/dev-login", map[string]string{"email": "foreign@example.test"}, false, "", 200)
	other.cookie = login.Result().Cookies()[0]
	if got, _ := listPending(t, other); len(got.Requests) != 0 {
		t.Fatalf("another tenant's owner sees %d requests", len(got.Requests))
	}
}

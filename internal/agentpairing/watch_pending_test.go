// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"net/http"
	"net/http/httptest"
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

// A request that still waits is always listed, however many newer ones ended after
// it and however old it is: the server filters to live requests first, the page
// never has to recover one by sorting. Ended requests stay a short, recent tail.
func TestAttachPendingKeepsEveryWaitingRequestBehindNewerEndedOnes(t *testing.T) {
	f, key, in := watchFixture(t)
	requestWatch(t, f, key, in)
	waiting := in.RequestID
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET created_at=clock_timestamp()-interval '14 minutes', expires_at=clock_timestamp()+interval '3 minutes' WHERE id=$1`, waiting); err != nil {
		t.Fatal(err)
	}
	for range 9 {
		ended := in
		ended.RequestID = uuid(t, f.db)
		requestWatch(t, f, key, ended)
		f.call("POST", "/api/agent-pairing/attach/"+ended.RequestID+"/revoke", nil, true, "", 200)
	}
	got, _ := listPending(t, f)
	var live, endedCount int
	found := false
	for _, item := range got.Requests {
		if item.RequestID == waiting {
			found = item.State == "pending"
		}
		if item.State == "pending" || item.State == "approved" {
			live++
		} else {
			endedCount++
		}
	}
	if !found || live != 1 {
		t.Fatalf("the older waiting request must be listed while nine newer ended ones exist: %+v", got.Requests)
	}
	if endedCount == 0 || endedCount > 4 {
		t.Fatalf("ended requests are a short recent tail, got %d", endedCount)
	}
	// Waiting requests are not bound by the recent window: only expiry ends them.
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET created_at=clock_timestamp()-interval '40 minutes', expires_at=clock_timestamp()+interval '1 minute' WHERE id=$1`, waiting); err != nil {
		t.Fatal(err)
	}
	got, _ = listPending(t, f)
	found = false
	for _, item := range got.Requests {
		found = found || (item.RequestID == waiting && item.State == "pending")
	}
	if !found {
		t.Fatalf("a request that has not expired stays listed: %+v", got.Requests)
	}
}

// One more computer of the same owner, paired in the same workspace, registered
// for attach: its own key, proof and poll key, the same project and ticket.
func extraWatchComputer(t *testing.T, f *fixture, base attachwatch.DeviceRequest) (string, attachwatch.DeviceRequest) {
	t.Helper()
	p := f.proposePlatformKey("darwin", "arm64", "", "codex")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	in := base
	in.ComputerID = *v.ComputerID
	in.DeviceProof = p.lifecycle
	in.Snapshot.ComputerID = *v.ComputerID
	in.PollKey = nonce()
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	f.call("POST", "/api/agent-pairing/attach", attachwatch.DeviceRequest{AttachProtocol: attachwatch.Protocol, LocalConsentProofVersion: attachwatch.LocalConsentProofVersion, Operation: "register", ComputerID: in.ComputerID, DeviceProof: p.lifecycle, PollKey: in.PollKey}, false, key, 200)
	return key, in
}

// A person never has more waiting requests than the list can show. Five computers
// of one owner are each under their own cap of eight, and the tenant's ten-minute
// window rolls over in the middle (one request early, then the rest), which is
// how 33 unexpired requests once fitted under the creation limits. The 33rd is
// refused with its own code, every admitted request is listed, and each way a
// request stops waiting frees exactly one slot.
func TestAttachAdmissionBoundsLiveRequestsAcrossComputersAndWindowRollover(t *testing.T) {
	f, key, in := watchFixture(t)
	type computer struct {
		key string
		in  attachwatch.DeviceRequest
	}
	computers := []computer{{key, in}}
	for len(computers) < 5 {
		k, c := extraWatchComputer(t, f, in)
		computers = append(computers, computer{k, c})
	}
	create := func(c computer, id string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := c.in
		req.RequestID = id
		req.Digest = req.Snapshot.Digest()
		return f.call("POST", "/api/agent-pairing/attach", req, false, c.key, status)
	}
	rollWindow := func() {
		t.Helper()
		if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_limits SET starts_at=clock_timestamp()-interval '11 minutes' WHERE bucket='request'`); err != nil {
			t.Fatal(err)
		}
	}
	refused := func(c computer) {
		t.Helper()
		w := create(c, uuid(t, f.db), 429)
		var body struct{ Error, Code string }
		decodeResult(t, w, &body)
		if body.Code != attachwatch.LiveLimitCode || body.Error != attachwatch.LiveLimitMessage {
			t.Fatalf("the bound answers with its own code, not a rate limit: %s", w.Body.String())
		}
	}
	var ids []string
	for i := range attachwatch.LiveMax {
		if i == 30 {
			// The tenant window has admitted 30; a new one starts here.
			rollWindow()
		}
		id := uuid(t, f.db)
		create(computers[i%len(computers)], id, 200)
		ids = append(ids, id)
	}
	refused(computers[0])
	refused(computers[4])

	live := func() map[string]string {
		t.Helper()
		got, _ := listPending(t, f)
		states := map[string]string{}
		for _, item := range got.Requests {
			if item.State == "pending" || item.State == "approved" {
				states[item.RequestID] = item.State
			}
		}
		return states
	}
	listed := live()
	if len(listed) != attachwatch.LiveMax {
		t.Fatalf("every admitted request is listed: %d of %d", len(listed), attachwatch.LiveMax)
	}
	for _, id := range ids {
		if _, ok := listed[id]; !ok {
			t.Fatalf("admitted request %s is hidden", id)
		}
	}

	// A retry of a request that already exists is answered, full list or not.
	create(computers[0], ids[0], 200)
	// Approving keeps the slot: the request is still the owner's to look at.
	approve := func(id string) {
		t.Helper()
		var v attachwatch.View
		decodeResult(t, f.call("POST", "/api/agent-pairing/attach/lookup", map[string]string{"user_code": codeOf(t, f, id)}, true, "", 200), &v)
		f.call("POST", "/api/agent-pairing/attach/"+id+"/approve", map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 200)
	}
	approve(ids[1])
	refused(computers[1])

	// Declining frees one slot, expiry frees one, and only one each.
	f.call("POST", "/api/agent-pairing/attach/"+ids[2]+"/revoke", nil, true, "", 200)
	create(computers[2], uuid(t, f.db), 200)
	refused(computers[2])
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, ids[3]); err != nil {
		t.Fatal(err)
	}
	create(computers[3], uuid(t, f.db), 200)
	refused(computers[3])
	if got := live(); len(got) != attachwatch.LiveMax {
		t.Fatalf("the list still shows every live request after the slots were reused: %d", len(got))
	}
}

// The attach code of a request, read around the API the way the owner's terminal shows it.
func codeOf(t *testing.T, f *fixture, id string) string {
	t.Helper()
	var code string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT user_code FROM harness_attach_requests WHERE id=$1`, id).Scan(&code); err != nil {
		t.Fatal(err)
	}
	return code
}

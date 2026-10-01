// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
	"github.com/jackc/pgx/v5"
)

const accountLinkPath = "/api/agent-pairing/account-link"

type linkReview struct {
	RequestID string `json:"request_id"`
	TenantID  string `json:"tenant_id"`
	AccountID string `json:"account_id"`
	PersonID  string `json:"person_id"`
	Label     string `json:"account_label"`
	Computer  string `json:"computer_name"`
	Revision  int64  `json:"revision"`
	State     string `json:"state"`
	Digest    string `json:"request_digest"`
}

func linkFixture(t *testing.T) (*fixture, *proposal, agentpairing.View, string) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	return f, p, v, "aeon_" + v.RuntimePrefix + "_" + p.runtime
}
func offerLink(t *testing.T, f *fixture, p *proposal, key, account string) agentsetup.AccountLinkView {
	t.Helper()
	var out agentsetup.AccountLinkView
	decodeResult(t, f.call("POST", accountLinkPath, map[string]string{"operation": "offer", "account_id": account, "device_proof": p.lifecycle}, false, key, 200), &out)
	return out
}
func reviewLink(t *testing.T, f *fixture, v agentsetup.AccountLinkView) linkReview {
	t.Helper()
	var out linkReview
	decodeResult(t, f.call("POST", accountLinkPath+"/lookup", map[string]string{"user_code": v.Code}, true, "", 200), &out)
	return out
}
func approveLinkBody(v linkReview) map[string]any {
	return map[string]any{"tenant_id": v.TenantID, "person_id": v.PersonID, "expected_revision": v.Revision, "request_digest": v.Digest}
}
func TestAccountLinkSingleUseAndOwnerUnlink(t *testing.T) {
	f, p, v, key := linkFixture(t)
	account := v.Enrollments[0].AccountID
	offer := offerLink(t, f, p, key, account)
	if !offer.ShowPrompt || len(offer.Code) != 7 || offer.URI != origin+"/link" || offer.ExpiresAt == nil {
		t.Fatal("one-line code offer missing")
	}
	again := offerLink(t, f, p, key, account)
	if again.ShowPrompt || again.Code != "" || again.RequestID != offer.RequestID {
		t.Fatal("offer repeated or changed")
	}
	review := reviewLink(t, f, offer)
	if review.AccountID != account || review.PersonID != f.person || review.Computer != "Test workstation" {
		t.Fatal("review omitted account/person/computer binding")
	}
	f.call("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), true, "", 200)
	f.call("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), true, "", 410)
	f.call("POST", accountLinkPath+"/lookup", map[string]string{"user_code": offer.Code}, true, "", 410)
	var poll agentsetup.AccountLinkView
	decodeResult(t, f.call("POST", accountLinkPath, map[string]string{"operation": "poll", "account_id": account, "device_proof": p.lifecycle, "request_id": offer.RequestID}, false, key, 200), &poll)
	if poll.State != "linked" || !poll.ShowResult || poll.PersonName == "" || poll.ShowPrompt || poll.Code != "" {
		t.Fatal("terminal result missing or code exposed")
	}
	decodeResult(t, f.call("POST", accountLinkPath, map[string]string{"operation": "poll", "account_id": account, "device_proof": p.lifecycle, "request_id": offer.RequestID}, false, key, 200), &poll)
	if poll.ShowResult {
		t.Fatal("another window repeated the link result")
	}
	if got := offerLink(t, f, p, key, account); got.State != "linked" || got.ShowPrompt || got.ShowResult || got.Code != "" {
		t.Fatal("linked account repeated its prompt")
	}
	var accounts []agentaccounts.Account
	decodeResult(t, f.call("GET", "/api/agent-accounts", nil, true, "", 200), &accounts)
	if len(accounts) != 1 || accounts[0].OwnerPersonID == nil || *accounts[0].OwnerPersonID != f.person || accounts[0].OngoingUseApproved {
		t.Fatal("ownership or spending approval conflated")
	}
	f.call("POST", "/api/agent-pairing/account-links/"+account+"/unlink", map[string]int64{"expected_revision": 0}, true, "", 204)
	// The consumed code cannot regain ownership after unlinking.
	f.call("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), true, "", 404)
	fresh := offerLink(t, f, p, key, account)
	if !fresh.ShowPrompt || fresh.Code == offer.Code || fresh.RequestID == offer.RequestID {
		t.Fatal("unlink reused its consumed code")
	}
	if f.events("account.linked") != 1 || f.events("account.unlinked") != 1 {
		t.Fatal("audit omitted or duplicated")
	}
	var eventText string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT coalesce(string_agg("after"::text,''),'') FROM events WHERE tenant_id=$1 AND type LIKE 'account.link%'`, f.tenantID).Scan(&eventText); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{p.lifecycle, offer.Code, strings.ReplaceAll(offer.Code, " ", "")} {
		if strings.Contains(eventText, private) {
			t.Fatal("code/proof reached audit")
		}
	}
}
func TestAccountLinkExpiryFreshnessAndPersonOnly(t *testing.T) {
	f, p, v, key := linkFixture(t)
	account := v.Enrollments[0].AccountID
	f.call("POST", accountLinkPath, map[string]string{"operation": "offer", "account_id": account, "device_proof": nonce()}, false, key, 404)
	offer := offerLink(t, f, p, key, account)
	review := reviewLink(t, f, offer)
	f.call("POST", accountLinkPath+"/lookup", map[string]string{"user_code": offer.Code}, false, key, 403)
	f.call("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), false, key, 403)
	f.call("GET", "/api/agent-pairing/account-links", nil, false, key, 403)
	f.call("POST", "/api/agent-pairing/account-links/"+account+"/unlink", map[string]int64{"expected_revision": 0}, false, key, 403)
	r := f.request("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), true, "")
	r.Header.Set("Origin", "https://evil.test")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin confirmation accepted")
	}
	body := approveLinkBody(review)
	body["person_id"] = uuid(t, f.db)
	f.call("POST", accountLinkPath+"/"+review.RequestID+"/approve", body, true, "", 409)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_accounts SET label='Changed account' WHERE id=$1`, account); err != nil {
		t.Fatal(err)
	}
	f.call("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), true, "", 409)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE account_person_link_requests SET created_at=clock_timestamp()-interval '11 minutes',expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, review.RequestID); err != nil {
		t.Fatal(err)
	}
	f.call("POST", accountLinkPath+"/lookup", map[string]string{"user_code": offer.Code}, true, "", 410)
	f.call("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), true, "", 410)
	stale := offerLink(t, f, p, key, account)
	if stale.State != "expired" || stale.ShowPrompt {
		t.Fatal("expired offer auto-renewed")
	}
	var renewed agentsetup.AccountLinkView
	decodeResult(t, f.call("POST", accountLinkPath, map[string]string{"operation": "renew", "account_id": account, "device_proof": p.lifecycle}, false, key, 200), &renewed)
	if !renewed.ShowPrompt || renewed.Code == offer.Code {
		t.Fatal("explicit renewal failed")
	}
}

func TestAccountLinkRejectsAnotherInstallationAndRevokedPairing(t *testing.T) {
	f, p, v, key := linkFixture(t)
	other := f.propose("codex")
	f.approve(other, "connect_only")
	otherView := f.redeem(other)
	f.call("POST", accountLinkPath, map[string]string{"operation": "offer", "account_id": otherView.Enrollments[0].AccountID, "device_proof": other.lifecycle}, false, key, 404)
	f.call("POST", accountLinkPath, map[string]string{"operation": "offer", "account_id": otherView.Enrollments[0].AccountID, "device_proof": p.lifecycle}, false, key, 404)
	account := v.Enrollments[0].AccountID
	offer := offerLink(t, f, p, key, account)
	review := reviewLink(t, f, offer)
	f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "", 200)
	f.call("POST", accountLinkPath, map[string]string{"operation": "poll", "account_id": account, "device_proof": p.lifecycle, "request_id": offer.RequestID}, false, key, 410)
	f.call("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), true, "", 410)
}
func TestAccountLinkSecondPersonAndTenantIsolation(t *testing.T) {
	f, p, v, key := linkFixture(t)
	secondAccount := f.addClaude(p, *v.ComputerID, "anna-claude-login").Enrollments
	var account2 string
	for _, e := range secondAccount {
		if e.AccountKey == "anna-claude-login" {
			account2 = e.AccountID
		}
	}
	if account2 == "" {
		t.Fatal("second login not enrolled")
	}
	first := offerLink(t, f, p, key, v.Enrollments[0].AccountID)
	r1 := reviewLink(t, f, first)
	f.call("POST", accountLinkPath+"/"+r1.RequestID+"/approve", approveLinkBody(r1), true, "", 200)
	second := uuid(t, f.db)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH i AS (INSERT INTO identities(issuer,subject,email) VALUES('fixture','anna','anna@example.test') RETURNING id) INSERT INTO principals(tenant_id,id,kind,identity_id,name,roles) SELECT $1,$2,'person',i.id,'Anna',ARRAY['member'] FROM i`, f.tenantID, second)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.db, f.tenantID, second, "member")
	f.cookie = f.call("POST", "/api/auth/dev-login", map[string]string{"email": "anna@example.test"}, false, "", 200).Result().Cookies()[0]
	f.call("POST", "/api/agent-pairing/account-links/"+r1.AccountID+"/unlink", map[string]int64{"expected_revision": 0}, true, "", 404)
	var own []linkReview
	decodeResult(t, f.call("GET", "/api/agent-pairing/account-links", nil, true, "", 200), &own)
	if len(own) != 0 {
		t.Fatal("another person's link leaked")
	}
	offer2 := offerLink(t, f, p, key, account2)
	r2 := reviewLink(t, f, offer2)
	if r2.PersonID != second {
		t.Fatal("second person not named")
	}
	f.call("POST", accountLinkPath+"/"+r2.RequestID+"/approve", approveLinkBody(r2), true, "", 200)
	decodeResult(t, f.call("GET", "/api/agent-pairing/account-links", nil, true, "", 200), &own)
	if len(own) != 1 || own[0].AccountID != account2 {
		t.Fatal("shared computer logins not isolated")
	}
	foreign, err := tenantbootstrap.Create(t.Context(), f.db.App, "link-foreign", "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	am, err := auth.New(auth.Config{Env: "dev", PublicURL: origin, SessionKey: []byte(nonce()), BootstrapTenantSlug: "link-foreign", BootstrapAdminEmail: "foreign@example.test"}, f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	api := &httpapi.Server{Pool: f.db.App, Modules: []httpapi.Module{am, agentpairing.New(f.db.App, origin, "link-foreign")}, Middleware: []func(http.Handler) http.Handler{am.Middleware}}
	other := &fixture{t: t, db: f.db, h: api.Handler(), tenantID: foreign}
	other.cookie = other.call("POST", "/api/auth/dev-login", map[string]string{"email": "foreign@example.test"}, false, "", 200).Result().Cookies()[0]
	other.call("POST", accountLinkPath+"/lookup", map[string]string{"user_code": offer2.Code}, true, "", 404)
	other.call("POST", accountLinkPath+"/"+r2.RequestID+"/approve", approveLinkBody(r2), true, "", 409)
}
func TestAccountLinkConcurrentConfirmationAndPersistentLimits(t *testing.T) {
	f, p, v, key := linkFixture(t)
	offer := offerLink(t, f, p, key, v.Enrollments[0].AccountID)
	review := reviewLink(t, f, offer)
	requests := []*http.Request{f.request("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), true, ""), f.request("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), true, "")}
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for _, r := range requests {
		wg.Add(1)
		go func() { defer wg.Done(); w := httptest.NewRecorder(); f.h.ServeHTTP(w, r); statuses <- w.Code }()
	}
	wg.Wait()
	close(statuses)
	count := map[int]int{}
	for status := range statuses {
		count[status]++
	}
	if count[200] != 1 || count[410] != 1 || f.events("account.linked") != 1 {
		t.Fatal("code consumed twice under concurrency")
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE account_person_link_limits SET attempts=29 WHERE tenant_id=$1 AND bucket='link_lookup'`, f.tenantID); err != nil {
		t.Fatal(err)
	}
	f.call("POST", accountLinkPath+"/lookup", map[string]string{"user_code": "000000"}, true, "", 404)
	f.call("POST", accountLinkPath+"/lookup", map[string]string{"user_code": "000000"}, true, "", 429)
	var attempts int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT attempts FROM account_person_link_limits WHERE tenant_id=$1 AND bucket='link_lookup'`, f.tenantID).Scan(&attempts); err != nil || attempts != 31 {
		t.Fatal("failed lookups did not commit their rate counter")
	}
	// The storage owner guard cannot bind an agent principal as a person.
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2,linked_at=clock_timestamp() WHERE id=$1`, review.AccountID, *v.PrincipalID); err == nil {
		t.Fatal("agent stored as person owner")
	}
	var body map[string]any
	if json.Unmarshal(f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200).Body.Bytes(), &body) != nil || body["computer_state"] != "connected" {
		t.Fatal("linking changed pairing state")
	}
}

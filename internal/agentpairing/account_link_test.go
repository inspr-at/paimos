// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	RequestID  string    `json:"request_id"`
	TenantID   string    `json:"tenant_id"`
	TenantName string    `json:"tenant_name"`
	AccountID  string    `json:"account_id"`
	Harness    string    `json:"harness"`
	Label      string    `json:"account_label"`
	Computer   string    `json:"computer_name"`
	PersonID   string    `json:"person_id"`
	PersonName string    `json:"person_name"`
	Revision   int64     `json:"revision"`
	State      string    `json:"state"`
	ExpiresAt  time.Time `json:"expires_at"`
	Digest     string    `json:"request_digest"`
	Code       string    `json:"-"`
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
	out.Code = v.Code
	return out
}
func approveLinkBody(v linkReview) map[string]any {
	return map[string]any{"tenant_id": v.TenantID, "person_id": v.PersonID, "expected_revision": v.Revision, "request_digest": v.Digest, "user_code": v.Code}
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

func assertLinkTrail(t *testing.T, f *fixture, offer agentsetup.AccountLinkView, state, event string) {
	t.Helper()
	var stored string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT state FROM account_person_link_requests WHERE id=$1`, offer.RequestID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != state || f.events(event) != 1 {
		t.Fatalf("link trail: state=%s, %s events=%d; want %s and one event", stored, event, f.events(event), state)
	}
	var audit string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT "after"::text FROM events WHERE tenant_id=$1 AND type=$2`, f.tenantID, event).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(audit, offer.AccountID) || strings.Contains(audit, strings.ReplaceAll(offer.Code, " ", "")) || strings.Contains(audit, offer.Code) {
		t.Fatal("link event omitted its account or exposed its code")
	}
}

func TestAccountLinkExpiryAudit(t *testing.T) {
	for _, operation := range []string{"poll", "offer", "lookup", "approve", "renew"} {
		t.Run(operation, func(t *testing.T) {
			f, p, v, key := linkFixture(t)
			account := v.Enrollments[0].AccountID
			offer := offerLink(t, f, p, key, account)
			review := reviewLink(t, f, offer)
			if _, err := f.db.Admin.Exec(t.Context(), `UPDATE account_person_link_requests SET created_at=clock_timestamp()-interval '11 minutes',expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, offer.RequestID); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				switch operation {
				case "lookup":
					f.call("POST", accountLinkPath+"/lookup", map[string]string{"user_code": offer.Code}, true, "", 410)
				case "approve":
					f.call("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), true, "", 410)
				default:
					body := map[string]string{"operation": operation, "account_id": account, "device_proof": p.lifecycle}
					if operation == "poll" {
						body["request_id"] = offer.RequestID
					}
					var result agentsetup.AccountLinkView
					decodeResult(t, f.call("POST", accountLinkPath, body, false, key, 200), &result)
					if operation == "renew" {
						if !result.ShowPrompt || result.Code == "" || result.RequestID == offer.RequestID {
							t.Fatal("renew did not replace the expired request")
						}
						// Repeated observations must not expire the old request twice.
						operation = "offer"
					} else if result.Code != "" || result.ShowPrompt {
						t.Fatal("observation repeated the clear code")
					}
				}
				assertLinkTrail(t, f, offer, "expired", "account.link_expired")
			}
		})
	}
}

func TestAccountLinkDisconnectAudit(t *testing.T) {
	for _, scope := range []string{"computer", "enrollment"} {
		for _, mode := range []string{"drain", "revoke_now"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
				f, p, v, key := linkFixture(t)
				account := v.Enrollments[0].AccountID
				offer := offerLink(t, f, p, key, account)
				review := reviewLink(t, f, offer)
				other := f.propose("codex")
				f.approve(other, "connect_only")
				otherView := f.redeem(other)
				otherOffer := offerLink(t, f, other, "aeon_"+otherView.RuntimePrefix+"_"+other.runtime, otherView.Enrollments[0].AccountID)
				path := "/api/agent-pairing/computers/" + *v.ComputerID
				if scope == "enrollment" {
					path += "/enrollments/" + account
				}
				for range 2 {
					f.call("POST", path+"/disconnect", map[string]string{"mode": mode}, true, "", 200)
					assertLinkTrail(t, f, offer, "revoked", "account.link_cancelled")
				}
				f.call("POST", accountLinkPath+"/lookup", map[string]string{"user_code": offer.Code}, true, "", 410)
				f.call("POST", accountLinkPath+"/"+review.RequestID+"/approve", approveLinkBody(review), true, "", 410)
				assertLinkTrail(t, f, offer, "revoked", "account.link_cancelled")
				if reviewLink(t, f, otherOffer).State != "pending" {
					t.Fatal("disconnect cancelled another computer's link")
				}
			})
		}
	}
}

func TestAccountLinkCodeHashAtRest(t *testing.T) {
	f, p, v, key := linkFixture(t)
	account := v.Enrollments[0].AccountID
	offer := offerLink(t, f, p, key, account)
	var clearColumn bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='account_person_link_requests' AND column_name='user_code')`).Scan(&clearColumn); err != nil {
		t.Fatal(err)
	}
	if clearColumn {
		t.Fatal("account link codes still have a plaintext storage column")
	}
	var stored string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT user_code_hash FROM account_person_link_requests WHERE id=$1`, offer.RequestID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	code := strings.ReplaceAll(offer.Code, " ", "")
	pepper, err := hkdf.Key(sha256.New, f.sessionKey, nil, "aeon/account-link-code/v1", sha256.Size)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte(code))
	direct := hmac.New(sha256.New, f.sessionKey)
	direct.Write([]byte(code))
	otherKey := append([]byte(nil), f.sessionKey...)
	otherKey[0] ^= 1
	otherPepper, err := hkdf.Key(sha256.New, otherKey, nil, "aeon/account-link-code/v1", sha256.Size)
	if err != nil {
		t.Fatal(err)
	}
	other := hmac.New(sha256.New, otherPepper)
	other.Write([]byte(code))
	if stored != hex.EncodeToString(mac.Sum(nil)) || stored == hash(code) || stored == code || stored == hex.EncodeToString(direct.Sum(nil)) || stored == hex.EncodeToString(other.Sum(nil)) {
		t.Fatal("account link did not store a domain-separated, server-peppered code HMAC")
	}
	if reviewLink(t, f, offer).RequestID != offer.RequestID {
		t.Fatal("hash lookup lost the offered request")
	}
	for _, operation := range []string{"offer", "poll"} {
		body := map[string]string{"operation": operation, "account_id": account, "device_proof": p.lifecycle}
		if operation == "poll" {
			body["request_id"] = offer.RequestID
		}
		var result agentsetup.AccountLinkView
		decodeResult(t, f.call("POST", accountLinkPath, body, false, key, 200), &result)
		if result.Code != "" || result.ShowPrompt {
			t.Fatal("clear code returned after its first offer")
		}
	}
}

func TestAccountLinkDatabaseReviewCannotApproveWithoutCode(t *testing.T) {
	f, p, v, key := linkFixture(t)
	offer := offerLink(t, f, p, key, v.Enrollments[0].AccountID)
	// Recreate the entire review and its public digest using database columns,
	// without performing a code lookup or knowing the server pepper.
	var forged linkReview
	var storedHash string
	err := f.db.Admin.QueryRow(t.Context(), `SELECT l.id::text,l.tenant_id::text,t.name,a.id::text,a.harness,a.label,q.details->>'computer_name',person.id::text,person.name,a.link_revision,l.state,l.expires_at,l.user_code_hash
FROM account_person_link_requests l JOIN agent_accounts a ON a.tenant_id=l.tenant_id AND a.id=l.account_id
JOIN tenants t ON t.id=l.tenant_id JOIN agent_pairing_enrollments e ON e.tenant_id=a.tenant_id AND e.account_id=a.id
JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
JOIN principals person ON person.tenant_id=l.tenant_id AND person.id=$2
WHERE l.id=$1`, offer.RequestID, f.person).Scan(&forged.RequestID, &forged.TenantID, &forged.TenantName, &forged.AccountID, &forged.Harness, &forged.Label, &forged.Computer, &forged.PersonID, &forged.PersonName, &forged.Revision, &forged.State, &forged.ExpiresAt, &storedHash)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	forged.Digest = hash(string(b))
	wrong := "000000"
	if strings.ReplaceAll(offer.Code, " ", "") == wrong {
		wrong = "000001"
	}
	for _, code := range []string{"", wrong, "12345", "123\n456", storedHash} {
		body := approveLinkBody(forged)
		if code == "" {
			delete(body, "user_code")
		} else {
			body["user_code"] = code
		}
		f.call("POST", accountLinkPath+"/"+forged.RequestID+"/approve", body, true, "", 404)
		var pending bool
		if err := f.db.Admin.QueryRow(t.Context(), `SELECT l.state='pending' AND a.owner_person_id IS NULL FROM account_person_link_requests l JOIN agent_accounts a ON a.tenant_id=l.tenant_id AND a.id=l.account_id WHERE l.id=$1`, forged.RequestID).Scan(&pending); err != nil || !pending || f.events("account.linked") != 0 {
			t.Fatal("a database-only confirmation consumed the code or changed ownership")
		}
	}
	// The same database-built digest succeeds only with the actual offered code.
	forged.Code = strings.ReplaceAll(offer.Code, " ", "-")
	f.call("POST", accountLinkPath+"/"+forged.RequestID+"/approve", approveLinkBody(forged), true, "", 200)
	if f.events("account.linked") != 1 {
		t.Fatal("code-backed confirmation did not link exactly once")
	}
}

func TestAccountLinkRequiresServerKey(t *testing.T) {
	m := agentpairing.New(nil, origin, "pairtest")
	if err := m.ConfigureAccountLink([]byte("short")); err == nil {
		t.Fatal("short session key enabled account linking")
	}
	mux := http.NewServeMux()
	m.Mount(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", accountLinkPath, nil))
	if w.Code != 503 {
		t.Fatal("account linking offered codes without a server pepper")
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
	// Immediate disconnect revokes the runtime key before the endpoint runs.
	f.call("POST", accountLinkPath, map[string]string{"operation": "poll", "account_id": account, "device_proof": p.lifecycle, "request_id": offer.RequestID}, false, key, 401)
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
	am, err := auth.New(auth.Config{Env: "dev", PublicURL: origin, SessionKey: f.sessionKey, BootstrapTenantSlug: "link-foreign", BootstrapAdminEmail: "foreign@example.test"}, f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	foreignPairing := agentpairing.New(f.db.App, origin, "link-foreign")
	if err := foreignPairing.ConfigureAccountLink(f.sessionKey); err != nil {
		t.Fatal(err)
	}
	api := &httpapi.Server{Pool: f.db.App, Modules: []httpapi.Module{am, foreignPairing}, Middleware: []func(http.Handler) http.Handler{am.Middleware}}
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

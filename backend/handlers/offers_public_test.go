// SPDX-License-Identifier: AGPL-3.0-only
package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/handlers"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func publicOfferFixture(t *testing.T, ts *testServer, expired bool) handlers.Offer {
	t.Helper()
	resp := ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, offerSettingsFixture())
	assertStatus(t, resp, 200)
	resp.Body.Close()
	resp = ts.post(t, "/api/customers", ts.adminCookie, map[string]any{"name": "Offer customer", "address": "Teststraße 1, Wien", "contact_name": "Eva Test"})
	assertStatus(t, resp, 201)
	var c struct {
		ID int64 `json:"id"`
	}
	decode(t, resp, &c)
	resp = ts.post(t, "/api/offers", ts.adminCookie, map[string]any{"customer_id": c.ID})
	assertStatus(t, resp, 201)
	var o handlers.Offer
	decode(t, resp, &o)
	o.Document.Positions = []handlers.OfferPosition{{ShortText: "Consulting", Quantity: 12, Unit: "Std./Monat", UnitPriceCents: 30000}}
	if expired {
		o.Document.OfferDate = "2020-01-01"
		o.Document.ValidUntil = "2020-01-31"
	}
	resp = ts.put(t, fmt.Sprintf("/api/offers/%d", o.ID), ts.adminCookie, map[string]any{"revision": o.Revision, "document": o.Document, "finalize": !expired})
	assertStatus(t, resp, 200)
	decode(t, resp, &o)
	if expired {
		o.PublicToken = strings.Repeat("A", 43)
		_, err := db.DB.Exec("UPDATE offers SET status='sent',public_token=? WHERE id=?", o.PublicToken, o.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	return o
}
func acceptRequest(t *testing.T, ts *testServer, o handlers.Offer, name string, confirmed bool) *http.Response {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"name": name, "company": "Offer customer", "note": "Bitte um Terminabstimmung.", "confirmed": confirmed, "revision": o.Revision})
	req, err := http.NewRequest("POST", ts.srv.URL+"/api/public/offers/"+o.PublicToken+"/accept", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Offer-Acceptance", "1")
	req.Header.Set("User-Agent", "Offer Acceptance Test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
func TestPublicOfferAcceptanceAtomicAndImmutable(t *testing.T) {
	ts := newTestServer(t)
	o := publicOfferFixture(t, ts, false)
	if len(o.PublicToken) != 43 {
		t.Fatal("missing capability")
	}
	path := "/api/public/offers/" + o.PublicToken
	resp := ts.get(t, path, "")
	assertStatus(t, resp, 200)
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("missing privacy headers")
	}
	var view map[string]any
	decode(t, resp, &view)
	for _, k := range []string{"id", "customer_id", "public_token", "created_by"} {
		if _, ok := view[k]; ok {
			t.Fatalf("private field %s exposed", k)
		}
	}
	resp = acceptRequest(t, ts, o, "Eva Test", false)
	assertStatus(t, resp, 400)
	resp.Body.Close()
	resp = ts.post(t, path+"/accept", "", map[string]any{"name": "Eva", "confirmed": true})
	assertStatus(t, resp, 403)
	resp.Body.Close()
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, name := range []string{"Eva Test", "Different signer"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			r := acceptRequest(t, ts, o, name, true)
			codes <- r.StatusCode
			r.Body.Close()
		}(name)
	}
	wg.Wait()
	close(codes)
	success, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatalf("unexpected result %d", code)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("acceptance was not single-winner")
	}
	var count int
	var name, hash string
	err := db.DB.QueryRow("SELECT count(*),accepted_name,document_sha256 FROM offer_acceptance_audit WHERE offer_id=?", o.ID).Scan(&count, &name, &hash)
	if err != nil || count != 1 || len(hash) != 64 {
		t.Fatal("missing audit", err)
	}
	resp = ts.get(t, fmt.Sprintf("/api/offers/%d", o.ID), ts.adminCookie)
	var accepted handlers.Offer
	decode(t, resp, &accepted)
	if accepted.Status != "accepted" || accepted.AcceptedAt == nil || accepted.AcceptedName != name || accepted.Revision != o.Revision+1 {
		t.Fatal("receipt does not match committed state")
	}
	if accepted.Document.NetTotalCents != 360000 {
		t.Fatal("snapshot changed")
	}
	if _, err = db.DB.Exec("UPDATE offer_acceptance_audit SET accepted_name='wrong' WHERE offer_id=?", o.ID); err == nil {
		t.Fatal("audit mutable")
	}
	resp = ts.get(t, "/api/offers/acceptances", ts.adminCookie)
	assertStatus(t, resp, 200)
	var notices []handlers.Offer
	decode(t, resp, &notices)
	if len(notices) != 1 || notices[0].PublicToken != "" {
		t.Fatal("creator notification missing or exposes token")
	}
	resp = ts.get(t, "/api/offers/acceptances", ts.memberCookie)
	assertStatus(t, resp, 200)
	decode(t, resp, &notices)
	if len(notices) != 0 {
		t.Fatal("notices disclosed across creators")
	}
	var leaked int
	err = db.DB.QueryRow("SELECT count(*) FROM session_activity WHERE path LIKE ?", "%"+o.PublicToken+"%").Scan(&leaked)
	if err != nil || leaked != 0 {
		t.Fatal("capability leaked in session audit", err)
	}
}
func TestPublicOfferExpiryDraftGateAndRateLimit(t *testing.T) {
	ts := newTestServer(t)
	o := publicOfferFixture(t, ts, true)
	path := "/api/public/offers/" + o.PublicToken
	resp := ts.get(t, path, "")
	assertStatus(t, resp, 200)
	var view struct {
		Status string `json:"status"`
	}
	decode(t, resp, &view)
	if view.Status != "expired" {
		t.Fatal("expired offer not projected")
	}
	resp = acceptRequest(t, ts, o, "Eva Test", true)
	assertStatus(t, resp, 409)
	resp.Body.Close()
	_, err := db.DB.Exec("UPDATE offers SET status='draft' WHERE id=?", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	resp = ts.get(t, path, "")
	assertStatus(t, resp, 404)
	resp.Body.Close()
	resp = ts.get(t, "/api/public/offers/1", "")
	assertStatus(t, resp, 404)
	resp.Body.Close()
	resp = ts.put(t, "/api/integrations/crm/module", ts.adminCookie, map[string]bool{"enabled": false})
	assertStatus(t, resp, 200)
	resp.Body.Close()
	resp = ts.get(t, path, "")
	assertStatus(t, resp, 404)
	resp.Body.Close()
	resp = ts.put(t, "/api/integrations/crm/module", ts.adminCookie, map[string]bool{"enabled": true})
	assertStatus(t, resp, 200)
	resp.Body.Close()
	limited := false
	for i := 0; i < 65; i++ {
		resp = ts.get(t, path, "")
		if resp.StatusCode == 429 {
			limited = true
			if resp.Header.Get("Retry-After") == "" {
				t.Fatal("retry header missing")
			}
		}
		resp.Body.Close()
		if limited {
			break
		}
	}
	if !limited {
		t.Fatal("token rate limit not enforced")
	}
}
func TestPublicOfferAuditFailureRollsBackAcceptance(t *testing.T) {
	ts := newTestServer(t)
	o := publicOfferFixture(t, ts, false)
	_, err := db.DB.Exec(`CREATE TRIGGER test_reject_offer_audit BEFORE INSERT ON offer_acceptance_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	resp := acceptRequest(t, ts, o, "Eva Test", true)
	assertStatus(t, resp, 503)
	resp.Body.Close()
	resp = ts.get(t, fmt.Sprintf("/api/offers/%d", o.ID), ts.adminCookie)
	var after handlers.Offer
	decode(t, resp, &after)
	if after.Status != "sent" || after.AcceptedAt != nil || after.Revision != o.Revision {
		t.Fatal("acceptance committed without audit")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package handlers_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/handlers"
)

func legacyNumberFixture(t *testing.T, ts *testServer, old string) (int64, handlers.Offer) {
	t.Helper()
	res, err := db.DB.Exec(`INSERT INTO customers(name,customer_no) VALUES('Legacy draft customer',?)`, old)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	r := ts.post(t, "/api/offers", ts.adminCookie, map[string]any{"customer_id": id})
	assertStatus(t, r, 201)
	var offer handlers.Offer
	decode(t, r, &offer)
	return id, offer
}

func TestCustomerNumberReformatDraftsAndRollback(t *testing.T) {
	ts := newTestServer(t)
	r := ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, offerSettingsFixture())
	assertStatus(t, r, 200)
	r.Body.Close()
	id, first := legacyNumberFixture(t, ts, "K26-001")
	r = ts.post(t, "/api/offers", ts.adminCookie, map[string]any{"customer_id": id})
	assertStatus(t, r, 201)
	var second handlers.Offer
	decode(t, r, &second)
	path := fmt.Sprintf("/api/customers/%d/number/reformat", id)
	body := map[string]string{"expected_customer_no": "K26-001"}
	r = ts.post(t, path, ts.memberCookie, body)
	assertStatus(t, r, 403)
	r.Body.Close()
	r = ts.post(t, path, ts.adminCookie, map[string]string{"expected_customer_no": "K26-999"})
	assertStatus(t, r, 409)
	r.Body.Close()
	// A failure halfway through the draft rewrite must not change the number,
	// any document, or the monthly allocator.
	if _, err := db.DB.Exec(fmt.Sprintf(`CREATE TRIGGER test_number_abort BEFORE UPDATE OF document ON offers WHEN OLD.id=%d BEGIN SELECT RAISE(ABORT,'test rollback'); END`, second.ID)); err != nil {
		t.Fatal(err)
	}
	r = ts.post(t, path, ts.adminCookie, body)
	assertStatus(t, r, 500)
	r.Body.Close()
	var old string
	if err := db.DB.QueryRow(`SELECT customer_no FROM customers WHERE id=?`, id).Scan(&old); err != nil || old != "K26-001" {
		t.Fatalf("partial customer update: %s %v", old, err)
	}
	if _, err := db.DB.Exec(`DROP TRIGGER test_number_abort`); err != nil {
		t.Fatal(err)
	}
	r = ts.post(t, path, ts.adminCookie, body)
	assertStatus(t, r, 200)
	var result map[string]string
	decode(t, r, &result)
	loc, _ := time.LoadLocation("Europe/Vienna")
	want := "K" + time.Now().In(loc).Format("0601") + "1"
	if result["customer_no"] != want {
		t.Fatal(result)
	}
	for _, before := range []handlers.Offer{first, second} {
		r = ts.get(t, fmt.Sprintf("/api/offers/%d", before.ID), ts.adminCookie)
		assertStatus(t, r, 200)
		var after handlers.Offer
		decode(t, r, &after)
		before.Document.Customer.CustomerNo = want
		if after.Revision != before.Revision+1 || after.Status != "draft" || after.OfferNo != before.OfferNo || !reflect.DeepEqual(after.Document, before.Document) {
			t.Fatal("draft changed beyond customer number and revision")
		}
	}
	r = ts.put(t, fmt.Sprintf("/api/offers/%d", first.ID), ts.adminCookie, map[string]any{"revision": first.Revision, "document": first.Document})
	assertStatus(t, r, 409)
	r.Body.Close()
	r = ts.post(t, path, ts.adminCookie, body)
	assertStatus(t, r, 409)
	r.Body.Close()
	if _, err := db.DB.Exec(`UPDATE customers SET customer_no=? WHERE id=?`, want+"9", id); err == nil {
		t.Fatal("monthly number was not immutable")
	}
}

func TestCustomerNumberReformatRejectsFinalizedAndInvalidNumbers(t *testing.T) {
	ts := newTestServer(t)
	r := ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, offerSettingsFixture())
	assertStatus(t, r, 200)
	r.Body.Close()
	id, offer := legacyNumberFixture(t, ts, "K26-002")
	for _, invalid := range []any{nil, "", "K26090", "K26131", "K2609A", "K260901"} {
		if _, err := db.DB.Exec(`UPDATE customers SET customer_no=? WHERE id=?`, invalid, id); err == nil {
			t.Fatalf("invalid number accepted: %v", invalid)
		}
	}
	if _, err := db.DB.Exec(`UPDATE offers SET status='sent',sent_at=datetime('now') WHERE id=?`, offer.ID); err != nil {
		t.Fatal(err)
	}
	r = ts.post(t, fmt.Sprintf("/api/customers/%d/number/reformat", id), ts.adminCookie, map[string]string{"expected_customer_no": "K26-002"})
	assertStatus(t, r, 409)
	r.Body.Close()
	if _, err := db.DB.Exec(`UPDATE customers SET customer_no='K26091' WHERE id=?`, id); err == nil {
		t.Fatal("finalized customer number changed")
	}
	var current string
	if err := db.DB.QueryRow(`SELECT customer_no FROM customers WHERE id=?`, id).Scan(&current); err != nil || current != "K26-002" {
		t.Fatal("finalized identity drifted")
	}
}

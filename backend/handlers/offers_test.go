// SPDX-License-Identifier: AGPL-3.0-only
package handlers_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/backend/handlers"
)

func offerSettingsFixture() handlers.OfferSettings {
	return handlers.OfferSettings{Sender: handlers.OfferSender{Company: "Test Consulting", Street: "Teststraße 1", PostalCode: "1010", City: "Wien", Country: "Österreich", Email: "office@example.test"}, Defaults: handlers.OfferDefaults{Intro: "Vielen Dank.", Blocks: []handlers.OfferBlock{{Heading: "Leistung", Body: "Beratung laut Positionen."}}, AcceptText: "Bitte unterschrieben zurücksenden.", VATNote: "exklusive 20 % USt"}}
}
func TestOffersLifecycleSnapshotsAndConcurrency(t *testing.T) {
	ts := newTestServer(t)
	settings := offerSettingsFixture()
	resp := ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, settings)
	assertStatus(t, resp, 200)
	resp.Body.Close()
	resp = ts.post(t, "/api/customers", ts.adminCookie, map[string]any{"name": "Testkunde", "address": "Kundenstraße 2\n1010 Wien", "contact_name": "Eva Test", "contact_email": "eva@example.test"})
	assertStatus(t, resp, 201)
	var customer struct {
		ID int64 `json:"id"`
	}
	decode(t, resp, &customer)
	resp = ts.post(t, "/api/offers", ts.adminCookie, map[string]any{"customer_id": customer.ID})
	assertStatus(t, resp, 201)
	var offer handlers.Offer
	decode(t, resp, &offer)
	if offer.Revision != 1 || offer.Status != "draft" || offer.Document.Customer.CustomerNo == "" || offer.Document.Customer.Contact != "Eva Test" {
		t.Fatalf("unexpected draft: %+v", offer)
	}
	offer.Document.Positions = []handlers.OfferPosition{{ShortText: "Beratung", Quantity: 2, Unit: "Tage", UnitPriceCents: 145000}, {ShortText: "Umsetzung", Quantity: 16, Unit: "Std.", UnitPriceCents: 16500}, {ShortText: "Abschluss", Quantity: 1.5, Unit: "Std.", UnitPriceCents: 9999, TotalCents: 1}}
	offer.Document.NetTotalCents = 1
	path := fmt.Sprintf("/api/offers/%d", offer.ID)
	update := map[string]any{"revision": offer.Revision, "document": offer.Document}
	resp = ts.put(t, path, ts.adminCookie, update)
	assertStatus(t, resp, 200)
	decode(t, resp, &offer)
	if offer.Document.NetTotalCents != 568999 || offer.Document.Positions[2].TotalCents != 14999 {
		t.Fatalf("wrong totals: %+v", offer.Document)
	}
	resp = ts.put(t, path, ts.adminCookie, update)
	assertStatus(t, resp, 409)
	resp.Body.Close()
	resp = ts.put(t, path, ts.memberCookie, map[string]any{"revision": offer.Revision, "document": offer.Document})
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "document": offer.Document, "finalize": true})
	assertStatus(t, resp, 200)
	decode(t, resp, &offer)
	if offer.Status != "sent" || offer.SentAt == nil {
		t.Fatal("finalize did not freeze")
	}
	settings.Sender.Company = "Changed company"
	resp = ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, settings)
	assertStatus(t, resp, 200)
	resp.Body.Close()
	resp = ts.get(t, path, ts.adminCookie)
	assertStatus(t, resp, 200)
	var frozen handlers.Offer
	decode(t, resp, &frozen)
	if frozen.Document.Sender.Company != "Test Consulting" {
		t.Fatal("sender snapshot drifted")
	}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "document": offer.Document})
	assertStatus(t, resp, 409)
	resp.Body.Close()
	resp = ts.post(t, "/api/offers", ts.adminCookie, map[string]any{"customer_id": customer.ID, "duplicate_id": offer.ID})
	assertStatus(t, resp, 201)
	var copy handlers.Offer
	decode(t, resp, &copy)
	if copy.OfferNo == offer.OfferNo || copy.Status != "draft" || copy.Document.Customer.CustomerNo != offer.Document.Customer.CustomerNo {
		t.Fatalf("bad duplicate: %+v", copy)
	}
}
func TestOffersRejectInvalidSettingsAndAmounts(t *testing.T) {
	ts := newTestServer(t)
	settings := offerSettingsFixture()
	settings.Sender.IBAN = "AT001234"
	resp := ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, settings)
	assertStatus(t, resp, 400)
	resp.Body.Close()
	settings.Sender.IBAN = "AT61 1904 3002 3457 3201"
	resp = ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, settings)
	assertStatus(t, resp, 200)
	resp.Body.Close()
	resp = ts.put(t, "/api/integrations/crm/offers", ts.memberCookie, settings)
	assertStatus(t, resp, 403)
	resp.Body.Close()
	resp = ts.post(t, "/api/customers", ts.adminCookie, map[string]any{"name": "Testkunde"})
	assertStatus(t, resp, 201)
	var customer struct {
		ID int64 `json:"id"`
	}
	decode(t, resp, &customer)
	resp = ts.post(t, "/api/offers", ts.adminCookie, map[string]any{"customer_id": customer.ID})
	assertStatus(t, resp, 201)
	var offer handlers.Offer
	decode(t, resp, &offer)
	for _, q := range []float64{-1, 0.001, 1000001} {
		offer.Document.Positions[0].Quantity = q
		resp = ts.put(t, fmt.Sprintf("/api/offers/%d", offer.ID), ts.adminCookie, map[string]any{"revision": offer.Revision, "document": offer.Document})
		assertStatus(t, resp, 400)
		resp.Body.Close()
	}
}

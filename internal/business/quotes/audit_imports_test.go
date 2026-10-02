// SPDX-License-Identifier: AGPL-3.0-only
package quotes

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/importer/offers"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestAEON587IssuedImportNativeVerificationBranchAndAccept(t *testing.T) {
	f := newQuoteFixture(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "import-document-integrity")
	ctx := t.Context()
	profile, _ := json.Marshal(syntheticProfile())
	var profileID string
	if err := db.InTenant(dbtest.Seed(ctx), f.database.App, f.tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO quote_document_profiles(tenant_id,name,current_revision) VALUES($1::uuid,'Import profile',1) RETURNING id::text`, f.tenantID).Scan(&profileID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO quote_document_profile_revisions(tenant_id,profile_id,revision,name,definition,created_by_principal_id) VALUES($1::uuid,$2::uuid,1,'Import profile',$3::jsonb,$4::uuid)`, f.tenantID, profileID, string(profile), f.ids["admin"]); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO quote_settings(tenant_id,revision,numbering_time_zone,default_currency,sender,defaults,layout,updated_by_principal_id,default_profile_id) VALUES($1::uuid,1,'Europe/Vienna','EUR','{}','{}','{}',$2::uuid,$3::uuid)`, f.tenantID, f.ids["admin"], profileID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	bundle := offers.Bundle{
		Customers: []offers.Customer{{ID: 17, Name: "Imported buyer", CustomerNo: "K26011", UpdatedAt: "2026-01-01 09:00:00"}},
		Contacts:  []offers.Contact{{ID: 29, CustomerID: 17, Name: "Buyer", Email: "buyer@example.invalid", IsPrimary: true, UpdatedAt: "2026-01-01 09:00:00"}},
		Offers:    []offers.Offer{{ID: 41, CustomerID: 17, OfferNo: "A260101-01", Revision: 1, Status: "sent", SentAt: "2026-01-02T10:00:00Z", Document: json.RawMessage(`{"title":"Imported","subtitle":"Terms","project_ref":"I-1","offer_date":"2026-01-01","valid_until":"2027-12-31","sender":{"company":"Synthetic","email":"sender@example.invalid","street":"Test 1"},"customer":{"name":"Imported buyer","contact":"Buyer","email":"buyer@example.invalid","customer_no":"K26011"},"intro":"Intro","accept_text":"Accept","vat_note":"VAT","footer":{"logo_width_mm":33,"logo_offset_mm":0},"blocks":[{"heading":"Scope","body":"Scope","nodes":[{"kind":"paragraph","text":"Marked prose","marks":[{"start":0,"end":6,"bold":true}]}]}],"positions":[{"short_text":"Service","long_text":"Work","quantity":1,"unit":"item","unit_price_cents":100,"total_cents":100}],"net_total_cents":100}`)}},
	}
	report, err := offers.Import(ctx, f.database.App, f.tenantID, f.ids["admin"], "digest-source", bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	id := report.Mappings[2].NodeID
	actor := tenant.Principal{TenantID: f.tenantID, ID: f.ids["admin"], Kind: tenant.Person, Roles: []string{"admin"}}
	if err := db.InTenant(dbtest.Seed(ctx), f.database.App, f.tenantID, func(tx pgx.Tx) error {
		q, err := readQuote(ctx, tx, id, true)
		if err != nil {
			return err
		}
		v, err := readVersion(ctx, tx, id, 1)
		if err != nil {
			return err
		}
		doc, err := decodeDocument(v.Document)
		if err != nil {
			return err
		}
		sum, err := documentDigest(id, 1, v.OfferNo, doc)
		if err != nil {
			return err
		}
		if sum != v.ContentSHA256 {
			t.Fatalf("import digest does not verify natively: %s vs %s", sum, v.ContentSHA256)
		}
		if doc.Profile == nil || doc.Profile.ID != profileID || len(doc.Sections[0].Nodes[0].Marks) != 1 {
			t.Fatal("profile or prose marks lost")
		}
		_, err = branchQuoteDraft(ctx, tx, actor, id, q.Revision, 1, v.ContentSHA256)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Reimport a distinct quote for acceptance, so branch and accept exercise
	// separate issued records rather than undoing the branch in the fixture.
	bundle.Offers[0].ID = 42
	bundle.Offers[0].OfferNo = "A260101-02"
	report, err = offers.Import(ctx, f.database.App, f.tenantID, f.ids["admin"], "digest-source", bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	id = report.Mappings[2].NodeID
	if err := db.InTenant(dbtest.Seed(ctx), f.database.App, f.tenantID, func(tx pgx.Tx) error {
		v, err := readVersion(ctx, tx, id, 1)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm_contact_principals(tenant_id,contact_node_id,principal_id,bound_by_principal_id) VALUES($1::uuid,$2::uuid,$3::uuid,$3::uuid)`, f.tenantID, v.RecipientContactNodeID, f.ids["admin"]); err != nil {
			return err
		}
		_, err = acceptQuoteVersion(ctx, tx, actor, id, 1, v.ContentSHA256)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	code, q := f.call("admin", "GET", "/api/quotes/"+id, "")
	if code != 200 || q["state"] != "accepted" {
		t.Fatalf("accept %d %v", code, q)
	}
	// Immutable snapshots remain separate from the new draft and decision.
	var count int
	if err := f.database.Admin.QueryRow(ctx, `SELECT count(*) FROM quote_version_snapshots`).Scan(&count); err != nil || count != 2 {
		t.Fatal(fmt.Sprint("snapshot count ", count, " ", err))
	}
}

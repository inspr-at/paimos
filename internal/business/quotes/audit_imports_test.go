// SPDX-License-Identifier: AGPL-3.0-only
package quotes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
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

func TestAEON587HistoricalImporterV1BranchAndAccept(t *testing.T) {
	for _, action := range []string{"branch", "accept", "tampered"} {
		t.Run(action, func(t *testing.T) {
			f := newQuoteFixture(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "historical-import-v1")
			ctx := t.Context()
			bundle := offers.Bundle{
				Customers: []offers.Customer{{ID: 17, Name: "Imported buyer", CustomerNo: "K26011", UpdatedAt: "2026-01-01 09:00:00"}},
				Contacts:  []offers.Contact{{ID: 29, CustomerID: 17, Name: "Buyer", Email: "buyer@example.invalid", IsPrimary: true, UpdatedAt: "2026-01-01 09:00:00"}},
				Offers:    []offers.Offer{{ID: 41, CustomerID: 17, OfferNo: "A260101-01", Revision: 1, Status: "draft", Document: json.RawMessage(`{"title":"Draft","offer_date":"2026-01-01","valid_until":"2099-12-31","sender":{"company":"Synthetic"},"customer":{"name":"Imported buyer","email":"buyer@example.invalid","customer_no":"K26011"},"footer":{},"blocks":[],"positions":[],"net_total_cents":0}`)}},
			}
			report, err := offers.Import(ctx, f.database.App, f.tenantID, f.ids["admin"], "historical-v1", bundle, true)
			if err != nil {
				t.Fatal(err)
			}
			id, contactID := report.Mappings[2].NodeID, report.Mappings[1].NodeID
			raw, err := os.ReadFile("../quotedocument/testdata/importer-v1-document.json")
			if err != nil {
				t.Fatal(err)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, []byte(strings.ReplaceAll(string(raw), "CONTACT_NODE_ID", contactID))); err != nil {
				t.Fatal(err)
			}
			raw = compact.Bytes()
			// The fixture is the historical writer's actual ordered JSON byte
			// layout, independent of both production digest implementations.
			payload := fmt.Sprintf(`{"mode":"document-v1","quote_node_id":%q,"version":1,"offer_no":"A260101-01","document":%s}`, id, raw)
			hash := sha256.Sum256([]byte(payload))
			digest := hex.EncodeToString(hash[:])
			stored := raw
			if action == "tampered" {
				stored = []byte(strings.Replace(string(raw), "Historical importer quote", "Tampered title", 1))
			}
			actor := tenant.Principal{TenantID: f.tenantID, ID: f.ids["admin"], Kind: tenant.Person, Roles: []string{"admin"}}
			// Seed an already-issued v1 row directly. Never update or relabel
			// any issued row or its snapshots, digest, issue or event evidence.
			if err := db.InTenant(dbtest.Seed(ctx), f.database.App, f.tenantID, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE business_quotes SET current_version=1 WHERE quote_node_id=$1::uuid`, id); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO quote_versions(tenant_id,quote_node_id,version,recipient_contact_node_id,currency,title,subtotal,tax_total,total,content_sha256,created_by_principal_id,digest_mode,pricing_mode) VALUES($1::uuid,$2::uuid,1,$3::uuid,'EUR','Historical importer quote',1,0,1,$4,$5::uuid,'document-v1','cent-half-up-v1')`, f.tenantID, id, contactID, digest, actor.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO quote_version_snapshots(tenant_id,quote_node_id,version,document,sender,recipient,legal,layout,offer_no,customer_no,project_ref,offer_date,valid_until,validity_time_zone,document_schema_version,renderer_version) VALUES($1::uuid,$2::uuid,1,$3::jsonb,$3::jsonb->'sender',$3::jsonb->'recipient',$3::jsonb->'legal',$3::jsonb->'layout','A260101-01','K26011','I-1','2026-01-01','2099-12-31','Europe/Vienna',1,'document-v1')`, f.tenantID, id, string(stored)); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO quote_document_lines(tenant_id,quote_node_id,version,position,line_id,pricing_source,short_text,long_text,unit_label,currency,quantity,unit_price_cents,total_cents,net_amount) VALUES($1::uuid,$2::uuid,1,0,'66666666-6666-4666-8666-666666666666','manual','Service','Work','item','EUR',1,100,100,1)`, f.tenantID, id); err != nil {
					return err
				}
				ev, err := events.Append(ctx, tx, actor, events.Change{NodeID: &id, Type: "quote.issued", After: map[string]any{"version": 1, "content_sha256": digest}})
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO quote_issues(tenant_id,quote_node_id,version,issued_by_principal_id,event_id) VALUES($1::uuid,$2::uuid,1,$3::uuid,$4)`, f.tenantID, id, actor.ID, ev.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO crm_contact_principals(tenant_id,contact_node_id,principal_id,bound_by_principal_id) VALUES($1::uuid,$2::uuid,$3::uuid,$3::uuid)`, f.tenantID, contactID, actor.ID); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `UPDATE business_quotes SET state='issued',revision=revision+1 WHERE quote_node_id=$1::uuid`, id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			evidence := func() string {
				t.Helper()
				var value string
				if err := f.database.Admin.QueryRow(ctx, `SELECT jsonb_build_object('version',to_jsonb(v),'snapshot',to_jsonb(s),'issue',to_jsonb(i),'event',to_jsonb(e))::text FROM quote_versions v JOIN quote_version_snapshots s USING(tenant_id,quote_node_id,version) JOIN quote_issues i USING(tenant_id,quote_node_id,version) JOIN events e ON e.tenant_id=i.tenant_id AND e.id=i.event_id WHERE v.quote_node_id=$1::uuid`, id).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			before := evidence()
			err = db.InTenant(dbtest.Seed(ctx), f.database.App, f.tenantID, func(tx pgx.Tx) error {
				if action == "accept" {
					out, err := acceptQuoteVersion(ctx, tx, actor, id, 1, digest)
					if err == nil && out.AcceptedContentSHA256 != digest {
						t.Fatal("acceptance evidence changed the historical digest")
					}
					return err
				}
				q, err := readQuote(ctx, tx, id, true)
				if err != nil {
					return err
				}
				_, err = branchQuoteDraft(ctx, tx, actor, id, q.Revision, 1, digest)
				return err
			})
			if action == "tampered" {
				if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
					t.Fatalf("tampered historical content accepted: %v", err)
				}
			} else if err != nil {
				t.Fatalf("historical v1 %s failed: %v", action, err)
			}
			if evidence() != before {
				t.Fatal("historical digest or issue evidence was rewritten")
			}
			code, q := f.call("admin", "GET", "/api/quotes/"+id, "")
			wantState := map[string]string{"branch": "draft", "accept": "accepted", "tampered": "issued"}[action]
			if code != 200 || q["state"] != wantState {
				t.Fatalf("historical %s state: %d %v", action, code, q)
			}
		})
	}
}

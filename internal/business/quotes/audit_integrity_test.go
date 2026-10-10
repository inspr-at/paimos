// SPDX-License-Identifier: AGPL-3.0-only
package quotes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func auditQuote(t *testing.T) (*quoteFixture, string, map[string]any) {
	t.Helper()
	f := newQuoteFixture(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "quote-integrity")
	code, q := f.call("admin", "POST", "/api/quotes", fmt.Sprintf(`{"title":"Integrity","project_node_id":%q,"customer_org_node_id":%q}`, f.ids["project"], f.ids["org"]))
	if code != 201 {
		t.Fatalf("create %d %v", code, q)
	}
	return f, "/api/quotes/" + q["quote_node_id"].(string), q
}

func TestAEON587ArchivedLegacyFreezeIsReadOnly(t *testing.T) {
	f, path, q := auditQuote(t)
	code, q := f.call("admin", "PATCH", path+"/visibility", fmt.Sprintf(`{"expected_revision":%s,"archived":true}`, revisionOf(q)))
	if code != 200 {
		t.Fatalf("archive %d %v", code, q)
	}
	var before, after []byte
	snapshot := func(dst *[]byte) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(context.Background()), f.database.App, f.tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `SELECT jsonb_build_object('quote',to_jsonb(q),'versions',(SELECT count(*) FROM quote_versions),'events',(SELECT count(*) FROM events)) FROM business_quotes q WHERE q.quote_node_id=$1::uuid`, q["quote_node_id"]).Scan(dst)
		}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot(&before)
	body := fmt.Sprintf(`{"expected_revision":%s,"recipient_contact_node_id":%q,"currency":"EUR","title":"Overwrite archive","terms_markdown":"","lines":[{"description":"Work","cost_unit_node_id":%q,"unit":"hour","quantity":1,"tax_rate":0.2}]}`, revisionOf(q), f.ids["contact"], f.ids["cost"])
	if code, result := f.call("admin", "POST", path+"/versions", body); code != 409 {
		t.Fatalf("freeze %d %v", code, result)
	}
	snapshot(&after)
	if string(before) != string(after) {
		t.Fatal("archived freeze changed quote, versions or events")
	}
}

func TestAEON587VisibilityUndoRejectsStaleRevision(t *testing.T) {
	for _, kind := range []string{"repeated transitions", "unrelated edit", "historical event"} {
		t.Run(kind, func(t *testing.T) {
			f, path, q := auditQuote(t)
			code, q := f.call("admin", "PATCH", path+"/visibility", fmt.Sprintf(`{"expected_revision":%s,"archived":true}`, revisionOf(q)))
			if code != 200 {
				t.Fatalf("archive %d %v", code, q)
			}
			event := f.lastEvent(q["quote_node_id"].(string), "quote.visibility_changed")
			if kind == "repeated transitions" {
				for _, archived := range []bool{false, true} {
					code, q = f.call("admin", "PATCH", path+"/visibility", fmt.Sprintf(`{"expected_revision":%s,"archived":%t}`, revisionOf(q), archived))
					if code != 200 {
						t.Fatalf("transition %d %v", code, q)
					}
				}
			} else if kind == "unrelated edit" {
				if _, err := f.database.Admin.Exec(context.Background(), `UPDATE business_quotes SET project_ref='native edit',revision=revision+1 WHERE quote_node_id=$1::uuid`, q["quote_node_id"]); err != nil {
					t.Fatal(err)
				}
			} else {
				// Historic visibility events lacked revisions; never infer freshness from the boolean.
				if err := db.InTenant(dbtest.Seed(context.Background()), f.database.App, f.tenantID, func(tx pgx.Tx) error {
					id := q["quote_node_id"].(string)
					ev, err := events.Append(context.Background(), tx, tenant.Principal{TenantID: f.tenantID, ID: f.ids["admin"], Kind: tenant.Person}, events.Change{NodeID: &id, Type: "quote.visibility_changed", Before: map[string]any{"archived": false}, After: map[string]any{"archived": true}})
					event = ev.ID
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			_, before := f.call("admin", "GET", path, "")
			if code, result := f.call("admin", "POST", fmt.Sprintf("/api/events/%d/undo", event), ""); code != 409 {
				t.Fatalf("stale undo %d %v", code, result)
			}
			_, after := f.call("admin", "GET", path, "")
			b, _ := json.Marshal(before)
			a, _ := json.Marshal(after)
			if string(b) != string(a) {
				t.Fatal("stale undo changed quote")
			}
		})
	}
}

func TestAEON587ProfileBundleRequiresFontAsset(t *testing.T) {
	f, _, _ := auditQuote(t)
	for _, field := range []string{"", `,"asset_id":""`} {
		bundle := ProfileBundle{Profile: []byte(`{"name":"Missing font","definition":{"schema":"inspr.document-profile.v1","fonts":[{"role":"body","family":"Example","weight":400,"style":"normal"` + field + `}]}}`)}
		for _, apply := range []bool{false, true} {
			_, err := ApplyProfileBundle(context.Background(), f.database.App, f.tenantID, f.ids["admin"], t.TempDir(), "", bundle, false, apply)
			if err == nil || !strings.Contains(err.Error(), "fonts[0].asset_id") {
				t.Fatalf("missing font reference apply=%t: %v", apply, err)
			}
		}
	}
}

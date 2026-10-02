// SPDX-License-Identifier: AGPL-3.0-only
package offers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestAEON587OfflineImportPreservesNativeEdits(t *testing.T) {
	for _, kind := range []string{"customer", "contact", "draft", "primary contact"} {
		t.Run(kind, func(t *testing.T) {
			d := dbtest.Open(t)
			ctx := t.Context()
			tid := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			var actor string
			if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name) VALUES($1::uuid,'native-import','Native import')`, tid); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Admin',ARRAY['admin']) RETURNING id::text`, tid).Scan(&actor); err != nil {
					return err
				}
				if err := dbtest.BindLegacyTx(ctx, tx, tid, actor); err != nil {
					return err
				}
				for slug, prefix := range map[string]string{"organisation": "ORG", "contact": "CON", "quote": "QUO"} {
					if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1::uuid,$2,$2,$3,$2)`, tid, slug, prefix); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			bundle := syntheticBundle()
			bundle.Offers[0].Status = "draft"
			report, err := Import(ctx, d.App, tid, actor, "native-source", bundle, true)
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]string{}
			for _, mapping := range report.Mappings {
				ids[mapping.SourceKind] = mapping.NodeID
			}
			if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
				var query, id string
				switch kind {
				case "customer":
					query = `UPDATE nodes SET fields=jsonb_set(fields,'{customer_notes}','"Native notes"') WHERE id=$1::uuid`
					id = ids["customer"]
					bundle.Customers[0].UpdatedAt = "2026-01-02 09:00:00"
					bundle.Customers[0].Notes = "Source notes"
				case "contact":
					query = `UPDATE nodes SET fields=jsonb_set(fields,'{email}','"native@example.invalid"') WHERE id=$1::uuid`
					id = ids["contact"]
					bundle.Contacts[0].UpdatedAt = "2026-01-02 09:00:00"
					bundle.Contacts[0].Email = "source@example.invalid"
				case "draft":
					query = `UPDATE quote_drafts SET document=jsonb_set(document,'{legal,intro}','"Native terms"'),draft_revision=draft_revision+1 WHERE quote_node_id=$1::uuid`
					id = ids["offer"]
					bundle.Offers[0].Revision++
				case "primary contact":
					query = `UPDATE crm_organisation_profiles SET primary_contact_node_id=NULL,revision=revision+1 WHERE organisation_node_id=$1::uuid`
					id = ids["customer"]
					bundle.Contacts[0].UpdatedAt = "2026-01-02 09:00:00"
				}
				_, err := tx.Exec(ctx, query, id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			snapshot := func() json.RawMessage {
				t.Helper()
				var raw []byte
				if err := d.Admin.QueryRow(ctx, `SELECT jsonb_build_object('nodes',(SELECT jsonb_agg(to_jsonb(n) ORDER BY n.id) FROM nodes n),'quotes',(SELECT jsonb_agg(to_jsonb(q)) FROM business_quotes q),'drafts',(SELECT jsonb_agg(to_jsonb(d)) FROM quote_drafts d),'profiles',(SELECT jsonb_agg(to_jsonb(o)) FROM crm_organisation_profiles o),'mappings',(SELECT jsonb_agg(to_jsonb(i) ORDER BY i.source_kind) FROM paimos_offer_imports i),'events',(SELECT count(*) FROM events))`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			before := snapshot()
			if _, err := Import(ctx, d.App, tid, actor, "native-source", bundle, true); err == nil || !strings.Contains(err.Error(), "import conflict") {
				t.Fatalf("native edit overwritten: %v", err)
			}
			if after := snapshot(); string(before) != string(after) {
				t.Fatal("conflict advanced provenance or changed native records/events")
			}
		})
	}
}

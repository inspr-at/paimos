// SPDX-License-Identifier: AGPL-3.0-only
package offers

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
	"github.com/jackc/pgx/v5/pgconn"
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

func newImportAuditFixture(t *testing.T) (*dbtest.DB, tenant.Principal) {
	t.Helper()
	d := dbtest.Open(t)
	p := tenant.Principal{TenantID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Kind: tenant.Person}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1::uuid,'historical-import','Historical import')`, p.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Admin',ARRAY['admin']) RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		if err := dbtest.BindLegacyTx(t.Context(), tx, p.TenantID, p.ID); err != nil {
			return err
		}
		for slug, prefix := range map[string]string{"organisation": "ORG", "contact": "CON", "quote": "QUO"} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1::uuid,$2,$2,$3,$2)`, p.TenantID, slug, prefix); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return d, p
}

func TestAEON587PreBaselineImportUpgrade(t *testing.T) {
	for _, nativeKind := range []string{"none", "customer", "contact", "offer"} {
		t.Run(nativeKind, func(t *testing.T) {
			d, p := newImportAuditFixture(t)
			ctx := t.Context()
			bundle := syntheticBundle()
			bundle.Offers[0].Status = "draft"
			report, err := Import(ctx, d.App, p.TenantID, p.ID, "historical-source", bundle, true)
			if err != nil {
				t.Fatal(err)
			}
			oldEvents := map[int64]string{}
			if err := db.InTenant(dbtest.Seed(ctx), d.App, p.TenantID, func(tx pgx.Tx) error {
				// Replay the historical writer's event shape. Never rewrite an
				// existing event or its evidence to manufacture the fixture.
				for _, m := range report.Mappings {
					ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &m.NodeID, Type: "import." + m.SourceKind, After: map[string]any{"action": "create", "source_system": "paimos", "source_id": m.SourceID, "source_number": m.SourceNumber, "source_revision": m.SourceRevision, "node_id": m.NodeID}})
					if err != nil {
						return err
					}
					oldEvents[ev.ID] = string(ev.After)
				}
				for _, m := range report.Mappings {
					if m.SourceKind != nativeKind {
						continue
					}
					if _, err := tx.Exec(ctx, `UPDATE nodes SET title='Native title' WHERE id=$1::uuid`, m.NodeID); err != nil {
						return err
					}
					_, err := events.Append(ctx, tx, p, events.Change{NodeID: &m.NodeID, Type: "node.updated", After: map[string]any{"title": "Native title"}})
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// Same-revision replay is harmless; only the newer source attempts
			// to advance these pre-baseline mappings.
			if _, err := Import(ctx, d.App, p.TenantID, p.ID, "historical-source", bundle, true); err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				t.Helper()
				var raw string
				if err := d.Admin.QueryRow(ctx, `SELECT jsonb_build_object('nodes',(SELECT jsonb_agg(to_jsonb(n) ORDER BY id) FROM nodes n),'drafts',(SELECT jsonb_agg(to_jsonb(d)) FROM quote_drafts d),'mappings',(SELECT jsonb_agg(to_jsonb(i) ORDER BY source_kind) FROM paimos_offer_imports i),'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e))::text`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			before := snapshot()
			bundle.Customers[0].UpdatedAt = "2026-01-02 09:00:00"
			bundle.Customers[0].Name = "New source buyer"
			bundle.Contacts[0].UpdatedAt = "2026-01-02 09:00:00"
			bundle.Contacts[0].Phone = "+43 1234"
			bundle.Offers[0].Revision++
			bundle.Offers[0].Document = json.RawMessage(strings.Replace(string(bundle.Offers[0].Document), "Synthetic steel work", "New source scope", 1))
			updated, err := Import(ctx, d.App, p.TenantID, p.ID, "historical-source", bundle, true)
			if nativeKind != "none" {
				if err == nil || !strings.Contains(err.Error(), "import conflict: native "+nativeKind) {
					t.Fatalf("historical native edit accepted: %v", err)
				}
				if snapshot() != before {
					t.Fatal("conflict changed records, provenance or events")
				}
				return
			}
			if err != nil {
				t.Fatalf("unedited pre-baseline import cannot advance: %v", err)
			}
			if err := db.InTenant(dbtest.Seed(ctx), d.App, p.TenantID, func(tx pgx.Tx) error {
				for i, m := range updated.Mappings {
					if m.Action != "update" || m.NodeID != report.Mappings[i].NodeID {
						t.Fatalf("mapping did not advance in place: %+v", m)
					}
					var seededTitle, latestTitle string
					if err := tx.QueryRow(ctx, `SELECT after->'native_baseline'->'node'->>'title' FROM events WHERE node_id=$1::uuid AND type=$2 AND after->>'action'='baseline' ORDER BY id DESC LIMIT 1`, m.NodeID, "import."+m.SourceKind).Scan(&seededTitle); err != nil {
						return err
					}
					if err := tx.QueryRow(ctx, `SELECT after->'native_baseline'->'node'->>'title' FROM events WHERE node_id=$1::uuid AND type=$2 ORDER BY id DESC LIMIT 1`, m.NodeID, "import."+m.SourceKind).Scan(&latestTitle); err != nil {
						return err
					}
					wantOld := map[string]string{"customer": "Synthetic Buyer", "contact": "Sample Person", "offer": "Synthetic steel work"}[m.SourceKind]
					wantNew := map[string]string{"customer": "New source buyer", "contact": "Sample Person", "offer": "New source scope"}[m.SourceKind]
					if seededTitle != wantOld || latestTitle != wantNew {
						t.Fatalf("baseline did not retain pre-update and final snapshots: %q %q", seededTitle, latestTitle)
					}
				}
				for id, original := range oldEvents {
					var raw []byte
					if err := tx.QueryRow(ctx, `SELECT after FROM events WHERE id=$1`, id).Scan(&raw); err != nil {
						return err
					}
					if string(raw) != original {
						t.Fatal("historical event was rewritten")
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type importLockOrderTx struct {
	pgx.Tx
	appended bool
}

func (tx *importLockOrderTx) check(sql string) error {
	if strings.Contains(sql, "INSERT INTO events") {
		tx.appended = true
	} else if tx.appended && (strings.Contains(sql, "INSERT INTO quote_issues") || strings.Contains(sql, "UPDATE business_quotes")) {
		return fmt.Errorf("quote resource write after event counter: %s", sql)
	}
	return nil
}

func (tx *importLockOrderTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := tx.check(sql); err != nil {
		return pgconn.CommandTag{}, err
	}
	return tx.Tx.Exec(ctx, sql, args...)
}

type importErrorRow struct{ err error }

func (r importErrorRow) Scan(...any) error { return r.err }

func (tx *importLockOrderTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := tx.check(sql); err != nil {
		return importErrorRow{err}
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func TestAEON587ImportIssueWritesBeforeEventCounter(t *testing.T) {
	d, p := newImportAuditFixture(t)
	ctx := t.Context()
	bundle := syntheticBundle()
	bundle.Offers[0].Status = "draft"
	report, err := Import(ctx, d.App, p.TenantID, p.ID, "lock-source", bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	id := report.Mappings[2].NodeID
	offer := syntheticBundle().Offers[0]
	doc, err := convertDocument("lock-source", offer, report.Mappings[1].NodeID)
	if err != nil {
		t.Fatal(err)
	}
	batch := &importEvents{}
	ctx = context.WithValue(ctx, importEventsKey{}, batch)
	if err := db.InTenant(dbtest.Seed(ctx), d.App, p.TenantID, func(realTx pgx.Tx) error {
		tx := &importLockOrderTx{Tx: realTx}
		var fence string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID).Scan(&fence); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'),0))`); err != nil {
			return err
		}
		if err := lockMappedRecords(ctx, tx, p.TenantID, "lock-source"); err != nil {
			return err
		}
		if err := writeOffer(ctx, tx, p, id, report.Mappings[0].NodeID, offer, doc, "update"); err != nil {
			return err
		}
		var state string
		var issues, issuanceEvents int
		if err := tx.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM quote_issues WHERE quote_node_id=$1::uuid),(SELECT count(*) FROM events WHERE node_id=$1::uuid AND type='quote.issued') FROM business_quotes WHERE quote_node_id=$1::uuid`, id).Scan(&state, &issues, &issuanceEvents); err != nil {
			return err
		}
		if state != "issued" || issues != 1 || issuanceEvents != 0 || tx.appended {
			t.Fatalf("quote writes were deferred until after the event counter: state=%s issues=%d events=%d", state, issues, issuanceEvents)
		}
		if err := batch.flush(); err != nil {
			return err
		}
		if !tx.appended {
			t.Fatal("final flush did not append")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var linked bool
	if err := d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM quote_issues i JOIN events e ON e.tenant_id=i.tenant_id AND e.id=i.event_id WHERE i.quote_node_id=$1::uuid AND e.type='quote.issued' AND e.node_id=i.quote_node_id AND e.after->>'version'=i.version::text AND e.at=i.issued_at)`, id).Scan(&linked); err != nil || !linked {
		t.Fatalf("issue evidence was not committed with its matching event: %v", err)
	}
}

func TestAEON587ImportIssueFlushFailureRollsBack(t *testing.T) {
	for _, failure := range []string{"missing event", "wrong event", "counter drift"} {
		t.Run(failure, func(t *testing.T) {
			d, p := newImportAuditFixture(t)
			ctx := t.Context()
			bundle := syntheticBundle()
			bundle.Offers[0].Status = "draft"
			report, err := Import(ctx, d.App, p.TenantID, p.ID, "failed-flush", bundle, true)
			if err != nil {
				t.Fatal(err)
			}
			id := report.Mappings[2].NodeID
			offer := syntheticBundle().Offers[0]
			doc, err := convertDocument("failed-flush", offer, report.Mappings[1].NodeID)
			if err != nil {
				t.Fatal(err)
			}
			var beforeEvents int
			if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&beforeEvents); err != nil {
				t.Fatal(err)
			}
			batch := &importEvents{}
			ctx = context.WithValue(ctx, importEventsKey{}, batch)
			err = db.InTenant(dbtest.Seed(ctx), d.App, p.TenantID, func(tx pgx.Tx) error {
				if err := writeOffer(ctx, tx, p, id, report.Mappings[0].NodeID, offer, doc, "update"); err != nil {
					return err
				}
				switch failure {
				case "missing event":
					return nil // The deferred FK must reject COMMIT.
				case "wrong event":
					// Fill both predicted identities, but use the wrong type for
					// issuance. The FK alone cannot prove valid evidence.
					if err := batch.pending[0](); err != nil {
						return err
					}
					_, err := events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "quote.draft_imported", After: map[string]any{"version": 1}})
					return err
				case "counter drift":
					// Inject the interleaving deterministically, without sleeps.
					if _, err := events.Append(ctx, tx, p, events.Change{Type: "import.counter_probe", After: map[string]any{"probe": true}}); err != nil {
						return err
					}
					return batch.flush()
				}
				return nil
			})
			if err == nil {
				t.Fatalf("%s committed invalid issuance", failure)
			}
			wantError := map[string]string{"missing event": "quote_issues_tenant_id_event_id_fkey", "wrong event": "matching quote.issued event", "counter drift": "event counter changed"}[failure]
			if !strings.Contains(err.Error(), wantError) {
				t.Fatalf("%s failed for the wrong reason: %v", failure, err)
			}
			var state string
			var version, issues, versions, events int
			if err := d.Admin.QueryRow(ctx, `SELECT state,current_version,(SELECT count(*) FROM quote_issues),(SELECT count(*) FROM quote_versions),(SELECT count(*) FROM events) FROM business_quotes WHERE quote_node_id=$1::uuid`, id).Scan(&state, &version, &issues, &versions, &events); err != nil {
				t.Fatal(err)
			}
			if state != "draft" || version != 0 || issues != 0 || versions != 0 || events != beforeEvents {
				t.Fatalf("failed flush left partial writes: %s %d %d %d %d/%d", state, version, issues, versions, events, beforeEvents)
			}
		})
	}
}

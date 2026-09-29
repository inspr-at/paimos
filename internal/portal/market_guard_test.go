// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestUnarmedPublicationStaysOffThePublicPage(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytesRepeat())
	mux := http.NewServeMux()
	m.Mount(mux)
	f := &fixture{t: t, m: m, d: d, h: mux}

	tenantID := makeTenant(t, d, "guard-a", "Guard A")
	admin := makePerson(t, d, tenantID, "Ada Admin", "admin")
	product := insertNode(t, d, tenantID, "PPR-1", "portal_product", "Harbour catalog", "A public summary.", "published", "", "{}")
	project := insertNode(t, d, tenantID, "PRJ-1", "project", "Private project", "Private body.", "open", "", "{}")
	wish := insertNode(t, d, tenantID, "PWS-1", "portal_wish", "A public wish", "Join without an account.", "published", product, "{}")
	feature := insertNode(t, d, tenantID, "PCF-1", "portal_feature", "Deadline radar", "A public summary.", "live", product, "{}")
	setPortal(t, d, tenantID, true)

	const quote = "GUARDED-QUOTE on the public help page."
	competitor := decodeItem[competitorItem](t, f.do(http.MethodPost, "/api/portal/competitors", `{"name":"Northwind"}`, "203.0.113.120:1000", &admin, nil, nil))
	mustOK(t, f.do(http.MethodPatch, "/api/portal/competitors/"+competitor.ID, `{"published":true}`, "203.0.113.120:1000", &admin, nil, nil))
	aspect := decodeItem[aspectItem](t, f.do(http.MethodPost, "/api/portal/aspects", `{"label":"Statutory deadlines"}`, "203.0.113.120:1000", &admin, nil, nil))
	day := shiftDay(t, portalDate(t, d, tenantID), -10)
	cell := putCell(t, f, &admin, aspect.ID, competitor.ID, "yes", quote, "https://northwind.example/deadlines", day)

	read := func() string {
		t.Helper()
		rec := f.do(http.MethodGet, "/api/public/portal/guard-a", "", "203.0.113.121:1000", nil, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("public read: %d %s", rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	if strings.Contains(read(), quote) {
		t.Fatal("draft cell was public")
	}

	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE portal_cells SET approved = true, updated_at = clock_timestamp() WHERE id = $1::uuid`, cell.ID)
		return err
	})
	if !moderationDenied(err) {
		t.Fatalf("unarmed approval: %v", err)
	}
	if strings.Contains(read(), quote) {
		t.Fatal("unarmed approval became public")
	}
	if approved, err := cellApproved(t, d, tenantID, cell.ID); err != nil || approved {
		t.Fatalf("cell approved after refusal: %v %v", approved, err)
	}

	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE portal_cells SET approved = true, updated_at = clock_timestamp() WHERE id = $1::uuid`, cell.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(), quote) {
		t.Fatal("armed approval stayed private")
	}

	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `
			INSERT INTO portal_corrections(tenant_id, product_id, competitor_name, aspect_label, statement)
			VALUES ($1::uuid, $2::uuid, 'Northwind', 'Statutory deadlines', 'A factual note without an address.')`, tenantID, product)
		return err
	})
	if err != nil {
		t.Fatalf("pending correction: %v", err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE portal_corrections SET state = 'closed'`)
		return err
	})
	if !moderationDenied(err) {
		t.Fatalf("unarmed close: %v", err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `
			INSERT INTO portal_aspects(tenant_id, product_id, label, position)
			VALUES ($1::uuid, $2::uuid, 'Unarmed row', 9)`, tenantID, product)
		return err
	})
	if !moderationDenied(err) {
		t.Fatalf("unarmed aspect: %v", err)
	}

	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `
			INSERT INTO portal_pace(tenant_id, project_node_id) VALUES ($1::uuid, $2::uuid)
			ON CONFLICT (tenant_id) DO UPDATE SET project_node_id = EXCLUDED.project_node_id`, tenantID, project)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `
			INSERT INTO portal_fulfillments(tenant_id, wish_id, feature_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, tenantID, wish, feature)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM portal_pace`)
		return err
	})
	if !moderationDenied(err) {
		t.Fatalf("unarmed pace delete: %v", err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM nodes WHERE id = $1::uuid`, project)
		return err
	})
	if err != nil {
		t.Fatalf("project delete: %v", err)
	}
	var paceLeft int
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM portal_pace`).Scan(&paceLeft)
	})
	if err != nil || paceLeft != 0 {
		t.Fatalf("pace after project delete: %d %v", paceLeft, err)
	}

	var triggers int
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `
			SELECT count(*) FROM pg_trigger
			WHERE NOT tgisinternal AND tgname IN (
				'portal_competitors_moderation', 'portal_aspects_moderation', 'portal_cells_moderation',
				'portal_cell_revisions_moderation', 'portal_corrections_moderation',
				'portal_pace_moderation', 'portal_fulfillments_moderation'
			)`).Scan(&triggers)
	})
	if err != nil || triggers != 7 {
		t.Fatalf("market triggers %d %v", triggers, err)
	}
}

func moderationDenied(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501" && pgErr.Message == "portal moderation required"
}

func cellApproved(t *testing.T, d *dbtest.DB, tenantID, id string) (bool, error) {
	t.Helper()
	var approved bool
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT approved FROM portal_cells WHERE id = $1::uuid`, id).Scan(&approved)
	})
	return approved, err
}

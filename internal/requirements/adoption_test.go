// SPDX-License-Identifier: AGPL-3.0-only
package requirements

import (
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
)

func TestAdoptedRequirementsRejectNamedReleaseAndGenerateUnplacedWork(t *testing.T) {
	f := setup(t)
	req := f.add("functional", "Accepted requirement")
	f.suggestions(req, true)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by) VALUES($1,$2,$3)`, f.person.TenantID, f.project, f.person.ID)
		return err
	})
	generate := func(release string) error {
		return db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
			if err := authz.LockProjectWrite(t.Context(), tx, f.person.TenantID); err != nil {
				return err
			}
			return generateWork(t.Context(), tx, f.person, f.project, req, release, true)
		})
	}
	if err := generate(f.release); err == nil || !strings.Contains(err.Error(), "This project plans with releases") {
		t.Fatal("named release did not refuse for mode", err)
	}
	if err := generate(""); err != nil {
		t.Fatal(err)
	}
	f.tx(func(tx pgx.Tx) error {
		var count, placed, ships int
		var state string
		if err := tx.QueryRow(t.Context(), `SELECT count(*),count(release_node_id) FROM journey_tickets WHERE project_node_id=$1 AND source='requirements'`, f.project).Scan(&count, &placed); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM ships_in WHERE project_node_id=$1`, f.project).Scan(&ships); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT state FROM journey_releases WHERE release_node_id=$1`, f.release).Scan(&state); err != nil {
			return err
		}
		if count != 2 || placed != 0 || ships != 0 || state != "planning" {
			t.Fatalf("requirements count=%d placed=%d ships=%d archive=%s", count, placed, ships, state)
		}
		return nil
	})
}

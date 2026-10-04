// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestWorkRoutingUpgradePreservesResidencyAndPins(t *testing.T) {
	d := workOldDatabase(t)
	tid := workSeed(t, d, "work-routing-upgrade")
	var leaf, order, run, account, group string
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `UPDATE nodes SET fields=fields||'{"residency":"eu","classic":{"id":42}}'::jsonb WHERE key='KEEP-3' RETURNING id::text`).Scan(&leaf); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label) SELECT $1,'upgrade','codex','fixture',id,'Upgrade' FROM principals WHERE name='Historical agent' RETURNING id::text`, tid).Scan(&account); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO account_groups(tenant_id,harness,name) VALUES($1,'codex','Upgrade group') RETURNING id::text`, tid).Scan(&group); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO account_ticket_pins(tenant_id,ticket_id,harness,account_id) VALUES($1,$2,'codex',$3)`, tid, leaf, account); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,'ORDER-1','Upgrade order',$2 FROM node_kinds WHERE slug='work_order' RETURNING id::text`, tid, leaf).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,assignee_principal_id,status) SELECT $1,$2,id,id,'ready' FROM principals WHERE name='Historical agent'`, tid, order); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,status,residency) SELECT $1,$2,a.id,m.id,'queued','any' FROM principals a,model_profiles m WHERE a.name='Historical agent' AND m.harness='codex' LIMIT 1 RETURNING id::text`, tid, order).Scan(&run)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		var kind, saved string
		if err := tx.QueryRow(ctx, `SELECT k.slug,p.account_id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id JOIN account_ticket_pins p ON p.tenant_id=n.tenant_id AND p.ticket_id=n.id WHERE n.id=$1`, leaf).Scan(&kind, &saved); err != nil {
			return err
		}
		if kind != "work" || saved != account {
			return fmt.Errorf("upgrade lost pin identity: %s %s", kind, saved)
		}
		requirement, _, err := modelprefs.OrderRequirementTrace(ctx, tx, order, nil)
		if err != nil {
			return err
		}
		if requirement != "eu" {
			return fmt.Errorf("upgraded admission requirement: %s", requirement)
		}
		policy, err := modelprefs.RunRequirement(ctx, tx, run)
		if err != nil {
			return err
		}
		if policy.Residency != "eu" {
			return fmt.Errorf("upgraded routing requirement: %s", policy.Residency)
		}
		if _, err := tx.Exec(ctx, `UPDATE account_ticket_pins SET account_id=NULL,group_id=$2 WHERE ticket_id=$1`, leaf, group); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE account_ticket_pins SET group_id=NULL,account_id=$2 WHERE ticket_id=$1`, leaf, account); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE nodes SET fields=fields||'{"residency":"local"}'::jsonb WHERE id=$1`, leaf)
		if err != nil {
			return err
		}
		policy, err = modelprefs.RunRequirement(ctx, tx, run)
		if err != nil {
			return err
		}
		if policy.Residency != "local" {
			return fmt.Errorf("upgraded live recheck: %s", policy.Residency)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The forward guard keeps its original harness validation and rejects
	// non-work targets. Each rejection runs in its own real transaction.
	for _, tc := range []struct{ query, message string }{
		{`UPDATE account_ticket_pins SET harness='claude' WHERE ticket_id=$1`, "pin account harness mismatch"},
		{`UPDATE account_ticket_pins SET ticket_id=(SELECT id FROM nodes WHERE key='PRJ-1') WHERE ticket_id=$1`, "pin target must be a ticket"},
	} {
		err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, tc.query, leaf); return err })
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "23514" || pe.Message != tc.message {
			t.Fatalf("wrong pin guard rejection: %v", err)
		}
	}
}

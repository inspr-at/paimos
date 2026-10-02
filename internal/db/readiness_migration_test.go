// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestReadinessMigrationPreservesOldAccountAndAllowanceWrites(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var tenantID, agentID, accountID, windowID, peerTenant, peerAccount string
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1135_account_readiness.sql" {
			return nil
		}
		if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('readiness-upgrade','Upgrade') RETURNING id::text`).Scan(&tenantID); err != nil {
			return err
		}
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Old daemon') RETURNING id::text`, tenantID).Scan(&agentID); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,'old','codex','daemon',$2,'Old') RETURNING id::text`, tenantID, agentID).Scan(&accountID); err != nil {
				return err
			}
			return tx.QueryRow(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,reserved,pace_model) VALUES($1,$2,now(),now()+interval '1 day','requests',100,41,17,'unrestricted') RETURNING id::text`, tenantID, accountID).Scan(&windowID)
		})
		if err != nil {
			return err
		}
		if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('readiness-peer','Peer') RETURNING id::text`).Scan(&peerTenant); err != nil {
			return err
		}
		return db.InTenant(dbtest.Seed(t.Context()), d.App, peerTenant, func(tx pgx.Tx) error {
			var peerAgent string
			if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Peer daemon') RETURNING id::text`, peerTenant).Scan(&peerAgent); err != nil {
				return err
			}
			return tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,'peer','pi','daemon',$2,'Peer') RETURNING id::text`, peerTenant, peerAgent).Scan(&peerAccount)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if tenantID == "" {
		t.Fatal("upgrade fixture was never installed before migration")
	}
	if err := db.InTenant(t.Context(), d.App, peerTenant, func(tx pgx.Tx) error {
		var members int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM account_readiness_memberships m JOIN account_readiness_resources r ON r.tenant_id=m.tenant_id AND r.id=m.resource_id WHERE m.account_id=$1 AND r.kind='key_cap' AND m.binding_revision=0`, peerAccount).Scan(&members); err != nil {
			return err
		}
		if members != 1 {
			t.Fatalf("peer Pi account membership: %d", members)
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM account_readiness_memberships WHERE account_id=$1`, accountID).Scan(&members); err != nil {
			return err
		}
		if members != 0 {
			t.Fatal("backfill bypassed tenant isolation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err = db.InTenant(t.Context(), d.App, tenantID, func(tx pgx.Tx) error {
		var used, reserved int64
		var sharing bool
		if err := tx.QueryRow(t.Context(), `SELECT w.used,w.reserved,a.share_usage FROM account_allowance_windows w JOIN agent_accounts a ON a.tenant_id=w.tenant_id AND a.id=w.account_id WHERE w.id=$1`, windowID).Scan(&used, &reserved, &sharing); err != nil {
			return err
		}
		if used != 41 || reserved != 17 || sharing {
			t.Fatalf("old ledgers or privacy default changed: %d %d %v", used, reserved, sharing)
		}
		var oldMembers int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM account_readiness_memberships WHERE account_id=$1 AND binding_revision=0`, accountID).Scan(&oldMembers); err != nil {
			return err
		}
		if oldMembers != 1 {
			t.Fatalf("existing account has no canonical membership: %d", oldMembers)
		}
		// Exact old probe statement and old registration column set still work.
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_at=now(),last_probe_ok=true,last_daemon_generation='old-g1' WHERE id=$1`, accountID); err != nil {
			return err
		}
		var newID string
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,'old-client-new-row','codex','daemon',$2,'New') RETURNING id::text`, tenantID, agentID).Scan(&newID); err != nil {
			return err
		}
		var members int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM account_readiness_memberships WHERE account_id=$1 AND binding_revision=0`, newID).Scan(&members); err != nil {
			return err
		}
		if members != 1 {
			t.Fatalf("new account resource seed %d", members)
		}
		var protected int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_class WHERE relname=ANY($1::text[]) AND relrowsecurity AND relforcerowsecurity`, []string{"account_readiness_resources", "account_readiness_memberships", "account_readiness_facts", "account_readiness_checks", "account_readiness_check_keys", "account_readiness_check_waits"}).Scan(&protected); err != nil {
			return err
		}
		if protected != 6 {
			t.Fatalf("tenant RLS protection count %d", protected)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

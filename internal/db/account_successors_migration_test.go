// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Risk: an upgrade silently misses FORCE-RLS tenants, loses immutable pairing
// grants during rollback, or weakens the account ownership/enrollment guard.
func TestAccountSuccessorMigrationAuditIsolationAndRollback(t *testing.T) {
	ctx := dbtest.Seed(t.Context())
	d, err := dbtest.NewUnmigrated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	body, err := os.ReadFile("migrations/1296_account_model_successors.sql")
	if err != nil {
		t.Fatal(err)
	}
	type fixture struct{ tenant, agent, pin, next, account string }
	var fixtures []fixture
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "1296_account_model_successors.sql" {
			return nil
		}
		for i := range 2 {
			f := fixture{}
			if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,'Successor migration') RETURNING id::text`, fmt.Sprintf("successor-%d", i)).Scan(&f.tenant); err != nil {
				return err
			}
			if err := db.InTenant(ctx, d.App, f.tenant, func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Old paired runner') RETURNING id::text`, f.tenant).Scan(&f.agent); err != nil {
					return err
				}
				for j, model := range []string{"gpt-6-sol", "gpt-6.1-sol"} {
					target := &f.pin
					if j == 1 {
						target = &f.next
					}
					if err := tx.QueryRow(ctx, `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'1','codex','openai',$3,'high','strong') RETURNING id::text`, f.tenant, fmt.Sprintf("model-%d", j), model).Scan(target); err != nil {
						return err
					}
				}
				if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,allowed_model_profile_ids)
 VALUES($1,'pinned','codex','old-daemon',$2,'Old paired account',ARRAY[$3::uuid]) RETURNING id::text`, f.tenant, f.agent, f.pin).Scan(&f.account); err != nil {
					return err
				}
				// Bind the old pin to a real enrollment before the upgrade. No
				// trigger disabling or privileged RLS bypass is permitted.
				var request, key, computer string
				if err := tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1,$2,'Fixture runtime',$1::uuid::text||'-fixture-runtime','test-only',ARRAY['account.probe']) RETURNING id::text`, f.tenant, f.agent).Scan(&key); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state)
 VALUES($1,gen_random_uuid(),'123456789',repeat('0',64),repeat('0',64),repeat('0',64),'{}','fixture','redeemed') RETURNING id::text`, f.tenant).Scan(&request); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash)
 VALUES($1,gen_random_uuid(),$2,$3,$4,'old-daemon',repeat('0',64)) RETURNING id::text`, f.tenant, request, f.agent, key).Scan(&computer); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,verification_expires_at,ongoing_approved_at)
 VALUES($1,$2,$3,$4,$5,now()+interval '1 day',now())`, f.tenant, f.account, computer, request, f.pin); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,allowed_model_profile_ids)
 VALUES($1,'empty','codex','other-daemon',$2,'Denied','{}'),($1,'null','codex','other-daemon',$2,'Unpinned',NULL)`, f.tenant, f.agent)
				return err
			}); err != nil {
				return err
			}
			fixtures = append(fixtures, f)
		}
		rollback := errors.New("rollback fixture migration")
		if err := db.InTenant(ctx, d.App, fixtures[0].tenant, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				return err
			}
			var count int
			if err := tx.QueryRow(ctx, `SELECT aeon_backfill_account_model_successors()`).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("trial migration audited %d accounts", count)
			}
			return rollback
		}); !errors.Is(err, rollback) {
			return fmt.Errorf("trial up/down: %w", err)
		}
		var clean bool
		if err := d.App.QueryRow(ctx, `SELECT to_regprocedure('aeon_account_allows_profile(text,uuid[],uuid)') IS NULL AND NOT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Scan(&clean); err != nil {
			return err
		}
		if !clean {
			return fmt.Errorf("rolled-back migration left schema or version behind")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 2 {
		t.Fatal("migration fixture did not run")
	}
	for _, f := range fixtures {
		if err := db.InTenant(ctx, d.App, f.tenant, func(tx pgx.Tx) error {
			var pins, effective []string
			var allowed, sameVersion bool
			if err := tx.QueryRow(ctx, `SELECT allowed_model_profile_ids::text[],aeon_account_allowed_profile_ids(harness,allowed_model_profile_ids)::text[],aeon_account_allows_profile(harness,allowed_model_profile_ids,$2),aeon_model_version_newer('6.0','6') FROM agent_accounts WHERE id=$1`, f.account, f.next).Scan(&pins, &effective, &allowed, &sameVersion); err != nil {
				return err
			}
			if !slices.Equal(pins, []string{f.pin}) || !slices.Contains(effective, f.pin) || !slices.Contains(effective, f.next) || len(effective) != 2 || !allowed || sameVersion {
				return fmt.Errorf("migration failed expansion or rewrote rollback pins")
			}
			var audit bool
			if err := tx.QueryRow(ctx, `SELECT count(*)=1 AND bool_and(before->'allowed_model_profile_ids'=to_jsonb(ARRAY[$1::text])) AND bool_and(after->'effective_model_profile_ids' ? $2::text) FROM events WHERE type='account.model_successors_enabled'`, f.pin, f.next).Scan(&audit); err != nil {
				return err
			}
			if !audit {
				return fmt.Errorf("missing, duplicate or incorrect expansion audit")
			}
			var count int
			if err := tx.QueryRow(ctx, `SELECT aeon_backfill_account_model_successors()`).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("backfill retry duplicates audit")
			}
			var denied bool
			if err := tx.QueryRow(ctx, `SELECT NOT aeon_account_allows_profile('codex',NULL,$1)`, fixtures[1].next).Scan(&denied); err != nil {
				return err
			}
			if f.tenant != fixtures[1].tenant && !denied {
				return fmt.Errorf("successor policy crossed tenant boundary")
			}
			if _, err := tx.Exec(ctx, `SAVEPOINT immutable_pin`); err != nil {
				return err
			}
			_, guardErr := tx.Exec(ctx, `UPDATE agent_accounts SET allowed_model_profile_ids=ARRAY[$2::uuid] WHERE id=$1`, f.account, f.next)
			var pgErr *pgconn.PgError
			if !errors.As(guardErr, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "paired account binding is immutable" {
				return fmt.Errorf("pairing grant guard changed: %v", guardErr)
			}
			_, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT immutable_pin`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MigrateWithHook(ctx, d.App, nil); err != nil {
		t.Fatal(err)
	}
}

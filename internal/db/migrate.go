// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/linkvault"
)

// migrationLock is the session advisory-lock key held for one migration run.
const migrationLock int64 = 780002

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrate applies each unrecorded SQL file in its own transaction, except
// explicitly marked single concurrent index builds (see concurrentIndex).
// A later call skips files already listed in schema_migrations.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return MigrateWithHook(ctx, pool, nil)
}

// MigrateWithHook runs pending migrations and calls before just before each
// file. Migration tests use it to populate an older schema; production passes
// no hook through migrate.
func MigrateWithHook(ctx context.Context, pool *pgxpool.Pool, before func(string) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLock); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLock)
	}()

	// pgvector is created by the deployment as superuser. Migrations only
	// ensure the extension is present.
	if _, err := conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return fmt.Errorf("vector extension: %w", err)
	}
	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}
	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}
	for _, name := range names {
		if applied[name] {
			continue
		}
		if before != nil {
			if err := before(name); err != nil {
				return fmt.Errorf("before %s: %w", name, err)
			}
		}
		if name == "0771_quote_link_drop_plaintext.sql" {
			if err := migratePublicLinkTokens(ctx, pool); err != nil {
				return fmt.Errorf("migrate public link tokens: %w", err)
			}
		}
		if name == "1061_agent_roles_models_read.sql" {
			if err := migrateTenantAgentRoles(ctx, conn, name); err != nil {
				return fmt.Errorf("migrate agent role model discovery: %w", err)
			}
		}
		if err := applyFile(ctx, conn, name); err != nil {
			return err
		}
	}
	return nil
}

// FORCE RLS also applies to the migration owner. Run the additive role backfill
// in each tenant before recording the file globally. ON CONFLICT makes retries
// after a partially completed run safe; the normal file application is a no-op
// for an unscoped owner and harmless for a bypass migration connection.
func migrateTenantAgentRoles(ctx context.Context, conn *pgxpool.Conn, name string) error {
	sql, err := migrationFiles.ReadFile("migrations/" + name)
	if err != nil {
		return err
	}
	// Reuse the advisory-lock connection, including for pools of size one.
	rows, err := conn.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := func() error {
			tx, err := conn.Begin(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if err := enterTenant(ctx, tx, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1::uuid FOR UPDATE`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}(); err != nil {
			return err
		}
	}
	return nil
}

// migratePublicLinkTokens runs after 0770 and before the plaintext column is
// dropped. Each tenant is scoped independently. A missing host key discards
// only the re-copy vault; existing SHA-256 verifiers remain active. Retrying
// after an interrupted migration is safe because converted rows are skipped.
func migratePublicLinkTokens(ctx context.Context, pool *pgxpool.Pool) error {
	key, err := config.LinkKeyFromEnv()
	if err != nil {
		return err
	}
	var tenantIDs []string
	err = InTenant(ctx, pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			tenantIDs = append(tenantIDs, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, tenantID := range tenantIDs {
		err = InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			if key == nil {
				_, err := tx.Exec(ctx, `DELETE FROM quote_public_link_tokens WHERE tenant_id=$1::uuid`, tenantID)
				return err
			}
			rows, err := tx.Query(ctx, `SELECT link_id::text,token FROM quote_public_link_tokens WHERE tenant_id=$1::uuid AND ciphertext IS NULL`, tenantID)
			if err != nil {
				return err
			}
			type oldToken struct{ id, token string }
			var old []oldToken
			for rows.Next() {
				var item oldToken
				if err := rows.Scan(&item.id, &item.token); err != nil {
					rows.Close()
					return err
				}
				old = append(old, item)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, item := range old {
				ciphertext, err := linkvault.Encrypt(key, tenantID, item.id, item.token)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE quote_public_link_tokens SET ciphertext=$1 WHERE tenant_id=$2::uuid AND link_id=$3::uuid`, ciphertext, tenantID, item.id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func applyFile(ctx context.Context, conn *pgxpool.Conn, name string) error {
	body, err := migrationFiles.ReadFile("migrations/" + name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	stmts := splitSQL(string(body))
	if len(stmts) == 0 {
		return fmt.Errorf("migration %s has no statements", name)
	}
	index, table, err := concurrentIndex(string(body), stmts)
	if err != nil {
		return fmt.Errorf("migration %s: %w", name, err)
	}
	if index != "" {
		return applyConcurrentIndex(ctx, conn, name, stmts[0], index, table)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, stmt := range stmts {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate %s: %w", name, err)
		}
	}
	if name == "1063_work_classification_model_identity.sql" {
		if err := backfillWorkMetadata(ctx, tx); err != nil {
			return fmt.Errorf("backfill %s: %w", name, err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s: %w", name, err)
	}
	return nil
}

// Run under ordinary tenant RLS and explicit service project visibility in the
// migration transaction. Failure rolls back both expansion and backfill, so a
// retry cannot silently skip unfinished work. Historical nodes are untouched.
func backfillWorkMetadata(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := enterTenant(AllProjects(ctx, "AEON-503 model identity migration"), tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{properties}',
		 coalesce(field_schema->'properties','{}'::jsonb) || aeon_work_classification_properties())
		 WHERE slug IN ('ticket','task') AND tenant_id=current_setting('aeon.tenant_id')::uuid`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT aeon_backfill_session_model_profiles()`); err != nil {
			return err
		}
	}
	return nil
}

// The first-line opt-out is deliberately narrow: exactly one CREATE [UNIQUE] INDEX
// CONCURRENTLY with unqualified, lowercase identifiers. It is not a general
// escape hatch for arbitrary nontransactional migration batches.
func concurrentIndex(body string, stmts []string) (string, string, error) {
	first, _, _ := strings.Cut(body, "\n")
	if strings.TrimSuffix(first, "\r") != "-- aeon:no-transaction" {
		return "", "", nil
	}
	if len(stmts) == 1 {
		pattern := `^CREATE (?:UNIQUE )?INDEX CONCURRENTLY (?:IF NOT EXISTS )?([a-z_][a-z0-9_]*) ON ([a-z_][a-z0-9_]*)\s*\(`
		if match := regexp.MustCompile(pattern).FindStringSubmatch(stmts[0]); match != nil {
			return match[1], match[2], nil
		}
	}
	return "", "", fmt.Errorf("no-transaction requires one CREATE INDEX CONCURRENTLY statement")
}

func applyConcurrentIndex(ctx context.Context, conn *pgxpool.Conn, name, stmt, index, table string) error {
	// SET LOCAL cannot cover nontransactional DDL. Bound lock acquisition on
	// this connection, then restore its previous setting even after cancellation.
	var lockTimeout string
	if err := conn.QueryRow(ctx, `SHOW lock_timeout`).Scan(&lockTimeout); err != nil {
		return fmt.Errorf("read lock timeout for %s: %w", name, err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, `SELECT set_config('lock_timeout', $1, false)`, lockTimeout); err != nil {
			// Never return a connection with a changed setting to the pool.
			_ = conn.Conn().Close(cleanup)
		}
	}()
	if _, err := conn.Exec(ctx, `SET lock_timeout = '5s'`); err != nil {
		return fmt.Errorf("set lock timeout for %s: %w", name, err)
	}
	// The migration advisory lock remains held on this connection. A crash can
	// leave either a valid index before recording, or an invalid partial build.
	// Reuse only a valid index on the expected table; rebuild an invalid one.
	var valid bool
	var unique bool
	requiresUnique := strings.HasPrefix(stmt, "CREATE UNIQUE INDEX ")
	var existingTable string
	err := conn.QueryRow(ctx, `SELECT i.indisvalid, i.indisunique, t.relname
		FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
		JOIN pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_class t ON t.oid=i.indrelid
		WHERE n.nspname=current_schema() AND c.relname=$1`, index).Scan(&valid, &unique, &existingTable)
	if err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("inspect %s: %w", name, err)
	}
	if err == nil && existingTable != table {
		return fmt.Errorf("migrate %s: index %s belongs to unexpected table %s", name, index, existingTable)
	}
	if err == nil && unique != requiresUnique {
		return fmt.Errorf("migrate %s: index %s has unexpected uniqueness", name, index)
	}
	if err == nil && !valid {
		if _, err := conn.Exec(ctx, `DROP INDEX CONCURRENTLY `+pgx.Identifier{index}.Sanitize()); err != nil {
			return fmt.Errorf("retry %s: %w", name, err)
		}
	}
	if !valid {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate %s: %w", name, err)
		}
		// IF NOT EXISTS may also skip a conflicting non-index relation. Never
		// record a skipped or incomplete build as a successful migration.
		if err := conn.QueryRow(ctx, `SELECT indisvalid AND indrelid=$2::regclass AND indisunique=$3
			FROM pg_index WHERE indexrelid=to_regclass($1)`, index, table, requiresUnique).Scan(&valid); err != nil {
			return fmt.Errorf("verify %s: %w", name, err)
		}
		if !valid {
			return fmt.Errorf("verify %s: index %s is not valid on %s", name, index, table)
		}
	}
	if _, err := conn.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func appliedVersions(ctx context.Context, conn *pgxpool.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[string]bool)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return applied, nil
}

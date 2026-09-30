// SPDX-License-Identifier: AGPL-3.0-only

// Package dbtest gives each test a private Postgres database.
//
// AEON_TEST_DATABASE_URL is a maintenance database on a server where that user
// can CREATE DATABASE (the CI service user is a superuser). Open clones a
// connection-free template migrated once per test binary as a NOSUPERUSER
// NOBYPASSRLS role, then transfers its schema to a fresh restricted app role.
// It drops the database and role on cleanup; the template is dropped when the
// binary exits. Tests run in parallel without sharing tables or app roles.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
)

// EnvDatabaseURL is the maintenance database tests use to create their own.
const EnvDatabaseURL = "AEON_TEST_DATABASE_URL"

// DB is one migrated database. Admin is the maintenance user (it bypasses
// row-level security). App is a LOGIN role with NOSUPERUSER and NOBYPASSRLS
// that owns the migrated schema, so FORCE ROW LEVEL SECURITY applies to it.
type DB struct {
	URL string
	// AppURL connects as the NOSUPERUSER NOBYPASSRLS app role, as production
	// does, for tests that start a whole server against this database.
	AppURL string
	Admin  *pgxpool.Pool
	App    *pgxpool.Pool
	Role   string
	Name   string

	maint string
	once  sync.Once
	err   error
}

// Open creates a database for t and drops it when t finishes.
func Open(t testing.TB) *DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	opened, err := New(ctx)
	if err != nil {
		t.Fatalf("dbtest: %v", err)
	}
	t.Cleanup(func() {
		if err := opened.Close(); err != nil {
			t.Errorf("dbtest cleanup: %v", err)
		}
	})
	return opened
}

// Seed marks ctx as a test fixture writer. Project row-level security shows a
// transaction nothing project-scoped unless a principal or a service path
// opens it (ADR-003 P2), so fixtures write and assert with every project
// visible, like the importer. Tests of visibility itself use a principal.
func Seed(ctx context.Context) context.Context {
	return db.AllProjects(ctx, "test fixture")
}

// BindLegacy seeds the workspace binding represented by fixture classic roles.
// Fixtures describe starting state, so this does not append a mutation event.
// Classic "external" people get no workspace binding: Guest is a project role.
func BindLegacy(t testing.TB, d *DB, tenantID, principalID string) {
	t.Helper()
	if err := BindLegacyTx(context.Background(), d.Admin, tenantID, principalID); err != nil {
		t.Fatalf("bind fixture role: %v", err)
	}
}

// BindRole seeds a workspace binding to the built-in role key. Fixture
// principals that call handlers directly need one to see any project data.
func BindRole(t testing.TB, d *DB, tenantID, principalID, role string) {
	t.Helper()
	BindRoleWith(t, d.Admin, tenantID, principalID, role)
}

// BindRoleWith is BindRole on any connection, such as a superuser pool.
func BindRoleWith(t testing.TB, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, tenantID, principalID, role string) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
		SELECT $1::uuid,$2::uuid,r.id,'workspace' FROM roles r WHERE r.tenant_id=$1::uuid AND r.key=$3
		ON CONFLICT (tenant_id,principal_id) WHERE scope_type='workspace' DO UPDATE SET role_id=EXCLUDED.role_id`, tenantID, principalID, role); err != nil {
		t.Fatalf("bind fixture role %s: %v", role, err)
	}
}

// BindLegacyTx seeds the same fixture binding within an existing transaction.
func BindLegacyTx(ctx context.Context, tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, tenantID, principalID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
		SELECT p.tenant_id,p.id,r.id,'workspace' FROM principals p
		JOIN roles r ON r.tenant_id=p.tenant_id AND r.key=CASE
		  WHEN 'super_admin'=ANY(p.roles) THEN 'owner'
		  WHEN 'admin'=ANY(p.roles) THEN 'admin'
		  WHEN 'member'=ANY(p.roles) OR 'reviewer'=ANY(p.roles) THEN 'member'
		  WHEN 'customer'=ANY(p.roles) THEN 'customer' END
		WHERE p.tenant_id=$1::uuid AND p.id=$2::uuid AND p.kind='person'
		ON CONFLICT DO NOTHING`, tenantID, principalID)
	return err
}

// New creates a migrated database. On failure the database and role are dropped.
// The caller must Close a successful handle.
func New(ctx context.Context) (opened *DB, err error) {
	return newDatabase(ctx, true)
}

// NewUnmigrated creates the same database, role and extension without applying
// migrations. Migration tests use it to seed data between historical files.
func NewUnmigrated(ctx context.Context) (opened *DB, err error) {
	return newDatabase(ctx, false)
}

func newDatabase(ctx context.Context, migrate bool) (opened *DB, err error) {
	base, maint, err := maintenanceURL()
	if err != nil {
		return nil, err
	}
	var template *DB
	if migrate {
		template, err = migratedTemplate(ctx, maint)
		if err != nil {
			return nil, err
		}
	}
	d := &DB{maint: maint}
	defer func() {
		if err != nil {
			if cerr := d.Close(); cerr != nil {
				err = fmt.Errorf("%w (cleanup: %v)", err, cerr)
			}
		}
	}()

	d.Name = randomIdent("aeon_")
	d.Role = randomIdent("aeon_rls_")
	conn, err := pgx.Connect(ctx, maint)
	if err != nil {
		return nil, fmt.Errorf("connect maintenance database: %w", err)
	}
	stmt := `CREATE DATABASE ` + quoteIdent(d.Name)
	if template != nil {
		stmt += ` TEMPLATE ` + quoteIdent(template.Name)
	}
	if _, err = conn.Exec(ctx, stmt); err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("create database %s: %w", d.Name, err)
	}
	if err = conn.Close(ctx); err != nil {
		return nil, fmt.Errorf("close maintenance connection: %w", err)
	}

	d.URL = adminURL(base, d.Name)
	d.Admin, err = pgxpool.New(ctx, d.URL)
	if err != nil {
		return nil, err
	}
	if err = d.Admin.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping maintenance role: %w", err)
	}
	if template == nil {
		if _, err = d.Admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
			return nil, fmt.Errorf("bootstrap vector extension: %w", err)
		}
	}

	password := randomIdent("")
	if err = createRole(ctx, d.Admin, d.Role, password); err != nil {
		return nil, err
	}
	if err = grantApp(ctx, d.Admin, d.Name, d.Role); err != nil {
		return nil, err
	}
	if template != nil {
		// Transfer tables, sequences, types and SECURITY DEFINER functions,
		// including their owner ACLs. Extension objects keep their bootstrap
		// owner. REASSIGN leaves the template role's explicit schema grant,
		// so revoke that copied grant without touching the template itself.
		if _, err = d.Admin.Exec(ctx, `REASSIGN OWNED BY `+quoteIdent(template.Role)+` TO `+quoteIdent(d.Role)+
			`; REVOKE ALL ON SCHEMA public FROM `+quoteIdent(template.Role)); err != nil {
			return nil, fmt.Errorf("assign cloned schema to %s: %w", d.Role, err)
		}
	}
	app := *base
	app.Path = "/" + d.Name
	app.User = url.UserPassword(d.Role, password)
	d.AppURL = app.String()
	d.App, err = openApp(ctx, base, d.Name, d.Role, password, migrate)
	if err != nil {
		return nil, err
	}
	if err = assertNoSuperuser(ctx, d.App, d.Role); err != nil {
		return nil, err
	}
	return d, nil
}

// Close closes both pools and drops the database and role.
func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	d.once.Do(func() { d.err = d.close() })
	return d.err
}

func (d *DB) close() error {
	if d.App != nil {
		d.App.Close()
	}
	if d.Admin != nil {
		d.Admin.Close()
	}
	if d.maint == "" || d.Name == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, d.maint)
	if err != nil {
		return fmt.Errorf("reconnect to drop %s: %w", d.Name, err)
	}
	defer conn.Close(ctx)

	var dropDB error
	for attempt := 0; attempt < 8; attempt++ {
		_, _ = conn.Exec(ctx, `
			SELECT pg_terminate_backend(pid)
			FROM pg_stat_activity
			WHERE datname = $1 AND pid <> pg_backend_pid()`, d.Name)
		_, dropDB = conn.Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(d.Name))
		if dropDB == nil {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 40 * time.Millisecond)
	}
	var dropRole error
	if d.Role != "" {
		_, dropRole = conn.Exec(ctx, `DROP ROLE IF EXISTS `+quoteIdent(d.Role))
	}
	if dropDB != nil {
		return fmt.Errorf("drop database %s: %w", d.Name, dropDB)
	}
	if dropRole != nil {
		return fmt.Errorf("drop role %s: %w", d.Role, dropRole)
	}
	return nil
}

func maintenanceURL() (*url.URL, string, error) {
	raw := os.Getenv(EnvDatabaseURL)
	if raw == "" {
		return nil, "", fmt.Errorf("%s is not set", EnvDatabaseURL)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, "", fmt.Errorf("%s must be a postgres:// URL", EnvDatabaseURL)
	}
	if u.User == nil || u.User.Username() == "" {
		return nil, "", fmt.Errorf("%s must include a user", EnvDatabaseURL)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" || strings.Contains(name, "/") {
		return nil, "", fmt.Errorf("%s must name a database", EnvDatabaseURL)
	}
	return u, raw, nil
}

func adminURL(base *url.URL, dbName string) string {
	u := *base
	u.Path = "/" + dbName
	return u.String()
}

func openApp(ctx context.Context, base *url.URL, dbName, role, password string, migrated bool) (*pgxpool.Pool, error) {
	u := *base
	u.Path = "/" + dbName
	u.User = url.UserPassword(role, password)
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, fmt.Errorf("parse app url: %w", err)
	}
	// The app role must use the same JIT setting as db.Open. Otherwise list
	// plans in tests compile expressions that production sessions do not.
	if _, set := cfg.ConnConfig.RuntimeParams["jit"]; !set && !strings.Contains(cfg.ConnConfig.RuntimeParams["options"], "jit") {
		cfg.ConnConfig.RuntimeParams["jit"] = "off"
	}
	if !migrated {
		cfg.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect app role: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping app role: %w", err)
	}
	return pool, nil
}

func createRole(ctx context.Context, admin *pgxpool.Pool, role, password string) error {
	var stmt string
	if err := admin.QueryRow(ctx, `
		SELECT format(
			'CREATE ROLE %I WITH LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE PASSWORD %L',
			$1::text, $2::text)`, role, password).Scan(&stmt); err != nil {
		return fmt.Errorf("create role statement: %w", err)
	}
	if _, err := admin.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("create role %s: %w", role, err)
	}
	return nil
}

func grantApp(ctx context.Context, admin *pgxpool.Pool, dbName, role string) error {
	dbIdent := quoteIdent(dbName)
	roleIdent := quoteIdent(role)
	stmts := []string{
		fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, dbIdent, roleIdent),
		fmt.Sprintf(`GRANT USAGE, CREATE ON SCHEMA public TO %s`, roleIdent),
	}
	for _, stmt := range stmts {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("grant to %s: %w", role, err)
		}
	}
	return nil
}

func assertNoSuperuser(ctx context.Context, app *pgxpool.Pool, role string) error {
	var user string
	var super, bypass bool
	if err := app.QueryRow(ctx, `
		SELECT current_user, rolsuper, rolbypassrls
		FROM pg_roles WHERE rolname = current_user`).Scan(&user, &super, &bypass); err != nil {
		return fmt.Errorf("app role attributes: %w", err)
	}
	if user != role || super || bypass {
		return fmt.Errorf("app connection is %s (super=%v bypass=%v), want nosuperuser %s", user, super, bypass, role)
	}
	return nil
}

func quoteIdent(name string) string {
	return pgx.Identifier{name}.Sanitize()
}

func randomIdent(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("dbtest: crypto/rand failed: " + err.Error())
	}
	return prefix + hex.EncodeToString(b[:])
}

// SPDX-License-Identifier: AGPL-3.0-only

package dbtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
)

// Each package's test binary has its own cache and random database/role names;
// packages never connect to or lock one another's templates. Failed creation
// is not cached, so a canceled first caller does not poison the entire binary.
var templates struct {
	sync.Mutex
	db    *DB
	lease *os.File
}

func migratedTemplate(ctx context.Context, maint string) (_ *DB, err error) {
	templates.Lock()
	defer templates.Unlock()
	if templates.db != nil {
		if templates.db.maint != maint {
			return nil, fmt.Errorf("%s changed after template initialization", EnvDatabaseURL)
		}
		return templates.db, nil
	}
	d, err := NewUnmigrated(ctx)
	if err != nil {
		return nil, fmt.Errorf("create migration template: %w", err)
	}
	defer func() {
		if err != nil {
			if cerr := d.Close(); cerr != nil {
				err = fmt.Errorf("%w (template cleanup: %v)", err, cerr)
			}
		}
	}()
	d.App.Close()
	d.App, err = db.Open(ctx, d.AppURL)
	if err != nil {
		return nil, fmt.Errorf("migrate template: %w", err)
	}
	if err = seedAccountUseFixturePolicy(ctx, d); err != nil {
		return nil, fmt.Errorf("account-use fixture policy: %w", err)
	}
	// CREATE DATABASE TEMPLATE rejects a source with other connections.
	// Close both pools and prohibit new connections before publishing it.
	d.App.Close()
	d.Admin.Close()
	conn, err := pgx.Connect(ctx, maint)
	if err != nil {
		return nil, fmt.Errorf("connect to seal template: %w", err)
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, `ALTER DATABASE `+quoteIdent(d.Name)+` ALLOW_CONNECTIONS false;
		ALTER ROLE `+quoteIdent(d.Role)+` NOLOGIN`); err != nil {
		return nil, fmt.Errorf("seal migration template: %w", err)
	}
	lease, err := watchTemplate(d)
	if err != nil {
		return nil, err
	}
	templates.db, templates.lease = d, lease
	return d, nil
}

// Existing fixtures describe pre-matrix, all-allowed tenants. Preserve that
// starting policy explicitly, without relaxing any production predicate or
// removing audits. The capability sweep additionally activates every such
// tenant. Migration tests use NewUnmigrated and retain the production seed;
// policy tests opt out on their tenant INSERT with a transaction-local flag.
func seedAccountUseFixturePolicy(ctx context.Context, d *DB) error {
	activate := "false"
	if os.Getenv("AEON_TEST_ACCOUNT_USE_ACTIVATED") == "1" {
		activate = "true"
	}
	_, err := d.App.Exec(ctx, `CREATE FUNCTION aeon_test_account_use_seed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE prior text := current_setting('aeon.tenant_id',true);
BEGIN
    IF current_setting('aeon.test_account_use_defaults',true)='production' THEN RETURN NEW; END IF;
    PERFORM set_config('aeon.tenant_id',NEW.id::text,true);
    UPDATE account_use_rules SET new_accounts='allow',new_contexts='allow',
        enforced_at=CASE WHEN `+activate+` THEN coalesce(enforced_at,clock_timestamp()) ELSE enforced_at END;
    PERFORM set_config('aeon.tenant_id',coalesce(prior,''),true);
    RETURN NEW;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('aeon.tenant_id',coalesce(prior,''),true);
    RAISE;
END $$;
CREATE TRIGGER zz_test_account_use_seed AFTER INSERT ON tenants FOR EACH ROW EXECUTE FUNCTION aeon_test_account_use_seed();`)
	return err
}

const templateCleanupArg = "--aeon-dbtest-template-cleanup"

// testing.TB has no binary-exit cleanup hook. A small child of this same
// executable waits for EOF on an anonymous pipe; the OS closes the writer even
// on os.Exit or a test timeout. It then drops only this binary's exact template
// and role. No shell, psql dependency, credentials in argv or test edits are
// needed, and no broad database/role-prefix cleanup touches other workers.
func watchTemplate(d *DB) (*os.File, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("template cleanup executable: %w", err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("template cleanup pipe: %w", err)
	}
	defer reader.Close()
	cmd := exec.Command(executable, templateCleanupArg)
	cmd.Stdin = reader
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		writer.Close()
		return nil, fmt.Errorf("start template cleanup: %w", err)
	}
	// Reap an unexpectedly early exit while the test binary is still running.
	go func() { _ = cmd.Wait() }()
	if err := json.NewEncoder(writer).Encode(templateResource{d.maint, d.Name, d.Role}); err != nil {
		writer.Close()
		return nil, fmt.Errorf("register template cleanup: %w", err)
	}
	return writer, nil
}

type templateResource struct {
	Maintenance string
	Name        string
	Role        string
}

func init() {
	if len(os.Args) != 2 || os.Args[1] != templateCleanupArg {
		return
	}
	if err := cleanupTemplate(os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest template cleanup (failure %d): %v\n", cleanupFailures.Add(1), err)
		os.Exit(1)
	}
	os.Exit(0)
}

func cleanupTemplate(input io.Reader) error {
	decoder := json.NewDecoder(input)
	var resource templateResource
	if err := decoder.Decode(&resource); err != nil {
		return fmt.Errorf("read template resource: %w", err)
	}
	// Include any bytes buffered by the JSON decoder before waiting for EOF.
	if _, err := io.Copy(io.Discard, io.MultiReader(decoder.Buffered(), input)); err != nil {
		return fmt.Errorf("wait for template lease: %w", err)
	}
	return (&DB{maint: resource.Maintenance, Name: resource.Name, Role: resource.Role}).Close()
}

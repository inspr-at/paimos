// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/principallink"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestClassicImportWaitsForInviteLinkLock(t *testing.T) {
	d := dbtest.Open(t)
	ctx, cancel := context.WithTimeout(dbtest.Seed(t.Context()), 10*time.Second)
	defer cancel()
	tid, err := tenantbootstrap.Create(ctx, d.App, "import-lock", "Import lock")
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,532))`, tid); err != nil {
		t.Fatal(err)
	}
	importTx, err := d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer importTx.Rollback(context.Background())
	if _, err := importTx.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),
		set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`, tid); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := importTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		var conflicts []ImportConflict
		_, _, err := importUsers(ctx, importTx, tid, Snapshot{SourceID: "race", Users: []Record{{"id": 7, "username": "classic", "email": "person@example.test", "role": "admin"}}}, &conflicts)
		if err == nil {
			err = importTx.Commit(ctx)
		}
		result <- err
		close(result)
	}()
	defer func() {
		_ = blocker.Rollback(context.Background())
		cancel()
		<-result
	}()
	// Observe the actual advisory lock wait, rather than assuming a goroutine
	// has started or relying on a sleep. Import must write nothing while held.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := d.Admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("import did not wait for invite/link lock: %v", err)
		case <-ctx.Done():
			t.Fatal("import never waited for invite/link lock")
		case <-ticker.C:
		}
	}
	var count int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM identities WHERE issuer='paimos-classic' AND subject='race:7'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("import changed identities before acquiring invite/link lock")
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("import did not resume after invite/link transaction committed")
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM identities WHERE issuer='paimos-classic' AND subject='race:7'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("import did not create classic identity after lock release: %d", count)
	}
}

func TestClassicUsernameEmailAndCanonicalAssignments(t *testing.T) {
	d := dbtest.Open(t)
	tid, err := tenantbootstrap.Create(t.Context(), d.App, "identity-import", "Identity import")
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{SourceID: "source", Users: []Record{{"id": 7, "username": "mba", "full_name": "Markus Barta", "email": "mba@example.test", "role": "member"}}, Projects: []Project{{Record: Record{"id": 1, "key": "ID", "name": "Identity"}, Issues: []Record{{"id": 1, "issue_key": "ID-1", "title": "Ticket", "type": "ticket", "assignee_id": 7}}}}}
	writer := PostgresWriter{Pool: d.App}
	if _, err := writer.Write(t.Context(), s, "identity-import"); err != nil {
		t.Fatal(err)
	}
	var alias string
	txdo := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, fn); err != nil {
			t.Fatal(err)
		}
	}
	txdo(func(tx pgx.Tx) error {
		var email, name, storedEmail string
		if err := tx.QueryRow(t.Context(), `SELECT p.id::text,p.name,p.email,e.after->'classic'->>'email' FROM principals p JOIN events e ON e.tenant_id=p.tenant_id AND e.after->'principal'->>'id'=p.id::text WHERE p.tenant_id=$1 AND e.type='import.user_created'`, tid).Scan(&alias, &name, &email, &storedEmail); err != nil {
			return err
		}
		if name != "mba" || email != "mba@example.test" || email != storedEmail {
			return fmt.Errorf("lost username/email")
		}
		return nil
	})
	target, err := tenantbootstrap.BindOIDC(t.Context(), d.App, "identity-import", "https://id.example.test", "markus", "Markus Barta", "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := principallink.New(d.App).Link(t.Context(), "identity-import", alias, target); err != nil {
		t.Fatal(err)
	}
	// Partial user payloads preserve email. Future imports write the canonical
	// assignee while immutable classic records retain source IDs and username.
	delete(s.Users[0], "email")
	if _, err := writer.Write(t.Context(), s, "identity-import"); err != nil {
		t.Fatal(err)
	}
	txdo(func(tx pgx.Tx) error {
		var name, email, assigned string
		if err := tx.QueryRow(t.Context(), `SELECT name,email FROM principals WHERE tenant_id=$1 AND id=$2`, tid, alias).Scan(&name, &email); err != nil {
			return err
		}
		if name != "mba" || email != "mba@example.test" {
			return fmt.Errorf("partial snapshot erased metadata")
		}
		if err := tx.QueryRow(t.Context(), `SELECT fields->>'assignee' FROM nodes WHERE tenant_id=$1 AND key='ID-1'`, tid).Scan(&assigned); err != nil {
			return err
		}
		if assigned != target {
			return fmt.Errorf("import persisted alias %s", assigned)
		}
		return nil
	})
	if report, err := writer.Write(t.Context(), s, "identity-import"); err != nil || report.Updated != 0 {
		t.Fatalf("replay %+v %v", report, err)
	}
	// Repair existing imports locally from the stored identity email, with an
	// event, then prove a repeat creates no additional mutation.
	txdo(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE principals SET email=NULL WHERE tenant_id=$1 AND id=$2`, tid, alias)
		return err
	})
	if report, err := BackfillPrincipals(t.Context(), d.App, tid); err != nil || report.Counts["emails"] != 1 {
		t.Fatalf("email backfill %+v %v", report, err)
	}
	if report, err := BackfillPrincipals(t.Context(), d.App, tid); err != nil || report.Writes != 0 {
		t.Fatalf("backfill replay %+v %v", report, err)
	}
}

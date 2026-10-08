// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestAttentionEventsMigrationUpgradeAndReapply(t *testing.T) {
	const name = "1241_autopilot_attention_events.sql"
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	stop := errors.New("release 123 schema boundary")
	err = db.MigrateWithHook(t.Context(), d.App, func(next string) error {
		if next == name {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("expected release 123 boundary before %s, got %v", name, err)
	}
	var versions string
	if err := d.App.QueryRow(t.Context(), `SELECT string_agg(version, ',' ORDER BY version) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	names := migrationNames(t)
	var prefix []string
	for _, next := range names {
		if next >= name {
			break
		}
		prefix = append(prefix, next)
	}
	if len(prefix) == 0 || prefix[len(prefix)-1] != "1240_work_account_pins.sql" || versions != strings.Join(prefix, ",") {
		t.Fatalf("release 123 migration prefix not installed: %s", versions)
	}

	type definition struct {
		policyID, functionID uint32
		policy, function     string
	}
	read := func() definition {
		t.Helper()
		var got definition
		if err := d.App.QueryRow(t.Context(), `SELECT p.oid, pg_get_expr(p.polqual, p.polrelid),
            'aeon_work_status_cause()'::regprocedure::oid,
            pg_get_functiondef('aeon_work_status_cause()'::regprocedure)
            FROM pg_policy p WHERE p.polrelid='events'::regclass AND p.polname='events_project_visibility'`).Scan(
			&got.policyID, &got.policy, &got.functionID, &got.function); err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := read()
	if strings.Contains(before.policy, "status_autopilot.attention_apply") || strings.Contains(before.function, "status_autopilot.attention_apply") {
		t.Fatal("upgrade fixture already has attention event definitions")
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatalf("upgrade release 123 schema: %v", err)
	}
	after := read()
	if after.policyID != before.policyID || after.functionID != before.functionID {
		t.Fatal("upgrade replaced policy or function identity")
	}
	for _, event := range []string{"status_autopilot.attention_apply", "status_autopilot.attention_dismiss", "status_autopilot.attention_undone"} {
		if !strings.Contains(after.policy, event) || !strings.Contains(after.function, event) {
			t.Fatalf("upgrade did not admit %s in policy and cause helper", event)
		}
	}
	if tag, err := d.App.Exec(t.Context(), `DELETE FROM schema_migrations WHERE version=$1`, name); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("remove migration record for reapply: %v (%d rows)", err, tag.RowsAffected())
	}
	var replayed []string
	if err := db.MigrateWithHook(t.Context(), d.App, func(next string) error {
		replayed = append(replayed, next)
		return nil
	}); err != nil {
		t.Fatalf("idempotent attention migration reapply: %v", err)
	}
	if len(replayed) != 1 || replayed[0] != name || read() != after {
		t.Fatalf("reapply changed definitions or replayed other migrations: %v", replayed)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, func(next string) error {
		t.Fatalf("completed migration replayed: %s", next)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestReleaseNotesBackfillUsage(t *testing.T) {
	const usage = "usage: aeon release-notes backfill --tenant SLUG [--actor-principal-id UUID] [--apply]"
	for _, args := range [][]string{nil, {"--apply"}, {"--tenant", "synthetic", "extra"}, {"--tenant", ""}} {
		err := releaseNotesBackfill(context.Background(), args, &bytes.Buffer{})
		if err == nil || err.Error() != usage {
			t.Fatalf("args %q: %v", args, err)
		}
	}
}

func TestReleaseNotesBackfillEmptyTenantDoesNotWrite(t *testing.T) {
	database := dbtest.Open(t)
	ctx := context.Background()
	tenantID, err := tenantbootstrap.Create(ctx, database.App, "notes-backfill", "Notes")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_ENV", "dev")
	t.Setenv("AEON_DATABASE_URL", database.AppURL)
	t.Setenv("AEON_DATABASE_PASSWORD_FILE", "")
	t.Setenv("AEON_MESSAGING_KEY_FILE", "")
	t.Setenv("AEON_LINK_KEY_FILE", "")
	var out bytes.Buffer
	if err := releaseNotesBackfill(ctx, []string{"--tenant", "notes-backfill"}, &out); err != nil {
		t.Fatal(err)
	}
	var dry map[string]any
	if err := json.Unmarshal(out.Bytes(), &dry); err != nil || dry["applied"] != false || dry["inserted"] != float64(0) || dry["tenant_id"] != tenantID {
		t.Fatalf("dry-run: %s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"planned":[]`)) || !bytes.Contains(out.Bytes(), []byte(`"skipped":[]`)) {
		t.Fatalf("empty slices: %s", out.String())
	}
	out.Reset()
	if err := releaseNotesBackfill(ctx, []string{"--tenant", "notes-backfill", "--apply"}, &out); err != nil {
		t.Fatal(err)
	}
	var applied map[string]any
	if err := json.Unmarshal(out.Bytes(), &applied); err != nil || applied["applied"] != true || applied["inserted"] != float64(0) {
		t.Fatalf("apply: %s", out.String())
	}
	var operators int
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM principals WHERE kind='agent' AND name='Access operator'`).Scan(&operators)
	}); err != nil || operators != 0 {
		t.Fatalf("operators=%d err=%v", operators, err)
	}
	if strings.Contains(out.String(), "postgres://") {
		t.Fatal("report contains a database url")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestReleaseNotesBackfillUsage(t *testing.T) {
	const usage = "usage: aeon release-notes backfill --tenant SLUG --project KEY --actor-principal-id UUID [--release VERSION | --all-missing] [--apply]"
	for _, args := range [][]string{nil, {"--apply"}, {"--tenant", "synthetic", "extra"}, {"--tenant", ""}, {"--tenant", "x", "--project", "AEON"}, {"--tenant", "x", "--actor-principal-id", "00000000-0000-0000-0000-000000000001"}, {"--tenant", "x", "--project", "AEON", "--actor-principal-id", "00000000-0000-0000-0000-000000000001", "--release", "260115100000.0.0", "--all-missing"}} {
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
	var actorID, projectID string
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Backfill admin') RETURNING id::text`, tenantID).Scan(&actorID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,'PRJ-1','Notes project','{"project_key":"NOTES"}'::jsonb FROM node_kinds WHERE slug='project' RETURNING id::text`, tenantID).Scan(&projectID)
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, database, tenantID, actorID, "admin")
	args := []string{"--tenant", "notes-backfill", "--project", "NOTES", "--actor-principal-id", actorID}

	var out bytes.Buffer
	if err := releaseNotesBackfillWithHistory(ctx, args, &out, func() (releasehistory.History, error) { return releasehistory.History{}, nil }); err != nil {
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
	if err := releaseNotesBackfillWithHistory(ctx, append(args, "--apply"), &out, func() (releasehistory.History, error) { return releasehistory.History{}, nil }); err != nil {
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

func TestReleaseNotesBackfillManifestCLISelectors(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	tenantID, err := tenantbootstrap.Create(ctx, database.App, "cli-backfill", "CLI backfill")
	if err != nil {
		t.Fatal(err)
	}
	var actor, project, ticket string
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Admin') RETURNING id::text`, tenantID).Scan(&actor); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,'PRJ-1','Project','{"project_key":"CLI"}'::jsonb FROM node_kinds WHERE slug='project' RETURNING nodes.id::text`, tenantID).Scan(&project); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state,fields) SELECT $1,id,'CLI-1','Ticket',$2,'done','{"pill_en":"Clear changes","pill_de":"Klare Änderungen","benefit_en":"You see benefits.","benefit_de":"Sie sehen Vorteile."}'::jsonb FROM node_kinds WHERE slug='ticket' RETURNING nodes.id::text`, tenantID, project).Scan(&ticket)
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, database, tenantID, actor, "admin")
	t.Setenv("AEON_ENV", "dev")
	t.Setenv("AEON_DATABASE_URL", database.AppURL)
	t.Setenv("AEON_DATABASE_PASSWORD_FILE", "")
	t.Setenv("AEON_MESSAGING_KEY_FILE", "")
	t.Setenv("AEON_LINK_KEY_FILE", "")
	when := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	load := func() (releasehistory.History, error) {
		return releasehistory.History{Releases: []releasehistory.Release{
			{Version: "260115100000.0.0", State: releasehistory.StatePublished, TaggedAt: &when, Tickets: []string{"CLI-1"}},
			{Version: "260115110000.0.0", State: releasehistory.StatePublished, TaggedAt: &when, Tickets: []string{"CLI-1"}},
		}}, nil
	}
	args := []string{"--tenant", "cli-backfill", "--project", "CLI", "--actor-principal-id", actor, "--release", "260115100000.0.0"}
	var out bytes.Buffer
	if err := releaseNotesBackfillWithHistory(ctx, args, &out, load); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"excluded_keys":[]`) || !strings.Contains(out.String(), `"gap_keys":[]`) || !strings.Contains(out.String(), `"notes_count":1`) || !strings.Contains(out.String(), `"inserted":0`) || strings.Contains(out.String(), "260115110000.0.0") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := releaseNotesBackfillWithHistory(ctx, append(args, "--apply"), &out, load); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"inserted":1`) {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := releaseNotesBackfillWithHistory(ctx, args, &out, load); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"unchanged":1`) || !strings.Contains(out.String(), `"planned":[]`) {
		t.Fatal(out.String())
	}
}

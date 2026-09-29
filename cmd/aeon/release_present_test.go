// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestReleaseNotesPresentUsage(t *testing.T) {
	base := []string{"--tenant", "x", "--project", "AEON", "--actor-principal-id", "00000000-0000-0000-0000-000000000001", "--release", "260929082208.0.0"}
	for _, args := range [][]string{
		nil,
		{"--tenant", "x"},
		base, // no text, file or clear
		append(append([]string{}, base...), "--theme", "t", "--headline", "h", "--clear"),
		append(append([]string{}, base...), "--theme", "t", "--file", "-"),
		append(append([]string{}, base...), "--clear", "extra"),
	} {
		err := releaseNotesPresent(context.Background(), args, strings.NewReader(""), &bytes.Buffer{})
		if err == nil || err.Error() != releasePresentUsage {
			t.Fatalf("args %q: %v", args, err)
		}
	}
	// Bad text fails before any database is opened.
	err := releaseNotesPresent(context.Background(), append(append([]string{}, base...), "--theme", "t", "--headline", "two\nlines"), strings.NewReader(""), &bytes.Buffer{})
	if !errors.Is(err, releasehistory.ErrPresentationInvalid) {
		t.Fatalf("invalid text: %v", err)
	}
}

func TestReleaseNotesPresentDryRunApplyAndAuthority(t *testing.T) {
	database := dbtest.Open(t)
	ctx := context.Background()
	tenantID, err := tenantbootstrap.Create(ctx, database.App, "notes-present", "Notes")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_ENV", "dev")
	t.Setenv("AEON_DATABASE_URL", database.AppURL)
	t.Setenv("AEON_DATABASE_PASSWORD_FILE", "")
	t.Setenv("AEON_MESSAGING_KEY_FILE", "")
	t.Setenv("AEON_LINK_KEY_FILE", "")
	var admin, agent, member string
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		for _, p := range []struct {
			id         *string
			kind, name string
		}{{&admin, "person", "Release admin"}, {&agent, "agent", "Release agent"}, {&member, "person", "Member"}} {
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, tenantID, p.kind, p.name).Scan(p.id); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,'PRJ-1','Aeon','{"project_key":"AEON"}'::jsonb FROM node_kinds WHERE slug='project'`, tenantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, database, tenantID, admin, "admin")
	dbtest.BindRole(t, database, tenantID, agent, "admin")
	dbtest.BindRole(t, database, tenantID, member, "member")
	const version = "260929082208.0.0"
	run := func(actor string, stdin string, extra ...string) (PresentReport, error) {
		t.Helper()
		args := append([]string{"--tenant", "notes-present", "--project", "AEON", "--actor-principal-id", actor, "--release", "v" + version}, extra...)
		var out bytes.Buffer
		if err := releaseNotesPresent(ctx, args, strings.NewReader(stdin), &out); err != nil {
			return PresentReport{}, err
		}
		var r PresentReport
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r, nil
	}
	stored := func() int {
		t.Helper()
		var n int
		if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM release_presentations`).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	text := []string{"--theme", "Named releases", "--headline", "Every release says what it is about.", "--intro", "A theme, a headline and a short intro.", "--theme-de", "Benannte Releases", "--headline-de", "Jedes Release sagt, worum es geht."}

	// A member lacks releases.deploy, even for a dry run.
	if _, err := run(member, "", text...); !errors.Is(err, releasehistory.ErrPresentationDenied) {
		t.Fatalf("member: %v", err)
	}
	dry, err := run(admin, "", text...)
	if err != nil || dry.Applied || !dry.Change.Changed || dry.Change.After == nil || dry.Change.After.ThemeEN != "Named releases" || dry.Change.Version != version {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if stored() != 0 {
		t.Fatal("dry run wrote")
	}
	applied, err := run(admin, "", append(text, "--apply")...)
	if err != nil || !applied.Applied || !applied.Change.Changed || applied.Change.After.Revision != 1 {
		t.Fatalf("apply: %+v %v", applied, err)
	}
	if again, err := run(admin, "", append(text, "--apply")...); err != nil || again.Change.Changed {
		t.Fatalf("identical apply: %+v %v", again, err)
	}
	// Offline there is no agent key: an agent is not an actor even with the admin role.
	file := `{"theme_en":"Named releases","headline_en":"Every release names itself.","intro_en":"","theme_de":"","headline_de":"","intro_de":""}`
	if _, err := run(agent, file, "--file", "-", "--apply"); !errors.Is(err, releasehistory.ErrPresentationDenied) {
		t.Fatalf("agent actor: %v", err)
	}
	// A JSON file on stdin edits and bumps the revision.
	byFile, err := run(admin, file, "--file", "-", "--apply")
	if err != nil || !byFile.Change.Changed || byFile.Change.After.Revision != 2 || byFile.Change.Before.HeadlineEN != "Every release says what it is about." {
		t.Fatalf("file apply: %+v %v", byFile, err)
	}
	if _, err := run(admin, `{"theme_en":"x","headline_en":"y","surprise":true}`, "--file", "-"); err == nil {
		t.Fatal("unknown file field accepted")
	}
	// Clear: dry run reports, apply removes.
	if c, err := run(admin, "", "--clear"); err != nil || !c.Change.Changed || stored() != 1 {
		t.Fatalf("clear dry run: %+v %v", c, err)
	}
	if c, err := run(admin, "", "--clear", "--apply"); err != nil || !c.Change.Changed || stored() != 0 {
		t.Fatalf("clear: %+v %v", c, err)
	}
	var sets, clears int
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE type=$1), count(*) FILTER (WHERE type=$2) FROM events`, releasehistory.PresentationSetEvent, releasehistory.PresentationClearedEvent).Scan(&sets, &clears)
	}); err != nil {
		t.Fatal(err)
	}
	if sets != 2 || clears != 1 {
		t.Fatalf("events: %d set, %d cleared", sets, clears)
	}
}

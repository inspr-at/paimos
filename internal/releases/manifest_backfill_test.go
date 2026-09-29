// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestManifestBackfillServedScopedImmutableAndIdempotent(t *testing.T) {
	f := ticketSetup(t)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	visible := f.existing("ticket", f.project, "Visible", "open")
	hidden := f.existing("ticket", f.project, "Hidden", "open")
	var visibleKey, hiddenKey string
	fields := `{"pill_en":"Clear benefits","pill_de":"Klare Vorteile","benefit_en":"You see what changed.","benefit_de":"Sie sehen die Änderungen.","private_field":"must not escape"}`
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=ANY($1::uuid[])`, []string{visible, hidden}, fields); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields || '{"hide_from_release_notes":true}'::jsonb WHERE id=$1`, hidden); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT key FROM nodes WHERE id=$1`, visible).Scan(&visibleKey); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT key FROM nodes WHERE id=$1`, hidden).Scan(&hiddenKey); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"project_key":"AEON"}'::jsonb WHERE id=$1`, f.project)
		return err
	})
	when := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	const version = "260115100000.0.0"
	const second = "260115110000.0.0"
	const tagged = "260115120000.0.0"
	history := releasehistory.History{Schema: releasehistory.Schema, Releases: []releasehistory.Release{
		gitTagWithoutNotes(t, version, visibleKey, hiddenKey, when),
		{Version: second, State: releasehistory.StatePublished, TaggedAt: &when, Tickets: []string{visibleKey}},
		{Version: tagged, State: releasehistory.StatePublished, TaggedAt: &when, Notes: &releasehistory.Notes{Source: "tag:file", Items: []releasehistory.NoteItem{{Key: visibleKey, BenefitEN: "Original tag wins"}}}},
	}}
	opts := NoteBackfillOptions{Project: "AEON", Release: version, History: history}
	// Use the actual HTTP handlers for both list and detail, not a formatter mock.
	module := releasehistory.NewWith(history, version).WithBackfills(f.db.App, "AEON")
	mux := http.NewServeMux()
	module.Mount(mux)
	request := func(p tenant.Principal, path string) string {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil).WithContext(tenant.WithPrincipal(t.Context(), p))
		mux.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("serve %s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if body := request(f.person, "/api/releases/"+version); !strings.Contains(body, "historical-tag-headline") || strings.Contains(body, "You see what changed") {
		t.Fatal(body)
	}
	dry, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, false, opts)
	if err != nil || len(dry.Planned) != 1 || dry.Inserted != 0 || dry.Planned[0].Tickets != 2 || dry.Planned[0].Notes != 1 || dry.Planned[0].Hidden != 1 {
		t.Fatalf("dry %+v %v", dry, err)
	}
	count := func() (int, int) {
		t.Helper()
		var snapshots, events int
		f.tx(func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM release_manifest_note_snapshots`).Scan(&snapshots); err != nil {
				return err
			}
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type=$1`, noteBackfillEvent).Scan(&events)
		})
		return snapshots, events
	}
	if s, e := count(); s != 0 || e != 0 {
		t.Fatalf("dry wrote %d %d", s, e)
	}
	applied, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, true, opts)
	if err != nil || applied.Inserted != 1 {
		t.Fatalf("apply %+v %v", applied, err)
	}
	var raw string
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT snapshot::text FROM release_manifest_note_snapshots WHERE version=$1`, version).Scan(&raw)
	})
	var snap releasehistory.NoteSnapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		t.Fatal(err)
	}
	if !snap.Backfilled || snap.ActorID != f.person.ID || snap.MembershipSource != releasehistory.ManifestMembershipSource || snap.ReleasedAt == nil || !snap.ReleasedAt.Equal(when) || time.Since(snap.CapturedAt) > time.Minute || len(snap.Tickets) != 2 || strings.Contains(raw, "must not escape") {
		t.Fatalf("snapshot %s", raw)
	}
	for _, path := range []string{"/api/releases", "/api/releases/" + version} {
		body := request(f.person, path)
		if !strings.Contains(body, "You see what changed.") || !strings.Contains(body, `"written_after_release":true`) || !strings.Contains(body, `"hidden":1`) {
			t.Fatal(body)
		}
		if strings.Contains(body, `"key":"`+hiddenKey+`"`) {
			t.Fatal("hidden note leaked")
		}
	}
	// No overlay bleeds into another tenant, even after the first tenant read it.
	if body := request(f.other, "/api/releases/"+version); strings.Contains(body, "You see what changed.") || strings.Contains(body, "written_after_release") {
		t.Fatal(body)
	}
	// Tag capture wins even when the store contains an older operator capture.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO release_manifest_note_snapshots(tenant_id,project_node_id,version,snapshot) SELECT tenant_id,project_node_id,$2,jsonb_set(snapshot,'{version}',to_jsonb($2::text)) FROM release_manifest_note_snapshots WHERE version=$1`, version, tagged)
		return err
	})
	if body := request(f.person, "/api/releases/"+tagged); !strings.Contains(body, "Original tag wins") || strings.Contains(body, "You see what changed") {
		t.Fatal(body)
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=jsonb_set(fields,'{benefit_en}','"Later ticket edit"') WHERE id=$1`, visible)
		return err
	})
	again, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, true, opts)
	if err != nil || again.Inserted != 0 || again.Unchanged != 1 {
		t.Fatalf("rerun %+v %v", again, err)
	}
	if body := request(f.person, "/api/releases/"+version); strings.Contains(body, "Later ticket edit") || !strings.Contains(body, "You see what changed") {
		t.Fatal(body)
	}
	if s, e := count(); s != 2 || e != 1 {
		t.Fatalf("rerun counts %d %d", s, e)
	}
	for _, sql := range []string{`UPDATE release_manifest_note_snapshots SET snapshot=snapshot`, `DELETE FROM release_manifest_note_snapshots`} {
		if err := f.txErr(func(tx pgx.Tx) error { _, err := tx.Exec(t.Context(), sql); return err }); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("immutability %v", err)
		}
	}
	// Selecting all missing picks the other version, leaves both existing captures.
	opts.Release = ""
	opts.AllMissing = true
	rest, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, false, opts)
	if err != nil || len(rest.Planned) != 1 || rest.Planned[0].Version != second || rest.Unchanged != 2 {
		t.Fatalf("all missing %+v %v", rest, err)
	}
	opts.Release = "260115130000.0.0"
	opts.AllMissing = false
	if _, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, true, opts); err == nil {
		t.Fatal("unknown release accepted")
	}
	// A different project cannot resolve these tickets or see the original rows.
	var projectB, member string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,'OTHER-1','Other','{"project_key":"OTHER"}'::jsonb FROM node_kinds WHERE slug='project' RETURNING nodes.id::text`, f.person.TenantID).Scan(&projectB); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other member') RETURNING id::text`, f.person.TenantID).Scan(&member); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE key='member'`, f.person.TenantID, member, projectB)
		return err
	})
	opts.Project = "OTHER"
	opts.Release = second
	scoped, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, true, opts)
	if err != nil || scoped.Inserted != 1 || scoped.Planned[0].Tickets != 0 {
		t.Fatalf("project scope %+v %v", scoped, err)
	}
	p := tenant.Principal{TenantID: f.person.TenantID, ID: member, Kind: tenant.Person}
	if body := request(p, "/api/releases/"+version); strings.Contains(body, "You see what changed.") || strings.Contains(body, "written_after_release") {
		t.Fatal(body)
	}
	err = db.InTenant(tenant.WithPrincipal(t.Context(), p), f.db.App, p.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM release_manifest_note_snapshots WHERE project_node_id=$1`, f.project).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("foreign project leaked %d rows", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBackfillRequiresActivePersonTenantAdminBeforePlanning(t *testing.T) {
	f := ticketSetup(t)
	opts := NoteBackfillOptions{Project: f.project}
	// A plain member, an admin agent, absent actor and another tenant's person
	// must all be denied, including dry-run. No implicit operator is ever made.
	dbtest.BindRole(t, f.db, f.agent.TenantID, f.agent.ID, "admin")
	dbtest.BindRole(t, f.db, f.other.TenantID, f.other.ID, "admin")
	for _, actor := range []string{"", f.person.ID, f.agent.ID, f.other.ID} {
		for _, apply := range []bool{false, true} {
			got, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, actor, apply, opts)
			if err == nil || len(got.Planned) != 0 || got.Inserted != 0 {
				t.Fatalf("actor=%s apply=%v %+v %v", actor, apply, got, err)
			}
		}
	}
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	// An authenticated agent cannot pass a person's UUID to escalate.
	if _, err := BackfillNoteSnapshots(tenant.WithPrincipal(t.Context(), f.agent), f.db.App, f.person.TenantID, f.person.ID, false, opts); err == nil {
		t.Fatal("agent impersonation")
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE principals SET status='deactivated' WHERE id=$1`, f.person.ID)
		return err
	})
	for _, apply := range []bool{false, true} {
		if _, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, apply, opts); err == nil {
			t.Fatal("inactive admin")
		}
	}
	if snapshots, events, operators := backfillCounts(t, f); snapshots != 0 || events != 0 || operators != 0 {
		t.Fatalf("denied operation wrote %d %d %d", snapshots, events, operators)
	}
}

// Build a real annotated tag with no snapshot. The production offline history
// builder must preserve its ticket refs for the database backfill to consume.
func gitTagWithoutNotes(t *testing.T, version, visible, hidden string, at time.Time) releasehistory.Release {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_DATE="+at.Format(time.RFC3339), "GIT_COMMITTER_DATE="+at.Format(time.RFC3339))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, out)
		}
	}
	run("init", "-q")
	raw, _ := json.Marshal(map[string]any{"product": "PAIMOS AEON", "version_scheme": "inspr-calendar-v2", "version": version, "ticket": visible})
	if err := os.WriteFile(filepath.Join(dir, "version.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "version.json")
	run("commit", "-qm", "feat: "+visible+" "+hidden+" OTHER-999")
	run("tag", "-a", "v"+version, "-m", "Historical tag headline "+visible)
	history, err := releasehistory.Build(t.Context(), releasehistory.Options{Repo: dir})
	if err != nil || len(history.Releases) != 1 {
		t.Fatalf("tag manifest %+v %v", history, err)
	}
	if history.Releases[0].Notes.Fallback != releasehistory.HistoricalFallback {
		t.Fatal("fixture unexpectedly has notes")
	}
	return history.Releases[0]
}

func TestNativeBackfillKeepsLegacyVersions(t *testing.T) {
	f := ticketSetup(t)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	disableNoteFreeze(t, f)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released',version='1.2.3',version_scheme='legacy',released_at='2025-01-15T10:00:00Z' WHERE release_node_id=$1`, f.release)
		return err
	})
	enableNoteFreeze(t, f)
	got, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, true, NoteBackfillOptions{Project: f.project, Release: "1.2.3"})
	if err != nil || got.Inserted != 1 || got.Planned[0].Version != "1.2.3" {
		t.Fatalf("legacy %+v %v", got, err)
	}
	if body := snapshotBody(t, f, f.release); !strings.Contains(body, `"version": "1.2.3"`) || !strings.Contains(body, `"version_scheme": "legacy"`) {
		t.Fatal(body)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package releases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestBackfillNoteSnapshotsPlansAppliesAndLeavesExistingRows(t *testing.T) {
	f := ticketSetup(t)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	if _, err := BackfillNoteSnapshots(t.Context(), nil, f.person.TenantID, f.person.ID, false, NoteBackfillOptions{Project: f.project}); err == nil {
		t.Fatal("nil pool")
	}
	visible := f.existing("ticket", f.project, "Frozen member", "open")
	membershipOK(t, f.addExisting([]string{visible}, 1, false))
	const frozenBenefit = "Benefit frozen at publication."
	frozenFields := `{"pill_en":"Frozen pill","pill_de":"Eingefrorene Pille","benefit_en":"` + frozenBenefit + `","benefit_de":"Nutzen bei der Veröffentlichung.","hide_from_release_notes":false}`
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, visible, frozenFields)
		return err
	})
	publishedAt := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f.tx(func(tx pgx.Tx) error {
		tag, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released', released_at=$2 WHERE release_node_id=$1`, f.release, publishedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("publish affected %d", tag.RowsAffected())
		}
		return nil
	})
	original := snapshotBody(t, f, f.release)
	if !strings.Contains(original, frozenBenefit) || strings.Contains(original, `"label"`) {
		t.Fatalf("publication snapshot: %s", original)
	}
	disableNoteFreeze(t, f)

	const backfillBenefit = "Benefit captured at backfill."
	gapFields := `{"pill_en":"Backfilled pill","pill_de":"Nachgetragene Pille","benefit_en":"` + backfillBenefit + `","benefit_de":"Nutzen beim Nachtragen.","hide_from_release_notes":false,"private_other_field":"not exported"}`
	gap := f.existing("release", f.project, "Gap release", "open")
	gapTicket := f.existing("ticket", f.project, "Gap member", "open")
	superseded := f.existing("release", f.project, "Older release", "open")
	planning := f.existing("release", f.project, "Still planning", "open")
	gapAt := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	superAt := time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)
	const gapVersion = "260115100000.0.0"
	f.tx(func(tx pgx.Tx) error {
		ctx := t.Context()
		if _, err := tx.Exec(ctx, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, gapTicket, gapFields); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at,version,version_scheme) VALUES($1,$2,$3,2,'released',$4,$5,'inspr-calendar-v2')`, f.person.TenantID, gap, f.project, gapAt, gapVersion); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,0,'manual')`, f.person.TenantID, gapTicket, f.project, gap); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at) VALUES($1,$2,$3,3,'superseded',$4)`, f.person.TenantID, superseded, f.project, superAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,4)`, f.person.TenantID, planning, f.project)
		return err
	})
	enableNoteFreeze(t, f)
	if snapshots, events, operators := backfillCounts(t, f); snapshots != 1 || events != 0 || operators != 0 {
		t.Fatalf("before backfill snapshots=%d events=%d operators=%d", snapshots, events, operators)
	}

	dry, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, false, NoteBackfillOptions{Project: f.project})
	if err != nil {
		t.Fatal(err)
	}
	if dry.Applied || dry.Inserted != 0 || dry.Unchanged != 1 || len(dry.Planned) != 2 || len(dry.Skipped) != 0 || dry.TenantID != f.person.TenantID {
		t.Fatalf("dry-run: %+v", dry)
	}
	if dry.Planned[0].ReleaseID != gap || dry.Planned[0].ProjectID != f.project || dry.Planned[0].Version != gapVersion || dry.Planned[0].Tickets != 1 || !dry.Planned[0].ReleasedAt.Equal(gapAt) {
		t.Fatalf("gap plan: %+v", dry.Planned[0])
	}
	if dry.Planned[1].ReleaseID != superseded || dry.Planned[1].Tickets != 0 || dry.Planned[1].Version != "" || !dry.Planned[1].ReleasedAt.Equal(superAt) {
		t.Fatalf("superseded plan: %+v", dry.Planned[1])
	}
	for _, item := range dry.Planned {
		if item.ReleaseID == planning || item.ReleaseID == f.release {
			t.Fatalf("planned a release that already has notes or is still planning: %+v", item)
		}
	}
	if snapshots, events, operators := backfillCounts(t, f); snapshots != 1 || events != 0 || operators != 0 || snapshotBody(t, f, f.release) != original {
		t.Fatalf("dry-run wrote snapshots=%d events=%d operators=%d", snapshots, events, operators)
	}

	scoped, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, false, NoteBackfillOptions{Project: f.project, Release: gapVersion})
	if err != nil || len(scoped.Planned) != 1 || scoped.Planned[0].ReleaseID != gap || scoped.Unchanged != 0 {
		t.Fatalf("native selector: %+v %v", scoped, err)
	}
	applied, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, true, NoteBackfillOptions{Project: f.project})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.Inserted != 2 || applied.Unchanged != 1 || len(applied.Planned) != 2 || applied.Planned[0].ReleaseID != gap || applied.Planned[1].ReleaseID != superseded {
		t.Fatalf("apply: %+v", applied)
	}
	if snapshots, events, operators := backfillCounts(t, f); snapshots != 3 || events != 2 || operators != 0 {
		t.Fatalf("after apply snapshots=%d events=%d operators=%d", snapshots, events, operators)
	}
	if snapshotBody(t, f, f.release) != original {
		t.Fatal("publication snapshot changed")
	}
	gapBody := snapshotBody(t, f, gap)
	if strings.Contains(gapBody, "not exported") || !strings.Contains(gapBody, backfillBenefit) {
		t.Fatalf("gap snapshot: %s", gapBody)
	}
	label, frozen, released, captured := snapshotTimes(t, f, gap)
	if label != releasehistory.BackfillLabel || !frozen || !released.Equal(gapAt) || !captured.After(gapAt) || time.Since(captured) > 2*time.Minute || time.Until(captured) > 2*time.Minute {
		t.Fatalf("gap label=%s frozen=%v released=%s captured=%s", label, frozen, released, captured)
	}
	superBody := snapshotBody(t, f, superseded)
	notes, err := releasehistory.NotesFromSnapshot([]byte(superBody), "260928120000.0.0", "test")
	if err != nil || !notes.WrittenAfterRelease || len(notes.Items) != 0 {
		t.Fatalf("superseded notes: %+v %v body %s", notes, err, superBody)
	}
	gapNotes, err := releasehistory.NotesFromSnapshot([]byte(gapBody), gapVersion, "test")
	if err != nil || !gapNotes.WrittenAfterRelease || len(gapNotes.Items) != 1 || gapNotes.Items[0].BenefitEN != backfillBenefit {
		t.Fatalf("gap notes: %+v %v", gapNotes, err)
	}
	// The same native backfill reaches the product release history too.
	historyMux := http.NewServeMux()
	releasehistory.NewWith(releasehistory.History{Releases: []releasehistory.Release{{Version: gapVersion, Notes: releasehistory.MissingNotes()}}}, gapVersion).WithBackfills(f.db.App, f.project).Mount(historyMux)
	historyResponse := httptest.NewRecorder()
	historyMux.ServeHTTP(historyResponse, httptest.NewRequest("GET", "/api/releases/"+gapVersion, nil).WithContext(tenant.WithPrincipal(t.Context(), f.person)))
	if historyResponse.Code != 200 || !strings.Contains(historyResponse.Body.String(), backfillBenefit) || !strings.Contains(historyResponse.Body.String(), `"written_after_release":true`) {
		t.Fatalf("native history: %d %s", historyResponse.Code, historyResponse.Body.String())
	}
	var eventBody, actor string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT coalesce(string_agg(after::text, ''), '') FROM events WHERE type=$1`, noteBackfillEvent).Scan(&eventBody); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT coalesce(string_agg(DISTINCT p.name, ','), '') FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id WHERE e.type=$1`, noteBackfillEvent).Scan(&actor)
	})
	if strings.Contains(eventBody, backfillBenefit) || strings.Contains(eventBody, "Backfilled pill") || !strings.Contains(eventBody, gap) || !strings.Contains(eventBody, superseded) || actor != "Person" {
		t.Fatalf("events actor=%s body=%s", actor, eventBody)
	}

	edited := strings.Replace(gapFields, backfillBenefit, "Benefit edited after backfill.", 1)
	editedFrozen := strings.Replace(frozenFields, frozenBenefit, "Benefit edited after publication.", 1)
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, gapTicket, edited); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, visible, editedFrozen)
		return err
	})
	if snapshotBody(t, f, f.release) != original || snapshotBody(t, f, gap) != gapBody {
		t.Fatal("later ticket edits rewrote a snapshot")
	}
	got := f.request(f.person, http.MethodGet, "/api/projects/"+f.project+"/releases/"+gap+"/note-snapshot", "")
	if got.Code != 200 {
		t.Fatalf("api: %d %s", got.Code, got.Body.String())
	}
	var snap releasehistory.NoteSnapshot
	if err := json.Unmarshal(got.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Label != releasehistory.BackfillLabel || !snap.Frozen || snap.ReleasedAt == nil || !snap.ReleasedAt.Equal(gapAt) || !strings.Contains(got.Body.String(), backfillBenefit) || strings.Contains(got.Body.String(), "Benefit edited after backfill.") {
		t.Fatalf("api snapshot: %s", got.Body.String())
	}

	againDry, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, false, NoteBackfillOptions{Project: f.project})
	if err != nil {
		t.Fatal(err)
	}
	again, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, true, NoteBackfillOptions{Project: f.project})
	if err != nil {
		t.Fatal(err)
	}
	if againDry.Applied || len(againDry.Planned) != 0 || againDry.Unchanged != 3 || again.Inserted != 0 || len(again.Planned) != 0 || again.Unchanged != 3 {
		t.Fatalf("re-run dry=%+v apply=%+v", againDry, again)
	}
	if snapshots, events, operators := backfillCounts(t, f); snapshots != 3 || events != 2 || operators != 0 || snapshotBody(t, f, f.release) != original || snapshotBody(t, f, gap) != gapBody {
		t.Fatalf("re-run wrote snapshots=%d events=%d operators=%d", snapshots, events, operators)
	}
	err = f.txErr(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_release_note_snapshots SET snapshot=snapshot WHERE release_node_id=$1`, gap)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("update: %v", err)
	}
	err = f.txErr(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM journey_release_note_snapshots WHERE release_node_id=$1`, gap)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("delete: %v", err)
	}
	other, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.other.TenantID, f.person.ID, true, NoteBackfillOptions{Project: f.project})
	if err == nil || other.Inserted != 0 || len(other.Planned) != 0 || other.Unchanged != 0 {
		t.Fatalf("other tenant: %+v %v", other, err)
	}
	var otherOps int
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.other.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM principals WHERE kind='agent' AND name='Access operator'`).Scan(&otherOps)
	}); err != nil || otherOps != 0 || snapshotBody(t, f, gap) != gapBody {
		t.Fatalf("other tenant operators=%d err=%v", otherOps, err)
	}
}

func TestBackfillNoteSnapshotsStayInsideTheProject(t *testing.T) {
	f := ticketSetup(t)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	memberA := f.existing("ticket", f.project, "Visible member", "open")
	membershipOK(t, f.addExisting([]string{memberA}, 1, false))
	const visibleBenefit = "Project A visible benefit."
	const secretBenefit = "Project B secret benefit."
	visibleFields := `{"pill_en":"Project A pill","pill_de":"Projekt A Pille","benefit_en":"` + visibleBenefit + `","benefit_de":"Projekt A sichtbarer Nutzen.","hide_from_release_notes":false}`
	var projectB, releaseB, memberB, principal string
	f.tx(func(tx pgx.Tx) error {
		ctx := t.Context()
		if _, err := tx.Exec(ctx, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, memberA, visibleFields); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Other project' FROM node_kinds WHERE slug='project' RETURNING nodes.id::text`, f.person.TenantID).Scan(&projectB); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2)`, f.person.TenantID, projectB); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),$2,'Other release' FROM node_kinds WHERE slug='release' RETURNING nodes.id::text`, f.person.TenantID, projectB).Scan(&releaseB); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,1)`, f.person.TenantID, releaseB, projectB); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title,state) SELECT $1,id,aeon_next_node_key($1,short_prefix),$2,'Secret ticket','open' FROM node_kinds WHERE slug='ticket' RETURNING nodes.id::text`, f.person.TenantID, projectB).Scan(&memberB); err != nil {
			return err
		}
		secret := `{"pill_en":"Project B pill","pill_de":"Projekt B Pille","benefit_en":"` + secretBenefit + `","benefit_de":"Projekt B geheimer Nutzen.","hide_from_release_notes":false}`
		if _, err := tx.Exec(ctx, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, memberB, secret); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,0,'manual')`, f.person.TenantID, memberB, projectB, releaseB); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Project A member') RETURNING id::text`, f.person.TenantID).Scan(&principal)
	})
	disableNoteFreeze(t, f)
	f.tx(func(tx pgx.Tx) error {
		tag, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released', released_at=$2 WHERE release_node_id=ANY($1::uuid[])`, []string{f.release, releaseB}, time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 2 {
			return fmt.Errorf("publish affected %d", tag.RowsAffected())
		}
		return nil
	})
	enableNoteFreeze(t, f)
	if snapshots, _, _ := backfillCounts(t, f); snapshots != 0 {
		t.Fatalf("trigger off still stored %d", snapshots)
	}
	applied, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, true, NoteBackfillOptions{Project: f.project})
	if err != nil || applied.Inserted != 1 {
		t.Fatalf("apply project A: %+v %v", applied, err)
	}
	appliedB, err := BackfillNoteSnapshots(t.Context(), f.db.App, f.person.TenantID, f.person.ID, true, NoteBackfillOptions{Project: projectB})
	if err != nil || appliedB.Inserted != 1 {
		t.Fatalf("apply project B: %+v %v", appliedB, err)
	}
	f.tx(func(tx pgx.Tx) error {
		tag, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, f.person.TenantID, principal, f.project)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("project binding affected %d", tag.RowsAffected())
		}
		return nil
	})
	member := tenant.Principal{ID: principal, TenantID: f.person.TenantID, Kind: tenant.Person}
	err = db.InTenant(tenant.WithPrincipal(t.Context(), member), f.db.App, member.TenantID, func(tx pgx.Tx) error {
		var rows, hidden, events int
		var body, eventBody, secret string
		if err := tx.QueryRow(t.Context(), `SELECT count(*), coalesce(string_agg(snapshot::text, ''), '') FROM journey_release_note_snapshots`).Scan(&rows, &body); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_release_note_snapshots WHERE project_node_id=$1 OR release_node_id=$2`, projectB, releaseB).Scan(&hidden); err != nil {
			return err
		}
		err := tx.QueryRow(t.Context(), `SELECT snapshot::text FROM journey_release_note_snapshots WHERE release_node_id=$1`, releaseB).Scan(&secret)
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("direct read of project B: %v %s", err, secret)
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*), coalesce(string_agg(after::text, ''), '') FROM events WHERE type=$1`, noteBackfillEvent).Scan(&events, &eventBody); err != nil {
			return err
		}
		if rows != 1 || hidden != 0 || events != 1 || !strings.Contains(body, visibleBenefit) || strings.Contains(body, secretBenefit) || strings.Contains(body, memberB) || !strings.Contains(eventBody, f.release) || strings.Contains(eventBody, releaseB) {
			return fmt.Errorf("rows=%d hidden=%d events=%d body=%s event=%s", rows, hidden, events, body, eventBody)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	own := f.request(member, http.MethodGet, "/api/projects/"+f.project+"/releases/"+f.release+"/note-snapshot", "")
	if own.Code != 200 || !strings.Contains(own.Body.String(), visibleBenefit) || strings.Contains(own.Body.String(), secretBenefit) {
		t.Fatalf("member api: %d %s", own.Code, own.Body.String())
	}
	var ownSnap releasehistory.NoteSnapshot
	if err := json.Unmarshal(own.Body.Bytes(), &ownSnap); err != nil || ownSnap.Label != releasehistory.BackfillLabel || !ownSnap.Frozen || ownSnap.ReleasedAt == nil {
		t.Fatalf("member snapshot: %v %s", err, own.Body.String())
	}
	foreign := f.request(member, http.MethodGet, "/api/projects/"+projectB+"/releases/"+releaseB+"/note-snapshot", "")
	if foreign.Code != 404 || strings.Contains(foreign.Body.String(), secretBenefit) {
		t.Fatalf("foreign api: %d %s", foreign.Code, foreign.Body.String())
	}
}

func (f *ticketFixture) txErr(fn func(pgx.Tx) error) error {
	return db.InTenant(dbtest.Seed(f.t.Context()), f.db.App, f.person.TenantID, fn)
}

func disableNoteFreeze(t *testing.T, f *ticketFixture) {
	t.Helper()
	if _, err := f.db.App.Exec(t.Context(), `ALTER TABLE journey_releases DISABLE TRIGGER journey_releases_freeze_note_snapshot`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.db.App.Exec(context.Background(), `ALTER TABLE journey_releases ENABLE TRIGGER journey_releases_freeze_note_snapshot`); err != nil {
			t.Errorf("re-enable snapshot trigger: %v", err)
		}
	})
}

func enableNoteFreeze(t *testing.T, f *ticketFixture) {
	t.Helper()
	if _, err := f.db.App.Exec(t.Context(), `ALTER TABLE journey_releases ENABLE TRIGGER journey_releases_freeze_note_snapshot`); err != nil {
		t.Fatal(err)
	}
}

func snapshotBody(t *testing.T, f *ticketFixture, release string) string {
	t.Helper()
	var body string
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT snapshot::text FROM journey_release_note_snapshots WHERE release_node_id=$1`, release).Scan(&body)
	})
	return body
}

func snapshotTimes(t *testing.T, f *ticketFixture, release string) (label string, frozen bool, released, captured time.Time) {
	t.Helper()
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT snapshot->>'label', (snapshot->>'frozen')::boolean, (snapshot->>'released_at')::timestamptz, (snapshot->>'captured_at')::timestamptz FROM journey_release_note_snapshots WHERE release_node_id=$1`, release).Scan(&label, &frozen, &released, &captured)
	})
	return label, frozen, released, captured
}

func backfillCounts(t *testing.T, f *ticketFixture) (snapshots, events, operators int) {
	t.Helper()
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_release_note_snapshots`).Scan(&snapshots); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type=$1`, noteBackfillEvent).Scan(&events); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM principals WHERE kind='agent' AND name='Access operator'`).Scan(&operators)
	})
	return snapshots, events, operators
}

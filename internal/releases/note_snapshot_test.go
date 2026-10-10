// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestNoteSnapshotUsesExactMembershipAndFields(t *testing.T) {
	f := ticketSetup(t)
	// Reuse the public membership API with a fixture-created ticket.
	id := f.existing("ticket", f.project, "Actual member", "open")
	membershipOK(t, f.addExisting([]string{id}, 1, false))
	fields := `{"pill_en":"Clear release notes","pill_de":"Verständliche Release Notes","benefit_en":"Know what changed.","benefit_de":"Änderungen werden verständlich.","hide_from_release_notes":false,"private_other_field":"not exported"}`
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, id, fields)
		return err
	})
	path := "/api/projects/" + f.project + "/releases/" + f.release + "/note-snapshot"
	w := f.request(f.person, http.MethodGet, path, "")
	if w.Code != 200 {
		t.Fatalf("snapshot: %d %s", w.Code, w.Body.String())
	}
	var s releasehistory.NoteSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Frozen || s.MembershipSource != releasehistory.MembershipSource || s.FieldSource != releasehistory.FieldSource || s.Revision != 2 || len(s.Tickets) != 1 || s.Tickets[0].ID != id || !strings.Contains(string(s.Tickets[0].Fields), "Änderungen werden verständlich.") || strings.Contains(w.Body.String(), "private_other_field") {
		t.Fatalf("snapshot: %s", w.Body.String())
	}
	if s.Tickets[0].Group != releasehistory.GroupFeatures {
		t.Fatal("classification was not captured")
	}
	// Version-less export records that the tagged path supplies the version binding.
	if notes, err := releasehistory.NotesFromSnapshot(w.Body.Bytes(), "260928120000.0.0", "test"); err != nil || len(notes.Gaps) != 1 || !strings.Contains(notes.Gaps[0], "no assigned version") {
		t.Fatalf("missing version binding gap: %+v %v", notes, err)
	}
	for _, p := range []tenant.Principal{f.other, {}} {
		w = f.request(p, "GET", path, "")
		if w.Code != 404 && w.Code != 401 {
			t.Fatalf("foreign: %d %s", w.Code, w.Body.String())
		}
	}
	w = f.request(f.person, "GET", strings.Replace(path, f.project, f.feature, 1), "")
	if w.Code != 404 {
		t.Fatalf("wrong project: %d", w.Code)
	}
}

func TestNoteSnapshotConcurrentMembershipAndFields(t *testing.T) {
	f := ticketSetup(t)
	ids := []string{f.existing("ticket", f.project, "First", "open"), f.existing("ticket", f.project, "Second", "open")}
	membershipOK(t, f.addExisting(ids, 1, false))
	start := make(chan struct{})
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := range 12 {
			err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
				fields := fmt.Sprintf(`{"pill_en":"Round %d"}`, i)
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=ANY($1::uuid[])`, ids, fields); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET revision=revision+1 WHERE release_node_id=$1`, f.release)
				return err
			})
			if err != nil {
				errs <- err
				return
			}
		}
	}()
	close(start)
	for range 12 {
		w := f.request(f.person, "GET", "/api/projects/"+f.project+"/releases/"+f.release+"/note-snapshot", "")
		if w.Code != 200 {
			t.Errorf("snapshot: %d", w.Code)
			break
		}
		var s releasehistory.NoteSnapshot
		if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
			t.Error(err)
			break
		}
		if len(s.Tickets) != 2 || string(s.Tickets[0].Fields) != string(s.Tickets[1].Fields) {
			t.Errorf("mixed field snapshots: %+v", s)
			break
		}
		if s.Revision > 2 && !strings.Contains(string(s.Tickets[0].Fields), fmt.Sprintf("Round %d", s.Revision-3)) {
			t.Errorf("revision differs from field snapshot: %+v", s)
			break
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPublishedNoteSnapshotIgnoresLaterTicketEdits(t *testing.T) {
	for _, flag := range []string{"hide_from_release_notes", "no_release_needed"} {
		t.Run(flag, func(t *testing.T) {
			f := ticketSetup(t)
			visible := f.existing("ticket", f.project, "Shown", "open")
			hidden := f.existing("ticket", f.project, "Quiet", "open")
			membershipOK(t, f.addExisting([]string{visible, hidden}, 1, false))
			visibleFields := `{"pill_en":"Clear release notes","pill_de":"Verständliche Release Notes","benefit_en":"Know what changed.","benefit_de":"Änderungen werden verständlich.","hide_from_release_notes":false}`
			hiddenFields := `{"pill_en":"Keep this private","pill_de":"Nicht öffentlich zeigen","benefit_en":"Stay out of the notes.","benefit_de":"Bleibt aus den Release Notes.","` + flag + `":true}`
			f.tx(func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, visible, visibleFields); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, hidden, hiddenFields)
				return err
			})
			path := "/api/projects/" + f.project + "/releases/" + f.release + "/note-snapshot"
			before := f.request(f.person, http.MethodGet, path, "")
			if before.Code != 200 || !strings.Contains(before.Body.String(), "Know what changed.") || strings.Contains(before.Body.String(), `"frozen": true`) {
				t.Fatalf("live preview: %d %s", before.Code, before.Body.String())
			}
			var stored int
			f.tx(func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_release_note_snapshots WHERE release_node_id=$1`, f.release).Scan(&stored)
			})
			if stored != 0 {
				t.Fatal("planning release froze notes")
			}
			edited := strings.Replace(visibleFields, "Know what changed.", "Edited before publication.", 1)
			edited = strings.Replace(edited, `{`, `{"tags":[{"name":"bug"}],`, 1)
			f.tx(func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, visible, edited)
				return err
			})
			f.tx(func(tx pgx.Tx) error {
				tag, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released', released_at=clock_timestamp() WHERE release_node_id=$1`, f.release)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return fmt.Errorf("publish affected %d", tag.RowsAffected())
				}
				return nil
			})
			afterEdit := strings.Replace(edited, "Edited before publication.", "Edited after publication.", 1)
			afterEdit = strings.Replace(afterEdit, `"tags":[{"name":"bug"}],`, "", 1)
			f.tx(func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, visible, afterEdit)
				return err
			})
			frozen := f.request(f.person, http.MethodGet, path, "")
			if frozen.Code != 200 || !strings.Contains(frozen.Body.String(), "Edited before publication.") || strings.Contains(frozen.Body.String(), "Edited after publication.") {
				t.Fatalf("frozen snapshot: %d %s", frozen.Code, frozen.Body.String())
			}
			var snap releasehistory.NoteSnapshot
			if err := json.Unmarshal(frozen.Body.Bytes(), &snap); err != nil {
				t.Fatal(err)
			}
			if !snap.Frozen {
				t.Fatal("published snapshot is not frozen")
			}
			notes, err := releasehistory.NotesFromSnapshot(frozen.Body.Bytes(), "260928120000.0.0", "test")
			if err != nil || notes.Hidden != 1 || len(notes.Items) != 1 || notes.Items[0].ID != visible || notes.Items[0].Group != releasehistory.GroupFixes || notes.Items[0].BenefitEN != "Edited before publication." || notes.Fallback != "" {
				t.Fatalf("notes from benefits: %+v %v body %s", notes, err, frozen.Body.String())
			}
			encoded := string(mustNotes(t, notes))
			if strings.Contains(encoded, hidden) || strings.Contains(encoded, "Stay out of the notes.") {
				t.Fatal("hidden ticket copied into notes")
			}
			err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE journey_release_note_snapshots SET snapshot=snapshot WHERE release_node_id=$1`, f.release)
				return err
			})
			if err == nil || !strings.Contains(err.Error(), "immutable") {
				t.Fatalf("update: %v", err)
			}
			err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `DELETE FROM journey_release_note_snapshots WHERE release_node_id=$1`, f.release)
				return err
			})
			if err == nil || !strings.Contains(err.Error(), "immutable") {
				t.Fatalf("delete: %v", err)
			}

			// A release inserted already published freezes membership committed with it.
			born := f.existing("release", f.project, "Born published", "open")
			member := f.existing("ticket", f.project, "Member at birth", "open")
			bornFields := `{"pill_en":"Born with notes","pill_de":"Mit Notizen geboren","benefit_en":"The first text stays.","benefit_de":"Der erste Text bleibt.","hide_from_release_notes":false}`
			f.tx(func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, member, bornFields); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at) VALUES($1,$2,$3,2,'released',clock_timestamp())`, f.person.TenantID, born, f.project); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,0,'manual')`, f.person.TenantID, member, f.project, born)
				return err
			})
			f.tx(func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, member, strings.Replace(bornFields, "The first text stays.", "Rewritten later.", 1))
				return err
			})
			got := f.request(f.person, http.MethodGet, "/api/projects/"+f.project+"/releases/"+born+"/note-snapshot", "")
			if got.Code != 200 || !strings.Contains(got.Body.String(), "The first text stays.") || strings.Contains(got.Body.String(), "Rewritten later.") {
				t.Fatalf("created published: %d %s", got.Code, got.Body.String())
			}

		})
	}
}
func TestPublishedBeforeSnapshotsDoesNotFollowTicketEdits(t *testing.T) {
	f := ticketSetup(t)
	visible := f.existing("ticket", f.project, "Historical member", "open")
	membershipOK(t, f.addExisting([]string{visible}, 1, false))
	original := `{"pill_en":"Original pill","pill_de":"Ursprüngliche Pille","benefit_en":"Benefit written before publication.","benefit_de":"Nutzen vor der Veröffentlichung.","hide_from_release_notes":false}`
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, visible, original)
		return err
	})
	if _, err := f.db.App.Exec(t.Context(), `ALTER TABLE journey_releases DISABLE TRIGGER journey_releases_freeze_note_snapshot`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.db.App.Exec(context.Background(), `ALTER TABLE journey_releases ENABLE TRIGGER journey_releases_freeze_note_snapshot`); err != nil {
			t.Errorf("re-enable snapshot trigger: %v", err)
		}
	})
	// Publication before the snapshot trigger existed leaves no stored row.
	// The count is after commit: the freeze trigger is deferred.
	f.tx(func(tx pgx.Tx) error {
		tag, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released', released_at=clock_timestamp() WHERE release_node_id=$1`, f.release)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("publish affected %d", tag.RowsAffected())
		}
		return nil
	})
	var stored int
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_release_note_snapshots WHERE release_node_id=$1`, f.release).Scan(&stored)
	})
	if stored != 0 {
		t.Fatalf("pre-migration release stored %d snapshots", stored)
	}
	if _, err := f.db.App.Exec(t.Context(), `ALTER TABLE journey_releases ENABLE TRIGGER journey_releases_freeze_note_snapshot`); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + f.project + "/releases/" + f.release + "/note-snapshot"
	before := f.request(f.person, http.MethodGet, path, "")
	edited := strings.Replace(original, "Benefit written before publication.", "Benefit edited after publication.", 1)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, visible, edited)
		return err
	})
	after := f.request(f.person, http.MethodGet, path, "")
	if before.Code != 200 || after.Code != 200 || before.Body.String() != after.Body.String() {
		t.Fatalf("notes changed: before %d %s after %d %s", before.Code, before.Body.String(), after.Code, after.Body.String())
	}
	if strings.Contains(after.Body.String(), "Benefit written before publication.") || strings.Contains(after.Body.String(), "Benefit edited after publication.") || strings.Contains(after.Body.String(), visible) {
		t.Fatalf("historical notes rebuilt from tickets: %s", after.Body.String())
	}
	var notes releasehistory.Notes
	if err := json.Unmarshal(after.Body.Bytes(), &notes); err != nil {
		t.Fatal(err)
	}
	if notes.Fallback != releasehistory.HistoricalFallback || notes.Source != "unavailable" || len(notes.Items) != 0 || len(notes.Gaps) == 0 {
		t.Fatalf("historical fallback: %+v", notes)
	}
}

func TestProjectMemberCannotReadAnotherProjectsNoteSnapshot(t *testing.T) {
	f := ticketSetup(t)
	memberA := f.existing("ticket", f.project, "Visible member", "open")
	membershipOK(t, f.addExisting([]string{memberA}, 1, false))
	visibleBenefit := `{"pill_en":"Project A pill","pill_de":"Projekt A Pille","benefit_en":"Project A visible benefit.","benefit_de":"Projekt A sichtbarer Nutzen.","hide_from_release_notes":false}`
	var projectB, releaseB, memberB, principal string
	f.tx(func(tx pgx.Tx) error {
		ctx := t.Context()
		if _, err := tx.Exec(ctx, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, memberA, visibleBenefit); err != nil {
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
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title,state) SELECT $1,id,aeon_next_node_key($1,short_prefix),$2,'Secret ticket','open' FROM node_kinds WHERE slug='work' RETURNING nodes.id::text`, f.person.TenantID, projectB).Scan(&memberB); err != nil {
			return err
		}
		secret := `{"pill_en":"Project B pill","pill_de":"Projekt B Pille","benefit_en":"Project B secret benefit.","benefit_de":"Projekt B geheimer Nutzen.","hide_from_release_notes":false}`
		if _, err := tx.Exec(ctx, `UPDATE nodes SET fields=$2::jsonb WHERE id=$1`, memberB, secret); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,0,'manual')`, f.person.TenantID, memberB, projectB, releaseB); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE journey_releases SET state='released', released_at=clock_timestamp() WHERE release_node_id=ANY($1::uuid[])`, []string{f.release, releaseB})
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 2 {
			return fmt.Errorf("publish affected %d", tag.RowsAffected())
		}
		return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Project A member') RETURNING id::text`, f.person.TenantID).Scan(&principal)
	})
	f.tx(func(tx pgx.Tx) error {
		tag, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, f.person.TenantID, principal, f.project)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("project binding affected %d", tag.RowsAffected())
		}
		var stored int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_release_note_snapshots`).Scan(&stored); err != nil {
			return err
		}
		if stored != 2 {
			return fmt.Errorf("stored snapshots %d", stored)
		}
		return nil
	})
	member := tenant.Principal{ID: principal, TenantID: f.person.TenantID, Kind: tenant.Person}
	err := db.InTenant(tenant.WithPrincipal(t.Context(), member), f.db.App, member.TenantID, func(tx pgx.Tx) error {
		var visibility, body string
		var rows, hidden int
		if err := tx.QueryRow(t.Context(), `SELECT coalesce(current_setting('aeon.visible_projects', true), '')`).Scan(&visibility); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*), coalesce(string_agg(snapshot::text, ''), '') FROM journey_release_note_snapshots`).Scan(&rows, &body); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_release_note_snapshots WHERE project_node_id=$1 OR release_node_id=$2`, projectB, releaseB).Scan(&hidden); err != nil {
			return err
		}
		var secret string
		err := tx.QueryRow(t.Context(), `SELECT snapshot::text FROM journey_release_note_snapshots WHERE release_node_id=$1`, releaseB).Scan(&secret)
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("direct read of project B: %v %s", err, secret)
		}
		if rows != 1 || hidden != 0 || !strings.Contains(body, "Project A visible benefit.") || strings.Contains(body, "Project B secret benefit.") || strings.Contains(body, memberB) {
			return fmt.Errorf("direct read visibility=%s rows=%d hidden=%d body=%s", visibility, rows, hidden, body)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mustNotes(t *testing.T, notes *releasehistory.Notes) []byte {
	t.Helper()
	raw, err := json.Marshal(notes)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

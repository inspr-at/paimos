// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"encoding/json"
	"fmt"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"net/http"
	"strings"
	"sync"
	"testing"

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
	if s.MembershipSource != releasehistory.MembershipSource || s.FieldSource != releasehistory.FieldSource || s.Revision != 2 || len(s.Tickets) != 1 || s.Tickets[0].ID != id || !strings.Contains(string(s.Tickets[0].Fields), "Änderungen werden verständlich.") || strings.Contains(w.Body.String(), "private_other_field") {
		t.Fatalf("snapshot: %s", w.Body.String())
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

// SPDX-License-Identifier: AGPL-3.0-only

package releases

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func (f *ticketFixture) membershipReadPath(project string, ids ...string) string {
	return "/api/projects/" + project + "/release-memberships?" + url.Values{"ticket_node_id": ids}.Encode()
}

func (f *ticketFixture) readMembership(ids ...string) []nativeMembership {
	f.t.Helper()
	w := f.request(f.person, http.MethodGet, f.membershipReadPath(f.project, ids...), "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		f.t.Fatalf("membership read %d %s", w.Code, w.Body.String())
	}
	var out nativeMemberships
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out.Tickets
}

func TestNativeWorkMembershipReadProjection(t *testing.T) {
	f := ticketSetup(t)
	a := f.existing("work", f.project, "Assigned work", "open")
	b := f.existing("work", f.project, "Unassigned work", "open")
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"release":{"label":"Imported release"},"keep":true}' WHERE id=ANY($1::uuid[])`, []string{a, b}); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,feature_node_id,release_node_id,walker_position,source)
			VALUES($1,$2,$3,$4,$5,0,'manual')`, f.person.TenantID, a, f.project, f.feature, f.release)
		return err
	})
	before := f.counts()
	rows := f.readMembership(b, a)
	if len(rows) != 2 || rows[0].TicketID != b || rows[0].ReleaseID != nil || rows[0].ReleaseTitle != nil || rows[0].ReleaseState != nil ||
		rows[1].TicketID != a || rows[1].ReleaseID == nil || *rows[1].ReleaseID != f.release || rows[1].ReleaseTitle == nil || *rows[1].ReleaseTitle != "release" || rows[1].ReleaseState == nil || *rows[1].ReleaseState != "planning" {
		t.Fatalf("work membership projection or request order lost: %+v", rows)
	}
	if f.counts() != before {
		t.Fatal("membership read mutated journey or audit history")
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE id=ANY($1::uuid[]) AND fields='{"release":{"label":"Imported release"},"keep":true}'::jsonb`, []string{a, b}).Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			t.Fatal("membership projection changed imported fields")
		}
		return nil
	})
}

func TestNativeMembershipReadsAssignmentRemovalAndUndo(t *testing.T) {
	f := ticketSetup(t)
	a := f.existing("ticket", f.project, "First", "open")
	b := f.existing("ticket", f.project, "Second", "open")
	task := f.existing("task", f.project, "Task", "open")
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"release":{"label":"Imported release"},"keep":true}' WHERE id=ANY($1::uuid[])`, []string{a, b})
		return err
	})
	assertRead := func(ids []string, assigned map[string]bool) {
		t.Helper()
		rows := f.readMembership(ids...)
		if len(rows) != len(ids) {
			t.Fatalf("rows %+v, requested %v", rows, ids)
		}
		for i, row := range rows {
			if row.TicketID != ids[i] {
				t.Fatalf("request order lost: %+v", rows)
			}
			if assigned[row.TicketID] {
				if row.ReleaseID == nil || *row.ReleaseID != f.release || row.ReleaseTitle == nil || *row.ReleaseTitle != "release" || row.ReleaseState == nil || *row.ReleaseState != "planning" {
					t.Fatalf("native assignment missing: %+v", row)
				}
			} else if row.ReleaseID != nil || row.ReleaseTitle != nil || row.ReleaseState != nil {
				t.Fatalf("expected native backlog: %+v", row)
			}
		}
	}
	assertRead([]string{b, a, task}, nil)
	added := membershipOK(t, f.addExisting([]string{a, b}, 1, false))
	assertRead([]string{b, a}, map[string]bool{a: true, b: true})
	assertRead([]string{a}, map[string]bool{a: true}) // Separate detail reload.
	plan := fmt.Sprintf(`{"expected_revision":%d,"ordered_ticket_ids":[%q,%q],"included_ticket_ids":[%q]}`, added.Walker.Revision, b, a, b)
	w := f.request(f.person, http.MethodPut, "/api/projects/"+f.project+"/releases/"+f.release+"/plan", plan)
	if w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	assertRead([]string{a, b}, map[string]bool{b: true})
	var removedEvent int64
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE node_id=$1 AND type='journey.release_membership_changed' ORDER BY id DESC LIMIT 1`, f.release).Scan(&removedEvent)
	})
	events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", removedEvent), ""); w.Code != 201 {
		t.Fatalf("undo %d %s", w.Code, w.Body.String())
	}
	assertRead([]string{a, b}, map[string]bool{a: true, b: true})
	f.tx(func(tx pgx.Tx) error {
		var unchanged int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE id=ANY($1::uuid[]) AND fields='{"release":{"label":"Imported release"},"keep":true}'::jsonb`, []string{a, b}).Scan(&unchanged)
		if err == nil && unchanged != 2 {
			t.Errorf("import fields changed on %d nodes", 2-unchanged)
		}
		return err
	})
}

func TestNativeMembershipVisibilityBoundsAndUninitializedProject(t *testing.T) {
	f := ticketSetup(t)
	id := f.existing("ticket", f.project, "Visible", "open")
	deleted := f.existing("ticket", f.project, "Deleted", "open")
	var otherProject string
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, deleted); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Other project' FROM node_kinds WHERE slug='project' RETURNING nodes.id::text`, f.person.TenantID).Scan(&otherProject)
	})
	foreign := f.existing("ticket", otherProject, "Other ticket", "open")
	rows := f.readMembership(deleted, id, foreign, f.feature, "00000000-0000-0000-0000-000000000099")
	if len(rows) != 1 || rows[0].TicketID != id || rows[0].ReleaseID != nil {
		t.Fatalf("visibility %+v", rows)
	}
	before := f.counts()
	w := f.request(f.person, http.MethodGet, f.membershipReadPath(otherProject, foreign), "")
	if w.Code != 200 || f.counts() != before {
		t.Fatalf("read initialized or rejected plain project: %d %s", w.Code, w.Body.String())
	}
	f.tx(func(tx pgx.Tx) error {
		var initialized bool
		err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM journey_projects WHERE project_node_id=$1)`, otherProject).Scan(&initialized)
		if initialized {
			t.Error("read initialized journey")
		}
		return err
	})
	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{"empty", nil}, {"invalid", []string{"not-a-uuid"}},
		{"duplicate", []string{id, strings.ToUpper(id)}}, {"comma", []string{id + "," + foreign}},
		{"over100", make([]string, 101)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := f.request(f.person, http.MethodGet, f.membershipReadPath(f.project, tc.ids...), ""); w.Code != 400 {
				t.Fatalf("status %d %s", w.Code, w.Body.String())
			}
		})
	}
	ids := []string{id}
	for i := 1; i < 100; i++ {
		ids = append(ids, fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
	}
	if got := f.readMembership(ids...); !reflect.DeepEqual(got, rows) {
		t.Fatalf("100 bound: %+v", got)
	}
	if w := f.request(f.other, http.MethodGet, f.membershipReadPath(f.project, id), ""); w.Code != 404 {
		t.Fatalf("tenant boundary %d %s", w.Code, w.Body.String())
	}
	if w := f.request(tenant.Principal{}, http.MethodGet, f.membershipReadPath(f.project, id), ""); w.Code != 401 {
		t.Fatalf("anonymous %d", w.Code)
	}
	// A project-only binding cannot read or write the other project.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET scope_type='project',scope_id=$2 WHERE principal_id=$1`, f.person.ID, f.project)
		return err
	})
	if w := f.request(f.person, http.MethodGet, f.membershipReadPath(otherProject, foreign), ""); w.Code != 403 {
		t.Fatalf("project grant %d %s", w.Code, w.Body.String())
	}
	if w := f.addExisting([]string{id, foreign}, 1, false); w.Code != 404 || f.counts() != before {
		t.Fatalf("foreign membership atomicity %d %s", w.Code, w.Body.String())
	}
	if got := f.readMembership(id); len(got) != 1 || got[0].ReleaseID != nil {
		t.Fatalf("partial foreign batch assignment %+v", got)
	}
}

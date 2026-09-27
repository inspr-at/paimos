// SPDX-License-Identifier: AGPL-3.0-only

package releases

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func (f *ticketFixture) existing(kind, parent, title, state string) string {
	f.t.Helper()
	var id string
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title,state) SELECT $1,id,aeon_next_node_key($1,short_prefix),$2,$3,$4 FROM node_kinds WHERE slug=$5 RETURNING nodes.id::text`, f.person.TenantID, parent, title, state, kind).Scan(&id)
	})
	return id
}
func (f *ticketFixture) request(p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}
func (f *ticketFixture) membershipPath() string {
	return "/api/projects/" + f.project + "/releases/" + f.release + "/membership"
}
func (f *ticketFixture) addExisting(ids []string, revision int, confirm bool) *httptest.ResponseRecorder {
	b, _ := json.Marshal(membershipInput{Revision: int64(revision), IDs: ids, ConfirmMove: confirm})
	return f.request(f.person, http.MethodPost, f.membershipPath(), string(b))
}
func membershipOK(t *testing.T, w *httptest.ResponseRecorder) membershipResult {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out membershipResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExistingTicketOptionsBulkAddAndUndo(t *testing.T) {
	f := ticketSetup(t)
	ids := []string{f.existing("ticket", f.project, "Alpha", "open"), f.existing("ticket", f.feature, "Beta", "open"), f.existing("ticket", f.project, "Gamma", "open")}
	closed := f.existing("ticket", f.project, "Closed", "done")
	task := f.existing("task", f.project, "Task", "open")
	options := f.request(f.person, http.MethodGet, "/api/projects/"+f.project+"/releases/"+f.release+"/ticket-options?q=Alpha", "")
	if options.Code != 200 {
		t.Fatalf("options %d %s", options.Code, options.Body.String())
	}
	var found optionsResult
	if err := json.Unmarshal(options.Body.Bytes(), &found); err != nil {
		t.Fatal(err)
	}
	if found.Revision != 1 || len(found.Tickets) != 1 || found.Tickets[0].NodeID != ids[0] || found.Tickets[0].Availability != "addable" {
		t.Fatalf("options %+v", found)
	}
	if w := f.addExisting([]string{closed}, 1, false); w.Code != 409 {
		t.Fatalf("closed %d %s", w.Code, w.Body.String())
	}
	if w := f.addExisting([]string{task}, 1, false); w.Code != 404 {
		t.Fatalf("task %d %s", w.Code, w.Body.String())
	}
	if w := f.addExisting(ids, 2, false); w.Code != 409 {
		t.Fatalf("stale %d %s", w.Code, w.Body.String())
	}
	out := membershipOK(t, f.addExisting(ids, 1, false))
	if len(out.Walker.Tickets) != 3 || out.Walker.Revision != 2 || out.EventID < 1 {
		t.Fatalf("bulk result %+v", out)
	}
	for _, ticket := range out.Walker.Tickets {
		if !ticket.Included {
			t.Fatalf("not included %+v", ticket)
		}
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_tickets WHERE release_node_id=$1 AND scope_revision_required`, f.release).Scan(&count)
		if err == nil && count != 3 {
			t.Errorf("scope count %d", count)
		}
		return err
	})
	if w := f.addExisting(ids, 2, false); w.Code != 409 {
		t.Fatalf("repeat %d %s", w.Code, w.Body.String())
	}
	events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	undo := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", out.EventID), "")
	if undo.Code != 201 {
		t.Fatalf("undo %d %s", undo.Code, undo.Body.String())
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_tickets WHERE project_node_id=$1`, f.project).Scan(&count)
		if err == nil && count != 0 {
			t.Errorf("undo left %d rows", count)
		}
		return err
	})
	if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", out.EventID), ""); w.Code != 409 {
		t.Fatalf("second undo %d %s", w.Code, w.Body.String())
	}
}

func TestExistingTicketMoveConfirmationAndProjectBoundary(t *testing.T) {
	f := ticketSetup(t)
	id := f.existing("ticket", f.project, "Move me", "open")
	var old string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),$2,'Release 0' FROM node_kinds WHERE slug='release' RETURNING nodes.id::text`, f.person.TenantID, f.project).Scan(&old); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,2)`, f.person.TenantID, old, f.project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,0,'manual')`, f.person.TenantID, id, f.project, old)
		return err
	})
	if w := f.addExisting([]string{id}, 1, false); w.Code != 409 || !strings.Contains(w.Body.String(), "confirm_move") {
		t.Fatalf("confirmation %d %s", w.Code, w.Body.String())
	}
	out := membershipOK(t, f.addExisting([]string{id}, 1, true))
	if len(out.Walker.Tickets) != 1 || !out.Walker.Tickets[0].Included {
		t.Fatalf("move %+v", out)
	}
	events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", out.EventID), ""); w.Code != 201 {
		t.Fatalf("undo move %d %s", w.Code, w.Body.String())
	}
	f.tx(func(tx pgx.Tx) error {
		var assigned string
		err := tx.QueryRow(t.Context(), `SELECT release_node_id::text FROM journey_tickets WHERE ticket_node_id=$1`, id).Scan(&assigned)
		if err == nil && assigned != old {
			t.Errorf("restored release %s", assigned)
		}
		return err
	})
	if w := f.request(f.other, http.MethodGet, "/api/projects/"+f.project+"/releases/"+f.release+"/ticket-options", ""); w.Code != 404 {
		t.Fatalf("other tenant %d %s", w.Code, w.Body.String())
	}
	if w := f.request(f.other, http.MethodPost, f.membershipPath(), `{"expected_revision":1,"ticket_node_ids":["`+id+`"]}`); w.Code != 404 {
		t.Fatalf("other tenant write %d %s", w.Code, w.Body.String())
	}
}

func TestPlanRemovalEmitsUndoableMembershipAndFreshScope(t *testing.T) {
	f := ticketSetup(t)
	id := f.existing("ticket", f.project, "Backlog ticket", "open")
	added := membershipOK(t, f.addExisting([]string{id}, 1, false))
	body := fmt.Sprintf(`{"expected_revision":%d,"ordered_ticket_ids":[%q],"included_ticket_ids":[]}`, added.Walker.Revision, id)
	path := "/api/projects/" + f.project + "/releases/" + f.release + "/plan"
	w := f.request(f.person, http.MethodPut, path, body)
	if w.Code != 200 {
		t.Fatalf("remove %d %s", w.Code, w.Body.String())
	}
	var eventID int64
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE node_id=$1 AND type='journey.release_membership_changed' ORDER BY id DESC LIMIT 1`, f.release).Scan(&eventID)
	})
	if eventID == added.EventID {
		t.Fatal("removal omitted membership event")
	}
	events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", eventID), ""); w.Code != 201 {
		t.Fatalf("undo removal %d %s", w.Code, w.Body.String())
	}
	f.tx(func(tx pgx.Tx) error {
		var assigned string
		var scope bool
		var revision int64
		err := tx.QueryRow(t.Context(), `SELECT t.release_node_id::text,t.scope_revision_required,r.revision FROM journey_tickets t JOIN journey_releases r ON r.release_node_id=$2 WHERE t.ticket_node_id=$1`, id, f.release).Scan(&assigned, &scope, &revision)
		if err == nil && (assigned != f.release || !scope) {
			t.Errorf("restored assigned=%s scope=%t", assigned, scope)
		}
		if err == nil && revision != 4 {
			t.Errorf("undo revision %d", revision)
		}
		return err
	})
}

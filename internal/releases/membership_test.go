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
	if kind == "epic" || kind == "ticket" || kind == "task" {
		kind = "work"
	}
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
	task := f.existing("memory", f.project, "Non-work", "open")
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

func TestTicketOptionsHistoricalParentCanJoinNextRelease(t *testing.T) {
	for _, tc := range []struct {
		name                string
		undo, backlogIntent bool
	}{
		{name: "future child follows current placement"},
		{name: "Undo removes current placement", undo: true},
		{name: "Undo restores explicit backlog intent", undo: true, backlogIntent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ticketSetup(t)
			membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
			f.tx(func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released',released_at=now() WHERE release_node_id=$1`, f.release)
				return err
			})
			path := "/api/projects/" + f.project + "/releases/" + f.release + "/note-snapshot"
			frozen := f.request(f.person, http.MethodGet, path, "")
			if frozen.Code != 200 || !strings.Contains(frozen.Body.String(), f.feature) || !strings.Contains(frozen.Body.String(), `"frozen":true`) {
				t.Fatalf("missing historical snapshot: %d %s", frozen.Code, frozen.Body.String())
			}
			var historical string
			f.tx(func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT to_jsonb(j)::text FROM journey_tickets j WHERE ticket_node_id=$1`, f.feature).Scan(&historical)
			})
			leaf := f.existing("work", f.feature, "New backlog leaf", "open")
			if row := f.readMembership(leaf)[0]; row.ReleaseID != nil {
				t.Fatalf("new leaf must begin in backlog: %+v", row)
			}
			var next string
			f.tx(func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),$2,'Next release' FROM node_kinds WHERE slug='release' RETURNING nodes.id::text`, f.person.TenantID, f.project).Scan(&next); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,2)`, f.person.TenantID, next, f.project); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE journey_projects SET current_release_node_id=$2 WHERE project_node_id=$1`, f.project, next)
				return err
			})
			options := f.request(f.person, http.MethodGet, "/api/projects/"+f.project+"/releases/"+next+"/ticket-options", "")
			var found optionsResult
			if options.Code != 200 || json.Unmarshal(options.Body.Bytes(), &found) != nil {
				t.Fatalf("options %d %s", options.Code, options.Body.String())
			}
			var parent *ticketOption
			for i := range found.Tickets {
				if found.Tickets[i].NodeID == f.feature {
					parent = &found.Tickets[i]
				}
			}
			if parent == nil || !parent.IsParent || parent.Availability != "addable" || parent.ReleaseID != nil || parent.ReleaseTitle != nil {
				t.Fatalf("historical parent must be selectable from its current backlog leaves: %+v", parent)
			}
			if tc.backlogIntent {
				f.tx(func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO work_parent_releases(tenant_id,parent_node_id,project_node_id,release_node_id) VALUES($1,$2,$3,NULL)`, f.person.TenantID, f.feature, f.project)
					return err
				})
			}
			raw, _ := json.Marshal(membershipInput{Revision: found.Revision, IDs: []string{f.feature}})
			result := membershipOK(t, f.request(f.person, http.MethodPost, "/api/projects/"+f.project+"/releases/"+next+"/membership", string(raw)))
			if len(result.LeafIDs) != 1 || result.LeafIDs[0] != leaf || len(result.Walker.Tickets) != 1 || !result.Walker.Tickets[0].Included {
				t.Fatalf("parent did not place exactly its current leaf: %+v", result)
			}
			f.tx(func(tx pgx.Tx) error {
				var after string
				if err := tx.QueryRow(t.Context(), `SELECT to_jsonb(j)::text FROM journey_tickets j WHERE ticket_node_id=$1`, f.feature).Scan(&after); err != nil {
					return err
				}
				if after != historical {
					t.Fatal("placing the current leaves changed historical membership")
				}
				return nil
			})
			if after := f.request(f.person, http.MethodGet, path, ""); after.Code != 200 || after.Body.String() != frozen.Body.String() {
				t.Fatal("placing the current leaves changed the frozen snapshot")
			}
			// Use the real compensating endpoint before another write, or prove its
			// revision fence rejects Undo after a newly inherited child changes scope.
			events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
			if tc.undo {
				if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", result.EventID), ""); w.Code != 201 {
					t.Fatalf("undo historical parent placement: %d %s", w.Code, w.Body.String())
				}
				if row := f.readMembership(leaf)[0]; row.ReleaseID != nil {
					t.Fatalf("Undo did not return the existing leaf to backlog: %+v", row)
				}
				f.tx(func(tx pgx.Tx) error {
					var count int
					if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM work_parent_releases WHERE parent_node_id=$1 AND release_node_id IS NULL`, f.feature).Scan(&count); err != nil {
						return err
					}
					want := 0
					if tc.backlogIntent {
						want = 1
					}
					if count != want {
						t.Fatalf("Undo restored %d backlog intents, want %d", count, want)
					}
					return nil
				})
			}
			fresh := f.existing("work", f.feature, "Future child", "open")
			row := f.readMembership(fresh)[0]
			if tc.undo {
				if row.ReleaseID != nil || row.ReleaseCount != 0 {
					t.Fatalf("future child inherited an undone placement: %+v", row)
				}
			} else {
				if row.ReleaseID == nil || *row.ReleaseID != next || row.ReleaseCount != 1 {
					t.Fatalf("future child did not inherit current parent placement: %+v", row)
				}
				if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", result.EventID), ""); w.Code != 409 || !strings.Contains(w.Body.String(), "conflict") {
					t.Fatalf("stale Undo after inherited child: %d %s", w.Code, w.Body.String())
				}
			}
			f.tx(func(tx pgx.Tx) error {
				var state string
				var note *string
				if err := tx.QueryRow(t.Context(), `SELECT state,fields->>'release_inheritance_note' FROM nodes WHERE id=$1`, fresh).Scan(&state, &note); err != nil {
					return err
				}
				if tc.undo && !tc.backlogIntent {
					if state != "backlog" || note == nil || *note != "parent_release_closed" {
						t.Fatalf("without current intent the historical closed release must explain backlog: state=%s note=%v", state, note)
					}
				} else if state != "open" || note != nil {
					t.Fatalf("current intent incorrectly inherited the historical closed-release note: state=%s note=%v", state, note)
				}
				var after string
				if err := tx.QueryRow(t.Context(), `SELECT to_jsonb(j)::text FROM journey_tickets j WHERE ticket_node_id=$1`, f.feature).Scan(&after); err != nil {
					return err
				}
				if after != historical {
					t.Fatal("future-child inheritance or Undo changed historical membership")
				}
				return nil
			})
			if after := f.request(f.person, http.MethodGet, path, ""); after.Code != 200 || after.Body.String() != frozen.Body.String() {
				t.Fatal("future-child inheritance or Undo changed the frozen snapshot")
			}
		})
	}
}

func TestTicketOptionsParentMatchesCurrentLeaves(t *testing.T) {
	for _, tc := range []struct {
		name, source, availability string
		backlog                    bool
	}{
		{"backlog", "", "addable", true},
		{"included", "target", "included", false},
		{"partly included", "target", "addable", true},
		{"another planning release", "planning", "other_release", false},
		{"partly in another release", "planning", "other_release", true},
		{"released leaf", "released", "released", true},
		{"superseded leaf", "superseded", "released", true},
		{"active leaf", "building", "active_release", true},
		{"deleted source", "deleted", "active_release", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ticketSetup(t)
			nested := f.existing("work", f.feature, "Nested parent", "open")
			a := f.existing("work", nested, "Nested leaf", "done")
			b := f.existing("work", f.feature, "Sibling leaf", "open")
			source := f.release
			f.tx(func(tx pgx.Tx) error {
				if tc.source != "" && tc.source != "target" {
					if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),$2,'Source release' FROM node_kinds WHERE slug='release' RETURNING nodes.id::text`, f.person.TenantID, f.project).Scan(&source); err != nil {
						return err
					}
					state := tc.source
					if state == "deleted" {
						state = "planning"
					}
					if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at) VALUES($1,$2,$3,2,$4,CASE WHEN $4 IN ('released','superseded') THEN now() END)`, f.person.TenantID, source, f.project, state); err != nil {
						return err
					}
				}
				if tc.source != "" {
					ids := []string{a}
					if !tc.backlog {
						ids = append(ids, b)
					}
					for i, id := range ids {
						if _, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,$5,'manual')`, f.person.TenantID, id, f.project, source, i); err != nil {
							return err
						}
					}
				}
				if tc.source == "deleted" {
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, source)
					return err
				}
				return nil
			})
			w := f.request(f.person, http.MethodGet, "/api/projects/"+f.project+"/releases/"+f.release+"/ticket-options", "")
			var options optionsResult
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &options) != nil {
				t.Fatalf("options %d %s", w.Code, w.Body.String())
			}
			seen := false
			for _, option := range options.Tickets {
				if option.NodeID != f.feature {
					continue
				}
				seen = true
				if !option.IsParent || option.Availability != tc.availability {
					t.Fatalf("parent must reflect its current leaves: %+v", option)
				}
				if tc.backlog {
					if option.ReleaseID != nil || option.ReleaseTitle != nil {
						t.Fatalf("partial coverage must not claim a single release: %+v", option)
					}
				} else if option.ReleaseID == nil || *option.ReleaseID != source || option.ReleaseTitle == nil {
					t.Fatalf("full coverage lost its release: %+v", option)
				}
			}
			if !seen {
				t.Fatal("parent absent from options")
			}
			w = f.addExisting([]string{f.feature}, int(options.Revision), false)
			switch tc.availability {
			case "other_release":
				if w.Code != 409 || !strings.Contains(w.Body.String(), "confirm_move") {
					t.Fatalf("expected confirmation: %d %s", w.Code, w.Body.String())
				}
				membershipOK(t, f.addExisting([]string{f.feature}, int(options.Revision), true))
			case "released", "active_release":
				if w.Code != 409 || (!strings.Contains(w.Body.String(), "cannot move") && !strings.Contains(w.Body.String(), "source release is unavailable")) {
					t.Fatalf("expected source refusal: %d %s", w.Code, w.Body.String())
				}
			default:
				membershipOK(t, w)
			}
		})
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

func TestTicketOptionsMatchSourceReleaseWriteEligibility(t *testing.T) {
	f := ticketSetup(t)
	for i, state := range []string{"planning", "building", "candidate", "deploying", "refused", "access", "released", "superseded", "deleted"} {
		t.Run(state, func(t *testing.T) {
			sub := *f
			sub.t = t
			f := &sub
			id := f.existing("ticket", f.project, "Source "+state, "open")
			var source string
			f.tx(func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),$2,'Source release' FROM node_kinds WHERE slug='release' RETURNING nodes.id::text`, f.person.TenantID, f.project).Scan(&source); err != nil {
					return err
				}
				stored := state
				if stored == "deleted" {
					stored = "planning"
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at) VALUES($1,$2,$3,$4,$5,CASE WHEN $5 IN ('released','superseded') THEN now() END)`, f.person.TenantID, source, f.project, i+2, stored); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,$5,'manual')`, f.person.TenantID, id, f.project, source, i); err != nil {
					return err
				}
				if state == "deleted" {
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, source)
					return err
				}
				return nil
			})
			var revision int
			f.tx(func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT revision FROM journey_releases WHERE release_node_id=$1`, f.release).Scan(&revision)
			})
			w := f.request(f.person, http.MethodGet, "/api/projects/"+f.project+"/releases/"+f.release+"/ticket-options", "")
			var options optionsResult
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &options) != nil {
				t.Fatalf("options %d %s", w.Code, w.Body.String())
			}
			want := "active_release"
			if state == "planning" {
				want = "other_release"
			} else if state == "released" || state == "superseded" {
				want = "released"
			}
			availability := "missing option"
			for _, option := range options.Tickets {
				if option.NodeID == id {
					availability = option.Availability
				}
			}
			if availability != want {
				t.Fatalf("availability %q want %q", availability, want)
			}
			before := f.counts()
			w = f.addExisting([]string{id}, revision, false)
			if w.Code != 409 || f.counts() != before {
				t.Fatalf("unconfirmed %d %s", w.Code, w.Body.String())
			}
			w = f.addExisting([]string{id}, revision, true)
			if state == "planning" {
				membershipOK(t, w)
			} else if w.Code != 409 || f.counts() != before {
				t.Fatalf("disabled source accepted or changed: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMixedPlanReorderMembershipUndoRestoresAllPositions(t *testing.T) {
	f := ticketSetup(t)
	a := f.existing("ticket", f.project, "A", "open")
	b := f.existing("ticket", f.project, "B", "open")
	added := membershipOK(t, f.addExisting([]string{a, b}, 1, false))
	if added.Walker.Tickets[0].NodeID != a || added.Walker.Tickets[0].Position != 0 || added.Walker.Tickets[1].NodeID != b || added.Walker.Tickets[1].Position != 1 {
		t.Fatalf("initial %+v", added.Walker.Tickets)
	}
	path := "/api/projects/" + f.project + "/releases/" + f.release
	plan := fmt.Sprintf(`{"expected_revision":2,"ordered_ticket_ids":[%q,%q],"included_ticket_ids":[%q]}`, b, a, b)
	changed := ticketPlan(t, f.request(f.person, http.MethodPut, path+"/plan", plan))
	if changed.Tickets[0].NodeID != b || changed.Tickets[0].Position != 0 || !changed.Tickets[0].Included || changed.Tickets[1].NodeID != a || changed.Tickets[1].Included {
		t.Fatalf("changed %+v", changed.Tickets)
	}
	var eventID int64
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE node_id=$1 AND type='journey.release_membership_changed' ORDER BY id DESC LIMIT 1`, f.release).Scan(&eventID)
	})
	events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	undo := fmt.Sprintf("/api/events/%d/undo", eventID)
	// Even B, whose membership did not change, must be fenced for its position.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_tickets SET walker_position=9 WHERE ticket_node_id=$1`, b)
		return err
	})
	if w := f.request(f.person, http.MethodPost, undo, ""); w.Code != 409 {
		t.Fatalf("unfenced other position: %d %s", w.Code, w.Body.String())
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_tickets SET walker_position=0 WHERE ticket_node_id=$1`, b)
		return err
	})
	if w := f.request(f.person, http.MethodPost, undo, ""); w.Code != 201 {
		t.Fatalf("undo %d %s", w.Code, w.Body.String())
	}
	restored := ticketPlan(t, f.request(f.person, http.MethodGet, path+"/walker", ""))
	if len(restored.Tickets) != 2 || restored.Tickets[0].NodeID != a || restored.Tickets[0].Position != 0 || !restored.Tickets[0].Included || restored.Tickets[1].NodeID != b || restored.Tickets[1].Position != 1 || !restored.Tickets[1].Included {
		t.Fatalf("undo order/membership %+v", restored.Tickets)
	}
	// A later plan revision cannot be overwritten by a stale Undo.
	plan = fmt.Sprintf(`{"expected_revision":%d,"ordered_ticket_ids":[%q,%q],"included_ticket_ids":[%q]}`, restored.Revision, b, a, b)
	changed = ticketPlan(t, f.request(f.person, http.MethodPut, path+"/plan", plan))
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE node_id=$1 AND type='journey.release_membership_changed' ORDER BY id DESC LIMIT 1`, f.release).Scan(&eventID)
	})
	plan = fmt.Sprintf(`{"expected_revision":%d,"ordered_ticket_ids":[%q,%q],"included_ticket_ids":[%q]}`, changed.Revision, a, b, b)
	ticketPlan(t, f.request(f.person, http.MethodPut, path+"/plan", plan))
	if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", eventID), ""); w.Code != 409 {
		t.Fatalf("stale undo %d %s", w.Code, w.Body.String())
	}
}

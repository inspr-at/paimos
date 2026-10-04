// SPDX-License-Identifier: AGPL-3.0-only
package crm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
)

func withUndo(t *testing.T, f *fixture) {
	t.Helper()
	plug, e := Plugin()
	if e != nil {
		t.Fatal(e)
	}
	reg := plugins.NewRegistry()
	if e = reg.Register(plug); e != nil {
		t.Fatal(e)
	}
	reg.Seal()
	f.handler = (&httpapi.Server{Pool: f.db.App, Modules: []httpapi.Module{New(f.db.App, reg), events.New(f.db.App, events.WithUndoHandlers(UndoHandlers(reg)))}}).Handler()
}
func eventOf(t *testing.T, f fixture, typ string) events.Event {
	t.Helper()
	all := logEvents(t, f)
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Type == typ {
			return all[i]
		}
	}
	t.Fatalf("missing %s event", typ)
	return events.Event{}
}
func undoEvent(t *testing.T, f fixture, event events.Event, status int) {
	t.Helper()
	expect(t, request(f.handler, f.admin, http.MethodPost, "/api/events/"+strconv.FormatInt(event.ID, 10)+"/undo", ""), status)
}

func TestCRMUndoRechecksUndoPermissionAfterFence(t *testing.T) {
	for _, other := range []bool{false, true} {
		name := "own event"
		if other {
			name = "other actor event"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			withUndo(t, &f)
			expect(t, jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations", map[string]any{"name": "Keep this customer"}), 201)
			event := eventOf(t, f, "crm.customer_created")
			caller := f.admin
			if other {
				caller.ID = f.second
			}
			var role string
			if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, caller.TenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'undo_writer','Undo writer') RETURNING id::text`, caller.TenantID).Scan(&role); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission)
				 SELECT $1::uuid,$2::uuid,unnest(ARRAY['nodes.read','events.read','events.undo','events.undo_other','crm.write'])`, caller.TenantID, role); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1 AND scope_type='workspace'`, caller.ID, role)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			revoke, err := f.db.Admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer revoke.Rollback(context.Background())
			if _, err = revoke.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, caller.TenantID); err != nil {
				t.Fatal(err)
			}
			if _, err = revoke.Exec(ctx, `DELETE FROM role_permissions WHERE tenant_id=$1 AND role_id=$2 AND permission='events.undo'`, caller.TenantID, role); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest(http.MethodPost, "/api/events/"+strconv.FormatInt(event.ID, 10)+"/undo", nil)
				r = r.WithContext(tenant.WithPrincipal(ctx, caller))
				w := httptest.NewRecorder()
				f.handler.ServeHTTP(w, r)
				done <- w
			}()
			// The route sees the committed grant. Reaching the tenant-row wait
			// proves the route accepted the request before revocation commits.
			dbtest.WaitForLock(t, ctx, f.db, revoke.Conn().PgConn().PID(), "transactionid")
			if err = revoke.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-done:
				expect(t, w, http.StatusForbidden)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var deleted bool
			var undos int
			if err = f.db.Admin.QueryRow(ctx, `SELECT deleted_at IS NOT NULL,
			 (SELECT count(*) FROM events WHERE tenant_id=$1 AND undo_of=$3)
			 FROM nodes WHERE tenant_id=$1 AND id=$2`, caller.TenantID, *event.NodeID, event.ID).Scan(&deleted, &undos); err != nil {
				t.Fatal(err)
			}
			if deleted || undos != 0 {
				t.Fatalf("denied undo mutated customer or history: deleted=%v undo events=%d", deleted, undos)
			}
			// Restoring exactly the withdrawn grant proves the resource and all
			// remaining permissions stayed valid throughout the denied request.
			if _, err = f.db.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'events.undo')`, caller.TenantID, role); err != nil {
				t.Fatal(err)
			}
			expect(t, request(f.handler, caller, http.MethodPost, "/api/events/"+strconv.FormatInt(event.ID, 10)+"/undo", ""), http.StatusCreated)
		})
	}
}

func TestCRMUndoUsesCurrentAuthority(t *testing.T) {
	for _, tc := range []struct {
		name        string
		permissions []string
		legacy      []string
		owner       bool
		want        int
	}{
		{"revoked stale admin", []string{"nodes.read", "events.read", "events.undo"}, []string{"admin"}, false, 403},
		{"events only custom role", []string{"nodes.read", "events.read", "events.undo", "events.undo_other"}, []string{"admin"}, false, 403},
		{"owner without legacy admin", nil, []string{}, true, 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			withUndo(t, &f)
			expect(t, jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations", map[string]any{"name": "Authority fixture"}), 201)
			event := eventOf(t, f, "crm.customer_created")
			if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.admin.TenantID, func(tx pgx.Tx) error {
				var role string
				if tc.owner {
					if err := tx.QueryRow(t.Context(), `SELECT id::text FROM roles WHERE key='owner'`).Scan(&role); err != nil {
						return err
					}
				} else {
					if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'undo_test','Undo test') RETURNING id::text`, f.admin.TenantID).Scan(&role); err != nil {
						return err
					}
					if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1::uuid,$2::uuid,unnest($3::text[])`, f.admin.TenantID, role, tc.permissions); err != nil {
						return err
					}
				}
				if _, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1 AND scope_type='workspace'`, f.admin.ID, role); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE principals SET roles=$2 WHERE id=$1`, f.admin.ID, tc.legacy)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			f.admin.Roles = tc.legacy
			undoEvent(t, f, event, tc.want)
			var deleted bool
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT deleted_at IS NOT NULL FROM nodes WHERE id=$1`, *event.NodeID).Scan(&deleted); err != nil {
				t.Fatal(err)
			}
			if deleted != (tc.want == 201) {
				t.Fatal("undo mutation disagrees with live authority")
			}
		})
	}
}

func TestCRMUndoBindingRequiresManageGrant(t *testing.T) {
	f := setup(t)
	withUndo(t, &f)
	expect(t, jsonRequest(t, f, f.admin, "POST", "/api/crm/contacts/"+f.contact+"/principals", map[string]any{"principal_id": f.customer}), 201)
	event := eventOf(t, f, EventContactBound)
	var role string
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.admin.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'crm_writer','CRM writer') RETURNING id::text`, f.admin.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1::uuid,$2::uuid,unnest(ARRAY['nodes.read','events.read','events.undo','crm.write'])`, f.admin.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1 AND scope_type='workspace'`, f.admin.ID, role)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	undoEvent(t, f, event, 403)
	if count(t, f, f.admin.TenantID, `SELECT count(*) FROM crm_contact_principals`) != 1 {
		t.Fatal("denied undo removed binding")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'crm.manage')`, f.admin.TenantID, role)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	undoEvent(t, f, event, 201)
	if count(t, f, f.admin.TenantID, `SELECT count(*) FROM crm_contact_principals`) != 0 {
		t.Fatal("permitted undo left binding")
	}
}
func TestCRMUndoStaleAndPrimary(t *testing.T) {
	f := setup(t)
	withUndo(t, &f)
	w := jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations", map[string]any{"name": "Undo Inc"})
	expect(t, w, 201)
	var c Customer
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	w = jsonRequest(t, f, f.admin, "PATCH", "/api/crm/organisations/"+c.ID, map[string]any{"name": "Changed Inc", "expected_revision": c.Revision})
	expect(t, w, 200)
	updated := eventOf(t, f, "crm.customer_updated")
	undoEvent(t, f, updated, 201)
	undoEvent(t, f, updated, 409)
	w = request(f.handler, f.admin, "GET", "/api/crm/organisations/"+c.ID, "")
	expect(t, w, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if c.Name != "Undo Inc" {
		t.Fatal("customer undo did not restore name")
	}
	w = jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations/"+c.ID+"/contacts", map[string]any{"name": "First"})
	expect(t, w, 201)
	var first ContactRecord
	_ = json.Unmarshal(w.Body.Bytes(), &first)
	firstPrimary := eventOf(t, f, "crm.primary_contact_changed")
	w = jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations/"+c.ID+"/contacts", map[string]any{"name": "Second"})
	expect(t, w, 201)
	var second ContactRecord
	_ = json.Unmarshal(w.Body.Bytes(), &second)
	w = request(f.handler, f.admin, "GET", "/api/crm/organisations/"+c.ID, "")
	expect(t, w, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	w = jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations/"+c.ID+"/primary-contact", map[string]any{"contact_node_id": second.ID, "expected_revision": c.Revision})
	expect(t, w, 200)
	promote := eventOf(t, f, "crm.primary_contact_changed")
	undoEvent(t, f, promote, 201)
	w = request(f.handler, f.admin, "GET", "/api/crm/organisations/"+c.ID, "")
	expect(t, w, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if c.PrimaryContactNodeID == nil || *c.PrimaryContactNodeID != first.ID {
		t.Fatal("primary undo failed")
	}
	// The first promotion is stale after further CRM changes.
	undoEvent(t, f, firstPrimary, 409)
	contactCreated := eventOf(t, f, "crm.contact_created")
	undoEvent(t, f, contactCreated, 201)
}
func TestCRMDeletionAndProjectUndo(t *testing.T) {
	f := setup(t)
	withUndo(t, &f)
	w := jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations", map[string]any{"name": "Delete Me"})
	expect(t, w, 201)
	var c Customer
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	w = jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations/"+c.ID+"/contacts", map[string]any{"name": "Contact"})
	expect(t, w, 201)
	var contact ContactRecord
	_ = json.Unmarshal(w.Body.Bytes(), &contact)
	var project string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.admin.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PRJ-1','Project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.admin.TenantID).Scan(&project)
	})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, jsonRequest(t, f, f.admin, "PUT", "/api/crm/projects/"+project+"/customer", map[string]any{"organisation_node_id": c.ID}), 200)
	link := eventOf(t, f, "crm.project_customer_changed")
	undoEvent(t, f, link, 201)
	expect(t, jsonRequest(t, f, f.admin, "PUT", "/api/crm/projects/"+project+"/cooperation", map[string]any{"engagement": "retainer"}), 200)
	cooperation := eventOf(t, f, "crm.project_cooperation_changed")
	undoEvent(t, f, cooperation, 201)
	expect(t, request(f.handler, f.admin, "DELETE", "/api/crm/organisations/"+c.ID, ""), 204)
	undoEvent(t, f, eventOf(t, f, "crm.customer_deleted"), 201)
	undoEvent(t, f, eventOf(t, f, "crm.contact_deleted"), 201)
	undoEvent(t, f, eventOf(t, f, "crm.primary_contact_changed"), 201)
	w = request(f.handler, f.admin, "GET", "/api/crm/organisations/"+c.ID, "")
	expect(t, w, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if c.PrimaryContactNodeID == nil || *c.PrimaryContactNodeID != contact.ID {
		t.Fatalf("primary not restored %+v", c)
	}
}

// QL1/AEON-109: archiving a customer hides it from the default list, keeps it
// readable, and is undone through its event; members cannot archive.
func TestCustomerArchiveListAndUndo(t *testing.T) {
	f := setup(t)
	withUndo(t, &f)
	w := jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations", map[string]any{"name": "Archive Me"})
	expect(t, w, 201)
	var c Customer
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	expect(t, jsonRequest(t, f, f.admin, "PATCH", "/api/crm/organisations/"+c.ID+"/visibility", map[string]any{"archived": true}), 400)
	expect(t, jsonRequest(t, f, f.admin, "PATCH", "/api/crm/organisations/"+c.ID+"/visibility", map[string]any{"archived": true, "expected_revision": c.Revision + 5}), 409)
	w = jsonRequest(t, f, f.admin, "PATCH", "/api/crm/organisations/"+c.ID+"/visibility", map[string]any{"archived": true, "expected_revision": c.Revision})
	expect(t, w, 200)
	var archived Customer
	_ = json.Unmarshal(w.Body.Bytes(), &archived)
	if !archived.Archived || archived.Revision != c.Revision+1 {
		t.Fatalf("archive %+v", archived)
	}
	listed := func(query string) bool {
		t.Helper()
		w := request(f.handler, f.admin, "GET", "/api/crm/organisations?limit=100"+query, "")
		expect(t, w, 200)
		return strings.Contains(w.Body.String(), c.ID)
	}
	if listed("") || !listed("&archived=all") || !listed("&archived=true") {
		t.Fatal("archived customers leave only the default list")
	}
	expect(t, request(f.handler, f.admin, "GET", "/api/crm/organisations?archived=maybe", ""), 400)
	expect(t, request(f.handler, f.admin, "GET", "/api/crm/organisations/"+c.ID, ""), 200)
	undoEvent(t, f, eventOf(t, f, EventCustomerVisibility), 201)
	w = request(f.handler, f.admin, "GET", "/api/crm/organisations/"+c.ID, "")
	expect(t, w, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if c.Archived || !listed("") {
		t.Fatal("archive undo did not restore the customer")
	}
	undoEvent(t, f, eventOf(t, f, EventCustomerVisibility), 409)
}

// The receipt of the first accepted write must never rebind to the second write.
func TestCustomerUndoReceiptSurvivesInterveningMutation(t *testing.T) {
	f := setup(t)
	withUndo(t, &f)
	w := jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations", map[string]any{"name": "Original"})
	expect(t, w, 201)
	var c Customer
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	w = jsonRequest(t, f, f.admin, "PATCH", "/api/crm/organisations/"+c.ID, map[string]any{"name": "First", "expected_revision": c.Revision})
	expect(t, w, 200)
	receipt := w.Header().Get(events.MutationEventHeader)
	first := eventOf(t, f, "crm.customer_updated")
	if receipt != strconv.FormatInt(first.ID, 10) {
		t.Fatalf("receipt %q does not identify accepted event %d", receipt, first.ID)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	w = jsonRequest(t, f, f.admin, "PATCH", "/api/crm/organisations/"+c.ID, map[string]any{"name": "Second", "expected_revision": c.Revision})
	expect(t, w, 200)
	if w.Header().Get(events.MutationEventHeader) == receipt {
		t.Fatal("distinct mutations share a receipt")
	}
	expect(t, request(f.handler, f.admin, "POST", "/api/events/"+receipt+"/undo", ""), 409)
	w = request(f.handler, f.admin, "GET", "/api/crm/organisations/"+c.ID, "")
	expect(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Name != "Second" {
		t.Fatalf("Undo reverted the later change: %s", c.Name)
	}
}

// JSON timestamps preserve instants, not time.Time's internal location pointer.
func TestCRMUndoBindingSnapshotTimestamp(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "same instant in another zone"
		if changed {
			name = "different instant conflicts"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			withUndo(t, &f)
			expect(t, jsonRequest(t, f, f.admin, "POST", "/api/crm/contacts/"+f.contact+"/principals", map[string]any{"principal_id": f.customer}), 201)
			event := eventOf(t, f, EventContactBound)
			expected, err := snapshotAs[Binding](event.After)
			if err != nil {
				t.Fatal(err)
			}
			expected.BoundAt = expected.BoundAt.In(time.FixedZone("snapshot", 2*60*60))
			if changed {
				expected.BoundAt = expected.BoundAt.Add(time.Microsecond)
			}
			event.After, err = json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			ctx := tenant.WithPrincipal(t.Context(), f.admin)
			err = db.InTenant(ctx, f.db.App, f.admin.TenantID, func(tx pgx.Tx) error {
				_, err := UndoHandlers()[EventContactBound](ctx, tx, f.admin, event)
				return err
			})
			if changed {
				if !errors.Is(err, events.ErrConflict) {
					t.Fatalf("changed timestamp: %v", err)
				}
			} else if err != nil {
				t.Fatalf("same timestamp instant: %v", err)
			}
			want := 0
			if changed {
				want = 1
			}
			if got := count(t, f, f.admin.TenantID, `SELECT count(*) FROM crm_contact_principals`); got != want {
				t.Fatalf("binding count=%d want %d", got, want)
			}
		})
	}
}

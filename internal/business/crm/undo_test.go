// SPDX-License-Identifier: AGPL-3.0-only
package crm

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
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

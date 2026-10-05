// SPDX-License-Identifier: AGPL-3.0-only

package relations

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/business/crm"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestCRMRelationKindsDirectionAndUndo(t *testing.T) {
	f := setup(t)
	ids := map[string]string{}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		for _, kind := range []struct{ slug, prefix string }{
			{crm.Organisation, "ORG"}, {crm.Contact, "CON"}, {crm.Quote, "QUO"},
		} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,$2,$2,$3,$2)`, f.a.TenantID, kind.slug, kind.prefix); err != nil {
				return err
			}
		}
		for _, node := range []struct{ name, slug, key string }{
			{"org", crm.Organisation, "ORG-1"},
			{"org2", crm.Organisation, "ORG-2"},
			{"contact", crm.Contact, "CON-1"},
			{"project", crm.Project, "PRJ-1"},
			{"quote", crm.Quote, "QUO-1"},
		} {
			var id string
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title)
				SELECT $1, id, $2, $3 FROM node_kinds WHERE tenant_id=$1 AND slug=$4 RETURNING id::text`,
				f.a.TenantID, node.key, node.name, node.slug).Scan(&id); err != nil {
				return err
			}
			ids[node.name] = id
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	customer := create(t, f, ids["org"], ids["project"], crm.CustomerOf)
	if customer.SourceNodeID != ids["org"] || customer.TargetNodeID != ids["project"] || customer.Type != crm.CustomerOf {
		t.Fatalf("direction changed %+v", customer)
	}
	create(t, f, ids["org2"], ids["quote"], crm.CustomerOf)
	create(t, f, ids["contact"], ids["org"], crm.ContactFor)
	create(t, f, ids["contact"], ids["project"], crm.ContactFor)
	create(t, f, ids["contact"], ids["quote"], crm.ContactFor)
	create(t, f, ids["org"], ids["project"], "relates")
	if len(logEvents(t, f)) != 6 {
		t.Fatalf("events %d", len(logEvents(t, f)))
	}
	refused := []struct{ source, target, typ string }{
		{ids["project"], ids["org"], crm.CustomerOf},
		{ids["org"], f.nodes[0], crm.CustomerOf},
		{ids["org"], ids["contact"], crm.CustomerOf},
		{ids["org"], ids["contact"], crm.ContactFor},
		{ids["contact"], f.nodes[0], crm.ContactFor},
		{f.nodes[0], f.nodes[1], crm.CustomerOf},
	}
	for _, tc := range refused {
		expect(t, request(f.handler, f.a, "POST", "/api/relations", fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":%q}`, tc.source, tc.target, tc.typ)), 409)
	}
	expect(t, request(f.handler, f.b, "POST", "/api/relations", fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":%q}`, ids["org"], ids["project"], crm.CustomerOf)), 404)
	if len(logEvents(t, f)) != 6 {
		t.Fatal("rejected CRM link wrote an event")
	}
	w := request(f.handler, f.b, "GET", "/api/relations?node_id="+ids["org"], "")
	expect(t, w, 200)
	if !json.Valid(w.Body.Bytes()) || !containsItemsEmpty(w.Body.String()) {
		t.Fatal("cross-tenant CRM relations leaked")
	}

	expect(t, request(f.handler, f.a, "DELETE", "/api/relations/"+customer.ID, ""), 204)
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET kind_id = (SELECT id FROM node_kinds WHERE tenant_id=$1 AND slug='work') WHERE id=$2`, f.a.TenantID, ids["org"])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	deleted := logEvents(t, f)
	var deleteID int64
	for _, ev := range deleted {
		if ev.Type == "relation.deleted" {
			deleteID = ev.ID
		}
	}
	if deleteID == 0 {
		t.Fatal("missing delete event")
	}
	expect(t, request(f.handler, f.a, "POST", fmt.Sprintf("/api/events/%d/undo", deleteID), ""), 409)
	if len(logEvents(t, f)) != len(deleted) {
		t.Fatal("kind mismatch undo appended an event")
	}
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET kind_id = (SELECT id FROM node_kinds WHERE tenant_id=$1 AND slug='organisation') WHERE id=$2`, f.a.TenantID, ids["org"])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.handler, f.a, "POST", fmt.Sprintf("/api/events/%d/undo", deleteID), ""), 201)
	w = request(f.handler, f.a, "GET", "/api/relations?node_id="+ids["org"], "")
	expect(t, w, 200)
	var pageBody page
	if err := json.Unmarshal(w.Body.Bytes(), &pageBody); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range pageBody.Items {
		if item.ID == customer.ID && item.Type == crm.CustomerOf && item.SourceNodeID == ids["org"] && item.TargetNodeID == ids["project"] {
			found = true
		}
	}
	if !found {
		t.Fatalf("undo did not restore customer_of: %+v", pageBody.Items)
	}
}

func containsItemsEmpty(body string) bool {
	return len(body) > 0 && (body == `{"items":[],"next_cursor":null}`+"\n" || jsonContainsEmpty(body))
}

func jsonContainsEmpty(body string) bool {
	var payload struct {
		Items []Relation `json:"items"`
	}
	return json.Unmarshal([]byte(body), &payload) == nil && len(payload.Items) == 0
}

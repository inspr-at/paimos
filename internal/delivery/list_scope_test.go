// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestUnscopedDeliveryOmitsProjectsWithoutDeliveryRead(t *testing.T) {
	// Risk: AnyProject delivery.read plus node visibility returns another
	// project's hold reason and head, and a row that belongs to no project.
	f := newFixture(t)
	var other, reader, granted, hidden, blank string
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'OTHER-1',id,'Other project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&other); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Delivery reader') RETURNING id::text`, f.person.TenantID).Scan(&reader); err != nil {
			return err
		}
		var nodesOnly, deliveryOnly string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'nodes_only','Nodes only') RETURNING id::text`, f.person.TenantID).Scan(&nodesOnly); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read')`, f.person.TenantID, nodesOnly); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'delivery_only','Delivery only') RETURNING id::text`, f.person.TenantID).Scan(&deliveryOnly); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'delivery.read'),($1,$2,'nodes.read')`, f.person.TenantID, deliveryOnly); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, f.person.TenantID, reader, nodesOnly); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, f.person.TenantID, reader, deliveryOnly, f.project); err != nil {
			return err
		}
		insert := func(project any, pr int, reason, sha string) (string, error) {
			var id string
			err := tx.QueryRow(t.Context(), `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,pull_request,branch,head_sha,state,state_since,owner,held_reason,observation,updated_at)
				VALUES($1,gen_random_uuid(),$2,$3,'example/delivery',$4,'work/secret',$5,'held',timestamptz '2026-10-07T00:00:00Z','person',$6,'{}'::jsonb,timestamptz '2026-10-07T00:00:00Z') RETURNING id::text`,
				f.person.TenantID, project, f.ticket, pr, sha, reason).Scan(&id)
			return id, err
		}
		var err error
		if granted, err = insert(f.project, 11, "visible freeze", strings.Repeat("a", 40)); err != nil {
			return err
		}
		if hidden, err = insert(other, 12, "hidden freeze", strings.Repeat("b", 40)); err != nil {
			return err
		}
		blank, err = insert(nil, 13, "orphan freeze", strings.Repeat("c", 40))
		return err
	})
	who := tenant.Principal{ID: reader, TenantID: f.person.TenantID, Kind: tenant.Person}
	var page Page
	f.call(t, who, "GET", "/api/delivery", nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].ID != granted || page.Items[0].HeldReason == nil || *page.Items[0].HeldReason != "visible freeze" || page.Items[0].Head != strings.Repeat("a", 40) {
		t.Fatalf("unscoped list leaked: %+v", page.Items)
	}
	f.call(t, who, "GET", "/api/delivery?project=OTHER", nil, 403, nil)
	var all Page
	f.call(t, f.person, "GET", "/api/delivery?limit=100", nil, 200, &all)
	seen := map[string]bool{}
	for _, item := range all.Items {
		seen[item.ID] = true
		if item.ID == hidden && (item.HeldReason == nil || *item.HeldReason != "hidden freeze") {
			t.Fatalf("workspace reader lost the other project: %+v", item)
		}
	}
	if !seen[granted] || !seen[hidden] || !seen[blank] {
		t.Fatalf("workspace reader missed rows: granted=%v hidden=%v blank=%v items=%d", seen[granted], seen[hidden], seen[blank], len(all.Items))
	}
}

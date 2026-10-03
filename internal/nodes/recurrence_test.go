// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestNodeRecurrenceProvenance(t *testing.T) {
	p := newPrincipal(t, "recurrence-marker")
	project, ticket := kindBySlug(t, p, "project"), kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Visible"}`)
	hidden := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Hidden source"}`)
	create := func(title, fields string) nodeJSON {
		return mustNode(t, p, `{"kind_id":"`+ticket.ID+`","parent_id":"`+root.ID+`","title":"`+title+`","fields":`+fields+`}`)
	}
	occurrence := create("Actual occurrence", `{}`)
	moved := create("Moved occurrence", `{}`)
	ordinary := create("Ordinary", `{}`)
	fake := create("Editable fields are not provenance", `{"recurrence_id":"63700000-0000-4000-8000-000000000001","occurrence_number":99}`)
	var source, hiddenSource string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		for i, projectID := range []string{root.ID, hidden.ID} {
			var id string
			err := tx.QueryRow(t.Context(), `INSERT INTO recurrences(tenant_id,project_id,parent_id,template,trigger,next_at,created_by_principal_id,occurrence_count)
				VALUES($1,$2,$2,'{}','{"kind":"time","rrule":"FREQ=WEEKLY;BYDAY=MO","time_of_day":"09:00","timezone":"Europe/Vienna"}',now(),$3,4) RETURNING id::text`, p.TenantID, projectID, p.ID).Scan(&id)
			if err != nil {
				return err
			}
			node := occurrence
			if i == 0 {
				source = id
			} else {
				hiddenSource, node = id, moved
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO recurrence_occurrences(tenant_id,recurrence_id,occurrence_key,number,scheduled_at,node_id,outcome)
				VALUES($1,$2,'manual:fixture',4,now(),$3,'created')`, p.TenantID, id, node.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(who tenant.Principal, retired bool, hiddenVisible bool) {
		t.Helper()
		status, body := call(t, &who, http.MethodGet, "/api/nodes?within="+root.ID+"&limit=4", "")
		page := decode[nodePage](t, status, body, http.StatusOK)
		if len(page.Items) != 4 {
			t.Fatalf("bounded page lost fixtures: %s", body)
		}
		for _, item := range page.Items {
			status, body := call(t, &who, http.MethodGet, "/api/nodes/"+item.ID, "")
			node := decode[nodeJSON](t, status, body, http.StatusOK)
			if string(mustMarshal(t, node.Recurrence)) != string(mustMarshal(t, item.Recurrence)) {
				t.Fatalf("GET and list disagree on %s", item.ID)
			}
			keyStatus, keyBody := call(t, &who, http.MethodGet, "/api/node-keys/"+item.Key, "")
			byKey := decode[nodeJSON](t, keyStatus, keyBody, http.StatusOK)
			if string(mustMarshal(t, byKey.Recurrence)) != string(mustMarshal(t, item.Recurrence)) {
				t.Fatalf("key GET and list disagree on %s", item.ID)
			}
			switch item.ID {
			case occurrence.ID:
				if item.Recurrence == nil || item.Recurrence.ID != source || item.Recurrence.ProjectID != root.ID || item.Recurrence.ProjectKey != root.Key || item.Recurrence.Number != 4 || item.Recurrence.Retired != retired || !strings.Contains(string(item.Recurrence.Trigger), "FREQ=WEEKLY;BYDAY=MO") {
					t.Fatalf("wrong authoritative provenance: %+v", item.Recurrence)
				}
			case moved.ID:
				if hiddenVisible {
					if item.Recurrence == nil || item.Recurrence.ID != hiddenSource || item.Recurrence.ProjectID != hidden.ID || item.Recurrence.ProjectKey != hidden.Key {
						t.Fatalf("moved occurrence lost source project: %+v", item.Recurrence)
					}
				} else if item.Recurrence != nil {
					t.Fatal("hidden recurrence leaked through its moved ticket")
				}
			case ordinary.ID, fake.ID:
				if item.Recurrence != nil || strings.Contains(string(body), `"recurrence":`) {
					t.Fatal("ordinary or spoofed ticket received a marker")
				}
			default:
				t.Fatal("unexpected fixture")
			}
		}
	}
	check(p, false, true)
	guest := tenant.Principal{TenantID: p.TenantID, Kind: tenant.Person, Name: "Project reader"}
	if err := adminPool.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Project reader') RETURNING id::text`, p.TenantID).Scan(&guest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, p.TenantID, guest.ID, root.ID); err != nil {
		t.Fatal(err)
	}
	check(guest, false, false)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE recurrences SET retired_at=now(),paused=true WHERE id=$1`, source)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check(p, true, true)
	check(guest, true, false)
	other := addPrincipal(t, "other-recurrence-tenant")
	status, body := call(t, &other, http.MethodGet, "/api/nodes/"+occurrence.ID, "")
	if status != http.StatusNotFound || strings.Contains(string(body), source) {
		t.Fatalf("tenant isolation failed: %d %s", status, body)
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNodeRecurrenceBounds(t *testing.T) {
	if _, err := loadNodeRecurrences(context.Background(), nil, make([]string, 501)); err == nil || !strings.Contains(err.Error(), "exceeds 500") {
		t.Fatalf("input must be rejected before database work: %v", err)
	}
	if empty, err := loadNodeRecurrences(context.Background(), nil, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty page: %v %v", empty, err)
	}
}

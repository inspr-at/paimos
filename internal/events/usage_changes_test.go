// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestLateUsageReportHasAuthorizedNodeHint(t *testing.T) {
	d, reader, foreign := fixture(t)
	dbtest.BindRole(t, d, reader.TenantID, reader.ID, "admin")
	var project, ticket, session string
	var reported Event
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, reader.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,title,key,fields)
 SELECT $1,id,'Project','LATE-1','{}' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, reader.TenantID).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,title,key,parent_id,fields)
 SELECT $1,id,'Leaf','LATE-2',$2,'{}' FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING id::text`, reader.TenantID, project).Scan(&ticket); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO harness_sessions
 (tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase,stopped_at,stop_reason)
 VALUES($1,$2,$3,$4,'codex','fixture','unmanaged','worker','ship',decode('01','hex'),decode('02','hex'),'stopped',now(),'completed') RETURNING id::text`, reader.TenantID, project, reader.ID, ticket).Scan(&session); err != nil {
			return err
		}
		// Matches the usage reporter payload: usage identity only, no ticket id.
		var err error
		reported, err = Append(t.Context(), tx, reader, Change{NodeID: &project, Type: "harness.usage_reported", After: map[string]any{"id": "usage-row", "session_id": session, "input_tokens": 100, "output_tokens": 20}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []NodeChange{{ID: ticket, ProjectID: &project, Change: "updated", Fields: []string{"estimate", "planning"}}}
	m := New(d.App).(*module)
	page, err := m.read(t.Context(), reader, "", reported.ID-1, 50)
	if err != nil || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0].NodeChanges, want) {
		t.Fatalf("late report history hint: %+v, %v; want %+v", page.Items, err, want)
	}
	// A page containing normal node changes must retain the same usage project
	// hint; the ordinary node-project attachment must not overwrite it.
	mixed := []Event{reported, {Type: "node.updated", NodeID: &ticket,
		Before: json.RawMessage(`{"id":"` + ticket + `","title":"Before"}`),
		After:  json.RawMessage(`{"id":"` + ticket + `","title":"After"}`)}}
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), reader), d.App, reader.TenantID, func(tx pgx.Tx) error {
		return attachNodeChanges(t.Context(), tx, reader.TenantID, mixed)
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mixed[0].NodeChanges, want) || len(mixed[1].NodeChanges) != 1 || mixed[1].NodeChanges[0].ProjectID == nil || *mixed[1].NodeChanges[0].ProjectID != project {
		t.Fatalf("mixed page lost project hints: %+v", mixed)
	}
	// Durable replay delivers the hint without a later heartbeat or node write.
	srv := testServer(t, d, reader, foreign)
	response, scanner := stream(t, srv, "")
	got := nextEvent(t, scanner)
	response.Body.Close()
	if got.ID != reported.ID || got.Type != "harness.usage_reported" || !reflect.DeepEqual(got.NodeChanges, want) {
		t.Fatalf("late report SSE hint: %+v; want %+v", got, want)
	}

	// Even if the event is available, lookup must obey the current node/session
	// visibility. A raw snapshot cannot introduce an unreadable binding.
	hidden := reader
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, reader.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','No projects') RETURNING id::text`, reader.TenantID).Scan(&hidden.ID)
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []tenant.Principal{hidden, foreign} {
		items := []Event{reported}
		if err := db.InTenant(tenant.WithPrincipal(t.Context(), p), d.App, p.TenantID, func(tx pgx.Tx) error {
			return attachNodeChanges(t.Context(), tx, p.TenantID, items)
		}); err != nil {
			t.Fatal(err)
		}
		if len(items[0].NodeChanges) != 0 {
			t.Fatalf("unreadable binding leaked: %+v", items[0].NodeChanges)
		}
	}
	for _, raw := range []string{`{"session_id":"not-a-uuid"}`, `{}`, `null`} {
		items := []Event{{Type: "harness.usage_reported", NodeID: &project, After: json.RawMessage(raw)}}
		if err := db.InTenant(tenant.WithPrincipal(t.Context(), reader), d.App, reader.TenantID, func(tx pgx.Tx) error {
			return attachNodeChanges(t.Context(), tx, reader.TenantID, items)
		}); err != nil || len(items[0].NodeChanges) != 0 {
			t.Fatalf("malformed report must not invent a node: %v %+v", err, items)
		}
	}
}

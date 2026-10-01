// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestSessionLabelsAndLiveHierarchy(t *testing.T) {
	f := fixture(t) // Migrations and handlers use the NOBYPASSRLS schema owner.
	base := "/api/projects/" + f.project + "/harness-sessions"
	registration := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "coordinator", "harness_session_ref": "parent-reference-" + uid(), "worker_lease": "parent-lease-" + uid()}
	w := f.call(f.agent, "POST", base, registration, "")
	expect(t, w, 201)
	parent := decode(t, w)
	if parent["display_label"] != nil {
		t.Fatal("unlabeled registration must stay compatible")
	}

	// The existing live channel must deliver new sessions after connection,
	// rather than merely replaying events when the page is manually reloaded.
	events.New(f.db.App).Mount(f.mux)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.person)))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	received := make(chan events.Event, 16)
	go func() {
		defer close(received)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				var event events.Event
				if json.Unmarshal([]byte(data), &event) == nil {
					select {
					case received <- event:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	await := func(kind, id string) {
		t.Helper()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case event, ok := <-received:
				if !ok {
					t.Fatal("live stream closed")
				}
				var after map[string]any
				if err := json.Unmarshal(event.After, &after); err != nil {
					t.Fatal(err)
				}
				if event.Type == kind && after["id"] == id {
					if after["ticket_node_id"] != f.ticket {
						t.Fatal("live session event lost its bound ticket")
					}
					if after["display_label"] != "AC4 · hierarchy" || after["parent_harness_session_id"] != parent["id"] {
						t.Fatal("live event lost session metadata")
					}
					return
				}
			case <-deadline.C:
				t.Fatalf("%s did not arrive within seconds", kind)
			}
		}
	}
	lease := "child-lease-" + uid()
	registration["role"] = "worker"
	registration["parent_harness_session_id"] = parent["id"]
	registration["ticket_node_id"] = f.ticket
	registration["work_shape"] = "ship"
	registration["harness_session_ref"] = "child-reference-" + uid()
	registration["worker_lease"] = lease
	registration["display_label"] = "  AC4 · hierarchy  "
	w = f.call(f.agent, "POST", base, registration, "")
	expect(t, w, 201)
	child := decode(t, w)
	id := child["id"].(string)
	if child["display_label"] != "AC4 · hierarchy" || child["agent_principal_id"] != parent["agent_principal_id"] {
		t.Fatal("per-session label not persisted independently of principal")
	}
	await("harness.registered", id)
	w = f.call(f.agent, "POST", base, registration, "")
	expect(t, w, 201)
	if decode(t, w)["id"] != id {
		t.Fatal("exact replay created another worker")
	}
	registration["display_label"] = "Another worker"
	expect(t, f.call(f.agent, "POST", base, registration, ""), 409)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.registered' AND after->>'id'=$1`, id).Scan(&n)
		if n != 1 {
			t.Errorf("replay/conflict appended %d registration events", n)
		}
		return err
	})
	w = f.call(f.person, "GET", "/api/harness-sessions?ticket="+f.ticket, nil, "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"display_label":"AC4 · hierarchy"`) {
		t.Fatal("list lost label")
	}
	w = f.call(f.foreign, "GET", "/api/harness-sessions", nil, "")
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), id) {
		t.Fatal("session leaked across tenants")
	}
	path := base + "/" + id
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "process_exited"}, lease), 200)
	await("harness.stopped", id)
}

func TestRegistrationLabelValidation(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	for _, tc := range []struct {
		label  string
		status int
		want   any
	}{
		{"", 201, nil}, {"   ", 201, nil}, {"  worker  ", 201, "worker"},
		{strings.Repeat("界", 128), 201, strings.Repeat("界", 128)},
		{strings.Repeat("界", 129), 400, nil}, {"worker\n", 400, nil}, {"worker\tname", 400, nil},
	} {
		body := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "label-reference-" + uid(), "worker_lease": "label-lease-" + uid(), "display_label": tc.label}
		w := f.call(f.agent, "POST", base, body, "")
		expect(t, w, tc.status)
		if tc.status == 201 && decode(t, w)["display_label"] != tc.want {
			t.Fatal("label normalization mismatch")
		}
	}
}

func TestLabelMigrationPreservesExistingGenerationsAsAppOwner(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	tenants := []string{uid(), uid()}
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "0864_harness_display_label.sql" {
			return nil
		}
		for _, tenantID := range tenants {
			if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1::uuid,$1::text,'AC4 migration')`, tenantID); err != nil {
					return err
				}
				var agent, project string
				if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','worker') RETURNING id::text`, tenantID).Scan(&agent); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'AC4-1','Migration project' FROM node_kinds WHERE slug='project' RETURNING id::text`, tenantID).Scan(&project); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest) VALUES($1,$2,$3,'codex','local','unmanaged','worker',$4,$5)`, tenantID, project, agent, []byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)))
				return err
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tenantID := range tenants {
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
			var total, unlabeled int
			if err := tx.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE display_label IS NULL) FROM harness_sessions`).Scan(&total, &unlabeled); err != nil {
				return err
			}
			if total != 1 || unlabeled != 1 {
				t.Fatalf("migration lost old generation or tenant isolation: total=%d unlabeled=%d", total, unlabeled)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestAutopilotUndoVisibleToProjectReaderWithUnchangedParent(t *testing.T) {
	f := setup(t)
	parent := f.add("AUT-2", "work", "in_progress", 0, nil)
	leaf := f.add("AUT-3", "work", "in_progress", 5, nil)
	sibling := f.add("AUT-4", "work", "in_progress", 0, nil)
	hiddenProject := f.add("AUT-20", "project", "open", 0, nil)
	reader := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Person}
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=ANY($2::uuid[])`, parent, []string{leaf, sibling}); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Project reader') RETURNING id::text`, reader.TenantID).Scan(&reader.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='viewer'`, reader.TenantID, reader.ID, f.project)
		return err
	})
	dbtest.EnableWorkParentStatus(t, f.d, f.p.TenantID)
	before := f.state(parent)
	f.run(f.now)
	if f.state(leaf).State != "open" || f.state(sibling).State != "in_progress" {
		t.Fatal("fixture must reopen only the stale leaf")
	}
	changes := f.changes(leaf)
	if len(changes) != 1 {
		t.Fatalf("autopilot changes: %+v", changes)
	}
	response := f.call(f.p, "POST", fmt.Sprintf("/api/events/%d/undo", changes[0].EventID), "", 201)
	var undone events.Event
	if err := json.Unmarshal(response.Body.Bytes(), &undone); err != nil {
		t.Fatal(err)
	}
	if undone.Type != "status_autopilot.undone" || undone.ActorPrincipalID != f.p.ID || undone.ActorPrincipalID == reader.ID || f.state(leaf).State != "in_progress" {
		t.Fatalf("real person Undo fixture: %+v", undone)
	}
	after := f.state(parent)
	if after.State != before.State || !after.Updated.Equal(before.Updated) {
		t.Fatal("parent status/revision must stay unchanged through change and Undo")
	}
	f.tx(func(tx pgx.Tx) error {
		// Neither unknown domains, hidden targets, nor hidden snapshot references
		// become visible by admitting the one public Undo event type.
		for _, c := range []events.Change{
			{NodeID: &leaf, Type: "status_autopilot.private", After: map[string]any{"id": leaf}},
			{NodeID: &hiddenProject, Type: "status_autopilot.undone", After: map[string]any{"id": hiddenProject}},
			{NodeID: &leaf, Type: "status_autopilot.undone", After: map[string]any{"id": leaf, "fields": map[string]any{"dependency": hiddenProject}}},
		} {
			if _, err := events.Append(t.Context(), tx, f.p, c); err != nil {
				return err
			}
		}
		return nil
	})
	assertUndo := func(t *testing.T, e events.Event) {
		t.Helper()
		if e.ID != undone.ID || e.Type != undone.Type || e.ActorPrincipalID != f.p.ID || e.UndoOf == nil || *e.UndoOf != changes[0].EventID {
			t.Fatalf("wrong Undo envelope: %+v", e)
		}
		if len(e.NodeChanges) != 1 || e.NodeChanges[0].ID != leaf || e.NodeChanges[0].ProjectID == nil || *e.NodeChanges[0].ProjectID != f.project || !slices.Contains(e.NodeChanges[0].Fields, "state") {
			t.Fatalf("Undo missing authorized leaf/project hint: %+v", e.NodeChanges)
		}
	}
	t.Run("history", func(t *testing.T) {
		response := f.call(reader, "GET", fmt.Sprintf("/api/events?after=%d", undone.ID-1), "", 200)
		var page struct {
			Items []events.Event `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("project history must contain exactly real Undo, with hidden references excluded: %s", response.Body)
		}
		assertUndo(t, page.Items[0])
	})
	t.Run("SSE", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.h.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), reader)))
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/events/stream?after=%d", server.URL, undone.ID-1), nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("SSE status %d", res.StatusCode)
		}
		scanner := bufio.NewScanner(res.Body)
		for scanner.Scan() {
			data, ok := strings.CutPrefix(scanner.Text(), "data: ")
			if !ok {
				continue
			}
			var event events.Event
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "" {
				continue
			} // stream.ready is not a durable event
			assertUndo(t, event)
			return
		}
		t.Fatalf("SSE did not replay another person's project-visible Undo: %v", scanner.Err())
	})
}

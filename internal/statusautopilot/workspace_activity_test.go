// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Main risk: paging must preserve current visibility and Undo must reject a
// changed ticket or authority, even after the page offered it. Timestamps are
// injected, including a tie and append order different from history order.
func TestWorkspaceActivityPagingAndCurrentUndoAuthority(t *testing.T) {
	f := setup(t)
	ids := []string{f.add("AUT-2", "work", "open", 1, nil), f.add("AUT-3", "work", "open", 1, nil), f.add("AUT-4", "work", "open", 1, nil)}
	hiddenProject := f.add("AUT-10", "project", "open", 1, nil)
	hiddenNode := f.add("AUT-11", "work", "open", 1, nil)
	reader := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Person}
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=$2`, hiddenProject, hiddenNode); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Workspace activity fixture' WHERE id=ANY($1::uuid[])`, append(ids, hiddenNode)); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Project reader') RETURNING id::text`, reader.TenantID).Scan(&reader.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='viewer'`, reader.TenantID, reader.ID, f.project)
		return err
	})
	tied := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	older := tied.Add(-time.Hour)
	eventIDs := []int64{}
	f.tx(func(tx pgx.Tx) error {
		for i, id := range append(ids, hiddenNode) {
			var after []byte
			if err := tx.QueryRow(t.Context(), `SELECT to_jsonb(n) FROM nodes n WHERE id=$1`, id).Scan(&after); err != nil {
				return err
			}
			var before map[string]any
			if err := json.Unmarshal(after, &before); err != nil {
				return err
			}
			before["state"] = "in_progress"
			at := tied
			if i == 1 {
				at = older
			}
			event, err := events.Append(t.Context(), tx, f.p, events.Change{NodeID: &id, Type: Changed, Before: before, After: json.RawMessage(after), At: &at, Metadata: json.RawMessage(`{"rule":"progress","reason":"No work; literal %_ reason"}`)})
			if err != nil {
				return err
			}
			eventIDs = append(eventIDs, event.ID)
		}
		// The node is visible, but quote metadata and hidden referenced nodes
		// must still be excluded. Do not weaken the existing log RLS policy.
		for _, change := range []events.Change{
			{NodeID: &ids[0], Type: "quote.updated", After: map[string]any{"title": "Workspace activity fixture private quote"}},
			{NodeID: &ids[0], Type: Changed, After: map[string]any{"id": ids[0], "fields": map[string]any{"dependency": hiddenNode}}, Metadata: json.RawMessage(`{"rule":"progress","reason":"hidden reference"}`)},
		} {
			if _, err := events.Append(t.Context(), tx, f.p, change); err != nil {
				return err
			}
		}
		return nil
	})
	type row struct {
		EventID          int64      `json:"event_id"`
		NodeID           string     `json:"node_id"`
		Revision         *time.Time `json:"revision"`
		Undoable, Undone bool
		ChangedSince     bool `json:"changed_since"`
	}
	type page struct {
		Items      []row
		NextCursor *string `json:"next_cursor"`
	}
	read := func(p tenant.Principal, query string) page {
		t.Helper()
		response := f.call(p, "GET", "/api/events/activity?"+query, "", 200)
		if strings.Contains(response.Body.String(), `"before"`) || strings.Contains(response.Body.String(), `"after"`) {
			t.Fatal("Activity returned raw snapshots")
		}
		var got page
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	query := "view=automatic&rule=progress&q=literal+%25_&project_id=" + f.project + "&limit=1"
	first := read(reader, query)
	if len(first.Items) != 1 || first.Items[0].EventID != eventIDs[2] || first.NextCursor == nil || first.Items[0].Undoable {
		t.Fatalf("wrong visible first page/permissions: %+v", first)
	}
	next := read(reader, query+"&cursor="+url.QueryEscape(*first.NextCursor))
	if len(next.Items) != 1 || next.Items[0].EventID != eventIDs[0] || next.NextCursor == nil {
		t.Fatalf("tie skipped or repeated: %+v", next)
	}
	last := read(reader, query+"&cursor="+url.QueryEscape(*next.NextCursor))
	if len(last.Items) != 1 || last.Items[0].EventID != eventIDs[1] || last.NextCursor != nil {
		t.Fatalf("timestamp order/exhaustion: %+v", last)
	}
	visible := read(reader, "view=automatic&q=Workspace+activity+fixture")
	if len(visible.Items) != 3 {
		t.Fatalf("hidden project/reference leaked: %+v", visible)
	}
	people := read(reader, "view=people&q=Workspace+activity+fixture&rule=progress")
	agents := read(reader, "view=agents&q=Workspace+activity+fixture&rule=progress")
	if len(people.Items) != 3 || len(agents.Items) != 0 {
		t.Fatalf("actor views misclassified: people=%+v agents=%+v", people, agents)
	}
	owner := read(f.p, query)
	if !owner.Items[0].Undoable || owner.Items[0].Revision == nil {
		t.Fatalf("unchanged automatic move must offer revision-bound Undo: %+v", owner)
	}
	// Revoke after the page was read. The final write must re-check authority.
	dbtest.BindRole(t, f.d, f.p.TenantID, f.p.ID, "viewer")
	f.call(f.p, "POST", fmt.Sprintf("/api/events/%d/undo", eventIDs[2]), "", 403)
	if f.state(ids[2]).State != "open" || read(f.p, query).Items[0].Undoable {
		t.Fatal("revoked authority mutated the ticket or still offered Undo")
	}
	dbtest.BindRole(t, f.d, f.p.TenantID, f.p.ID, "owner")
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET title=title||' edited',updated_at=updated_at+interval '1 second' WHERE id=$1`, ids[2])
		return err
	})
	f.call(f.p, "POST", fmt.Sprintf("/api/events/%d/undo", eventIDs[2]), "", 409)
	stale := read(f.p, query).Items[0]
	if stale.Undoable || !stale.ChangedSince || f.state(ids[2]).State != "open" {
		t.Fatalf("stale Undo was hidden or applied: %+v", stale)
	}
	f.call(f.p, "POST", fmt.Sprintf("/api/events/%d/undo", eventIDs[0]), "", 201)
	if f.state(ids[0]).State != "in_progress" {
		t.Fatal("successful Undo did not restore the recorded prior state")
	}
	undone := read(f.p, "view=automatic&rule=progress&q=literal+%25_&project_id="+f.project+"&limit=100")
	if len(undone.Items) != 3 || !undone.Items[1].Undone || undone.Items[1].Undoable {
		t.Fatalf("Undo result not reflected: %+v", undone)
	}
}

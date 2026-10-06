// SPDX-License-Identifier: AGPL-3.0-only
package releases_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/jackc/pgx/v5"
)

// This integration test lives outside releases: nodes uses workqueue's
// autopilot projection, which in turn uses releases for attention membership.
func TestLeafShapeChangesPreserveCompleteProjectionAndUndo(t *testing.T) {
	f := releases.NodeUndoFixtureForTest(t)
	former := f.Existing("work", f.Feature, "Estimated access change", "open")
	releases.CheckMembershipForTest(t, f.AddExisting([]string{f.Feature}, 1, false))
	f.Tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE journey_tickets SET feature_node_id=$2,walker_position=17,source='requirements',estimated_hours=12.5,access_change=true,scope_revision_required=false WHERE ticket_node_id=$1`, former, f.Feature)
		return err
	})
	var original []byte
	f.Tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT to_jsonb(t) FROM journey_tickets t WHERE ticket_node_id=$1`, former).Scan(&original)
	})
	child := f.Existing("work", former, "Temporary child", "open")
	nodes.New(f.DB.App, nil).Mount(f.Mux)
	events.New(f.DB.App, events.WithUndoHandlers(nodes.UndoHandlers())).Mount(f.Mux)
	assertProjection := func() {
		t.Helper()
		f.Tx(func(tx pgx.Tx) error {
			var actual []byte
			var access bool
			if err := tx.QueryRow(t.Context(), `SELECT to_jsonb(t),r.access_required FROM journey_tickets t JOIN journey_releases r ON r.release_node_id=t.release_node_id WHERE t.ticket_node_id=$1`, former).Scan(&actual, &access); err != nil {
				return err
			}
			if string(actual) != string(original) || !access {
				t.Errorf("projection lost across shape change: original=%s actual=%s access_required=%v", original, actual, access)
			}
			return nil
		})
	}
	// Exercise the actual move and compensating-event paths, twice, rather
	// than simulating Undo with another fixture update.
	for range 2 {
		w := f.Request(f.Person, http.MethodPost, "/api/nodes/"+child+"/move", fmt.Sprintf(`{"parent_id":%q}`, f.Project))
		if w.Code != 200 {
			t.Fatalf("move: %d %s", w.Code, w.Body.String())
		}
		assertProjection()
		var eventID int64
		f.Tx(func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE node_id=$1 AND type='node.moved' AND undo_of IS NULL ORDER BY id DESC LIMIT 1`, child).Scan(&eventID)
		})
		if w := f.Request(f.Person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", eventID), ""); w.Code != 201 {
			t.Fatalf("undo move: %d %s", w.Code, w.Body.String())
		}
		f.Tx(func(tx pgx.Tx) error {
			var count int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_tickets WHERE ticket_node_id=$1`, former).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Error("Undo retained a parent as a live member")
			}
			return nil
		})
	}
	// Undoing a move into the parent must restore the complete leaf projection
	// too, including the release's access-review requirement.
	f.Tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, child, f.Project)
		return err
	})
	assertProjection()
	w := f.Request(f.Person, http.MethodPost, "/api/nodes/"+child+"/move", fmt.Sprintf(`{"parent_id":%q}`, former))
	if w.Code != 200 {
		t.Fatalf("move back: %d %s", w.Code, w.Body.String())
	}
	var eventID int64
	f.Tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE node_id=$1 AND type='node.moved' AND undo_of IS NULL ORDER BY id DESC LIMIT 1`, child).Scan(&eventID)
	})
	if w := f.Request(f.Person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", eventID), ""); w.Code != 201 {
		t.Fatalf("undo move into parent: %d %s", w.Code, w.Body.String())
	}
	assertProjection()
	f.Tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, child, former); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, child)
		return err
	})
	assertProjection()
}

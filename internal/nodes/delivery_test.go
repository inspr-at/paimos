// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/jackc/pgx/v5"
)

func adoptNodeFixture(t *testing.T, pTenant, pActor, project, release string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, pTenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by,next_sequence) VALUES($1,$2,$3,2)`, pTenant, project, pActor); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank,origin) VALUES($1,$2,$3,1,'B','adopted_planned')`, pTenant, project, release)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestAdoptedReleaseGenericWritersAndPreAdoptionBulkUndoAreGuarded(t *testing.T) {
	p := newPrincipal(t, "release-node-guard")
	projectKind, releaseKind := kindBySlug(t, p, "project"), kindBySlug(t, p, "release")
	project := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Project"}`)
	release := mustNode(t, p, `{"kind_id":"`+releaseKind.ID+`","parent_id":"`+project.ID+`","title":"Release 1"}`)
	code, raw := call(t, &p, http.MethodPost, "/api/nodes/bulk", `{"ids":["`+release.ID+`"],"state":"archived"}`)
	batch := decode[bulkResult](t, code, raw, 200)
	if batch.EventID == nil {
		t.Fatal("pre-adoption fixture did not mutate")
	}
	adoptNodeFixture(t, p.TenantID, p.ID, project.ID, release.ID)
	for _, body := range []string{`{"state":"open"}`, `{"fields":{}}`, `{"estimate_hours":3}`, `{"kind_id":"` + releaseKind.ID + `"}`} {
		code, raw = call(t, &p, http.MethodPatch, "/api/nodes/"+release.ID, body)
		if code != 409 || !strings.Contains(string(raw), "release API") {
			t.Fatalf("generic release writer: %d %s", code, raw)
		}
	}
	code, raw = call(t, &p, http.MethodPatch, "/api/nodes/"+release.ID, `{"title":"Human title","body":"Human body"}`)
	if code != 200 {
		t.Fatalf("person title/body patch: %d %s", code, raw)
	}
	code, raw = call(t, &p, http.MethodDelete, "/api/nodes/"+release.ID, "")
	if code != 409 {
		t.Fatalf("generic release delete: %d %s", code, raw)
	}
	code, raw = call(t, &p, http.MethodPost, "/api/nodes/bulk", `{"ids":["`+release.ID+`"],"state":"open"}`)
	skipped := decode[bulkResult](t, code, raw, 200)
	if len(skipped.Skipped) != 1 || skipped.Skipped[0].Code != "release_api" || len(skipped.Items) != 0 {
		t.Fatal("bulk did not return release_api")
	}
	code, raw = callAs(t, events.New(appPool, events.WithUndoHandlers(UndoHandlers())), &p, http.MethodPost, "/api/events/"+strconv.FormatInt(*batch.EventID, 10)+"/undo", "")
	if code != 409 {
		t.Fatalf("pre-adoption bulk undo: %d %s", code, raw)
	}
}

func TestShipsMoveBlocksReleasedDescendantAndBacklogUndoReturnsTail(t *testing.T) {
	p := newPrincipal(t, "ships-move")
	projectKind, releaseKind, epicKind, ticketKind := kindBySlug(t, p, "project"), kindBySlug(t, p, "release"), kindBySlug(t, p, "epic"), kindBySlug(t, p, "ticket")
	source := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Source","fields":{"project_key":"SRC"}}`)
	target := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Target","fields":{"project_key":"DST"}}`)
	release := mustNode(t, p, `{"kind_id":"`+releaseKind.ID+`","title":"Release","parent_id":"`+source.ID+`"}`)
	epic := mustNode(t, p, `{"kind_id":"`+epicKind.ID+`","title":"Epic","parent_id":"`+source.ID+`"}`)
	ticket := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","title":"Ticket","parent_id":"`+epic.ID+`"}`)
	adoptNodeFixture(t, p.TenantID, p.ID, source.ID, release.ID)
	seed := func(q string, args ...any) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error { _, err := tx.Exec(t.Context(), q, args...); return err }); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO ships_in(tenant_id,project_node_id,item_node_id,release_node_id,rank,source,placed_by) VALUES($1,$2,$3,$4,'B','seed',$5)`, p.TenantID, source.ID, ticket.ID, release.ID, p.ID)
	code, raw := call(t, &p, http.MethodPost, "/api/kinds", `{"slug":"story","label":"Story","short_prefix":"STY","icon":"ticket","field_schema":{"type":"object","issue_family":true}}`)
	decode[kindJSON](t, code, raw, http.StatusCreated)
	code, raw = call(t, &p, http.MethodPost, "/api/nodes/"+ticket.ID+"/convert", `{"to_kind":"story"}`)
	if code != http.StatusConflict || !strings.Contains(string(raw), "remove the item from its release") {
		t.Fatalf("placed item converted out of permitted release kinds: %d %s", code, raw)
	}
	code, raw = call(t, &p, http.MethodPost, "/api/nodes/"+ticket.ID+"/convert", `{"to_kind":"task"}`)
	decode[nodeJSON](t, code, raw, http.StatusOK)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if err := lockTree(t.Context(), tx); err != nil {
			return err
		}
		_, err := shipsInBeforeMove(t.Context(), tx, epic.ID, &target.ID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), ticket.ID) {
		t.Fatal("subtree move omitted placed descendant", err)
	}
	// Backlog-only node moves clear rank; undo keeps the source tail unranked.
	seed(`DELETE FROM ships_in WHERE item_node_id=$1`, ticket.ID)
	seed(`UPDATE nodes SET parent_id=$2 WHERE id=$1`, ticket.ID, source.ID)
	seed(`INSERT INTO ships_in(tenant_id,project_node_id,item_node_id,rank,source,placed_by) VALUES($1,$2,$3,'B','seed',$4)`, p.TenantID, source.ID, ticket.ID, p.ID)
	code, raw = call(t, &p, http.MethodPost, "/api/nodes/"+ticket.ID+"/project-move", `{"project_id":"`+target.ID+`"}`)
	moved := decode[projectMoveResult](t, code, raw, 200)
	if len(moved.Notes) == 0 {
		t.Fatal("rank-loss warning missing")
	}
	var eventID int64
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE node_id=$1 AND type='node.project_moved' ORDER BY id DESC LIMIT 1`, ticket.ID).Scan(&eventID)
	}); err != nil {
		t.Fatal(err)
	}
	code, raw = callAs(t, events.New(appPool, events.WithUndoHandlers(UndoHandlers())), &p, http.MethodPost, "/api/events/"+strconv.FormatInt(eventID, 10)+"/undo", "")
	if code != 201 || !strings.Contains(string(raw), "tail") {
		t.Fatalf("move undo warning: %d %s", code, raw)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM ships_in WHERE item_node_id=$1`, ticket.ID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("undo recreated removed rank")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

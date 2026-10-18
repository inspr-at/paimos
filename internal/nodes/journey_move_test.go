// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
)

func TestPlainMoveTransfersJourneyAndUndoRestoresIt(t *testing.T) {
	p := newPrincipal(t, "plain-journey-move")
	projectKind := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "work")
	source := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Source"}`)
	target := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Target"}`)
	noJourney := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"No journey"}`)
	within := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","title":"Sibling","parent_id":"`+source.ID+`"}`)
	issue := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","title":"Move me","parent_id":"`+source.ID+`"}`)
	seed := func() error {
		return db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id)
			 VALUES($1::uuid,$2::uuid),($1::uuid,$3::uuid)`, p.TenantID, source.ID, target.ID); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,walker_position,source,access_change,estimated_hours)
			 VALUES($1::uuid,$2::uuid,$3::uuid,7,'requirements',true,2.50)`, p.TenantID, issue.ID, source.ID)
			return err
		})
	}
	if err := seed(); err != nil {
		t.Fatal(err)
	}
	read := func() (*journeyMembership, int64, int64) {
		t.Helper()
		var membership *journeyMembership
		var sourceRev, targetRev int64
		err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
			var err error
			membership, err = loadJourneyMembership(t.Context(), tx, issue.ID)
			if err != nil {
				return err
			}
			return tx.QueryRow(t.Context(), `SELECT s.revision,t.revision FROM journey_projects s,journey_projects t
			 WHERE s.project_node_id=$1::uuid AND t.project_node_id=$2::uuid`, source.ID, target.ID).Scan(&sourceRev, &targetRev)
		})
		if err != nil {
			t.Fatal(err)
		}
		return membership, sourceRev, targetRev
	}
	before, sourceRev, targetRev := read()
	status, raw := call(t, &p, http.MethodPost, "/api/nodes/"+issue.ID+"/move", `{"parent_id":"`+within.ID+`"}`)
	decode[nodeJSON](t, status, raw, http.StatusOK)
	unchanged, s, d := read()
	if !reflect.DeepEqual(unchanged, before) || s != sourceRev || d != targetRev {
		t.Fatalf("within-project move changed journey: %+v, revisions %d/%d", unchanged, s, d)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+issue.ID+"/move", `{"parent_id":"`+target.ID+`"}`)
	moved := decode[nodeJSON](t, status, raw, http.StatusOK)
	if moved.ParentID == nil || *moved.ParentID != target.ID {
		t.Fatalf("moved node: %+v", moved)
	}
	after, s, d := read()
	if after == nil || after.ProjectID != target.ID || after.FeatureID != nil || after.ReleaseID != nil ||
		after.WalkerPosition != 0 || after.Source != "manual" || !after.ScopeRevisionRequired ||
		!after.AccessChange || after.EstimatedHours == nil || *after.EstimatedHours != "2.50" ||
		s != sourceRev+1 || d != targetRev+1 {
		t.Fatalf("transferred journey: %+v, revisions %d/%d", after, s, d)
	}
	var eventID int64
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE type='node.moved' AND node_id=$1::uuid ORDER BY id DESC LIMIT 1`, issue.ID).Scan(&eventID)
	}); err != nil {
		t.Fatal(err)
	}
	status, raw = callAs(t, events.New(appPool, events.WithUndoHandlers(UndoHandlers())), &p,
		http.MethodPost, "/api/events/"+strconv.FormatInt(eventID, 10)+"/undo", "")
	if status != http.StatusCreated {
		t.Fatalf("undo: %d %s", status, raw)
	}
	restored, s, d := read()
	if restored == nil || restored.ProjectID != source.ID || restored.WalkerPosition != before.WalkerPosition ||
		restored.Source != before.Source || restored.ScopeRevisionRequired != before.ScopeRevisionRequired ||
		restored.AccessChange != before.AccessChange || restored.EstimatedHours == nil ||
		*restored.EstimatedHours != *before.EstimatedHours || s != sourceRev+2 || d != targetRev+2 {
		t.Fatalf("restored journey: %+v, revisions %d/%d", restored, s, d)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+issue.ID+"/move", `{"parent_id":"`+noJourney.ID+`"}`)
	decode[nodeJSON](t, status, raw, http.StatusOK)
	removed, _, _ := read()
	if removed != nil {
		t.Fatalf("membership in project without journey: %+v", removed)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE type='node.moved' AND node_id=$1::uuid ORDER BY id DESC LIMIT 1`, issue.ID).Scan(&eventID)
	}); err != nil {
		t.Fatal(err)
	}
	status, raw = callAs(t, events.New(appPool, events.WithUndoHandlers(UndoHandlers())), &p,
		http.MethodPost, "/api/events/"+strconv.FormatInt(eventID, 10)+"/undo", "")
	if status != http.StatusCreated {
		t.Fatalf("undo removal: %d %s", status, raw)
	}
	restored, _, _ = read()
	if restored == nil || restored.ProjectID != source.ID || restored.WalkerPosition != before.WalkerPosition {
		t.Fatalf("restored removed membership: %+v", restored)
	}
}

func TestPlainMoveTransfersDescendantJourneyTickets(t *testing.T) {
	p := newPrincipal(t, "subtree-journey-move")
	projectKind := kindBySlug(t, p, "project")
	epicKind := kindBySlug(t, p, "work")
	ticketKind := kindBySlug(t, p, "work")
	source := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Source"}`)
	target := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Target"}`)
	epic := mustNode(t, p, `{"kind_id":"`+epicKind.ID+`","title":"Epic","parent_id":"`+source.ID+`"}`)
	issue := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","title":"Ticket","parent_id":"`+epic.ID+`"}`)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id)
		 VALUES($1::uuid,$2::uuid),($1::uuid,$3::uuid)`, p.TenantID, source.ID, target.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,walker_position,source)
		 VALUES($1::uuid,$2::uuid,$3::uuid,3,'manual')`, p.TenantID, issue.ID, source.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, raw := call(t, &p, http.MethodPost, "/api/nodes/"+epic.ID+"/move", `{"parent_id":"`+target.ID+`"}`)
	decode[nodeJSON](t, status, raw, http.StatusOK)
	var membership *journeyMembership
	var eventID int64
	read := func() {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
			var err error
			membership, err = loadJourneyMembership(t.Context(), tx, issue.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if membership == nil || membership.ProjectID != target.ID {
		t.Fatalf("descendant membership after move: %+v", membership)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE type='node.moved' AND node_id=$1::uuid ORDER BY id DESC LIMIT 1`, epic.ID).Scan(&eventID)
	}); err != nil {
		t.Fatal(err)
	}
	status, raw = callAs(t, events.New(appPool, events.WithUndoHandlers(UndoHandlers())), &p,
		http.MethodPost, "/api/events/"+strconv.FormatInt(eventID, 10)+"/undo", "")
	if status != http.StatusCreated {
		t.Fatalf("undo: %d %s", status, raw)
	}
	read()
	if membership == nil || membership.ProjectID != source.ID || membership.WalkerPosition != 3 {
		t.Fatalf("descendant membership after undo: %+v", membership)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+epic.ID+"/move", `{"parent_id":"`+target.ID+`"}`)
	decode[nodeJSON](t, status, raw, http.StatusOK)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE type='node.moved' AND node_id=$1::uuid ORDER BY id DESC LIMIT 1`, epic.ID).Scan(&eventID)
	}); err != nil {
		t.Fatal(err)
	}
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/"+issue.ID+"/move", `{"parent_id":"`+target.ID+`"}`)
	decode[nodeJSON](t, status, raw, http.StatusOK)
	status, _ = callAs(t, events.New(appPool, events.WithUndoHandlers(UndoHandlers())), &p,
		http.MethodPost, "/api/events/"+strconv.FormatInt(eventID, 10)+"/undo", "")
	if status != http.StatusConflict {
		t.Fatalf("undo after descendant moved out: %d", status)
	}
	read()
	if membership == nil || membership.ProjectID != target.ID {
		t.Fatalf("refused undo changed descendant membership: %+v", membership)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestProjectMoveAliasAndUndo(t *testing.T) {
	p := newPrincipal(t, "project-move")
	projectKind := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "work")
	source := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Source","fields":{"project_key":"SRC"}}`)
	target := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Target","fields":{"project_key":"DST"}}`)
	issue := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","title":"Move me","parent_id":"`+source.ID+`","key_prefix":"SRC"}`)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id)
		 VALUES($1::uuid,$2::uuid),($1::uuid,$3::uuid)`, p.TenantID, source.ID, target.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,walker_position,source,estimated_hours)
		 VALUES($1::uuid,$2::uuid,$3::uuid,7,'manual',2.5)`, p.TenantID, issue.ID, source.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(appPool, nil).Mount(mux)
	events.New(appPool, events.WithUndoHandlers(UndoHandlers())).Mount(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(tenant.WithPrincipal(t.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := request(http.MethodPost, "/api/nodes/"+issue.ID+"/project-move", `{"project_id":"`+target.ID+`"}`)
	moved := decode[projectMoveResult](t, w.Code, w.Body.Bytes(), http.StatusOK)
	if moved.OldKey != issue.Key || !strings.HasPrefix(moved.NewKey, "DST-") || moved.IssueID != issue.ID || moved.ProjectID != target.ID {
		t.Fatalf("move: %+v", moved)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		membership, err := loadJourneyMembership(t.Context(), tx, issue.ID)
		if err != nil {
			return err
		}
		if membership == nil || membership.ProjectID != target.ID || !membership.ScopeRevisionRequired || membership.EstimatedHours == nil || *membership.EstimatedHours != "2.50" {
			t.Errorf("moved journey membership: %+v", membership)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{issue.Key, moved.NewKey} {
		w = request(http.MethodGet, "/api/node-keys/"+key, "")
		resolved := decode[nodeJSON](t, w.Code, w.Body.Bytes(), http.StatusOK)
		if resolved.ID != issue.ID || resolved.Key != moved.NewKey {
			t.Fatalf("resolve %s: %+v", key, resolved)
		}
	}
	// Reserved aliases cannot be reused by an unrelated node.
	w = request(http.MethodPost, "/api/nodes", `{"kind_id":"`+ticketKind.ID+`","title":"Duplicate","key":"`+issue.Key+`"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("alias reuse status %d: %s", w.Code, w.Body.String())
	}
	var eventID int64
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE type='node.project_moved' AND node_id=$1::uuid ORDER BY id DESC LIMIT 1`, issue.ID).Scan(&eventID)
	}); err != nil {
		t.Fatal(err)
	}
	w = request(http.MethodPost, "/api/events/"+strconv.FormatInt(eventID, 10)+"/undo", "")
	if w.Code != http.StatusCreated {
		t.Fatalf("undo status %d: %s", w.Code, w.Body.String())
	}
	for _, key := range []string{issue.Key, moved.NewKey} {
		w = request(http.MethodGet, "/api/node-keys/"+key, "")
		resolved := decode[nodeJSON](t, w.Code, w.Body.Bytes(), http.StatusOK)
		if resolved.ID != issue.ID || resolved.Key != issue.Key || resolved.ParentID == nil || *resolved.ParentID != source.ID {
			t.Fatalf("undo resolve %s: %+v", key, resolved)
		}
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		membership, err := loadJourneyMembership(t.Context(), tx, issue.ID)
		if err != nil {
			return err
		}
		if membership == nil || membership.ProjectID != source.ID || membership.WalkerPosition != 7 || membership.ScopeRevisionRequired {
			t.Errorf("restored journey membership: %+v", membership)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectMoveRejectsNestedIssues(t *testing.T) {
	p := newPrincipal(t, "project-move-children")
	projectKind := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "work")
	taskKind := kindBySlug(t, p, "work")
	source := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Source","fields":{"project_key":"SRC"}}`)
	target := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Target","fields":{"project_key":"DST"}}`)
	issue := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","title":"Parent","parent_id":"`+source.ID+`","key_prefix":"SRC"}`)
	_ = mustNode(t, p, `{"kind_id":"`+taskKind.ID+`","title":"Child","parent_id":"`+issue.ID+`","key_prefix":"SRC"}`)
	mux := http.NewServeMux()
	New(appPool, nil).Mount(mux)
	r := httptest.NewRequest(http.MethodPost, "/api/nodes/"+issue.ID+"/project-move", strings.NewReader(`{"project_id":"`+target.ID+`"}`)).WithContext(tenant.WithPrincipal(t.Context(), p))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("nested move status %d: %s", w.Code, w.Body.String())
	}
}

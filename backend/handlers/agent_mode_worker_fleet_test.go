// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/workerfleet"
)

func TestAgentModeWorkerFleetStrictQueryAndClosedEmptyContract(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Fleet project", "FLT")

	response := ts.get(t, "/api/agent-mode/worker-fleet/v1", ts.adminCookie)
	assertStatus(t, response, http.StatusOK)
	if response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("cache policy=%q", response.Header.Get("Cache-Control"))
	}
	var snapshot workerfleet.Snapshot
	decode(t, response, &snapshot)
	if snapshot.SchemaVersion != 1 || snapshot.Scope.Kind != "portfolio" || snapshot.Zoom != "10" ||
		snapshot.Band != "overview" || snapshot.SampleLimit != 10 || snapshot.Workers == nil || snapshot.Projects == nil ||
		snapshot.Provenance.Source != "authoritative_database" || snapshot.Provenance.Cache != "none" || snapshot.Provenance.RemoteCache {
		t.Fatalf("empty contract=%+v", snapshot)
	}
	for _, query := range []string{"zoom=", "zoom=0", "zoom=01", "zoom=1.0", "other=1", "zoom=1&zoom=2", "zoom=" + strings.Repeat("9", 65)} {
		invalid := ts.get(t, "/api/agent-mode/worker-fleet/v1?"+query, ts.adminCookie)
		assertStatus(t, invalid, http.StatusBadRequest)
		if invalid.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("invalid cache policy=%q", invalid.Header.Get("Cache-Control"))
		}
	}
	projectResponse := ts.get(t, "/api/agent-mode/projects/"+itoa(projectID)+"/worker-fleet/v1?zoom=1", ts.adminCookie)
	assertStatus(t, projectResponse, http.StatusOK)
	var projectSnapshot workerfleet.Snapshot
	decode(t, projectResponse, &projectSnapshot)
	if projectSnapshot.Scope.Kind != "project" || projectSnapshot.Scope.ProjectID == nil ||
		*projectSnapshot.Scope.ProjectID != projectID || projectSnapshot.SampleLimit != 1 {
		t.Fatalf("project route contract=%+v", projectSnapshot)
	}
}

func TestAgentModeWorkerFleetConcealsUnauthorizedAndMissingProjects(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Concealed fleet", "CFL")
	paths := []string{
		"/api/agent-mode/projects/999999/worker-fleet/v1",
		"/api/agent-mode/projects/" + itoa(projectID) + "/worker-fleet/v1",
	}
	for _, path := range paths {
		missing := ts.get(t, path, ts.externalCookie)
		assertStatus(t, missing, http.StatusNotFound)
		body, _ := io.ReadAll(missing.Body)
		var problem map[string]any
		if err := json.Unmarshal(body, &problem); err != nil || problem["status"] != float64(http.StatusNotFound) {
			t.Fatalf("concealed response path=%s body=%s err=%v", path, body, err)
		}
		if missing.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("concealed cache policy=%q", missing.Header.Get("Cache-Control"))
		}
	}
	missing := ts.get(t, paths[0], ts.adminCookie)
	assertStatus(t, missing, http.StatusNotFound)
}

func TestAgentModeWorkerFleetKeepsStoppedWorkerWhenBoundTicketIsSoftDeleted(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Deleted ticket fleet", "DTF")
	issueResult, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,904,'ticket','Deleted binding','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issueResult.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'stopped-worker')`, projectID); err != nil {
		t.Fatal(err)
	}
	session, _, err := managedharness.NewService(db.DB).Register(context.Background(), managedharness.RegisterInput{
		ProjectID: projectID, AgentName: "stopped-worker", Harness: "codex", Host: "test-host",
		SessionRef: "deleted-ticket-generation", WorkerLease: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ManagementMode: managedharness.ManagementManaged, Role: managedharness.RoleWorker,
		TicketID: &issueID, SteerMode: managedharness.SteerNone,
		Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managedharness.NewService(db.DB).Stop(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE issues SET deleted_at=CURRENT_TIMESTAMP WHERE id=?`, issueID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/agent-mode/worker-fleet/v1?zoom=100",
		"/api/agent-mode/projects/" + itoa(projectID) + "/worker-fleet/v1?zoom=100",
	} {
		response := ts.get(t, path, ts.adminCookie)
		assertStatus(t, response, http.StatusOK)
		var snapshot workerfleet.Snapshot
		decode(t, response, &snapshot)
		if len(snapshot.Workers) != 1 || snapshot.Workers[0].HarnessSessionID != session.ID ||
			snapshot.Workers[0].Ticket == nil || snapshot.Workers[0].Ticket.ID != issueID ||
			snapshot.Workers[0].Ticket.DetailsAvailable || snapshot.Workers[0].DeliveryTrust.Reason != "trust_unavailable" {
			t.Fatalf("soft-deleted ticket fleet path=%s snapshot=%+v", path, snapshot)
		}
	}
}

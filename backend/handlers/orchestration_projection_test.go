// PAIMOS — Copyright (C) 2026 Markus Barta
package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/handlers"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/workerfleet"
)

func orchestrationGet(path, cookie string) *http.Response {
	r := chi.NewRouter()
	r.Route("/api/agent-mode", func(r chi.Router) {
		r.Use(auth.AgentModePrivateNoStore, auth.Middleware, auth.RequireAgentModeInternal, auth.CSRFMiddleware, auth.MustChangePasswordGate)
		handlers.RegisterOrchestrationProjectionRoutes(r)
	})
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Cookie", cookie)
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	return recorder.Result()
}

func orchestrationSnapshot(t *testing.T, path, cookie string) handlers.OrchestrationSnapshotV1 {
	t.Helper()
	response := orchestrationGet(path, cookie)
	assertStatus(t, response, http.StatusOK)
	if response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("missing privacy cache policy")
	}
	var snapshot handlers.OrchestrationSnapshotV1
	decode(t, response, &snapshot)
	return snapshot
}

func orchestrationWorker(t *testing.T, project int64, name, generation, role string, parent *string, harness ...string) models.HarnessSession {
	t.Helper()
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO project_agents(project_id,name) VALUES(?,?)`, project, name); err != nil {
		t.Fatal(err)
	}
	vendor := "codex"
	if len(harness) > 0 {
		vendor = harness[0]
	}
	session, _, err := managedharness.NewService(db.DB).Register(context.Background(), managedharness.RegisterInput{
		ProjectID: project, AgentName: name, Harness: vendor, Host: "fixture-machine", SessionRef: generation,
		WorkerLease: strings.Repeat("A", 43), ManagementMode: managedharness.ManagementManaged, Role: role, ParentSessionID: parent,
		SteerMode: managedharness.SteerNone, Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true},
		Workspace: &models.HarnessWorkspaceProvenance{CanonicalPath: "/fixture/private-workspace", Identity: strings.Repeat("a", 64), Kind: "directory", Mode: "shared"},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err = managedharness.NewService(db.DB).HeartbeatWithActivity(context.Background(), session.ID, managedharness.PhaseWorking, managedharness.ActivityEvidence{Sequence: 1, Kind: "turn_started"})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestOrchestrationEmptyProjectsAndStrictPrivateAPI(t *testing.T) {
	ts := newTestServer(t)
	project := seedBatchProject(t, "Empty project", "EMP")
	path := "/api/agent-mode/orchestration/v1"
	unauthorized := orchestrationGet(path, "")
	assertStatus(t, unauthorized, http.StatusUnauthorized)
	if unauthorized.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("authentication error cache policy")
	}
	unauthorized.Body.Close()
	snapshot := orchestrationSnapshot(t, path, ts.adminCookie)
	if snapshot.SchemaVersion != 1 || snapshot.InstanceRoot.ConfiguredIdentity != nil || snapshot.InstanceRoot.ActiveGeneration.State != "unset" || snapshot.Fleet.SchemaVersion != 2 || len(snapshot.ProjectCoordination) != 1 || snapshot.ProjectCoordination[0].Coordinator.Reason != "no_active_coordinator" {
		t.Fatal("empty contract lost explicit unset project/root")
	}
	for _, query := range []string{"zoom=", "zoom=0", "zoom=01", "zoom=-1", "zoom=1.0", "zoom=1&zoom=2", "other=1", "zoom=" + strings.Repeat("9", 65)} {
		response := orchestrationGet(path+"?"+query, ts.adminCookie)
		assertStatus(t, response, http.StatusBadRequest)
		if response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("invalid query cache policy")
		}
		response.Body.Close()
	}
	for _, scope := range []string{path, fmt.Sprintf("/api/agent-mode/projects/%d/orchestration/v1", project), "/api/agent-mode/projects/999999/orchestration/v1"} {
		response := orchestrationGet(scope, ts.externalCookie)
		assertStatus(t, response, http.StatusNotFound)
		response.Body.Close()
	}
	empty := orchestrationSnapshot(t, fmt.Sprintf("/api/agent-mode/projects/%d/orchestration/v1?zoom=1", project), ts.memberCookie)
	if empty.CoordinationBounds.TotalProjects != 1 || len(empty.ProjectCoordination) != 1 || empty.Fleet.Totals.Workers != 0 {
		t.Fatal("empty project scope missing")
	}
}

func TestOrchestrationRootGenerationHierarchyAndImmutableHistory(t *testing.T) {
	ts := newTestServer(t)
	promoteToSuperAdmin(t, "admin")
	first, _ := seedOrchestratorAgent(t, ts, "ONE", "root")
	second, _ := seedOrchestratorAgent(t, ts, "TWO", "next-root")
	setOrchestrator(t, ts, 0, first, "root", "Root")
	configured := orchestrationSnapshot(t, "/api/agent-mode/orchestration/v1", ts.adminCookie)
	if configured.InstanceRoot.ActiveGeneration.State != "unset" || configured.InstanceRoot.ActiveGeneration.Reason != "no_active_root_generation" {
		t.Fatal("configured identity guessed alive")
	}
	root := orchestrationWorker(t, first, "root", "first-generation", "coordinator", nil)
	child := orchestrationWorker(t, first, "child", "child-generation", "worker", &root.ID)
	ticketResult, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status) VALUES(?,925,'ticket','Fixture work','in-progress')`, first)
	if err != nil {
		t.Fatal(err)
	}
	ticketID, _ := ticketResult.LastInsertId()
	child, err = managedharness.NewService(db.DB).AssignBinding(context.Background(), managedharness.BindingInput{ProjectID: first, SessionID: child.ID, ExpectedRevision: child.Revision, ParentSessionID: &root.ID, TicketID: &ticketID, WorkShape: "ship"})
	if err != nil {
		t.Fatal(err)
	}
	next := orchestrationWorker(t, second, "next-root", "next-generation", "coordinator", nil)
	path := "/api/agent-mode/orchestration/v1?zoom=100"
	before := orchestrationSnapshot(t, path, ts.adminCookie)
	if before.InstanceRoot.ActiveGeneration.SessionID == nil || *before.InstanceRoot.ActiveGeneration.SessionID != root.ID {
		t.Fatal("configured active root not resolved")
	}
	project := orchestrationSnapshot(t, fmt.Sprintf("/api/agent-mode/projects/%d/orchestration/v1?zoom=100", first), ts.adminCookie)
	for _, worker := range project.Fleet.Workers {
		for _, portfolioWorker := range before.Fleet.Workers {
			if worker.HarnessSessionID != portfolioWorker.HarnessSessionID {
				continue
			}
			worker.Liveness.ObservedAt = portfolioWorker.Liveness.ObservedAt
			if !reflect.DeepEqual(worker, portfolioWorker) {
				t.Fatal("project and portfolio worker truth disagree")
			}
		}
	}
	if len(project.ProjectCoordination) != 1 || project.ProjectCoordination[0].Coordinator.SessionID == nil || *project.ProjectCoordination[0].Coordinator.SessionID != root.ID {
		t.Fatal("project coordinator lost")
	}
	if _, err := db.DB.Exec(`UPDATE harness_sessions SET heartbeat_at=? WHERE id=?`, time.Now().Add(-10*time.Minute).UTC().Format("2006-01-02T15:04:05.000Z"), root.ID); err != nil {
		t.Fatal(err)
	}
	stale := orchestrationSnapshot(t, path, ts.adminCookie)
	if stale.InstanceRoot.ActiveGeneration.State != "unknown" || stale.InstanceRoot.ActiveGeneration.SessionID != nil || stale.ProjectCoordination[0].Coordinator.State != "unset" {
		t.Fatal("stale root guessed live")
	}
	for _, worker := range stale.Fleet.Workers {
		if worker.HarnessSessionID == child.ID && (worker.Liveness.State != "busy" || worker.ParentSessionID == nil || *worker.ParentSessionID != root.ID) {
			t.Fatal("mixed-staleness hierarchy changed")
		}
	}
	// Reassign the root across projects; old generation and same-project child
	// bindings remain durable facts, never rewritten into cross-project parents.
	setOrchestrator(t, ts, 1, second, "next-root", "Next root")
	after := orchestrationSnapshot(t, path, ts.adminCookie)
	if after.InstanceRoot.BindingRevision != 2 || after.InstanceRoot.ActiveGeneration.SessionID == nil || *after.InstanceRoot.ActiveGeneration.SessionID != next.ID {
		t.Fatal("root reassignment did not select new identity")
	}
	outside := orchestrationSnapshot(t, fmt.Sprintf("/api/agent-mode/projects/%d/orchestration/v1?zoom=1", first), ts.adminCookie)
	if outside.InstanceRoot.ActiveGeneration.State != "unknown" || outside.InstanceRoot.ActiveGeneration.Reason != "generation_not_in_sample" || outside.InstanceRoot.ActiveGeneration.SessionID != nil {
		t.Fatal("out-of-scope generation guessed")
	}
	service := managedharness.NewService(db.DB)
	if _, err := service.Stop(context.Background(), next.ID); err != nil {
		t.Fatal(err)
	}
	replacement := orchestrationWorker(t, second, "next-root", "replacement-generation", "coordinator", nil)
	latest := orchestrationSnapshot(t, path, ts.adminCookie)
	if latest.InstanceRoot.ActiveGeneration.SessionID == nil || *latest.InstanceRoot.ActiveGeneration.SessionID != replacement.ID {
		t.Fatal("terminal generation reused")
	}
	oldFound := false
	for _, worker := range latest.Fleet.Workers {
		if worker.HarnessSessionID == next.ID {
			oldFound = worker.Liveness.State == "dead"
		}
	}
	if !oldFound {
		t.Fatal("terminal generation history disappeared")
	}
	var events int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM instance_orchestrator_events`).Scan(&events); err != nil || events != 2 {
		t.Fatal("root event history rewritten")
	}
	var bindingEvents int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_events WHERE harness_session_id=? AND operation='binding_changed'`, child.ID).Scan(&bindingEvents); err != nil || bindingEvents != 1 {
		t.Fatal("binding history missing")
	}
	var retainedParent string
	if err := db.DB.QueryRow(`SELECT parent_harness_session_id FROM harness_sessions WHERE id=?`, child.ID).Scan(&retainedParent); err != nil || retainedParent != root.ID {
		t.Fatal("root replacement rewrote parentage")
	}
	// Two processes for the same canonical root remain ambiguous even if one is stale.
	orchestrationWorker(t, second, "next-root", "competing-generation", "coordinator", nil, "claude")
	ambiguous := orchestrationSnapshot(t, path, ts.adminCookie)
	if ambiguous.InstanceRoot.ActiveGeneration.State != "ambiguous" || ambiguous.InstanceRoot.ActiveGeneration.SessionID != nil || ambiguous.ProjectCoordination[1].Coordinator.State != "ambiguous" {
		t.Fatal("multiple coordinators guessed away")
	}
}

func TestOrchestrationBoundsPrivacyAndCommunication(t *testing.T) {
	ts := newTestServer(t)
	promoteToSuperAdmin(t, "admin")
	hidden, _ := seedOrchestratorAgent(t, ts, "HID", "private-root")
	setOrchestrator(t, ts, 0, hidden, "private-root", "Public label")
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) SELECT ?,id,'none' FROM users WHERE username='member' ON CONFLICT(user_id,project_id) DO UPDATE SET access_level='none'`, hidden); err != nil {
		t.Fatal(err)
	}
	baseline := orchestrationSnapshot(t, "/api/agent-mode/orchestration/v1", ts.memberCookie)
	orchestrationWorker(t, hidden, "private-root", "private-generation", "coordinator", nil)
	after := orchestrationSnapshot(t, "/api/agent-mode/orchestration/v1", ts.memberCookie)
	baseline.Fleet.ObservedAt = after.Fleet.ObservedAt
	if !reflect.DeepEqual(baseline, after) {
		t.Fatal("hidden node creation changed visible state or counts")
	}
	var concealed []byte
	for _, id := range []int64{hidden, 999999} {
		response := orchestrationGet(fmt.Sprintf("/api/agent-mode/projects/%d/orchestration/v1", id), ts.memberCookie)
		assertStatus(t, response, http.StatusNotFound)
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		// Problem instance is request-specific; compare all other public fields.
		var problem map[string]any
		if err := json.Unmarshal(body, &problem); err != nil {
			t.Fatal(err)
		}
		delete(problem, "instance")
		delete(problem, "request_id")
		normalized, _ := json.Marshal(problem)
		if concealed != nil && string(concealed) != string(normalized) {
			t.Fatal("missing and unauthorized project responses differ")
		}
		concealed = normalized
	}
	visible := seedBatchProject(t, "Visible", "VIS")
	worker := orchestrationWorker(t, visible, "worker", "visible-generation", "worker", nil)
	for i := 0; i < 6; i++ {
		if _, err := db.DB.Exec(`INSERT INTO agent_messages(from_agent_id,to_agent_id,body,message_id,parts_json,metadata_json,delivery_level) VALUES(?,?,?,?,'[]','{}','steer')`, worker.ProjectAgentID, worker.ProjectAgentID, "fixture-private-message", fmt.Sprintf("00000000-0000-4000-8000-%012d", i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 104; i++ {
		p := seedBatchProject(t, fmt.Sprintf("Project %d", i), fmt.Sprintf("Z%03d", i))
		orchestrationWorker(t, p, "worker", fmt.Sprintf("generation-%d", i), "worker", nil)
	}
	for _, zoom := range []string{"1", "10", "100", "1000", strings.Repeat("9", 64)} {
		snap := orchestrationSnapshot(t, "/api/agent-mode/orchestration/v1?zoom="+zoom, ts.memberCookie)
		_, _, limit, _ := workerfleet.ParseZoom(zoom)
		if len(snap.Fleet.Workers) != limit || snap.Fleet.SampleLimit != limit || snap.Fleet.Totals.Workers != 105 || snap.Fleet.Totals.OmittedWorkers != int64(105-limit) || snap.CoordinationBounds.TotalProjects != 105 || snap.CoordinationBounds.OmittedProjects != int64(105-limit) {
			t.Fatal("zoom bounds or authorized count drift")
		}
		for _, w := range snap.Fleet.Workers {
			if w.HarnessSessionID == worker.ID && (len(w.RecentCommunication) != 4 || w.RecentCommunicationOmitted != 2) {
				t.Fatal("communication not bounded")
			}
		}
		raw, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"private-root", "private-generation", "fixture-private-message", "/fixture/private-workspace", `"body"`, `"target_ref"`, `"worker_lease"`, `"session_ref"`} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatal("projection exposed private evidence")
			}
		}
	}
}

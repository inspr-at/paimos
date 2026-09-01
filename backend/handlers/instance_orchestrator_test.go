// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <camyb@users.noreply.github.com>

package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

func orchestratorRequest(t *testing.T, ts *testServer, method, path, cookie string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
		if method != http.MethodGet && method != http.MethodHead {
			req.Header.Set("X-CSRF-Token", csrfTokenForSessionCookie(t, cookie))
			req.Header.Set("Origin", ts.srv.URL)
		}
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertOrchestratorError(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d want=%d body=%s", response.StatusCode, status, body)
	}
	var payload map[string]string
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 1 || payload["error"] != code {
		t.Fatalf("error payload=%v want only error=%q", payload, code)
	}
}

func seedOrchestratorAgent(t *testing.T, ts *testServer, projectKey, agentKey string) (int64, int64) {
	t.Helper()
	projectID := createTestProject(t, ts, projectKey+" project", projectKey)
	response := ts.post(t, agentsURL(projectID), ts.adminCookie, map[string]any{"name": agentKey})
	assertStatus(t, response, http.StatusCreated)
	var agent models.ProjectAgent
	decode(t, response, &agent)
	return projectID, agent.ID
}

func setOrchestrator(t *testing.T, ts *testServer, expected, projectID int64, key, label string) models.OrchestratorConfigResponse {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"expected_revision": expected,
		"orchestrator":      map[string]any{"project_id": projectID, "key": key, "display_label": label},
	})
	response := orchestratorRequest(t, ts, http.MethodPut, "/api/orchestrator/v1/config", ts.adminCookie, body)
	assertStatus(t, response, http.StatusOK)
	var result models.OrchestratorConfigResponse
	decode(t, response, &result)
	return result
}

func TestOrchestratorProjectionStartsUnsetAndNeverInfersOnlyAgent(t *testing.T) {
	ts := newTestServer(t)
	_, _ = seedOrchestratorAgent(t, ts, "OUI", "amy")

	response := ts.get(t, "/api/orchestrator/v1", ts.memberCookie)
	assertStatus(t, response, http.StatusOK)
	if got := response.Header.Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	var projection models.OrchestratorProjectionResponse
	decode(t, response, &projection)
	if projection.SchemaVersion != 1 || projection.Revision != 0 || projection.Orchestrator != nil || projection.UpdatedAt != nil {
		t.Fatalf("fresh projection=%+v, want exact unset", projection)
	}
}

func TestOrchestratorSetRedactsProjectionAndWritesAudit(t *testing.T) {
	ts := newTestServer(t)
	promoteToSuperAdmin(t, "admin")
	projectID, agentID := seedOrchestratorAgent(t, ts, "ORC", "amy")

	config := setOrchestrator(t, ts, 0, projectID, "amy", "Amy")
	if config.Revision != 1 || config.Orchestrator == nil || config.Orchestrator.ProjectAgentID != agentID ||
		config.Orchestrator.ProjectID != projectID || config.Orchestrator.ProjectKey != "ORC" || config.Orchestrator.Key != "amy" ||
		config.Orchestrator.DisplayLabel != "Amy" || config.UpdatedAt == nil {
		t.Fatalf("config=%+v", config)
	}

	ordinary := ts.get(t, "/api/orchestrator/v1", ts.memberCookie)
	assertStatus(t, ordinary, http.StatusOK)
	raw, _ := io.ReadAll(ordinary.Body)
	ordinary.Body.Close()
	if string(raw) == "" || strings.Contains(string(raw), `"project_id"`) || strings.Contains(string(raw), `"project_agent_id"`) || strings.Contains(string(raw), `"key"`) {
		t.Fatalf("ordinary projection leaked target identity: %s", raw)
	}
	var projection models.OrchestratorProjectionResponse
	if err := json.Unmarshal(raw, &projection); err != nil || projection.Orchestrator == nil || projection.Orchestrator.DisplayLabel != "Amy" {
		t.Fatalf("projection=%+v err=%v body=%s", projection, err, raw)
	}

	eventsResponse := ts.get(t, "/api/orchestrator/v1/events", ts.adminCookie)
	assertStatus(t, eventsResponse, http.StatusOK)
	var events models.OrchestratorEventsResponse
	decode(t, eventsResponse, &events)
	if len(events.Events) != 1 || events.Events[0].Operation != "set" || events.Events[0].Before != nil ||
		events.Events[0].After == nil || events.Events[0].After.Key != "amy" || events.NextAfterRevision == nil || *events.NextAfterRevision != 1 {
		t.Fatalf("events=%+v", events)
	}
}

func TestOrchestratorStrictCASAndAuthorization(t *testing.T) {
	ts := newTestServer(t)
	projectID, _ := seedOrchestratorAgent(t, ts, "CAS", "amy")

	assertOrchestratorError(t, orchestratorRequest(t, ts, http.MethodPut, "/api/orchestrator/v1/config", ts.adminCookie,
		[]byte(`{"expected_revision":0,"orchestrator":null}`)), http.StatusForbidden, "forbidden")
	promoteToSuperAdmin(t, "admin")
	for _, body := range []string{
		`{"expected_revision":0,"expected_revision":1,"orchestrator":null}`,
		`{"expected_revision":0,"orchestrator":{"project_id":1,"key":"amy","display_label":"Amy","extra":true}}`,
		`{"expected_revision":0,"orchestrator":null} {}`,
	} {
		assertOrchestratorError(t, orchestratorRequest(t, ts, http.MethodPut, "/api/orchestrator/v1/config", ts.adminCookie, []byte(body)),
			http.StatusBadRequest, "invalid_request")
	}
	setOrchestrator(t, ts, 0, projectID, "amy", "Amy")
	stale, _ := json.Marshal(map[string]any{"expected_revision": 0, "orchestrator": nil})
	assertOrchestratorError(t, orchestratorRequest(t, ts, http.MethodPut, "/api/orchestrator/v1/config", ts.adminCookie, stale),
		http.StatusConflict, "revision_conflict")
	noChange, _ := json.Marshal(map[string]any{"expected_revision": 1,
		"orchestrator": map[string]any{"project_id": projectID, "key": "amy", "display_label": "Amy"}})
	assertOrchestratorError(t, orchestratorRequest(t, ts, http.MethodPut, "/api/orchestrator/v1/config", ts.adminCookie, noChange),
		http.StatusBadRequest, "no_change")
	clearBody, _ := json.Marshal(map[string]any{"expected_revision": 1, "orchestrator": nil})
	clearResponse := orchestratorRequest(t, ts, http.MethodPut, "/api/orchestrator/v1/config", ts.adminCookie, clearBody)
	assertStatus(t, clearResponse, http.StatusOK)
	var cleared models.OrchestratorConfigResponse
	decode(t, clearResponse, &cleared)
	if cleared.Revision != 2 || cleared.Orchestrator != nil || cleared.UpdatedAt == nil || *cleared.UpdatedAt == "" {
		t.Fatalf("cleared lifecycle=%+v", cleared)
	}
	assertOrchestratorError(t, ts.get(t, "/api/orchestrator/v1/config", ts.memberCookie), http.StatusForbidden, "forbidden")
}

func TestOrchestratorConcurrentCASHasOneWinner(t *testing.T) {
	ts := newTestServer(t)
	promoteToSuperAdmin(t, "admin")
	projectA, _ := seedOrchestratorAgent(t, ts, "CA1", "amy")
	projectB, _ := seedOrchestratorAgent(t, ts, "CA2", "sam")
	csrf := csrfTokenForSessionCookie(t, ts.adminCookie)

	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, target := range []struct {
		projectID int64
		key       string
	}{{projectA, "amy"}, {projectB, "sam"}} {
		wg.Add(1)
		go func(target struct {
			projectID int64
			key       string
		}) {
			defer wg.Done()
			body := fmt.Sprintf(`{"expected_revision":0,"orchestrator":{"project_id":%d,"key":%q,"display_label":"Winner"}}`, target.projectID, target.key)
			req, _ := http.NewRequest(http.MethodPut, ts.srv.URL+"/api/orchestrator/v1/config", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", ts.adminCookie)
			req.Header.Set("X-CSRF-Token", csrf)
			req.Header.Set("Origin", ts.srv.URL)
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				statuses <- 0
				return
			}
			defer response.Body.Close()
			statuses <- response.StatusCode
		}(target)
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("concurrent statuses=%v", counts)
	}
	var revision, events int
	if err := db.DB.QueryRow(`SELECT revision FROM instance_orchestrator`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM instance_orchestrator_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || events != 1 {
		t.Fatalf("revision=%d events=%d", revision, events)
	}
}

func TestOrchestratorPrivateNoStoreCoversEarlyRefusals(t *testing.T) {
	ts := newTestServer(t)
	for name, response := range map[string]*http.Response{
		"unauthenticated": ts.get(t, "/api/orchestrator/v1", ""),
		"external":        ts.get(t, "/api/orchestrator/v1", ts.externalCookie),
		"forbidden":       ts.get(t, "/api/orchestrator/v1/config", ts.memberCookie),
	} {
		response.Body.Close()
		if response.StatusCode < 400 || response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s status=%d Cache-Control=%q", name, response.StatusCode, response.Header.Get("Cache-Control"))
		}
	}
}

func TestOrchestratorAssignedRenameAndDeleteGuards(t *testing.T) {
	ts := newTestServer(t)
	promoteToSuperAdmin(t, "admin")
	projectID, agentID := seedOrchestratorAgent(t, ts, "GRD", "amy")
	setOrchestrator(t, ts, 0, projectID, "amy", "Amy")

	assertOrchestratorError(t, ts.del(t, agentURL(projectID, "amy"), ts.adminCookie), http.StatusConflict, "orchestrator_assigned")
	assertOrchestratorError(t, ts.del(t, fmt.Sprintf("/api/projects/%d", projectID), ts.adminCookie), http.StatusConflict, "orchestrator_assigned")
	assertOrchestratorError(t, ts.put(t, fmt.Sprintf("/api/projects/%d", projectID), ts.adminCookie, map[string]any{"status": "deleted"}),
		http.StatusConflict, "orchestrator_assigned")

	if _, err := db.DB.Exec(`UPDATE users SET role_key='admin',is_super_admin=0 WHERE username='admin'`); err != nil {
		t.Fatal(err)
	}
	assertOrchestratorError(t, ts.put(t, agentURL(projectID, "amy"), ts.adminCookie, map[string]any{
		"name": "amelia", "expected_orchestrator_revision": 1,
	}), http.StatusForbidden, "forbidden")
	promoteToSuperAdmin(t, "admin")
	rename := ts.put(t, agentURL(projectID, "amy"), ts.adminCookie, map[string]any{
		"name": "amelia", "expected_orchestrator_revision": 1,
	})
	assertStatus(t, rename, http.StatusOK)
	rename.Body.Close()
	var stableID, revision int64
	var operation, beforeKey, afterKey string
	if err := db.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amelia'`, projectID).Scan(&stableID); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT revision FROM instance_orchestrator`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT operation,before_key,after_key FROM instance_orchestrator_events WHERE after_revision=2`).Scan(&operation, &beforeKey, &afterKey); err != nil {
		t.Fatal(err)
	}
	if stableID != agentID || revision != 2 || operation != "target_rename" || beforeKey != "amy" || afterKey != "amelia" {
		t.Fatalf("stableID=%d revision=%d event=%s/%s/%s", stableID, revision, operation, beforeKey, afterKey)
	}
}

func TestOrchestratorAssignedRenameRejectsLiveHarnessWithoutChanges(t *testing.T) {
	ts := newTestServer(t)
	promoteToSuperAdmin(t, "admin")
	projectID, agentID := seedOrchestratorAgent(t, ts, "LIV", "amy")
	setOrchestrator(t, ts, 0, projectID, "amy", "Amy")
	if _, err := db.DB.Exec(`INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,
		management_mode,role,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase)
		VALUES('22222222-2222-4222-8222-222222222222',?,?,?,'codex','host',?,randomblob(32),
		'managed','worker','none',0,1,0,0,0,'working')`, projectID, agentID, "amy", make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	response := ts.put(t, agentURL(projectID, "amy"), ts.adminCookie, map[string]any{
		"name": "amelia", "expected_orchestrator_revision": 1,
	})
	assertOrchestratorError(t, response, http.StatusConflict, "active_harness_rename_conflict")
	var name string
	var revision, events int
	if err := db.DB.QueryRow(`SELECT name FROM project_agents WHERE id=?`, agentID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT revision FROM instance_orchestrator`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM instance_orchestrator_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if name != "amy" || revision != 1 || events != 1 {
		t.Fatalf("name=%q revision=%d events=%d", name, revision, events)
	}
}

func TestOrchestratorAuditFailureRollsBackAssignment(t *testing.T) {
	ts := newTestServer(t)
	promoteToSuperAdmin(t, "admin")
	projectID, _ := seedOrchestratorAgent(t, ts, "RBK", "amy")
	setOrchestrator(t, ts, 0, projectID, "amy", "Amy")
	if _, err := db.DB.Exec(`CREATE TRIGGER test_orchestrator_event_abort BEFORE INSERT ON instance_orchestrator_events
		WHEN NEW.operation='display_label_update' BEGIN SELECT RAISE(ABORT,'test audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"expected_revision": 1,
		"orchestrator": map[string]any{"project_id": projectID, "key": "amy", "display_label": "Amelia"}})
	assertOrchestratorError(t, orchestratorRequest(t, ts, http.MethodPut, "/api/orchestrator/v1/config", ts.adminCookie, body),
		http.StatusInternalServerError, "internal_error")
	var label string
	var revision, events int
	if err := db.DB.QueryRow(`SELECT display_label,revision FROM instance_orchestrator`).Scan(&label, &revision); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM instance_orchestrator_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if label != "Amy" || revision != 1 || events != 1 {
		t.Fatalf("rollback label=%q revision=%d events=%d", label, revision, events)
	}
}

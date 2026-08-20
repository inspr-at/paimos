// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/sse"
)

const telemetryActivityBoundaryBytes = 280

type directTelemetryServer struct {
	handler        http.Handler
	adminCookie    string
	memberCookie   string
	externalCookie string
}

func newDirectTelemetryServer(t *testing.T) *directTelemetryServer {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.DB.Close()
		db.DB = nil
	})
	for _, user := range []struct {
		name, password, role string
	}{{"admin", "adminpass", "admin"}, {"member", "memberpass", "member"}, {"external", "externalpass", "external"}} {
		hash, _ := auth.HashPassword(user.password)
		res, err := db.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES(?,?,?,'active')`, user.name, hash, user.role)
		if err != nil {
			t.Fatal(err)
		}
		if user.role == "admin" {
			id, _ := res.LastInsertId()
			auth.SeedAccessForUser(id, user.role)
		}
	}
	ts := &directTelemetryServer{handler: buildRouter()}
	ts.adminCookie = ts.login(t, "admin", "adminpass")
	ts.memberCookie = ts.login(t, "member", "memberpass")
	ts.externalCookie = ts.login(t, "external", "externalpass")
	return ts
}

func (ts *directTelemetryServer) login(t *testing.T, username, password string) string {
	t.Helper()
	resp := ts.request(t, http.MethodPost, "/api/auth/login", "", map[string]any{"username": username, "password": password})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(resp.Cookies()) == 0 {
		t.Fatalf("login %s status=%d", username, resp.StatusCode)
	}
	return resp.Cookies()[0].Name + "=" + resp.Cookies()[0].Value
}

func (ts *directTelemetryServer) request(t *testing.T, method, path, cookie string, body any) *http.Response {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	ts.handler.ServeHTTP(rec, req)
	return rec.Result()
}

func (ts *directTelemetryServer) get(t *testing.T, path, cookie string) *http.Response {
	return ts.request(t, http.MethodGet, path, cookie, nil)
}

func (ts *directTelemetryServer) post(t *testing.T, path, cookie string, body any) *http.Response {
	return ts.request(t, http.MethodPost, path, cookie, body)
}

func (ts *directTelemetryServer) patch(t *testing.T, path, cookie string, body any) *http.Response {
	return ts.request(t, http.MethodPatch, path, cookie, body)
}

func seedTelemetryRun(t *testing.T, ts *directTelemetryServer, requestedCookie string) (int64, int64) {
	t.Helper()
	projectID := seedBatchProject(t, "TEL", "Telemetry")
	res, err := db.DB.Exec(`INSERT INTO issues(project_id, issue_number, type, title, status) VALUES(?,?,?,?,?)`,
		projectID, 1, "ticket", "Instrument me", "in-progress")
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := res.LastInsertId()
	resp := ts.post(t, "/api/issues/"+itoa(issueID)+"/implement", requestedCookie, map[string]any{})
	assertStatus(t, resp, http.StatusCreated)
	var run map[string]any
	decode(t, resp, &run)
	return issueID, int64(run["id"].(float64))
}

func telemetryReport(sequence int64, reportedAt string) map[string]any {
	return map[string]any{
		"sequence": sequence, "correlation_id": "claude-session-01",
		"provider": "anthropic", "adapter": "claude-code",
		"agent_reported_at": reportedAt, "kind": "progress", "heartbeat": true,
		"phase": "implementing", "activity": "Implementing the bounded telemetry contract",
		"needs_input": false, "blocker_state": "none",
		"estimate_revision": 1, "progress_percent": 25.0,
		"eta_seconds": 900, "eta_min_seconds": 600, "eta_max_seconds": 1200,
		"estimate_source": "agent", "estimate_confidence": 0.7,
		"estimate_basis": "One of four named implementation checkpoints completed",
	}
}

func TestAgentRunTelemetryAppendDuplicateOrderingAndLatest(t *testing.T) {
	ts := newDirectTelemetryServer(t)
	_, runID := seedTelemetryRun(t, ts, ts.adminCookie)

	// Runs with legacy/uninstrumented reporters are explicit, not misleadingly
	// represented as 0% or stale.
	resp := ts.get(t, "/api/runs/"+itoa(runID)+"/telemetry/latest", ts.adminCookie)
	assertStatus(t, resp, http.StatusOK)
	var empty map[string]any
	decode(t, resp, &empty)
	if empty["instrumented"] != false || empty["liveness"] != "unknown" || empty["latest"] != nil {
		t.Fatalf("uninstrumented snapshot=%+v", empty)
	}

	first := telemetryReport(1, "2026-08-20T09:00:00+02:00")
	resp = ts.post(t, "/api/runs/"+itoa(runID)+"/telemetry", ts.adminCookie, first)
	assertStatus(t, resp, http.StatusCreated)
	var accepted map[string]any
	decode(t, resp, &accepted)
	if accepted["duplicate"] != false {
		t.Fatalf("first append=%+v", accepted)
	}

	// Exact replay is idempotent; the server receipt timestamp is retained.
	resp = ts.post(t, "/api/runs/"+itoa(runID)+"/telemetry", ts.adminCookie, first)
	assertStatus(t, resp, http.StatusOK)
	decode(t, resp, &accepted)
	if accepted["duplicate"] != true {
		t.Fatalf("duplicate append=%+v", accepted)
	}

	// Same sequence with different facts and lower sequences are deterministic
	// conflicts, while a delayed report with a higher sequence is accepted.
	conflict := telemetryReport(1, "2026-08-20T09:00:00+02:00")
	conflict["activity"] = "Different activity"
	assertStatus(t, ts.post(t, "/api/runs/"+itoa(runID)+"/telemetry", ts.adminCookie, conflict), http.StatusConflict)
	assertStatus(t, ts.post(t, "/api/runs/"+itoa(runID)+"/telemetry", ts.adminCookie,
		telemetryReport(0, "2026-08-20T09:00:00Z")), http.StatusBadRequest)

	delayed := telemetryReport(3, "2020-01-01T00:00:00Z")
	delayed["estimate_revision"] = 2
	delayed["progress_percent"] = 50.0
	resp = ts.post(t, "/api/runs/"+itoa(runID)+"/telemetry", ts.adminCookie, delayed)
	assertStatus(t, resp, http.StatusCreated)
	decode(t, resp, &accepted)
	event := accepted["telemetry"].(map[string]any)
	if event["clock_skewed"] != true {
		t.Fatalf("clock-skewed event=%+v", event)
	}
	outOfOrder := telemetryReport(2, "2026-08-20T09:01:00Z")
	outOfOrder["estimate_revision"] = 2
	assertStatus(t, ts.post(t, "/api/runs/"+itoa(runID)+"/telemetry", ts.adminCookie, outOfOrder), http.StatusConflict)

	resp = ts.get(t, "/api/runs/"+itoa(runID)+"/telemetry/latest", ts.adminCookie)
	assertStatus(t, resp, http.StatusOK)
	var latest map[string]any
	decode(t, resp, &latest)
	if latest["instrumented"] != true || latest["liveness"] != "live" {
		t.Fatalf("latest snapshot=%+v", latest)
	}
	if latest["freshness_seconds"].(float64) > 5 {
		t.Fatalf("freshness used agent clock instead of receipt clock: %+v", latest)
	}
	if latest["latest"].(map[string]any)["sequence"] != float64(3) {
		t.Fatalf("latest snapshot=%+v", latest)
	}

	resp = ts.get(t, "/api/runs/"+itoa(runID)+"/telemetry?after_sequence=0&limit=10", ts.adminCookie)
	assertStatus(t, resp, http.StatusOK)
	var history struct {
		Events []map[string]any `json:"events"`
	}
	decode(t, resp, &history)
	if len(history.Events) != 2 || history.Events[0]["sequence"] != float64(1) || history.Events[1]["sequence"] != float64(3) {
		t.Fatalf("history=%+v", history.Events)
	}
}

func TestAgentRunTelemetryValidationPrivacyAndStableIdentity(t *testing.T) {
	ts := newDirectTelemetryServer(t)
	_, runID := seedTelemetryRun(t, ts, ts.adminCookie)
	path := "/api/runs/" + itoa(runID) + "/telemetry"
	now := time.Now().UTC().Format(time.RFC3339Nano)

	bad := []map[string]any{
		{"sequence": 1, "correlation_id": "ok", "provider": "anthropic", "adapter": "claude-code", "agent_reported_at": "not-a-time", "kind": "heartbeat"},
		{"sequence": 1, "correlation_id": "ok", "provider": "anthropic", "adapter": "claude-code", "agent_reported_at": now, "kind": "progress", "progress_percent": 101, "estimate_revision": 1, "estimate_source": "agent", "estimate_confidence": .5, "estimate_basis": "evidence"},
		{"sequence": 1, "correlation_id": "ok", "provider": "anthropic", "adapter": "claude-code", "agent_reported_at": now, "kind": "progress", "progress_percent": 20},
		{"sequence": 1, "correlation_id": "ok", "provider": "anthropic", "adapter": "claude-code", "agent_reported_at": now, "kind": "estimate", "eta_seconds": 20, "estimate_revision": 1, "estimate_source": "agent", "estimate_confidence": .5, "estimate_basis": "evidence"},
		{"sequence": 1, "correlation_id": "ok", "provider": "anthropic", "adapter": "claude-code", "agent_reported_at": now, "kind": "heartbeat"},
		{"sequence": 1, "correlation_id": "ok", "provider": "anthropic", "adapter": "claude-code", "agent_reported_at": now, "kind": "heartbeat", "activity": "raw\ncommand output"},
		{"sequence": 1, "correlation_id": "ok", "provider": "anthropic", "adapter": "claude-code", "agent_reported_at": now, "kind": "heartbeat", "provider_payload": map[string]any{"prompt": "must never persist"}},
		{"sequence": 1, "correlation_id": "ok", "provider": "anthropic", "adapter": "claude-code", "agent_reported_at": now, "kind": "phase", "phase": "implementing", "activity": "Authorization: Bearer obvioussecretvalue123"},
		{"sequence": 1, "correlation_id": "ok", "provider": "anthropic", "adapter": "claude-code", "agent_reported_at": now, "kind": "progress", "phase": "implementing", "progress_percent": 20, "estimate_revision": 1, "estimate_source": "agent", "estimate_confidence": .5, "estimate_basis": "token=obvioussecretvalue123"},
		{"sequence": 1, "correlation_id": "ok", "provider": "anthropic", "adapter": "claude-code", "agent_reported_at": now, "kind": "phase", "phase": "implementing", "activity": "credential ghp_abcdefghijklmnopqrstuvwxyz123456"},
		{"sequence": 1, "correlation_id": "ok", "provider": "anthropic", "adapter": "claude-code", "agent_reported_at": now, "kind": "phase", "phase": "implementing", "activity": "fetch https://runner:password123@example.test/repo"},
		{"sequence": 1, "correlation_id": "ok", "provider": "anthropic", "adapter": "claude-code", "agent_reported_at": now, "kind": "phase", "phase": "implementing", "activity": strings.Repeat("a", 17<<10)},
	}
	for i, body := range bad {
		resp := ts.post(t, path, ts.adminCookie, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("bad case %d status=%d, want 400", i, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	good := telemetryReport(1, now)
	good["activity"] = strings.Repeat("a", telemetryActivityBoundaryBytes-2) + "é"
	assertStatus(t, ts.post(t, path, ts.adminCookie, good), http.StatusCreated)
	changedIdentity := telemetryReport(2, now)
	changedIdentity["provider"] = "another-provider"
	assertStatus(t, ts.post(t, path, ts.adminCookie, changedIdentity), http.StatusConflict)
	changedEstimate := telemetryReport(2, now)
	changedEstimate["progress_percent"] = 30.0
	assertStatus(t, ts.post(t, path, ts.adminCookie, changedEstimate), http.StatusConflict)

	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_run_telemetry WHERE run_id=?`, runID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("persisted events=%d err=%v, want only valid event", count, err)
	}
}

func TestAgentRunTelemetryHeartbeatPreservesSemanticAndEstimateSnapshot(t *testing.T) {
	ts := newDirectTelemetryServer(t)
	_, runID := seedTelemetryRun(t, ts, ts.adminCookie)
	path := "/api/runs/" + itoa(runID) + "/telemetry"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	estimate := telemetryReport(1, now)
	estimate["heartbeat"] = false
	assertStatus(t, ts.post(t, path, ts.adminCookie, estimate), http.StatusCreated)

	// A semantic event alone is fresh activity, but is deliberately not
	// supervisor liveness evidence.
	resp := ts.get(t, path+"/latest", ts.adminCookie)
	assertStatus(t, resp, http.StatusOK)
	var before map[string]any
	decode(t, resp, &before)
	if before["liveness"] != "unknown" || before["latest_heartbeat"] != nil {
		t.Fatalf("semantic-only snapshot=%+v", before)
	}

	heartbeat := map[string]any{
		"sequence": 2, "correlation_id": "claude-session-01",
		"provider": "anthropic", "adapter": "claude-code",
		"agent_reported_at": now, "kind": "heartbeat", "heartbeat": true,
		"phase": "implementing", "needs_input": false, "blocker_state": "none",
	}
	assertStatus(t, ts.post(t, path, ts.adminCookie, heartbeat), http.StatusCreated)
	resp = ts.get(t, path+"/latest", ts.adminCookie)
	assertStatus(t, resp, http.StatusOK)
	var snapshot map[string]any
	decode(t, resp, &snapshot)
	if snapshot["liveness"] != "live" {
		t.Fatalf("heartbeat snapshot=%+v", snapshot)
	}
	for _, field := range []string{"latest_semantic", "latest_estimate"} {
		fact, ok := snapshot[field].(map[string]any)
		if !ok || fact["sequence"] != float64(1) || fact["progress_percent"] != float64(25) {
			t.Fatalf("%s was erased by heartbeat: %+v", field, snapshot[field])
		}
	}
	if event := snapshot["latest_event"].(map[string]any); event["sequence"] != float64(2) || event["kind"] != "heartbeat" {
		t.Fatalf("latest_event=%+v", event)
	}
}

func TestAgentRunTelemetryNewSemanticEventCannotHideStaleHeartbeat(t *testing.T) {
	ts := newDirectTelemetryServer(t)
	_, runID := seedTelemetryRun(t, ts, ts.adminCookie)
	path := "/api/runs/" + itoa(runID) + "/telemetry"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	heartbeat := map[string]any{
		"sequence": 1, "correlation_id": "stale-heartbeat-session",
		"provider": "openai", "adapter": "codex-cli", "agent_reported_at": now,
		"kind": "heartbeat", "heartbeat": true, "phase": "implementing",
		"needs_input": false, "blocker_state": "none",
	}
	assertStatus(t, ts.post(t, path, ts.adminCookie, heartbeat), http.StatusCreated)
	if _, err := db.DB.Exec(`UPDATE agent_run_telemetry_latest SET last_heartbeat_at=datetime('now','-10 minutes') WHERE run_id=?`, runID); err != nil {
		t.Fatal(err)
	}
	semantic := map[string]any{
		"sequence": 2, "correlation_id": "stale-heartbeat-session",
		"provider": "openai", "adapter": "codex-cli", "agent_reported_at": now,
		"kind": "phase", "phase": "reviewing", "activity": "Reviewing implementation",
		"needs_input": false, "blocker_state": "none",
	}
	assertStatus(t, ts.post(t, path, ts.adminCookie, semantic), http.StatusCreated)
	resp := ts.get(t, path+"/latest", ts.adminCookie)
	assertStatus(t, resp, http.StatusOK)
	var snapshot map[string]any
	decode(t, resp, &snapshot)
	if snapshot["liveness"] != "stale" || snapshot["latest_event"].(map[string]any)["sequence"] != float64(2) ||
		snapshot["latest_heartbeat"].(map[string]any)["sequence"] != float64(1) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestAgentRunTelemetryPublishesSSEInvalidationHintOnly(t *testing.T) {
	ts := newDirectTelemetryServer(t)
	_, runID := seedTelemetryRun(t, ts, ts.adminCookie)
	var projectID int64
	if err := db.DB.QueryRow(`SELECT project_id FROM agent_runs WHERE id=?`, runID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	sub := sse.GlobalBroker().Subscribe(999, "telemetry-test", projectID, false)
	t.Cleanup(func() { sse.GlobalBroker().Close(sub) })
	assertStatus(t, ts.post(t, "/api/runs/"+itoa(runID)+"/telemetry", ts.adminCookie,
		telemetryReport(1, time.Now().UTC().Format(time.RFC3339Nano))), http.StatusCreated)
	select {
	case event := <-sub.Events():
		if event.Type != "run_telemetry" || event.Name != itoa(runID) || event.Rev != "1" {
			t.Fatalf("hint=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("missing run_telemetry invalidation hint")
	}
}

func TestAgentRunTelemetryAuthorizationAndTerminalImmutability(t *testing.T) {
	ts := newDirectTelemetryServer(t)
	_, runID := seedTelemetryRun(t, ts, ts.adminCookie)
	path := "/api/runs/" + itoa(runID) + "/telemetry"
	report := telemetryReport(1, time.Now().UTC().Format(time.RFC3339Nano))

	// Unauthorized callers get existence-hiding 404 for reads and writes.
	assertStatus(t, ts.post(t, path, ts.externalCookie, report), http.StatusNotFound)
	assertStatus(t, ts.get(t, path, ts.externalCookie), http.StatusNotFound)
	assertStatus(t, ts.get(t, path+"/latest", ts.externalCookie), http.StatusNotFound)
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE agent_runs SET claimed_by=?, status='running', started_at=datetime('now') WHERE id=?`, memberID, runID); err != nil {
		t.Fatal(err)
	}
	// A stamped claimer can report even though only the requester/admin can
	// otherwise manage the run.
	assertStatus(t, ts.post(t, path, ts.memberCookie, report), http.StatusCreated)

	assertStatus(t, ts.patch(t, "/api/runs/"+itoa(runID), ts.adminCookie, map[string]any{"status": "failed"}), http.StatusOK)
	// A response-lost append remains idempotently replayable after lifecycle
	// completion. It must not update the immutable history or latest pointer.
	resp := ts.post(t, path, ts.adminCookie, report)
	assertStatus(t, resp, http.StatusOK)
	var duplicate map[string]any
	decode(t, resp, &duplicate)
	if duplicate["duplicate"] != true || duplicate["telemetry"].(map[string]any)["sequence"] != float64(1) {
		t.Fatalf("terminal exact duplicate=%+v", duplicate)
	}
	terminalConflict := telemetryReport(1, time.Now().UTC().Format(time.RFC3339Nano))
	terminalConflict["activity"] = "Conflicting terminal replay"
	assertStatus(t, ts.post(t, path, ts.adminCookie, terminalConflict), http.StatusConflict)
	late := telemetryReport(2, time.Now().UTC().Format(time.RFC3339Nano))
	late["estimate_revision"] = 2
	assertStatus(t, ts.post(t, path, ts.adminCookie, late), http.StatusConflict)

	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_run_telemetry WHERE run_id=?`, runID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("post-terminal telemetry changed history: count=%d err=%v", count, err)
	}
	resp = ts.get(t, path+"/latest", ts.adminCookie)
	assertStatus(t, resp, http.StatusOK)
	var snapshot map[string]any
	decode(t, resp, &snapshot)
	if snapshot["liveness"] != "ended" {
		t.Fatalf("terminal liveness=%+v", snapshot)
	}

	res, err := db.DB.Exec(`INSERT INTO agent_runs(issue_id, project_id, requested_by, status, started_at)
		SELECT issue_id, project_id, ?, 'running', datetime('now') FROM agent_runs WHERE id=?`, memberID, runID)
	if err != nil {
		t.Fatal(err)
	}
	requestedRunID, _ := res.LastInsertId()
	requesterReport := telemetryReport(1, time.Now().UTC().Format(time.RFC3339Nano))
	assertStatus(t, ts.post(t, "/api/runs/"+itoa(requestedRunID)+"/telemetry", ts.memberCookie, requesterReport), http.StatusCreated)
}

func TestAgentRunCompletedLifecycleAndTestEvidence(t *testing.T) {
	ts := newDirectTelemetryServer(t)
	_, completedID := seedTelemetryRun(t, ts, ts.adminCookie)
	assertStatus(t, ts.patch(t, "/api/runs/"+itoa(completedID), ts.adminCookie, map[string]any{
		"status": "running", "if_status": "queued", "device_id": "runner-1",
		"expects_supervisor_telemetry": true,
	}), http.StatusOK)
	resp := ts.patch(t, "/api/runs/"+itoa(completedID), ts.adminCookie, map[string]any{"status": "completed"})
	assertStatus(t, resp, http.StatusOK)
	var completed map[string]any
	decode(t, resp, &completed)
	if completed["status"] != "completed" || completed["finished_at"] == nil || completed["expects_supervisor_telemetry"] != true {
		t.Fatalf("completed=%+v", completed)
	}
	assertStatus(t, ts.patch(t, "/api/runs/"+itoa(completedID), ts.adminCookie, map[string]any{"status": "running"}), http.StatusConflict)
	assertStatus(t, ts.post(t, "/api/runs/"+itoa(completedID)+"/telemetry", ts.adminCookie,
		telemetryReport(1, time.Now().UTC().Format(time.RFC3339Nano))), http.StatusConflict)

	var projectID int64
	if err := db.DB.QueryRow(`SELECT project_id FROM agent_runs WHERE id=?`, completedID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	res, err := db.DB.Exec(`INSERT INTO issues(project_id, issue_number, type, title, status) VALUES(?,2,'ticket','Test evidence','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := res.LastInsertId()
	resp = ts.post(t, "/api/issues/"+itoa(issueID)+"/implement", ts.adminCookie, map[string]any{})
	assertStatus(t, resp, http.StatusCreated)
	var tested map[string]any
	decode(t, resp, &tested)
	testedID := int64(tested["id"].(float64))
	assertStatus(t, ts.patch(t, "/api/runs/"+itoa(testedID), ts.adminCookie, map[string]any{"status": "running"}), http.StatusOK)
	assertStatus(t, ts.patch(t, "/api/runs/"+itoa(testedID), ts.adminCookie, map[string]any{"status": "tests_passed"}), http.StatusConflict)
	assertStatus(t, ts.patch(t, "/api/runs/"+itoa(testedID), ts.adminCookie, map[string]any{
		"status": "tests_passed", "tests_summary": "configured test command passed",
	}), http.StatusOK)
}

func TestAgentRunTelemetryTerminalHandlerRaceHasOneConsistentWinner(t *testing.T) {
	ts := newDirectTelemetryServer(t)
	_, runID := seedTelemetryRun(t, ts, ts.adminCookie)
	assertStatus(t, ts.patch(t, "/api/runs/"+itoa(runID), ts.adminCookie, map[string]any{
		"status": "running", "if_status": "queued", "device_id": "runner-race",
	}), http.StatusOK)
	start := make(chan struct{})
	statuses := make(chan int, 2)
	go func() {
		<-start
		resp := ts.post(t, "/api/runs/"+itoa(runID)+"/telemetry", ts.adminCookie,
			telemetryReport(1, time.Now().UTC().Format(time.RFC3339Nano)))
		statuses <- resp.StatusCode
		_ = resp.Body.Close()
	}()
	go func() {
		<-start
		resp := ts.patch(t, "/api/runs/"+itoa(runID), ts.adminCookie, map[string]any{"status": "completed"})
		statuses <- resp.StatusCode
		_ = resp.Body.Close()
	}()
	close(start)
	got := []int{<-statuses, <-statuses}
	terminalOK := 0
	telemetryOK := 0
	for _, status := range got {
		if status == http.StatusOK {
			terminalOK++
		}
		if status == http.StatusCreated {
			telemetryOK++
		}
		if status != http.StatusOK && status != http.StatusCreated && status != http.StatusConflict {
			t.Fatalf("race statuses=%v", got)
		}
	}
	if terminalOK != 1 || telemetryOK > 1 {
		t.Fatalf("race statuses=%v", got)
	}
	var status string
	if err := db.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("final status=%q err=%v", status, err)
	}
	assertStatus(t, ts.post(t, "/api/runs/"+itoa(runID)+"/telemetry", ts.adminCookie,
		telemetryReport(2, time.Now().UTC().Format(time.RFC3339Nano))), http.StatusConflict)
}

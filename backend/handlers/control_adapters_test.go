// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/supervision"
	_ "modernc.org/sqlite"
)

func seedControlPriorityMutation(t *testing.T) (int64, supervision.PriorityMutation) {
	t.Helper()
	projectID := seedChangesProject(t, "CPR")
	issueID := seedChangesIssue(t, projectID, 1)
	userResult, err := db.DB.Exec(`INSERT INTO users(username,password,role) VALUES('control-priority-user','x','admin')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := userResult.LastInsertId()
	var revision int64
	var updatedAt string
	if err := db.DB.QueryRow(`SELECT control.revision,issue.updated_at FROM issues issue
		JOIN issue_control_revisions control ON control.issue_id=issue.id WHERE issue.id=?`, issueID).
		Scan(&revision, &updatedAt); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(issueETag(issueID, updatedAt)))
	return issueID, supervision.PriorityMutation{IssueID: issueID, ExpectedRevision: revision,
		ExpectedETagDigest: digest, Priority: "high", ActorUserID: userID,
		CommandID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
}

func TestControlPriorityMutationCASAuditAndRollbackAreAtomic(t *testing.T) {
	openChangesTestDB(t)
	issueID, mutation := seedControlPriorityMutation(t)
	mutator := controlSynchronousMutator{}

	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := mutator.SetIssuePriorityTx(context.Background(), tx, mutation); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var priority string
	if err := db.DB.QueryRow(`SELECT priority FROM issues WHERE id=?`, issueID).Scan(&priority); err != nil || priority != "medium" {
		t.Fatalf("rolled-back priority=%q err=%v", priority, err)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM mutation_log WHERE request_id=?`,
		`SELECT COUNT(*) FROM issue_history WHERE issue_id=?`,
	} {
		argument := any(mutation.CommandID)
		if strings.Contains(query, "issue_history") {
			argument = issueID
		}
		var count int
		if err := db.DB.QueryRow(query, argument).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rollback query=%q count=%d err=%v", query, count, err)
		}
	}

	tx, err = db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := mutator.SetIssuePriorityTx(context.Background(), tx, mutation); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var revision, mutations, history int64
	if err := db.DB.QueryRow(`SELECT issue.priority,control.revision FROM issues issue
		JOIN issue_control_revisions control ON control.issue_id=issue.id WHERE issue.id=?`, issueID).
		Scan(&priority, &revision); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM mutation_log WHERE request_id=? AND user_id=?
		AND mutation_type='control.issue.priority.set'`, mutation.CommandID, mutation.ActorUserID).Scan(&mutations); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM issue_history WHERE issue_id=? AND changed_by=?`, issueID,
		mutation.ActorUserID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if priority != "high" || revision != mutation.ExpectedRevision+1 || mutations != 1 || history != 1 {
		t.Fatalf("priority=%q revision=%d mutations=%d history=%d", priority, revision, mutations, history)
	}

	staleTx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := mutator.SetIssuePriorityTx(context.Background(), staleTx, mutation); err == nil {
		t.Fatal("stale priority mutation was accepted")
	}
	_ = staleTx.Rollback()
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM mutation_log WHERE request_id=?`, mutation.CommandID).Scan(&mutations); err != nil || mutations != 1 {
		t.Fatalf("stale retry changed audit count=%d err=%v", mutations, err)
	}
}

func seedControlledCancellationRun(t *testing.T, number int) int64 {
	t.Helper()
	var projectID int64
	if err := db.DB.QueryRow(`SELECT id FROM projects WHERE key='CTR'`).Scan(&projectID); err != nil {
		result, insertErr := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Control','CTR')`)
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		projectID, _ = result.LastInsertId()
	}
	result, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,?,'ticket','control cancellation','in-progress')`, projectID, number)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := result.LastInsertId()
	result, err = db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,status,started_at)
		VALUES(?,?,'running',datetime('now'))`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	return runID
}

func TestNonOperatorCancellationRequiresExactRunnerKeyAndDevice(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	prior := db.DB
	db.DB = database
	t.Cleanup(func() { db.DB = prior })
	if _, err := database.Exec(`CREATE TABLE control_capability_leases(
		lease_id TEXT,revision INTEGER,agent_run_id INTEGER,user_id INTEGER,actor_api_key_id INTEGER,
		device_id TEXT,revoked_at TEXT,expires_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_capability_leases VALUES(
		'lease',1,17,5,11,'device-a',NULL,strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour'))`); err != nil {
		t.Fatal(err)
	}
	run := &AgentRun{ID: 17, DeviceID: "device-a"}
	requestFor := func(key int64) *http.Request {
		principal, principalErr := auth.NewAPIKeyPrincipal(key, 5, auth.ScopeSet{auth.ScopeAgentControlsRunner: {}})
		if principalErr != nil {
			t.Fatal(principalErr)
		}
		request := httptest.NewRequest("PATCH", "/api/runs/17", nil)
		return request.WithContext(auth.WithPrincipal(request.Context(), principal))
	}
	if canRecordNonOperatorCancellation(requestFor(12), run, "device-a") {
		t.Fatal("second same-user API key crossed the lease binding")
	}
	if canRecordNonOperatorCancellation(requestFor(11), run, "device-b") {
		t.Fatal("wrong device crossed the run binding")
	}
	if !canRecordNonOperatorCancellation(requestFor(11), run, "device-a") {
		t.Fatal("exact current runner binding was rejected")
	}
}

func TestNonOperatorCancellationWrongKeyAndDeviceLeaveNoFactOrEvent(t *testing.T) {
	openChangesTestDB(t)
	projectID := seedChangesProject(t, "CNP")
	issueID := seedChangesIssue(t, projectID, 1)
	userResult, err := db.DB.Exec(`INSERT INTO users(username,password,role) VALUES('control-side-door-user','x','admin')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := userResult.LastInsertId()
	runResult, err := db.DB.Exec(`INSERT INTO agent_runs(
		issue_id,project_id,requested_by,claimed_by,device_id,status,started_at)
		VALUES(?,?,?,?,?,'running',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		issueID, projectID, userID, userID, "device-a")
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := runResult.LastInsertId()
	// This test isolates the HTTP authorization side door, not target activation.
	// Remove only the fixture-only current-target trigger so an otherwise closed
	// lease row can name the exact run without constructing a delivery attempt.
	if _, err := db.DB.Exec(`DROP TRIGGER trg_control_lease_current_binding_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO control_capability_leases(
		lease_id,revision,actor_user_id,user_id,principal_kind,actor_api_key_id,device_id,
		delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		attempt_id,attempt_number,plan_revision,stage_key,execution_number,
		execution_start_stage_event_id,authority_epoch,authority_stage_event_id,reporter_id,
		agent_run_id,binding_digest,action_set_digest,action_count,expires_at)
		VALUES('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',1,?,?,'api_key',11,'device-a',
		1,'delivery:opaque',1,?,?,1,1,1,1,'implementation',1,1,1,1,1,?,zeroblob(32),zeroblob(32),1,
		strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour'))`, userID, userID, projectID, issueID, runID); err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	router.Patch("/api/runs/{id}", PatchAgentRun)
	for _, tc := range []struct {
		name     string
		keyID    int64
		deviceID string
	}{
		{name: "wrong key", keyID: 12, deviceID: "device-a"},
		{name: "wrong device", keyID: 11, deviceID: "device-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			principal, principalErr := auth.NewAPIKeyPrincipal(tc.keyID, userID,
				auth.ScopeSet{auth.ScopeAgentControlsRunner: {}})
			if principalErr != nil {
				t.Fatal(principalErr)
			}
			body := `{"status":"cancelled","if_status":"running","device_id":"` + tc.deviceID +
				`","cancellation_cause":"execution_timeout"}`
			request := httptest.NewRequest(http.MethodPatch, "/api/runs/"+strconv.FormatInt(runID, 10), strings.NewReader(body))
			ctx := context.WithValue(request.Context(), auth.UserKey, &models.User{ID: userID, Role: auth.RoleAdmin})
			request = request.WithContext(auth.WithPrincipal(ctx, principal))
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var status string
			if err := db.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&status); err != nil || status != "running" {
				t.Fatalf("run status=%q err=%v", status, err)
			}
			for _, query := range []string{
				`SELECT COUNT(*) FROM agent_run_cancellation_facts WHERE run_id=?`,
				`SELECT COUNT(*) FROM control_events WHERE cancellation_run_id=?`,
			} {
				var count int
				if err := db.DB.QueryRow(query, runID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("query=%q count=%d err=%v", query, count, err)
				}
			}
		})
	}
}

func TestNonOperatorCancellationFactAndEventAreAtomic(t *testing.T) {
	openChangesTestDB(t)
	runID := seedControlledCancellationRun(t, 1)
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE agent_runs SET status='cancelled',finished_at=datetime('now') WHERE id=? AND status='running'`, runID); err != nil {
		t.Fatal(err)
	}
	if err := recordNonOperatorCancellationTx(context.Background(), tx, runID, "execution_timeout"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var cause, event string
	if err := db.DB.QueryRow(`SELECT cancellation_cause FROM agent_run_cancellation_facts WHERE run_id=?`, runID).Scan(&cause); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT event_kind FROM control_events WHERE cancellation_run_id=?`, runID).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if cause != "execution_timeout" || event != "cancellation_recorded" {
		t.Fatalf("cause=%q event=%q", cause, event)
	}
}

func TestNonOperatorCancellationRollbackLeavesNoFactOrEvent(t *testing.T) {
	openChangesTestDB(t)
	runID := seedControlledCancellationRun(t, 2)
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE agent_runs SET status='cancelled',finished_at=datetime('now') WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if err := recordNonOperatorCancellationTx(context.Background(), tx, runID, "runner_shutdown"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM agent_run_cancellation_facts WHERE run_id=?`,
		`SELECT COUNT(*) FROM control_events WHERE cancellation_run_id=?`,
	} {
		var count int
		if err := db.DB.QueryRow(query, runID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("query=%q count=%d err=%v", query, count, err)
		}
	}
	var status string
	if err := db.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&status); err != nil || status != "running" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

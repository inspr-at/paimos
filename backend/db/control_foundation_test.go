// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package db

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	controlCredentialA = "12345678-1234-4234-9234-123456789abc"
	controlCredentialB = "abcdefab-cdef-4abc-8def-abcdefabcdef"
	controlUUIDA       = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	controlUUIDB       = "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb"
	controlTime        = "2026-08-21T12:00:00.000Z"
)

var canonicalV4UUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func controlDigest(label string) []byte {
	digest := sha256.Sum256([]byte(label))
	return digest[:]
}

type controlGraphFixture struct {
	database         *sql.DB
	deliveryID       int64
	deliveryKey      string
	deliveryRevision int64
	projectID        int64
	issueID          int64
	issueRevision    int64
	attemptID        int64
	reporterID       int64
	startEventID     int64
	authorityEventID int64
	authorityEpoch   int64
	runID            int64
	humanUserID      int64
	runnerUserID     int64
	runnerAPIKeyID   int64
	grantID          string
	leaseID          string
	grantBinding     []byte
	grantActions     []byte
	leaseBinding     []byte
	leaseActions     []byte
}

func seedControlGraph(t *testing.T, database *sql.DB) controlGraphFixture {
	return seedControlGraphWithExpiries(t, database, "+1 hour", "+1 hour")
}

func seedControlGraphWithExpiries(t *testing.T, database *sql.DB, grantExpiry, leaseExpiry string) controlGraphFixture {
	t.Helper()
	deliveryID, attemptID, reporterID, startEventID := seedDeliverySchemaGraph(t, database)
	fixture := controlGraphFixture{
		database:         database,
		deliveryID:       deliveryID,
		attemptID:        attemptID,
		reporterID:       reporterID,
		startEventID:     startEventID,
		authorityEventID: startEventID,
		authorityEpoch:   1,
		grantID:          controlUUIDA,
		leaseID:          controlUUIDB,
		grantBinding:     controlDigest("grant-binding"),
		grantActions:     controlDigest("grant-actions"),
		leaseBinding:     controlDigest("lease-binding"),
		leaseActions:     controlDigest("lease-actions"),
	}
	if err := database.QueryRow(`SELECT d.delivery_key,d.issue_id,i.project_id
		FROM deliveries d JOIN issues i ON i.id=d.issue_id WHERE d.id=?`, deliveryID).
		Scan(&fixture.deliveryKey, &fixture.issueID, &fixture.projectID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT revision FROM issue_control_revisions WHERE issue_id=?`, fixture.issueID).
		Scan(&fixture.issueRevision); err != nil {
		t.Fatal(err)
	}

	insertUser := func(username string) int64 {
		t.Helper()
		result, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES(?,'x','member','active')`, username)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	fixture.humanUserID = insertUser("control-human")
	fixture.runnerUserID = insertUser("control-runner")
	if _, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('control-human-bearer',?,'2026-12-01 00:00:00','2026-08-01 00:00:00',?)`,
		fixture.humanUserID, controlCredentialA); err != nil {
		t.Fatal(err)
	}
	key, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'control-runner',?,'paimos_runner','*')`, fixture.runnerUserID, strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	fixture.runnerAPIKeyID, _ = key.LastInsertId()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,device_id,requested_by,agent_name,status,started_at)
		VALUES(?,?,'runner-01',?,'control-runner','running',datetime('now'))`, fixture.issueID, fixture.projectID, fixture.humanUserID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.runID, _ = run.LastInsertId()
	if err := database.QueryRow(`SELECT COALESCE(MAX(delivery_revision),0)+1 FROM delivery_events WHERE delivery_id=?`, deliveryID).
		Scan(&fixture.deliveryRevision); err != nil {
		t.Fatal(err)
	}
	linkEnvelope, err := database.Exec(`INSERT INTO delivery_events(
		delivery_id,delivery_revision,idempotency_key,payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,?,?,zeroblob(32),'run_linked',?,'2026-08-20T10:01:00Z')`,
		deliveryID, fixture.deliveryRevision, "run-link", reporterID)
	if err != nil {
		t.Fatal(err)
	}
	linkEnvelopeID, _ := linkEnvelope.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_agent_run_links(
		agent_run_id,root_issue_id,delivery_id,attempt_id,stage_key,execution_number,
		execution_start_stage_event_id,reporter_id,link_delivery_event_id,created_at)
		VALUES(?,?,?,?,'specification',1,?,?,?,'2026-08-20T10:01:00Z')`,
		fixture.runID, fixture.issueID, deliveryID, attemptID, startEventID, reporterID, linkEnvelopeID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_agent_run_activations(
		delivery_id,attempt_id,stage_key,execution_number,authority_epoch,agent_run_id,reporter_id,
		authority_stage_event_id,telemetry_sequence_cutoff,created_at)
		VALUES(?,?,'specification',1,1,?,?,?,0,'2026-08-20T10:01:00Z')`,
		deliveryID, attemptID, fixture.runID, reporterID, startEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_stage_latest(
		delivery_id,attempt_id,stage_key,execution_number,authority_epoch,current_reporter_id,
		execution_start_stage_event_id,authority_stage_event_id,updated_at)
		VALUES(?,?,'specification',1,1,?,?,?,'2026-08-20T10:01:00Z')`,
		deliveryID, attemptID, reporterID, startEventID, startEventID); err != nil {
		t.Fatal(err)
	}

	if _, err := database.Exec(`INSERT INTO control_capability_grants(
		grant_id,revision,actor_user_id,user_id,principal_kind,actor_session_credential_id,
		delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		issue_etag_digest,binding_digest,action_set_digest,action_count,expires_at)
		VALUES(?,1,?,?,'session',?,?,?,?,?,?,?, ?,?,?,6,strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`,
		fixture.grantID, fixture.humanUserID, fixture.humanUserID, controlCredentialA,
		deliveryID, fixture.deliveryKey, fixture.deliveryRevision, fixture.projectID, fixture.issueID, fixture.issueRevision,
		controlDigest("issue-etag"), fixture.grantBinding, fixture.grantActions, grantExpiry); err != nil {
		t.Fatalf("insert grant: %v", err)
	}
	for _, action := range []string{"issue.priority.set", "run.cancel.queued", "run.cancel.running", "input.respond", "run.pause", "run.resume"} {
		if _, err := database.Exec(`INSERT INTO control_capability_grant_actions(grant_id,grant_revision,action) VALUES(?,1,?)`, fixture.grantID, action); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO control_capability_grant_seals(
		grant_id,grant_revision,binding_digest,action_set_digest,action_count) VALUES(?,1,?,?,6)`,
		fixture.grantID, fixture.grantBinding, fixture.grantActions); err != nil {
		t.Fatalf("seal grant: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO control_capability_leases(
		lease_id,revision,actor_user_id,user_id,principal_kind,actor_api_key_id,device_id,
		delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		attempt_id,attempt_number,plan_revision,stage_key,execution_number,execution_start_stage_event_id,
		authority_epoch,authority_stage_event_id,reporter_id,agent_run_id,binding_digest,
		action_set_digest,action_count,expires_at)
		VALUES(?,1,?,?,'api_key',?,'runner-01',?,?,?,?,?,?, ?,1,1,'specification',1,?, 1,?,?,?, ?,?,4,
		strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`,
		fixture.leaseID, fixture.runnerUserID, fixture.runnerUserID, fixture.runnerAPIKeyID,
		deliveryID, fixture.deliveryKey, fixture.deliveryRevision, fixture.projectID, fixture.issueID, fixture.issueRevision,
		attemptID, startEventID, startEventID, reporterID, fixture.runID, fixture.leaseBinding, fixture.leaseActions, leaseExpiry); err != nil {
		t.Fatalf("insert lease: %v", err)
	}
	for _, action := range []string{"run.cancel.running", "input.respond", "run.pause", "run.resume"} {
		if _, err := database.Exec(`INSERT INTO control_capability_lease_actions(lease_id,lease_revision,action) VALUES(?,1,?)`, fixture.leaseID, action); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO control_capability_lease_seals(
		lease_id,lease_revision,binding_digest,action_set_digest,action_count) VALUES(?,1,?,?,4)`,
		fixture.leaseID, fixture.leaseBinding, fixture.leaseActions); err != nil {
		t.Fatalf("seal lease: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO control_runtime_states(
		agent_run_id,delivery_id,root_issue_id,attempt_id,stage_key,
		execution_number,execution_start_stage_event_id,state,revision)
		VALUES(?,?,?,?,'specification',1,?,'running',1)`,
		fixture.runID, deliveryID, fixture.issueID, attemptID, startEventID); err != nil {
		t.Fatalf("insert runtime state: %v", err)
	}
	return fixture
}

func insertGrantAudit(t *testing.T, fixture controlGraphFixture, sequence, revision int, kind string, reason any) error {
	t.Helper()
	_, err := fixture.database.Exec(`INSERT INTO control_events(
		sequence,event_kind,grant_id,grant_revision,actor_user_id,user_id,principal_kind,
		actor_session_credential_id,actor_api_key_id,delivery_id,root_issue_id,issue_revision,
		binding_digest,action_set_digest,subject_expires_at,subject_updated_at,safe_reason)
		SELECT ?,?,grant_id,revision,actor_user_id,user_id,principal_kind,
		 actor_session_credential_id,actor_api_key_id,delivery_id,root_issue_id,issue_revision,
		 binding_digest,action_set_digest,expires_at,updated_at,?
		FROM control_capability_grants WHERE grant_id=? AND revision=?`,
		sequence, kind, reason, fixture.grantID, revision)
	return err
}

func insertLeaseAudit(t *testing.T, fixture controlGraphFixture, sequence, revision int, kind string, reason any) error {
	t.Helper()
	_, err := fixture.database.Exec(`INSERT INTO control_events(
		sequence,event_kind,lease_id,lease_revision,executor_user_id,executor_principal_kind,
		executor_api_key_id,device_id,delivery_id,root_issue_id,issue_revision,attempt_id,stage_key,
		execution_number,authority_epoch,reporter_id,agent_run_id,binding_digest,action_set_digest,
		subject_expires_at,subject_updated_at,safe_reason)
		SELECT ?,?,lease_id,revision,user_id,'api_key',actor_api_key_id,device_id,delivery_id,
		 root_issue_id,issue_revision,attempt_id,stage_key,execution_number,authority_epoch,reporter_id,
		 agent_run_id,binding_digest,action_set_digest,expires_at,updated_at,?
		FROM control_capability_leases WHERE lease_id=? AND revision=?`,
		sequence, kind, reason, fixture.leaseID, revision)
	return err
}

func insertControlRootAudits(t *testing.T, fixture controlGraphFixture) {
	t.Helper()
	if err := insertGrantAudit(t, fixture, 1, 1, "grant_issued", nil); err != nil {
		t.Fatalf("grant issued audit: %v", err)
	}
	if err := insertLeaseAudit(t, fixture, 1, 1, "lease_issued", nil); err != nil {
		t.Fatalf("lease issued audit: %v", err)
	}
}

func insertGrantRevision(fixture controlGraphFixture, grantID string, revision int, credential string, binding, actions []byte, actionCount int) error {
	return insertGrantRevisionAt(fixture, grantID, revision, credential, binding, actions, actionCount, nil)
}

func insertGrantRevisionAt(fixture controlGraphFixture, grantID string, revision int, credential string, binding, actions []byte, actionCount int, expiresAt any) error {
	_, err := fixture.database.Exec(`INSERT INTO control_capability_grants(
		grant_id,revision,actor_user_id,user_id,principal_kind,actor_session_credential_id,
		delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		issue_etag_digest,binding_digest,action_set_digest,action_count,expires_at)
		VALUES(?,?,?,?,'session',?,?,?,?,?,?,?, ?,?,?,?,COALESCE(?,strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour')))`,
		grantID, revision, fixture.humanUserID, fixture.humanUserID, credential,
		fixture.deliveryID, fixture.deliveryKey, fixture.deliveryRevision, fixture.projectID, fixture.issueID, fixture.issueRevision,
		controlDigest("issue-etag"), binding, actions, actionCount, expiresAt)
	return err
}

func insertLeaseRevision(fixture controlGraphFixture, leaseID string, revision int, apiKeyID int64, device string, binding, actions []byte, actionCount int) error {
	return insertLeaseRevisionAt(fixture, leaseID, revision, apiKeyID, device, binding, actions, actionCount, nil)
}

func insertLeaseRevisionAt(fixture controlGraphFixture, leaseID string, revision int, apiKeyID int64, device string, binding, actions []byte, actionCount int, expiresAt any) error {
	_, err := fixture.database.Exec(`INSERT INTO control_capability_leases(
		lease_id,revision,actor_user_id,user_id,principal_kind,actor_api_key_id,device_id,
		delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		attempt_id,attempt_number,plan_revision,stage_key,execution_number,execution_start_stage_event_id,
		authority_epoch,authority_stage_event_id,reporter_id,agent_run_id,binding_digest,
		action_set_digest,action_count,expires_at)
		VALUES(?,?,?,?,'api_key',?,?, ?,?,?,?,?,?, ?,1,1,'specification',1,?, ?,?,?,?, ?,?,?,
		COALESCE(?,strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour')))`,
		leaseID, revision, fixture.runnerUserID, fixture.runnerUserID, apiKeyID, device,
		fixture.deliveryID, fixture.deliveryKey, fixture.deliveryRevision, fixture.projectID, fixture.issueID, fixture.issueRevision,
		fixture.attemptID, fixture.startEventID, fixture.authorityEpoch, fixture.authorityEventID, fixture.reporterID, fixture.runID,
		binding, actions, actionCount, expiresAt)
	return err
}

func controlTimeOffset(t *testing.T, database *sql.DB, modifier string) string {
	t.Helper()
	var value string
	if err := database.QueryRow(`SELECT strftime('%Y-%m-%dT%H:%M:%fZ','now',?)`, modifier).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func insertPriorityCommand(fixture controlGraphFixture, commandID string, canonical []byte, expiresAt string) error {
	_, err := fixture.database.Exec(`INSERT INTO control_commands(
		command_id,actor_user_id,user_id,principal_kind,actor_session_credential_id,canonical_digest,
		grant_id,grant_revision,grant_expires_at,grant_binding_digest,grant_action_digest,action,status,
		challenge_template,delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		issue_etag_digest,target_snapshot_digest,priority_value,parameter_digest,expires_at)
		SELECT ?,actor_user_id,user_id,principal_kind,actor_session_credential_id,?,
		 grant_id,revision,expires_at,binding_digest,action_set_digest,'issue.priority.set','pending_confirmation',
		 'issue_priority_set',delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		 issue_etag_digest,?,'high',?,?
		FROM control_capability_grants WHERE grant_id=? AND revision=1`,
		commandID, canonical, controlDigest("priority-target"), controlDigest("priority-parameter"), expiresAt, fixture.grantID)
	return err
}

func insertPauseCommand(fixture controlGraphFixture, commandID string, canonical []byte, leaseRevision, runtimeRevision int, expiresAt string) error {
	_, err := fixture.database.Exec(`INSERT INTO control_commands(
		command_id,actor_user_id,user_id,principal_kind,actor_session_credential_id,canonical_digest,
		grant_id,grant_revision,grant_expires_at,grant_binding_digest,grant_action_digest,action,status,
		challenge_template,delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		issue_etag_digest,target_snapshot_digest,attempt_id,attempt_number,plan_revision,stage_key,
		execution_number,execution_start_stage_event_id,authority_epoch,authority_stage_event_id,reporter_id,
		agent_run_id,lease_id,lease_revision,lease_expires_at,lease_binding_digest,lease_action_digest,
		runtime_revision,parameter_digest,expires_at)
		SELECT ?,grant_row.actor_user_id,grant_row.user_id,grant_row.principal_kind,
		 grant_row.actor_session_credential_id,?,grant_row.grant_id,grant_row.revision,grant_row.expires_at,
		 grant_row.binding_digest,grant_row.action_set_digest,'run.pause','pending_confirmation','run_pause',
		 grant_row.delivery_id,grant_row.delivery_key,grant_row.delivery_revision,grant_row.project_id,
		 grant_row.root_issue_id,grant_row.issue_revision,grant_row.issue_etag_digest,?,
		 lease.attempt_id,lease.attempt_number,lease.plan_revision,lease.stage_key,lease.execution_number,
		 lease.execution_start_stage_event_id,lease.authority_epoch,lease.authority_stage_event_id,
		 lease.reporter_id,lease.agent_run_id,lease.lease_id,lease.revision,lease.expires_at,
		 lease.binding_digest,lease.action_set_digest,?,?,?
		FROM control_capability_grants grant_row JOIN control_capability_leases lease
		 ON lease.delivery_id=grant_row.delivery_id
		WHERE grant_row.grant_id=? AND grant_row.revision=1 AND lease.lease_id=? AND lease.revision=?`,
		commandID, canonical, controlDigest("pause-target"), runtimeRevision, controlDigest("pause-parameter"), expiresAt,
		fixture.grantID, fixture.leaseID, leaseRevision)
	return err
}

type controlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertCommandAudit(t *testing.T, fixture controlGraphFixture, commandID string, sequence int, kind string) error {
	return insertCommandAuditOn(t, fixture.database, commandID, sequence, kind)
}

func insertCommandAuditOn(t *testing.T, execer controlExecer, commandID string, sequence int, kind string) error {
	t.Helper()
	executor := kind == "effect_claimed" || kind == "effect_outcome_unknown" || kind == "effect_acknowledged" ||
		kind == "effect_reconciled" || kind == "runtime_changed"
	runtime := kind == "runtime_changed"
	_, err := execer.Exec(`INSERT INTO control_events(
		sequence,event_kind,command_id,command_status_revision,actor_user_id,user_id,principal_kind,
		actor_session_credential_id,actor_api_key_id,executor_user_id,executor_principal_kind,
		executor_api_key_id,device_id,delivery_id,root_issue_id,issue_revision,attempt_id,stage_key,
		execution_number,authority_epoch,reporter_id,agent_run_id,action,command_status,runtime_state,
		runtime_revision,outcome,safe_reason,parameter_digest,binding_digest,action_set_digest,result_digest)
		SELECT ?,?,command.command_id,command.status_revision,command.actor_user_id,command.user_id,
		 command.principal_kind,command.actor_session_credential_id,command.actor_api_key_id,
		 CASE WHEN ? THEN outbox.claim_user_id END,CASE WHEN ? THEN 'api_key' END,
		 CASE WHEN ? THEN outbox.claim_api_key_id END,CASE WHEN ? THEN outbox.claim_device_id END,
		 command.delivery_id,command.root_issue_id,command.issue_revision,command.attempt_id,command.stage_key,
		 command.execution_number,command.authority_epoch,command.reporter_id,command.agent_run_id,command.action,
		 command.status,CASE WHEN ? THEN runtime.state END,CASE WHEN ? THEN runtime.revision END,
		 command.outcome,command.safe_reason,command.parameter_digest,command.target_snapshot_digest,
		 command.grant_action_digest,command.result_digest
		FROM control_commands command
		LEFT JOIN control_outbox outbox ON outbox.command_id=command.command_id
		LEFT JOIN control_runtime_states runtime ON runtime.agent_run_id=command.agent_run_id
		WHERE command.command_id=?`,
		sequence, kind, executor, executor, executor, executor, runtime, runtime, commandID)
	return err
}

func insertInputRevisionOn(execer controlExecer, fixture controlGraphFixture, requestID string, revision, leaseRevision int,
	requestKind, promptTemplate string, optionCount int, digest []byte, expiresAt string) error {
	_, err := execer.Exec(`INSERT INTO control_input_requests(
		request_id,revision,lease_id,lease_revision,delivery_id,delivery_key,delivery_revision,
		project_id,root_issue_id,issue_revision,attempt_id,attempt_number,plan_revision,stage_key,
		execution_number,execution_start_stage_event_id,authority_epoch,authority_stage_event_id,
		reporter_id,agent_run_id,request_kind,prompt_template,option_count,request_digest,expires_at)
		SELECT ?,?,lease_id,revision,delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,
		 issue_revision,attempt_id,attempt_number,plan_revision,stage_key,execution_number,
		 execution_start_stage_event_id,authority_epoch,authority_stage_event_id,reporter_id,agent_run_id,
		 ?,?,?,?,?
		FROM control_capability_leases WHERE lease_id=? AND revision=?`,
		requestID, revision, requestKind, promptTemplate, optionCount, digest, expiresAt, fixture.leaseID, leaseRevision)
	return err
}

func insertInputAuditOn(t *testing.T, execer controlExecer, requestID string, sequence, revision int, kind string) error {
	t.Helper()
	requested := kind == "input_requested"
	_, err := execer.Exec(`INSERT INTO control_events(
		sequence,event_kind,input_request_id,input_request_revision,executor_user_id,
		executor_principal_kind,executor_api_key_id,device_id,delivery_id,root_issue_id,issue_revision,
		attempt_id,stage_key,execution_number,authority_epoch,reporter_id,agent_run_id,binding_digest,
		parameter_digest,result_digest,safe_reason)
		SELECT ?,?,request.request_id,request.revision,
		 CASE WHEN ? THEN lease.user_id END,CASE WHEN ? THEN 'api_key' END,
		 CASE WHEN ? THEN lease.actor_api_key_id END,CASE WHEN ? THEN lease.device_id END,
		 request.delivery_id,request.root_issue_id,request.issue_revision,request.attempt_id,request.stage_key,
		 request.execution_number,request.authority_epoch,request.reporter_id,request.agent_run_id,lease.binding_digest,
		 CASE WHEN ? THEN request.request_digest END,CASE WHEN ? THEN NULL ELSE terminal.event_digest END,
		 CASE WHEN ? THEN NULL ELSE terminal.safe_reason END
		FROM control_input_requests request
		JOIN control_capability_leases lease ON lease.lease_id=request.lease_id AND lease.revision=request.lease_revision
		LEFT JOIN control_input_resolution_events terminal ON terminal.request_id=request.request_id
		 AND terminal.request_revision=request.revision
		WHERE request.request_id=? AND request.revision=?`,
		sequence, kind, requested, requested, requested, requested, requested, requested, requested, requestID, revision)
	return err
}

func openThroughMigration(t *testing.T, version int) *sql.DB {
	t.Helper()
	t.Setenv("PAIMOS_TEST_MODE", "1")
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := migrateThrough(database, version); err != nil {
		t.Fatalf("migrate through %d: %v", version, err)
	}
	return database
}

func requireConstraint(t *testing.T, operation func() error) {
	t.Helper()
	if err := operation(); err == nil {
		t.Fatal("operation unexpectedly succeeded")
	}
}

func TestM147PopulatedUpgradeBackfillsSafeCredentialAndControlBaselines(t *testing.T) {
	database := openThroughMigration(t, 146)
	user, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('m147-user','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	for _, bearer := range []string{"raw-cookie-secret-A", "raw-cookie-secret-B"} {
		if _, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at) VALUES(?,?,?,?)`,
			bearer, userID, "2026-09-21 12:00:00", "2026-08-21 12:00:00"); err != nil {
			t.Fatal(err)
		}
	}
	// M89 gave created_at an empty default. These are the exact column
	// shapes used by the legacy post-M89 TOTP and dev-login mint paths.
	if _, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at)
		VALUES('legacy-totp-bearer',?,datetime('now','+1 hour'))`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,via_dev_login)
		VALUES('legacy-dev-bearer',?,datetime('now','+1 hour'),1)`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at)
		VALUES('legacy-malformed-created',?,datetime('now','+1 hour'),'not-a-time')`, userID); err != nil {
		t.Fatal(err)
	}
	var emptyCreated int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sessions
		WHERE id IN ('legacy-totp-bearer','legacy-dev-bearer') AND created_at=''`).Scan(&emptyCreated); err != nil {
		t.Fatal(err)
	}
	if emptyCreated != 2 {
		t.Fatalf("legacy empty-created_at fixture rows=%d, want 2", emptyCreated)
	}
	if _, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'legacy',?,'paimos_legacy','*')`, userID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M147','M147')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,title) VALUES(?,1,'M147 issue')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()

	if err := migrateThrough(database, 147); err != nil {
		t.Fatalf("apply M147: %v", err)
	}
	rows, err := database.Query(`SELECT id,credential_id,created_at,via_dev_login FROM sessions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	for rows.Next() {
		var bearer, credential, createdAt string
		var viaDevLogin int
		if err := rows.Scan(&bearer, &credential, &createdAt, &viaDevLogin); err != nil {
			t.Fatal(err)
		}
		if !canonicalV4UUID.MatchString(credential) || credential == bearer {
			t.Fatalf("unsafe session identity bearer=%q credential=%q", bearer, credential)
		}
		if _, duplicate := seen[credential]; duplicate {
			t.Fatalf("duplicate backfilled credential %q", credential)
		}
		seen[credential] = struct{}{}
		switch bearer {
		case "legacy-totp-bearer":
			if createdAt == "" || viaDevLogin != 0 {
				t.Fatalf("legacy TOTP session was not safely repaired: created_at=%q via_dev_login=%d", createdAt, viaDevLogin)
			}
		case "legacy-dev-bearer":
			if createdAt == "" || viaDevLogin != 1 {
				t.Fatalf("legacy dev session was not safely repaired: created_at=%q via_dev_login=%d", createdAt, viaDevLogin)
			}
		case "legacy-malformed-created":
			if createdAt != "not-a-time" {
				t.Fatalf("nonempty malformed created_at was silently repaired: %q", createdAt)
			}
		}
	}
	if len(seen) != 5 {
		t.Fatalf("backfilled credentials=%d, want 5", len(seen))
	}
	// The two compatible legacy rows remain live under the same admission
	// predicates and can still receive a normal sliding expiry renewal.
	var admissible int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sessions session
		JOIN users user ON user.id=session.user_id
		WHERE session.id IN ('legacy-totp-bearer','legacy-dev-bearer')
		 AND session.created_at<>'' AND datetime(session.created_at) IS NOT NULL
		 AND session.expires_at>datetime('now') AND user.status='active'
		 AND session.credential_id IS NOT NULL`).Scan(&admissible); err != nil {
		t.Fatal(err)
	}
	if admissible != 2 {
		t.Fatalf("repaired legacy sessions admissible=%d, want 2", admissible)
	}
	if _, err := database.Exec(`UPDATE sessions SET expires_at=datetime('now','+2 hours')
		WHERE id IN ('legacy-totp-bearer','legacy-dev-bearer')`); err != nil {
		t.Fatalf("repaired legacy sessions could not survive renewal: %v", err)
	}
	var retained int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sessions
		WHERE id IN ('legacy-totp-bearer','legacy-dev-bearer')`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 2 {
		t.Fatalf("repaired legacy sessions retained=%d, want 2", retained)
	}
	var disabled, expires sql.NullString
	if err := database.QueryRow(`SELECT disabled_at,expires_at FROM api_keys WHERE user_id=?`, userID).Scan(&disabled, &expires); err != nil {
		t.Fatal(err)
	}
	if disabled.Valid || expires.Valid {
		t.Fatalf("legacy API key did not remain enabled/nonexpiring: disabled=%v expires=%v", disabled, expires)
	}
	var revision int
	if err := database.QueryRow(`SELECT revision FROM issue_control_revisions WHERE issue_id=?`, issueID).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("issue baseline revision=%d err=%v, want 1", revision, err)
	}
}

func TestM147PreconditionRejectsOwnedObjectCollisions(t *testing.T) {
	tests := []struct {
		name string
		seed string
	}{
		{"table", `CREATE TABLE control_events(id INTEGER)`},
		{"index", `CREATE INDEX idx_control_events_delivery_tail ON users(id)`},
		{"trigger", `CREATE TRIGGER trg_control_events_probe AFTER UPDATE ON users BEGIN SELECT 1; END`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openThroughMigration(t, 146)
			if _, err := database.Exec(test.seed); err != nil {
				t.Fatal(err)
			}
			err := migrateThrough(database, 147)
			if err == nil || !strings.Contains(err.Error(), "M147 schema is partially present") {
				t.Fatalf("collision was not rejected before M147: %v", err)
			}
			if tableExists(t, database, "control_operation_keys") {
				t.Fatal("precondition failure left partial M147 schema")
			}
		})
	}
}

func TestM147IsPureNonIdempotentSQL(t *testing.T) {
	body, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(body), "// M147 / PAI-809")
	if start < 0 {
		t.Fatal("could not isolate M147 source")
	}
	// Bound the slice at the next migration block: later migrations (for
	// example the M155 target-table rebuild) may legitimately use the
	// foreign-key pragma without changing what M147 itself contains.
	end := strings.Index(string(body)[start:], "\n\t\t// M148 / PAI-810")
	if end < 0 {
		t.Fatal("could not isolate M147 source")
	}
	migration147 := string(body)[start : start+end]
	if strings.Contains(strings.ToUpper(migration147), "PRAGMA FOREIGN_KEYS") {
		t.Fatal("M147 must not use or mention the foreign-key pragma")
	}
	if strings.Contains(strings.ToUpper(migration147), "IF NOT EXISTS") {
		t.Fatal("M147 must not mask incompatible objects with IF NOT EXISTS")
	}
}

func TestM147SessionAPIKeyAndIssueGuards(t *testing.T) {
	database := openTestDB(t)
	user, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('guards','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	otherUser, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('guards-other','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	otherUserID, _ := otherUser.LastInsertId()
	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
			VALUES(?,?,'2026-12-01 00:00:00','2026-08-01 00:00:00',?)`,
			controlCredentialA, userID, controlCredentialA)
		return err
	})

	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at) VALUES('missing-credential',?,?,?)`,
			userID, "2026-09-21 12:00:00", "2026-08-21 12:00:00")
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id) VALUES('bad-credential',?,?,?,'ABC')`,
			userID, "2026-09-21 12:00:00", "2026-08-21 12:00:00")
		return err
	})
	if _, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id) VALUES('safe-bearer',?,?,?,?)`,
		userID, "2026-09-21 12:00:00", "2026-08-21 12:00:00", controlCredentialA); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{
		`UPDATE sessions SET id='replacement-bearer' WHERE credential_id='` + controlCredentialA + `'`,
		`UPDATE sessions SET credential_id='` + controlCredentialB + `' WHERE credential_id='` + controlCredentialA + `'`,
		`UPDATE sessions SET user_id=` + fmt.Sprint(otherUserID) + ` WHERE credential_id='` + controlCredentialA + `'`,
		`UPDATE sessions SET created_at='2026-08-20 12:00:00' WHERE credential_id='` + controlCredentialA + `'`,
		`UPDATE sessions SET via_dev_login=1 WHERE credential_id='` + controlCredentialA + `'`,
		`UPDATE sessions SET via_oidc=1 WHERE credential_id='` + controlCredentialA + `'`,
	} {
		requireConstraint(t, func() error { _, err := database.Exec(mutation); return err })
	}
	if _, err := database.Exec(`UPDATE sessions SET expires_at='2026-10-01 12:00:00' WHERE credential_id=?`, controlCredentialA); err != nil {
		t.Fatalf("mutable session expiry was rejected: %v", err)
	}

	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes,expires_at)
			VALUES(?,'bad-time',?,'paimos_bad','*','2026-08-21 12:00:00')`, userID, strings.Repeat("b", 64))
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes,expires_at)
			VALUES(?,'bad-time-null',?,'paimos_bad2','*',?)`, userID, strings.Repeat("f", 64), strings.Repeat("x", 24))
		return err
	})
	key, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'guarded',?,'paimos_guard','*')`, userID, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := key.LastInsertId()
	if _, err := database.Exec(`UPDATE api_keys SET disabled_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, keyID); err != nil {
		t.Fatalf("first disable: %v", err)
	}
	for _, mutation := range []string{
		`UPDATE api_keys SET disabled_at=NULL WHERE id=?`,
		`UPDATE api_keys SET disabled_at='2030-01-01T00:00:00.000Z' WHERE id=?`,
		`UPDATE api_keys SET key_hash='` + strings.Repeat("d", 64) + `' WHERE id=?`,
		`UPDATE api_keys SET user_id=` + fmt.Sprint(otherUserID) + ` WHERE id=?`,
	} {
		requireConstraint(t, func() error { _, err := database.Exec(mutation, keyID); return err })
	}

	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Revision','REV')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,title) VALUES(?,1,'Revision')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	if _, err := database.Exec(`UPDATE issues SET title='Revision two' WHERE id=?`, issueID); err != nil {
		t.Fatal(err)
	}
	var revision int
	if err := database.QueryRow(`SELECT revision FROM issue_control_revisions WHERE issue_id=?`, issueID).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("issue revision=%d err=%v, want exactly 2", revision, err)
	}
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE issue_control_revisions SET revision=revision+1,
			updated_at='2030-01-01T00:00:00.000Z' WHERE issue_id=?`, issueID)
		return err
	})
	if _, err := database.Exec(`UPDATE issues SET title='Revision three' WHERE id=?`, issueID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE issues SET title='Revision four' WHERE id=?`, issueID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT revision FROM issue_control_revisions WHERE issue_id=?`, issueID).Scan(&revision); err != nil || revision != 4 {
		t.Fatalf("two ordinary issue updates produced revision=%d err=%v, want 4", revision, err)
	}
	for _, value := range []int{1, 4, 9} {
		requireConstraint(t, func() error {
			_, err := database.Exec(`UPDATE issue_control_revisions SET revision=? WHERE issue_id=?`, value, issueID)
			return err
		})
	}
}

func TestM147ControlTimestampChecksAreCanonicalAndTotal(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.Exec(`CREATE TABLE control_timestamp_probe(
		required_at TEXT CHECK(` + sqlControlTimestampCheck("required_at") + `),
		optional_at TEXT CHECK(` + sqlNullableControlTimestampCheck("optional_at") + `))`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_timestamp_probe(required_at,optional_at)
		VALUES(strftime('%Y-%m-%dT%H:%M:%fZ','now'),NULL)`); err != nil {
		t.Fatalf("canonical timestamp/null optional pair: %v", err)
	}
	invalid := []string{
		strings.Repeat("x", 24),
		"2026-99-99T99:99:99.999Z",
		"2026-02-30T12:00:00.000Z",
		"2026-08-21 12:00:00.000Z",
		"2026-08-21T12:00:00Z",
		"2026-08-21T12:00:00.0000Z",
		"2026-08-21T12:00:00.000+00:00",
	}
	for _, value := range invalid {
		value := value
		t.Run(value, func(t *testing.T) {
			requireConstraint(t, func() error {
				_, err := database.Exec(`INSERT INTO control_timestamp_probe(required_at) VALUES(?)`, value)
				return err
			})
			requireConstraint(t, func() error {
				_, err := database.Exec(`INSERT INTO control_timestamp_probe(required_at,optional_at)
					VALUES(strftime('%Y-%m-%dT%H:%M:%fZ','now'),?)`, value)
				return err
			})
		})
	}
}

func TestM147OperationKeysUseTypedHashedNamespaces(t *testing.T) {
	database := openTestDB(t)
	digestA := make([]byte, 32)
	digestB := make([]byte, 32)
	digestC := make([]byte, 32)
	digestA[0], digestB[0], digestC[0] = 1, 2, 3

	insert := func(kind, operation string, session any, api any, key, request, result []byte, subjectColumn string, subject any) error {
		_, err := database.Exec(`INSERT INTO control_operation_keys(
			actor_user_id,user_id,principal_kind,actor_session_credential_id,actor_api_key_id,
			operation_kind,operation_key_digest,request_digest,result_digest,`+subjectColumn+`)
			VALUES(1,1,?,?,?,?,?,?,?,?)`, kind, session, api, operation, key, request, result, subject)
		return err
	}
	if err := insert("session", "grant.put", controlCredentialA, nil, digestA, digestB, digestC, "grant_id", controlUUIDA); err != nil {
		t.Fatalf("session operation key: %v", err)
	}
	requireConstraint(t, func() error {
		return insert("session", "grant.put", controlCredentialA, nil, digestA, digestC, digestB, "grant_id", controlUUIDA)
	})
	if err := insert("api_key", "grant.put", nil, int64(9), digestA, digestB, digestC, "grant_id", controlUUIDA); err != nil {
		t.Fatalf("typed API-key namespace collided with session: %v", err)
	}
	requireConstraint(t, func() error {
		return insert("session", "lease.issue", controlCredentialA, nil, digestB, digestB, digestC, "lease_id", controlUUIDB)
	})
	if err := insert("api_key", "lease.issue", nil, int64(9), digestB, digestB, digestC, "lease_id", controlUUIDB); err != nil {
		t.Fatalf("runner API-key operation: %v", err)
	}
	rawKey := "PRIVATE-IDEMPOTENCY-" + strings.Repeat("K", 108)
	if len(rawKey) != 128 {
		t.Fatalf("idempotency canary length=%d", len(rawKey))
	}
	rawKeyDigest := sha256.Sum256([]byte(rawKey))
	if err := insert("session", "grant.revoke", controlCredentialA, nil, rawKeyDigest[:], digestB, digestC, "grant_id", controlUUIDA); err != nil {
		t.Fatalf("hashed 128-byte operation key: %v", err)
	}
	var leaked int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_operation_keys
		WHERE instr(CAST(operation_key_digest AS TEXT),?)>0 OR instr(CAST(request_digest AS TEXT),?)>0
		 OR instr(CAST(result_digest AS TEXT),?)>0`, rawKey, rawKey, rawKey).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("raw idempotency key was persisted in a control row")
	}
	var databasePath string
	if err := database.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&databasePath); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	databaseBytes, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(databaseBytes), rawKey) {
		t.Fatal("raw idempotency key leaked into the SQLite file")
	}
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_operation_keys SET result_digest=? WHERE id=1`, digestA)
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`DELETE FROM control_operation_keys WHERE id=1`)
		return err
	})
}

func TestM147ControlGraphFixture(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	for table, want := range map[string]int{
		"control_capability_grants":      1,
		"control_capability_grant_seals": 1,
		"control_capability_leases":      1,
		"control_capability_lease_seals": 1,
		"control_runtime_states":         1,
	} {
		var got int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count=%d err=%v, want %d", table, got, err, want)
		}
	}
	if fixture.deliveryRevision != 3 {
		t.Fatalf("delivery revision=%d, want 3", fixture.deliveryRevision)
	}
}

func TestM147CapabilityLineageSealsAndAuditGraph(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)

	if err := insertGrantAudit(t, fixture, 1, 1, "grant_issued", nil); err != nil {
		t.Fatalf("grant issued audit: %v", err)
	}
	if err := insertLeaseAudit(t, fixture, 1, 1, "lease_issued", nil); err != nil {
		t.Fatalf("lease issued audit: %v", err)
	}
	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO control_capability_grant_actions(grant_id,grant_revision,action)
			VALUES(?,1,'issue.priority.set')`, fixture.grantID)
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO control_capability_lease_actions(lease_id,lease_revision,action)
			VALUES(?,1,'input.respond')`, fixture.leaseID)
		return err
	})

	if _, err := database.Exec(`UPDATE control_capability_grants
		SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE grant_id=? AND revision=1`, fixture.grantID); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	if _, err := database.Exec(`UPDATE control_capability_leases
		SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE lease_id=? AND revision=1`, fixture.leaseID); err != nil {
		t.Fatalf("revoke lease: %v", err)
	}
	if err := insertGrantAudit(t, fixture, 2, 1, "grant_revoked", "capability_revoked"); err != nil {
		t.Fatalf("grant revoked audit: %v", err)
	}
	if err := insertLeaseAudit(t, fixture, 2, 1, "lease_revoked", "lease_revoked"); err != nil {
		t.Fatalf("lease revoked audit: %v", err)
	}

	// Terminal rows cannot drift away from the immutable terminal audit proof.
	time.Sleep(2 * time.Millisecond)
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_capability_grants SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE grant_id=? AND revision=1`, fixture.grantID)
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_capability_leases SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE lease_id=? AND revision=1`, fixture.leaseID)
		return err
	})

	if _, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('control-human-bearer-2',?,'2026-12-01 00:00:00','2026-08-01 00:00:00',?)`,
		fixture.humanUserID, controlCredentialB); err != nil {
		t.Fatal(err)
	}
	secondKey, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'control-runner-2',?,'paimos_runner2','*')`, fixture.runnerUserID, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	secondKeyID, _ := secondKey.LastInsertId()

	requireConstraint(t, func() error {
		return insertGrantRevision(fixture, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", 1,
			controlCredentialB, controlDigest("wrong-grant"), controlDigest("wrong-actions"), 1)
	})
	requireConstraint(t, func() error {
		return insertLeaseRevision(fixture, "dddddddd-dddd-4ddd-8ddd-dddddddddddd", 1,
			secondKeyID, "runner-02", controlDigest("wrong-lease"), controlDigest("wrong-lease-actions"), 1)
	})

	grantBinding2, grantActions2 := controlDigest("grant-binding-2"), controlDigest("grant-actions-2")
	if err := insertGrantRevision(fixture, fixture.grantID, 2, controlCredentialB, grantBinding2, grantActions2, 1); err != nil {
		t.Fatalf("grant revision 2: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO control_capability_grant_actions(grant_id,grant_revision,action)
		VALUES(?,2,'issue.priority.set')`, fixture.grantID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_capability_grant_seals(
		grant_id,grant_revision,binding_digest,action_set_digest,action_count) VALUES(?,2,?,?,1)`,
		fixture.grantID, grantBinding2, grantActions2); err != nil {
		t.Fatal(err)
	}
	if err := insertGrantAudit(t, fixture, 3, 2, "grant_renewed", nil); err != nil {
		t.Fatalf("grant revision audit after terminal revision: %v", err)
	}

	leaseBinding2, leaseActions2 := controlDigest("lease-binding-2"), controlDigest("lease-actions-2")
	if err := insertLeaseRevision(fixture, fixture.leaseID, 2, secondKeyID, "runner-02", leaseBinding2, leaseActions2, 1); err != nil {
		t.Fatalf("lease revision 2: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO control_capability_lease_actions(lease_id,lease_revision,action)
		VALUES(?,2,'input.respond')`, fixture.leaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_capability_lease_seals(
		lease_id,lease_revision,binding_digest,action_set_digest,action_count) VALUES(?,2,?,?,1)`,
		fixture.leaseID, leaseBinding2, leaseActions2); err != nil {
		t.Fatal(err)
	}
	if err := insertLeaseAudit(t, fixture, 3, 2, "lease_renewed", nil); err != nil {
		t.Fatalf("lease revision audit after terminal revision: %v", err)
	}

	// A terminal fact is per revision: no same-revision resurrection is legal.
	if _, err := database.Exec(`UPDATE control_capability_grants
		SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE grant_id=? AND revision=2`, fixture.grantID); err != nil {
		t.Fatal(err)
	}
	if err := insertGrantAudit(t, fixture, 4, 2, "grant_revoked", "capability_revoked"); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertGrantAudit(t, fixture, 5, 2, "grant_renewed", nil) })

	if _, err := database.Exec(`UPDATE control_capability_leases
		SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE lease_id=? AND revision=2`, fixture.leaseID); err != nil {
		t.Fatal(err)
	}
	if err := insertLeaseAudit(t, fixture, 4, 2, "lease_revoked", "lease_revoked"); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertLeaseAudit(t, fixture, 5, 2, "lease_renewed", nil) })
}

func TestM147CommandAndOutboxAuditOrdering(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	expires := controlTimeOffset(t, database, "+5 minutes")

	priorityID := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	if err := insertPriorityCommand(fixture, priorityID, controlDigest("priority-command"), expires); err != nil {
		t.Fatalf("insert priority command: %v", err)
	}
	if err := insertCommandAudit(t, fixture, priorityID, 1, "command_created"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_commands SET status='accepted',status_revision=2,
		accepted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, priorityID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, priorityID, 2, "command_accepted"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_commands SET status='applied',status_revision=3,outcome='applied',
		result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, controlDigest("priority-result"), priorityID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, priorityID, 3, "command_applied"); err != nil {
		t.Fatalf("synchronous terminal audit: %v", err)
	}

	pauseID := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	pauseCanonical := controlDigest("pause-command")
	if err := insertPauseCommand(fixture, pauseID, pauseCanonical, 1, 1, expires); err != nil {
		t.Fatalf("insert pause command: %v", err)
	}
	if err := insertCommandAudit(t, fixture, pauseID, 1, "command_created"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_commands SET status='accepted',status_revision=2,
		accepted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, pauseID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, pauseID, 2, "command_accepted"); err != nil {
		t.Fatal(err)
	}

	// An async command cannot acquire a terminal audit before its exact effect
	// delivery chain. Keep the deliberately invalid update inside a rollback.
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE control_commands SET status='applied',status_revision=3,outcome='applied',
		result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, controlDigest("pause-result"), pauseID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := insertCommandAuditOn(t, tx, pauseID, 3, "command_applied"); err == nil {
		_ = tx.Rollback()
		t.Fatal("async command acquired terminal audit before queue/claim/result")
	}
	_ = tx.Rollback()

	if _, err := database.Exec(`INSERT INTO control_outbox(
		command_id,lease_id,lease_revision,delivery_state,effect_digest)
		VALUES(?,?,1,'queued',?)`, pauseID, fixture.leaseID, pauseCanonical); err != nil {
		t.Fatalf("queue effect: %v", err)
	}
	if err := insertCommandAudit(t, fixture, pauseID, 3, "effect_queued"); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_outbox SET delivery_state='claimed',claim_sequence=1,
			claim_user_id=?,claim_principal_kind='session',claim_session_credential_id=?,claim_device_id='runner-01',
			claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE command_id=?`, fixture.runnerUserID, controlCredentialA, pauseID)
		return err
	})
	if _, err := database.Exec(`UPDATE control_outbox SET delivery_state='claimed',claim_sequence=1,
		claim_user_id=?,claim_principal_kind='api_key',claim_api_key_id=?,claim_device_id='runner-01',
		claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, fixture.runnerUserID, fixture.runnerAPIKeyID, pauseID); err != nil {
		t.Fatalf("claim effect: %v", err)
	}
	if err := insertCommandAudit(t, fixture, pauseID, 4, "effect_claimed"); err != nil {
		t.Fatal(err)
	}

	pauseResult := controlDigest("pause-result")
	if _, err := database.Exec(`UPDATE control_commands SET status='applied',status_revision=3,outcome='applied',
		result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, pauseResult, pauseID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_outbox SET delivery_state='acknowledged',result_sequence=1,
		result_digest=?,result_outcome='applied',acknowledged_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, pauseResult, pauseID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, pauseID, 5, "command_applied"); err != nil {
		t.Fatalf("async terminal audit after result proof: %v", err)
	}

	tx, err = database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE control_runtime_states SET state='paused',revision=2,last_command_id=?,
		last_result_digest=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE agent_run_id=?`,
		pauseID, pauseResult, fixture.runID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := insertCommandAuditOn(t, tx, pauseID, 6, "runtime_changed"); err == nil {
		_ = tx.Rollback()
		t.Fatal("runtime audit preceded effect acknowledgement audit")
	}
	_ = tx.Rollback()

	if err := insertCommandAudit(t, fixture, pauseID, 6, "effect_acknowledged"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_runtime_states SET state='paused',revision=2,last_command_id=?,
		last_result_digest=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE agent_run_id=?`,
		pauseID, pauseResult, fixture.runID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, pauseID, 7, "runtime_changed"); err != nil {
		t.Fatalf("runtime audit after acknowledged effect: %v", err)
	}
}

func TestM147RuntimeStateSurvivesLeaseCredentialRotation(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	if _, err := database.Exec(`UPDATE control_capability_leases
		SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE lease_id=? AND revision=1`, fixture.leaseID); err != nil {
		t.Fatal(err)
	}
	if err := insertLeaseAudit(t, fixture, 2, 1, "lease_revoked", "lease_revoked"); err != nil {
		t.Fatal(err)
	}
	secondKey, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'rotated-runner',?,'paimos_rotated','*')`, fixture.runnerUserID, strings.Repeat("7", 64))
	if err != nil {
		t.Fatal(err)
	}
	secondKeyID, _ := secondKey.LastInsertId()
	binding, actions := controlDigest("rotated-lease-binding"), controlDigest("rotated-lease-actions")
	if err := insertLeaseRevision(fixture, fixture.leaseID, 2, secondKeyID, "runner-rotated", binding, actions, 2); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"run.pause", "run.resume"} {
		if _, err := database.Exec(`INSERT INTO control_capability_lease_actions(lease_id,lease_revision,action) VALUES(?,2,?)`, fixture.leaseID, action); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO control_capability_lease_seals(
		lease_id,lease_revision,binding_digest,action_set_digest,action_count) VALUES(?,2,?,?,2)`,
		fixture.leaseID, binding, actions); err != nil {
		t.Fatal(err)
	}
	if err := insertLeaseAudit(t, fixture, 3, 2, "lease_renewed", nil); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error {
		return insertPauseCommand(fixture, "11111111-1111-4111-8111-111111111111",
			controlDigest("stale-lease-pause"), 1, 1, controlTimeOffset(t, database, "+5 minutes"))
	})
	commandID := "12121212-1212-4212-8212-121212121212"
	canonical := controlDigest("rotated-pause")
	if err := insertPauseCommand(fixture, commandID, canonical, 2, 1,
		controlTimeOffset(t, database, "+5 minutes")); err != nil {
		t.Fatalf("new lease could not act on stable runtime revision: %v", err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 1, "command_created"); err != nil {
		t.Fatal(err)
	}
	acceptControlCommand(t, fixture, commandID)
	if err := insertCommandAudit(t, fixture, commandID, 2, "command_accepted"); err != nil {
		t.Fatal(err)
	}
	queueControlEffect(t, fixture, commandID, 2, canonical)
	if err := insertCommandAudit(t, fixture, commandID, 3, "effect_queued"); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_outbox SET delivery_state='claimed',claim_sequence=1,
			claim_user_id=?,claim_principal_kind='api_key',claim_api_key_id=?,claim_device_id='runner-01',
			claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE command_id=?`, fixture.runnerUserID, fixture.runnerAPIKeyID, commandID)
		return err
	})
	if _, err := database.Exec(`UPDATE control_outbox SET delivery_state='claimed',claim_sequence=1,
		claim_user_id=?,claim_principal_kind='api_key',claim_api_key_id=?,claim_device_id='runner-rotated',
		claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, fixture.runnerUserID, secondKeyID, commandID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 4, "effect_claimed"); err != nil {
		t.Fatal(err)
	}
	pauseResult := controlDigest("rotated-pause-result")
	if _, err := database.Exec(`UPDATE control_commands SET status='applied',status_revision=3,outcome='applied',
		result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, pauseResult, commandID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_outbox SET delivery_state='acknowledged',result_sequence=1,
		result_digest=?,result_outcome='applied',acknowledged_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, pauseResult, commandID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 5, "command_applied"); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 6, "effect_acknowledged"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_runtime_states SET state='paused',revision=2,last_command_id=?,
		last_result_digest=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE agent_run_id=?`,
		commandID, pauseResult, fixture.runID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 7, "runtime_changed"); err != nil {
		t.Fatal(err)
	}

	resumeID := "13131313-1313-4313-8313-131313131313"
	resumeCanonical := controlDigest("rotated-resume")
	if err := insertRuntimeCommand(fixture, resumeID, "run.resume", resumeCanonical, 1, 2, 2,
		controlTimeOffset(t, database, "+5 minutes")); err != nil {
		t.Fatalf("resume under rotated lease: %v", err)
	}
	if err := insertCommandAudit(t, fixture, resumeID, 1, "command_created"); err != nil {
		t.Fatalf("resume did not observe prior runtime audit: %v", err)
	}
	acceptControlCommand(t, fixture, resumeID)
	if err := insertCommandAudit(t, fixture, resumeID, 2, "command_accepted"); err != nil {
		t.Fatal(err)
	}
	queueControlEffect(t, fixture, resumeID, 2, resumeCanonical)
	if err := insertCommandAudit(t, fixture, resumeID, 3, "effect_queued"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_outbox SET delivery_state='claimed',claim_sequence=1,
		claim_user_id=?,claim_principal_kind='api_key',claim_api_key_id=?,claim_device_id='runner-rotated',
		claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, fixture.runnerUserID, secondKeyID, resumeID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, resumeID, 4, "effect_claimed"); err != nil {
		t.Fatal(err)
	}
	resumeResult := controlDigest("rotated-resume-result")
	if _, err := database.Exec(`UPDATE control_commands SET status='applied',status_revision=3,outcome='applied',
		result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, resumeResult, resumeID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_outbox SET delivery_state='acknowledged',result_sequence=1,
		result_digest=?,result_outcome='applied',acknowledged_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, resumeResult, resumeID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, resumeID, 5, "command_applied"); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, resumeID, 6, "effect_acknowledged"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_runtime_states SET state='running',revision=3,last_command_id=?,
		last_result_digest=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE agent_run_id=?`,
		resumeID, resumeResult, fixture.runID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, resumeID, 7, "runtime_changed"); err != nil {
		t.Fatalf("resume runtime audit: %v", err)
	}
}

func TestM147RuntimeStateSurvivesSameRunAuthorityRotation(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)

	nextDeliveryRevision := fixture.deliveryRevision + 1
	envelope, err := database.Exec(`INSERT INTO delivery_events(
		delivery_id,delivery_revision,idempotency_key,payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,?,?,zeroblob(32),'handoff',?,'2026-08-20T10:02:00Z')`,
		fixture.deliveryID, nextDeliveryRevision, "control-authority-2", fixture.reporterID)
	if err != nil {
		t.Fatal(err)
	}
	envelopeID, _ := envelope.LastInsertId()
	authority, err := database.Exec(`INSERT INTO delivery_stage_events(
		delivery_id,attempt_id,stage_key,execution_number,event_sequence,authority_epoch,delivery_event_id,
		event_type,reporter_id,execution_start_stage_event_id,previous_stage_event_id,handoff_from_reporter_id,
		authority_source_sequence_cutoff,reason_code,server_received_at)
		VALUES(?,?,'specification',1,2,2,?,'handoff',?,?,?,?,0,'authority_rotated','2026-08-20T10:02:00Z')`,
		fixture.deliveryID, fixture.attemptID, envelopeID, fixture.reporterID, fixture.startEventID,
		fixture.startEventID, fixture.reporterID)
	if err != nil {
		t.Fatal(err)
	}
	authorityEventID, _ := authority.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_agent_run_activations(
		delivery_id,attempt_id,stage_key,execution_number,authority_epoch,agent_run_id,reporter_id,
		authority_stage_event_id,telemetry_sequence_cutoff,created_at)
		VALUES(?,?,'specification',1,2,?,?,?,0,'2026-08-20T10:02:00Z')`, fixture.deliveryID,
		fixture.attemptID, fixture.runID, fixture.reporterID, authorityEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE delivery_stage_latest SET authority_epoch=2,
		authority_stage_event_id=?,updated_at='2026-08-20T10:02:00Z'
		WHERE delivery_id=? AND attempt_id=? AND stage_key='specification'`,
		authorityEventID, fixture.deliveryID, fixture.attemptID); err != nil {
		t.Fatal(err)
	}

	if _, err := database.Exec(`UPDATE control_capability_grants
		SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE grant_id=? AND revision=1`, fixture.grantID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_capability_leases
		SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE lease_id=? AND revision=1`, fixture.leaseID); err != nil {
		t.Fatal(err)
	}
	if err := insertGrantAudit(t, fixture, 2, 1, "grant_revoked", "authority_changed"); err != nil {
		t.Fatal(err)
	}
	if err := insertLeaseAudit(t, fixture, 2, 1, "lease_revoked", "authority_changed"); err != nil {
		t.Fatal(err)
	}

	fixture.deliveryRevision = nextDeliveryRevision
	fixture.authorityEpoch = 2
	fixture.authorityEventID = authorityEventID
	grantBinding2, grantActions2 := controlDigest("authority-2-grant-binding"), controlDigest("authority-2-grant-actions")
	if err := insertGrantRevision(fixture, fixture.grantID, 2, controlCredentialA, grantBinding2, grantActions2, 6); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"issue.priority.set", "run.cancel.queued", "run.cancel.running", "input.respond", "run.pause", "run.resume"} {
		if _, err := database.Exec(`INSERT INTO control_capability_grant_actions(grant_id,grant_revision,action) VALUES(?,2,?)`, fixture.grantID, action); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO control_capability_grant_seals(
		grant_id,grant_revision,binding_digest,action_set_digest,action_count) VALUES(?,2,?,?,6)`,
		fixture.grantID, grantBinding2, grantActions2); err != nil {
		t.Fatal(err)
	}
	if err := insertGrantAudit(t, fixture, 3, 2, "grant_renewed", nil); err != nil {
		t.Fatal(err)
	}
	leaseBinding2, leaseActions2 := controlDigest("authority-2-lease-binding"), controlDigest("authority-2-lease-actions")
	if err := insertLeaseRevision(fixture, fixture.leaseID, 2, fixture.runnerAPIKeyID, "runner-01", leaseBinding2, leaseActions2, 4); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"run.cancel.running", "input.respond", "run.pause", "run.resume"} {
		if _, err := database.Exec(`INSERT INTO control_capability_lease_actions(lease_id,lease_revision,action) VALUES(?,2,?)`, fixture.leaseID, action); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO control_capability_lease_seals(
		lease_id,lease_revision,binding_digest,action_set_digest,action_count) VALUES(?,2,?,?,4)`,
		fixture.leaseID, leaseBinding2, leaseActions2); err != nil {
		t.Fatal(err)
	}
	if err := insertLeaseAudit(t, fixture, 3, 2, "lease_renewed", nil); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error {
		return insertRuntimeCommand(fixture, "14141414-1414-4414-8414-141414141414", "run.pause",
			controlDigest("old-authority-pause"), 1, 1, 1, controlTimeOffset(t, database, "+5 minutes"))
	})

	runRuntimeAction := func(commandID, action string, runtimeRevision, nextRevision int, nextState string) {
		t.Helper()
		canonical := controlDigest(commandID + "-canonical")
		if err := insertRuntimeCommand(fixture, commandID, action, canonical, 2, 2, runtimeRevision,
			controlTimeOffset(t, database, "+5 minutes")); err != nil {
			t.Fatal(err)
		}
		if err := insertCommandAudit(t, fixture, commandID, 1, "command_created"); err != nil {
			t.Fatal(err)
		}
		acceptControlCommand(t, fixture, commandID)
		if err := insertCommandAudit(t, fixture, commandID, 2, "command_accepted"); err != nil {
			t.Fatal(err)
		}
		queueControlEffect(t, fixture, commandID, 2, canonical)
		if err := insertCommandAudit(t, fixture, commandID, 3, "effect_queued"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE control_outbox SET delivery_state='claimed',claim_sequence=1,
			claim_user_id=?,claim_principal_kind='api_key',claim_api_key_id=?,claim_device_id='runner-01',
			claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE command_id=?`, fixture.runnerUserID, fixture.runnerAPIKeyID, commandID); err != nil {
			t.Fatal(err)
		}
		if err := insertCommandAudit(t, fixture, commandID, 4, "effect_claimed"); err != nil {
			t.Fatal(err)
		}
		resultDigest := controlDigest(commandID + "-result")
		if _, err := database.Exec(`UPDATE control_commands SET status='applied',status_revision=3,outcome='applied',
			result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE command_id=?`, resultDigest, commandID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE control_outbox SET delivery_state='acknowledged',result_sequence=1,
			result_digest=?,result_outcome='applied',acknowledged_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, resultDigest, commandID); err != nil {
			t.Fatal(err)
		}
		if err := insertCommandAudit(t, fixture, commandID, 5, "command_applied"); err != nil {
			t.Fatal(err)
		}
		if err := insertCommandAudit(t, fixture, commandID, 6, "effect_acknowledged"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE control_runtime_states SET state=?,revision=?,last_command_id=?,
			last_result_digest=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE agent_run_id=?`,
			nextState, nextRevision, commandID, resultDigest, fixture.runID); err != nil {
			t.Fatal(err)
		}
		if err := insertCommandAudit(t, fixture, commandID, 7, "runtime_changed"); err != nil {
			t.Fatal(err)
		}
	}
	runRuntimeAction("15151515-1515-4515-8515-151515151515", "run.pause", 1, 2, "paused")
	runRuntimeAction("16161616-1616-4616-8616-161616161616", "run.resume", 2, 3, "running")
}

func TestM147InputRevisionSealAndAuditOrdering(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	requestID := "34343434-3434-4434-8434-343434343434"
	expires := controlTimeOffset(t, database, "+10 minutes")
	if err := insertInputRevisionOn(database, fixture, requestID, 1, 1,
		"approval", "approval_required", 0, controlDigest("input-rev-1"), expires); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_seals(request_id,request_revision) VALUES(?,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_states(request_id,current_revision,state_revision) VALUES(?,1,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	if err := insertInputAuditOn(t, database, requestID, 1, 1, "input_requested"); err != nil {
		t.Fatalf("input requested audit: %v", err)
	}

	// Neither a forged future clock nor a real pre-expiry transaction may
	// terminalize the provider request as expired.
	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO control_input_resolution_events(
			request_id,request_revision,sequence,event_kind,event_digest,safe_reason,created_at)
			VALUES(?,1,1,'expired',?,'input_expired',?)`, requestID, controlDigest("future-expiry"), expires)
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO control_input_resolution_events(
			request_id,request_revision,sequence,event_kind,event_digest,safe_reason)
			VALUES(?,1,1,'expired',?,'input_expired')`, requestID, controlDigest("early-expiry"))
		return err
	})

	terminalDigest1 := controlDigest("input-superseded-1")
	terminal, err := database.Exec(`INSERT INTO control_input_resolution_events(
		request_id,request_revision,sequence,event_kind,event_digest,safe_reason)
		VALUES(?,1,1,'superseded',?,'input_superseded')`, requestID, terminalDigest1)
	if err != nil {
		t.Fatal(err)
	}
	terminalID1, _ := terminal.LastInsertId()
	if _, err := database.Exec(`UPDATE control_input_request_states SET state_revision=2,terminal_event_id=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE request_id=?`, terminalID1, requestID); err != nil {
		t.Fatal(err)
	}

	// State alone is not an audit predecessor: rev2 requested must follow the
	// immutable rev1 input_superseded fact in the same global request stream.
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := insertInputRevisionOn(tx, fixture, requestID, 2, 1,
		"approval", "approval_required", 0, controlDigest("input-rev-2-probe"), expires); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO control_input_request_seals(request_id,request_revision) VALUES(?,2)`, requestID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE control_input_request_states SET current_revision=2,state_revision=3,terminal_event_id=NULL,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE request_id=?`, requestID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := insertInputAuditOn(t, tx, requestID, 2, 2, "input_requested"); err == nil {
		_ = tx.Rollback()
		t.Fatal("rev2 requested audit preceded rev1 superseded audit")
	}
	_ = tx.Rollback()

	if err := insertInputAuditOn(t, database, requestID, 2, 1, "input_superseded"); err != nil {
		t.Fatalf("superseded audit: %v", err)
	}
	if err := insertInputRevisionOn(database, fixture, requestID, 2, 1,
		"approval", "approval_required", 0, controlDigest("input-rev-2"), expires); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_seals(request_id,request_revision) VALUES(?,2)`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_input_request_states SET current_revision=2,state_revision=3,terminal_event_id=NULL,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE request_id=?`, requestID); err != nil {
		t.Fatal(err)
	}

	// A terminal rev2 audit cannot skip that revision's requested fact.
	tx, err = database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	terminal2, err := tx.Exec(`INSERT INTO control_input_resolution_events(
		request_id,request_revision,sequence,event_kind,event_digest,safe_reason)
		VALUES(?,2,2,'superseded',?,'input_superseded')`, requestID, controlDigest("input-superseded-2-probe"))
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	terminalID2, _ := terminal2.LastInsertId()
	if _, err := tx.Exec(`UPDATE control_input_request_states SET state_revision=4,terminal_event_id=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE request_id=?`, terminalID2, requestID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := insertInputAuditOn(t, tx, requestID, 3, 2, "input_superseded"); err == nil {
		_ = tx.Rollback()
		t.Fatal("rev2 terminal audit skipped rev2 input_requested")
	}
	_ = tx.Rollback()

	if err := insertInputAuditOn(t, database, requestID, 3, 2, "input_requested"); err != nil {
		t.Fatal(err)
	}
	terminal2, err = database.Exec(`INSERT INTO control_input_resolution_events(
		request_id,request_revision,sequence,event_kind,event_digest,safe_reason)
		VALUES(?,2,2,'superseded',?,'input_superseded')`, requestID, controlDigest("input-superseded-2"))
	if err != nil {
		t.Fatal(err)
	}
	terminalID2, _ = terminal2.LastInsertId()
	if _, err := database.Exec(`UPDATE control_input_request_states SET state_revision=4,terminal_event_id=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE request_id=?`, terminalID2, requestID); err != nil {
		t.Fatal(err)
	}
	if err := insertInputAuditOn(t, database, requestID, 4, 2, "input_superseded"); err != nil {
		t.Fatalf("ordered rev2 terminal audit: %v", err)
	}

	// A semantic change is a new provider request ID, never revision churn.
	requireConstraint(t, func() error {
		return insertInputRevisionOn(database, fixture, requestID, 3, 1,
			"choice", "choice_required", 1, controlDigest("wrong-kind-rev-3"), expires)
	})
}

func TestM147InputChoiceOptionsSealExactly(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	requestID := "56565656-5656-4656-8656-565656565656"
	if err := insertInputRevisionOn(database, fixture, requestID, 1, 1,
		"choice", "choice_required", 2, controlDigest("choice-request"), controlTimeOffset(t, database, "+10 minutes")); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO control_input_request_seals(request_id,request_revision) VALUES(?,1)`, requestID)
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO control_input_request_options(request_id,request_revision,ordinal,option_code)
			VALUES(?,1,3,'choice_3')`, requestID)
		return err
	})
	for ordinal := 1; ordinal <= 2; ordinal++ {
		if _, err := database.Exec(`INSERT INTO control_input_request_options(request_id,request_revision,ordinal,option_code)
			VALUES(?,1,?,'choice_'||?)`, requestID, ordinal, ordinal); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_seals(request_id,request_revision) VALUES(?,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO control_input_request_options(request_id,request_revision,ordinal,option_code)
			VALUES(?,1,2,'choice_2')`, requestID)
		return err
	})
}

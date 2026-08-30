// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package db

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func insertGrantAuditWithID(fixture controlGraphFixture, id, sequence, revision int, kind string, reason any) error {
	_, err := fixture.database.Exec(`INSERT INTO control_events(
		id,sequence,event_kind,grant_id,grant_revision,actor_user_id,user_id,principal_kind,
		actor_session_credential_id,actor_api_key_id,delivery_id,root_issue_id,issue_revision,
		binding_digest,action_set_digest,subject_expires_at,subject_updated_at,safe_reason)
		SELECT ?,?,?,grant_id,revision,actor_user_id,user_id,principal_kind,
		 actor_session_credential_id,actor_api_key_id,delivery_id,root_issue_id,issue_revision,
		 binding_digest,action_set_digest,expires_at,updated_at,?
		FROM control_capability_grants WHERE grant_id=? AND revision=?`,
		id, sequence, kind, reason, fixture.grantID, revision)
	return err
}

func acceptControlCommand(t *testing.T, fixture controlGraphFixture, commandID string) {
	t.Helper()
	if err := acceptControlCommandErr(fixture, commandID); err != nil {
		t.Fatalf("accept command: %v", err)
	}
}

func acceptControlCommandErr(fixture controlGraphFixture, commandID string) error {
	_, err := fixture.database.Exec(`UPDATE control_commands SET status='accepted',status_revision=2,
		accepted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, commandID)
	return err
}

func queueControlEffect(t *testing.T, fixture controlGraphFixture, commandID string, leaseRevision int, digest []byte) {
	t.Helper()
	if _, err := fixture.database.Exec(`INSERT INTO control_outbox(
		command_id,lease_id,lease_revision,delivery_state,effect_digest)
		VALUES(?,?,?,'queued',?)`, commandID, fixture.leaseID, leaseRevision, digest); err != nil {
		t.Fatalf("queue effect: %v", err)
	}
}

func claimControlEffect(fixture controlGraphFixture, commandID string) error {
	_, err := fixture.database.Exec(`UPDATE control_outbox SET delivery_state='claimed',claim_sequence=1,
		claim_user_id=?,claim_principal_kind='api_key',claim_api_key_id=?,claim_device_id='runner-01',
		claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, fixture.runnerUserID, fixture.runnerAPIKeyID, commandID)
	return err
}

func directControlResult(fixture controlGraphFixture, commandID, status, outcome string, reason any, digest []byte) error {
	tx, err := fixture.database.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE control_commands SET status=?,status_revision=3,outcome=?,safe_reason=?,
		result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`,
		status, outcome, reason, digest, commandID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE control_outbox SET delivery_state='acknowledged',result_sequence=1,
		result_digest=?,result_outcome=?,safe_reason=?,
		acknowledged_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, digest, outcome, reason, commandID); err != nil {
		return err
	}
	return tx.Commit()
}

func rotateControlAuthority(t *testing.T, fixture controlGraphFixture, idempotency string) {
	t.Helper()
	envelope, err := fixture.database.Exec(`INSERT INTO delivery_events(
		delivery_id,delivery_revision,idempotency_key,payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,?,?,zeroblob(32),'handoff',?,'2026-08-20T10:03:00Z')`,
		fixture.deliveryID, fixture.deliveryRevision+1, idempotency, fixture.reporterID)
	if err != nil {
		t.Fatal(err)
	}
	envelopeID, _ := envelope.LastInsertId()
	authority, err := fixture.database.Exec(`INSERT INTO delivery_stage_events(
		delivery_id,attempt_id,stage_key,execution_number,event_sequence,authority_epoch,delivery_event_id,
		event_type,reporter_id,execution_start_stage_event_id,previous_stage_event_id,handoff_from_reporter_id,
		authority_source_sequence_cutoff,reason_code,server_received_at)
		VALUES(?,?,'specification',1,2,2,?,'handoff',?,?,?,?,0,'authority_rotated','2026-08-20T10:03:00Z')`,
		fixture.deliveryID, fixture.attemptID, envelopeID, fixture.reporterID, fixture.startEventID,
		fixture.startEventID, fixture.reporterID)
	if err != nil {
		t.Fatal(err)
	}
	authorityEventID, _ := authority.LastInsertId()
	if _, err := fixture.database.Exec(`INSERT INTO delivery_agent_run_activations(
		delivery_id,attempt_id,stage_key,execution_number,authority_epoch,agent_run_id,reporter_id,
		authority_stage_event_id,telemetry_sequence_cutoff,created_at)
		VALUES(?,?,'specification',1,2,?,?,?,0,'2026-08-20T10:03:00Z')`, fixture.deliveryID,
		fixture.attemptID, fixture.runID, fixture.reporterID, authorityEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`UPDATE delivery_stage_latest SET authority_epoch=2,
		authority_stage_event_id=?,updated_at='2026-08-20T10:03:00Z'
		WHERE delivery_id=? AND attempt_id=? AND stage_key='specification'`,
		authorityEventID, fixture.deliveryID, fixture.attemptID); err != nil {
		t.Fatal(err)
	}
}

func seedApprovalInputRequest(t *testing.T, fixture controlGraphFixture, requestID, expires string) {
	t.Helper()
	if err := insertInputRevisionOn(fixture.database, fixture, requestID, 1, 1,
		"approval", "approval_required", 0, controlDigest("approval-request-"+requestID), expires); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`INSERT INTO control_input_request_seals(request_id,request_revision)
		VALUES(?,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`INSERT INTO control_input_request_states(request_id,current_revision,state_revision)
		VALUES(?,1,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	if err := insertInputAuditOn(t, fixture.database, requestID, 1, 1, "input_requested"); err != nil {
		t.Fatal(err)
	}
}

func insertInputResponseCommand(fixture controlGraphFixture, requestID, commandID string, canonical []byte, expiresAt string) error {
	_, err := fixture.database.Exec(`INSERT INTO control_commands(
		command_id,actor_user_id,user_id,principal_kind,actor_session_credential_id,canonical_digest,
		grant_id,grant_revision,grant_expires_at,grant_binding_digest,grant_action_digest,action,status,
		challenge_template,delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		issue_etag_digest,target_snapshot_digest,attempt_id,attempt_number,plan_revision,stage_key,
		execution_number,execution_start_stage_event_id,authority_epoch,authority_stage_event_id,reporter_id,
		agent_run_id,lease_id,lease_revision,lease_expires_at,lease_binding_digest,lease_action_digest,
		input_request_id,input_request_revision,input_request_expires_at,input_response_kind,
		parameter_digest,expires_at)
		SELECT ?,grant_row.actor_user_id,grant_row.user_id,grant_row.principal_kind,
		 grant_row.actor_session_credential_id,?,grant_row.grant_id,grant_row.revision,grant_row.expires_at,
		 grant_row.binding_digest,grant_row.action_set_digest,'input.respond','pending_confirmation','input_approve',
		 request.delivery_id,request.delivery_key,request.delivery_revision,request.project_id,request.root_issue_id,
		 request.issue_revision,grant_row.issue_etag_digest,?,request.attempt_id,request.attempt_number,
		 request.plan_revision,request.stage_key,request.execution_number,request.execution_start_stage_event_id,
		 request.authority_epoch,request.authority_stage_event_id,request.reporter_id,request.agent_run_id,
		 request.lease_id,request.lease_revision,lease.expires_at,lease.binding_digest,lease.action_set_digest,
		 request.request_id,request.revision,request.expires_at,'approve',?,?
		FROM control_capability_grants grant_row
		JOIN control_input_requests request ON request.request_id=? AND request.revision=1
		JOIN control_capability_leases lease ON lease.lease_id=request.lease_id AND lease.revision=request.lease_revision
		WHERE grant_row.grant_id=? AND grant_row.revision=1`,
		commandID, canonical, controlDigest("input-response-target"), controlDigest("input-response-parameter"), expiresAt,
		requestID, fixture.grantID)
	return err
}

func insertRuntimeCommand(fixture controlGraphFixture, commandID, action string, canonical []byte, grantRevision, leaseRevision, runtimeRevision int, expiresAt string) error {
	template := "run_pause"
	if action == "run.resume" {
		template = "run_resume"
	}
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
		 grant_row.binding_digest,grant_row.action_set_digest,?,'pending_confirmation',?,
		 grant_row.delivery_id,grant_row.delivery_key,grant_row.delivery_revision,grant_row.project_id,
		 grant_row.root_issue_id,grant_row.issue_revision,grant_row.issue_etag_digest,?,
		 lease.attempt_id,lease.attempt_number,lease.plan_revision,lease.stage_key,lease.execution_number,
		 lease.execution_start_stage_event_id,lease.authority_epoch,lease.authority_stage_event_id,
		 lease.reporter_id,lease.agent_run_id,lease.lease_id,lease.revision,lease.expires_at,
		 lease.binding_digest,lease.action_set_digest,?,?,?
		FROM control_capability_grants grant_row JOIN control_capability_leases lease
		 ON lease.delivery_id=grant_row.delivery_id
		WHERE grant_row.grant_id=? AND grant_row.revision=? AND lease.lease_id=? AND lease.revision=?`,
		commandID, canonical, action, template, controlDigest("runtime-target"), runtimeRevision,
		controlDigest("runtime-parameter"), expiresAt, fixture.grantID, grantRevision, fixture.leaseID, leaseRevision)
	return err
}

func insertResolvedInputAudit(t *testing.T, fixture controlGraphFixture, requestID string, sequence int) error {
	t.Helper()
	_, err := fixture.database.Exec(`INSERT INTO control_events(
		sequence,event_kind,input_request_id,input_request_revision,actor_user_id,user_id,principal_kind,
		actor_session_credential_id,actor_api_key_id,executor_user_id,executor_principal_kind,
		executor_api_key_id,device_id,delivery_id,root_issue_id,issue_revision,attempt_id,stage_key,
		execution_number,authority_epoch,reporter_id,agent_run_id,action,command_status,outcome,
		parameter_digest,binding_digest,result_digest)
		SELECT ?,'input_resolved',request.request_id,request.revision,command.actor_user_id,command.user_id,
		 command.principal_kind,command.actor_session_credential_id,command.actor_api_key_id,
		 outbox.claim_user_id,'api_key',outbox.claim_api_key_id,outbox.claim_device_id,
		 request.delivery_id,request.root_issue_id,request.issue_revision,request.attempt_id,request.stage_key,
		 request.execution_number,request.authority_epoch,request.reporter_id,request.agent_run_id,
		 'input.respond','applied','applied',command.parameter_digest,lease.binding_digest,terminal.event_digest
		FROM control_input_requests request
		JOIN control_input_resolution_events terminal ON terminal.request_id=request.request_id
		 AND terminal.request_revision=request.revision
		JOIN control_commands command ON command.command_id=terminal.command_id
		JOIN control_outbox outbox ON outbox.command_id=command.command_id
		JOIN control_capability_leases lease ON lease.lease_id=request.lease_id AND lease.revision=request.lease_revision
		WHERE request.request_id=? AND request.revision=1`, sequence, requestID)
	return err
}

func insertQueuedCancelCommand(fixture controlGraphFixture, commandID string, canonical []byte, expiresAt string) error {
	_, err := fixture.database.Exec(`INSERT INTO control_commands(
		command_id,actor_user_id,user_id,principal_kind,actor_session_credential_id,canonical_digest,
		grant_id,grant_revision,grant_expires_at,grant_binding_digest,grant_action_digest,action,status,
		challenge_template,delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		issue_etag_digest,target_snapshot_digest,attempt_id,attempt_number,plan_revision,stage_key,
		execution_number,execution_start_stage_event_id,authority_epoch,authority_stage_event_id,reporter_id,
		agent_run_id,parameter_digest,expires_at)
		SELECT ?,grant_row.actor_user_id,grant_row.user_id,grant_row.principal_kind,
		 grant_row.actor_session_credential_id,?,grant_row.grant_id,grant_row.revision,grant_row.expires_at,
		 grant_row.binding_digest,grant_row.action_set_digest,'run.cancel.queued','pending_confirmation',
		 'run_cancel_queued',grant_row.delivery_id,grant_row.delivery_key,grant_row.delivery_revision,
		 grant_row.project_id,grant_row.root_issue_id,grant_row.issue_revision,grant_row.issue_etag_digest,?,
		 lease.attempt_id,lease.attempt_number,lease.plan_revision,lease.stage_key,lease.execution_number,
		 lease.execution_start_stage_event_id,lease.authority_epoch,lease.authority_stage_event_id,
		 lease.reporter_id,lease.agent_run_id,?,?
		FROM control_capability_grants grant_row JOIN control_capability_leases lease
		 ON lease.delivery_id=grant_row.delivery_id
		WHERE grant_row.grant_id=? AND grant_row.revision=1 AND lease.lease_id=? AND lease.revision=1`,
		commandID, canonical, controlDigest("queued-cancel-target"), controlDigest("queued-cancel-parameter"),
		expiresAt, fixture.grantID, fixture.leaseID)
	return err
}

func insertRunningCancelCommand(fixture controlGraphFixture, commandID string, canonical []byte, expiresAt string) error {
	return insertRunningCancelCommandRevision(fixture, commandID, canonical, 1, 1, expiresAt)
}

func insertRunningCancelCommandRevision(fixture controlGraphFixture, commandID string, canonical []byte, grantRevision, leaseRevision int, expiresAt string) error {
	_, err := fixture.database.Exec(`INSERT INTO control_commands(
		command_id,actor_user_id,user_id,principal_kind,actor_session_credential_id,canonical_digest,
		grant_id,grant_revision,grant_expires_at,grant_binding_digest,grant_action_digest,action,status,
		challenge_template,delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		issue_etag_digest,target_snapshot_digest,attempt_id,attempt_number,plan_revision,stage_key,
		execution_number,execution_start_stage_event_id,authority_epoch,authority_stage_event_id,reporter_id,
		agent_run_id,lease_id,lease_revision,lease_expires_at,lease_binding_digest,lease_action_digest,
		parameter_digest,expires_at)
		SELECT ?,grant_row.actor_user_id,grant_row.user_id,grant_row.principal_kind,
		 grant_row.actor_session_credential_id,?,grant_row.grant_id,grant_row.revision,grant_row.expires_at,
		 grant_row.binding_digest,grant_row.action_set_digest,'run.cancel.running','pending_confirmation',
		 'run_cancel_running',grant_row.delivery_id,grant_row.delivery_key,grant_row.delivery_revision,
		 grant_row.project_id,grant_row.root_issue_id,grant_row.issue_revision,grant_row.issue_etag_digest,?,
		 lease.attempt_id,lease.attempt_number,lease.plan_revision,lease.stage_key,lease.execution_number,
		 lease.execution_start_stage_event_id,lease.authority_epoch,lease.authority_stage_event_id,
		 lease.reporter_id,lease.agent_run_id,lease.lease_id,lease.revision,lease.expires_at,
		 lease.binding_digest,lease.action_set_digest,?,?
		FROM control_capability_grants grant_row JOIN control_capability_leases lease
		 ON lease.delivery_id=grant_row.delivery_id
		WHERE grant_row.grant_id=? AND grant_row.revision=? AND lease.lease_id=? AND lease.revision=?`,
		commandID, canonical, controlDigest("running-cancel-target"), controlDigest("running-cancel-parameter"),
		expiresAt, fixture.grantID, grantRevision, fixture.leaseID, leaseRevision)
	return err
}

func insertOperatorCancellationAudit(t *testing.T, fixture controlGraphFixture, commandID string, sequence int) error {
	t.Helper()
	_, err := fixture.database.Exec(`INSERT INTO control_events(
		sequence,event_kind,cancellation_run_id,cancellation_command_id,cancellation_cause,
		actor_user_id,user_id,principal_kind,actor_session_credential_id,actor_api_key_id,
		delivery_id,root_issue_id,issue_revision,attempt_id,stage_key,execution_number,authority_epoch,
		reporter_id,agent_run_id,action,command_status,outcome,parameter_digest,binding_digest,
		action_set_digest,result_digest)
		SELECT ?,'cancellation_recorded',agent_run_id,command_id,'operator_command',actor_user_id,user_id,
		 principal_kind,actor_session_credential_id,actor_api_key_id,delivery_id,root_issue_id,issue_revision,
		 attempt_id,stage_key,execution_number,authority_epoch,reporter_id,agent_run_id,action,status,outcome,
		 parameter_digest,target_snapshot_digest,grant_action_digest,result_digest
		FROM control_commands WHERE command_id=?`, sequence, commandID)
	return err
}

func TestM147UUIDChecksRejectExtraHyphenAndNULTail(t *testing.T) {
	database := openTestDB(t)
	user, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('uuid-guard','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	invalid := []string{
		"12345678-1234-4234-9234-123456789ab-",
		controlCredentialA + "\x00secret",
	}
	for i, credential := range invalid {
		requireConstraint(t, func() error {
			_, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
				VALUES(?,?, '2026-12-01 00:00:00','2026-08-01 00:00:00',?)`,
				fmt.Sprintf("uuid-bearer-%d", i), userID, credential)
			return err
		})
	}

	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	expires := controlTimeOffset(t, database, "+5 minutes")
	for i, commandID := range invalid {
		requireConstraint(t, func() error {
			return insertPriorityCommand(fixture, commandID, controlDigest(fmt.Sprintf("invalid-uuid-command-%d", i)), expires)
		})
	}
}

func TestM147ConcurrentCanonicalCommandsConverge(t *testing.T) {
	testM147ConcurrentCanonicalCommandsConverge(t, 32)
}

func TestM147ConcurrentCanonicalCommandsConvergeProductionPool(t *testing.T) {
	testM147ConcurrentCanonicalCommandsConverge(t, DefaultMaxOpenConnections)
}

func testM147ConcurrentCanonicalCommandsConverge(t *testing.T, maxOpenConnections int) {
	t.Helper()
	database := openTestDB(t)
	database.SetMaxOpenConns(maxOpenConnections)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	if got := database.Stats().MaxOpenConnections; got != maxOpenConnections {
		t.Fatalf("max open connections=%d, want %d", got, maxOpenConnections)
	}
	expires := controlTimeOffset(t, database, "+5 minutes")
	canonical := controlDigest("concurrent-canonical-command")
	start := make(chan struct{})
	results := make(chan error, 32)
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func(ordinal int) {
			defer workers.Done()
			<-start
			commandID := fmt.Sprintf("%08x-1111-4111-8111-%012x", ordinal+1, ordinal+1)
			results <- insertPriorityCommand(fixture, commandID, canonical, expires)
		}(i)
	}
	close(start)
	workers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !strings.Contains(err.Error(), "UNIQUE constraint failed: control_commands.canonical_digest") {
			t.Fatalf("concurrent canonical command returned non-arbitration error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent canonical command successes=%d, want 1", successes)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_commands WHERE canonical_digest=?`, canonical).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("canonical command rows=%d, want 1", count)
	}
}

func TestM147ConcurrentRuntimeAcceptanceHasOneEffectOwner(t *testing.T) {
	testM147ConcurrentRuntimeAcceptanceHasOneEffectOwner(t, 32)
}

func TestM147ConcurrentRuntimeAcceptanceHasOneEffectOwnerProductionPool(t *testing.T) {
	testM147ConcurrentRuntimeAcceptanceHasOneEffectOwner(t, DefaultMaxOpenConnections)
}

func testM147ConcurrentRuntimeAcceptanceHasOneEffectOwner(t *testing.T, maxOpenConnections int) {
	t.Helper()
	database := openTestDB(t)
	database.SetMaxOpenConns(maxOpenConnections)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	if got := database.Stats().MaxOpenConnections; got != maxOpenConnections {
		t.Fatalf("max open connections=%d, want %d", got, maxOpenConnections)
	}
	expires := controlTimeOffset(t, database, "+5 minutes")
	commandIDs := make([]string, 32)
	for i := range commandIDs {
		commandIDs[i] = fmt.Sprintf("%08x-1212-4212-8212-%012x", i+1, i+1)
		if err := insertPauseCommand(fixture, commandIDs[i], controlDigest(fmt.Sprintf("runtime-owner-%d", i)), 1, 1, expires); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		commandID string
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, len(commandIDs))
	var workers sync.WaitGroup
	for _, commandID := range commandIDs {
		workers.Add(1)
		go func(commandID string) {
			defer workers.Done()
			<-start
			results <- result{commandID: commandID, err: acceptControlCommandErr(fixture, commandID)}
		}(commandID)
	}
	close(start)
	workers.Wait()
	close(results)
	winner := ""
	for result := range results {
		if result.err == nil {
			if winner != "" {
				t.Fatalf("multiple runtime owners accepted: %s and %s", winner, result.commandID)
			}
			winner = result.commandID
			continue
		}
		if !strings.Contains(result.err.Error(), "UNIQUE constraint failed: control_commands.agent_run_id, control_commands.runtime_revision") {
			t.Fatalf("runtime acceptance returned non-arbitration error: %v", result.err)
		}
	}
	if winner == "" {
		t.Fatal("runtime acceptance had no winner")
	}
	var digest []byte
	if err := database.QueryRow(`SELECT canonical_digest FROM control_commands WHERE command_id=?`, winner).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	queueControlEffect(t, fixture, winner, 1, digest)
	claimStart := make(chan struct{})
	claimResults := make(chan error, 32)
	workers = sync.WaitGroup{}
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-claimStart
			claimResults <- claimControlEffect(fixture, winner)
		}()
	}
	close(claimStart)
	workers.Wait()
	close(claimResults)
	claimSuccesses := 0
	for err := range claimResults {
		if err == nil {
			claimSuccesses++
			continue
		}
		if !strings.Contains(err.Error(), "invalid control outbox transition") &&
			!strings.Contains(err.Error(), "control outbox unknown outcome lacks command proof") {
			t.Fatalf("concurrent claim returned non-CAS error: %v", err)
		}
	}
	if claimSuccesses != 1 {
		t.Fatalf("concurrent claim successes=%d, want 1", claimSuccesses)
	}
	var accepted, effects int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_commands WHERE status='accepted'`).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_outbox`).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if accepted != 1 || effects != 1 {
		t.Fatalf("accepted=%d effects=%d, want exactly one of each", accepted, effects)
	}
}

func TestM147OutcomeUnknownRequiresClaimedAsyncEffect(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	expires := controlTimeOffset(t, database, "+5 minutes")

	priorityID := "67676767-6767-4767-8767-676767676767"
	if err := insertPriorityCommand(fixture, priorityID, controlDigest("unknown-sync"), expires); err != nil {
		t.Fatal(err)
	}
	acceptControlCommand(t, fixture, priorityID)
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_commands SET status_revision=3,outcome='outcome_unknown',
			safe_reason='runner_lost',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, priorityID)
		return err
	})

	pauseID := "68686868-6868-4868-8868-686868686868"
	canonical := controlDigest("unknown-async")
	if err := insertPauseCommand(fixture, pauseID, canonical, 1, 1, expires); err != nil {
		t.Fatal(err)
	}
	acceptControlCommand(t, fixture, pauseID)
	queueControlEffect(t, fixture, pauseID, 1, canonical)
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_commands SET status_revision=3,outcome='outcome_unknown',
			safe_reason='runner_lost',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, pauseID)
		return err
	})

	if _, err := database.Exec(`UPDATE control_outbox SET delivery_state='claimed',claim_sequence=1,
		claim_user_id=?,claim_principal_kind='api_key',claim_api_key_id=?,claim_device_id='runner-01',
		claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, fixture.runnerUserID, fixture.runnerAPIKeyID, pauseID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_commands SET status_revision=3,outcome='outcome_unknown',
		safe_reason='runner_lost',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, pauseID); err != nil {
		t.Fatalf("claimed async effect could not become outcome_unknown: %v", err)
	}
}

func TestM147OutboxStateShapeIsNullTotal(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	commandID := "69696969-6969-4969-8969-696969696969"
	canonical := controlDigest("outbox-null-total")
	if err := insertPauseCommand(fixture, commandID, canonical, 1, 1,
		controlTimeOffset(t, database, "+5 minutes")); err != nil {
		t.Fatal(err)
	}
	acceptControlCommand(t, fixture, commandID)
	queueControlEffect(t, fixture, commandID, 1, canonical)

	// A first claim has no loss proof yet. runner_lost is reserved for the
	// later claimed->claimed outcome-unknown transition.
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_outbox SET delivery_state='claimed',claim_sequence=1,
			claim_user_id=?,claim_principal_kind='api_key',claim_api_key_id=?,claim_device_id='runner-01',
			claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),safe_reason='runner_lost',
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`,
			fixture.runnerUserID, fixture.runnerAPIKeyID, commandID)
		return err
	})

	// Isolate the table's durable row-shape contract from the lifecycle
	// triggers. Every nullable field that is required by a claimed or
	// acknowledged state must make the whole CHECK false, never NULL.
	for _, trigger := range []string{
		"trg_control_outbox_transition_guard",
		"trg_control_outbox_claim_binding_guard",
		"trg_control_outbox_result_binding_guard",
	} {
		if _, err := database.Exec(`DROP TRIGGER ` + trigger); err != nil {
			t.Fatal(err)
		}
	}
	var rowTime string
	if err := database.QueryRow(`SELECT created_at FROM control_outbox WHERE command_id=?`, commandID).Scan(&rowTime); err != nil {
		t.Fatal(err)
	}
	type claimShape struct {
		name      string
		sequence  any
		userID    any
		kind      any
		apiKeyID  any
		deviceID  any
		claimedAt any
	}
	validClaim := claimShape{
		sequence:  int64(1),
		userID:    fixture.runnerUserID,
		kind:      "api_key",
		apiKeyID:  fixture.runnerAPIKeyID,
		deviceID:  "runner-01",
		claimedAt: rowTime,
	}
	claimCases := []claimShape{
		{name: "claim sequence", userID: validClaim.userID, kind: validClaim.kind, apiKeyID: validClaim.apiKeyID, deviceID: validClaim.deviceID, claimedAt: validClaim.claimedAt},
		{name: "claim user", sequence: validClaim.sequence, kind: validClaim.kind, apiKeyID: validClaim.apiKeyID, deviceID: validClaim.deviceID, claimedAt: validClaim.claimedAt},
		{name: "claim principal kind", sequence: validClaim.sequence, userID: validClaim.userID, apiKeyID: validClaim.apiKeyID, deviceID: validClaim.deviceID, claimedAt: validClaim.claimedAt},
		{name: "claim API key", sequence: validClaim.sequence, userID: validClaim.userID, kind: validClaim.kind, deviceID: validClaim.deviceID, claimedAt: validClaim.claimedAt},
		{name: "claim device", sequence: validClaim.sequence, userID: validClaim.userID, kind: validClaim.kind, apiKeyID: validClaim.apiKeyID, claimedAt: validClaim.claimedAt},
		{name: "claimed at", sequence: validClaim.sequence, userID: validClaim.userID, kind: validClaim.kind, apiKeyID: validClaim.apiKeyID, deviceID: validClaim.deviceID},
	}
	updateClaim := func(shape claimShape) error {
		_, err := database.Exec(`UPDATE control_outbox SET delivery_state='claimed',claim_sequence=?,
			claim_user_id=?,claim_principal_kind=?,claim_session_credential_id=NULL,claim_api_key_id=?,
			claim_device_id=?,claimed_at=?,result_sequence=NULL,result_digest=NULL,result_outcome=NULL,
			safe_reason=NULL,acknowledged_at=NULL,updated_at=? WHERE command_id=?`,
			shape.sequence, shape.userID, shape.kind, shape.apiKeyID, shape.deviceID, shape.claimedAt,
			rowTime, commandID)
		return err
	}
	for _, test := range claimCases {
		t.Run("claimed without "+test.name, func(t *testing.T) {
			requireConstraint(t, func() error { return updateClaim(test) })
		})
	}
	if err := updateClaim(validClaim); err != nil {
		t.Fatalf("valid claimed shape: %v", err)
	}

	type acknowledgedShape struct {
		name           string
		claimSequence  any
		claimUserID    any
		claimKind      any
		claimAPIKeyID  any
		claimDeviceID  any
		claimedAt      any
		resultSequence any
		resultDigest   any
		resultOutcome  any
		safeReason     any
		acknowledgedAt any
	}
	resultDigest := controlDigest("outbox-null-total-result")
	validAcknowledged := acknowledgedShape{
		claimSequence:  int64(1),
		claimUserID:    fixture.runnerUserID,
		claimKind:      "api_key",
		claimAPIKeyID:  fixture.runnerAPIKeyID,
		claimDeviceID:  "runner-01",
		claimedAt:      rowTime,
		resultSequence: int64(1),
		resultDigest:   resultDigest,
		resultOutcome:  "applied",
		acknowledgedAt: rowTime,
	}
	acknowledgedCases := []acknowledgedShape{
		{name: "claim sequence", claimUserID: validAcknowledged.claimUserID, claimKind: validAcknowledged.claimKind, claimAPIKeyID: validAcknowledged.claimAPIKeyID, claimDeviceID: validAcknowledged.claimDeviceID, claimedAt: validAcknowledged.claimedAt, resultSequence: validAcknowledged.resultSequence, resultDigest: resultDigest, resultOutcome: "applied", acknowledgedAt: rowTime},
		{name: "claim user", claimSequence: 1, claimKind: validAcknowledged.claimKind, claimAPIKeyID: validAcknowledged.claimAPIKeyID, claimDeviceID: validAcknowledged.claimDeviceID, claimedAt: validAcknowledged.claimedAt, resultSequence: validAcknowledged.resultSequence, resultDigest: resultDigest, resultOutcome: "applied", acknowledgedAt: rowTime},
		{name: "claim principal kind", claimSequence: 1, claimUserID: validAcknowledged.claimUserID, claimAPIKeyID: validAcknowledged.claimAPIKeyID, claimDeviceID: validAcknowledged.claimDeviceID, claimedAt: validAcknowledged.claimedAt, resultSequence: validAcknowledged.resultSequence, resultDigest: resultDigest, resultOutcome: "applied", acknowledgedAt: rowTime},
		{name: "claim API key", claimSequence: 1, claimUserID: validAcknowledged.claimUserID, claimKind: validAcknowledged.claimKind, claimDeviceID: validAcknowledged.claimDeviceID, claimedAt: validAcknowledged.claimedAt, resultSequence: validAcknowledged.resultSequence, resultDigest: resultDigest, resultOutcome: "applied", acknowledgedAt: rowTime},
		{name: "claim device", claimSequence: 1, claimUserID: validAcknowledged.claimUserID, claimKind: validAcknowledged.claimKind, claimAPIKeyID: validAcknowledged.claimAPIKeyID, claimedAt: validAcknowledged.claimedAt, resultSequence: validAcknowledged.resultSequence, resultDigest: resultDigest, resultOutcome: "applied", acknowledgedAt: rowTime},
		{name: "claimed at", claimSequence: 1, claimUserID: validAcknowledged.claimUserID, claimKind: validAcknowledged.claimKind, claimAPIKeyID: validAcknowledged.claimAPIKeyID, claimDeviceID: validAcknowledged.claimDeviceID, resultSequence: validAcknowledged.resultSequence, resultDigest: resultDigest, resultOutcome: "applied", acknowledgedAt: rowTime},
		{name: "result sequence", claimSequence: 1, claimUserID: validAcknowledged.claimUserID, claimKind: validAcknowledged.claimKind, claimAPIKeyID: validAcknowledged.claimAPIKeyID, claimDeviceID: validAcknowledged.claimDeviceID, claimedAt: rowTime, resultDigest: resultDigest, resultOutcome: "applied", acknowledgedAt: rowTime},
		{name: "result digest", claimSequence: 1, claimUserID: validAcknowledged.claimUserID, claimKind: validAcknowledged.claimKind, claimAPIKeyID: validAcknowledged.claimAPIKeyID, claimDeviceID: validAcknowledged.claimDeviceID, claimedAt: rowTime, resultSequence: 1, resultOutcome: "applied", acknowledgedAt: rowTime},
		{name: "result outcome", claimSequence: 1, claimUserID: validAcknowledged.claimUserID, claimKind: validAcknowledged.claimKind, claimAPIKeyID: validAcknowledged.claimAPIKeyID, claimDeviceID: validAcknowledged.claimDeviceID, claimedAt: rowTime, resultSequence: 1, resultDigest: resultDigest, acknowledgedAt: rowTime},
		{name: "acknowledged at", claimSequence: 1, claimUserID: validAcknowledged.claimUserID, claimKind: validAcknowledged.claimKind, claimAPIKeyID: validAcknowledged.claimAPIKeyID, claimDeviceID: validAcknowledged.claimDeviceID, claimedAt: rowTime, resultSequence: 1, resultDigest: resultDigest, resultOutcome: "applied"},
		{name: "rejected safe reason", claimSequence: 1, claimUserID: validAcknowledged.claimUserID, claimKind: validAcknowledged.claimKind, claimAPIKeyID: validAcknowledged.claimAPIKeyID, claimDeviceID: validAcknowledged.claimDeviceID, claimedAt: rowTime, resultSequence: 1, resultDigest: resultDigest, resultOutcome: "rejected", acknowledgedAt: rowTime},
	}
	for _, test := range acknowledgedCases {
		t.Run("acknowledged without "+test.name, func(t *testing.T) {
			requireConstraint(t, func() error {
				_, err := database.Exec(`UPDATE control_outbox SET delivery_state='acknowledged',
					claim_sequence=?,claim_user_id=?,claim_principal_kind=?,claim_session_credential_id=NULL,
					claim_api_key_id=?,claim_device_id=?,claimed_at=?,result_sequence=?,result_digest=?,
					result_outcome=?,safe_reason=?,acknowledged_at=?,updated_at=? WHERE command_id=?`,
					test.claimSequence, test.claimUserID, test.claimKind, test.claimAPIKeyID,
					test.claimDeviceID, test.claimedAt, test.resultSequence, test.resultDigest,
					test.resultOutcome, test.safeReason, test.acknowledgedAt, rowTime, commandID)
				return err
			})
		})
	}
}

func TestM147ReconciledTerminalAuditRequiresPriorUnknownFact(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	commandID := "84848484-8484-4484-8484-848484848484"
	canonical := controlDigest("reconcile-without-unknown")
	if err := insertPauseCommand(fixture, commandID, canonical, 1, 1,
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
	queueControlEffect(t, fixture, commandID, 1, canonical)
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
	if _, err := database.Exec(`UPDATE control_commands SET status_revision=3,outcome='outcome_unknown',
		safe_reason='runner_lost',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, commandID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_outbox SET safe_reason='runner_lost',
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, commandID); err != nil {
		t.Fatal(err)
	}
	resultDigest := controlDigest("late-reconciled-result")
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE control_commands SET status='applied',status_revision=4,outcome='applied',
		safe_reason=NULL,result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, resultDigest, commandID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE control_outbox SET delivery_state='acknowledged',result_sequence=1,
		result_digest=?,result_outcome='applied',safe_reason=NULL,
		acknowledged_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, resultDigest, commandID); err == nil {
		_ = tx.Rollback()
		t.Fatal("reconciled result preceded the outcome-unknown fact")
	}
	_ = tx.Rollback()

	if err := insertCommandAudit(t, fixture, commandID, 5, "effect_outcome_unknown"); err != nil {
		t.Fatalf("claimed-loss audit: %v", err)
	}
	rotateControlAuthority(t, fixture, "reconciled-authority-2")
	if _, err := database.Exec(`UPDATE control_commands SET status='applied',status_revision=4,outcome='applied',
		safe_reason=NULL,result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, resultDigest, commandID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_outbox SET delivery_state='acknowledged',result_sequence=1,
		result_digest=?,result_outcome='applied',safe_reason=NULL,
		acknowledged_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, resultDigest, commandID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 6, "command_applied"); err != nil {
		t.Fatalf("reconciled terminal audit: %v", err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 7, "effect_reconciled"); err != nil {
		t.Fatalf("effect reconciliation audit: %v", err)
	}
}

func TestM147AuditFactsRequireCausalRoots(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)

	// Durable issuance facts cannot precede the exact immutable action seal.
	if _, err := database.Exec(`DROP TRIGGER trg_control_grant_seals_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM control_capability_grant_seals WHERE grant_id=? AND grant_revision=1`, fixture.grantID); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertGrantAudit(t, fixture, 1, 1, "grant_issued", nil) })
	if _, err := database.Exec(`INSERT INTO control_capability_grant_seals(
		grant_id,grant_revision,binding_digest,action_set_digest,action_count) VALUES(?,1,?,?,6)`,
		fixture.grantID, fixture.grantBinding, fixture.grantActions); err != nil {
		t.Fatal(err)
	}
	expires := controlTimeOffset(t, database, "+5 minutes")
	priorityID := "74747474-7474-4474-8474-747474747474"
	if err := insertPriorityCommand(fixture, priorityID, controlDigest("causal-priority"), expires); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertCommandAudit(t, fixture, priorityID, 1, "command_created") })
	if err := insertGrantAudit(t, fixture, 1, 1, "grant_issued", nil); err != nil {
		t.Fatal(err)
	}

	if _, err := database.Exec(`DROP TRIGGER trg_control_lease_seals_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM control_capability_lease_seals WHERE lease_id=? AND lease_revision=1`, fixture.leaseID); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertLeaseAudit(t, fixture, 1, 1, "lease_issued", nil) })
	if _, err := database.Exec(`INSERT INTO control_capability_lease_seals(
		lease_id,lease_revision,binding_digest,action_set_digest,action_count) VALUES(?,1,?,?,4)`,
		fixture.leaseID, fixture.leaseBinding, fixture.leaseActions); err != nil {
		t.Fatal(err)
	}

	if err := insertCommandAudit(t, fixture, priorityID, 1, "command_created"); err != nil {
		t.Fatalf("command should see exact grant fact: %v", err)
	}

	requestID := "75757575-7575-4575-8575-757575757575"
	if err := insertInputRevisionOn(database, fixture, requestID, 1, 1,
		"approval", "approval_required", 0, controlDigest("causal-input"), expires); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertInputAuditOn(t, database, requestID, 1, 1, "input_requested") })
	if _, err := database.Exec(`INSERT INTO control_input_request_seals(request_id,request_revision) VALUES(?,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertInputAuditOn(t, database, requestID, 1, 1, "input_requested") })
	if _, err := database.Exec(`INSERT INTO control_input_request_states(request_id,current_revision,state_revision) VALUES(?,1,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertInputAuditOn(t, database, requestID, 1, 1, "input_requested") })

	pauseID := "76767676-7676-4676-8676-767676767676"
	if err := insertPauseCommand(fixture, pauseID, controlDigest("causal-pause"), 1, 1, expires); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertCommandAudit(t, fixture, pauseID, 1, "command_created") })

	if err := insertLeaseAudit(t, fixture, 1, 1, "lease_issued", nil); err != nil {
		t.Fatal(err)
	}
	if err := insertInputAuditOn(t, database, requestID, 1, 1, "input_requested"); err != nil {
		t.Fatalf("input request did not observe sealed request/state/lease fact: %v", err)
	}
	if err := insertCommandAudit(t, fixture, pauseID, 1, "command_created"); err != nil {
		t.Fatalf("async command did not observe exact lease fact: %v", err)
	}
}

func TestM147ControlEventIDsAreGloballyAppendOrdered(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	for _, invalidID := range []int{-1, 0} {
		requireConstraint(t, func() error {
			return insertGrantAuditWithID(fixture, invalidID, 1, 1, "grant_issued", nil)
		})
	}
	if err := insertGrantAudit(t, fixture, 1, 1, "grant_issued", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE sqlite_sequence SET seq=100 WHERE name='control_events'`); err != nil {
		t.Fatal(err)
	}
	if err := insertLeaseAudit(t, fixture, 1, 1, "lease_issued", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := database.Exec(`UPDATE control_capability_grants
		SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+2 hours'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE grant_id=? AND revision=1`, fixture.grantID); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error {
		return insertGrantAuditWithID(fixture, 2, 2, 1, "grant_renewed", nil)
	})
	if err := insertGrantAudit(t, fixture, 2, 1, "grant_renewed", nil); err != nil {
		t.Fatalf("append-allocated event after rejected backfill: %v", err)
	}
}

func TestM147AuditAndControlHistoryAreRetainedWithoutAuditFKs(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	expires := controlTimeOffset(t, database, "+5 minutes")
	priorityID := "93939393-9393-4393-8393-939393939393"
	if err := insertPriorityCommand(fixture, priorityID, controlDigest("retained-priority"), expires); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, priorityID, 1, "command_created"); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_events SET safe_reason='withdrawn' WHERE command_id=?`, priorityID)
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`DELETE FROM control_events WHERE command_id=?`, priorityID)
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`DELETE FROM control_commands WHERE command_id=?`, priorityID)
		return err
	})

	pauseID := "94949494-9494-4494-8494-949494949494"
	canonical := controlDigest("retained-outbox")
	if err := insertPauseCommand(fixture, pauseID, canonical, 1, 1, expires); err != nil {
		t.Fatal(err)
	}
	acceptControlCommand(t, fixture, pauseID)
	queueControlEffect(t, fixture, pauseID, 1, canonical)
	requireConstraint(t, func() error {
		_, err := database.Exec(`DELETE FROM control_outbox WHERE command_id=?`, pauseID)
		return err
	})

	var eventCount, commandCount, outboxCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_events WHERE command_id=?`, priorityID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_commands WHERE command_id=?`, priorityID).Scan(&commandCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_outbox WHERE command_id=?`, pauseID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || commandCount != 1 || outboxCount != 1 {
		t.Fatalf("retained counts event=%d command=%d outbox=%d", eventCount, commandCount, outboxCount)
	}

	rows, err := database.Query(`PRAGMA foreign_key_list(control_events)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("control_events must remain cascade-unreachable with zero foreign keys")
	}
	violations, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer violations.Close()
	if violations.Next() {
		t.Fatal("seeded control graph violates foreign keys")
	}
}

func TestM147InputRunTerminalRequiresExplicitFinishedRun(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	requestID := "69696969-6969-4969-8969-696969696969"
	if err := insertInputRevisionOn(database, fixture, requestID, 1, 1,
		"approval", "approval_required", 0, controlDigest("run-terminal-input"),
		controlTimeOffset(t, database, "+10 minutes")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_seals(request_id,request_revision) VALUES(?,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_states(request_id,current_revision,state_revision) VALUES(?,1,1)`, requestID); err != nil {
		t.Fatal(err)
	}

	insertTerminal := func(reason any) error {
		_, err := database.Exec(`INSERT INTO control_input_resolution_events(
			request_id,request_revision,sequence,event_kind,event_digest,safe_reason)
			VALUES(?,1,1,'run_terminal',?,?)`, requestID, controlDigest("run-terminal-event"), reason)
		return err
	}
	requireConstraint(t, func() error { return insertTerminal("run_terminal") })
	requireConstraint(t, func() error { return insertTerminal(nil) })

	if _, err := database.Exec(`UPDATE agent_runs SET status='failed',finished_at=datetime('now') WHERE id=?`, fixture.runID); err != nil {
		t.Fatal(err)
	}
	if err := insertTerminal("run_terminal"); err != nil {
		t.Fatalf("explicit terminal run did not authorize run_terminal resolution: %v", err)
	}
}

func TestM147InputCancellationAuditRequiresPriorCancellationFact(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	requestID := "85858585-8585-4585-8585-858585858585"
	if err := insertInputRevisionOn(database, fixture, requestID, 1, 1,
		"approval", "approval_required", 0, controlDigest("cancelled-input"),
		controlTimeOffset(t, database, "+10 minutes")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_seals(request_id,request_revision) VALUES(?,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_states(request_id,current_revision,state_revision) VALUES(?,1,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	if err := insertInputAuditOn(t, database, requestID, 1, 1, "input_requested"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET status='cancelled',finished_at=datetime('now') WHERE id=?`, fixture.runID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO agent_run_cancellation_facts(run_id,cancellation_cause,recorded_at)
		VALUES(?,'server_cancel',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, fixture.runID); err != nil {
		t.Fatal(err)
	}
	terminalDigest := controlDigest("cancelled-input-terminal")
	terminal, err := database.Exec(`INSERT INTO control_input_resolution_events(
		request_id,request_revision,sequence,event_kind,event_digest,safe_reason)
		VALUES(?,1,1,'cancelled',?,'cancelled')`, requestID, terminalDigest)
	if err != nil {
		t.Fatal(err)
	}
	terminalID, _ := terminal.LastInsertId()
	if _, err := database.Exec(`UPDATE control_input_request_states SET state_revision=2,terminal_event_id=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE request_id=?`, terminalID, requestID); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertInputAuditOn(t, database, requestID, 2, 1, "input_cancelled") })
	if _, err := database.Exec(`INSERT INTO control_events(
		sequence,event_kind,cancellation_run_id,agent_run_id,cancellation_cause)
		VALUES(1,'cancellation_recorded',?,?,'server_cancel')`, fixture.runID, fixture.runID); err != nil {
		t.Fatalf("record cancellation audit: %v", err)
	}
	if err := insertInputAuditOn(t, database, requestID, 2, 1, "input_cancelled"); err != nil {
		t.Fatalf("input cancellation did not observe causal audit: %v", err)
	}
}

func TestM147OperatorCancellationAuditRequiresCommandAndEffectFacts(t *testing.T) {
	t.Run("queued cancel is synchronous", func(t *testing.T) {
		database := openTestDB(t)
		fixture := seedControlGraph(t, database)
		insertControlRootAudits(t, fixture)
		if _, err := database.Exec(`UPDATE agent_runs SET status='queued',started_at=NULL WHERE id=?`, fixture.runID); err != nil {
			t.Fatal(err)
		}
		commandID := "96969696-9696-4696-8696-969696969696"
		if err := insertQueuedCancelCommand(fixture, commandID, controlDigest("queued-operator-cancel"),
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
		resultDigest := controlDigest("queued-operator-cancel-result")
		if _, err := database.Exec(`UPDATE control_commands SET status='applied',status_revision=3,outcome='applied',
			result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, resultDigest, commandID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE agent_runs SET status='cancelled',finished_at=datetime('now') WHERE id=?`, fixture.runID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO agent_run_cancellation_facts(
			run_id,cancellation_cause,command_id,recorded_at)
			VALUES(?,'operator_command',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, fixture.runID, commandID); err != nil {
			t.Fatal(err)
		}
		requireConstraint(t, func() error { return insertOperatorCancellationAudit(t, fixture, commandID, 1) })
		if err := insertCommandAudit(t, fixture, commandID, 3, "command_applied"); err != nil {
			t.Fatal(err)
		}
		if err := insertOperatorCancellationAudit(t, fixture, commandID, 1); err != nil {
			t.Fatalf("queued cancellation audit after command fact: %v", err)
		}
	})

	t.Run("running cancel requires acknowledged effect", func(t *testing.T) {
		database := openTestDB(t)
		fixture := seedControlGraph(t, database)
		insertControlRootAudits(t, fixture)
		commandID := "97979797-9797-4797-8797-979797979797"
		canonical := controlDigest("running-operator-cancel")
		if err := insertRunningCancelCommand(fixture, commandID, canonical,
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
		queueControlEffect(t, fixture, commandID, 1, canonical)
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
		resultDigest := controlDigest("running-operator-cancel-result")
		if _, err := database.Exec(`UPDATE control_commands SET status='applied',status_revision=3,outcome='applied',
			result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, resultDigest, commandID); err != nil {
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
		if _, err := database.Exec(`UPDATE agent_runs SET status='cancelled',finished_at=datetime('now') WHERE id=?`, fixture.runID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO agent_run_cancellation_facts(
			run_id,cancellation_cause,command_id,recorded_at)
			VALUES(?,'operator_command',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, fixture.runID, commandID); err != nil {
			t.Fatal(err)
		}
		requireConstraint(t, func() error { return insertOperatorCancellationAudit(t, fixture, commandID, 1) })
		if err := insertCommandAudit(t, fixture, commandID, 6, "effect_acknowledged"); err != nil {
			t.Fatal(err)
		}
		if err := insertOperatorCancellationAudit(t, fixture, commandID, 1); err != nil {
			t.Fatalf("running cancellation audit after effect fact: %v", err)
		}
	})
}

func TestM147InputCommandAndResolutionRequireCausalAudit(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	requestID := "77777777-7777-4777-8777-777777777777"
	expires := controlTimeOffset(t, database, "+5 minutes")
	if err := insertInputRevisionOn(database, fixture, requestID, 1, 1,
		"approval", "approval_required", 0, controlDigest("causal-input-command"), expires); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_seals(request_id,request_revision) VALUES(?,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_states(request_id,current_revision,state_revision) VALUES(?,1,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	commandID := "78787878-7878-4878-8878-787878787878"
	canonical := controlDigest("causal-input-response")
	if err := insertInputResponseCommand(fixture, requestID, commandID, canonical, expires); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertCommandAudit(t, fixture, commandID, 1, "command_created") })
	if err := insertInputAuditOn(t, database, requestID, 1, 1, "input_requested"); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 1, "command_created"); err != nil {
		t.Fatal(err)
	}
	acceptControlCommand(t, fixture, commandID)
	if err := insertCommandAudit(t, fixture, commandID, 2, "command_accepted"); err != nil {
		t.Fatal(err)
	}
	queueControlEffect(t, fixture, commandID, 1, canonical)
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
	resultDigest := controlDigest("causal-input-result")
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
	terminal, err := database.Exec(`INSERT INTO control_input_resolution_events(
		request_id,request_revision,sequence,event_kind,event_digest,command_id)
		VALUES(?,1,1,'approve',?,?)`, requestID, resultDigest, commandID)
	if err != nil {
		t.Fatal(err)
	}
	terminalID, _ := terminal.LastInsertId()
	if _, err := database.Exec(`UPDATE control_input_request_states SET state_revision=2,terminal_event_id=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE request_id=?`, terminalID, requestID); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertResolvedInputAudit(t, fixture, requestID, 2) })
	if err := insertCommandAudit(t, fixture, commandID, 5, "command_applied"); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 6, "effect_acknowledged"); err != nil {
		t.Fatal(err)
	}
	if err := insertResolvedInputAudit(t, fixture, requestID, 2); err != nil {
		t.Fatalf("input resolution did not observe command/effect facts: %v", err)
	}
}

func TestM147InputRevisionPreservesLineageButRotatesRevisionPayload(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	requestID := "95959595-9595-4595-8595-959595959595"
	expires := controlTimeOffset(t, database, "+10 minutes")
	if err := insertInputRevisionOn(database, fixture, requestID, 1, 1,
		"choice", "choice_required", 1, controlDigest("choice-lineage-v1"), expires); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_options(request_id,request_revision,ordinal,option_code)
		VALUES(?,1,1,'choice_1')`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_seals(request_id,request_revision) VALUES(?,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_states(request_id,current_revision,state_revision) VALUES(?,1,1)`, requestID); err != nil {
		t.Fatal(err)
	}
	if err := insertInputAuditOn(t, database, requestID, 1, 1, "input_requested"); err != nil {
		t.Fatal(err)
	}
	terminal, err := database.Exec(`INSERT INTO control_input_resolution_events(
		request_id,request_revision,sequence,event_kind,event_digest,safe_reason)
		VALUES(?,1,1,'superseded',?,'input_superseded')`, requestID, controlDigest("choice-lineage-superseded"))
	if err != nil {
		t.Fatal(err)
	}
	terminalID, _ := terminal.LastInsertId()
	if _, err := database.Exec(`UPDATE control_input_request_states SET state_revision=2,terminal_event_id=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE request_id=?`, terminalID, requestID); err != nil {
		t.Fatal(err)
	}
	if err := insertInputAuditOn(t, database, requestID, 2, 1, "input_superseded"); err != nil {
		t.Fatal(err)
	}

	// Isolate the immutable request-lineage guard from the current-lease guard:
	// a different run/execution is a new request ID, never revision churn.
	if _, err := database.Exec(`DROP TRIGGER trg_control_input_current_binding_guard`); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO control_input_requests(
			request_id,revision,lease_id,lease_revision,delivery_id,delivery_key,delivery_revision,
			project_id,root_issue_id,issue_revision,attempt_id,attempt_number,plan_revision,stage_key,
			execution_number,execution_start_stage_event_id,authority_epoch,authority_stage_event_id,
			reporter_id,agent_run_id,request_kind,prompt_template,option_count,request_digest,expires_at)
			SELECT ?,2,lease_id,revision,delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,
			 issue_revision,attempt_id,attempt_number,plan_revision,stage_key,execution_number,
			 execution_start_stage_event_id,authority_epoch,authority_stage_event_id,reporter_id,agent_run_id+1,
			 'choice','choice_required',2,?,?
			FROM control_capability_leases WHERE lease_id=? AND revision=1`,
			requestID, controlDigest("wrong-run-lineage"), expires, fixture.leaseID)
		return err
	})

	if err := insertInputRevisionOn(database, fixture, requestID, 2, 1,
		"choice", "choice_required", 2, controlDigest("choice-lineage-v2"), expires); err != nil {
		t.Fatalf("revision-scoped digest/option rotation: %v", err)
	}
	for ordinal := 1; ordinal <= 2; ordinal++ {
		if _, err := database.Exec(`INSERT INTO control_input_request_options(request_id,request_revision,ordinal,option_code)
			VALUES(?,2,?,'choice_'||?)`, requestID, ordinal, ordinal); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO control_input_request_seals(request_id,request_revision) VALUES(?,2)`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_input_request_states SET current_revision=2,state_revision=3,
		terminal_event_id=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE request_id=?`, requestID); err != nil {
		t.Fatal(err)
	}
	if err := insertInputAuditOn(t, database, requestID, 3, 2, "input_requested"); err != nil {
		t.Fatalf("revision-scoped request audit: %v", err)
	}
	requireConstraint(t, func() error {
		_, err := database.Exec(`INSERT INTO control_input_request_options(request_id,request_revision,ordinal,option_code)
			VALUES(?,1,2,'choice_2')`, requestID)
		return err
	})
}

func TestM147RuntimeRevisionCommandRequiresPriorRuntimeAudit(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	if _, err := database.Exec(`DROP TRIGGER trg_control_runtime_command_proof_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_runtime_states SET state='paused',revision=2,
		last_command_id='82828282-8282-4282-8282-828282828282',last_result_digest=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE agent_run_id=?`,
		controlDigest("synthetic-prior-runtime-result"), fixture.runID); err != nil {
		t.Fatal(err)
	}
	commandID := "83838383-8383-4383-8383-838383838383"
	if err := insertRuntimeCommand(fixture, commandID, "run.resume", controlDigest("causal-resume"), 1, 1, 2,
		controlTimeOffset(t, database, "+5 minutes")); err != nil {
		t.Fatal(err)
	}
	requireConstraint(t, func() error { return insertCommandAudit(t, fixture, commandID, 1, "command_created") })
}

func TestM147TerminalCommandShapesRejectMissingTruth(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	expires := controlTimeOffset(t, database, "+5 minutes")

	for i, mutation := range []string{
		`UPDATE control_commands SET status='expired',status_revision=2,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`,
		`UPDATE control_commands SET status='applied',status_revision=3,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`,
		`UPDATE control_commands SET status='rejected',status_revision=3,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`,
	} {
		commandID := []string{
			"70707070-7070-4070-8070-707070707070",
			"71717171-7171-4171-8171-717171717171",
			"72727272-7272-4272-8272-727272727272",
		}[i]
		if err := insertPriorityCommand(fixture, commandID, controlDigest(commandID), expires); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			acceptControlCommand(t, fixture, commandID)
		}
		requireConstraint(t, func() error {
			_, err := database.Exec(mutation, commandID)
			return err
		})
	}
}

func TestM147RejectedCommandCannotBorrowExpiryOrUnknownReasons(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	for i, reason := range []string{"withdrawn", "confirmation_expired", "runner_lost"} {
		commandID := []string{
			"79797979-7979-4979-8979-797979797979",
			"80808080-8080-4080-8080-808080808080",
			"81818181-8181-4181-8181-818181818181",
		}[i]
		if err := insertPriorityCommand(fixture, commandID, controlDigest("reserved-reject-"+reason),
			controlTimeOffset(t, database, "+5 minutes")); err != nil {
			t.Fatal(err)
		}
		acceptControlCommand(t, fixture, commandID)
		requireConstraint(t, func() error {
			_, err := database.Exec(`UPDATE control_commands SET status='rejected',status_revision=3,
				outcome='rejected',safe_reason=?,result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
				updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`,
				reason, controlDigest("reserved-result-"+reason), commandID)
			return err
		})
	}
}

func TestM147CommandExpiryLifecycleUsesStrictChallengeBoundary(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)

	withdrawID := "98989898-9898-4898-8898-989898989898"
	if err := insertPriorityCommand(fixture, withdrawID, controlDigest("withdraw-before-expiry"),
		controlTimeOffset(t, database, "+5 minutes")); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, withdrawID, 1, "command_created"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_commands SET status='expired',status_revision=2,
		safe_reason='withdrawn',terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, withdrawID); err != nil {
		t.Fatalf("pre-expiry withdraw: %v", err)
	}
	if err := insertCommandAudit(t, fixture, withdrawID, 2, "command_withdrawn"); err != nil {
		t.Fatalf("withdraw audit: %v", err)
	}
	requireConstraint(t, func() error { return insertCommandAudit(t, fixture, withdrawID, 3, "command_expired") })

	boundary := controlTimeOffset(t, database, "+0.50 seconds")
	expireID := "a9a9a9a9-a9a9-49a9-89a9-a9a9a9a9a9a9"
	acceptedID := "b9b9b9b9-b9b9-49b9-89b9-b9b9b9b9b9b9"
	if err := insertPriorityCommand(fixture, expireID, controlDigest("expire-at-boundary"), boundary); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, expireID, 1, "command_created"); err != nil {
		t.Fatal(err)
	}
	if err := insertPriorityCommand(fixture, acceptedID, controlDigest("accepted-never-expires"), boundary); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, acceptedID, 1, "command_created"); err != nil {
		t.Fatal(err)
	}
	acceptControlCommand(t, fixture, acceptedID)
	if err := insertCommandAudit(t, fixture, acceptedID, 2, "command_accepted"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(700 * time.Millisecond)
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_commands SET status='accepted',status_revision=2,
			accepted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE command_id=?`, expireID)
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_commands SET status='expired',status_revision=2,
			safe_reason='withdrawn',terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, expireID)
		return err
	})
	if _, err := database.Exec(`UPDATE control_commands SET status='expired',status_revision=2,
		safe_reason='confirmation_expired',terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, expireID); err != nil {
		t.Fatalf("challenge expiry at/after boundary: %v", err)
	}
	if err := insertCommandAudit(t, fixture, expireID, 2, "command_expired"); err != nil {
		t.Fatalf("expiry audit: %v", err)
	}
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_commands SET status='expired',status_revision=3,
			safe_reason='confirmation_expired',terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, acceptedID)
		return err
	})
	var acceptedStatus string
	if err := database.QueryRow(`SELECT status FROM control_commands WHERE command_id=?`, acceptedID).Scan(&acceptedStatus); err != nil {
		t.Fatal(err)
	}
	if acceptedStatus != "accepted" {
		t.Fatalf("accepted command status=%q after challenge deadline", acceptedStatus)
	}
}

func TestM147ExpiredCapabilitiesCannotBeRevived(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraphWithExpiries(t, database, "+0.20 seconds", "+0.20 seconds")
	if err := insertGrantAudit(t, fixture, 1, 1, "grant_issued", nil); err != nil {
		t.Fatal(err)
	}
	if err := insertLeaseAudit(t, fixture, 1, 1, "lease_issued", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	for _, table := range []string{"control_capability_grants", "control_capability_leases"} {
		idColumn := "grant_id"
		id := fixture.grantID
		if table == "control_capability_leases" {
			idColumn, id = "lease_id", fixture.leaseID
		}
		requireConstraint(t, func() error {
			_, err := database.Exec(`UPDATE `+table+` SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour'),
				updated_at=updated_at WHERE `+idColumn+`=? AND revision=1`, id)
			return err
		})
		requireConstraint(t, func() error {
			_, err := database.Exec(`UPDATE `+table+` SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour'),
				revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
				WHERE `+idColumn+`=? AND revision=1`, id)
			return err
		})
		if _, err := database.Exec(`UPDATE `+table+` SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE `+idColumn+`=? AND revision=1`, id); err != nil {
			t.Fatalf("terminalize expired %s: %v", table, err)
		}
	}
	requireConstraint(t, func() error { return insertGrantAudit(t, fixture, 2, 1, "grant_revoked", "capability_revoked") })
	requireConstraint(t, func() error { return insertLeaseAudit(t, fixture, 2, 1, "lease_revoked", "lease_revoked") })
	if err := insertGrantAudit(t, fixture, 2, 1, "grant_expired", "capability_expired"); err != nil {
		t.Fatalf("grant expiry audit: %v", err)
	}
	if err := insertLeaseAudit(t, fixture, 2, 1, "lease_expired", "lease_expired"); err != nil {
		t.Fatalf("lease expiry audit: %v", err)
	}

	// Terminal expiry permits exact next-revision reacquisition, including an
	// otherwise unchanged binding; it never revives the old row.
	if err := insertGrantRevision(fixture, fixture.grantID, 2, controlCredentialA,
		fixture.grantBinding, fixture.grantActions, 1); err != nil {
		t.Fatalf("grant reacquisition: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO control_capability_grant_actions(grant_id,grant_revision,action)
		VALUES(?,2,'issue.priority.set')`, fixture.grantID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_capability_grant_seals(
		grant_id,grant_revision,binding_digest,action_set_digest,action_count) VALUES(?,2,?,?,1)`,
		fixture.grantID, fixture.grantBinding, fixture.grantActions); err != nil {
		t.Fatal(err)
	}
	if err := insertLeaseRevision(fixture, fixture.leaseID, 2, fixture.runnerAPIKeyID, "runner-01",
		fixture.leaseBinding, fixture.leaseActions, 1); err != nil {
		t.Fatalf("lease reacquisition: %v", err)
	}
}

func TestM147SameRevisionRenewalRequiresRealTTLAdvance(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	requireConstraint(t, func() error { return insertGrantAudit(t, fixture, 2, 1, "grant_renewed", nil) })
	requireConstraint(t, func() error { return insertLeaseAudit(t, fixture, 2, 1, "lease_renewed", nil) })
	time.Sleep(2 * time.Millisecond)
	if _, err := database.Exec(`UPDATE control_capability_grants
		SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+2 hours'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE grant_id=? AND revision=1`, fixture.grantID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_capability_leases
		SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+2 hours'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE lease_id=? AND revision=1`, fixture.leaseID); err != nil {
		t.Fatal(err)
	}
	// Every row transition must be narrated before a later transition can
	// replace its exact old-state proof in the immutable event stream.
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_capability_grants
			SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+3 hours'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE grant_id=? AND revision=1`, fixture.grantID)
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_capability_grants
			SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE grant_id=? AND revision=1`, fixture.grantID)
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_capability_leases
			SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+3 hours'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE lease_id=? AND revision=1`, fixture.leaseID)
		return err
	})
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_capability_leases
			SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE lease_id=? AND revision=1`, fixture.leaseID)
		return err
	})
	if err := insertGrantAudit(t, fixture, 2, 1, "grant_renewed", nil); err != nil {
		t.Fatalf("real grant TTL renewal audit: %v", err)
	}
	if err := insertLeaseAudit(t, fixture, 2, 1, "lease_renewed", nil); err != nil {
		t.Fatalf("real lease TTL renewal audit: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := database.Exec(`UPDATE control_capability_grants
		SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+3 hours'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE grant_id=? AND revision=1`, fixture.grantID); err != nil {
		t.Fatalf("grant update after exact renewal fact: %v", err)
	}
	if _, err := database.Exec(`UPDATE control_capability_leases
		SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+3 hours'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE lease_id=? AND revision=1`, fixture.leaseID); err != nil {
		t.Fatalf("lease update after exact renewal fact: %v", err)
	}
	if err := insertGrantAudit(t, fixture, 3, 1, "grant_renewed", nil); err != nil {
		t.Fatalf("second grant TTL renewal audit: %v", err)
	}
	if err := insertLeaseAudit(t, fixture, 3, 1, "lease_renewed", nil); err != nil {
		t.Fatalf("second lease TTL renewal audit: %v", err)
	}
}

func TestM147CapturedLeaseExpiryCannotBeRenewedIntoClaim(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraphWithExpiries(t, database, "+1 hour", "+1.50 seconds")
	insertControlRootAudits(t, fixture)
	var capturedExpiry string
	if err := database.QueryRow(`SELECT expires_at FROM control_capability_leases WHERE lease_id=? AND revision=1`, fixture.leaseID).Scan(&capturedExpiry); err != nil {
		t.Fatal(err)
	}
	commandID := "73737373-7373-4373-8373-737373737373"
	canonical := controlDigest("captured-lease-expiry")
	if err := insertPauseCommand(fixture, commandID, canonical, 1, 1, capturedExpiry); err != nil {
		t.Fatal(err)
	}
	acceptControlCommand(t, fixture, commandID)
	queueControlEffect(t, fixture, commandID, 1, canonical)
	if _, err := database.Exec(`UPDATE control_capability_leases
		SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE lease_id=? AND revision=1`, fixture.leaseID); err != nil {
		t.Fatalf("pre-expiry same-revision renewal: %v", err)
	}
	time.Sleep(1700 * time.Millisecond)
	requireConstraint(t, func() error {
		_, err := database.Exec(`UPDATE control_outbox SET delivery_state='claimed',claim_sequence=1,
			claim_user_id=?,claim_principal_kind='api_key',claim_api_key_id=?,claim_device_id='runner-01',
			claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE command_id=?`, fixture.runnerUserID, fixture.runnerAPIKeyID, commandID)
		return err
	})
}

func TestM147OutboxClaimRechecksCurrentRunnerAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, controlGraphFixture)
	}{
		{
			name: "run terminalized",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE agent_runs SET status='failed',finished_at=datetime('now') WHERE id=?`, fixture.runID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "stage authority rotated",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				envelope, err := fixture.database.Exec(`INSERT INTO delivery_events(
					delivery_id,delivery_revision,idempotency_key,payload_hash,kind,reporter_id,server_received_at)
					VALUES(?,?,?,zeroblob(32),'handoff',?,'2026-08-20T10:03:00Z')`,
					fixture.deliveryID, fixture.deliveryRevision+1, "claim-authority-2", fixture.reporterID)
				if err != nil {
					t.Fatal(err)
				}
				envelopeID, _ := envelope.LastInsertId()
				authority, err := fixture.database.Exec(`INSERT INTO delivery_stage_events(
					delivery_id,attempt_id,stage_key,execution_number,event_sequence,authority_epoch,delivery_event_id,
					event_type,reporter_id,execution_start_stage_event_id,previous_stage_event_id,handoff_from_reporter_id,
					authority_source_sequence_cutoff,reason_code,server_received_at)
					VALUES(?,?,'specification',1,2,2,?,'handoff',?,?,?,?,0,'authority_rotated','2026-08-20T10:03:00Z')`,
					fixture.deliveryID, fixture.attemptID, envelopeID, fixture.reporterID, fixture.startEventID,
					fixture.startEventID, fixture.reporterID)
				if err != nil {
					t.Fatal(err)
				}
				authorityEventID, _ := authority.LastInsertId()
				if _, err := fixture.database.Exec(`INSERT INTO delivery_agent_run_activations(
					delivery_id,attempt_id,stage_key,execution_number,authority_epoch,agent_run_id,reporter_id,
					authority_stage_event_id,telemetry_sequence_cutoff,created_at)
					VALUES(?,?,'specification',1,2,?,?,?,0,'2026-08-20T10:03:00Z')`, fixture.deliveryID,
					fixture.attemptID, fixture.runID, fixture.reporterID, authorityEventID); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.database.Exec(`UPDATE delivery_stage_latest SET authority_epoch=2,
					authority_stage_event_id=?,updated_at='2026-08-20T10:03:00Z'
					WHERE delivery_id=? AND attempt_id=? AND stage_key='specification'`,
					authorityEventID, fixture.deliveryID, fixture.attemptID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "runner key disabled",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE api_keys SET disabled_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, fixture.runnerAPIKeyID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "runner user disabled",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE users SET status='inactive' WHERE id=?`, fixture.runnerUserID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "issue hidden",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE issues SET deleted_at=datetime('now') WHERE id=?`, fixture.issueID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "project archived",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE projects SET status='archived' WHERE id=?`, fixture.projectID); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestDB(t)
			fixture := seedControlGraph(t, database)
			insertControlRootAudits(t, fixture)
			commandID := fmt.Sprintf("%08x-2020-4020-8020-%012x", len(test.name), len(test.name))
			canonical := controlDigest("claim-current-authority-" + test.name)
			if err := insertPauseCommand(fixture, commandID, canonical, 1, 1,
				controlTimeOffset(t, database, "+5 minutes")); err != nil {
				t.Fatal(err)
			}
			acceptControlCommand(t, fixture, commandID)
			queueControlEffect(t, fixture, commandID, 1, canonical)
			test.mutate(t, fixture)
			requireConstraint(t, func() error {
				_, err := database.Exec(`UPDATE control_outbox SET delivery_state='claimed',claim_sequence=1,
					claim_user_id=?,claim_principal_kind='api_key',claim_api_key_id=?,claim_device_id='runner-01',
					claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
					WHERE command_id=?`, fixture.runnerUserID, fixture.runnerAPIKeyID, commandID)
				return err
			})
		})
	}
}

func TestM147DirectResultRechecksCurrentRunnerAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, controlGraphFixture)
	}{
		{
			name: "runner key disabled",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE api_keys SET disabled_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, fixture.runnerAPIKeyID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "runner user inactive",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE users SET status='inactive' WHERE id=?`, fixture.runnerUserID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "run terminalized",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE agent_runs SET status='failed',finished_at=datetime('now') WHERE id=?`, fixture.runID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "authority rotated",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				rotateControlAuthority(t, fixture, "result-authority-2")
			},
		},
		{
			name: "issue revision changed",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE issues SET status=status WHERE id=?`, fixture.issueID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "grant revoked",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE control_capability_grants
					SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
					WHERE grant_id=? AND revision=1`, fixture.grantID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "project archived for pause",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE projects SET status='archived' WHERE id=?`, fixture.projectID); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestDB(t)
			fixture := seedControlGraph(t, database)
			insertControlRootAudits(t, fixture)
			commandID := fmt.Sprintf("%08x-3030-4030-8030-%012x", len(test.name), len(test.name))
			canonical := controlDigest("result-current-authority-" + test.name)
			if err := insertPauseCommand(fixture, commandID, canonical, 1, 1,
				controlTimeOffset(t, database, "+5 minutes")); err != nil {
				t.Fatal(err)
			}
			acceptControlCommand(t, fixture, commandID)
			queueControlEffect(t, fixture, commandID, 1, canonical)
			if err := claimControlEffect(fixture, commandID); err != nil {
				t.Fatalf("claim before fence: %v", err)
			}
			test.mutate(t, fixture)
			requireConstraint(t, func() error {
				return directControlResult(fixture, commandID, "applied", "applied", nil,
					controlDigest("stale-result-"+test.name))
			})
			var status, deliveryState string
			if err := database.QueryRow(`SELECT command.status,outbox.delivery_state FROM control_commands command
				JOIN control_outbox outbox ON outbox.command_id=command.command_id WHERE command.command_id=?`, commandID).
				Scan(&status, &deliveryState); err != nil {
				t.Fatal(err)
			}
			if status != "accepted" || deliveryState != "claimed" {
				t.Fatalf("stale result escaped transaction: status=%s outbox=%s", status, deliveryState)
			}
		})
	}
}

func TestM147DirectResultReasonTruthAndNaturalExit(t *testing.T) {
	for _, terminalStatus := range []string{"completed", "drafted"} {
		t.Run("natural exit "+terminalStatus, func(t *testing.T) {
			database := openTestDB(t)
			fixture := seedControlGraph(t, database)
			insertControlRootAudits(t, fixture)
			commandID := "30303030-3030-4030-8030-303030303030"
			canonical := controlDigest("natural-exit-" + terminalStatus)
			if err := insertRunningCancelCommand(fixture, commandID, canonical,
				controlTimeOffset(t, database, "+5 minutes")); err != nil {
				t.Fatal(err)
			}
			acceptControlCommand(t, fixture, commandID)
			queueControlEffect(t, fixture, commandID, 1, canonical)
			if err := claimControlEffect(fixture, commandID); err != nil {
				t.Fatal(err)
			}
			requireConstraint(t, func() error {
				return directControlResult(fixture, commandID, "rejected", "rejected", "natural_exit",
					controlDigest("premature-natural-exit"))
			})
			if _, err := database.Exec(`UPDATE agent_runs SET status=?,finished_at=datetime('now') WHERE id=?`, terminalStatus, fixture.runID); err != nil {
				t.Fatal(err)
			}
			if err := directControlResult(fixture, commandID, "rejected", "rejected", "natural_exit",
				controlDigest("verified-natural-exit")); err != nil {
				t.Fatalf("verified natural exit: %v", err)
			}
		})
	}

	t.Run("cancellation fact cannot be relabelled natural", func(t *testing.T) {
		database := openTestDB(t)
		fixture := seedControlGraph(t, database)
		insertControlRootAudits(t, fixture)
		commandID := "31313131-3131-4131-8131-313131313131"
		canonical := controlDigest("cancelled-not-natural")
		if err := insertRunningCancelCommand(fixture, commandID, canonical,
			controlTimeOffset(t, database, "+5 minutes")); err != nil {
			t.Fatal(err)
		}
		acceptControlCommand(t, fixture, commandID)
		queueControlEffect(t, fixture, commandID, 1, canonical)
		if err := claimControlEffect(fixture, commandID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE agent_runs SET status='cancelled',finished_at=datetime('now') WHERE id=?`, fixture.runID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO agent_run_cancellation_facts(run_id,cancellation_cause,recorded_at)
			VALUES(?,'server_cancel',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, fixture.runID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE agent_runs SET status='failed' WHERE id=?`, fixture.runID); err != nil {
			t.Fatal(err)
		}
		requireConstraint(t, func() error {
			return directControlResult(fixture, commandID, "rejected", "rejected", "natural_exit",
				controlDigest("false-natural-exit"))
		})
	})

	t.Run("stale reason is not a direct runner result", func(t *testing.T) {
		database := openTestDB(t)
		fixture := seedControlGraph(t, database)
		insertControlRootAudits(t, fixture)
		commandID := "32323232-3232-4232-8232-323232323232"
		canonical := controlDigest("false-direct-reason")
		if err := insertPauseCommand(fixture, commandID, canonical, 1, 1,
			controlTimeOffset(t, database, "+5 minutes")); err != nil {
			t.Fatal(err)
		}
		acceptControlCommand(t, fixture, commandID)
		queueControlEffect(t, fixture, commandID, 1, canonical)
		if err := claimControlEffect(fixture, commandID); err != nil {
			t.Fatal(err)
		}
		for _, reason := range []string{"input_expired", "runtime_state_changed", "credential_revoked", "authority_changed"} {
			requireConstraint(t, func() error {
				return directControlResult(fixture, commandID, "rejected", "rejected", reason,
					controlDigest("false-direct-"+reason))
			})
		}
		if err := directControlResult(fixture, commandID, "rejected", "rejected", "effect_rejected",
			controlDigest("valid-direct-effect-rejected")); err != nil {
			t.Fatalf("valid direct rejection: %v", err)
		}
	})
}

func TestM147AcceptedCASReservesTypedEffects(t *testing.T) {
	t.Run("runtime revision", func(t *testing.T) {
		database := openTestDB(t)
		fixture := seedControlGraph(t, database)
		insertControlRootAudits(t, fixture)
		expires := controlTimeOffset(t, database, "+5 minutes")
		firstID := "41414141-4141-4141-8141-414141414141"
		secondID := "42424242-4242-4242-8242-424242424242"
		firstDigest := controlDigest("runtime-reservation-first")
		secondDigest := controlDigest("runtime-reservation-second")
		if err := insertPauseCommand(fixture, firstID, firstDigest, 1, 1, expires); err != nil {
			t.Fatal(err)
		}
		if err := insertPauseCommand(fixture, secondID, secondDigest, 1, 1, expires); err != nil {
			t.Fatal(err)
		}
		acceptControlCommand(t, fixture, firstID)
		queueControlEffect(t, fixture, firstID, 1, firstDigest)
		requireConstraint(t, func() error { return acceptControlCommandErr(fixture, secondID) })
		if err := claimControlEffect(fixture, firstID); err != nil {
			t.Fatalf("claim first runtime effect: %v", err)
		}
		if err := directControlResult(fixture, firstID, "rejected", "rejected", "effect_rejected",
			controlDigest("runtime-reservation-rejected")); err != nil {
			t.Fatalf("reject claimed runtime effect: %v", err)
		}
		if err := acceptControlCommandErr(fixture, secondID); err != nil {
			t.Fatalf("rejected runtime effect did not release reservation: %v", err)
		}
		queueControlEffect(t, fixture, secondID, 1, secondDigest)
		if err := claimControlEffect(fixture, secondID); err != nil {
			t.Fatalf("runtime retry claim: %v", err)
		}
	})

	t.Run("input request revision", func(t *testing.T) {
		database := openTestDB(t)
		fixture := seedControlGraph(t, database)
		insertControlRootAudits(t, fixture)
		expires := controlTimeOffset(t, database, "+5 minutes")
		requestID := "43434343-4343-4343-8343-434343434343"
		seedApprovalInputRequest(t, fixture, requestID, expires)
		firstID := "44444444-4444-4444-8444-444444444444"
		secondID := "45454545-4545-4545-8545-454545454545"
		firstDigest := controlDigest("input-reservation-first")
		secondDigest := controlDigest("input-reservation-second")
		if err := insertInputResponseCommand(fixture, requestID, firstID, firstDigest, expires); err != nil {
			t.Fatal(err)
		}
		if err := insertInputResponseCommand(fixture, requestID, secondID, secondDigest, expires); err != nil {
			t.Fatal(err)
		}
		acceptControlCommand(t, fixture, firstID)
		queueControlEffect(t, fixture, firstID, 1, firstDigest)
		requireConstraint(t, func() error { return acceptControlCommandErr(fixture, secondID) })
		if err := claimControlEffect(fixture, firstID); err != nil {
			t.Fatalf("claim first input effect: %v", err)
		}
		if err := directControlResult(fixture, firstID, "rejected", "rejected", "effect_rejected",
			controlDigest("input-reservation-rejected")); err != nil {
			t.Fatalf("reject claimed input effect: %v", err)
		}
		if err := acceptControlCommandErr(fixture, secondID); err != nil {
			t.Fatalf("rejected input effect did not release reservation: %v", err)
		}
		queueControlEffect(t, fixture, secondID, 1, secondDigest)
		if err := claimControlEffect(fixture, secondID); err != nil {
			t.Fatalf("input retry claim: %v", err)
		}
	})

	t.Run("running cancel execution", func(t *testing.T) {
		database := openTestDB(t)
		fixture := seedControlGraph(t, database)
		insertControlRootAudits(t, fixture)
		expires := controlTimeOffset(t, database, "+5 minutes")
		firstID := "46464646-4646-4646-8646-464646464646"
		secondID := "47474747-4747-4747-8747-474747474747"
		firstDigest := controlDigest("cancel-reservation-first")
		secondDigest := controlDigest("cancel-reservation-second")
		if err := insertRunningCancelCommand(fixture, firstID, firstDigest, expires); err != nil {
			t.Fatal(err)
		}
		if err := insertRunningCancelCommand(fixture, secondID, secondDigest, expires); err != nil {
			t.Fatal(err)
		}
		acceptControlCommand(t, fixture, firstID)
		queueControlEffect(t, fixture, firstID, 1, firstDigest)
		requireConstraint(t, func() error { return acceptControlCommandErr(fixture, secondID) })
		if err := claimControlEffect(fixture, firstID); err != nil {
			t.Fatalf("claim first cancel effect: %v", err)
		}
		if err := directControlResult(fixture, firstID, "rejected", "rejected", "effect_rejected",
			controlDigest("cancel-reservation-rejected")); err != nil {
			t.Fatalf("reject claimed cancel effect: %v", err)
		}
		if err := acceptControlCommandErr(fixture, secondID); err != nil {
			t.Fatalf("rejected cancel effect did not release reservation: %v", err)
		}
		queueControlEffect(t, fixture, secondID, 1, secondDigest)
		if err := claimControlEffect(fixture, secondID); err != nil {
			t.Fatalf("cancel retry claim: %v", err)
		}
	})

	t.Run("queued cancel run", func(t *testing.T) {
		database := openTestDB(t)
		fixture := seedControlGraph(t, database)
		insertControlRootAudits(t, fixture)
		if _, err := database.Exec(`UPDATE agent_runs SET status='queued',started_at=NULL WHERE id=?`, fixture.runID); err != nil {
			t.Fatal(err)
		}
		expires := controlTimeOffset(t, database, "+5 minutes")
		firstID := "48484848-4848-4848-8848-484848484848"
		secondID := "49494949-4949-4949-8949-494949494949"
		if err := insertQueuedCancelCommand(fixture, firstID, controlDigest("queued-cancel-reservation-first"), expires); err != nil {
			t.Fatal(err)
		}
		if err := insertQueuedCancelCommand(fixture, secondID, controlDigest("queued-cancel-reservation-second"), expires); err != nil {
			t.Fatal(err)
		}
		acceptControlCommand(t, fixture, firstID)
		requireConstraint(t, func() error { return acceptControlCommandErr(fixture, secondID) })
		if _, err := database.Exec(`UPDATE control_commands SET status='rejected',status_revision=3,
			outcome='rejected',safe_reason='stale_target',result_digest=?,
			terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE command_id=?`, controlDigest("queued-cancel-release"), firstID); err != nil {
			t.Fatal(err)
		}
		if err := acceptControlCommandErr(fixture, secondID); err != nil {
			t.Fatalf("rejected queued cancel did not release reservation: %v", err)
		}
	})
}

func TestM147CommandAcceptanceRechecksLifecycleAndStructure(t *testing.T) {
	tests := []struct {
		name       string
		action     string
		project    string
		mutate     func(*testing.T, controlGraphFixture)
		wantAccept bool
	}{
		{name: "active pause", action: "run.pause", wantAccept: true},
		{name: "frozen transition stales pause", action: "run.pause", project: "frozen", wantAccept: false},
		{name: "archived pause", action: "run.pause", project: "archived", wantAccept: false},
		{name: "archived transition stales running cancel", action: "run.cancel.running", project: "archived", wantAccept: false},
		{name: "deleted running cancel", action: "run.cancel.running", project: "deleted", wantAccept: false},
		{
			name:   "issue revision drift",
			action: "run.pause",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE issues SET status=status WHERE id=?`, fixture.issueID); err != nil {
					t.Fatal(err)
				}
			},
			wantAccept: false,
		},
		{
			name:   "grant revoked",
			action: "run.pause",
			mutate: func(t *testing.T, fixture controlGraphFixture) {
				if _, err := fixture.database.Exec(`UPDATE control_capability_grants
					SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
					WHERE grant_id=? AND revision=1`, fixture.grantID); err != nil {
					t.Fatal(err)
				}
			},
			wantAccept: false,
		},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestDB(t)
			fixture := seedControlGraph(t, database)
			insertControlRootAudits(t, fixture)
			commandID := fmt.Sprintf("%08x-5050-4050-8050-%012x", i+1, i+1)
			canonical := controlDigest("accept-lifecycle-" + test.name)
			expires := controlTimeOffset(t, database, "+5 minutes")
			var err error
			if test.action == "run.cancel.running" {
				err = insertRunningCancelCommand(fixture, commandID, canonical, expires)
			} else {
				err = insertPauseCommand(fixture, commandID, canonical, 1, 1, expires)
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.project != "" {
				if _, err := database.Exec(`UPDATE projects SET status=? WHERE id=?`, test.project, fixture.projectID); err != nil {
					t.Fatal(err)
				}
			}
			if test.mutate != nil {
				test.mutate(t, fixture)
			}
			err = acceptControlCommandErr(fixture, commandID)
			if test.wantAccept {
				if err != nil {
					t.Fatalf("valid acceptance failed: %v", err)
				}
				queueControlEffect(t, fixture, commandID, 1, canonical)
				if err := claimControlEffect(fixture, commandID); err != nil {
					t.Fatalf("valid lifecycle claim failed: %v", err)
				}
			} else if err == nil {
				t.Fatal("stale lifecycle challenge was accepted")
			}
		})
	}
}

func TestM147InputTerminalEventIsAuthoritativeAtAcceptClaimAndResult(t *testing.T) {
	for _, fence := range []string{"accept", "claim", "result"} {
		t.Run(fence, func(t *testing.T) {
			database := openTestDB(t)
			fixture := seedControlGraph(t, database)
			insertControlRootAudits(t, fixture)
			expires := controlTimeOffset(t, database, "+5 minutes")
			requestID := fmt.Sprintf("%08x-6060-4060-8060-%012x", len(fence), len(fence))
			commandID := fmt.Sprintf("%08x-6161-4161-8161-%012x", len(fence), len(fence))
			seedApprovalInputRequest(t, fixture, requestID, expires)
			canonical := controlDigest("orphan-terminal-" + fence)
			if err := insertInputResponseCommand(fixture, requestID, commandID, canonical, expires); err != nil {
				t.Fatal(err)
			}
			if fence != "accept" {
				acceptControlCommand(t, fixture, commandID)
				queueControlEffect(t, fixture, commandID, 1, canonical)
			}
			if fence == "result" {
				if err := claimControlEffect(fixture, commandID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := database.Exec(`INSERT INTO control_input_resolution_events(
				request_id,request_revision,sequence,event_kind,event_digest,safe_reason)
				VALUES(?,1,1,'superseded',?,'input_superseded')`, requestID,
				controlDigest("orphan-terminal-event-"+fence)); err != nil {
				t.Fatal(err)
			}
			switch fence {
			case "accept":
				requireConstraint(t, func() error { return acceptControlCommandErr(fixture, commandID) })
			case "claim":
				requireConstraint(t, func() error { return claimControlEffect(fixture, commandID) })
			case "result":
				requireConstraint(t, func() error {
					return directControlResult(fixture, commandID, "applied", "applied", nil,
						controlDigest("orphan-terminal-result"))
				})
			}
		})
	}
}

func TestM147SoftDeletedClaimedEffectRequiresUnknownReconciliation(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	commandID := "62626262-6262-4262-8262-626262626262"
	canonical := controlDigest("soft-delete-reconciliation")
	if err := insertPauseCommand(fixture, commandID, canonical, 1, 1,
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
	queueControlEffect(t, fixture, commandID, 1, canonical)
	if err := insertCommandAudit(t, fixture, commandID, 3, "effect_queued"); err != nil {
		t.Fatal(err)
	}
	if err := claimControlEffect(fixture, commandID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 4, "effect_claimed"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE issues SET deleted_at=datetime('now') WHERE id=?`, fixture.issueID); err != nil {
		t.Fatal(err)
	}
	resultDigest := controlDigest("post-delete-result")
	requireConstraint(t, func() error {
		return directControlResult(fixture, commandID, "applied", "applied", nil, resultDigest)
	})
	if _, err := database.Exec(`UPDATE control_commands SET status_revision=3,outcome='outcome_unknown',
		safe_reason='runner_lost',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, commandID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_outbox SET safe_reason='runner_lost',
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, commandID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 5, "effect_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_commands SET status='applied',status_revision=4,outcome='applied',
		safe_reason=NULL,result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, resultDigest, commandID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_outbox SET delivery_state='acknowledged',result_sequence=1,
		result_digest=?,result_outcome='applied',safe_reason=NULL,
		acknowledged_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, resultDigest, commandID); err != nil {
		t.Fatalf("post-fence reconciliation result: %v", err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 6, "command_applied"); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 7, "effect_reconciled"); err != nil {
		t.Fatal(err)
	}
}

func TestM147ArchivedClaimedCancellationRequiresUnknownReconciliation(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	commandID := "68686868-6868-4868-8868-686868686868"
	canonical := controlDigest("archived-claimed-cancellation")
	if err := insertRunningCancelCommand(fixture, commandID, canonical,
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
	queueControlEffect(t, fixture, commandID, 1, canonical)
	if err := insertCommandAudit(t, fixture, commandID, 3, "effect_queued"); err != nil {
		t.Fatal(err)
	}
	if err := claimControlEffect(fixture, commandID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 4, "effect_claimed"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE projects SET status='archived' WHERE id=?`, fixture.projectID); err != nil {
		t.Fatal(err)
	}
	resultDigest := controlDigest("archived-cancel-result")
	requireConstraint(t, func() error {
		return directControlResult(fixture, commandID, "applied", "applied", nil, resultDigest)
	})
	if _, err := database.Exec(`UPDATE control_commands SET status_revision=3,outcome='outcome_unknown',
		safe_reason='runner_lost',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, commandID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_outbox SET safe_reason='runner_lost',
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, commandID); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 5, "effect_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_commands SET status='applied',status_revision=4,outcome='applied',
		safe_reason=NULL,result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, resultDigest, commandID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_outbox SET delivery_state='acknowledged',result_sequence=1,
		result_digest=?,result_outcome='applied',safe_reason=NULL,
		acknowledged_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, resultDigest, commandID); err != nil {
		t.Fatalf("archived cancellation reconciliation: %v", err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 6, "command_applied"); err != nil {
		t.Fatal(err)
	}
	if err := insertCommandAudit(t, fixture, commandID, 7, "effect_reconciled"); err != nil {
		t.Fatal(err)
	}
}

func TestM147LifecycleMatrixAtSealAndRenewal(t *testing.T) {
	t.Run("archived rejects broad seals", func(t *testing.T) {
		for _, subject := range []string{"grant", "lease"} {
			t.Run(subject, func(t *testing.T) {
				database := openTestDB(t)
				fixture := seedControlGraph(t, database)
				insertControlRootAudits(t, fixture)
				if subject == "grant" {
					if _, err := database.Exec(`UPDATE control_capability_grants SET
						revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
						WHERE grant_id=? AND revision=1`, fixture.grantID); err != nil {
						t.Fatal(err)
					}
				} else if _, err := database.Exec(`UPDATE control_capability_leases SET
					revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
					WHERE lease_id=? AND revision=1`, fixture.leaseID); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`UPDATE projects SET status='archived' WHERE id=?`, fixture.projectID); err != nil {
					t.Fatal(err)
				}
				if err := database.QueryRow(`SELECT revision FROM issue_control_revisions WHERE issue_id=?`, fixture.issueID).
					Scan(&fixture.issueRevision); err != nil {
					t.Fatal(err)
				}
				if subject == "grant" {
					binding, actions := controlDigest("archived-broad-grant-binding"), controlDigest("archived-broad-grant-actions")
					if err := insertGrantRevision(fixture, fixture.grantID, 2, controlCredentialA, binding, actions, 6); err != nil {
						t.Fatal(err)
					}
					for _, action := range []string{"issue.priority.set", "run.cancel.queued", "run.cancel.running", "input.respond", "run.pause", "run.resume"} {
						if _, err := database.Exec(`INSERT INTO control_capability_grant_actions(grant_id,grant_revision,action)
							VALUES(?,2,?)`, fixture.grantID, action); err != nil {
							t.Fatal(err)
						}
					}
					requireConstraint(t, func() error {
						_, err := database.Exec(`INSERT INTO control_capability_grant_seals(
							grant_id,grant_revision,binding_digest,action_set_digest,action_count) VALUES(?,2,?,?,6)`,
							fixture.grantID, binding, actions)
						return err
					})
				} else {
					binding, actions := controlDigest("archived-broad-lease-binding"), controlDigest("archived-broad-lease-actions")
					if err := insertLeaseRevision(fixture, fixture.leaseID, 2, fixture.runnerAPIKeyID, "runner-01", binding, actions, 4); err != nil {
						t.Fatal(err)
					}
					for _, action := range []string{"run.cancel.running", "input.respond", "run.pause", "run.resume"} {
						if _, err := database.Exec(`INSERT INTO control_capability_lease_actions(lease_id,lease_revision,action)
							VALUES(?,2,?)`, fixture.leaseID, action); err != nil {
							t.Fatal(err)
						}
					}
					requireConstraint(t, func() error {
						_, err := database.Exec(`INSERT INTO control_capability_lease_seals(
							lease_id,lease_revision,binding_digest,action_set_digest,action_count) VALUES(?,2,?,?,4)`,
							fixture.leaseID, binding, actions)
						return err
					})
				}
			})
		}
	})

	t.Run("archived cancel-only seal and renewal", func(t *testing.T) {
		database := openTestDB(t)
		fixture := seedControlGraph(t, database)
		insertControlRootAudits(t, fixture)
		if _, err := database.Exec(`UPDATE control_capability_grants SET
			revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE grant_id=? AND revision=1`, fixture.grantID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE control_capability_leases SET
			revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE lease_id=? AND revision=1`, fixture.leaseID); err != nil {
			t.Fatal(err)
		}
		if err := insertGrantAudit(t, fixture, 2, 1, "grant_revoked", "capability_revoked"); err != nil {
			t.Fatal(err)
		}
		if err := insertLeaseAudit(t, fixture, 2, 1, "lease_revoked", "lease_revoked"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE projects SET status='archived' WHERE id=?`, fixture.projectID); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRow(`SELECT revision FROM issue_control_revisions WHERE issue_id=?`, fixture.issueID).
			Scan(&fixture.issueRevision); err != nil {
			t.Fatal(err)
		}
		grantBinding, grantActions := controlDigest("archived-cancel-grant-binding"), controlDigest("archived-cancel-grant-actions")
		if err := insertGrantRevision(fixture, fixture.grantID, 2, controlCredentialA, grantBinding, grantActions, 2); err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"run.cancel.queued", "run.cancel.running"} {
			if _, err := database.Exec(`INSERT INTO control_capability_grant_actions(grant_id,grant_revision,action)
				VALUES(?,2,?)`, fixture.grantID, action); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := database.Exec(`INSERT INTO control_capability_grant_seals(
			grant_id,grant_revision,binding_digest,action_set_digest,action_count) VALUES(?,2,?,?,2)`,
			fixture.grantID, grantBinding, grantActions); err != nil {
			t.Fatalf("archived cancel grant seal: %v", err)
		}
		if err := insertGrantAudit(t, fixture, 3, 2, "grant_renewed", nil); err != nil {
			t.Fatal(err)
		}
		leaseBinding, leaseActions := controlDigest("archived-cancel-lease-binding"), controlDigest("archived-cancel-lease-actions")
		if err := insertLeaseRevision(fixture, fixture.leaseID, 2, fixture.runnerAPIKeyID, "runner-01", leaseBinding, leaseActions, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO control_capability_lease_actions(lease_id,lease_revision,action)
			VALUES(?,2,'run.cancel.running')`, fixture.leaseID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO control_capability_lease_seals(
			lease_id,lease_revision,binding_digest,action_set_digest,action_count) VALUES(?,2,?,?,1)`,
			fixture.leaseID, leaseBinding, leaseActions); err != nil {
			t.Fatalf("archived cancel lease seal: %v", err)
		}
		if err := insertLeaseAudit(t, fixture, 3, 2, "lease_renewed", nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
		if _, err := database.Exec(`UPDATE control_capability_grants SET
			expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+2 hours'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE grant_id=? AND revision=2`, fixture.grantID); err != nil {
			t.Fatalf("archived cancel grant renewal: %v", err)
		}
		if _, err := database.Exec(`UPDATE control_capability_leases SET
			expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+2 hours'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE lease_id=? AND revision=2`, fixture.leaseID); err != nil {
			t.Fatalf("archived cancel lease renewal: %v", err)
		}
		if err := insertGrantAudit(t, fixture, 4, 2, "grant_renewed", nil); err != nil {
			t.Fatal(err)
		}
		if err := insertLeaseAudit(t, fixture, 4, 2, "lease_renewed", nil); err != nil {
			t.Fatal(err)
		}
		requireConstraint(t, func() error {
			return insertRuntimeCommand(fixture, "65656565-6565-4565-8565-656565656565", "run.pause",
				controlDigest("archived-pause-denied"), 2, 2, 1, controlTimeOffset(t, database, "+5 minutes"))
		})
		commandID := "63636363-6363-4363-8363-636363636363"
		canonical := controlDigest("fresh-archived-running-cancel")
		if err := insertRunningCancelCommandRevision(fixture, commandID, canonical, 2, 2,
			controlTimeOffset(t, database, "+5 minutes")); err != nil {
			t.Fatalf("fresh archived cancel challenge: %v", err)
		}
		acceptControlCommand(t, fixture, commandID)
		queueControlEffect(t, fixture, commandID, 2, canonical)
		if err := claimControlEffect(fixture, commandID); err != nil {
			t.Fatalf("fresh archived cancel claim: %v", err)
		}
		if err := directControlResult(fixture, commandID, "rejected", "rejected", "effect_rejected",
			controlDigest("fresh-archived-cancel-result")); err != nil {
			t.Fatalf("fresh archived cancel result: %v", err)
		}
	})

	t.Run("archived broad live renewal fails", func(t *testing.T) {
		database := openTestDB(t)
		fixture := seedControlGraph(t, database)
		insertControlRootAudits(t, fixture)
		if _, err := database.Exec(`UPDATE projects SET status='archived' WHERE id=?`, fixture.projectID); err != nil {
			t.Fatal(err)
		}
		for _, subject := range []struct {
			table, idColumn, id string
		}{{"control_capability_grants", "grant_id", fixture.grantID}, {"control_capability_leases", "lease_id", fixture.leaseID}} {
			requireConstraint(t, func() error {
				_, err := database.Exec(`UPDATE `+subject.table+` SET
					expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+2 hours'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
					WHERE `+subject.idColumn+`=? AND revision=1`, subject.id)
				return err
			})
		}
	})
}

func TestM147ProjectStatusTransitionsAdvanceIssueAuthority(t *testing.T) {
	database := openTestDB(t)
	fixture := seedControlGraph(t, database)
	insertControlRootAudits(t, fixture)
	second, err := database.Exec(`INSERT INTO issues(project_id,issue_number,title)
		VALUES(?,999,'Second lifecycle issue')`, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	secondIssueID, _ := second.LastInsertId()
	commandID := "64646464-6464-4464-8464-646464646464"
	if err := insertPauseCommand(fixture, commandID, controlDigest("status-aba-command"), 1, 1,
		controlTimeOffset(t, database, "+5 minutes")); err != nil {
		t.Fatal(err)
	}
	readRevision := func(issueID int64) int64 {
		t.Helper()
		var revision int64
		if err := database.QueryRow(`SELECT revision FROM issue_control_revisions WHERE issue_id=?`, issueID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		return revision
	}
	firstBefore, secondBefore := readRevision(fixture.issueID), readRevision(secondIssueID)
	if _, err := database.Exec(`UPDATE projects SET status=status WHERE id=?`, fixture.projectID); err != nil {
		t.Fatal(err)
	}
	if got := readRevision(fixture.issueID); got != firstBefore {
		t.Fatalf("same-status update advanced first issue: got %d want %d", got, firstBefore)
	}
	if got := readRevision(secondIssueID); got != secondBefore {
		t.Fatalf("same-status update advanced second issue: got %d want %d", got, secondBefore)
	}
	for step, status := range []string{"archived", "active"} {
		if _, err := database.Exec(`UPDATE projects SET status=? WHERE id=?`, status, fixture.projectID); err != nil {
			t.Fatal(err)
		}
		if got, want := readRevision(fixture.issueID), firstBefore+int64(step)+1; got != want {
			t.Fatalf("first issue after %s: got %d want %d", status, got, want)
		}
		if got, want := readRevision(secondIssueID), secondBefore+int64(step)+1; got != want {
			t.Fatalf("second issue after %s: got %d want %d", status, got, want)
		}
		requireConstraint(t, func() error { return acceptControlCommandErr(fixture, commandID) })
	}
	if err := insertPauseCommand(fixture, "66666666-6666-4666-8666-666666666666", controlDigest("status-aba-new-command"), 1, 1,
		controlTimeOffset(t, database, "+5 minutes")); err == nil {
		t.Fatal("active→archived→active revived stale pre-transition capabilities")
	}
	if _, err := database.Exec(`UPDATE control_capability_grants SET
		revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE grant_id=? AND revision=1`, fixture.grantID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_capability_leases SET
		revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE lease_id=? AND revision=1`, fixture.leaseID); err != nil {
		t.Fatal(err)
	}
	if err := insertGrantAudit(t, fixture, 2, 1, "grant_revoked", "capability_revoked"); err != nil {
		t.Fatal(err)
	}
	if err := insertLeaseAudit(t, fixture, 2, 1, "lease_revoked", "lease_revoked"); err != nil {
		t.Fatal(err)
	}
	fixture.issueRevision = readRevision(fixture.issueID)
	grantBinding, grantActions := controlDigest("post-lifecycle-grant-binding"), controlDigest("post-lifecycle-grant-actions")
	if err := insertGrantRevision(fixture, fixture.grantID, 2, controlCredentialA, grantBinding, grantActions, 6); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"issue.priority.set", "run.cancel.queued", "run.cancel.running", "input.respond", "run.pause", "run.resume"} {
		if _, err := database.Exec(`INSERT INTO control_capability_grant_actions(grant_id,grant_revision,action)
			VALUES(?,2,?)`, fixture.grantID, action); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO control_capability_grant_seals(
		grant_id,grant_revision,binding_digest,action_set_digest,action_count) VALUES(?,2,?,?,6)`,
		fixture.grantID, grantBinding, grantActions); err != nil {
		t.Fatal(err)
	}
	if err := insertGrantAudit(t, fixture, 3, 2, "grant_renewed", nil); err != nil {
		t.Fatal(err)
	}
	leaseBinding, leaseActions := controlDigest("post-lifecycle-lease-binding"), controlDigest("post-lifecycle-lease-actions")
	if err := insertLeaseRevision(fixture, fixture.leaseID, 2, fixture.runnerAPIKeyID, "runner-01", leaseBinding, leaseActions, 4); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"run.cancel.running", "input.respond", "run.pause", "run.resume"} {
		if _, err := database.Exec(`INSERT INTO control_capability_lease_actions(lease_id,lease_revision,action)
			VALUES(?,2,?)`, fixture.leaseID, action); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO control_capability_lease_seals(
		lease_id,lease_revision,binding_digest,action_set_digest,action_count) VALUES(?,2,?,?,4)`,
		fixture.leaseID, leaseBinding, leaseActions); err != nil {
		t.Fatal(err)
	}
	if err := insertLeaseAudit(t, fixture, 3, 2, "lease_renewed", nil); err != nil {
		t.Fatal(err)
	}
	freshID := "67676767-6767-4767-8767-676767676767"
	freshDigest := controlDigest("post-lifecycle-fresh-command")
	if err := insertRuntimeCommand(fixture, freshID, "run.pause", freshDigest, 2, 2, 1,
		controlTimeOffset(t, database, "+5 minutes")); err != nil {
		t.Fatalf("fresh post-transition command: %v", err)
	}
	acceptControlCommand(t, fixture, freshID)
	queueControlEffect(t, fixture, freshID, 2, freshDigest)
	if err := claimControlEffect(fixture, freshID); err != nil {
		t.Fatalf("fresh post-transition claim: %v", err)
	}
}

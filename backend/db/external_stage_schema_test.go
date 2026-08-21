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
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/contracts"
)

const (
	externalStageOperatorCredential = "81000000-0000-4000-8000-000000000001"
	externalStageValidLocator       = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

type externalStageSchemaFixture struct {
	database         *sql.DB
	deliveryID       int64
	deliveryKey      string
	projectID        int64
	issueID          int64
	attemptID        int64
	reporterID       int64
	registrationID   int64
	apiKeyID         int64
	reporterUserID   int64
	operatorUserID   int64
	startEventID     int64
	authorityEventID int64
	handoffRowID     int64
}

func externalStageBindingDigests(t *testing.T, fixture externalStageSchemaFixture, registrationID int64) ([32]byte, [32]byte, [32]byte) {
	t.Helper()
	var planRevision, startDeliveryEventID, semanticStageEventID int64
	if err := fixture.database.QueryRow(`SELECT attempt.plan_revision,attempt.start_delivery_event_id,
		COALESCE(latest.semantic_stage_event_id,0) FROM delivery_attempts attempt
		JOIN delivery_stage_latest latest ON latest.attempt_id=attempt.id AND latest.stage_key='deployment'
		WHERE attempt.id=?`, fixture.attemptID).Scan(&planRevision, &startDeliveryEventID, &semanticStageEventID); err != nil {
		t.Fatal(err)
	}
	plan := sha256.Sum256([]byte(fmt.Sprintf("paimos.external-stage.plan.v1\x00%d:%d:%d", fixture.attemptID, planRevision, startDeliveryEventID)))
	predecessor := sha256.Sum256([]byte(fmt.Sprintf("paimos.external-stage.predecessor.v1\x00%d:%d:%d", fixture.startEventID, fixture.authorityEventID, semanticStageEventID)))
	context := sha256.Sum256([]byte(fmt.Sprintf("paimos.external-stage.context.v1\x00%s\x00%d\x00deployment\x001\x001\x00%d", fixture.deliveryKey, fixture.attemptID, registrationID)))
	var sqlPlan, sqlPredecessor, sqlContext []byte
	if err := fixture.database.QueryRow(`SELECT
		paimos_domain_sha256('paimos.external-stage.plan.v1',printf('%d:%d:%d',attempt.id,attempt.plan_revision,attempt.start_delivery_event_id)),
		paimos_domain_sha256('paimos.external-stage.predecessor.v1',printf('%d:%d:%d',latest.execution_start_stage_event_id,latest.authority_stage_event_id,COALESCE(latest.semantic_stage_event_id,0))),
		paimos_domain_sha256('paimos.external-stage.context.v1',delivery.delivery_key,CAST(attempt.id AS TEXT),'deployment','1','1',CAST(? AS TEXT))
		FROM delivery_attempts attempt JOIN deliveries delivery ON delivery.id=attempt.delivery_id
		JOIN delivery_stage_latest latest ON latest.attempt_id=attempt.id AND latest.stage_key='deployment'
		WHERE attempt.id=?`, registrationID, fixture.attemptID).Scan(&sqlPlan, &sqlPredecessor, &sqlContext); err != nil {
		t.Fatal(err)
	}
	if string(sqlPlan) != string(plan[:]) || string(sqlPredecessor) != string(predecessor[:]) || string(sqlContext) != string(context[:]) {
		t.Fatalf("Go/SQLite external-stage digest derivation drift: plan=%t predecessor=%t context=%t",
			string(sqlPlan) == string(plan[:]), string(sqlPredecessor) == string(predecessor[:]), string(sqlContext) == string(context[:]))
	}
	return plan, predecessor, context
}

func trySeedExternalStageSchemaFixture(t *testing.T, locator string) (externalStageSchemaFixture, error) {
	t.Helper()
	database := openTestDB(t)
	deliveryID, attemptID, _, startEventID := seedDeliverySchemaGraph(t, database)
	fixture := externalStageSchemaFixture{database: database, deliveryID: deliveryID, attemptID: attemptID, startEventID: startEventID}
	if err := database.QueryRow(`SELECT delivery.delivery_key,delivery.issue_id,issue.project_id
		FROM deliveries delivery JOIN issues issue ON issue.id=delivery.issue_id WHERE delivery.id=?`, deliveryID).
		Scan(&fixture.deliveryKey, &fixture.issueID, &fixture.projectID); err != nil {
		t.Fatal(err)
	}

	operator, err := database.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('external-stage-operator','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	fixture.operatorUserID, _ = operator.LastInsertId()
	if _, err := database.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')`,
		fixture.operatorUserID, fixture.projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('external-stage-operator-session',?,datetime('now','+1 hour'),datetime('now'),?)`,
		fixture.operatorUserID, externalStageOperatorCredential); err != nil {
		t.Fatal(err)
	}

	reporterUser, err := database.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('external-stage-reporter','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	fixture.reporterUserID, _ = reporterUser.LastInsertId()
	apiKey, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'external-stage-reporter',?,'paimos_ext_stage','*')`, fixture.reporterUserID, strings.Repeat("8", 64))
	if err != nil {
		t.Fatal(err)
	}
	fixture.apiKeyID, _ = apiKey.LastInsertId()
	reporter, err := database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
		VALUES(?,'external','pharos-production',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, fixture.deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.reporterID, _ = reporter.LastInsertId()
	var deliveryRevision int64
	if err := database.QueryRow(`SELECT COALESCE(MAX(delivery_revision),0)+1 FROM delivery_events WHERE delivery_id=?`,
		fixture.deliveryID).Scan(&deliveryRevision); err != nil {
		t.Fatal(err)
	}
	envelope, err := database.Exec(`INSERT INTO delivery_events(
		delivery_id,delivery_revision,idempotency_key,payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,?,'external-stage-deployment',zeroblob(32),'stage_execution_started',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		fixture.deliveryID, deliveryRevision, fixture.reporterID)
	if err != nil {
		t.Fatal(err)
	}
	envelopeID, _ := envelope.LastInsertId()
	authority, err := database.Exec(`INSERT INTO delivery_stage_events(
		delivery_id,attempt_id,stage_key,execution_number,event_sequence,authority_epoch,delivery_event_id,
		event_type,reporter_id,semantic_state,authority_source_sequence_cutoff,server_received_at)
		VALUES(?,?,'deployment',1,1,1,?,'execution_started',?,'active',0,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		fixture.deliveryID, fixture.attemptID, envelopeID, fixture.reporterID)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, _ := authority.LastInsertId()
	fixture.startEventID = authorityID
	fixture.authorityEventID = authorityID
	if _, err := database.Exec(`INSERT INTO delivery_stage_latest(
		delivery_id,attempt_id,stage_key,execution_number,authority_epoch,current_reporter_id,
		execution_start_stage_event_id,authority_stage_event_id,updated_at)
		VALUES(?,?,'deployment',1,1,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, fixture.deliveryID,
		fixture.attemptID, fixture.reporterID, fixture.startEventID, authorityID); err != nil {
		t.Fatal(err)
	}
	registration, err := database.Exec(`INSERT INTO external_stage_reporter_registrations(
		delivery_id,project_id,user_id,api_key_id,reporter_id,reporter_class,reporter_role,
		workflow_symbol,environment_symbol,allow_deployment,allow_verification,allow_authorization,allow_credential_handoff)
		VALUES(?,?,?,?,?,'pharos','owner','deploy-production','production',1,1,0,0)`, fixture.deliveryID,
		fixture.projectID, fixture.reporterUserID, fixture.apiKeyID, fixture.reporterID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.registrationID, _ = registration.LastInsertId()
	setupDigest := controlDigest("registration-created")
	if _, err := database.Exec(`INSERT INTO external_stage_setup_events(
		event_kind,delivery_id,project_id,registration_id,actor_user_id,actor_principal_kind,actor_session_id,
		request_digest,idempotency_digest)
		VALUES('registration_created',?,?,?,?,'session',?,?,?)`, fixture.deliveryID, fixture.projectID,
		fixture.registrationID, fixture.operatorUserID, externalStageOperatorCredential, setupDigest, setupDigest); err != nil {
		t.Fatal(err)
	}

	digest := contracts.ExternalStageV1FixtureDigest()
	planDigest, predecessorDigest, contextDigest := externalStageBindingDigests(t, fixture, fixture.registrationID)
	handoff, err := database.Exec(`INSERT INTO external_stage_handoffs(
		handoff_id,delivery_id,delivery_key,root_issue_id,project_id,attempt_id,attempt_number,plan_revision,
		plan_digest,stage_key,execution_number,execution_start_stage_event_id,predecessor_digest,authority_epoch,
		authority_stage_event_id,reporter_registration_id,reporter_id,api_key_id,reporter_class,reporter_role,
		workflow_symbol,environment_symbol,allow_deployment,allow_verification,allow_authorization,
		allow_credential_handoff,contract_major,fixture_digest,expires_at,context_digest)
		VALUES(?,?,?,?,?,?,1,1,?,'deployment',1,?,?,1,?,?,?,?,
		'pharos','owner','deploy-production','production',1,1,0,0,1,?,strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour'),?)`,
		locator, fixture.deliveryID, fixture.deliveryKey, fixture.issueID, fixture.projectID, fixture.attemptID,
		planDigest[:], fixture.startEventID, predecessorDigest[:], fixture.authorityEventID, fixture.registrationID,
		fixture.reporterID, fixture.apiKeyID, digest[:], contextDigest[:])
	if err != nil {
		return fixture, err
	}
	fixture.handoffRowID, _ = handoff.LastInsertId()
	return fixture, nil
}

func seedExternalStageSchemaFixture(t *testing.T, locator string) externalStageSchemaFixture {
	t.Helper()
	fixture, err := trySeedExternalStageSchemaFixture(t, locator)
	if err != nil {
		t.Fatalf("insert handoff %q: %v", locator, err)
	}
	return fixture
}

func insertExternalStageOperation(t *testing.T, fixture externalStageSchemaFixture, kind string, epoch int64,
	sequence any, actorKind string, actorUserID int64, sessionID any, apiKeyID any) (int64, string) {
	t.Helper()
	digest := controlDigest(fmt.Sprintf("%s-%d-%v-%d-%d", kind, epoch, sequence, actorUserID, fixture.handoffRowID))
	result, err := fixture.database.Exec(`INSERT INTO external_stage_operation_events(
		handoff_row_id,operation_kind,request_digest,idempotency_digest,actor_user_id,actor_principal_kind,
		actor_session_id,actor_api_key_id,credential_epoch,sequence)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, fixture.handoffRowID, kind, digest, digest, actorUserID, actorKind,
		sessionID, apiKeyID, epoch, sequence)
	if err != nil {
		t.Fatalf("insert %s operation: %v", kind, err)
	}
	id, _ := result.LastInsertId()
	var received string
	if err := fixture.database.QueryRow(`SELECT server_received_at FROM external_stage_operation_events WHERE id=?`, id).Scan(&received); err != nil {
		t.Fatal(err)
	}
	return id, received
}

func auditExternalStageOperation(t *testing.T, fixture externalStageSchemaFixture, operationID int64, kind string,
	epoch int64, sequence any, apiKeyID any) {
	t.Helper()
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_audit_events(
		event_kind,handoff_row_id,operation_event_id,api_key_id,credential_epoch,sequence,outcome,server_received_at)
		SELECT ?,handoff_row_id,id,?,?,?,'committed',server_received_at
		FROM external_stage_operation_events WHERE id=?`, kind, apiKeyID, epoch, sequence, operationID); err != nil {
		t.Fatalf("audit %s operation: %v", kind, err)
	}
}

func mintExternalStageCredential(t *testing.T, fixture externalStageSchemaFixture) {
	t.Helper()
	operationID, _ := insertExternalStageOperation(t, fixture, "secret_minted", 1, nil, "session",
		fixture.operatorUserID, externalStageOperatorCredential, nil)
	auditExternalStageOperation(t, fixture, operationID, "secret_minted", 1, nil, nil)
	if _, err := fixture.database.Exec(`UPDATE external_stage_handoffs SET credential_epoch=1,secret_digest=? WHERE id=?`,
		controlDigest("domain-separated-handoff-secret"), fixture.handoffRowID); err != nil {
		t.Fatalf("apply mint: %v", err)
	}
}

func acceptExternalStageHandoff(t *testing.T, fixture externalStageSchemaFixture) {
	t.Helper()
	operationID, received := insertExternalStageOperation(t, fixture, "accepted", 1, int64(1), "api_key",
		fixture.reporterUserID, nil, fixture.apiKeyID)
	auditExternalStageOperation(t, fixture, operationID, "accepted", 1, int64(1), fixture.apiKeyID)
	if _, err := fixture.database.Exec(`UPDATE external_stage_handoffs
		SET lifecycle_state='accepted',last_sequence=1,accepted_at=? WHERE id=?`, received, fixture.handoffRowID); err != nil {
		t.Fatalf("apply accept: %v", err)
	}
}

func commitExternalStageOwnerState(t *testing.T, fixture externalStageSchemaFixture, sequence int64, state string,
	blocker string) int64 {
	t.Helper()
	digest := controlDigest(fmt.Sprintf("semantic-%d-%s", sequence, state))
	declared := 0
	if blocker != "" {
		declared = 1
	}
	result, err := fixture.database.Exec(`INSERT INTO external_stage_report_events(
		handoff_row_id,actor_api_key_id,sequence,credential_epoch,request_digest,idempotency_digest,
		lifecycle_state,observed_at,heartbeat,declared_blockers)
		VALUES(?,?,?,1,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),0,?)`, fixture.handoffRowID,
		fixture.apiKeyID, sequence, digest, digest, state, declared)
	if err != nil {
		t.Fatal(err)
	}
	reportID, _ := result.LastInsertId()
	if blocker != "" {
		if _, err := fixture.database.Exec(`INSERT INTO external_stage_report_blockers(report_event_id,ordinal,blocker_code)
			VALUES(?,0,?)`, reportID, blocker); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_audit_events(
		event_kind,handoff_row_id,report_event_id,api_key_id,credential_epoch,sequence,outcome,server_received_at)
		SELECT 'reported',handoff_row_id,id,actor_api_key_id,credential_epoch,sequence,'committed',server_received_at
		FROM external_stage_report_events WHERE id=?`, reportID); err != nil {
		t.Fatal(err)
	}
	owner, err := fixture.database.Exec(`INSERT INTO external_stage_owner_events(
		delivery_id,attempt_id,stage_key,execution_number,authority_epoch,handoff_row_id,report_event_id,
		sequence,stream_sequence,lifecycle_state,server_received_at)
		SELECT ?,?,'deployment',1,1,handoff_row_id,id,sequence,sequence-1,lifecycle_state,server_received_at
		FROM external_stage_report_events WHERE id=?`, fixture.deliveryID, fixture.attemptID, reportID)
	if err != nil {
		t.Fatal(err)
	}
	ownerID, _ := owner.LastInsertId()
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_owner_latest(
		delivery_id,attempt_id,stage_key,execution_number,authority_epoch,owner_event_id,handoff_row_id,
		report_event_id,sequence,stream_sequence,lifecycle_state,updated_at)
		SELECT ?,?,'deployment',1,1,?,handoff_row_id,id,sequence,sequence-1,lifecycle_state,server_received_at
		FROM external_stage_report_events WHERE id=?`, fixture.deliveryID, fixture.attemptID, ownerID, reportID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`UPDATE external_stage_handoffs
		SET lifecycle_state=?,last_sequence=? WHERE id=?`, state, sequence, fixture.handoffRowID); err != nil {
		t.Fatal(err)
	}
	return reportID
}

func prepareExternalStageDeploymentEvidenceReport(t *testing.T) (externalStageSchemaFixture, int64) {
	t.Helper()
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	mintExternalStageCredential(t, fixture)
	acceptExternalStageHandoff(t, fixture)
	commitExternalStageOwnerState(t, fixture, 2, "active", "")
	seedJanusPrerequisite(t, fixture, "required", true)
	digest := controlDigest("deployment-evidence-report")
	result, err := fixture.database.Exec(`INSERT INTO external_stage_report_events(
		handoff_row_id,actor_api_key_id,sequence,credential_epoch,request_digest,idempotency_digest,
		lifecycle_state,observed_at,heartbeat,declared_blockers,evidence_kind)
		VALUES(?,?,3,1,?,?,'succeeded',strftime('%Y-%m-%dT%H:%M:%fZ','now'),0,0,'deployment')`,
		fixture.handoffRowID, fixture.apiKeyID, digest, digest)
	if err != nil {
		var ownerState string
		var ownerSequence, sealedCount, prerequisiteCount, satisfiedCount int
		_ = fixture.database.QueryRow(`SELECT lifecycle_state,last_sequence FROM external_stage_handoffs WHERE id=?`, fixture.handoffRowID).Scan(&ownerState, &ownerSequence)
		_ = fixture.database.QueryRow(`SELECT COUNT(*) FROM external_stage_prerequisite_sets WHERE attempt_id=? AND stage_key='deployment' AND execution_number=1 AND authority_epoch=1 AND sealed_at IS NOT NULL`, fixture.attemptID).Scan(&sealedCount)
		_ = fixture.database.QueryRow(`SELECT COUNT(*) FROM external_stage_prerequisites WHERE attempt_id=? AND stage_key='deployment' AND execution_number=1 AND authority_epoch=1`, fixture.attemptID).Scan(&prerequisiteCount)
		_ = fixture.database.QueryRow(`SELECT COUNT(*) FROM external_stage_dependency_latest latest JOIN external_stage_handoffs handoff ON handoff.id=latest.handoff_row_id WHERE latest.attempt_id=? AND latest.stage_key='deployment' AND latest.execution_number=1 AND latest.authority_epoch=1 AND latest.lifecycle_state='succeeded' AND handoff.lifecycle_state='succeeded' AND handoff.terminal_at IS NOT NULL AND julianday(handoff.expires_at)>julianday('now')`, fixture.attemptID).Scan(&satisfiedCount)
		t.Fatalf("insert succeeded owner report: %v (owner=%s/%d sealed=%d prerequisites=%d satisfied=%d)", err, ownerState, ownerSequence, sealedCount, prerequisiteCount, satisfiedCount)
	}
	reportID, _ := result.LastInsertId()
	return fixture, reportID
}

func seedJanusPrerequisite(t *testing.T, fixture externalStageSchemaFixture, requirement string, satisfied bool) {
	t.Helper()
	userResult, err := fixture.database.Exec(`INSERT INTO users(username,password,role,status) VALUES('janus-reporter','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := userResult.LastInsertId()
	keyResult, err := fixture.database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'janus',?,'paimos_janus','*')`, userID, strings.Repeat("9", 64))
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := keyResult.LastInsertId()
	reporterResult, err := fixture.database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at) VALUES(?,'external','janus-auth',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, fixture.deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	reporterID, _ := reporterResult.LastInsertId()
	registrationResult, err := fixture.database.Exec(`INSERT INTO external_stage_reporter_registrations(delivery_id,project_id,user_id,api_key_id,reporter_id,reporter_class,reporter_role,dependency_key,allow_deployment,allow_verification,allow_authorization,allow_credential_handoff) VALUES(?,?,?,?,?,'janus','dependency','janus-auth',0,0,1,1)`, fixture.deliveryID, fixture.projectID, userID, keyID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	registrationID, _ := registrationResult.LastInsertId()
	registrationDigest := controlDigest("janus-registration-created")
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_setup_events(event_kind,delivery_id,project_id,registration_id,actor_user_id,actor_principal_kind,actor_session_id,request_digest,idempotency_digest) VALUES('registration_created',?,?,?,?,'session',?,?,?)`, fixture.deliveryID, fixture.projectID, registrationID, fixture.operatorUserID, externalStageOperatorCredential, registrationDigest, registrationDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_prerequisite_sets(
		delivery_id,attempt_id,stage_key,execution_number,execution_start_stage_event_id,
		authority_epoch,authority_stage_event_id,declared_count)
		VALUES(?,?,'deployment',1,?,1,?,1)`, fixture.deliveryID, fixture.attemptID,
		fixture.startEventID, fixture.authorityEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_prerequisites(delivery_id,attempt_id,stage_key,execution_number,authority_epoch,dependency_key,registration_id,requirement,ordinal) VALUES(?,?,'deployment',1,1,'janus-auth',?,?,0)`, fixture.deliveryID, fixture.attemptID, registrationID, requirement); err != nil {
		t.Fatal(err)
	}
	sealDigest := controlDigest("seal-janus-deployment-prerequisites")
	var sealedAt string
	result, err := fixture.database.Exec(`INSERT INTO external_stage_setup_events(
		event_kind,delivery_id,project_id,attempt_id,stage_key,execution_number,authority_epoch,
		actor_user_id,actor_principal_kind,actor_session_id,request_digest,idempotency_digest)
		VALUES('prerequisites_sealed',?,?,?,'deployment',1,1,?,'session',?,?,?)`, fixture.deliveryID,
		fixture.projectID, fixture.attemptID, fixture.operatorUserID, externalStageOperatorCredential,
		sealDigest, sealDigest)
	if err != nil {
		t.Fatal(err)
	}
	setupID, _ := result.LastInsertId()
	if err := fixture.database.QueryRow(`SELECT server_received_at FROM external_stage_setup_events WHERE id=?`, setupID).
		Scan(&sealedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`UPDATE external_stage_prerequisite_sets SET sealed_at=?
		WHERE attempt_id=? AND stage_key='deployment' AND execution_number=1 AND authority_epoch=1`,
		sealedAt, fixture.attemptID); err != nil {
		t.Fatal(err)
	}
	if !satisfied {
		return
	}
	digestFixture := contracts.ExternalStageV1FixtureDigest()
	planDigest, predecessorDigest, contextDigest := externalStageBindingDigests(t, fixture, registrationID)
	handoffResult, err := fixture.database.Exec(`INSERT INTO external_stage_handoffs(handoff_id,delivery_id,delivery_key,root_issue_id,project_id,attempt_id,attempt_number,plan_revision,plan_digest,stage_key,execution_number,execution_start_stage_event_id,predecessor_digest,authority_epoch,authority_stage_event_id,reporter_registration_id,reporter_id,api_key_id,reporter_class,reporter_role,dependency_key,allow_deployment,allow_verification,allow_authorization,allow_credential_handoff,contract_major,fixture_digest,expires_at,context_digest) VALUES('01ARZ3NDEKTSV4RRFFQ69G5FB0',?,?,?,?,?,1,1,?,'deployment',1,?,?,1,?,?,?,?,'janus','dependency','janus-auth',0,0,1,1,1,?,strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour'),?)`, fixture.deliveryID, fixture.deliveryKey, fixture.issueID, fixture.projectID, fixture.attemptID, planDigest[:], fixture.startEventID, predecessorDigest[:], fixture.authorityEventID, registrationID, reporterID, keyID, digestFixture[:], contextDigest[:])
	if err != nil {
		t.Fatal(err)
	}
	depHandoffID, _ := handoffResult.LastInsertId()
	dependency := fixture
	dependency.registrationID = registrationID
	dependency.reporterID = reporterID
	dependency.apiKeyID = keyID
	dependency.reporterUserID = userID
	dependency.handoffRowID = depHandoffID
	mintExternalStageCredential(t, dependency)
	acceptExternalStageHandoff(t, dependency)
	activeDigest := controlDigest("janus-active")
	activeResult, err := fixture.database.Exec(`INSERT INTO external_stage_report_events(handoff_row_id,actor_api_key_id,sequence,credential_epoch,request_digest,idempotency_digest,lifecycle_state,observed_at,heartbeat,declared_blockers,evidence_kind) VALUES(?,?,2,1,?,?,'active',strftime('%Y-%m-%dT%H:%M:%fZ','now'),0,0,NULL)`, depHandoffID, keyID, activeDigest, activeDigest)
	if err != nil {
		t.Fatalf("insert active janus report: %v", err)
	}
	activeReportID, _ := activeResult.LastInsertId()
	var activeReceived string
	if err := fixture.database.QueryRow(`SELECT server_received_at FROM external_stage_report_events WHERE id=?`, activeReportID).Scan(&activeReceived); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_audit_events(event_kind,handoff_row_id,report_event_id,api_key_id,credential_epoch,sequence,outcome,server_received_at) VALUES('reported',?,?,?,?,2,'committed',?)`, depHandoffID, activeReportID, keyID, 1, activeReceived); err != nil {
		t.Fatal(err)
	}
	activeEventResult, err := fixture.database.Exec(`INSERT INTO external_stage_dependency_events(delivery_id,attempt_id,stage_key,execution_number,authority_epoch,dependency_key,registration_id,handoff_row_id,report_event_id,credential_epoch,sequence,stream_sequence,lifecycle_state,server_received_at) VALUES(?,?,'deployment',1,1,'janus-auth',?,?,?,1,2,1,'active',?)`, fixture.deliveryID, fixture.attemptID, registrationID, depHandoffID, activeReportID, activeReceived)
	if err != nil {
		t.Fatal(err)
	}
	activeEventID, _ := activeEventResult.LastInsertId()
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_dependency_latest(delivery_id,attempt_id,stage_key,execution_number,authority_epoch,dependency_key,registration_id,credential_epoch,dependency_event_id,handoff_row_id,report_event_id,sequence,stream_sequence,lifecycle_state,updated_at) VALUES(?,?,'deployment',1,1,'janus-auth',?,1,?,?,?,2,1,'active',?)`, fixture.deliveryID, fixture.attemptID, registrationID, activeEventID, depHandoffID, activeReportID, activeReceived); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`UPDATE external_stage_handoffs SET lifecycle_state='active',last_sequence=2 WHERE id=?`, depHandoffID); err != nil {
		t.Fatal(err)
	}
	reportDigest := controlDigest("janus-satisfied")
	reportResult, err := fixture.database.Exec(`INSERT INTO external_stage_report_events(handoff_row_id,actor_api_key_id,sequence,credential_epoch,request_digest,idempotency_digest,lifecycle_state,observed_at,heartbeat,declared_blockers,evidence_kind) VALUES(?,?,3,1,?,?,'succeeded',strftime('%Y-%m-%dT%H:%M:%fZ','now'),0,0,'authorization')`, depHandoffID, keyID, reportDigest, reportDigest)
	if err != nil {
		t.Fatalf("insert succeeded janus report: %v", err)
	}
	reportID, _ := reportResult.LastInsertId()
	var received string
	if err := fixture.database.QueryRow(`SELECT server_received_at FROM external_stage_report_events WHERE id=?`, reportID).Scan(&received); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_janus_evidence(report_event_id,evidence_kind,result,authorized,observed_at,server_received_at) SELECT id,'authorization','satisfied',1,observed_at,server_received_at FROM external_stage_report_events WHERE id=?`, reportID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_audit_events(event_kind,handoff_row_id,report_event_id,api_key_id,credential_epoch,sequence,outcome,server_received_at) VALUES('reported',?,?,?,?,3,'committed',?)`, depHandoffID, reportID, keyID, 1, received); err != nil {
		t.Fatal(err)
	}
	eventResult, err := fixture.database.Exec(`INSERT INTO external_stage_dependency_events(delivery_id,attempt_id,stage_key,execution_number,authority_epoch,dependency_key,registration_id,handoff_row_id,report_event_id,credential_epoch,sequence,stream_sequence,lifecycle_state,server_received_at) VALUES(?,?,'deployment',1,1,'janus-auth',?,?,?,1,3,2,'succeeded',?)`, fixture.deliveryID, fixture.attemptID, registrationID, depHandoffID, reportID, received)
	if err != nil {
		t.Fatal(err)
	}
	eventID, _ := eventResult.LastInsertId()
	if _, err := fixture.database.Exec(`UPDATE external_stage_dependency_latest SET dependency_event_id=?,handoff_row_id=?,report_event_id=?,sequence=3,stream_sequence=2,lifecycle_state='succeeded',updated_at=? WHERE attempt_id=? AND stage_key='deployment' AND execution_number=1 AND authority_epoch=1 AND dependency_key='janus-auth'`, eventID, depHandoffID, reportID, received, fixture.attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`UPDATE external_stage_handoffs SET lifecycle_state='succeeded',last_sequence=3,terminal_at=? WHERE id=?`, received, depHandoffID); err != nil {
		t.Fatal(err)
	}
}

func insertExternalStageDeploymentEvidence(database *sql.DB, reportID int64, commit string) error {
	_, err := database.Exec(`INSERT INTO external_stage_pharos_evidence(
		report_event_id,evidence_kind,workflow_symbol,environment_symbol,artifact_version,artifact_digest,
		commit_digest,result,observed_at,server_received_at)
		SELECT id,'deployment','deploy-production','production','v1.2.3',zeroblob(32),?,'succeeded',
		 observed_at,server_received_at FROM external_stage_report_events WHERE id=?`, commit, reportID)
	return err
}

func TestM148SchemaPinsFixtureDigestAndOpaqueULID(t *testing.T) {
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	var got []byte
	if err := fixture.database.QueryRow(`SELECT fixture_digest FROM external_stage_handoffs WHERE id=?`, fixture.handoffRowID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want := contracts.ExternalStageV1FixtureDigest()
	if hex.EncodeToString(got) != contracts.ExternalStageV1FixtureDigestHex || string(got) != string(want[:]) {
		t.Fatalf("fixture digest=%x want=%s", got, contracts.ExternalStageV1FixtureDigestHex)
	}
}

func TestM148HandoffInsertRejectsForgedBindingDigests(t *testing.T) {
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	operationID, received := insertExternalStageOperation(t, fixture, "revoked", 0, nil, "session",
		fixture.operatorUserID, externalStageOperatorCredential, nil)
	auditExternalStageOperation(t, fixture, operationID, "revoked", 0, nil, nil)
	if _, err := fixture.database.Exec(`UPDATE external_stage_handoffs SET revoked_at=? WHERE id=?`, received, fixture.handoffRowID); err != nil {
		t.Fatal(err)
	}
	plan, predecessor, context := externalStageBindingDigests(t, fixture, fixture.registrationID)
	for _, mutation := range []struct {
		name                       string
		plan, predecessor, context []byte
	}{
		{name: "plan", plan: make([]byte, 32), predecessor: predecessor[:], context: context[:]},
		{name: "predecessor", plan: plan[:], predecessor: make([]byte, 32), context: context[:]},
		{name: "context", plan: plan[:], predecessor: predecessor[:], context: make([]byte, 32)},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			_, err := fixture.database.Exec(`INSERT INTO external_stage_handoffs(
				handoff_id,delivery_id,delivery_key,root_issue_id,project_id,attempt_id,attempt_number,plan_revision,
				plan_digest,stage_key,execution_number,execution_start_stage_event_id,predecessor_digest,authority_epoch,
				authority_stage_event_id,reporter_registration_id,reporter_id,api_key_id,reporter_class,reporter_role,
				workflow_symbol,environment_symbol,allow_deployment,allow_verification,allow_authorization,
				allow_credential_handoff,contract_major,fixture_digest,expires_at,context_digest)
			SELECT '01ARZ3NDEKTSV4RRFFQ69G5FB1',delivery_id,delivery_key,root_issue_id,project_id,attempt_id,
				attempt_number,plan_revision,?,stage_key,execution_number,execution_start_stage_event_id,?,authority_epoch,
				authority_stage_event_id,reporter_registration_id,reporter_id,api_key_id,reporter_class,reporter_role,
				workflow_symbol,environment_symbol,allow_deployment,allow_verification,allow_authorization,
				allow_credential_handoff,contract_major,fixture_digest,expires_at,?
			FROM external_stage_handoffs WHERE id=?`, mutation.plan, mutation.predecessor, mutation.context, fixture.handoffRowID)
			if err == nil {
				t.Fatal("forged handoff digest unexpectedly persisted")
			}
		})
	}
}

func TestM148DomainDigestIsFramedAndRejectsInvalidArguments(t *testing.T) {
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	var got []byte
	if err := fixture.database.QueryRow(`SELECT paimos_domain_sha256('domain','value')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("domain\x00value"))
	if string(got) != string(want[:]) {
		t.Fatalf("domain digest=%x want=%x", got, want)
	}
	var unframed []byte
	if err := fixture.database.QueryRow(`SELECT paimos_domain_sha256('domainvalue','')`).Scan(&unframed); err != nil {
		t.Fatal(err)
	}
	if string(got) == string(unframed) {
		t.Fatal("domain framing collision")
	}
	for _, query := range []string{
		`SELECT paimos_domain_sha256('domain')`,
		`SELECT paimos_domain_sha256('domain',NULL)`,
		`SELECT paimos_domain_sha256('domain',1)`,
	} {
		if err := fixture.database.QueryRow(query).Scan(&got); err == nil {
			t.Fatalf("invalid digest query unexpectedly succeeded: %s", query)
		}
	}
}

func TestM148ExternalLookupUsesBoundedCompositeIndex(t *testing.T) {
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	rows, err := fixture.database.Query(`EXPLAIN QUERY PLAN SELECT id FROM external_stage_handoffs
		WHERE api_key_id=? AND handoff_id=?`, fixture.apiKeyID, externalStageValidLocator)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(details, "\n")
	if !strings.Contains(joined, "USING INDEX") || strings.Contains(joined, "SCAN external_stage_handoffs") {
		t.Fatalf("unbounded external-stage lookup plan:\n%s", joined)
	}
}

func assertM148InvalidLocator(t *testing.T, locator string) {
	t.Helper()
	if _, err := trySeedExternalStageSchemaFixture(t, locator); err == nil {
		t.Fatalf("invalid locator %q unexpectedly succeeded", locator)
	}
}

func TestM148LocatorRejectsLowercase(t *testing.T) {
	assertM148InvalidLocator(t, "01arz3ndektsv4rrffq69g5fav")
}

func TestM148LocatorRejectsAmbiguousCharacter(t *testing.T) {
	assertM148InvalidLocator(t, "01ARZ3NDEKTSV4RRFFQ69G5FAI")
}

func TestM148LocatorRejectsWrongLength(t *testing.T) {
	assertM148InvalidLocator(t, "01ARZ3NDEKTSV4RRFFQ69G5FA")
}

func TestM148LocatorRejectsLongValue(t *testing.T) {
	assertM148InvalidLocator(t, "01ARZ3NDEKTSV4RRFFQ69G5FAV0")
}

func TestM148DirectHandoffMutationsRequireCausalFacts(t *testing.T) {
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	for name, statement := range map[string]string{
		"digest without epoch":     `UPDATE external_stage_handoffs SET secret_digest=zeroblob(32) WHERE id=?`,
		"epoch without operation":  `UPDATE external_stage_handoffs SET credential_epoch=1,secret_digest=zeroblob(32) WHERE id=?`,
		"accept without operation": `UPDATE external_stage_handoffs SET lifecycle_state='accepted',last_sequence=1,accepted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		"arbitrary revoke":         `UPDATE external_stage_handoffs SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		"delete":                   `DELETE FROM external_stage_handoffs WHERE id=?`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.database.Exec(statement, fixture.handoffRowID); err == nil {
				t.Fatal("direct mutation unexpectedly succeeded")
			}
		})
	}
	mintExternalStageCredential(t, fixture)
	acceptExternalStageHandoff(t, fixture)
	if _, err := fixture.database.Exec(`UPDATE external_stage_handoffs
		SET lifecycle_state='active',last_sequence=2 WHERE id=?`, fixture.handoffRowID); err == nil {
		t.Fatal("lifecycle advance without report/audit/latest unexpectedly succeeded")
	}
}

func TestM148ReportEvidenceAuditAndLatestDirectWriteGuards(t *testing.T) {
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	mintExternalStageCredential(t, fixture)
	acceptExternalStageHandoff(t, fixture)

	insertReport := func(sequence int64, state string, blockers int, evidence any) (int64, error) {
		digest := controlDigest(fmt.Sprintf("report-%d", sequence))
		result, err := fixture.database.Exec(`INSERT INTO external_stage_report_events(
			handoff_row_id,actor_api_key_id,sequence,credential_epoch,request_digest,idempotency_digest,
			lifecycle_state,observed_at,heartbeat,declared_blockers,evidence_kind)
			VALUES(?,?,?,1,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),0,?,?)`, fixture.handoffRowID,
			fixture.apiKeyID, sequence, digest, digest, state, blockers, evidence)
		if err != nil {
			return 0, err
		}
		id, _ := result.LastInsertId()
		return id, nil
	}
	if _, err := insertReport(3, "active", 0, nil); err == nil {
		t.Fatal("non-exact report sequence unexpectedly succeeded")
	}
	if _, err := insertReport(2, "active", 0, "authorization"); err == nil {
		t.Fatal("cross-class evidence kind unexpectedly succeeded")
	}
	reportID, err := insertReport(2, "blocked", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_audit_events(
		event_kind,handoff_row_id,report_event_id,api_key_id,credential_epoch,sequence,outcome,server_received_at)
		SELECT 'reported',handoff_row_id,id,?,credential_epoch,sequence,'committed',server_received_at
		FROM external_stage_report_events WHERE id=?`, fixture.apiKeyID, reportID); err == nil {
		t.Fatal("audit accepted an incomplete blocker payload")
	}
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_report_blockers(report_event_id,ordinal,blocker_code)
		VALUES(?,0,'external_waiting')`, reportID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_janus_evidence(
		report_event_id,evidence_kind,result,authorized,observed_at,server_received_at)
		SELECT id,'authorization','blocked',0,observed_at,server_received_at FROM external_stage_report_events WHERE id=?`, reportID); err == nil {
		t.Fatal("Janus evidence attached to a Pharos report")
	}
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_owner_latest(
		delivery_id,attempt_id,stage_key,execution_number,authority_epoch,owner_event_id,handoff_row_id,
		report_event_id,sequence,stream_sequence,lifecycle_state,updated_at)
		VALUES(?,?,'deployment',1,1,999,?,?,2,1,'blocked',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		fixture.deliveryID, fixture.attemptID, fixture.handoffRowID, reportID); err == nil {
		t.Fatal("owner latest accepted a nonexistent causal event")
	}
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_dependency_latest(
		delivery_id,attempt_id,stage_key,execution_number,authority_epoch,dependency_key,registration_id,
		credential_epoch,dependency_event_id,handoff_row_id,report_event_id,sequence,stream_sequence,lifecycle_state,updated_at)
		VALUES(?,?,'deployment',1,1,'janus-auth',?,1,999,?,?,2,1,'blocked',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		fixture.deliveryID, fixture.attemptID, fixture.registrationID, fixture.handoffRowID, reportID); err == nil {
		t.Fatal("dependency latest accepted an owner handoff")
	}
}

func TestM148BlockedHeartbeatBurstUsesCoalescedLivenessProjection(t *testing.T) {
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	mintExternalStageCredential(t, fixture)
	acceptExternalStageHandoff(t, fixture)
	commitExternalStageOwnerState(t, fixture, 2, "blocked", "external_waiting")

	var reportsBefore, auditBefore, ownerBefore, changesBefore int
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM external_stage_report_events`: &reportsBefore,
		`SELECT COUNT(*) FROM external_stage_audit_events`:  &auditBefore,
		`SELECT COUNT(*) FROM external_stage_owner_events`:  &ownerBefore,
		`SELECT COUNT(*) FROM delivery_change_log`:          &changesBefore,
	} {
		if err := fixture.database.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	leakDigest := controlDigest("heartbeat-secret-sentinel")
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_heartbeat_windows(
		handoff_row_id,actor_api_key_id,credential_epoch,window_number,first_sequence,last_sequence,
		heartbeat_count,lifecycle_state,replay_json,last_observed_at)
		VALUES(?,?,1,1,3,3,1,'blocked',json_array(json_object(
		 'sequence',3,'request_digest',?,'idempotency_digest',?,'secret','must-not-persist')),
		 strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, fixture.handoffRowID, fixture.apiKeyID,
		hex.EncodeToString(leakDigest), hex.EncodeToString(leakDigest)); err == nil {
		t.Fatal("heartbeat replay accepted an unknown raw-secret field")
	}
	for sequence := int64(3); sequence <= 4; sequence++ {
		digest := controlDigest(fmt.Sprintf("heartbeat-%d", sequence))
		if sequence == 3 {
			_, err := fixture.database.Exec(`INSERT INTO external_stage_heartbeat_windows(
				handoff_row_id,actor_api_key_id,credential_epoch,window_number,first_sequence,last_sequence,
				heartbeat_count,lifecycle_state,replay_json,last_observed_at)
				VALUES(?,?,1,1,?,?,1,'blocked',json_array(json_object(
				 'sequence',?,'request_digest',?,'idempotency_digest',?,
				 'server_received_at',strftime('%Y-%m-%dT%H:%M:%fZ','now'))),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
				fixture.handoffRowID, fixture.apiKeyID, sequence, sequence, sequence,
				hex.EncodeToString(digest), hex.EncodeToString(digest))
			if err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := fixture.database.Exec(`UPDATE external_stage_heartbeat_windows SET
				last_sequence=?,heartbeat_count=heartbeat_count+1,
				replay_json=json_insert(replay_json,'$[#]',json_object(
				 'sequence',?,'request_digest',?,'idempotency_digest',?,
				 'server_received_at',strftime('%Y-%m-%dT%H:%M:%fZ','now'))),
				last_observed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
				last_received_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
				WHERE handoff_row_id=? AND window_number=1`, sequence, sequence,
				hex.EncodeToString(digest), hex.EncodeToString(digest), fixture.handoffRowID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := fixture.database.Exec(`UPDATE external_stage_handoffs SET last_sequence=? WHERE id=?`,
			sequence, fixture.handoffRowID); err != nil {
			t.Fatal(err)
		}
	}
	var windows, count int
	var lastSequence int64
	if err := fixture.database.QueryRow(`SELECT (SELECT COUNT(*) FROM external_stage_heartbeat_windows),
		heartbeat_count,last_sequence FROM external_stage_heartbeat_windows WHERE handoff_row_id=?`, fixture.handoffRowID).
		Scan(&windows, &count, &lastSequence); err != nil {
		t.Fatal(err)
	}
	if windows != 1 || count != 2 || lastSequence != 4 {
		t.Fatalf("coalesced heartbeat windows=%d count=%d sequence=%d", windows, count, lastSequence)
	}
	for query, before := range map[string]int{
		`SELECT COUNT(*) FROM external_stage_report_events`: reportsBefore,
		`SELECT COUNT(*) FROM external_stage_audit_events`:  auditBefore,
		`SELECT COUNT(*) FROM external_stage_owner_events`:  ownerBefore,
		`SELECT COUNT(*) FROM delivery_change_log`:          changesBefore,
	} {
		var after int
		if err := fixture.database.QueryRow(query).Scan(&after); err != nil || after != before {
			t.Fatalf("heartbeat wrote semantic stream before=%d after=%d err=%v", before, after, err)
		}
	}
}

func TestM148OperationAuthorizationMatrix(t *testing.T) {
	cases := []struct {
		name, role, roleKey, membership string
		isSuper                         int
		want                            bool
	}{
		{name: "admin", role: "admin", membership: "", want: true},
		{name: "super admin", role: "member", membership: "", isSuper: 1, want: true},
		{name: "implicit member", role: "member", membership: "", want: true},
		{name: "explicit editor", role: "member", membership: "editor", want: true},
		{name: "viewer", role: "member", membership: "viewer", want: false},
		{name: "none", role: "member", membership: "none", want: false},
		{name: "external editor", role: "external", membership: "editor", want: false},
		{name: "role key external drift", role: "member", roleKey: "external", membership: "editor", want: false},
	}
	for _, principalKind := range []string{"session", "api_key"} {
		for _, tc := range cases {
			t.Run(principalKind+"/"+tc.name, func(t *testing.T) {
				fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
				result, err := fixture.database.Exec(`INSERT INTO users(username,password,role,status,is_super_admin)
					VALUES(?, 'x', ?, 'active', ?)`, "operation-"+principalKind+"-"+strings.ReplaceAll(tc.name, " ", "-"), tc.role, tc.isSuper)
				if err != nil {
					t.Fatal(err)
				}
				actorID, _ := result.LastInsertId()
				if tc.roleKey != "" {
					if _, err := fixture.database.Exec(`UPDATE users SET role_key=? WHERE id=?`, tc.roleKey, actorID); err != nil {
						t.Fatal(err)
					}
				}
				if tc.membership != "" {
					if _, err := fixture.database.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,?)`,
						actorID, fixture.projectID, tc.membership); err != nil {
						t.Fatal(err)
					}
				}
				var sessionID, keyID any
				if principalKind == "session" {
					credential := fmt.Sprintf("81000000-0000-4000-8000-%012d", actorID+100)
					if _, err := fixture.database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
						VALUES(?,?,datetime('now','+1 hour'),datetime('now'),?)`, "operation-session-"+fmt.Sprint(actorID), actorID, credential); err != nil {
						t.Fatal(err)
					}
					sessionID = credential
				} else {
					key, err := fixture.database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
						VALUES(?,'operation-key',?,'paimos_operation','agent-controls:write')`, actorID,
						fmt.Sprintf("%064x", actorID+1000))
					if err != nil {
						t.Fatal(err)
					}
					keyID, _ = key.LastInsertId()
				}
				digest := controlDigest("authorization-" + principalKind + "-" + tc.name)
				_, err = fixture.database.Exec(`INSERT INTO external_stage_operation_events(
					handoff_row_id,operation_kind,request_digest,idempotency_digest,actor_user_id,actor_principal_kind,
					actor_session_id,actor_api_key_id,credential_epoch)
					VALUES(?,'secret_minted',?,?,?,?,?,?,1)`, fixture.handoffRowID, digest, digest, actorID,
					principalKind, sessionID, keyID)
				if (err == nil) != tc.want {
					t.Fatalf("operation allowed=%v want=%v err=%v", err == nil, tc.want, err)
				}
			})
		}
	}
}

func TestM148EffectiveExternalReporterCannotRegisterOrAccept(t *testing.T) {
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	if _, err := fixture.database.Exec(`UPDATE users SET role_key='external' WHERE id=?`, fixture.reporterUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`INSERT INTO project_members(user_id,project_id,access_level)
		VALUES(?,?,'editor')`, fixture.reporterUserID, fixture.projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_reporter_registrations(
		delivery_id,project_id,user_id,api_key_id,reporter_id,reporter_class,reporter_role,dependency_key,
		allow_deployment,allow_verification,allow_authorization,allow_credential_handoff)
		VALUES(?,?,?,?,?,'janus','dependency','portal.external',0,0,1,1)`, fixture.deliveryID, fixture.projectID,
		fixture.reporterUserID, fixture.apiKeyID, fixture.reporterID); err == nil {
		t.Fatal("effective external user received a reporter registration")
	}
	mintExternalStageCredential(t, fixture)
	digest := controlDigest("external-reporter-accept")
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_operation_events(
		handoff_row_id,operation_kind,request_digest,idempotency_digest,actor_user_id,actor_principal_kind,
		actor_api_key_id,credential_epoch,sequence) VALUES(?,'accepted',?,?,?,'api_key',?,1,1)`,
		fixture.handoffRowID, digest, digest, fixture.reporterUserID, fixture.apiKeyID); err == nil {
		t.Fatal("effective external reporter accepted a handoff")
	}
}

func TestM148OwnerStreamSequenceIsServerDerivedAndWireIndependent(t *testing.T) {
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	mintExternalStageCredential(t, fixture)
	acceptExternalStageHandoff(t, fixture)
	digest := controlDigest("owner-stream-sequence")
	result, err := fixture.database.Exec(`INSERT INTO external_stage_report_events(
		handoff_row_id,actor_api_key_id,sequence,credential_epoch,request_digest,idempotency_digest,
		lifecycle_state,observed_at,heartbeat,declared_blockers)
		VALUES(?,?,2,1,?,?,'active',strftime('%Y-%m-%dT%H:%M:%fZ','now'),0,0)`,
		fixture.handoffRowID, fixture.apiKeyID, digest, digest)
	if err != nil {
		t.Fatal(err)
	}
	reportID, _ := result.LastInsertId()
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_audit_events(
		event_kind,handoff_row_id,report_event_id,api_key_id,credential_epoch,sequence,outcome,server_received_at)
		SELECT 'reported',handoff_row_id,id,actor_api_key_id,credential_epoch,sequence,'committed',server_received_at
		FROM external_stage_report_events WHERE id=?`, reportID); err != nil {
		t.Fatal(err)
	}
	insert := func(streamSequence int64) error {
		_, err := fixture.database.Exec(`INSERT INTO external_stage_owner_events(
			delivery_id,attempt_id,stage_key,execution_number,authority_epoch,handoff_row_id,report_event_id,
			sequence,stream_sequence,lifecycle_state,server_received_at)
			SELECT ?,?,'deployment',1,1,handoff_row_id,id,sequence,?,'active',server_received_at
			FROM external_stage_report_events WHERE id=?`, fixture.deliveryID, fixture.attemptID, streamSequence, reportID)
		return err
	}
	if err := insert(2); err == nil {
		t.Fatal("wire sequence was accepted as the first owner stream sequence")
	}
	if err := insert(1); err != nil {
		t.Fatalf("derived first owner stream sequence rejected: %v", err)
	}
}

func TestM148ClosedSymbolAndEvidenceShapes(t *testing.T) {
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	for _, mutation := range []struct {
		name, workflow, environment string
	}{
		{name: "uppercase workflow", workflow: "Deploy", environment: "production"},
		{name: "colon workflow", workflow: "deploy:prod", environment: "production"},
		{name: "uppercase environment", workflow: "deploy", environment: "Production"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			if _, err := fixture.database.Exec(`INSERT INTO external_stage_reporter_registrations(
				delivery_id,project_id,user_id,api_key_id,reporter_id,reporter_class,reporter_role,
				workflow_symbol,environment_symbol,allow_deployment,allow_verification,allow_authorization,allow_credential_handoff)
				VALUES(?,?,?,?,?,'pharos','owner',?,?,1,1,0,0)`, fixture.deliveryID, fixture.projectID,
				fixture.reporterUserID, fixture.apiKeyID, fixture.reporterID, mutation.workflow, mutation.environment); err == nil {
				t.Fatal("invalid symbolic binding unexpectedly succeeded")
			}
		})
	}
}

func TestM148PharosCommitIdentityAcceptsLowercase40(t *testing.T) {
	fixture, reportID := prepareExternalStageDeploymentEvidenceReport(t)
	if err := insertExternalStageDeploymentEvidence(fixture.database, reportID, strings.Repeat("a", 40)); err != nil {
		t.Fatalf("40-hex commit rejected: %v", err)
	}
}

func TestM148OptionalDependencyDoesNotBlockOwnerSuccess(t *testing.T) {
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	mintExternalStageCredential(t, fixture)
	acceptExternalStageHandoff(t, fixture)
	commitExternalStageOwnerState(t, fixture, 2, "active", "")
	seedJanusPrerequisite(t, fixture, "optional", false)
	digest := controlDigest("optional-dependency-owner-success")
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_report_events(
		handoff_row_id,actor_api_key_id,sequence,credential_epoch,request_digest,idempotency_digest,
		lifecycle_state,observed_at,heartbeat,declared_blockers,evidence_kind)
		VALUES(?,?,3,1,?,?,'succeeded',strftime('%Y-%m-%dT%H:%M:%fZ','now'),0,0,'deployment')`,
		fixture.handoffRowID, fixture.apiKeyID, digest, digest); err != nil {
		t.Fatalf("optional dependency blocked owner success: %v", err)
	}
}

func TestM148UnsatisfiedRequiredDependencyBlocksOwnerSuccess(t *testing.T) {
	fixture := seedExternalStageSchemaFixture(t, externalStageValidLocator)
	mintExternalStageCredential(t, fixture)
	acceptExternalStageHandoff(t, fixture)
	commitExternalStageOwnerState(t, fixture, 2, "active", "")
	seedJanusPrerequisite(t, fixture, "required", false)
	digest := controlDigest("required-dependency-owner-success")
	if _, err := fixture.database.Exec(`INSERT INTO external_stage_report_events(
		handoff_row_id,actor_api_key_id,sequence,credential_epoch,request_digest,idempotency_digest,
		lifecycle_state,observed_at,heartbeat,declared_blockers,evidence_kind)
		VALUES(?,?,3,1,?,?,'succeeded',strftime('%Y-%m-%dT%H:%M:%fZ','now'),0,0,'deployment')`,
		fixture.handoffRowID, fixture.apiKeyID, digest, digest); err == nil {
		t.Fatal("unsatisfied required dependency allowed owner success")
	}
}

func TestM148PharosCommitIdentityAcceptsLowercase64(t *testing.T) {
	fixture, reportID := prepareExternalStageDeploymentEvidenceReport(t)
	if err := insertExternalStageDeploymentEvidence(fixture.database, reportID, strings.Repeat("b", 64)); err != nil {
		t.Fatalf("64-hex commit rejected: %v", err)
	}
}

func TestM148PharosCommitIdentityRejectsMalformedValues(t *testing.T) {
	fixture, reportID := prepareExternalStageDeploymentEvidenceReport(t)
	for _, commit := range []string{
		strings.Repeat("A", 40), strings.Repeat("g", 40), strings.Repeat("a", 39), strings.Repeat("a", 41),
		strings.Repeat("b", 63), strings.Repeat("b", 65),
	} {
		if err := insertExternalStageDeploymentEvidence(fixture.database, reportID, commit); err == nil {
			t.Fatalf("malformed commit %q unexpectedly persisted", commit)
		}
	}
}

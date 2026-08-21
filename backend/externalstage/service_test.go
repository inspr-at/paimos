// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package externalstage

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/contracts"
	appdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
)

type serviceFixture struct {
	database         *sql.DB
	service          *Service
	operator         Principal
	reporter         Principal
	deliveryID       int64
	deliveryKey      string
	issueID          int64
	attemptID        int64
	registrationID   int64
	executionStartID int64
	authorityStageID int64
	now              time.Time
	wakes            atomic.Int64
}

type partialSecretReader struct{ target []byte }

func assertExternalStageSentinelsAbsent(t *testing.T, database *sql.DB, secret []byte) {
	t.Helper()
	hexValue := hex.EncodeToString(secret)
	sentinels := [][]byte{
		append([]byte(nil), secret...),
		[]byte(hexValue),
		[]byte("sha256:" + hexValue),
		[]byte(base64.RawURLEncoding.EncodeToString(secret)),
		[]byte(base64.URLEncoding.EncodeToString(secret)),
		[]byte(base64.RawStdEncoding.EncodeToString(secret)),
		[]byte(base64.StdEncoding.EncodeToString(secret)),
	}
	tables, err := database.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'external_stage_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := tables.Close(); err != nil {
		t.Fatal(err)
	}
	for _, table := range names {
		quotedTable := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		columns, err := database.Query(`PRAGMA table_info(` + quotedTable + `)`)
		if err != nil {
			t.Fatal(err)
		}
		var columnNames []string
		for columns.Next() {
			var ordinal int
			var name, kind string
			var nullable, primaryKey int
			var defaultValue any
			if err := columns.Scan(&ordinal, &name, &kind, &nullable, &defaultValue, &primaryKey); err != nil {
				t.Fatal(err)
			}
			columnNames = append(columnNames, name)
		}
		if err := columns.Close(); err != nil {
			t.Fatal(err)
		}
		for _, column := range columnNames {
			quotedColumn := `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
			rows, err := database.Query(`SELECT ` + quotedColumn + ` FROM ` + quotedTable + ` WHERE ` + quotedColumn + ` IS NOT NULL`)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var value any
				if err := rows.Scan(&value); err != nil {
					t.Fatal(err)
				}
				var raw []byte
				switch typed := value.(type) {
				case string:
					raw = []byte(typed)
				case []byte:
					raw = typed
				default:
					continue
				}
				for _, sentinel := range sentinels {
					if len(sentinel) > 0 && bytes.Contains(raw, sentinel) {
						t.Fatalf("secret sentinel persisted in %s.%s", table, column)
					}
				}
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func (r *partialSecretReader) Read(p []byte) (int, error) {
	r.target = p
	copy(p, []byte("partial-secret"))
	return len("partial-secret"), errors.New("injected random failure")
}

func setupServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := appdb.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = appdb.DB.Close()
		appdb.DB = nil
	})
	f := &serviceFixture{database: appdb.DB, now: time.Now().UTC().Truncate(time.Millisecond)}
	result, err := f.database.Exec(`INSERT INTO projects(name,key) VALUES('External stage service','ESS')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := result.LastInsertId()
	result, err = f.database.Exec(`INSERT INTO issues(project_id,issue_number,title) VALUES(?,810,'External stage root')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	f.issueID, _ = result.LastInsertId()
	f.deliveryKey = fmt.Sprintf("issue:%d", f.issueID)
	result, err = f.database.Exec(`INSERT INTO deliveries(issue_id,delivery_key,project_id_hint,created_at,updated_at)
		VALUES(?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, f.issueID, f.deliveryKey, projectID)
	if err != nil {
		t.Fatal(err)
	}
	f.deliveryID, _ = result.LastInsertId()
	result, err = f.database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
		VALUES(?,'system','external-stage-test',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, f.deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	systemReporterID, _ := result.LastInsertId()
	result, err = f.database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
		kind,reporter_id,server_received_at) VALUES(?,1,'attempt',zeroblob(32),'attempt_started',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, f.deliveryID, systemReporterID)
	if err != nil {
		t.Fatal(err)
	}
	attemptEventID, _ := result.LastInsertId()
	result, err = f.database.Exec(`INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,start_delivery_event_id,
		project_id_at_start,reason_code,created_at) VALUES(?,1,1,?,?,'external_stage_test',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, f.deliveryID, attemptEventID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	f.attemptID, _ = result.LastInsertId()
	for index, stage := range []string{"specification", "implementation", "qa", "deployment", "verification"} {
		if stage == "deployment" || stage == "verification" {
			if _, err := f.database.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,sort_order,
				applicability,weight,created_at) VALUES(?,?,?,?,'required',50,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, f.deliveryID, f.attemptID, stage, index+1); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err := f.database.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,sort_order,
			applicability,weight,policy_reference,reason_code,authorized_by_reporter_id,created_at)
			VALUES(?,?,?,?,'not_applicable',0,'external-stage-test','not_required',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
			f.deliveryID, f.attemptID, stage, index+1, systemReporterID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.database.Exec(`INSERT INTO delivery_attempt_policy_seals(delivery_id,attempt_id,sealed_at)
		VALUES(?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, f.deliveryID, f.attemptID); err != nil {
		t.Fatal(err)
	}
	result, err = f.database.Exec(`INSERT INTO users(username,password,role,status) VALUES('external-stage-operator','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	operatorID, _ := result.LastInsertId()
	if _, err := f.database.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')`, operatorID, projectID); err != nil {
		t.Fatal(err)
	}
	const operatorCredential = "81000000-0000-4000-8000-000000000810"
	if _, err := f.database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('external-stage-service-session',?,datetime('now','+1 hour'),datetime('now'),?)`, operatorID, operatorCredential); err != nil {
		t.Fatal(err)
	}
	f.operator = Principal{UserID: operatorID, Kind: "session", SessionCredentialID: operatorCredential}
	result, err = f.database.Exec(`INSERT INTO users(username,password,role,status) VALUES('pharos-service','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	reporterUserID, _ := result.LastInsertId()
	result, err = f.database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'pharos-service',?,'paimos_pharos_service','*')`, reporterUserID, fmt.Sprintf("%064d", 810))
	if err != nil {
		t.Fatal(err)
	}
	reporterAPIKeyID, _ := result.LastInsertId()
	f.reporter = Principal{UserID: reporterUserID, Kind: "api_key", APIKeyID: reporterAPIKeyID}
	service, err := NewService(f.database, Options{FixtureDigest: contracts.ExternalStageV1FixtureDigest(),
		Random: cryptorand.Reader, Clock: clockFunc(func() time.Time { return f.now }),
		Observer: func(context.Context, delivery.ChangeHint) { f.wakes.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	f.service = service
	registration, err := service.RegisterReporter(context.Background(), f.operator, f.deliveryKey, "register-pharos",
		RegisterReporterRequest{APIKeyID: reporterAPIKeyID, ReporterClass: ReporterClassPharos,
			ReporterRole: ReporterRoleOwner, Workflow: "deploy-production", Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	f.registrationID = registration.RegistrationID
	result, err = f.database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
		kind,reporter_id,server_received_at) VALUES(?,2,'deployment-start',zeroblob(32),'stage_execution_started',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, f.deliveryID, registration.ReporterID)
	if err != nil {
		t.Fatal(err)
	}
	startEnvelopeID, _ := result.LastInsertId()
	result, err = f.database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,semantic_state,
		authority_source_sequence_cutoff,server_received_at) VALUES(?,?,'deployment',1,1,1,?,'execution_started',?,'active',0,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, f.deliveryID, f.attemptID, startEnvelopeID, registration.ReporterID)
	if err != nil {
		t.Fatal(err)
	}
	f.executionStartID, _ = result.LastInsertId()
	f.authorityStageID = f.executionStartID
	if _, err := f.database.Exec(`INSERT INTO delivery_stage_latest(delivery_id,attempt_id,stage_key,execution_number,
		authority_epoch,current_reporter_id,execution_start_stage_event_id,authority_stage_event_id,updated_at)
		VALUES(?,?,'deployment',1,1,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, f.deliveryID, f.attemptID,
		registration.ReporterID, f.executionStartID, f.authorityStageID); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *serviceFixture) sealEmpty(t *testing.T) {
	t.Helper()
	if _, err := f.service.SealPrerequisites(context.Background(), f.operator, f.deliveryKey, "seal-omitted",
		SealPrerequisitesRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("omitted prerequisites err=%v", err)
	}
	set, err := f.service.SealPrerequisites(context.Background(), f.operator, f.deliveryKey, "seal-empty",
		SealPrerequisitesRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, Prerequisites: []Prerequisite{}})
	if err != nil {
		t.Fatal(err)
	}
	if set.DeclaredCount != 0 {
		t.Fatalf("declared_count=%d", set.DeclaredCount)
	}
	replay, err := f.service.SealPrerequisites(context.Background(), f.operator, f.deliveryKey, "seal-empty",
		SealPrerequisitesRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, Prerequisites: []Prerequisite{}})
	if err != nil || replay.SealedAt != set.SealedAt || replay.DeclaredCount != 0 {
		t.Fatalf("empty seal replay=%+v err=%v", replay, err)
	}
	if _, err := f.service.SealPrerequisites(context.Background(), f.operator, f.deliveryKey, "seal-empty",
		SealPrerequisitesRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 2,
			ExpectedAuthorityEpoch: 1, Prerequisites: []Prerequisite{}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting seal replay err=%v", err)
	}
}

func TestServiceOwnerLifecycleReplayHeartbeatAndRestart(t *testing.T) {
	f := setupServiceFixture(t)
	f.sealEmpty(t)
	ctx := context.Background()
	request := CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
		ExpectedAuthorityEpoch: 1, ReporterRegistrationID: f.registrationID,
		ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)}
	metadata, err := f.service.CreateHandoff(ctx, f.operator, f.deliveryKey, "create-owner", request)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Duplicate || metadata.CredentialEpoch != 0 || metadata.State != HandoffStateIssued {
		t.Fatalf("unexpected create metadata: %+v", metadata)
	}
	replay, err := f.service.CreateHandoff(ctx, f.operator, f.deliveryKey, "create-owner", request)
	if err != nil || !replay.Duplicate || replay.HandoffID != metadata.HandoffID || replay.CredentialEpoch != 0 {
		t.Fatalf("create replay=%+v err=%v", replay, err)
	}
	if _, err := f.database.Exec(`UPDATE project_members SET access_level='viewer' WHERE user_id=?`, f.operator.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CreateHandoff(ctx, f.operator, f.deliveryKey, "create-owner", request); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lost-access create replay err=%v", err)
	}
	if _, err := f.database.Exec(`UPDATE project_members SET access_level='editor' WHERE user_id=?`, f.operator.UserID); err != nil {
		t.Fatal(err)
	}
	secret, err := f.service.Mint(ctx, f.operator, metadata.HandoffID, 0, false)
	if err != nil || len(secret) != OneTimeSecretBytes {
		t.Fatalf("mint len=%d err=%v", len(secret), err)
	}
	if _, err := f.service.Pull(ctx, f.reporter, metadata.HandoffID, secret); err != nil {
		t.Fatal(err)
	}
	oldSecret := append([]byte(nil), secret...)
	secret, err = f.service.Mint(ctx, f.operator, metadata.HandoffID, 1, true)
	if err != nil || len(secret) != OneTimeSecretBytes {
		t.Fatalf("rotate len=%d err=%v", len(secret), err)
	}
	if _, err := f.service.Pull(ctx, f.reporter, metadata.HandoffID, oldSecret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rotated secret remained valid: %v", err)
	}
	if _, err := f.service.Pull(ctx, f.reporter, metadata.HandoffID, secret); err != nil {
		t.Fatal(err)
	}
	wrongKey := Principal{UserID: f.reporter.UserID, Kind: "api_key", APIKeyID: f.reporter.APIKeyID + 1000}
	if _, err := f.service.Pull(ctx, wrongKey, metadata.HandoffID, secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong key with right secret err=%v", err)
	}
	if _, err := f.database.Exec(`UPDATE users SET status='inactive' WHERE id=?`, f.reporter.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Pull(ctx, f.reporter, metadata.HandoffID, secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("inactive reporter pull err=%v", err)
	}
	if _, err := f.database.Exec(`UPDATE users SET status='active' WHERE id=?`, f.reporter.UserID); err != nil {
		t.Fatal(err)
	}
	accepted, err := f.service.Accept(ctx, f.reporter, metadata.HandoffID, "accept-owner", secret,
		AcceptRequest{Sequence: 1, ObservedAt: f.now.Format(time.RFC3339Nano)})
	if err != nil || accepted.Duplicate {
		t.Fatalf("accept=%+v err=%v", accepted, err)
	}
	acceptedReplay, err := f.service.Accept(ctx, f.reporter, metadata.HandoffID, "accept-owner", secret,
		AcceptRequest{Sequence: 1, ObservedAt: f.now.Format(time.RFC3339Nano)})
	if err != nil || !acceptedReplay.Duplicate || acceptedReplay.ServerReceivedAt != accepted.ServerReceivedAt {
		t.Fatalf("accept replay=%+v err=%v", acceptedReplay, err)
	}
	active, err := f.service.Report(ctx, f.reporter, metadata.HandoffID, "active-owner", secret,
		ReportRequest{Sequence: 2, State: HandoffStateActive, ObservedAt: f.now.Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	wakesAfterActive := f.wakes.Load()
	heartbeatRequest := ReportRequest{Sequence: 3, State: HandoffStateActive, ObservedAt: f.now.Format(time.RFC3339Nano), Heartbeat: true}
	heartbeat, err := f.service.Report(ctx, f.reporter, metadata.HandoffID, "heartbeat-owner", secret, heartbeatRequest)
	if err != nil {
		t.Fatal(err)
	}
	if f.wakes.Load() != wakesAfterActive {
		t.Fatalf("heartbeat emitted wake: before=%d after=%d", wakesAfterActive, f.wakes.Load())
	}
	var semanticEvents, changeHints int
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM delivery_stage_events WHERE attempt_id=? AND stage_key='deployment'`, f.attemptID).Scan(&semanticEvents); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log WHERE delivery_id=?`, f.deliveryID).Scan(&changeHints); err != nil {
		t.Fatal(err)
	}
	if semanticEvents != 2 || changeHints != 1 {
		t.Fatalf("heartbeat semantic spill: stage_events=%d change_hints=%d", semanticEvents, changeHints)
	}
	f.now = f.now.Add(time.Second)
	terminalRequest := ReportRequest{Sequence: 4, State: HandoffStateSucceeded, ObservedAt: f.now.Format(time.RFC3339Nano),
		PharosEvidence: &PharosEvidence{Kind: EvidenceKindDeployment, Workflow: "deploy-production", Environment: "production",
			Artifact: ArtifactEvidence{Version: "v1.2.3", Digest: "sha256:" + fmt.Sprintf("%064x", 810), CommitDigest: fmt.Sprintf("%040x", 810)},
			Result:   EvidenceResultSucceeded, ObservedAt: f.now.Format(time.RFC3339Nano)}}
	var externalReportsBefore, externalAuditsBefore int
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM external_stage_report_events`).Scan(&externalReportsBefore); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM external_stage_audit_events`).Scan(&externalAuditsBefore); err != nil {
		t.Fatal(err)
	}
	secretHex := hex.EncodeToString(secret)
	secretEchoes := []struct {
		name   string
		mutate func(*PharosEvidence)
	}{
		{name: "version-base64url", mutate: func(e *PharosEvidence) { e.Artifact.Version = base64.RawURLEncoding.EncodeToString(secret) }},
		{name: "version-prefixed-base64url", mutate: func(e *PharosEvidence) { e.Artifact.Version = "v" + base64.RawURLEncoding.EncodeToString(secret) }},
		{name: "artifact-digest-hex", mutate: func(e *PharosEvidence) { e.Artifact.Digest = "sha256:" + secretHex }},
		{name: "commit-digest-hex", mutate: func(e *PharosEvidence) { e.Artifact.CommitDigest = secretHex }},
	}
	for _, test := range secretEchoes {
		evidence := *terminalRequest.PharosEvidence
		test.mutate(&evidence)
		request := terminalRequest
		request.PharosEvidence = &evidence
		if _, err := f.service.Report(ctx, f.reporter, metadata.HandoffID, "secret-echo-"+test.name, secret, request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s secret echo err=%v", test.name, err)
		}
	}
	var externalReportsAfter, externalAuditsAfter int
	var ownerStateAfterEcho string
	var ownerSequenceAfterEcho int64
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM external_stage_report_events`).Scan(&externalReportsAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM external_stage_audit_events`).Scan(&externalAuditsAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT lifecycle_state,sequence FROM external_stage_owner_latest
		WHERE attempt_id=? AND stage_key='deployment'`, f.attemptID).Scan(&ownerStateAfterEcho, &ownerSequenceAfterEcho); err != nil {
		t.Fatal(err)
	}
	if externalReportsAfter != externalReportsBefore || externalAuditsAfter != externalAuditsBefore ||
		ownerStateAfterEcho != "active" || ownerSequenceAfterEcho != 2 || f.wakes.Load() != wakesAfterActive {
		t.Fatalf("secret echo leaked effects reports=%d/%d audits=%d/%d owner=%s/%d wakes=%d/%d",
			externalReportsBefore, externalReportsAfter, externalAuditsBefore, externalAuditsAfter,
			ownerStateAfterEcho, ownerSequenceAfterEcho, wakesAfterActive, f.wakes.Load())
	}
	injected := errors.New("injected pre-commit refusal")
	f.service.beforeCommit = func(operation string) error {
		if operation != "report" {
			t.Fatalf("unexpected failure operation %q", operation)
		}
		return injected
	}
	if _, err := f.service.Report(ctx, f.reporter, metadata.HandoffID, "rollback-terminal", secret, terminalRequest); !errors.Is(err, injected) {
		t.Fatalf("failure injection err=%v", err)
	}
	f.service.beforeCommit = nil
	var evidenceAfterRollback, stageEventsAfterRollback, changesAfterRollback int
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM external_stage_pharos_evidence`).Scan(&evidenceAfterRollback); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM delivery_stage_events WHERE attempt_id=? AND stage_key='deployment'`, f.attemptID).Scan(&stageEventsAfterRollback); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log WHERE delivery_id=?`, f.deliveryID).Scan(&changesAfterRollback); err != nil {
		t.Fatal(err)
	}
	if evidenceAfterRollback != 0 || stageEventsAfterRollback != semanticEvents || changesAfterRollback != changeHints || f.wakes.Load() != wakesAfterActive {
		t.Fatalf("rollback leaked evidence=%d stage_events=%d/%d changes=%d/%d wakes=%d/%d", evidenceAfterRollback,
			stageEventsAfterRollback, semanticEvents, changesAfterRollback, changeHints, f.wakes.Load(), wakesAfterActive)
	}
	terminal, err := f.service.Report(ctx, f.reporter, metadata.HandoffID, "succeed-owner", secret, terminalRequest)
	if err != nil {
		t.Fatal(err)
	}
	assertExternalStageSentinelsAbsent(t, f.database, secret)
	assertExternalStageSentinelsAbsent(t, f.database, oldSecret)
	if err := f.database.Close(); err != nil {
		t.Fatal(err)
	}
	appdb.DB = nil
	if err := appdb.Open(); err != nil {
		t.Fatal(err)
	}
	f.database = appdb.DB
	restarted, err := NewService(f.database, Options{FixtureDigest: contracts.ExternalStageV1FixtureDigest(), Clock: clockFunc(func() time.Time { return f.now })})
	if err != nil {
		t.Fatal(err)
	}
	f.service = restarted
	heartbeatReplay, err := restarted.Report(ctx, f.reporter, metadata.HandoffID, "heartbeat-owner", secret, heartbeatRequest)
	if err != nil || !heartbeatReplay.Duplicate || heartbeatReplay.ServerReceivedAt != heartbeat.ServerReceivedAt {
		t.Fatalf("heartbeat restart replay=%+v err=%v", heartbeatReplay, err)
	}
	terminalReplay, err := restarted.Report(ctx, f.reporter, metadata.HandoffID, "succeed-owner", secret, terminalRequest)
	if err != nil || !terminalReplay.Duplicate || terminalReplay.ServerReceivedAt != terminal.ServerReceivedAt {
		t.Fatalf("terminal restart replay=%+v err=%v", terminalReplay, err)
	}
	terminalCreateReplay, err := restarted.CreateHandoff(ctx, f.operator, f.deliveryKey, "create-owner", request)
	if err != nil || !terminalCreateReplay.Duplicate || terminalCreateReplay.HandoffID != metadata.HandoffID {
		t.Fatalf("terminal create replay=%+v err=%v", terminalCreateReplay, err)
	}
	if _, err := restarted.CreateHandoff(ctx, f.operator, f.deliveryKey, "create-after-terminal", request); !errors.Is(err, ErrNotFound) {
		t.Fatalf("new create after terminal err=%v", err)
	}
	if _, err := restarted.SealPrerequisites(ctx, f.operator, f.deliveryKey, "seal-empty",
		SealPrerequisitesRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, Prerequisites: []Prerequisite{}}); err != nil {
		t.Fatalf("terminal seal replay err=%v", err)
	}
	if _, err := restarted.SealPrerequisites(ctx, f.operator, f.deliveryKey, "seal-after-terminal",
		SealPrerequisitesRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, Prerequisites: []Prerequisite{}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("new seal after terminal err=%v", err)
	}
	verificationRegistration, err := f.service.RegisterReporter(ctx, f.operator, f.deliveryKey, "register-verifier",
		RegisterReporterRequest{APIKeyID: f.reporter.APIKeyID, ReporterClass: ReporterClassPharos,
			ReporterRole: ReporterRoleOwner, Workflow: "verify-production", Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	var deploymentTerminalID, nextRevision int64
	if err := f.database.QueryRow(`SELECT semantic_stage_event_id FROM delivery_stage_latest
		WHERE attempt_id=? AND stage_key='deployment'`, f.attemptID).Scan(&deploymentTerminalID); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT COALESCE(MAX(delivery_revision),0)+1 FROM delivery_events WHERE delivery_id=?`, f.deliveryID).Scan(&nextRevision); err != nil {
		t.Fatal(err)
	}
	result, err := f.database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
		kind,reporter_id,server_received_at) VALUES(?,?,'verification-start',zeroblob(32),'stage_execution_started',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		f.deliveryID, nextRevision, verificationRegistration.ReporterID)
	if err != nil {
		t.Fatal(err)
	}
	verificationEnvelopeID, _ := result.LastInsertId()
	result, err = f.database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,based_on_stage_event_id,semantic_state,
		authority_source_sequence_cutoff,server_received_at) VALUES(?,?,'verification',1,1,1,?,'execution_started',?,?,'active',0,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		f.deliveryID, f.attemptID, verificationEnvelopeID, verificationRegistration.ReporterID, deploymentTerminalID)
	if err != nil {
		t.Fatal(err)
	}
	verificationStartID, _ := result.LastInsertId()
	if _, err := f.database.Exec(`INSERT INTO delivery_stage_latest(delivery_id,attempt_id,stage_key,execution_number,
		authority_epoch,current_reporter_id,execution_start_stage_event_id,authority_stage_event_id,updated_at)
		VALUES(?,?,'verification',1,1,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, f.deliveryID, f.attemptID,
		verificationRegistration.ReporterID, verificationStartID, verificationStartID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SealPrerequisites(ctx, f.operator, f.deliveryKey, "seal-verification-empty",
		SealPrerequisitesRequest{StageKey: "verification", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, Prerequisites: []Prerequisite{}}); err != nil {
		t.Fatal(err)
	}
	deployedAt, err := time.Parse(time.RFC3339Nano, terminal.ServerReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	f.now = deployedAt.Add(time.Second)
	verificationHandoff, err := f.service.CreateHandoff(ctx, f.operator, f.deliveryKey, "create-verification",
		CreateHandoffRequest{StageKey: "verification", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, ReporterRegistrationID: verificationRegistration.RegistrationID,
			ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	verificationSecret, err := f.service.Mint(ctx, f.operator, verificationHandoff.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Accept(ctx, f.reporter, verificationHandoff.HandoffID, "accept-verification", verificationSecret,
		AcceptRequest{Sequence: 1, ObservedAt: f.now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Report(ctx, f.reporter, verificationHandoff.HandoffID, "active-verification", verificationSecret,
		ReportRequest{Sequence: 2, State: HandoffStateActive, ObservedAt: f.now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	verificationEvidence := PharosEvidence{Kind: EvidenceKindVerification, Workflow: "verify-production", Environment: "production",
		Artifact: terminalRequest.PharosEvidence.Artifact, Result: EvidenceResultSucceeded, ObservedAt: f.now.Format(time.RFC3339Nano)}
	verificationMismatches := []struct {
		name   string
		mutate func(*PharosEvidence)
	}{
		{name: "artifact-version", mutate: func(e *PharosEvidence) { e.Artifact.Version = "v9.9.9" }},
		{name: "artifact-digest", mutate: func(e *PharosEvidence) {
			e.Artifact.Digest = "sha256:" + fmt.Sprintf("%064x", 811)
		}},
		{name: "commit-digest", mutate: func(e *PharosEvidence) { e.Artifact.CommitDigest = fmt.Sprintf("%040x", 811) }},
		{name: "observation-at-deployment-receipt", mutate: func(e *PharosEvidence) {
			e.ObservedAt = terminal.ServerReceivedAt
		}},
	}
	for _, test := range verificationMismatches {
		mismatch := verificationEvidence
		test.mutate(&mismatch)
		if _, err := f.service.Report(ctx, f.reporter, verificationHandoff.HandoffID, "mismatch-verification-"+test.name, verificationSecret,
			ReportRequest{Sequence: 3, State: HandoffStateSucceeded, ObservedAt: mismatch.ObservedAt, PharosEvidence: &mismatch}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s verification mismatch err=%v", test.name, err)
		}
	}

	var deploymentEvidenceReceivedAt, deploymentEnvironment, evidenceNoUpdateTriggerSQL string
	if err := f.database.QueryRow(`SELECT server_received_at,environment_symbol FROM external_stage_pharos_evidence
		WHERE evidence_kind='deployment'`).Scan(&deploymentEvidenceReceivedAt, &deploymentEnvironment); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger'
		AND name='trg_external_stage_pharos_evidence_no_update'`).Scan(&evidenceNoUpdateTriggerSQL); err != nil {
		t.Fatal(err)
	}
	// SQLite owns the receipt clock. In this isolated database, temporarily lift
	// the append-only update guard and advance the already-valid deployment
	// receipt so the real verification insert guard deterministically exercises
	// its exact environment and server-receipt branches. Restore both before the
	// positive verification below.
	if _, err := f.database.Exec(`DROP TRIGGER trg_external_stage_pharos_evidence_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec(`UPDATE external_stage_pharos_evidence SET environment_symbol='staging'
		WHERE evidence_kind='deployment'`); err != nil {
		t.Fatal(err)
	}
	_, environmentErr := f.service.Report(ctx, f.reporter, verificationHandoff.HandoffID, "mismatch-verification-environment", verificationSecret,
		ReportRequest{Sequence: 3, State: HandoffStateSucceeded, ObservedAt: verificationEvidence.ObservedAt, PharosEvidence: &verificationEvidence})
	if _, err := f.database.Exec(`UPDATE external_stage_pharos_evidence SET environment_symbol=?
		WHERE evidence_kind='deployment'`, deploymentEnvironment); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(environmentErr, ErrInvalid) {
		t.Fatalf("verification environment mismatch err=%v", environmentErr)
	}
	futureDeploymentReceipt := f.now.Add(30 * time.Second).Format(time.RFC3339Nano)
	if _, err := f.database.Exec(`UPDATE external_stage_pharos_evidence SET server_received_at=?
		WHERE evidence_kind='deployment'`, futureDeploymentReceipt); err != nil {
		t.Fatal(err)
	}
	receiptMismatch := verificationEvidence
	receiptMismatch.ObservedAt = f.now.Add(31 * time.Second).Format(time.RFC3339Nano)
	_, receiptErr := f.service.Report(ctx, f.reporter, verificationHandoff.HandoffID, "mismatch-verification-server-receipt", verificationSecret,
		ReportRequest{Sequence: 3, State: HandoffStateSucceeded, ObservedAt: receiptMismatch.ObservedAt, PharosEvidence: &receiptMismatch})
	if _, err := f.database.Exec(`UPDATE external_stage_pharos_evidence SET server_received_at=?
		WHERE evidence_kind='deployment'`, deploymentEvidenceReceivedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec(evidenceNoUpdateTriggerSQL); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(receiptErr, ErrInvalid) {
		t.Fatalf("verification server receipt at-or-before deployment receipt err=%v", receiptErr)
	}
	verified, err := f.service.Report(ctx, f.reporter, verificationHandoff.HandoffID, "exact-verification", verificationSecret,
		ReportRequest{Sequence: 3, State: HandoffStateSucceeded, ObservedAt: f.now.Format(time.RFC3339Nano), PharosEvidence: &verificationEvidence})
	if err != nil || verified.State != HandoffStateSucceeded {
		t.Fatalf("exact verification=%+v err=%v", verified, err)
	}
	f.now = f.now.Add(2 * time.Hour)
	expiredReplay, err := restarted.Report(ctx, f.reporter, metadata.HandoffID, "succeed-owner", secret, terminalRequest)
	if err != nil || !expiredReplay.Duplicate || expiredReplay.ServerReceivedAt != terminal.ServerReceivedAt {
		t.Fatalf("expired terminal replay=%+v err=%v", expiredReplay, err)
	}
	if _, err := restarted.Report(ctx, f.reporter, metadata.HandoffID, "after-terminal", secret,
		ReportRequest{Sequence: 5, State: HandoffStateActive, ObservedAt: f.now.Format(time.RFC3339Nano)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("new terminal report err=%v", err)
	}
	if _, err := restarted.Mint(ctx, f.operator, metadata.HandoffID, 2, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal rotation err=%v", err)
	}
	if active.Sequence != 2 || terminal.Sequence != 4 {
		t.Fatalf("receipt sequences active=%d terminal=%d", active.Sequence, terminal.Sequence)
	}
}

func TestServiceCurrentExternalBindingMutationsAreConcealed(t *testing.T) {
	createMintedHandoff := func(t *testing.T) (*serviceFixture, CreateHandoffResult, []byte) {
		t.Helper()
		f := setupServiceFixture(t)
		f.sealEmpty(t)
		handoff, err := f.service.CreateHandoff(t.Context(), f.operator, f.deliveryKey, "binding-create",
			CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
				ExpectedAuthorityEpoch: 1, ReporterRegistrationID: f.registrationID,
				ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)})
		if err != nil {
			t.Fatal(err)
		}
		secret, err := f.service.Mint(t.Context(), f.operator, handoff.HandoffID, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for index := range secret {
				secret[index] = 0
			}
		})
		return f, handoff, secret
	}

	t.Run("revoked reporter registration", func(t *testing.T) {
		f, handoff, secret := createMintedHandoff(t)
		if _, err := f.service.Pull(t.Context(), f.reporter, handoff.HandoffID, secret); err != nil {
			t.Fatalf("current registration rejected: %v", err)
		}
		if _, err := f.service.RevokeReporter(t.Context(), f.operator, f.deliveryKey, "binding-revoke", f.registrationID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.Pull(t.Context(), f.reporter, handoff.HandoffID, secret); !errors.Is(err, ErrNotFound) {
			t.Fatalf("revoked registration remained valid: %v", err)
		}
	})

	t.Run("disabled api key", func(t *testing.T) {
		f, handoff, secret := createMintedHandoff(t)
		if _, err := f.database.Exec(`UPDATE api_keys SET disabled_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, f.reporter.APIKeyID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.Pull(t.Context(), f.reporter, handoff.HandoffID, secret); !errors.Is(err, ErrNotFound) {
			t.Fatalf("disabled key remained valid: %v", err)
		}
	})

	t.Run("expired api key", func(t *testing.T) {
		f, handoff, secret := createMintedHandoff(t)
		if _, err := f.database.Exec(`UPDATE api_keys SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 minute') WHERE id=?`, f.reporter.APIKeyID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.Pull(t.Context(), f.reporter, handoff.HandoffID, secret); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expired key remained valid: %v", err)
		}
	})

	t.Run("superseded attempt", func(t *testing.T) {
		f, handoff, secret := createMintedHandoff(t)
		var reporterID, nextRevision int64
		if err := f.database.QueryRow(`SELECT id FROM delivery_reporters WHERE delivery_id=? AND opaque_key='external-stage-test'`,
			f.deliveryID).Scan(&reporterID); err != nil {
			t.Fatal(err)
		}
		if err := f.database.QueryRow(`SELECT COALESCE(MAX(delivery_revision),0)+1 FROM delivery_events WHERE delivery_id=?`,
			f.deliveryID).Scan(&nextRevision); err != nil {
			t.Fatal(err)
		}
		result, err := f.database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
			kind,reporter_id,server_received_at) VALUES(?,?,'binding-attempt-2',zeroblob(32),'attempt_started',?,
			strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, f.deliveryID, nextRevision, reporterID)
		if err != nil {
			t.Fatal(err)
		}
		startEventID, _ := result.LastInsertId()
		attemptResult, err := f.database.Exec(`INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,previous_attempt_id,
			start_delivery_event_id,reason_code,created_at) VALUES(?,2,2,?,?,'retry',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
			f.deliveryID, f.attemptID, startEventID)
		if err != nil {
			t.Fatal(err)
		}
		attemptID, _ := attemptResult.LastInsertId()
		if _, err := f.database.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,sort_order,
			applicability,weight,policy_reference,reason_code,reason_text,authorized_by_reporter_id,created_at)
			SELECT delivery_id,?,stage_key,sort_order,applicability,weight,policy_reference,reason_code,reason_text,
			authorized_by_reporter_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
			FROM delivery_attempt_stage_policy WHERE attempt_id=?`, attemptID, f.attemptID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.database.Exec(`INSERT INTO delivery_attempt_policy_seals(delivery_id,attempt_id,sealed_at)
			VALUES(?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, f.deliveryID, attemptID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.Pull(t.Context(), f.reporter, handoff.HandoffID, secret); !errors.Is(err, ErrNotFound) {
			t.Fatalf("superseded attempt handoff remained current: %v", err)
		}
	})
}

func TestServiceJanusDependencyIsAtomicAndCannotOwnCanonicalStage(t *testing.T) {
	f := setupServiceFixture(t)
	ctx := context.Background()
	result, err := f.database.Exec(`INSERT INTO users(username,password,role,status) VALUES('janus-service','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	janusUserID, _ := result.LastInsertId()
	result, err = f.database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'janus-service',?,'paimos_janus_service','*')`, janusUserID, fmt.Sprintf("%064d", 811))
	if err != nil {
		t.Fatal(err)
	}
	janusAPIKeyID, _ := result.LastInsertId()
	janusPrincipal := Principal{UserID: janusUserID, Kind: "api_key", APIKeyID: janusAPIKeyID}
	required, err := f.service.RegisterReporter(ctx, f.operator, f.deliveryKey, "register-janus-required",
		RegisterReporterRequest{APIKeyID: janusAPIKeyID, ReporterClass: ReporterClassJanus,
			ReporterRole: ReporterRoleDependency, DependencyKey: "security.approval"})
	if err != nil {
		t.Fatal(err)
	}
	optional, err := f.service.RegisterReporter(ctx, f.operator, f.deliveryKey, "register-janus-optional",
		RegisterReporterRequest{APIKeyID: janusAPIKeyID, ReporterClass: ReporterClassJanus,
			ReporterRole: ReporterRoleDependency, DependencyKey: "change.window"})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := f.service.SealPrerequisites(ctx, f.operator, f.deliveryKey, "seal-mixed",
		SealPrerequisitesRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, Prerequisites: []Prerequisite{
				{DependencyKey: "security.approval", ReporterRegistrationID: required.RegistrationID, Requirement: PrerequisiteRequired},
				{DependencyKey: "change.window", ReporterRegistrationID: optional.RegistrationID, Requirement: PrerequisiteOptional},
			}})
	if err != nil || sealed.DeclaredCount != 2 {
		t.Fatalf("seal mixed=%+v err=%v", sealed, err)
	}
	expiresAt := f.now.Add(time.Hour).Format(time.RFC3339Nano)
	owner, err := f.service.CreateHandoff(ctx, f.operator, f.deliveryKey, "create-owner-mixed",
		CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, ReporterRegistrationID: f.registrationID, ExpiresAt: expiresAt})
	if err != nil {
		t.Fatal(err)
	}
	dependency, err := f.service.CreateHandoff(ctx, f.operator, f.deliveryKey, "create-janus-required",
		CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, ReporterRegistrationID: required.RegistrationID, ExpiresAt: expiresAt})
	if err != nil {
		t.Fatal(err)
	}
	ownerSecret, err := f.service.Mint(ctx, f.operator, owner.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	dependencySecret, err := f.service.Mint(ctx, f.operator, dependency.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	observedAt := f.now.Format(time.RFC3339Nano)
	if _, err := f.service.Accept(ctx, f.reporter, owner.HandoffID, "accept-owner-mixed", ownerSecret,
		AcceptRequest{Sequence: 1, ObservedAt: observedAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Accept(ctx, janusPrincipal, dependency.HandoffID, "accept-janus-required", dependencySecret,
		AcceptRequest{Sequence: 1, ObservedAt: observedAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Report(ctx, f.reporter, owner.HandoffID, "active-owner-mixed", ownerSecret,
		ReportRequest{Sequence: 2, State: HandoffStateActive, ObservedAt: observedAt}); err != nil {
		t.Fatal(err)
	}
	if got := f.wakes.Load(); got != 1 {
		t.Fatalf("active owner wake count=%d", got)
	}
	if _, err := f.service.Report(ctx, janusPrincipal, dependency.HandoffID, "active-janus-required", dependencySecret,
		ReportRequest{Sequence: 2, State: HandoffStateActive, ObservedAt: observedAt}); err != nil {
		t.Fatal(err)
	}
	if got := f.wakes.Load(); got != 2 {
		t.Fatalf("active owner/dependency wakes=%d", got)
	}
	f.now = f.now.Add(time.Second)
	observedAt = f.now.Format(time.RFC3339Nano)
	ownerTerminal := ReportRequest{Sequence: 3, State: HandoffStateSucceeded, ObservedAt: observedAt,
		PharosEvidence: &PharosEvidence{Kind: EvidenceKindDeployment, Workflow: "deploy-production", Environment: "production",
			Artifact: ArtifactEvidence{Version: "v2.0.0", Digest: "sha256:" + fmt.Sprintf("%064x", 812), CommitDigest: fmt.Sprintf("%040x", 812)},
			Result:   EvidenceResultSucceeded, ObservedAt: observedAt}}
	var reportsBefore, auditsBefore, changesBefore int
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM external_stage_report_events`).Scan(&reportsBefore); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM external_stage_audit_events`).Scan(&auditsBefore); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log WHERE delivery_id=?`, f.deliveryID).Scan(&changesBefore); err != nil {
		t.Fatal(err)
	}
	wakesBefore := f.wakes.Load()
	if _, err := f.service.Report(ctx, f.reporter, owner.HandoffID, "owner-before-required", ownerSecret, ownerTerminal); !errors.Is(err, ErrConflict) {
		t.Fatalf("owner passed unsatisfied required dependency: %v", err)
	}
	var reportsAfter, auditsAfter, changesAfter int
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM external_stage_report_events`).Scan(&reportsAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM external_stage_audit_events`).Scan(&auditsAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log WHERE delivery_id=?`, f.deliveryID).Scan(&changesAfter); err != nil {
		t.Fatal(err)
	}
	if reportsAfter != reportsBefore || auditsAfter != auditsBefore || changesAfter != changesBefore || f.wakes.Load() != wakesBefore {
		t.Fatalf("rejected owner report leaked effects: reports %d/%d audits %d/%d changes %d/%d wakes %d/%d",
			reportsBefore, reportsAfter, auditsBefore, auditsAfter, changesBefore, changesAfter, wakesBefore, f.wakes.Load())
	}
	authorized := true
	dependencyTerminal := ReportRequest{Sequence: 3, State: HandoffStateSucceeded, ObservedAt: observedAt,
		JanusEvidence: &JanusEvidence{Kind: EvidenceKindAuthorization, Result: EvidenceResultSatisfied,
			Authorized: &authorized, ObservedAt: observedAt}}
	type reportResult struct{ err error }
	start := make(chan struct{})
	dependencyResult, ownerResult := make(chan reportResult, 1), make(chan reportResult, 1)
	go func() {
		<-start
		_, reportErr := f.service.Report(ctx, janusPrincipal, dependency.HandoffID, "succeed-janus-required", dependencySecret, dependencyTerminal)
		dependencyResult <- reportResult{err: reportErr}
	}()
	go func() {
		<-start
		_, reportErr := f.service.Report(ctx, f.reporter, owner.HandoffID, "succeed-owner-mixed", ownerSecret, ownerTerminal)
		ownerResult <- reportResult{err: reportErr}
	}()
	close(start)
	dependencyErr, ownerErr := (<-dependencyResult).err, (<-ownerResult).err
	if dependencyErr != nil {
		t.Fatalf("dependency terminal report failed: %v", dependencyErr)
	}
	if ownerErr != nil {
		if !errors.Is(ownerErr, ErrConflict) {
			t.Fatalf("concurrent owner report failed unexpectedly: %v", ownerErr)
		}
		if _, err := f.service.Report(ctx, f.reporter, owner.HandoffID, "succeed-owner-mixed", ownerSecret, ownerTerminal); err != nil {
			t.Fatalf("owner retry after dependency commit: %v", err)
		}
	}
	var dependencyState, ownerState, canonicalState string
	if err := f.database.QueryRow(`SELECT lifecycle_state FROM external_stage_dependency_latest
		WHERE attempt_id=? AND stage_key='deployment' AND dependency_key='security.approval'`, f.attemptID).Scan(&dependencyState); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT lifecycle_state FROM external_stage_owner_latest
		WHERE attempt_id=? AND stage_key='deployment'`, f.attemptID).Scan(&ownerState); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT event.semantic_state FROM delivery_stage_latest latest
		JOIN delivery_stage_events event ON event.id=latest.semantic_stage_event_id
		WHERE latest.attempt_id=? AND latest.stage_key='deployment'`, f.attemptID).Scan(&canonicalState); err != nil {
		t.Fatal(err)
	}
	var janusCanonical, finalChanges int
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM delivery_stage_events WHERE reporter_id=?`, required.ReporterID).Scan(&janusCanonical); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log WHERE delivery_id=?`, f.deliveryID).Scan(&finalChanges); err != nil {
		t.Fatal(err)
	}
	if dependencyState != "succeeded" || ownerState != "succeeded" || canonicalState != "succeeded" || janusCanonical != 0 {
		t.Fatalf("stream isolation dependency=%q owner=%q canonical=%q janus_canonical=%d", dependencyState, ownerState, canonicalState, janusCanonical)
	}
	if finalChanges-changesBefore != 2 || f.wakes.Load()-wakesBefore != 2 {
		t.Fatalf("terminal effects not one-per-commit: changes=%d wakes=%d", finalChanges-changesBefore, f.wakes.Load()-wakesBefore)
	}
	revoked, err := f.service.RevokeReporter(ctx, f.operator, f.deliveryKey, "revoke-janus-required", required.RegistrationID)
	if err != nil || revoked.RevokedAt == "" {
		t.Fatalf("revoke Janus registration=%+v err=%v", revoked, err)
	}
	revokedReplay, err := f.service.RevokeReporter(ctx, f.operator, f.deliveryKey, "revoke-janus-required", required.RegistrationID)
	if err != nil || revokedReplay.RevokedAt != revoked.RevokedAt {
		t.Fatalf("revoke replay=%+v err=%v", revokedReplay, err)
	}
	if _, err := f.service.RevokeReporter(ctx, f.operator, f.deliveryKey, "revoke-janus-required", optional.RegistrationID); !errors.Is(err, ErrConflict) {
		t.Fatalf("revoke conflicting replay err=%v", err)
	}
	replacement, err := f.service.RegisterReporter(ctx, f.operator, f.deliveryKey, "reregister-janus-required",
		RegisterReporterRequest{APIKeyID: janusAPIKeyID, ReporterClass: ReporterClassJanus,
			ReporterRole: ReporterRoleDependency, DependencyKey: "security.approval"})
	if err != nil || replacement.RegistrationID == required.RegistrationID {
		t.Fatalf("re-register exact revoked Janus binding=%+v err=%v", replacement, err)
	}
}

func TestActiveReporterStalenessUsesServerReceiptAndHeartbeatRecovers(t *testing.T) {
	f := setupServiceFixture(t)
	f.sealEmpty(t)
	ctx := context.Background()
	handoff, err := f.service.CreateHandoff(ctx, f.operator, f.deliveryKey, "create-stale-owner",
		CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, ReporterRegistrationID: f.registrationID,
			ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := f.service.Mint(ctx, f.operator, handoff.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Accept(ctx, f.reporter, handoff.HandoffID, "accept-stale-owner", secret,
		AcceptRequest{Sequence: 1, ObservedAt: f.now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Report(ctx, f.reporter, handoff.HandoffID, "active-stale-owner", secret,
		ReportRequest{Sequence: 2, State: HandoffStateActive, ObservedAt: f.now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	staleService, err := NewService(f.database, Options{FixtureDigest: contracts.ExternalStageV1FixtureDigest(),
		Clock: clockFunc(func() time.Time { return f.now }), ActiveStaleAfter: 50 * time.Millisecond, Observer: func(context.Context, delivery.ChangeHint) { f.wakes.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	f.now = f.now.Add(time.Second)
	terminal := ReportRequest{Sequence: 3, State: HandoffStateSucceeded, ObservedAt: f.now.Format(time.RFC3339Nano),
		PharosEvidence: &PharosEvidence{Kind: EvidenceKindDeployment, Workflow: "deploy-production", Environment: "production",
			Artifact: ArtifactEvidence{Version: "v3.0.0", Digest: "sha256:" + fmt.Sprintf("%064x", 813), CommitDigest: fmt.Sprintf("%040x", 813)},
			Result:   EvidenceResultSucceeded, ObservedAt: f.now.Format(time.RFC3339Nano)}}
	wakesBefore := f.wakes.Load()
	if _, err := staleService.Report(ctx, f.reporter, handoff.HandoffID, "stale-terminal", secret, terminal); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale active reporter completed: %v", err)
	}
	if f.wakes.Load() != wakesBefore {
		t.Fatalf("stale completion emitted wake: %d/%d", wakesBefore, f.wakes.Load())
	}
	oldObservedAt := f.now.Add(-24 * time.Hour).Format(time.RFC3339Nano)
	heartbeat, err := staleService.Report(ctx, f.reporter, handoff.HandoffID, "recover-heartbeat", secret,
		ReportRequest{Sequence: 3, State: HandoffStateActive, ObservedAt: oldObservedAt, Heartbeat: true})
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.ServerReceivedAt == oldObservedAt {
		t.Fatal("reporter timestamp became the liveness clock")
	}
	terminal.Sequence = 4
	completed, err := staleService.Report(ctx, f.reporter, handoff.HandoffID, "fresh-terminal", secret, terminal)
	if err != nil || completed.State != HandoffStateSucceeded {
		t.Fatalf("fresh heartbeat did not restore completion: %+v err=%v", completed, err)
	}
}

func TestUnmintedHandoffCanBeRevokedAndExactlyReplayed(t *testing.T) {
	f := setupServiceFixture(t)
	f.sealEmpty(t)
	ctx := context.Background()
	handoff, err := f.service.CreateHandoff(ctx, f.operator, f.deliveryKey, "create-unminted-revoke",
		CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, ReporterRegistrationID: f.registrationID,
			ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := f.service.Revoke(ctx, f.operator, handoff.HandoffID, "revoke-unminted", 0)
	if err != nil || revoked.RevokedAt == "" || revoked.CredentialEpoch != 0 {
		t.Fatalf("revoke unminted=%+v err=%v", revoked, err)
	}
	replay, err := f.service.Revoke(ctx, f.operator, handoff.HandoffID, "revoke-unminted", 0)
	if err != nil || replay.RevokedAt != revoked.RevokedAt || replay.CredentialEpoch != 0 {
		t.Fatalf("revoke unminted replay=%+v err=%v", replay, err)
	}
	if _, err := f.service.Revoke(ctx, f.operator, handoff.HandoffID, "revoke-unminted", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("revoke conflicting epoch replay err=%v", err)
	}
	if _, err := f.service.Mint(ctx, f.operator, handoff.HandoffID, 0, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("revoked unminted handoff minted: %v", err)
	}
}

func TestValidateJanusBlockedRequiresTypedEvidence(t *testing.T) {
	handoff := handoffRow{class: string(ReporterClassJanus), role: string(ReporterRoleDependency), allowAuthorization: true}
	err := validateSemanticReport(handoff, ReportRequest{Sequence: 2, State: HandoffStateBlocked,
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), BlockerCodes: []BlockerCode{BlockerDependencyPending}}, nil)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("blocked Janus without evidence err=%v", err)
	}
}

func TestServiceRejectsUnknownStageBeforeLookup(t *testing.T) {
	f := setupServiceFixture(t)
	if _, err := f.service.CreateHandoff(context.Background(), f.operator, f.deliveryKey, "bad-stage",
		CreateHandoffRequest{StageKey: "release", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, ReporterRegistrationID: f.registrationID,
			ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create invalid stage err=%v", err)
	}
	if _, err := f.service.SealPrerequisites(context.Background(), f.operator, f.deliveryKey, "bad-seal-stage",
		SealPrerequisitesRequest{StageKey: "release", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, Prerequisites: []Prerequisite{}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("seal invalid stage err=%v", err)
	}
}

func TestReporterStageOwnershipIsClosed(t *testing.T) {
	for _, stage := range []string{"specification", "implementation", "qa"} {
		if validReporterStage(ReporterRoleOwner, stage) {
			t.Fatalf("Pharos owner unexpectedly permitted on %s", stage)
		}
	}
	for _, stage := range []string{"deployment", "verification"} {
		if !validReporterStage(ReporterRoleOwner, stage) {
			t.Fatalf("Pharos owner rejected on %s", stage)
		}
	}
	if !validReporterStage(ReporterRoleDependency, "qa") {
		t.Fatal("declared Janus dependency unexpectedly rejected on qa")
	}
}

func TestMintScrubsPartialRandomReadOnError(t *testing.T) {
	f := setupServiceFixture(t)
	f.sealEmpty(t)
	metadata, err := f.service.CreateHandoff(context.Background(), f.operator, f.deliveryKey, "create-partial-rng",
		CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, ReporterRegistrationID: f.registrationID,
			ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	reader := &partialSecretReader{}
	f.service.random = reader
	if _, err := f.service.Mint(context.Background(), f.operator, metadata.HandoffID, 0, false); err == nil {
		t.Fatal("partial random read unexpectedly minted")
	}
	if len(reader.target) != OneTimeSecretBytes || !bytes.Equal(reader.target, make([]byte, OneTimeSecretBytes)) {
		t.Fatalf("partial random buffer was not scrubbed: %x", reader.target)
	}
	var epoch int64
	var digest []byte
	if err := f.database.QueryRow(`SELECT credential_epoch,secret_digest FROM external_stage_handoffs WHERE handoff_id=?`, metadata.HandoffID).Scan(&epoch, &digest); err != nil {
		t.Fatal(err)
	}
	if epoch != 0 || digest != nil {
		t.Fatalf("failed mint persisted epoch=%d digest=%x", epoch, digest)
	}
}

func TestConcurrentCreateCommitsOneHandoffAndOneReplay(t *testing.T) {
	f := setupServiceFixture(t)
	f.sealEmpty(t)
	request := CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
		ExpectedAuthorityEpoch: 1, ReporterRegistrationID: f.registrationID,
		ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)}
	type result struct {
		metadata CreateHandoffResult
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			metadata, err := f.service.CreateHandoff(context.Background(), f.operator, f.deliveryKey, "concurrent-create", request)
			results <- result{metadata: metadata, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.metadata.HandoffID == "" || first.metadata.HandoffID != second.metadata.HandoffID || first.metadata.Duplicate == second.metadata.Duplicate {
		t.Fatalf("concurrent results first=%+v second=%+v", first, second)
	}
	var handoffs, operations int
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM external_stage_handoffs`).Scan(&handoffs); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM external_stage_operation_events WHERE operation_kind='created'`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if handoffs != 1 || operations != 1 {
		t.Fatalf("concurrent create persisted handoffs=%d operations=%d", handoffs, operations)
	}
}

func createAcceptedServiceHandoff(t *testing.T, f *serviceFixture, registrationID int64, reporter Principal, key string, expiresAt time.Time) (CreateHandoffResult, []byte) {
	t.Helper()
	handoff, err := f.service.CreateHandoff(t.Context(), f.operator, f.deliveryKey, "create-"+key,
		CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, ReporterRegistrationID: registrationID,
			ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := f.service.Mint(t.Context(), f.operator, handoff.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Accept(t.Context(), reporter, handoff.HandoffID, "accept-"+key, secret,
		AcceptRequest{Sequence: 1, ObservedAt: f.now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	return handoff, secret
}

func registerRequiredServiceDependency(t *testing.T, f *serviceFixture, dependencyKey, idempotency string) ReporterRegistration {
	t.Helper()
	registration, err := f.service.RegisterReporter(t.Context(), f.operator, f.deliveryKey, "register-"+idempotency,
		RegisterReporterRequest{APIKeyID: f.reporter.APIKeyID, ReporterClass: ReporterClassJanus,
			ReporterRole: ReporterRoleDependency, DependencyKey: dependencyKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SealPrerequisites(t.Context(), f.operator, f.deliveryKey, "seal-"+idempotency,
		SealPrerequisitesRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, Prerequisites: []Prerequisite{{DependencyKey: dependencyKey,
				ReporterRegistrationID: registration.RegistrationID, Requirement: PrerequisiteRequired}}}); err != nil {
		t.Fatal(err)
	}
	return registration
}

func TestInternalAPIKeyOperatorLifecycleAndReplayAuditsExactPrincipal(t *testing.T) {
	f := setupServiceFixture(t)
	f.sealEmpty(t)
	var projectID int64
	if err := f.database.QueryRow(`SELECT project_id FROM issues WHERE id=?`, f.issueID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	result, err := f.database.Exec(`INSERT INTO users(username,password,role,status) VALUES('external-stage-api-operator','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	operatorID, _ := result.LastInsertId()
	if _, err := f.database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'editor')`, projectID, operatorID); err != nil {
		t.Fatal(err)
	}
	result, err = f.database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'external-stage-api-operator',?,'paimos_external_operator','agent-controls:write')`, operatorID, fmt.Sprintf("%064d", 819))
	if err != nil {
		t.Fatal(err)
	}
	apiKeyID, _ := result.LastInsertId()
	operator := Principal{UserID: operatorID, Kind: "api_key", APIKeyID: apiKeyID}
	request := CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
		ExpectedAuthorityEpoch: 1, ReporterRegistrationID: f.registrationID,
		ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)}
	handoff, err := f.service.CreateHandoff(t.Context(), operator, f.deliveryKey, "api-operator-create", request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.service.CreateHandoff(t.Context(), operator, f.deliveryKey, "api-operator-create", request)
	if err != nil || !replay.Duplicate || replay.HandoffID != handoff.HandoffID {
		t.Fatalf("create replay=%+v err=%v", replay, err)
	}
	if _, err := f.service.Mint(t.Context(), operator, handoff.HandoffID, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Mint(t.Context(), operator, handoff.HandoffID, 1, true); err != nil {
		t.Fatal(err)
	}
	revoked, err := f.service.Revoke(t.Context(), operator, handoff.HandoffID, "api-operator-revoke", 2)
	if err != nil || revoked.RevokedAt == "" {
		t.Fatalf("revoke=%+v err=%v", revoked, err)
	}
	revokedReplay, err := f.service.Revoke(t.Context(), operator, handoff.HandoffID, "api-operator-revoke", 2)
	if err != nil || revokedReplay.RevokedAt != revoked.RevokedAt {
		t.Fatalf("revoke replay=%+v err=%v", revokedReplay, err)
	}
	var exactAudits int
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM external_stage_operation_events operation
		JOIN external_stage_audit_events audit ON audit.operation_event_id=operation.id
		WHERE operation.handoff_row_id=(SELECT id FROM external_stage_handoffs WHERE handoff_id=?)
		 AND operation.operation_kind IN ('created','secret_minted','secret_rotated','revoked')
		 AND operation.actor_principal_kind='api_key' AND operation.actor_api_key_id=? AND audit.api_key_id=?`,
		handoff.HandoffID, apiKeyID, apiKeyID).Scan(&exactAudits); err != nil {
		t.Fatal(err)
	}
	if exactAudits != 4 {
		t.Fatalf("exact API-key operation audits=%d", exactAudits)
	}
}

func TestReplacementHandoffsResetWireSequenceButAdvanceStreams(t *testing.T) {
	t.Run("owner", func(t *testing.T) {
		f := setupServiceFixture(t)
		f.sealEmpty(t)
		old, oldSecret := createAcceptedServiceHandoff(t, f, f.registrationID, f.reporter, "old-owner", f.now.Add(time.Hour))
		if _, err := f.service.Report(t.Context(), f.reporter, old.HandoffID, "old-owner-active", oldSecret,
			ReportRequest{Sequence: 2, State: HandoffStateActive, ObservedAt: f.now.Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.Revoke(t.Context(), f.operator, old.HandoffID, "revoke-old-owner", 1); err != nil {
			t.Fatal(err)
		}
		replacement, secret := createAcceptedServiceHandoff(t, f, f.registrationID, f.reporter, "replacement-owner", f.now.Add(time.Hour))
		receipt, err := f.service.Report(t.Context(), f.reporter, replacement.HandoffID, "replacement-owner-waiting", secret,
			ReportRequest{Sequence: 2, State: HandoffStateWaiting, ObservedAt: f.now.Format(time.RFC3339Nano)})
		if err != nil || receipt.Sequence != 2 {
			t.Fatalf("replacement owner report=%+v err=%v", receipt, err)
		}
		var wireMin, wireMax, streamMin, streamMax, canonicalMin, canonicalMax int64
		if err := f.database.QueryRow(`SELECT MIN(sequence),MAX(sequence),MIN(stream_sequence),MAX(stream_sequence)
			FROM external_stage_owner_events WHERE attempt_id=? AND stage_key='deployment'`, f.attemptID).
			Scan(&wireMin, &wireMax, &streamMin, &streamMax); err != nil {
			t.Fatal(err)
		}
		if err := f.database.QueryRow(`SELECT MIN(source_sequence),MAX(source_sequence) FROM delivery_stage_events
			WHERE attempt_id=? AND stage_key='deployment' AND event_type='semantic_report'`, f.attemptID).
			Scan(&canonicalMin, &canonicalMax); err != nil {
			t.Fatal(err)
		}
		if wireMin != 2 || wireMax != 2 || streamMin != 1 || streamMax != 2 || canonicalMin != 1 || canonicalMax != 2 {
			t.Fatalf("owner wire=%d..%d stream=%d..%d canonical=%d..%d", wireMin, wireMax, streamMin, streamMax, canonicalMin, canonicalMax)
		}
	})

	t.Run("dependency", func(t *testing.T) {
		f := setupServiceFixture(t)
		registration := registerRequiredServiceDependency(t, f, "security.approval", "replacement-dependency")
		old, oldSecret := createAcceptedServiceHandoff(t, f, registration.RegistrationID, f.reporter, "old-dependency", f.now.Add(time.Hour))
		if _, err := f.service.Report(t.Context(), f.reporter, old.HandoffID, "old-dependency-active", oldSecret,
			ReportRequest{Sequence: 2, State: HandoffStateActive, ObservedAt: f.now.Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.Revoke(t.Context(), f.operator, old.HandoffID, "revoke-old-dependency", 1); err != nil {
			t.Fatal(err)
		}
		replacement, secret := createAcceptedServiceHandoff(t, f, registration.RegistrationID, f.reporter, "replacement-dependency", f.now.Add(time.Hour))
		if _, err := f.service.Report(t.Context(), f.reporter, replacement.HandoffID, "replacement-dependency-active", secret,
			ReportRequest{Sequence: 2, State: HandoffStateActive, ObservedAt: f.now.Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
		ready := true
		f.now = f.now.Add(time.Second)
		terminal := ReportRequest{Sequence: 3, State: HandoffStateSucceeded, ObservedAt: f.now.Format(time.RFC3339Nano),
			JanusEvidence: &JanusEvidence{Kind: EvidenceKindCredentialHandoff, Result: EvidenceResultSatisfied,
				CredentialReady: &ready, ObservedAt: f.now.Format(time.RFC3339Nano)}}
		if _, err := f.service.Report(t.Context(), f.reporter, replacement.HandoffID, "replacement-dependency-succeeded", secret, terminal); err != nil {
			t.Fatal(err)
		}
		var wire, stream int64
		if err := f.database.QueryRow(`SELECT sequence,stream_sequence FROM external_stage_dependency_latest
			WHERE attempt_id=? AND stage_key='deployment' AND dependency_key='security.approval'`, f.attemptID).Scan(&wire, &stream); err != nil {
			t.Fatal(err)
		}
		if wire != 3 || stream != 3 {
			t.Fatalf("dependency latest wire=%d stream=%d", wire, stream)
		}
	})
}

func TestRequiredDependencySuccessRemainsSatisfiedAfterBindingCloses(t *testing.T) {
	test := func(t *testing.T, closeBinding func(*serviceFixture, ReporterRegistration, CreateHandoffResult)) {
		f := setupServiceFixture(t)
		registration := registerRequiredServiceDependency(t, f, "release.approval", t.Name())
		owner, ownerSecret := createAcceptedServiceHandoff(t, f, f.registrationID, f.reporter, t.Name()+"-owner", f.now.Add(time.Hour))
		dependency, dependencySecret := createAcceptedServiceHandoff(t, f, registration.RegistrationID, f.reporter,
			t.Name()+"-dependency", f.now.Add(time.Minute))
		observed := f.now.Format(time.RFC3339Nano)
		if _, err := f.service.Report(t.Context(), f.reporter, owner.HandoffID, t.Name()+"-owner-active", ownerSecret,
			ReportRequest{Sequence: 2, State: HandoffStateActive, ObservedAt: observed}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.Report(t.Context(), f.reporter, dependency.HandoffID, t.Name()+"-dependency-active", dependencySecret,
			ReportRequest{Sequence: 2, State: HandoffStateActive, ObservedAt: observed}); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Second)
		authorized := true
		if _, err := f.service.Report(t.Context(), f.reporter, dependency.HandoffID, t.Name()+"-dependency-succeeded", dependencySecret,
			ReportRequest{Sequence: 3, State: HandoffStateSucceeded, ObservedAt: f.now.Format(time.RFC3339Nano),
				JanusEvidence: &JanusEvidence{Kind: EvidenceKindAuthorization, Result: EvidenceResultSatisfied,
					Authorized: &authorized, ObservedAt: f.now.Format(time.RFC3339Nano)}}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.CreateHandoff(t.Context(), f.operator, f.deliveryKey, t.Name()+"-replace-satisfied",
			CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
				ExpectedAuthorityEpoch: 1, ReporterRegistrationID: registration.RegistrationID,
				ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)}); !errors.Is(err, ErrConflict) {
			t.Fatalf("replaced absorbing succeeded dependency: %v", err)
		}
		closeBinding(f, registration, dependency)
		ownerEvidence := &PharosEvidence{Kind: EvidenceKindDeployment, Workflow: "deploy-production", Environment: "production",
			Artifact: ArtifactEvidence{Version: "v4.0.0", Digest: "sha256:" + fmt.Sprintf("%064x", 820), CommitDigest: fmt.Sprintf("%040x", 820)},
			Result:   EvidenceResultSucceeded, ObservedAt: f.now.Format(time.RFC3339Nano)}
		receipt, err := f.service.Report(t.Context(), f.reporter, owner.HandoffID, t.Name()+"-owner-succeeded", ownerSecret,
			ReportRequest{Sequence: 3, State: HandoffStateSucceeded, ObservedAt: f.now.Format(time.RFC3339Nano), PharosEvidence: ownerEvidence})
		if err != nil || receipt.State != HandoffStateSucceeded {
			t.Fatalf("owner after immutable dependency success=%+v err=%v", receipt, err)
		}
	}

	t.Run("expired_handoff", func(t *testing.T) {
		test(t, func(f *serviceFixture, _ ReporterRegistration, dependency CreateHandoffResult) {
			f.now = f.now.Add(2 * time.Minute)
			expires, err := time.Parse(time.RFC3339Nano, dependency.ExpiresAt)
			if err != nil || !expires.Before(f.now) {
				t.Fatalf("dependency did not expire under service clock: expires=%s now=%s err=%v", dependency.ExpiresAt, f.now, err)
			}
		})
	})

	t.Run("revoked_registration", func(t *testing.T) {
		test(t, func(f *serviceFixture, registration ReporterRegistration, dependency CreateHandoffResult) {
			revoked, err := f.service.RevokeReporter(t.Context(), f.operator, f.deliveryKey,
				"revoke-satisfied-registration", registration.RegistrationID)
			if err != nil || revoked.RevokedAt == "" {
				t.Fatalf("revoke satisfied registration=%+v err=%v", revoked, err)
			}
			if _, err := f.service.CreateHandoff(t.Context(), f.operator, f.deliveryKey, "replace-satisfied-dependency",
				CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
					ExpectedAuthorityEpoch: 1, ReporterRegistrationID: registration.RegistrationID,
					ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)}); err == nil {
				t.Fatalf("replaced absorbing succeeded dependency %s", dependency.HandoffID)
			}
		})
	})
}

func TestRevokedUnsatisfiedRequiredDependencyStillBlocksOwner(t *testing.T) {
	f := setupServiceFixture(t)
	registration := registerRequiredServiceDependency(t, f, "release.approval", "revoked-unsatisfied")
	owner, secret := createAcceptedServiceHandoff(t, f, f.registrationID, f.reporter, "revoked-unsatisfied-owner", f.now.Add(time.Hour))
	if _, err := f.service.Report(t.Context(), f.reporter, owner.HandoffID, "revoked-unsatisfied-owner-active", secret,
		ReportRequest{Sequence: 2, State: HandoffStateActive, ObservedAt: f.now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RevokeReporter(t.Context(), f.operator, f.deliveryKey, "revoke-unsatisfied-registration", registration.RegistrationID); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Second)
	evidence := &PharosEvidence{Kind: EvidenceKindDeployment, Workflow: "deploy-production", Environment: "production",
		Artifact: ArtifactEvidence{Version: "v4.1.0", Digest: "sha256:" + fmt.Sprintf("%064x", 821), CommitDigest: fmt.Sprintf("%040x", 821)},
		Result:   EvidenceResultSucceeded, ObservedAt: f.now.Format(time.RFC3339Nano)}
	if _, err := f.service.Report(t.Context(), f.reporter, owner.HandoffID, "owner-with-revoked-unsatisfied", secret,
		ReportRequest{Sequence: 3, State: HandoffStateSucceeded, ObservedAt: f.now.Format(time.RFC3339Nano), PharosEvidence: evidence}); !errors.Is(err, ErrConflict) {
		t.Fatalf("owner passed revoked unsatisfied dependency: %v", err)
	}
}

func TestExternalEffectiveRoleCannotRegisterOrUseReporterBinding(t *testing.T) {
	f := setupServiceFixture(t)
	var projectID int64
	if err := f.database.QueryRow(`SELECT project_id FROM issues WHERE id=?`, f.issueID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	result, err := f.database.Exec(`INSERT INTO users(username,password,role,role_key,status)
		VALUES('portal-external-reporter','x','member','external','active')`)
	if err != nil {
		t.Fatal(err)
	}
	externalUserID, _ := result.LastInsertId()
	if _, err := f.database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'editor')`, projectID, externalUserID); err != nil {
		t.Fatal(err)
	}
	result, err = f.database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'portal-external-reporter',?,'paimos_portal_external','*')`, externalUserID, fmt.Sprintf("%064d", 822))
	if err != nil {
		t.Fatal(err)
	}
	externalKeyID, _ := result.LastInsertId()
	if _, err := f.service.RegisterReporter(t.Context(), f.operator, f.deliveryKey, "register-portal-external",
		RegisterReporterRequest{APIKeyID: externalKeyID, ReporterClass: ReporterClassJanus,
			ReporterRole: ReporterRoleDependency, DependencyKey: "portal.external"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("registered effective external reporter: %v", err)
	}
	f.sealEmpty(t)
	handoff, err := f.service.CreateHandoff(t.Context(), f.operator, f.deliveryKey, "create-before-role-drift",
		CreateHandoffRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1,
			ExpectedAuthorityEpoch: 1, ReporterRegistrationID: f.registrationID,
			ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := f.service.Mint(t.Context(), f.operator, handoff.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec(`UPDATE users SET role_key='external' WHERE id=?`, f.reporter.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Pull(t.Context(), f.reporter, handoff.HandoffID, secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("external role pulled handoff: %v", err)
	}
	if _, err := f.service.Accept(t.Context(), f.reporter, handoff.HandoffID, "accept-after-role-drift", secret,
		AcceptRequest{Sequence: 1, ObservedAt: f.now.Format(time.RFC3339Nano)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("external role accepted handoff: %v", err)
	}
}

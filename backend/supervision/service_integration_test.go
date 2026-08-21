// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	storedb "github.com/inspr-at/paimos/backend/db"
)

const testSessionCredential = "12345678-1234-4234-9234-123456789abc"

func openSupervisionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	priorDataDir, priorTestMode := os.Getenv("DATA_DIR"), os.Getenv("PAIMOS_TEST_MODE")
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := storedb.Open(); err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	database := storedb.DB
	t.Cleanup(func() {
		_ = database.Close()
		storedb.DB = nil
		_ = os.Setenv("DATA_DIR", priorDataDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", priorTestMode)
	})
	return database
}

func seedGrantTarget(t *testing.T, database *sql.DB) (deliveryID, userID int64, principal auth.Principal) {
	t.Helper()
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Supervision','SUP')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,title) VALUES(?,1,'Control root')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	delivery, err := database.Exec(`INSERT INTO deliveries(issue_id,delivery_key,project_id_hint,created_at,updated_at)
		VALUES(?, ?, ?, '2026-08-20T10:00:00Z','2026-08-20T10:00:00Z')`, issueID, "issue:1", projectID)
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, _ = delivery.LastInsertId()
	reporter, err := database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
		VALUES(?,'system','supervision-test','2026-08-20T10:00:00Z')`, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	reporterID, _ := reporter.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,1,'test-attempt',zeroblob(32),'attempt_started',?,'2026-08-20T10:00:00Z')`, deliveryID, reporterID); err != nil {
		t.Fatal(err)
	}
	user, err := database.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('supervision-human','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ = user.LastInsertId()
	if _, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('supervision-bearer',?,'2030-12-01 00:00:00','2026-08-01 00:00:00',?)`,
		userID, testSessionCredential); err != nil {
		t.Fatal(err)
	}
	principal, err = auth.NewSessionPrincipal(testSessionCredential, userID, userID, false)
	if err != nil {
		t.Fatal(err)
	}
	return deliveryID, userID, principal
}

func TestGrantReplayAndCompetingRevocationUseM147Truth(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, _, principal := seedGrantTarget(t, database)
	ids := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	service := NewService(database, Options{Clock: ClockFunc(time.Now), IDs: IDSourceFunc(func() string {
		return ids[0]
	})})
	key := sha256.Sum256([]byte("validated-operation-key"))
	grant, err := service.IssueActorGrant(context.Background(), principal, GrantIssueRequest{
		DeliveryID: deliveryID, OperationKeyDigest: key,
	})
	if err != nil {
		t.Fatalf("issue grant: %v (%s)", err, ErrorCode(err))
	}
	replayed, err := service.IssueActorGrant(context.Background(), principal, GrantIssueRequest{
		DeliveryID: deliveryID, OperationKeyDigest: key,
	})
	if err != nil {
		t.Fatalf("replay grant: %v (%s)", err, ErrorCode(err))
	}
	if replayed.GrantID != grant.GrantID || replayed.Revision != grant.Revision || !replayed.ExpiresAt.Equal(grant.ExpiresAt) {
		t.Fatalf("replay changed projection: first=%+v replay=%+v", grant, replayed)
	}
	var operationCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_operation_keys WHERE operation_kind='grant.put'`).Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	if operationCount != 1 {
		t.Fatalf("grant replay wrote %d operation rows, want 1", operationCount)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, label := range []string{"revoker-a", "revoker-b"} {
		wait.Add(1)
		go func(label string) {
			defer wait.Done()
			<-start
			digest := sha256.Sum256([]byte(label))
			_, err := service.RevokeActorGrant(context.Background(), principal, GrantRevokeRequest{
				GrantID: grant.GrantID, Revision: grant.Revision, OperationKeyDigest: digest,
			})
			results <- err
		}(label)
	}
	close(start)
	wait.Wait()
	close(results)
	successes, losers := 0, 0
	for result := range results {
		if result == nil {
			successes++
		} else if errors.Is(result, ErrNotFound) || errors.Is(result, ErrConflict) {
			losers++
		} else {
			t.Fatalf("unexpected competing terminalization error: %v (%s)", result, ErrorCode(result))
		}
	}
	if successes != 1 || losers != 1 {
		t.Fatalf("competing terminalization outcomes: successes=%d losers=%d", successes, losers)
	}
	var revokeEvents, revokeOperations int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_events WHERE grant_id=? AND event_kind='grant_revoked'`, grant.GrantID).Scan(&revokeEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_operation_keys WHERE operation_kind='grant.revoke'`).Scan(&revokeOperations); err != nil {
		t.Fatal(err)
	}
	if revokeEvents != 1 || revokeOperations != 1 {
		t.Fatalf("terminal fact was not singular: events=%d operations=%d", revokeEvents, revokeOperations)
	}
	if violations, err := foreignKeyViolations(database); err != nil || violations != 0 {
		t.Fatalf("foreign key check: violations=%d err=%v database=%s", violations, err, filepath.Base(os.Getenv("DATA_DIR")))
	}
}

type priorityMutator struct{ fail error }

func (mutator priorityMutator) SetIssuePriorityTx(ctx context.Context, tx *sql.Tx, mutation PriorityMutation) error {
	if mutator.fail != nil {
		return mutator.fail
	}
	result, err := tx.ExecContext(ctx, `UPDATE issues SET priority=?,updated_at=datetime('now') WHERE id=?`,
		mutation.Priority, mutation.IssueID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("issue disappeared")
	}
	return nil
}

func (priorityMutator) CancelQueuedRunTx(context.Context, *sql.Tx, RunCancellationMutation) error {
	return errors.New("unexpected queued cancellation")
}

func (priorityMutator) CancelRunningRunTx(context.Context, *sql.Tx, RunCancellationMutation) error {
	return errors.New("unexpected running cancellation")
}

type testChanges struct{ fail error }

func (changes testChanges) RecordControlChangeTx(context.Context, *sql.Tx, ControlChange) (CommitWake, error) {
	return CommitWake{ID: 1}, changes.fail
}

func (testChanges) WakeControlChange(context.Context, CommitWake) {}

func TestPriorityCommandRollsBackMutationWhenHintAppendFails(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, _, principal := seedGrantTarget(t, database)
	idIndex := 0
	ids := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb",
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc"}
	service := NewService(database, Options{IDs: IDSourceFunc(func() string {
		id := ids[idIndex]
		idIndex++
		return id
	})})
	grantKey := sha256.Sum256([]byte("grant-key"))
	grant, err := service.IssueActorGrant(context.Background(), principal, GrantIssueRequest{DeliveryID: deliveryID, OperationKeyDigest: grantKey})
	if err != nil {
		t.Fatalf("issue grant: %v (%s)", err, ErrorCode(err))
	}
	commandKey := sha256.Sum256([]byte("command-key"))
	command, err := service.CreateCommand(context.Background(), principal, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "issue.priority.set", Priority: "high", OperationKeyDigest: commandKey})
	if err != nil {
		t.Fatalf("create priority command: %v (%s)", err, ErrorCode(err))
	}
	replay, err := service.CreateCommand(context.Background(), principal, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "issue.priority.set", Priority: "high", OperationKeyDigest: commandKey})
	if err != nil || replay.CommandID != command.CommandID {
		t.Fatalf("command replay=%+v err=%v", replay, err)
	}
	convergingKey := sha256.Sum256([]byte("same-semantic-command-new-key"))
	converged, err := service.CreateCommand(context.Background(), principal, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "issue.priority.set", Priority: "high", OperationKeyDigest: convergingKey})
	if err != nil || converged.CommandID != command.CommandID {
		t.Fatalf("semantic convergence=%+v err=%v", converged, err)
	}
	var commandCount, createOperationCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_commands`).Scan(&commandCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_operation_keys WHERE operation_kind='command.create'`).
		Scan(&createOperationCount); err != nil {
		t.Fatal(err)
	}
	if commandCount != 1 || createOperationCount != 2 {
		t.Fatalf("semantic convergence rows: commands=%d operation_keys=%d", commandCount, createOperationCount)
	}
	failing := NewService(database, Options{Mutator: priorityMutator{}, Changes: testChanges{fail: errors.New("audit down")}})
	confirmKey := sha256.Sum256([]byte("confirm-key"))
	if _, err := failing.ConfirmCommand(context.Background(), principal, CommandConfirmRequest{CommandID: command.CommandID,
		StatusRevision: 1, OperationKeyDigest: confirmKey}); !errors.Is(err, ErrConflict) {
		t.Fatalf("confirm failure=%v code=%s", err, ErrorCode(err))
	}
	var priority, status string
	var statusRevision int64
	if err := database.QueryRow(`SELECT issue.priority,command.status,command.status_revision FROM control_commands command
		JOIN issues issue ON issue.id=command.root_issue_id WHERE command.command_id=?`, command.CommandID).
		Scan(&priority, &status, &statusRevision); err != nil {
		t.Fatal(err)
	}
	if priority != "medium" || status != "pending_confirmation" || statusRevision != 1 {
		t.Fatalf("failed hint append leaked mutation: priority=%s status=%s revision=%d", priority, status, statusRevision)
	}
	var acceptedEvents int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_events WHERE command_id=? AND event_kind='command_accepted'`,
		command.CommandID).Scan(&acceptedEvents); err != nil {
		t.Fatal(err)
	}
	if acceptedEvents != 0 {
		t.Fatalf("failed hint append leaked %d accepted events", acceptedEvents)
	}
}

func TestThirtyTwoConnectionCommandCreateAndConfirmConverge(t *testing.T) {
	database := openSupervisionTestDB(t)
	database.SetMaxOpenConns(32)
	deliveryID, _, principal := seedGrantTarget(t, database)
	service := NewService(database, Options{Mutator: priorityMutator{}, Changes: testChanges{}})
	grantKey := sha256.Sum256([]byte("concurrent-grant"))
	grant, err := service.IssueActorGrant(context.Background(), principal, GrantIssueRequest{
		DeliveryID: deliveryID, OperationKeyDigest: grantKey,
	})
	if err != nil {
		t.Fatalf("issue grant: %v (%s)", err, ErrorCode(err))
	}

	const contenders = 32
	start := make(chan struct{})
	created := make(chan struct {
		projection CommandProjection
		err        error
	}, contenders)
	for index := 0; index < contenders; index++ {
		go func(index int) {
			<-start
			key := sha256.Sum256([]byte("create-" + string(rune(index))))
			var projection CommandProjection
			var err error
			for attempt := 0; attempt < 8; attempt++ {
				projection, err = service.CreateCommand(context.Background(), principal, CommandCreateRequest{
					GrantID: grant.GrantID, GrantRevision: grant.Revision, Action: "issue.priority.set",
					Priority: "high", OperationKeyDigest: key,
				})
				if !IsCode(err, CodeStorageUnavailable) {
					break
				}
				time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
			}
			created <- struct {
				projection CommandProjection
				err        error
			}{projection, err}
		}(index)
	}
	close(start)
	var commandID string
	for index := 0; index < contenders; index++ {
		result := <-created
		if result.err != nil {
			t.Fatalf("create contender %d: %v (%s)", index, result.err, ErrorCode(result.err))
		}
		if commandID == "" {
			commandID = result.projection.CommandID
		} else if result.projection.CommandID != commandID {
			t.Fatalf("create diverged: %s != %s", result.projection.CommandID, commandID)
		}
	}

	start = make(chan struct{})
	confirmed := make(chan struct {
		projection CommandProjection
		err        error
	}, contenders)
	for index := 0; index < contenders; index++ {
		go func(index int) {
			<-start
			key := sha256.Sum256([]byte("confirm-" + string(rune(index))))
			var projection CommandProjection
			var err error
			for attempt := 0; attempt < 8; attempt++ {
				projection, err = service.ConfirmCommand(context.Background(), principal, CommandConfirmRequest{
					CommandID: commandID, StatusRevision: 1, OperationKeyDigest: key,
				})
				if !IsCode(err, CodeStorageUnavailable) {
					break
				}
				time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
			}
			confirmed <- struct {
				projection CommandProjection
				err        error
			}{projection, err}
		}(index)
	}
	close(start)
	for index := 0; index < contenders; index++ {
		result := <-confirmed
		if result.err != nil {
			t.Fatalf("confirm contender %d: %v (%s)", index, result.err, ErrorCode(result.err))
		}
		if result.projection.CommandID != commandID || result.projection.Status != "applied" {
			t.Fatalf("confirm diverged: %+v", result.projection)
		}
	}
	var commands, appliedEvents, createOps, confirmOps int
	queries := []struct {
		query string
		value *int
	}{
		{`SELECT COUNT(*) FROM control_commands`, &commands},
		{`SELECT COUNT(*) FROM control_events WHERE command_id=? AND event_kind='command_applied'`, &appliedEvents},
		{`SELECT COUNT(*) FROM control_operation_keys WHERE operation_kind='command.create'`, &createOps},
		{`SELECT COUNT(*) FROM control_operation_keys WHERE operation_kind='command.confirm'`, &confirmOps},
	}
	for _, item := range queries {
		var err error
		if item.query == queries[1].query {
			err = database.QueryRow(item.query, commandID).Scan(item.value)
		} else {
			err = database.QueryRow(item.query).Scan(item.value)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if commands != 1 || appliedEvents != 1 || createOps != contenders || confirmOps != contenders {
		t.Fatalf("convergence rows commands=%d applied=%d create_ops=%d confirm_ops=%d",
			commands, appliedEvents, createOps, confirmOps)
	}
}

func TestActorAPIKeyUsesExactScopeAndRechecksScopeRemoval(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, userID, _ := seedGrantTarget(t, database)
	keyRow, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'control-actor',?,'paimos_actor',?)`, userID, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", ScopeActorWrite)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := keyRow.LastInsertId()
	principal, err := auth.NewAPIKeyPrincipal(keyID, userID, auth.ScopeSet{ScopeActorWrite: {}})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(database, Options{})
	operation := sha256.Sum256([]byte("exact-scope-issue"))
	if _, err := service.IssueActorGrant(context.Background(), principal, GrantIssueRequest{
		DeliveryID: deliveryID, OperationKeyDigest: operation,
	}); err != nil {
		t.Fatalf("exact actor scope rejected: %v (%s)", err, ErrorCode(err))
	}
	if _, err := database.Exec(`UPDATE api_keys SET scopes=? WHERE id=?`, ScopeRunner, keyID); err != nil {
		t.Fatal(err)
	}
	operation = sha256.Sum256([]byte("removed-scope-issue"))
	if _, err := service.IssueActorGrant(context.Background(), principal, GrantIssueRequest{
		DeliveryID: deliveryID, OperationKeyDigest: operation,
	}); !IsCode(err, CodeScopeRevoked) {
		t.Fatalf("removed scope error=%v code=%s", err, ErrorCode(err))
	}
}

func seedRunnerActivation(t *testing.T, database *sql.DB, deliveryID, requestedBy int64) (int64, auth.Principal) {
	t.Helper()
	var issueID, projectID, reporterID, attemptEventID int64
	if err := database.QueryRow(`SELECT delivery.issue_id,issue.project_id,reporter.id,event.id
		FROM deliveries delivery JOIN issues issue ON issue.id=delivery.issue_id
		JOIN delivery_reporters reporter ON reporter.delivery_id=delivery.id
		JOIN delivery_events event ON event.delivery_id=delivery.id AND event.kind='attempt_started'
		WHERE delivery.id=?`, deliveryID).Scan(&issueID, &projectID, &reporterID, &attemptEventID); err != nil {
		t.Fatal(err)
	}
	attempt, err := database.Exec(`INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,
		start_delivery_event_id,project_id_at_start,reason_code,created_at)
		VALUES(?,1,1,?,?,'test','2026-08-20T10:00:00Z')`, deliveryID, attemptEventID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	attemptID, _ := attempt.LastInsertId()
	stages := []string{"specification", "implementation", "qa", "deployment", "verification"}
	weights := []int{10, 45, 20, 15, 10}
	for index := range stages {
		if _, err := database.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,
			sort_order,applicability,weight,created_at) VALUES(?,?,?,?,'required',?,'2026-08-20T10:00:00Z')`,
			deliveryID, attemptID, stages[index], index+1, weights[index]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO delivery_attempt_policy_seals(delivery_id,attempt_id,sealed_at)
		VALUES(?,?,'2026-08-20T10:00:00Z')`, deliveryID, attemptID); err != nil {
		t.Fatal(err)
	}
	startEnvelope, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,2,'test-stage-start',zeroblob(32),'stage_execution_started',?,'2026-08-20T10:00:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	startEnvelopeID, _ := startEnvelope.LastInsertId()
	start, err := database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,semantic_state,
		authority_source_sequence_cutoff,server_received_at)
		VALUES(?,?,'specification',1,1,1,?,'execution_started',?,'active',0,'2026-08-20T10:00:00Z')`,
		deliveryID, attemptID, startEnvelopeID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	startID, _ := start.LastInsertId()
	runner, err := database.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('supervision-runner','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	runnerID, _ := runner.LastInsertId()
	key, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'runner',?,'paimos_runner',?)`, runnerID,
		"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", ScopeRunner)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := key.LastInsertId()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,device_id,requested_by,agent_name,status,
		delivery_instrumentation_version,started_at)
		VALUES(?,?,'runner-01',?,'supervision-runner','running',1,datetime('now'))`, issueID, projectID, requestedBy)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	linkEnvelope, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,3,'test-run-link',zeroblob(32),'run_linked',?,'2026-08-20T10:01:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	linkEnvelopeID, _ := linkEnvelope.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_agent_run_links(agent_run_id,root_issue_id,delivery_id,
		attempt_id,stage_key,execution_number,execution_start_stage_event_id,reporter_id,link_delivery_event_id,created_at)
		VALUES(?,?,?,?,'specification',1,?,?,?,'2026-08-20T10:01:00Z')`, runID, issueID, deliveryID,
		attemptID, startID, reporterID, linkEnvelopeID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_agent_run_activations(delivery_id,attempt_id,stage_key,
		execution_number,authority_epoch,agent_run_id,reporter_id,authority_stage_event_id,
		telemetry_sequence_cutoff,created_at) VALUES(?,?,'specification',1,1,?,?,?,0,'2026-08-20T10:01:00Z')`,
		deliveryID, attemptID, runID, reporterID, startID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_stage_latest(delivery_id,attempt_id,stage_key,
		execution_number,authority_epoch,current_reporter_id,execution_start_stage_event_id,
		authority_stage_event_id,updated_at) VALUES(?,?,'specification',1,1,?,?,?,'2026-08-20T10:01:00Z')`,
		deliveryID, attemptID, reporterID, startID, startID); err != nil {
		t.Fatal(err)
	}
	principal, err := auth.NewAPIKeyPrincipal(keyID, runnerID, auth.ScopeSet{ScopeRunner: {}})
	if err != nil {
		t.Fatal(err)
	}
	return runID, principal
}

func TestRunnerLeaseOutboxClaimAndRuntimeResultFollowM147Ordering(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, human := seedGrantTarget(t, database)
	runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
	service := NewService(database, Options{})
	leaseKey := sha256.Sum256([]byte("lease-issue"))
	lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running", "input.respond", "run.pause", "run.resume"},
		OperationKeyDigest: leaseKey})
	if err != nil {
		t.Fatalf("issue runner lease: %v (%s)", err, ErrorCode(err))
	}
	inputKey := sha256.Sum256([]byte("approval-input"))
	input, err := service.CreateInputRequest(context.Background(), runner, InputCreateRequest{LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, Kind: "approval", PromptTemplate: "approval_required",
		OperationKeyDigest: inputKey})
	if err != nil || input.Revision != 1 || input.Kind != "approval" {
		t.Fatalf("create input: input=%+v err=%v code=%s", input, err, ErrorCode(err))
	}
	grantKey := sha256.Sum256([]byte("actor-grant-with-lease"))
	grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: grantKey})
	if err != nil {
		t.Fatalf("issue actor grant: %v (%s)", err, ErrorCode(err))
	}
	commandKey := sha256.Sum256([]byte("pause-command"))
	command, err := service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "run.pause", RunID: runID, RuntimeRevision: 1,
		OperationKeyDigest: commandKey})
	if err != nil {
		t.Fatalf("create pause: %v (%s)", err, ErrorCode(err))
	}
	confirmKey := sha256.Sum256([]byte("pause-confirm"))
	command, err = service.ConfirmCommand(context.Background(), human, CommandConfirmRequest{CommandID: command.CommandID,
		StatusRevision: 1, OperationKeyDigest: confirmKey})
	if err != nil || command.Status != "accepted" {
		t.Fatalf("confirm pause: command=%+v err=%v code=%s", command, err, ErrorCode(err))
	}
	pulled, err := service.Pull(context.Background(), runner, PullRequest{LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision})
	if err != nil || len(pulled.Effects) != 1 || pulled.Effects[0].CommandID != command.CommandID {
		t.Fatalf("pull: projection=%+v err=%v code=%s", pulled, err, ErrorCode(err))
	}
	claimKey := sha256.Sum256([]byte("pause-claim"))
	effect, err := service.Claim(context.Background(), runner, ClaimRequest{CommandID: command.CommandID,
		LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1, DeviceID: "runner-01",
		OperationKeyDigest: claimKey})
	if err != nil || effect.CommandID != command.CommandID {
		t.Fatalf("claim: effect=%+v err=%v code=%s", effect, err, ErrorCode(err))
	}
	resultKey := sha256.Sum256([]byte("pause-result"))
	command, err = service.RecordResult(context.Background(), runner, ResultRequest{CommandID: command.CommandID,
		LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1, ClaimSequence: 1,
		ResultSequence: 1, DeviceID: "runner-01", Outcome: "applied", OperationKeyDigest: resultKey})
	if err != nil || command.Status != "applied" || command.Outcome != "applied" {
		t.Fatalf("result: command=%+v err=%v code=%s", command, err, ErrorCode(err))
	}
	var runtimeState, outboxState string
	var runtimeRevision int64
	if err := database.QueryRow(`SELECT state,revision FROM control_runtime_states WHERE agent_run_id=?`, runID).
		Scan(&runtimeState, &runtimeRevision); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT delivery_state FROM control_outbox WHERE command_id=?`, command.CommandID).
		Scan(&outboxState); err != nil {
		t.Fatal(err)
	}
	if runtimeState != "paused" || runtimeRevision != 2 || outboxState != "acknowledged" {
		t.Fatalf("runtime/outbox truth state=%s revision=%d outbox=%s", runtimeState, runtimeRevision, outboxState)
	}
}

func foreignKeyViolations(database *sql.DB) (int, error) {
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	return count, rows.Err()
}

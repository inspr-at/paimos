// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	storedb "github.com/inspr-at/paimos/backend/db"
	"modernc.org/sqlite"
)

const testSessionCredential = "12345678-1234-4234-9234-123456789abc"

type mutableClock struct {
	mu  sync.RWMutex
	now time.Time
}

type countingDriver struct {
	inner driver.Driver
	count *atomic.Int64
}

func (wrapped countingDriver) Open(name string) (driver.Conn, error) {
	connection, err := wrapped.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingConnection{Conn: connection, count: wrapped.count}, nil
}

type countingConnection struct {
	driver.Conn
	count *atomic.Int64
}

func (connection *countingConnection) QueryContext(ctx context.Context, query string,
	args []driver.NamedValue) (driver.Rows, error) {
	connection.count.Add(1)
	return connection.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (connection *countingConnection) ExecContext(ctx context.Context, query string,
	args []driver.NamedValue) (driver.Result, error) {
	connection.count.Add(1)
	return connection.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (connection *countingConnection) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return connection.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

var countingDriverSequence atomic.Int64

func (clock *mutableClock) Now() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return clock.now
}

func (clock *mutableClock) Set(now time.Time) {
	clock.mu.Lock()
	clock.now = now
	clock.mu.Unlock()
}

func waitForSQLiteTime(t *testing.T, database *sql.DB, boundary time.Time) {
	waitForSQLiteTimeWithin(t, database, boundary, 2*time.Second)
}

func waitForSQLiteTimeWithin(t *testing.T, database *sql.DB, boundary time.Time, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		var nowText string
		if err := database.QueryRow(`SELECT strftime('%Y-%m-%dT%H:%M:%fZ','now')`).Scan(&nowText); err != nil {
			t.Fatal(err)
		}
		now, err := parseControlTime(nowText)
		if err != nil {
			t.Fatal(err)
		}
		if !now.Before(boundary) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("SQLite clock did not reach %s", boundary)
}

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
	return seedGrantTargetNamed(t, database, "", "SUP", testSessionCredential)
}

func seedGrantTargetNamed(t *testing.T, database *sql.DB, suffix, projectKey, sessionCredential string) (deliveryID, userID int64, principal auth.Principal) {
	t.Helper()
	deliveryID = seedDeliveryTargetNamed(t, database, suffix, projectKey)
	user, err := database.Exec(`INSERT INTO users(username,password,role,status)
		VALUES(?,'x','member','active')`, "supervision-human"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ = user.LastInsertId()
	if _, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES(?,?,'2030-12-01 00:00:00','2026-08-01 00:00:00',?)`,
		"supervision-bearer"+suffix, userID, sessionCredential); err != nil {
		t.Fatal(err)
	}
	principal, err = auth.NewSessionPrincipal(sessionCredential, userID, userID, false)
	if err != nil {
		t.Fatal(err)
	}
	return deliveryID, userID, principal
}

func seedDeliveryTargetNamed(t *testing.T, database *sql.DB, suffix, projectKey string) int64 {
	t.Helper()
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES(?,?)`, "Supervision"+suffix, projectKey)
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
		VALUES(?, ?, ?, '2026-08-20T10:00:00Z','2026-08-20T10:00:00Z')`, issueID, "issue:1"+suffix, projectID)
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, _ := delivery.LastInsertId()
	reporter, err := database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
		VALUES(?,'system',?,'2026-08-20T10:00:00Z')`, deliveryID, "supervision-test"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	reporterID, _ := reporter.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,1,'test-attempt',zeroblob(32),'attempt_started',?,'2026-08-20T10:00:00Z')`, deliveryID, reporterID); err != nil {
		t.Fatal(err)
	}
	return deliveryID
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
	otherDelivery := seedDeliveryTargetNamed(t, database, "-conflict", "SUC")
	if _, err := service.IssueActorGrant(context.Background(), principal, GrantIssueRequest{
		DeliveryID: otherDelivery, OperationKeyDigest: key,
	}); !IsCode(err, CodeIdempotencyConflict) {
		t.Fatalf("changed grant request error=%v code=%s", err, ErrorCode(err))
	}
	var expiryAfterConflict string
	if err := database.QueryRow(`SELECT expires_at FROM control_capability_grants WHERE grant_id=? AND revision=?`,
		grant.GrantID, grant.Revision).Scan(&expiryAfterConflict); err != nil {
		t.Fatal(err)
	}
	if expiryAfterConflict != controlTimestamp(grant.ExpiresAt) {
		t.Fatalf("grant conflict renewed TTL: got=%s want=%s", expiryAfterConflict, controlTimestamp(grant.ExpiresAt))
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

type queuedCancelMutator struct{ fail error }

func (queuedCancelMutator) SetIssuePriorityTx(context.Context, *sql.Tx, PriorityMutation) error {
	return errors.New("unexpected priority mutation")
}

func (mutator queuedCancelMutator) CancelQueuedRunTx(ctx context.Context, tx *sql.Tx, mutation RunCancellationMutation) error {
	if mutator.fail != nil {
		return mutator.fail
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='cancelled',finished_at=datetime('now')
		WHERE id=? AND issue_id=? AND status='queued'`, mutation.RunID, mutation.IssueID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("queued run disappeared")
	}
	return nil
}

func (queuedCancelMutator) CancelRunningRunTx(context.Context, *sql.Tx, RunCancellationMutation) error {
	return errors.New("unexpected running cancellation")
}

type trackingChanges struct {
	fail  error
	wakes *int
}

func (changes trackingChanges) RecordControlChangeTx(ctx context.Context, tx *sql.Tx, change ControlChange) (CommitWake, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO supervision_test_hints(command_id) VALUES(?)`, change.CommandID); err != nil {
		return CommitWake{}, err
	}
	if changes.fail != nil {
		return CommitWake{}, changes.fail
	}
	return CommitWake{ID: 1}, nil
}

func (changes trackingChanges) WakeControlChange(context.Context, CommitWake) {
	if changes.wakes != nil {
		(*changes.wakes)++
	}
}

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
	if _, err := service.CreateCommand(context.Background(), principal, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "issue.priority.set", Priority: "low", OperationKeyDigest: commandKey}); !IsCode(err, CodeIdempotencyConflict) {
		t.Fatalf("changed command request error=%v code=%s", err, ErrorCode(err))
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

func TestQueuedCancelRollsBackMutatorAndHintFailuresWithoutWake(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, human := seedGrantTarget(t, database)
	runID, _ := seedRunnerActivation(t, database, deliveryID, humanID)
	if _, err := database.Exec(`UPDATE agent_runs SET status='queued',started_at=NULL WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE supervision_test_hints(command_id TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	service := NewService(database, Options{})
	grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: sha256.Sum256([]byte("queued-rollback-grant"))})
	if err != nil {
		t.Fatalf("grant: %v (%s)", err, ErrorCode(err))
	}
	command, err := service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "run.cancel.queued", RunID: runID,
		OperationKeyDigest: sha256.Sum256([]byte("queued-rollback-command"))})
	if err != nil {
		t.Fatalf("command: %v (%s)", err, ErrorCode(err))
	}

	wakes := 0
	mutatorFailure := NewService(database, Options{Mutator: queuedCancelMutator{fail: errors.New("mutator failed")},
		Changes: trackingChanges{wakes: &wakes}})
	if _, err := mutatorFailure.ConfirmCommand(context.Background(), human, CommandConfirmRequest{CommandID: command.CommandID,
		StatusRevision: 1, OperationKeyDigest: sha256.Sum256([]byte("queued-mutator-failure"))}); err == nil {
		t.Fatal("mutator failure unexpectedly committed")
	}
	assertQueuedCancelRollback(t, database, command.CommandID, runID, wakes)

	hintFailure := NewService(database, Options{Mutator: queuedCancelMutator{},
		Changes: trackingChanges{fail: errors.New("hint failed"), wakes: &wakes}})
	if _, err := hintFailure.ConfirmCommand(context.Background(), human, CommandConfirmRequest{CommandID: command.CommandID,
		StatusRevision: 1, OperationKeyDigest: sha256.Sum256([]byte("queued-hint-failure"))}); err == nil {
		t.Fatal("hint failure unexpectedly committed")
	}
	assertQueuedCancelRollback(t, database, command.CommandID, runID, wakes)
}

func assertQueuedCancelRollback(t *testing.T, database *sql.DB, commandID string, runID int64, wakes int) {
	t.Helper()
	var status string
	var revision, outcomePresent, terminalEvents, cancellationFacts, hints, operations int
	if err := database.QueryRow(`SELECT status,status_revision,outcome IS NOT NULL FROM control_commands WHERE command_id=?`,
		commandID).Scan(&status, &revision, &outcomePresent); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_events WHERE command_id=? AND event_kind IN
		('command_accepted','command_applied','cancellation_recorded')`, commandID).Scan(&terminalEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM agent_run_cancellation_facts WHERE run_id=?`, runID).Scan(&cancellationFacts); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM supervision_test_hints`).Scan(&hints); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_operation_keys WHERE operation_kind='command.confirm'
		AND command_id=?`, commandID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	var runStatus string
	if err := database.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if status != "pending_confirmation" || revision != 1 || outcomePresent != 0 || terminalEvents != 0 ||
		cancellationFacts != 0 || hints != 0 || operations != 0 || wakes != 0 || runStatus != "queued" {
		t.Fatalf("rollback status=%s rev=%d outcome=%d events=%d facts=%d hints=%d operations=%d wakes=%d run=%s",
			status, revision, outcomePresent, terminalEvents, cancellationFacts, hints, operations, wakes, runStatus)
	}
}

// The protected race gate runs four phases that use this helper. Linux race
// instrumentation plus SQLite's five-second busy waits can serialize the tail
// of 32 durable writers beyond one minute even while the database is making
// progress. A two-minute bound preserves that production-shaped contention
// while leaving the package inside the workflow's eight-minute timeout.
const concurrentStorageRetryLimit = 120 * time.Second

type concurrentMutationResult[T any] struct {
	index      int
	attempts   int
	projection T
	err        error
}

func retryConcurrentStorageUnavailable[T any](parent context.Context, index int,
	operation func(context.Context) (T, error),
) concurrentMutationResult[T] {
	ctx, cancel := context.WithTimeout(parent, concurrentStorageRetryLimit)
	defer cancel()

	var lastStorageErr error
	for attempt := 1; ; attempt++ {
		projection, err := operation(ctx)
		result := concurrentMutationResult[T]{index: index, attempts: attempt, projection: projection, err: err}
		if !IsCode(err, CodeStorageUnavailable) {
			if (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) && lastStorageErr != nil {
				result.err = fmt.Errorf("storage retry deadline exceeded; final operation: %v: %w", err, lastStorageErr)
			}
			return result
		}
		lastStorageErr = err

		shift := attempt - 1
		if shift > 4 {
			shift = 4
		}
		backoff := 20 * time.Millisecond * time.Duration(1<<shift)
		jitter := time.Duration(((index+1)*53+attempt*97)%181) * time.Millisecond
		timer := time.NewTimer(backoff + jitter)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			result.err = fmt.Errorf("storage retry deadline exceeded: %w", lastStorageErr)
			return result
		}
	}
}

func TestThirtyTwoConcurrentCommandCreateAndConfirmConverge(t *testing.T) {
	database := openSupervisionTestDB(t)
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
	created := make(chan concurrentMutationResult[CommandProjection], contenders)
	for index := 0; index < contenders; index++ {
		go func(index int) {
			<-start
			key := sha256.Sum256([]byte("create-" + string(rune(index))))
			created <- retryConcurrentStorageUnavailable(context.Background(), index,
				func(ctx context.Context) (CommandProjection, error) {
					return service.CreateCommand(ctx, principal, CommandCreateRequest{
						GrantID: grant.GrantID, GrantRevision: grant.Revision, Action: "issue.priority.set",
						Priority: "high", OperationKeyDigest: key,
					})
				})
		}(index)
	}
	close(start)
	var commandID string
	for index := 0; index < contenders; index++ {
		result := <-created
		if result.err != nil {
			t.Fatalf("create contender %d after %d attempts: %v (%s)", result.index, result.attempts,
				result.err, ErrorCode(result.err))
		}
		if commandID == "" {
			commandID = result.projection.CommandID
		} else if result.projection.CommandID != commandID {
			t.Fatalf("create diverged: %s != %s", result.projection.CommandID, commandID)
		}
	}

	start = make(chan struct{})
	confirmed := make(chan concurrentMutationResult[CommandProjection], contenders)
	for index := 0; index < contenders; index++ {
		go func(index int) {
			<-start
			key := sha256.Sum256([]byte("confirm-" + string(rune(index))))
			confirmed <- retryConcurrentStorageUnavailable(context.Background(), index,
				func(ctx context.Context) (CommandProjection, error) {
					return service.ConfirmCommand(ctx, principal, CommandConfirmRequest{
						CommandID: commandID, StatusRevision: 1, OperationKeyDigest: key,
					})
				})
		}(index)
	}
	close(start)
	for index := 0; index < contenders; index++ {
		result := <-confirmed
		if result.err != nil {
			t.Fatalf("confirm contender %d after %d attempts: %v (%s)", result.index, result.attempts,
				result.err, ErrorCode(result.err))
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

func TestProjectLifecycleAuthorityDoesNotReviveOldGrant(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, _, principal := seedGrantTarget(t, database)
	service := NewService(database, Options{})
	issueKey := sha256.Sum256([]byte("lifecycle-grant"))
	grant, err := service.IssueActorGrant(context.Background(), principal, GrantIssueRequest{
		DeliveryID: deliveryID, OperationKeyDigest: issueKey,
	})
	if err != nil {
		t.Fatalf("issue grant: %v (%s)", err, ErrorCode(err))
	}
	command, err := service.CreateCommand(context.Background(), principal, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "issue.priority.set", Priority: "high",
		OperationKeyDigest: sha256.Sum256([]byte("lifecycle-command"))})
	if err != nil {
		t.Fatalf("create lifecycle command: %v (%s)", err, ErrorCode(err))
	}
	var projectID int64
	if err := database.QueryRow(`SELECT issue.project_id FROM deliveries delivery
		JOIN issues issue ON issue.id=delivery.issue_id WHERE delivery.id=?`, deliveryID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE projects SET status='archived' WHERE id=?`, projectID); err != nil {
		t.Fatal(err)
	}
	if projection, err := service.GetActorGrant(context.Background(), principal, GrantGetRequest{GrantID: grant.GrantID,
		Revision: grant.Revision}); !errors.Is(err, ErrConflict) || len(projection.Targets) != 0 {
		t.Fatalf("archived priority grant targets=%+v error=%v code=%s", projection.Targets, err, ErrorCode(err))
	}
	confirmKey := sha256.Sum256([]byte("lifecycle-confirm"))
	if _, err := service.ConfirmCommand(context.Background(), principal, CommandConfirmRequest{CommandID: command.CommandID,
		StatusRevision: 1, OperationKeyDigest: confirmKey}); err == nil {
		t.Fatal("archived project accepted stale priority challenge")
	}
	if _, err := database.Exec(`UPDATE projects SET status='active' WHERE id=?`, projectID); err != nil {
		t.Fatal(err)
	}
	if projection, err := service.GetActorGrant(context.Background(), principal, GrantGetRequest{GrantID: grant.GrantID,
		Revision: grant.Revision}); !IsCode(err, CodeStaleTarget) || len(projection.Targets) != 0 {
		t.Fatalf("revived old grant targets=%+v error=%v code=%s", projection.Targets, err, ErrorCode(err))
	}
	if _, err := service.ConfirmCommand(context.Background(), principal, CommandConfirmRequest{CommandID: command.CommandID,
		StatusRevision: 1, OperationKeyDigest: confirmKey}); err == nil {
		t.Fatal("active-again project revived stale challenge")
	}
	renewKey := sha256.Sum256([]byte("lifecycle-grant-new-authority"))
	renewed, err := service.IssueActorGrant(context.Background(), principal, GrantIssueRequest{
		DeliveryID: deliveryID, OperationKeyDigest: renewKey,
	})
	if err != nil {
		t.Fatalf("issue new authority grant: %v (%s)", err, ErrorCode(err))
	}
	if renewed.GrantID != grant.GrantID || renewed.Revision != grant.Revision+1 {
		t.Fatalf("authority renewal lineage got=%+v old=%+v", renewed, grant)
	}
}

func seedRunnerActivation(t *testing.T, database *sql.DB, deliveryID, requestedBy int64) (int64, auth.Principal) {
	return seedRunnerActivationNamed(t, database, deliveryID, requestedBy, "", "runner-01")
}

func seedRunnerActivationNamed(t *testing.T, database *sql.DB, deliveryID, requestedBy int64,
	suffix, deviceID string) (int64, auth.Principal) {
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
		VALUES(?,'x','member','active')`, "supervision-runner"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	runnerID, _ := runner.LastInsertId()
	keyHash := fmt.Sprintf("%064x", sha256.Sum256([]byte("runner-key"+suffix)))
	key, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,?,?, ?,?)`, runnerID, "runner"+suffix, keyHash, "paimos_runner"+suffix, ScopeRunner)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := key.LastInsertId()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,device_id,requested_by,agent_name,status,
		delivery_instrumentation_version,started_at)
		VALUES(?,?,?,?,?,'running',1,datetime('now'))`, issueID, projectID, deviceID, requestedBy, "supervision-runner"+suffix)
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
	if _, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running", "input.respond"},
		OperationKeyDigest: leaseKey}); !IsCode(err, CodeIdempotencyConflict) {
		t.Fatalf("changed lease request error=%v code=%s", err, ErrorCode(err))
	}
	var leaseExpiryAfterConflict string
	if err := database.QueryRow(`SELECT expires_at FROM control_capability_leases WHERE lease_id=? AND revision=?`,
		lease.LeaseID, lease.Revision).Scan(&leaseExpiryAfterConflict); err != nil {
		t.Fatal(err)
	}
	if leaseExpiryAfterConflict != controlTimestamp(lease.ExpiresAt) {
		t.Fatalf("lease conflict renewed TTL: got=%s want=%s", leaseExpiryAfterConflict, controlTimestamp(lease.ExpiresAt))
	}
	inputKey := sha256.Sum256([]byte("approval-input"))
	input, err := service.CreateInputRequest(context.Background(), runner, InputCreateRequest{LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, Kind: "approval", PromptTemplate: "approval_required",
		OperationKeyDigest: inputKey})
	if err != nil || input.Revision != 1 || input.Kind != "approval" {
		t.Fatalf("create input: input=%+v err=%v code=%s", input, err, ErrorCode(err))
	}
	choiceKey := sha256.Sum256([]byte("choice-input"))
	choice, err := service.CreateInputRequest(context.Background(), runner, InputCreateRequest{LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, Kind: "choice", PromptTemplate: "choice_required",
		OptionCodes: []string{"choice_1", "choice_2"}, OperationKeyDigest: choiceKey})
	if err != nil || choice.Revision != 1 || len(choice.OptionCodes) != 2 {
		t.Fatalf("create choice input: input=%+v err=%v code=%s", choice, err, ErrorCode(err))
	}
	supersedeKey := sha256.Sum256([]byte("choice-input-supersede"))
	supersedeRequest := InputCreateRequest{LeaseID: lease.LeaseID, LeaseRevision: lease.Revision,
		RequestID: choice.RequestID, Kind: "choice", PromptTemplate: "choice_required",
		OptionCodes: []string{"choice_1", "choice_2"}, OperationKeyDigest: supersedeKey}
	startSupersede := make(chan struct{})
	superseded := make(chan struct {
		projection InputRequestProjection
		err        error
	}, 2)
	for range 2 {
		go func() {
			<-startSupersede
			projection, err := service.CreateInputRequest(context.Background(), runner, supersedeRequest)
			superseded <- struct {
				projection InputRequestProjection
				err        error
			}{projection, err}
		}()
	}
	close(startSupersede)
	for range 2 {
		result := <-superseded
		if result.err != nil || result.projection.Revision != 2 || result.projection.RequestID != choice.RequestID {
			t.Fatalf("choice supersession race: projection=%+v err=%v code=%s", result.projection,
				result.err, ErrorCode(result.err))
		}
	}
	var requestedFacts, supersededFacts, seals, currentInputRevision int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_events WHERE input_request_id=? AND event_kind='input_requested'`,
		choice.RequestID).Scan(&requestedFacts); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_events WHERE input_request_id=? AND event_kind='input_superseded'`,
		choice.RequestID).Scan(&supersededFacts); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_input_request_seals WHERE request_id=?`, choice.RequestID).
		Scan(&seals); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT current_revision FROM control_input_request_states WHERE request_id=?
		AND terminal_event_id IS NULL`, choice.RequestID).Scan(&currentInputRevision); err != nil {
		t.Fatal(err)
	}
	if requestedFacts != 2 || supersededFacts != 1 || seals != 2 || currentInputRevision != 2 {
		t.Fatalf("choice lineage requested=%d superseded=%d seals=%d current=%d", requestedFacts,
			supersededFacts, seals, currentInputRevision)
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
		LeaseRevision: lease.Revision, DeviceID: "runner-01"})
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
	if _, err := service.RecordResult(context.Background(), runner, ResultRequest{CommandID: command.CommandID,
		LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1, ClaimSequence: 1,
		ResultSequence: 1, DeviceID: "runner-01", Outcome: "rejected", Reason: "effect_rejected",
		OperationKeyDigest: resultKey}); !IsCode(err, CodeIdempotencyConflict) {
		t.Fatalf("changed result request error=%v code=%s", err, ErrorCode(err))
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

	resumeGrant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: sha256.Sum256([]byte("actor-grant-after-pause"))})
	if err != nil || !containsAction(resumeGrant.Actions, "run.resume") || containsAction(resumeGrant.Actions, "run.pause") ||
		!grantHasExactRunTarget(resumeGrant, "run.resume", runID) {
		t.Fatalf("resume grant=%+v err=%v code=%s", resumeGrant, err, ErrorCode(err))
	}
	resumeKey := sha256.Sum256([]byte("resume-command"))
	resume, err := service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: resumeGrant.GrantID,
		GrantRevision: resumeGrant.Revision, Action: "run.resume", RunID: runID, RuntimeRevision: 2,
		OperationKeyDigest: resumeKey})
	if err != nil {
		t.Fatalf("create resume: %v (%s)", err, ErrorCode(err))
	}
	resumeConfirmKey := sha256.Sum256([]byte("resume-confirm"))
	resume, err = service.ConfirmCommand(context.Background(), human, CommandConfirmRequest{CommandID: resume.CommandID,
		StatusRevision: 1, OperationKeyDigest: resumeConfirmKey})
	if err != nil || resume.Status != "accepted" {
		t.Fatalf("confirm resume: command=%+v err=%v code=%s", resume, err, ErrorCode(err))
	}
	resumeClaimKey := sha256.Sum256([]byte("resume-claim"))
	if _, err := service.Claim(context.Background(), runner, ClaimRequest{CommandID: resume.CommandID,
		LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1, DeviceID: "runner-01",
		OperationKeyDigest: claimKey}); !IsCode(err, CodeIdempotencyConflict) {
		t.Fatalf("changed claim request error=%v code=%s", err, ErrorCode(err))
	}
	var resumeDeliveryState string
	if err := database.QueryRow(`SELECT delivery_state FROM control_outbox WHERE command_id=?`, resume.CommandID).
		Scan(&resumeDeliveryState); err != nil || resumeDeliveryState != "queued" {
		t.Fatalf("claim conflict mutated state=%q err=%v", resumeDeliveryState, err)
	}
	if _, err := service.Claim(context.Background(), runner, ClaimRequest{CommandID: resume.CommandID,
		LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1, DeviceID: "runner-01",
		OperationKeyDigest: resumeClaimKey}); err != nil {
		t.Fatalf("claim resume: %v (%s)", err, ErrorCode(err))
	}
	revokeKey := sha256.Sum256([]byte("lease-loss"))
	if _, err := service.RevokeRunnerLease(context.Background(), runner, LeaseRevokeRequest{LeaseID: lease.LeaseID,
		Revision: lease.Revision, DeviceID: "runner-01", OperationKeyDigest: revokeKey}); err != nil {
		t.Fatalf("revoke claimed lease: %v (%s)", err, ErrorCode(err))
	}
	reconcileRequest := ReconcileRequest{Mode: ReconcileRunner, Limit: 100, LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, DeviceID: "runner-01"}
	reconciled, err := service.Reconcile(context.Background(), runner, reconcileRequest)
	if err != nil || reconciled.UnknownOutcomes != 1 {
		t.Fatalf("reconcile lease loss: projection=%+v err=%v code=%s", reconciled, err, ErrorCode(err))
	}
	if repeated, err := service.Reconcile(context.Background(), runner, reconcileRequest); err != nil ||
		repeated.UnknownOutcomes != 0 {
		t.Fatalf("repeat reconcile duplicated loss: projection=%+v err=%v code=%s", repeated, err, ErrorCode(err))
	}
	var resumeRevision int64
	var resumeStatus, resumeOutcome, resumeReason, resumeOutboxState, resumeOutboxReason string
	if err := database.QueryRow(`SELECT command.status_revision,command.status,command.outcome,command.safe_reason,
		outbox.delivery_state,outbox.safe_reason FROM control_commands command JOIN control_outbox outbox
		ON outbox.command_id=command.command_id WHERE command.command_id=?`, resume.CommandID).Scan(&resumeRevision,
		&resumeStatus, &resumeOutcome, &resumeReason, &resumeOutboxState, &resumeOutboxReason); err != nil {
		t.Fatal(err)
	}
	if resumeRevision != 3 || resumeStatus != "accepted" || resumeOutcome != "outcome_unknown" ||
		resumeReason != "runner_lost" || resumeOutboxState != "claimed" || resumeOutboxReason != "runner_lost" {
		t.Fatalf("unknown state rev=%d status=%s outcome=%s reason=%s outbox=%s/%s", resumeRevision,
			resumeStatus, resumeOutcome, resumeReason, resumeOutboxState, resumeOutboxReason)
	}
	lateKey := sha256.Sum256([]byte("late-resume-result"))
	lateRequest := ResultRequest{CommandID: resume.CommandID, LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, EffectSequence: 1, ClaimSequence: 1, ResultSequence: 1,
		DeviceID: "runner-01", Outcome: "applied", OperationKeyDigest: lateKey}
	late, err := service.RecordResult(context.Background(), runner, lateRequest)
	if err != nil || late.Status != "applied" || late.StatusRevision != 4 || late.Outcome != "applied" {
		t.Fatalf("late exact result: command=%+v err=%v code=%s", late, err, ErrorCode(err))
	}
	replayedLate, err := service.RecordResult(context.Background(), runner, lateRequest)
	if err != nil || replayedLate.CommandID != late.CommandID || replayedLate.StatusRevision != 4 {
		t.Fatalf("late result replay: command=%+v err=%v code=%s", replayedLate, err, ErrorCode(err))
	}
	convergedLate := lateRequest
	convergedLate.OperationKeyDigest = sha256.Sum256([]byte("late-resume-result-new-key"))
	if projection, err := service.RecordResult(context.Background(), runner, convergedLate); err != nil ||
		projection.CommandID != late.CommandID || projection.StatusRevision != 4 {
		t.Fatalf("late result semantic converge: command=%+v err=%v code=%s", projection, err, ErrorCode(err))
	}
	var reconciledFacts, acknowledgedFacts, unknownFacts, resultOperations int
	for _, check := range []struct {
		query string
		value *int
	}{
		{`SELECT COUNT(*) FROM control_events WHERE command_id=? AND event_kind='effect_reconciled'`, &reconciledFacts},
		{`SELECT COUNT(*) FROM control_events WHERE command_id=? AND event_kind='effect_acknowledged'`, &acknowledgedFacts},
		{`SELECT COUNT(*) FROM control_events WHERE command_id=? AND event_kind='effect_outcome_unknown'`, &unknownFacts},
		{`SELECT COUNT(*) FROM control_operation_keys WHERE command_id=? AND operation_kind='command.result'`, &resultOperations},
	} {
		if err := database.QueryRow(check.query, resume.CommandID).Scan(check.value); err != nil {
			t.Fatal(err)
		}
	}
	if reconciledFacts != 1 || acknowledgedFacts != 0 || unknownFacts != 1 || resultOperations != 2 {
		t.Fatalf("late result facts reconciled=%d acknowledged=%d unknown=%d operations=%d",
			reconciledFacts, acknowledgedFacts, unknownFacts, resultOperations)
	}
	if err := database.QueryRow(`SELECT state,revision FROM control_runtime_states WHERE agent_run_id=?`, runID).
		Scan(&runtimeState, &runtimeRevision); err != nil {
		t.Fatal(err)
	}
	if runtimeState != "running" || runtimeRevision != 3 {
		t.Fatalf("late runtime truth state=%s revision=%d", runtimeState, runtimeRevision)
	}
	if _, err := database.Exec(`UPDATE api_keys SET scopes='' WHERE id=?`, runner.APIKeyID()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordResult(context.Background(), runner, lateRequest); !IsCode(err, CodeScopeRevoked) {
		t.Fatalf("late replay after scope removal error=%v code=%s", err, ErrorCode(err))
	}
}

func TestThirtyTwoConcurrentAcceptedEffectReservationAndClaimConverge(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, human := seedGrantTarget(t, database)
	runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
	service := NewService(database, Options{})
	// This test measures durable convergence across two bounded 32-writer phases,
	// not lease expiry. Give its fixture a lease longer than the eight-minute race
	// gate; TestClaimAgainstExpiredLeaseFailsClosed owns the fail-closed boundary,
	// while TestFrozenDurationsAndActionPolicy keeps production LeaseTTL frozen.
	service.leaseTTL = 10 * time.Minute
	leaseKey := sha256.Sum256([]byte("concurrent-lease"))
	lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running", "run.pause", "run.resume"},
		OperationKeyDigest: leaseKey})
	if err != nil {
		t.Fatalf("issue lease: %v (%s)", err, ErrorCode(err))
	}
	grantKey := sha256.Sum256([]byte("concurrent-async-grant"))
	grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: grantKey})
	if err != nil {
		t.Fatalf("issue grant: %v (%s)", err, ErrorCode(err))
	}
	createKey := sha256.Sum256([]byte("concurrent-pause"))
	command, err := service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "run.pause", RunID: runID, RuntimeRevision: 1,
		OperationKeyDigest: createKey})
	if err != nil {
		t.Fatalf("create command: %v (%s)", err, ErrorCode(err))
	}

	const contenders = 32
	start := make(chan struct{})
	confirmResults := make(chan concurrentMutationResult[CommandProjection], contenders)
	for index := 0; index < contenders; index++ {
		go func(index int) {
			<-start
			key := sha256.Sum256([]byte(fmt.Sprintf("async-confirm-%02d", index)))
			confirmResults <- retryConcurrentStorageUnavailable(context.Background(), index,
				func(ctx context.Context) (CommandProjection, error) {
					return service.ConfirmCommand(ctx, human, CommandConfirmRequest{CommandID: command.CommandID,
						StatusRevision: 1, OperationKeyDigest: key})
				})
		}(index)
	}
	close(start)
	for index := 0; index < contenders; index++ {
		result := <-confirmResults
		if result.err != nil {
			t.Fatalf("confirm contender %d after %d attempts: %v (%s)", result.index, result.attempts,
				result.err, ErrorCode(result.err))
		}
	}

	start = make(chan struct{})
	claimResults := make(chan concurrentMutationResult[EffectProjection], contenders)
	for index := 0; index < contenders; index++ {
		go func(index int) {
			<-start
			key := sha256.Sum256([]byte(fmt.Sprintf("async-claim-%02d", index)))
			claimResults <- retryConcurrentStorageUnavailable(context.Background(), index,
				func(ctx context.Context) (EffectProjection, error) {
					return service.Claim(ctx, runner, ClaimRequest{CommandID: command.CommandID,
						LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1,
						DeviceID: "runner-01", OperationKeyDigest: key})
				})
		}(index)
	}
	close(start)
	for index := 0; index < contenders; index++ {
		result := <-claimResults
		if result.err != nil {
			t.Fatalf("claim contender %d after %d attempts: %v (%s)", result.index, result.attempts,
				result.err, ErrorCode(result.err))
		}
	}

	var outboxes, queuedFacts, claimedFacts, confirmOps, claimOps int
	for _, check := range []struct {
		query string
		value *int
	}{
		{`SELECT COUNT(*) FROM control_outbox WHERE command_id=? AND delivery_state='claimed'`, &outboxes},
		{`SELECT COUNT(*) FROM control_events WHERE command_id=? AND event_kind='effect_queued'`, &queuedFacts},
		{`SELECT COUNT(*) FROM control_events WHERE command_id=? AND event_kind='effect_claimed'`, &claimedFacts},
		{`SELECT COUNT(*) FROM control_operation_keys WHERE command_id=? AND operation_kind='command.confirm'`, &confirmOps},
		{`SELECT COUNT(*) FROM control_operation_keys WHERE command_id=? AND operation_kind='command.claim'`, &claimOps},
	} {
		if err := database.QueryRow(check.query, command.CommandID).Scan(check.value); err != nil {
			t.Fatal(err)
		}
	}
	if outboxes != 1 || queuedFacts != 1 || claimedFacts != 1 || confirmOps != contenders || claimOps != contenders {
		t.Fatalf("async convergence outbox=%d queued=%d claimed=%d confirm_ops=%d claim_ops=%d",
			outboxes, queuedFacts, claimedFacts, confirmOps, claimOps)
	}
}

func TestClaimAgainstExpiredLeaseFailsClosed(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, human := seedGrantTarget(t, database)
	runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
	service := NewService(database, Options{})
	// Leave ample race-instrumented setup headroom while making expiry quick
	// enough to exercise in every full-suite run. SQLite's clock below owns the
	// actual boundary.
	service.leaseTTL = 15 * time.Second
	actions := []Action{"run.cancel.running", "run.pause", "run.resume"}
	lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: actions,
		OperationKeyDigest: sha256.Sum256([]byte("expired-claim-lease"))})
	if err != nil {
		t.Fatalf("issue lease: %v (%s)", err, ErrorCode(err))
	}
	grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: sha256.Sum256([]byte("expired-claim-grant"))})
	if err != nil {
		t.Fatalf("issue grant: %v (%s)", err, ErrorCode(err))
	}
	command, err := service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "run.pause", RunID: runID, RuntimeRevision: 1,
		OperationKeyDigest: sha256.Sum256([]byte("expired-claim-command"))})
	if err != nil {
		t.Fatalf("create command: %v (%s)", err, ErrorCode(err))
	}
	command, err = service.ConfirmCommand(context.Background(), human, CommandConfirmRequest{
		CommandID: command.CommandID, StatusRevision: 1,
		OperationKeyDigest: sha256.Sum256([]byte("expired-claim-confirm"))})
	if err != nil || command.Status != "accepted" {
		t.Fatalf("confirm command: command=%+v err=%v code=%s", command, err, ErrorCode(err))
	}

	// Cross expiry using SQLite's clock—the authority Claim checks.
	waitForSQLiteTimeWithin(t, database, lease.ExpiresAt, 20*time.Second)
	if _, err := service.Claim(context.Background(), runner, ClaimRequest{CommandID: command.CommandID,
		LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1,
		DeviceID: "runner-01", OperationKeyDigest: sha256.Sum256([]byte("expired-claim-attempt"))}); !IsCode(err, CodeCapabilityUnavailable) {
		t.Fatalf("claim after lease expiry error=%v code=%s", err, ErrorCode(err))
	}

	var claimedOutboxes, claimedFacts, claimOperations int
	for _, check := range []struct {
		query string
		value *int
	}{
		{`SELECT COUNT(*) FROM control_outbox WHERE command_id=? AND delivery_state='claimed'`, &claimedOutboxes},
		{`SELECT COUNT(*) FROM control_events WHERE command_id=? AND event_kind='effect_claimed'`, &claimedFacts},
		{`SELECT COUNT(*) FROM control_operation_keys WHERE command_id=? AND operation_kind='command.claim'`, &claimOperations},
	} {
		if err := database.QueryRow(check.query, command.CommandID).Scan(check.value); err != nil {
			t.Fatal(err)
		}
	}
	if claimedOutboxes != 0 || claimedFacts != 0 || claimOperations != 0 {
		t.Fatalf("expired claim mutated state: outboxes=%d facts=%d operations=%d",
			claimedOutboxes, claimedFacts, claimOperations)
	}
}

func TestAuthorityRotationInvalidatesAndRevisesLeaseLineage(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, human := seedGrantTarget(t, database)
	runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
	service := NewService(database, Options{})
	firstKey := sha256.Sum256([]byte("authority-lease-1"))
	lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running", "run.pause", "run.resume"},
		OperationKeyDigest: firstKey})
	if err != nil {
		t.Fatalf("issue lease: %v (%s)", err, ErrorCode(err))
	}
	grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: sha256.Sum256([]byte("authority-target-grant"))})
	if err != nil || !grantHasExactRunTarget(grant, "run.cancel.running", runID) {
		t.Fatalf("initial authority target grant=%+v err=%v", grant, err)
	}
	var attemptID, reporterID, startID int64
	if err := database.QueryRow(`SELECT attempt_id,reporter_id,execution_start_stage_event_id
		FROM delivery_agent_run_links WHERE agent_run_id=?`, runID).Scan(&attemptID, &reporterID, &startID); err != nil {
		t.Fatal(err)
	}
	envelope, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,4,'authority-rotation',zeroblob(32),'handoff',?,'2026-08-20T10:03:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	envelopeID, _ := envelope.LastInsertId()
	authority, err := database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,
		execution_number,event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,
		execution_start_stage_event_id,previous_stage_event_id,handoff_from_reporter_id,
		authority_source_sequence_cutoff,reason_code,server_received_at)
		VALUES(?,?,'specification',1,2,2,?,'handoff',?,?,?,?,0,'authority_rotated','2026-08-20T10:03:00Z')`,
		deliveryID, attemptID, envelopeID, reporterID, startID, startID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, _ := authority.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_agent_run_activations(delivery_id,attempt_id,stage_key,
		execution_number,authority_epoch,agent_run_id,reporter_id,authority_stage_event_id,
		telemetry_sequence_cutoff,created_at) VALUES(?,?,'specification',1,2,?,?,?,0,'2026-08-20T10:03:00Z')`,
		deliveryID, attemptID, runID, reporterID, authorityID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE delivery_stage_latest SET authority_epoch=2,authority_stage_event_id=?,
		updated_at='2026-08-20T10:03:00Z' WHERE delivery_id=? AND attempt_id=? AND stage_key='specification'`,
		authorityID, deliveryID, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Pull(context.Background(), runner, PullRequest{LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, DeviceID: "runner-01"}); !IsCode(err, CodeStaleTarget) {
		t.Fatalf("old authority pull error=%v code=%s", err, ErrorCode(err))
	}
	if projection, err := service.GetActorGrant(context.Background(), human, GrantGetRequest{GrantID: grant.GrantID,
		Revision: grant.Revision}); !IsCode(err, CodeStaleTarget) || len(projection.Targets) != 0 {
		t.Fatalf("authority/revision change disclosed targets=%+v err=%v code=%s", projection.Targets, err, ErrorCode(err))
	}
	secondKey := sha256.Sum256([]byte("authority-lease-2"))
	revised, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running", "run.pause", "run.resume"},
		OperationKeyDigest: secondKey})
	if err != nil {
		t.Fatalf("revise lease: %v (%s)", err, ErrorCode(err))
	}
	if revised.LeaseID != lease.LeaseID || revised.Revision != lease.Revision+1 || revised.Target.AuthorityEpoch != 2 {
		t.Fatalf("authority lease lineage old=%+v new=%+v", lease, revised)
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

func TestPullAndReconcileQueryPlansUseM147Indexes(t *testing.T) {
	database := openSupervisionTestDB(t)
	cases := []struct {
		query string
		index string
	}{
		{`EXPLAIN QUERY PLAN SELECT id FROM control_outbox WHERE lease_id='aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
		 AND lease_revision=1 AND delivery_state='queued' AND id>0 AND id<=100 ORDER BY id LIMIT 101`,
			"idx_control_outbox_lease"},
		{`EXPLAIN QUERY PLAN SELECT command_id FROM control_commands WHERE status='pending_confirmation'
		 AND expires_at<='2026-08-21T12:00:00.000Z' ORDER BY expires_at,command_id LIMIT 100`,
			"idx_control_commands_status"},
		{`EXPLAIN QUERY PLAN SELECT lease_id FROM control_capability_leases WHERE revoked_at IS NULL
		 AND expires_at<='2026-08-21T12:00:00.000Z' ORDER BY expires_at,lease_id LIMIT 100`,
			"idx_control_leases_expiry"},
	}
	for _, testCase := range cases {
		rows, err := database.Query(testCase.query)
		if err != nil {
			t.Fatal(err)
		}
		var detail strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var line string
			if err := rows.Scan(&id, &parent, &unused, &line); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			detail.WriteString(line)
		}
		rows.Close()
		if !strings.Contains(detail.String(), testCase.index) {
			t.Fatalf("plan %q does not use %s", detail.String(), testCase.index)
		}
	}
}

func TestPullAndReconcileHaveBoundedActualQueryCounts(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, _ := seedGrantTarget(t, database)
	runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
	lease, err := NewService(database, Options{}).IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"input.respond"},
		OperationKeyDigest: sha256.Sum256([]byte("query-count-lease"))})
	if err != nil {
		t.Fatal(err)
	}
	var databaseSequence int
	var databaseName, databasePath string
	if err := database.QueryRow(`PRAGMA database_list`).Scan(&databaseSequence, &databaseName, &databasePath); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	storedb.DB = nil

	counter := &atomic.Int64{}
	driverName := fmt.Sprintf("supervision-counting-sqlite-%d", countingDriverSequence.Add(1))
	sql.Register(driverName, countingDriver{inner: &sqlite.Driver{}, count: counter})
	countingDB, err := sql.Open(driverName, databasePath+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = countingDB.Close() })
	countingDB.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
		if _, err := countingDB.Exec(pragma); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(countingDB, Options{})
	probeTx, err := countingDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.ReauthorizePrincipalTx(context.Background(), probeTx, runner, time.Now().UTC()); err != nil {
		_ = probeTx.Rollback()
		t.Fatalf("counting driver reauthorize: %v", err)
	}
	if _, err := loadCurrentLeaseRecordTx(context.Background(), probeTx, runner, lease.LeaseID, lease.Revision, "", ""); err != nil {
		_ = probeTx.Rollback()
		t.Fatalf("counting driver lease load: %v", err)
	}
	_ = probeTx.Rollback()

	counter.Store(0)
	if _, err := service.Pull(context.Background(), runner, PullRequest{LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, DeviceID: "runner-01"}); err != nil {
		t.Fatalf("pull: %v (%s)", err, ErrorCode(err))
	}
	pullQueries := counter.Load()
	if pullQueries < 1 || pullQueries > 8 {
		t.Fatalf("Pull executed %d statements, bound is 8", pullQueries)
	}

	counter.Store(0)
	if _, err := service.Reconcile(context.Background(), runner, ReconcileRequest{Mode: ReconcileRunner,
		LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, DeviceID: "runner-01", Limit: 100}); err != nil {
		t.Fatalf("reconcile: %v (%s)", err, ErrorCode(err))
	}
	reconcileQueries := counter.Load()
	if reconcileQueries < 1 || reconcileQueries > 8 {
		t.Fatalf("Reconcile executed %d statements, bound is 8", reconcileQueries)
	}
}

func TestReconcileScopesEveryMutationAndCountToCurrentOwnedAuthority(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryA, userA, actorA := seedGrantTargetNamed(t, database, "-a", "SPA",
		"aaaaaaaa-1234-4234-9234-123456789abc")
	deliveryB, userB, actorB := seedGrantTargetNamed(t, database, "-b", "SPB",
		"bbbbbbbb-1234-4234-9234-123456789abc")
	clockA, clockB := &mutableClock{now: time.Now().UTC()}, &mutableClock{now: time.Now().UTC()}
	actorServiceA, actorServiceB := NewService(database, Options{Clock: clockA}), NewService(database, Options{Clock: clockB})
	actorServiceA.grantTTL, actorServiceB.grantTTL = 350*time.Millisecond, 350*time.Millisecond
	grantA, err := actorServiceA.IssueActorGrant(context.Background(), actorA, GrantIssueRequest{
		DeliveryID: deliveryA, OperationKeyDigest: sha256.Sum256([]byte("privacy-grant-a")),
	})
	if err != nil {
		t.Fatalf("issue grant A: %v (%s)", err, ErrorCode(err))
	}
	clockB.Set(time.Now().UTC())
	grantB, err := actorServiceB.IssueActorGrant(context.Background(), actorB, GrantIssueRequest{
		DeliveryID: deliveryB, OperationKeyDigest: sha256.Sum256([]byte("privacy-grant-b")),
	})
	if err != nil {
		t.Fatalf("issue grant B: %v (%s)", err, ErrorCode(err))
	}

	// Move A's issue after capability capture. Access must be resolved from the
	// new current project, not from grant.project_id.
	projectCResult, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Moved','SPC','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectC, _ := projectCResult.LastInsertId()
	if _, err := database.Exec(`UPDATE issues SET project_id=? WHERE id=(SELECT issue_id FROM deliveries WHERE id=?)`,
		projectC, deliveryA); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer')`,
		projectC, userA); err != nil {
		t.Fatal(err)
	}

	clockA.Set(grantA.ExpiresAt.Add(-time.Millisecond))
	if before, err := actorServiceA.Reconcile(context.Background(), actorA,
		ReconcileRequest{Mode: ReconcileActor, Limit: 1}); err != nil || before.ExpiredGrants != 0 {
		t.Fatalf("T-1 actor reconcile projection=%+v err=%v", before, err)
	}
	waitForSQLiteTime(t, database, grantA.ExpiresAt)
	clockA.Set(grantA.ExpiresAt)
	if viewed, err := actorServiceA.Reconcile(context.Background(), actorA,
		ReconcileRequest{Mode: ReconcileActor, Limit: 1}); err != nil || viewed != (ReconcileProjection{}) {
		t.Fatalf("viewer reconcile disclosed/mutated projection=%+v err=%v", viewed, err)
	}
	if _, err := database.Exec(`UPDATE project_members SET access_level='editor' WHERE project_id=? AND user_id=?`,
		projectC, userA); err != nil {
		t.Fatal(err)
	}
	owned, err := actorServiceA.Reconcile(context.Background(), actorA,
		ReconcileRequest{Mode: ReconcileActor, Limit: 1})
	if err != nil || owned.ExpiredGrants != 1 {
		t.Fatalf("owned actor reconcile projection=%+v err=%v code=%s", owned, err, ErrorCode(err))
	}
	var revokedA, revokedB bool
	if err := database.QueryRow(`SELECT revoked_at IS NOT NULL FROM control_capability_grants WHERE grant_id=? AND revision=?`,
		grantA.GrantID, grantA.Revision).Scan(&revokedA); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT revoked_at IS NOT NULL FROM control_capability_grants WHERE grant_id=? AND revision=?`,
		grantB.GrantID, grantB.Revision).Scan(&revokedB); err != nil {
		t.Fatal(err)
	}
	if !revokedA || revokedB {
		t.Fatalf("cross-user grant mutation A=%v B=%v", revokedA, revokedB)
	}
	if userA == userB {
		t.Fatal("test users unexpectedly equal")
	}
	clockB.Set(grantB.ExpiresAt.Add(time.Millisecond))
	waitForSQLiteTime(t, database, grantB.ExpiresAt)
	if ownedB, err := actorServiceB.Reconcile(context.Background(), actorB,
		ReconcileRequest{Mode: ReconcileActor, Limit: 1}); err != nil || ownedB.ExpiredGrants != 1 {
		t.Fatalf("actor B owned reconcile projection=%+v err=%v", ownedB, err)
	}
}

func TestRunnerReconcileRejectsCrossKeyDeviceLeaseAndProjectAccess(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryA, humanA, actorA := seedGrantTargetNamed(t, database, "-ra", "SRA",
		"cccccccc-1234-4234-9234-123456789abc")
	deliveryB, humanB, actorB := seedGrantTargetNamed(t, database, "-rb", "SRB",
		"dddddddd-1234-4234-9234-123456789abc")
	runA, runnerA := seedRunnerActivationNamed(t, database, deliveryA, humanA, "-a", "runner-a")
	runB, runnerB := seedRunnerActivationNamed(t, database, deliveryB, humanB, "-b", "runner-b")
	clockA, clockB := &mutableClock{now: time.Now().UTC()}, &mutableClock{now: time.Now().UTC()}
	serviceA, serviceB := NewService(database, Options{Clock: clockA}), NewService(database, Options{Clock: clockB})
	leaseA, err := serviceA.IssueRunnerLease(context.Background(), runnerA, LeaseIssueRequest{RunID: runA,
		DeviceID: "runner-a", SupportedActions: []Action{"input.respond", "run.pause", "run.resume"},
		OperationKeyDigest: sha256.Sum256([]byte("privacy-lease-a"))})
	if err != nil {
		t.Fatalf("issue lease A: %v (%s)", err, ErrorCode(err))
	}
	clockA.Set(time.Now().UTC())
	inputA, err := serviceA.CreateInputRequest(context.Background(), runnerA, InputCreateRequest{LeaseID: leaseA.LeaseID,
		LeaseRevision: leaseA.Revision, Kind: "approval", PromptTemplate: "approval_required",
		OperationKeyDigest: sha256.Sum256([]byte("privacy-input-a"))})
	if err != nil {
		t.Fatalf("create input A: %v (%s)", err, ErrorCode(err))
	}
	clockB.Set(time.Now().UTC())
	leaseB, err := serviceB.IssueRunnerLease(context.Background(), runnerB, LeaseIssueRequest{RunID: runB,
		DeviceID: "runner-b", SupportedActions: []Action{"input.respond", "run.pause", "run.resume"},
		OperationKeyDigest: sha256.Sum256([]byte("privacy-lease-b"))})
	if err != nil {
		t.Fatalf("issue lease B: %v (%s)", err, ErrorCode(err))
	}
	claimLostEffect := func(label string, service *Service, actor, runner auth.Principal, deliveryID, runID int64,
		lease LeaseProjection, device string) string {
		t.Helper()
		grant, err := service.IssueActorGrant(context.Background(), actor, GrantIssueRequest{DeliveryID: deliveryID,
			OperationKeyDigest: sha256.Sum256([]byte(label + "-grant"))})
		if err != nil {
			t.Fatalf("%s grant: %v (%s)", label, err, ErrorCode(err))
		}
		command, err := service.CreateCommand(context.Background(), actor, CommandCreateRequest{GrantID: grant.GrantID,
			GrantRevision: grant.Revision, Action: "run.pause", RunID: runID, RuntimeRevision: 1,
			OperationKeyDigest: sha256.Sum256([]byte(label + "-command"))})
		if err != nil {
			t.Fatalf("%s command: %v (%s)", label, err, ErrorCode(err))
		}
		command, err = service.ConfirmCommand(context.Background(), actor, CommandConfirmRequest{CommandID: command.CommandID,
			StatusRevision: 1, OperationKeyDigest: sha256.Sum256([]byte(label + "-confirm"))})
		if err != nil {
			t.Fatalf("%s confirm: %v (%s)", label, err, ErrorCode(err))
		}
		if _, err := service.Claim(context.Background(), runner, ClaimRequest{CommandID: command.CommandID,
			LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1, DeviceID: device,
			OperationKeyDigest: sha256.Sum256([]byte(label + "-claim"))}); err != nil {
			t.Fatalf("%s claim: %v (%s)", label, err, ErrorCode(err))
		}
		return command.CommandID
	}
	commandA := claimLostEffect("privacy-a", serviceA, actorA, runnerA, deliveryA, runA, leaseA, "runner-a")
	commandB := claimLostEffect("privacy-b", serviceB, actorB, runnerB, deliveryB, runB, leaseB, "runner-b")

	requestA := ReconcileRequest{Mode: ReconcileRunner, Limit: 10, LeaseID: leaseA.LeaseID,
		LeaseRevision: leaseA.Revision, DeviceID: "runner-a"}
	wrongLease := requestA
	wrongLease.LeaseID, wrongLease.LeaseRevision, wrongLease.DeviceID = leaseB.LeaseID, leaseB.Revision, "runner-b"
	if projection, err := serviceA.Reconcile(context.Background(), runnerA, wrongLease); !IsCode(err, CodeForbidden) ||
		projection != (ReconcileProjection{}) {
		t.Fatalf("cross-key reconcile projection=%+v err=%v code=%s", projection, err, ErrorCode(err))
	}
	wrongDevice := requestA
	wrongDevice.DeviceID = "runner-b"
	if projection, err := serviceA.Reconcile(context.Background(), runnerA, wrongDevice); !IsCode(err, CodeForbidden) ||
		projection != (ReconcileProjection{}) {
		t.Fatalf("cross-device reconcile projection=%+v err=%v code=%s", projection, err, ErrorCode(err))
	}

	movedProjectResult, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Runner moved','SRM','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectA, _ := movedProjectResult.LastInsertId()
	if _, err := database.Exec(`UPDATE issues SET project_id=? WHERE id=(SELECT issue_id FROM deliveries WHERE id=?)`,
		projectA, deliveryA); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer')`,
		projectA, runnerA.UserID()); err != nil {
		t.Fatal(err)
	}
	if projection, err := serviceA.Reconcile(context.Background(), runnerA, requestA); !IsCode(err, CodeForbidden) ||
		projection != (ReconcileProjection{}) {
		t.Fatalf("runner viewer reconcile projection=%+v err=%v code=%s", projection, err, ErrorCode(err))
	}
	if _, err := database.Exec(`UPDATE project_members SET access_level='editor' WHERE project_id=? AND user_id=?`,
		projectA, runnerA.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := serviceA.RevokeRunnerLease(context.Background(), runnerA, LeaseRevokeRequest{LeaseID: leaseA.LeaseID,
		Revision: leaseA.Revision, DeviceID: "runner-a",
		OperationKeyDigest: sha256.Sum256([]byte("privacy-a-revoke"))}); err != nil {
		t.Fatalf("revoke owned lease: %v (%s)", err, ErrorCode(err))
	}
	owned, err := serviceA.Reconcile(context.Background(), runnerA, requestA)
	if err != nil || owned.ExpiredLeases != 0 || owned.ExpiredInputs != 0 || owned.UnknownOutcomes != 1 {
		t.Fatalf("owned runner reconcile projection=%+v err=%v code=%s input=%+v", owned, err, ErrorCode(err), inputA)
	}
	var leaseARevoked, leaseBRevoked bool
	if err := database.QueryRow(`SELECT revoked_at IS NOT NULL FROM control_capability_leases WHERE lease_id=? AND revision=?`,
		leaseA.LeaseID, leaseA.Revision).Scan(&leaseARevoked); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT revoked_at IS NOT NULL FROM control_capability_leases WHERE lease_id=? AND revision=?`,
		leaseB.LeaseID, leaseB.Revision).Scan(&leaseBRevoked); err != nil {
		t.Fatal(err)
	}
	if !leaseARevoked || leaseBRevoked {
		t.Fatalf("cross-key lease mutation A=%v B=%v", leaseARevoked, leaseBRevoked)
	}
	var outcomeA, outcomeB, reasonA, reasonB sql.NullString
	if err := database.QueryRow(`SELECT command.outcome,outbox.safe_reason FROM control_commands command
		JOIN control_outbox outbox ON outbox.command_id=command.command_id WHERE command.command_id=?`, commandA).
		Scan(&outcomeA, &reasonA); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT command.outcome,outbox.safe_reason FROM control_commands command
		JOIN control_outbox outbox ON outbox.command_id=command.command_id WHERE command.command_id=?`, commandB).
		Scan(&outcomeB, &reasonB); err != nil {
		t.Fatal(err)
	}
	if outcomeA.String != "outcome_unknown" || reasonA.String != "runner_lost" || outcomeB.Valid || reasonB.Valid {
		t.Fatalf("cross-key effect mutation A=%v/%v B=%v/%v", outcomeA, reasonA, outcomeB, reasonB)
	}
}

func TestReconcileRealMutationExpiryBoundaries(t *testing.T) {
	boundaries := []struct {
		name  string
		delta time.Duration
		want  int
	}{{"T-minus-1ms", -time.Millisecond, 0}, {"T", 0, 1}, {"T-plus-1ms", time.Millisecond, 1}}

	for _, boundary := range boundaries {
		t.Run("grant/"+boundary.name, func(t *testing.T) {
			database := openSupervisionTestDB(t)
			deliveryID, _, actor := seedGrantTarget(t, database)
			clock := &mutableClock{now: time.Now().UTC()}
			service := NewService(database, Options{Clock: clock})
			service.grantTTL = 250 * time.Millisecond
			grant, err := service.IssueActorGrant(context.Background(), actor, GrantIssueRequest{DeliveryID: deliveryID,
				OperationKeyDigest: sha256.Sum256([]byte("boundary-grant-" + boundary.name))})
			if err != nil {
				t.Fatal(err)
			}
			if boundary.delta >= 0 {
				waitForSQLiteTime(t, database, grant.ExpiresAt)
			}
			clock.Set(grant.ExpiresAt.Add(boundary.delta))
			projection, err := service.Reconcile(context.Background(), actor, ReconcileRequest{Mode: ReconcileActor})
			if err != nil || projection.ExpiredGrants != boundary.want {
				t.Fatalf("projection=%+v err=%v want=%d", projection, err, boundary.want)
			}
		})

		t.Run("pending-command/"+boundary.name, func(t *testing.T) {
			database := openSupervisionTestDB(t)
			deliveryID, _, actor := seedGrantTarget(t, database)
			clock := &mutableClock{now: time.Now().UTC()}
			service := NewService(database, Options{Clock: clock})
			service.grantTTL, service.challengeTTL = 2*time.Second, 250*time.Millisecond
			grant, err := service.IssueActorGrant(context.Background(), actor, GrantIssueRequest{DeliveryID: deliveryID,
				OperationKeyDigest: sha256.Sum256([]byte("boundary-command-grant-" + boundary.name))})
			if err != nil {
				t.Fatal(err)
			}
			clock.Set(time.Now().UTC())
			command, err := service.CreateCommand(context.Background(), actor, CommandCreateRequest{GrantID: grant.GrantID,
				GrantRevision: grant.Revision, Action: "issue.priority.set", Priority: "high",
				OperationKeyDigest: sha256.Sum256([]byte("boundary-command-" + boundary.name))})
			if err != nil {
				t.Fatal(err)
			}
			if boundary.delta >= 0 {
				waitForSQLiteTime(t, database, command.ExpiresAt)
			}
			clock.Set(command.ExpiresAt.Add(boundary.delta))
			projection, err := service.Reconcile(context.Background(), actor, ReconcileRequest{Mode: ReconcileActor})
			if err != nil || projection.ExpiredCommands != boundary.want {
				t.Fatalf("projection=%+v err=%v want=%d", projection, err, boundary.want)
			}
		})

		t.Run("lease/"+boundary.name, func(t *testing.T) {
			database := openSupervisionTestDB(t)
			deliveryID, humanID, _ := seedGrantTarget(t, database)
			runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
			clock := &mutableClock{now: time.Now().UTC()}
			service := NewService(database, Options{Clock: clock})
			service.leaseTTL = 250 * time.Millisecond
			lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
				DeviceID: "runner-01", SupportedActions: []Action{"input.respond"},
				OperationKeyDigest: sha256.Sum256([]byte("boundary-lease-" + boundary.name))})
			if err != nil {
				t.Fatal(err)
			}
			if boundary.delta >= 0 {
				waitForSQLiteTime(t, database, lease.ExpiresAt)
			}
			clock.Set(lease.ExpiresAt.Add(boundary.delta))
			projection, err := service.Reconcile(context.Background(), runner, ReconcileRequest{Mode: ReconcileRunner,
				LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, DeviceID: "runner-01"})
			if err != nil || projection.ExpiredLeases != boundary.want {
				t.Fatalf("projection=%+v err=%v want=%d", projection, err, boundary.want)
			}
		})

		t.Run("input/"+boundary.name, func(t *testing.T) {
			database := openSupervisionTestDB(t)
			deliveryID, humanID, _ := seedGrantTarget(t, database)
			runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
			clock := &mutableClock{now: time.Now().UTC()}
			service := NewService(database, Options{Clock: clock})
			service.leaseTTL, service.inputTTL = 2*time.Second, 250*time.Millisecond
			lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
				DeviceID: "runner-01", SupportedActions: []Action{"input.respond"},
				OperationKeyDigest: sha256.Sum256([]byte("boundary-input-lease-" + boundary.name))})
			if err != nil {
				t.Fatal(err)
			}
			clock.Set(time.Now().UTC())
			input, err := service.CreateInputRequest(context.Background(), runner, InputCreateRequest{LeaseID: lease.LeaseID,
				LeaseRevision: lease.Revision, Kind: "approval", PromptTemplate: "approval_required",
				OperationKeyDigest: sha256.Sum256([]byte("boundary-input-" + boundary.name))})
			if err != nil {
				t.Fatal(err)
			}
			if boundary.delta >= 0 {
				waitForSQLiteTime(t, database, input.ExpiresAt)
			}
			clock.Set(input.ExpiresAt.Add(boundary.delta))
			projection, err := service.Reconcile(context.Background(), runner, ReconcileRequest{Mode: ReconcileRunner,
				LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, DeviceID: "runner-01"})
			if err != nil || projection.ExpiredInputs != boundary.want {
				t.Fatalf("projection=%+v err=%v want=%d", projection, err, boundary.want)
			}
		})
	}
}

func TestRunnerLeaseRevocationIsAtomicCancellationBarrier(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, human := seedGrantTarget(t, database)
	runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
	service := NewService(database, Options{})
	lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running"},
		OperationKeyDigest: sha256.Sum256([]byte("success-barrier-lease"))})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: sha256.Sum256([]byte("success-barrier-grant"))})
	if err != nil {
		t.Fatal(err)
	}
	command, err := service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "run.cancel.running", RunID: runID,
		OperationKeyDigest: sha256.Sum256([]byte("success-barrier-cancel"))})
	if err != nil {
		t.Fatal(err)
	}
	revoke := LeaseRevokeRequest{LeaseID: lease.LeaseID, Revision: lease.Revision, DeviceID: "runner-01",
		OperationKeyDigest: sha256.Sum256([]byte("success-barrier-revoke"))}
	if _, err := service.RevokeRunnerLease(context.Background(), runner, revoke); !IsCode(err, CodeStaleTarget) {
		t.Fatalf("pending cancellation did not block revoke: err=%v code=%s", err, ErrorCode(err))
	}
	var revoked bool
	if err := database.QueryRow(`SELECT revoked_at IS NOT NULL FROM control_capability_leases
		WHERE lease_id=? AND revision=?`, lease.LeaseID, lease.Revision).Scan(&revoked); err != nil || revoked {
		t.Fatalf("blocked revocation mutated lease: revoked=%v err=%v", revoked, err)
	}
	if _, err := service.WithdrawCommand(context.Background(), human, CommandWithdrawRequest{CommandID: command.CommandID,
		StatusRevision: 1, OperationKeyDigest: sha256.Sum256([]byte("success-barrier-withdraw"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RevokeRunnerLease(context.Background(), runner, revoke); err != nil {
		t.Fatalf("withdrawn cancellation still blocked revoke: %v (%s)", err, ErrorCode(err))
	}
}

func TestProjectLifecycleRealServiceActionMatrix(t *testing.T) {
	for _, status := range []string{"active", "frozen", "archived", "deleted"} {
		t.Run(status, func(t *testing.T) {
			database := openSupervisionTestDB(t)
			deliveryID, humanID, human := seedGrantTarget(t, database)
			runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
			var projectID int64
			if err := database.QueryRow(`SELECT issue.project_id FROM deliveries delivery JOIN issues issue
				ON issue.id=delivery.issue_id WHERE delivery.id=?`, deliveryID).Scan(&projectID); err != nil {
				t.Fatal(err)
			}
			if status != "active" {
				if _, err := database.Exec(`UPDATE projects SET status=? WHERE id=?`, status, projectID); err != nil {
					t.Fatal(err)
				}
			}
			service := NewService(database, Options{})
			lease, leaseErr := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
				DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running", "input.respond", "run.pause", "run.resume"},
				OperationKeyDigest: sha256.Sum256([]byte("matrix-lease-" + status))})
			var input InputRequestProjection
			var inputErr error
			if status == "active" || status == "frozen" {
				input, inputErr = service.CreateInputRequest(context.Background(), runner, InputCreateRequest{LeaseID: lease.LeaseID,
					LeaseRevision: lease.Revision, Kind: "approval", PromptTemplate: "approval_required",
					OperationKeyDigest: sha256.Sum256([]byte("matrix-input-" + status))})
			}
			grant, grantErr := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
				OperationKeyDigest: sha256.Sum256([]byte("matrix-grant-" + status))})
			if status == "deleted" {
				if leaseErr == nil || grantErr == nil || len(grant.Targets) != 0 {
					t.Fatalf("deleted project exposed controls lease=%+v/%v grant=%+v/%v", lease, leaseErr, grant, grantErr)
				}
				return
			}
			if leaseErr != nil || grantErr != nil || inputErr != nil {
				t.Fatalf("issue controls status=%s lease=%+v/%v grant=%+v/%v input=%+v/%v",
					status, lease, leaseErr, grant, grantErr, input, inputErr)
			}
			if status == "archived" {
				if len(grant.Actions) != 1 || grant.Actions[0] != "run.cancel.running" || len(lease.Actions) != 1 ||
					lease.Actions[0] != "run.cancel.running" || !grantHasExactRunTarget(grant, "run.cancel.running", runID) {
					t.Fatalf("archived action surface grant=%v lease=%v", grant.Actions, lease.Actions)
				}
				if _, err := service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: grant.GrantID,
					GrantRevision: grant.Revision, Action: "run.cancel.running", RunID: runID,
					OperationKeyDigest: sha256.Sum256([]byte("matrix-archived-cancel"))}); err != nil {
					t.Fatalf("archived cancel rejected: %v (%s)", err, ErrorCode(err))
				}
				if _, err := service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: grant.GrantID,
					GrantRevision: grant.Revision, Action: "issue.priority.set", Priority: "high",
					OperationKeyDigest: sha256.Sum256([]byte("matrix-archived-priority"))}); err == nil {
					t.Fatal("archived priority command accepted")
				}
				return
			}
			for _, action := range []Action{"issue.priority.set", "run.cancel.running", "input.respond", "run.pause"} {
				if !containsAction(grant.Actions, action) {
					t.Fatalf("%s grant omitted %s: %v", status, action, grant.Actions)
				}
			}
			if containsAction(grant.Actions, "run.resume") {
				t.Fatalf("%s grant advertised resume from running runtime: %v", status, grant.Actions)
			}
			for _, action := range []Action{"issue.priority.set", "run.cancel.running", "input.respond", "run.pause"} {
				if !grantHasTarget(grant, action) {
					t.Fatalf("%s grant omitted target %s: %+v", status, action, grant.Targets)
				}
			}
			requests := []CommandCreateRequest{
				{GrantID: grant.GrantID, GrantRevision: grant.Revision, Action: "issue.priority.set", Priority: "high"},
				{GrantID: grant.GrantID, GrantRevision: grant.Revision, Action: "run.cancel.running", RunID: runID},
				{GrantID: grant.GrantID, GrantRevision: grant.Revision, Action: "input.respond", InputRequestID: input.RequestID,
					InputRequestRevision: input.Revision, InputResponse: "approve"},
				{GrantID: grant.GrantID, GrantRevision: grant.Revision, Action: "run.pause", RunID: runID, RuntimeRevision: 1},
			}
			for index := range requests {
				requests[index].OperationKeyDigest = sha256.Sum256([]byte(fmt.Sprintf("matrix-%s-%d", status, index)))
				if _, err := service.CreateCommand(context.Background(), human, requests[index]); err != nil {
					t.Fatalf("%s action %s rejected: %v (%s)", status, requests[index].Action, err, ErrorCode(err))
				}
			}
		})
	}
}

func TestInputResponseAndSupersedeRaceConvergesToOneTerminalSeal(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, human := seedGrantTarget(t, database)
	runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
	service := NewService(database, Options{})
	lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"input.respond"},
		OperationKeyDigest: sha256.Sum256([]byte("input-race-lease"))})
	if err != nil {
		t.Fatal(err)
	}
	input, err := service.CreateInputRequest(context.Background(), runner, InputCreateRequest{LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, Kind: "approval", PromptTemplate: "approval_required",
		OperationKeyDigest: sha256.Sum256([]byte("input-race-request"))})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: sha256.Sum256([]byte("input-race-grant"))})
	if err != nil {
		t.Fatal(err)
	}
	command, err := service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "input.respond", InputRequestID: input.RequestID,
		InputRequestRevision: input.Revision, InputResponse: "approve",
		OperationKeyDigest: sha256.Sum256([]byte("input-race-command"))})
	if err != nil {
		t.Fatal(err)
	}
	command, err = service.ConfirmCommand(context.Background(), human, CommandConfirmRequest{CommandID: command.CommandID,
		StatusRevision: 1, OperationKeyDigest: sha256.Sum256([]byte("input-race-confirm"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Claim(context.Background(), runner, ClaimRequest{CommandID: command.CommandID,
		LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1, DeviceID: "runner-01",
		OperationKeyDigest: sha256.Sum256([]byte("input-race-claim"))}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, resultErr := service.RecordResult(context.Background(), runner, ResultRequest{CommandID: command.CommandID,
			LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1, ClaimSequence: 1,
			ResultSequence: 1, DeviceID: "runner-01", Outcome: "applied",
			OperationKeyDigest: sha256.Sum256([]byte("input-race-result"))})
		results <- resultErr
	}()
	go func() {
		<-start
		_, supersedeErr := service.CreateInputRequest(context.Background(), runner, InputCreateRequest{LeaseID: lease.LeaseID,
			LeaseRevision: lease.Revision, RequestID: input.RequestID, Kind: "approval", PromptTemplate: "approval_required",
			OperationKeyDigest: sha256.Sum256([]byte("input-race-supersede"))})
		results <- supersedeErr
	}()
	close(start)
	successes := 0
	for range 2 {
		if resultErr := <-results; resultErr == nil {
			successes++
		} else if !errors.Is(resultErr, ErrConflict) && !errors.Is(resultErr, ErrUnavailable) {
			t.Fatalf("unexpected input race loser: %v (%s)", resultErr, ErrorCode(resultErr))
		}
	}
	if successes != 1 {
		t.Fatalf("input race successes=%d want 1", successes)
	}
	var terminalEvents, terminalAuditEvents, currentRevision, terminalPresent int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_input_resolution_events WHERE request_id=?`, input.RequestID).
		Scan(&terminalEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_events WHERE input_request_id=? AND event_kind IN
		('input_resolved','input_superseded','input_expired','input_cancelled','input_run_terminal')`, input.RequestID).
		Scan(&terminalAuditEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT current_revision,terminal_event_id IS NOT NULL FROM control_input_request_states
		WHERE request_id=?`, input.RequestID).Scan(&currentRevision, &terminalPresent); err != nil {
		t.Fatal(err)
	}
	var seals int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_input_request_seals WHERE request_id=?`, input.RequestID).Scan(&seals); err != nil {
		t.Fatal(err)
	}
	if terminalEvents != 1 || terminalAuditEvents != 1 ||
		!((currentRevision == 1 && terminalPresent == 1 && seals == 1) || (currentRevision == 2 && terminalPresent == 0 && seals == 2)) {
		t.Fatalf("terminal events=%d audit=%d revision=%d terminal=%d seals=%d",
			terminalEvents, terminalAuditEvents, currentRevision, terminalPresent, seals)
	}
}

func TestSecretSentinelCannotCrossRealServiceOrPersistenceBoundary(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, _ := seedGrantTarget(t, database)
	runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
	service := NewService(database, Options{})
	const sentinel = "PAIMOS_SECRET_SENTINEL_DO_NOT_PERSIST_7f91"
	_, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner\n" + sentinel, SupportedActions: []Action{"input.respond"},
		OperationKeyDigest: sha256.Sum256([]byte("secret-invalid-request"))})
	if !IsCode(err, CodeInvalidDevice) || strings.Contains(fmt.Sprint(err), sentinel) {
		t.Fatalf("unsafe invalid-device error=%q code=%s", err, ErrorCode(err))
	}
	lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"input.respond"},
		OperationKeyDigest: sha256.Sum256([]byte("secret-valid-request"))})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", lease), sentinel) {
		t.Fatalf("secret leaked in projection: %+v", lease)
	}

	tables, err := database.Query(`SELECT name FROM sqlite_master WHERE type='table' AND
		(name LIKE 'control_%' OR name IN ('agent_runs','deliveries','issues','projects')) ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tableNames []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			tables.Close()
			t.Fatal(err)
		}
		tableNames = append(tableNames, name)
	}
	if err := tables.Close(); err != nil {
		t.Fatal(err)
	}
	for _, table := range tableNames {
		columns, err := database.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		var columnNames []string
		for columns.Next() {
			var column string
			if err := columns.Scan(&column); err != nil {
				columns.Close()
				t.Fatal(err)
			}
			columnNames = append(columnNames, column)
		}
		columns.Close()
		for _, column := range columnNames {
			var count int
			query := fmt.Sprintf(`SELECT COUNT(*) FROM "%s" WHERE instr(CAST("%s" AS TEXT),?)>0`, table, column)
			if err := database.QueryRow(query, sentinel).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("secret persisted in %s.%s", table, column)
			}
		}
	}
}

func TestInputResponseAndRunTerminalRaceConvergesToOneTerminalEvent(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, human := seedGrantTarget(t, database)
	runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
	service := NewService(database, Options{})
	lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"input.respond"},
		OperationKeyDigest: sha256.Sum256([]byte("terminal-race-lease"))})
	if err != nil {
		t.Fatal(err)
	}
	input, err := service.CreateInputRequest(context.Background(), runner, InputCreateRequest{LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, Kind: "approval", PromptTemplate: "approval_required",
		OperationKeyDigest: sha256.Sum256([]byte("terminal-race-input"))})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: sha256.Sum256([]byte("terminal-race-grant"))})
	if err != nil {
		t.Fatal(err)
	}
	command, err := service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "input.respond", InputRequestID: input.RequestID,
		InputRequestRevision: input.Revision, InputResponse: "approve",
		OperationKeyDigest: sha256.Sum256([]byte("terminal-race-command"))})
	if err != nil {
		t.Fatal(err)
	}
	command, err = service.ConfirmCommand(context.Background(), human, CommandConfirmRequest{CommandID: command.CommandID,
		StatusRevision: 1, OperationKeyDigest: sha256.Sum256([]byte("terminal-race-confirm"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Claim(context.Background(), runner, ClaimRequest{CommandID: command.CommandID,
		LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1, DeviceID: "runner-01",
		OperationKeyDigest: sha256.Sum256([]byte("terminal-race-claim"))}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	resultDone := make(chan error, 1)
	runDone := make(chan error, 1)
	go func() {
		<-start
		_, resultErr := service.RecordResult(context.Background(), runner, ResultRequest{CommandID: command.CommandID,
			LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1, ClaimSequence: 1,
			ResultSequence: 1, DeviceID: "runner-01", Outcome: "applied",
			OperationKeyDigest: sha256.Sum256([]byte("terminal-race-result"))})
		resultDone <- resultErr
	}()
	go func() {
		<-start
		_, updateErr := database.Exec(`UPDATE agent_runs SET status='failed',finished_at=datetime('now')
			WHERE id=? AND status='running'`, runID)
		runDone <- updateErr
	}()
	close(start)
	resultErr, updateErr := <-resultDone, <-runDone
	if updateErr != nil {
		t.Fatalf("terminal truth update: %v", updateErr)
	}
	if resultErr != nil && !errors.Is(resultErr, ErrConflict) && !errors.Is(resultErr, ErrUnavailable) {
		t.Fatalf("unexpected result loser: %v (%s)", resultErr, ErrorCode(resultErr))
	}
	reconciled, err := service.Reconcile(context.Background(), runner, ReconcileRequest{Mode: ReconcileRunner,
		LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, DeviceID: "runner-01"})
	if err != nil {
		t.Fatalf("terminal reconcile: %v (%s)", err, ErrorCode(err))
	}
	if (resultErr == nil && reconciled.TerminalInputs != 0) || (resultErr != nil && reconciled.TerminalInputs != 1) {
		t.Fatalf("resultErr=%v reconcile=%+v", resultErr, reconciled)
	}
	var terminalEvents, terminalAudits, stateTerminal int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_input_resolution_events WHERE request_id=?`, input.RequestID).
		Scan(&terminalEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_events WHERE input_request_id=?
		AND event_kind IN ('input_resolved','input_run_terminal')`, input.RequestID).Scan(&terminalAudits); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT terminal_event_id IS NOT NULL FROM control_input_request_states WHERE request_id=?`,
		input.RequestID).Scan(&stateTerminal); err != nil {
		t.Fatal(err)
	}
	if terminalEvents != 1 || terminalAudits != 1 || stateTerminal != 1 {
		t.Fatalf("terminal events=%d audits=%d state=%d", terminalEvents, terminalAudits, stateTerminal)
	}
}

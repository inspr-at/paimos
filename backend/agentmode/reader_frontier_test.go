package agentmode

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/deliverytrust"
)

func TestReaderRedactsLegacySecretLikeTelemetryBeforeDTOAndTrust(t *testing.T) {
	database := openAgentModeTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Privacy','PRV','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issueID := insertFrontierIssue(t, database, projectID, 1)
	adminID := insertAgentModeUser(t, database, "privacy-admin", "admin", "admin")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,delivery_instrumentation_version)
		VALUES(?,?,?,'running',1)`, issueID, projectID, adminID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects := store.NewEffects()
	if _, err = store.BootstrapRunTx(context.Background(), tx, effects, delivery.RunBootstrap{IssueID: issueID,
		RunID: runID, Mode: "implementation", Actor: delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", adminID)},
		IdempotencyKey: "privacy-bootstrap"}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())

	stamp := now.Format(time.RFC3339Nano)
	insert := func(sequence int64, activity, basis string) int64 {
		t.Helper()
		result, insertErr := database.Exec(`INSERT INTO agent_run_telemetry(
			run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,phase,
			activity,estimate_revision,progress_percent,estimate_source,estimate_confidence,estimate_basis)
			VALUES(?,?,?,'legacy','direct-seed',?,?,'progress','implementing',?,?,?,?,?,?)`, runID, sequence,
			fmt.Sprintf("privacy-%d", sequence), stamp, stamp, activity, sequence, 44, "agent", .9, basis)
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		id, _ := result.LastInsertId()
		if _, insertErr = database.Exec(`INSERT INTO agent_run_telemetry_latest(run_id,telemetry_id,sequence,
			semantic_telemetry_id,estimate_telemetry_id,latest_event_at,latest_semantic_at,latest_estimate_at)
			VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET telemetry_id=excluded.telemetry_id,
			 sequence=excluded.sequence,semantic_telemetry_id=excluded.semantic_telemetry_id,
			 estimate_telemetry_id=excluded.estimate_telemetry_id,latest_event_at=excluded.latest_event_at,
			 latest_semantic_at=excluded.latest_semantic_at,latest_estimate_at=excluded.latest_estimate_at`,
			runID, id, sequence, id, id, stamp, stamp, stamp); insertErr != nil {
			t.Fatal(insertErr)
		}
		return id
	}
	activityCanary := "working sk_live_12345678"
	basisCanary := "basis github_pat_12345678901234567890"
	reader := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time { return now.Add(time.Second) })})
	read := func() Snapshot {
		t.Helper()
		result, readErr := reader.Read(context.Background(), Request{UserID: adminID,
			Filters: Filters{Attention: "all", Health: "all"}})
		if readErr != nil {
			t.Fatal(readErr)
		}
		return result
	}
	baseline := read()
	// Simulate a row persisted before the M145 database backstop existed.
	if _, err := database.Exec(`DROP TRIGGER trg_agent_run_telemetry_delivery_secret_guard`); err != nil {
		t.Fatal(err)
	}
	insert(1, activityCanary, basisCanary)
	unsafe := read()
	raw, _ := json.Marshal(unsafe)
	for _, canary := range []string{activityCanary, basisCanary, "sk_live_", "github_pat_"} {
		if strings.Contains(string(raw), canary) {
			t.Fatalf("legacy secret canary %q leaked in %s", canary, raw)
		}
	}
	if len(unsafe.Rows) != 1 || len(baseline.Rows) != 1 || unsafe.Rows[0].Activity.Text != "" ||
		unsafe.Rows[0].TrustRevision != baseline.Rows[0].TrustRevision || unsafe.Rows[0].Trust.Basis == basisCanary ||
		(unsafe.Rows[0].Progress != nil && unsafe.Rows[0].Progress.Basis != nil && *unsafe.Rows[0].Progress.Basis != "") {
		t.Fatalf("unsafe telemetry influenced projection: %+v", unsafe.Rows)
	}

	shortURLCanary := "https://runner:p@example.test/repo"
	insert(2, shortURLCanary, shortURLCanary)
	shortURLUnsafe := read()
	shortRaw, _ := json.Marshal(shortURLUnsafe)
	if strings.Contains(string(shortRaw), shortURLCanary) || len(shortURLUnsafe.Rows) != 1 ||
		shortURLUnsafe.Rows[0].Activity.Text != "" ||
		shortURLUnsafe.Rows[0].TrustRevision != baseline.Rows[0].TrustRevision {
		t.Fatalf("short URL userinfo influenced projection: %s", shortRaw)
	}

	for index, canary := range []string{"passwd=abcdefgh", "DB_PASSWORD=abcdefgh", "sk_abcdefghijklmnopqrst", "sk-ant-abcdefghijklmnopqrst"} {
		insert(int64(index+3), canary, canary)
		legacyUnsafe := read()
		legacyRaw, _ := json.Marshal(legacyUnsafe)
		if strings.Contains(string(legacyRaw), canary) || len(legacyUnsafe.Rows) != 1 ||
			legacyUnsafe.Rows[0].Activity.Text != "" || legacyUnsafe.Rows[0].TrustRevision != baseline.Rows[0].TrustRevision {
			t.Fatalf("legacy canary %q influenced projection: %s", canary, legacyRaw)
		}
	}

	insert(7, "passwordless auth; sketch approved", "token budget and credential rotation; sk_abcdefghijklmnopqrs")
	safe := read()
	if len(safe.Rows) != 1 || safe.Rows[0].Activity.Text != "passwordless auth; sketch approved" ||
		safe.Rows[0].Progress == nil || safe.Rows[0].Progress.Basis == nil ||
		*safe.Rows[0].Progress.Basis != "token budget and credential rotation; sk_abcdefghijklmnopqrs" {
		t.Fatalf("safe telemetry was over-redacted: %+v", safe.Rows)
	}
}

func TestReaderRedactsRetainedSecretLikeBlockerSummaries(t *testing.T) {
	database := openAgentModeTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Blocker privacy','BPR','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issueID := insertFrontierIssue(t, database, projectID, 1)
	adminID := insertAgentModeUser(t, database, "blocker-privacy-admin", "admin", "admin")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	actor := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", adminID)}
	attempt, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID,
		Actor: actor, Policies: delivery.DefaultPolicy(), ReasonCode: "instrumentation", IdempotencyKey: "blocker-privacy-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	reporter := delivery.Actor{Type: "external", OpaqueKey: "blocker-privacy-reporter"}
	stage, err := store.StartStageRetry(context.Background(), delivery.StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: delivery.StageSpecification, Reporter: reporter,
		ReasonCode: "stage_start", IdempotencyKey: "blocker-privacy-stage"})
	if err != nil {
		t.Fatal(err)
	}
	sequence := int64(1)
	if _, err := store.ReportStage(context.Background(), delivery.StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: stage.StageKey, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: stage.AuthorityEpoch, Reporter: reporter, SourceSequence: &sequence,
		IdempotencyKey: "blocker-privacy-wait", Kind: "semantic", State: "waiting", NeedsInput: true,
		Blockers: []delivery.Blocker{{Key: "approval", Class: "input", Summary: "Safe approval", HumanWait: true}},
	}); err != nil {
		t.Fatal(err)
	}
	var blockerEventID int64
	if err := database.QueryRow(`SELECT stage_event_id FROM delivery_stage_blockers
		WHERE blocker_key='approval' ORDER BY stage_event_id DESC LIMIT 1`).Scan(&blockerEventID); err != nil {
		t.Fatal(err)
	}
	// Simulate an M144-retained/direct-seeded row after the append-only and
	// privacy guards have been removed by an offline repair. Projection remains
	// sterile even when storage predates the stronger M145 corpus.
	if _, err := database.Exec(`DROP TRIGGER trg_delivery_blockers_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TRIGGER trg_delivery_blocker_secret_guard`); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time { return now.Add(time.Second) })})
	read := func() Snapshot {
		t.Helper()
		snapshot, readErr := reader.Read(context.Background(), Request{UserID: adminID,
			Filters: Filters{Attention: "all", Health: "all"}})
		if readErr != nil {
			t.Fatal(readErr)
		}
		return snapshot
	}
	assertBlocker := func(snapshot Snapshot, wantText string) bool {
		if len(snapshot.Rows) != 1 || len(snapshot.Rows[0].Blockers) != 1 ||
			snapshot.Rows[0].Blockers[0].Kind != "input" || snapshot.Rows[0].Blockers[0].Text != wantText {
			return false
		}
		for _, projectedStage := range snapshot.Rows[0].Stages {
			if projectedStage.Key == delivery.StageSpecification {
				return len(projectedStage.Blockers) == 1 && projectedStage.Blockers[0].Kind == "input" &&
					projectedStage.Blockers[0].Text == wantText
			}
		}
		return false
	}
	for _, canary := range []string{
		"passwd=abcdefgh", "DB_PASSWORD=abcdefgh", "GITHUB_TOKEN=abcdefgh", "OPENAI_API_KEY=abcdefgh",
		"sk_abcdefghijklmnopqrst", "sk-ant-abcdefghijklmnopqrst", "github_pat_12345678901234567890",
		"https://runner:p@example.test/repo", "eyJabcdefgh.abcdefgh.abcdefgh",
	} {
		if _, err := database.Exec(`UPDATE delivery_stage_blockers SET summary=? WHERE stage_event_id=? AND ordinal=0`,
			canary, blockerEventID); err != nil {
			t.Fatal(err)
		}
		snapshot := read()
		raw, _ := json.Marshal(snapshot)
		if strings.Contains(string(raw), canary) || !assertBlocker(snapshot, "") {
			t.Fatalf("retained blocker canary %q leaked: %s", canary, raw)
		}
	}
	for _, safe := range []string{"passwordless auth", "token budget", "credential rotation", "sk_abcdefghijklmnopqrs"} {
		if _, err := database.Exec(`UPDATE delivery_stage_blockers SET summary=? WHERE stage_event_id=? AND ordinal=0`,
			safe, blockerEventID); err != nil {
			t.Fatal(err)
		}
		snapshot := read()
		if !assertBlocker(snapshot, safe) {
			t.Fatalf("safe blocker %q was over-redacted: %+v", safe, snapshot.Rows)
		}
	}
}

func TestTrustEstimateFrontierMatchesFullLedgerForEveryReporterSourcePair(t *testing.T) {
	database := openAgentModeTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Frontier','FRT','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	adminID := insertAgentModeUser(t, database, "frontier-admin", "admin", "admin")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	sources := []deliverytrust.EstimateSource{deliverytrust.SourceAgent, deliverytrust.SourceAdapter,
		deliverytrust.SourceProvider, deliverytrust.SourceTool, deliverytrust.SourceExternal}
	issueNumber := 0
	for _, reporter := range []deliverytrust.ReporterKind{deliverytrust.ReporterAgentRun, deliverytrust.ReporterExternal} {
		for _, source := range sources {
			issueNumber++
			t.Run(string(reporter)+"_"+string(source), func(t *testing.T) {
				issueID := insertFrontierIssue(t, database, projectID, issueNumber)
				var stageKey string
				var full []deliverytrust.EstimateFact
				if reporter == deliverytrust.ReporterExternal {
					stageKey, full = seedExternalFrontier(t, database, store, issueID, adminID, source, now)
				} else {
					stageKey, full = seedAgentFrontier(t, database, store, issueID, projectID, adminID, source, now)
				}
				facts := loadIssueTrustFacts(t, database, issueID)
				stage := facts[issueID][stageKey]
				if stage == nil {
					t.Fatalf("missing %s trust stage", stageKey)
				}
				frontierOutput := evaluateEstimateSet(t, reporter, stage.Estimates, now)
				fullOutput := evaluateEstimateSet(t, reporter, full, now)
				if !reflect.DeepEqual(frontierOutput, fullOutput) {
					t.Fatalf("frontier diverged from full ledger\nfrontier=%+v\nfull=%+v\nfacts=%+v",
						frontierOutput, fullOutput, stage.Estimates)
				}
			})
		}
	}
}

func insertFrontierIssue(t *testing.T, database *sql.DB, projectID int64, number int) int64 {
	t.Helper()
	result, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,?,'ticket',?,'in-progress')`, projectID, number, fmt.Sprintf("Frontier %d", number))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func seedExternalFrontier(t *testing.T, database *sql.DB, store *delivery.Store, issueID, adminID int64,
	testedSource deliverytrust.EstimateSource, now time.Time) (string, []deliverytrust.EstimateFact) {
	t.Helper()
	user := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", adminID)}
	attempt, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID, Actor: user,
		Policies: delivery.DefaultPolicy(), ReasonCode: "instrumentation", IdempotencyKey: fmt.Sprintf("frontier-attempt-%d", issueID)})
	if err != nil {
		t.Fatal(err)
	}
	reporter := delivery.Actor{Type: "external", OpaqueKey: fmt.Sprintf("external:frontier:%d", issueID)}
	stage, err := store.StartStageRetry(context.Background(), delivery.StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: delivery.StageSpecification, Reporter: reporter,
		ReasonCode: "stage_start", IdempotencyKey: fmt.Sprintf("frontier-stage-%d", issueID)})
	if err != nil {
		t.Fatal(err)
	}
	sources := []deliverytrust.EstimateSource{deliverytrust.SourceExternal, testedSource}
	progresses := []float64{80, 90}
	full := make([]deliverytrust.EstimateFact, 0, 2)
	for index := range sources {
		revision, sequence, confidence := int64(index+1), int64(index+1), 0.9
		progress := progresses[index]
		if _, err := store.ReportStage(context.Background(), delivery.StageReport{IssueID: issueID,
			AttemptNumber: attempt.AttemptNumber, StageKey: stage.StageKey, ExecutionNumber: stage.ExecutionNumber,
			AuthorityEpoch: stage.AuthorityEpoch, Reporter: reporter,
			IdempotencyKey: fmt.Sprintf("frontier-estimate-%d-%d", issueID, index), SourceSequence: &sequence, Kind: "estimate",
			Estimate: delivery.EstimateEvidence{Revision: &revision, Progress: &progress, Source: string(sources[index]),
				Confidence: &confidence, Basis: "frontier source matrix"}}); err != nil {
			t.Fatal(err)
		}
		var eventID int64
		if err := database.QueryRow(`SELECT id FROM delivery_stage_events WHERE attempt_id=? AND stage_key=?
			AND event_type='estimate' AND source_sequence=?`, stage.AttemptID, stage.StageKey, sequence).Scan(&eventID); err != nil {
			t.Fatal(err)
		}
		value := progress
		full = append(full, deliverytrust.EstimateFact{Identity: fmt.Sprintf("stage_event:%d", eventID),
			Reporter: deliverytrust.ReporterExternal, Revision: uint64(revision), Sequence: uint64(sequence),
			Source: sources[index], ServerReceivedAt: now, Confidence: confidence, Basis: "frontier source matrix",
			ProgressPercent: &value})
	}
	return stage.StageKey, full
}

func seedAgentFrontier(t *testing.T, database *sql.DB, store *delivery.Store, issueID, projectID, adminID int64,
	testedSource deliverytrust.EstimateSource, now time.Time) (string, []deliverytrust.EstimateFact) {
	t.Helper()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,delivery_instrumentation_version)
		VALUES(?,?,?,'running',1)`, issueID, projectID, adminID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects := store.NewEffects()
	stage, err := store.BootstrapRunTx(context.Background(), tx, effects, delivery.RunBootstrap{IssueID: issueID,
		RunID: runID, Mode: "implementation", Actor: delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", adminID)},
		IdempotencyKey: fmt.Sprintf("frontier-run-%d", runID)})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())
	sources := []deliverytrust.EstimateSource{deliverytrust.SourceAgent, testedSource}
	progresses := []float64{80, 90}
	full := make([]deliverytrust.EstimateFact, 0, 2)
	for index := range sources {
		sequence, revision := int64(index+1), int64(index+1)
		progress := progresses[index]
		telemetryID := insertAgentFrontierEstimate(t, database, runID, sequence, revision, progress, string(sources[index]), now)
		value := progress
		full = append(full, deliverytrust.EstimateFact{Identity: fmt.Sprintf("telemetry:%d", telemetryID),
			Reporter: deliverytrust.ReporterAgentRun, Revision: uint64(revision), Sequence: uint64(sequence),
			Source: sources[index], ServerReceivedAt: now, Confidence: 0.9, Basis: "frontier source matrix",
			ProgressPercent: &value})
	}
	return stage.StageKey, full
}

func insertAgentFrontierEstimate(t *testing.T, database *sql.DB, runID, sequence, revision int64,
	progress float64, source string, now time.Time) int64 {
	t.Helper()
	insert := func(exec interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}) (sql.Result, error) {
		stamp := now.UTC().Format(time.RFC3339Nano)
		return exec.ExecContext(context.Background(), `INSERT INTO agent_run_telemetry(
			run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,phase,
			estimate_revision,progress_percent,estimate_source,estimate_confidence,estimate_basis)
			VALUES(?,?,?,'test','test',?,?,'progress','implementing',?,?,?,?,?)`, runID, sequence,
			fmt.Sprintf("frontier-%d", runID), stamp, stamp, revision, progress, source, 0.9, "frontier source matrix")
	}
	var result sql.Result
	var err error
	if source != string(deliverytrust.SourceExternal) {
		result, err = insert(database)
	} else {
		conn, connErr := database.Conn(context.Background())
		if connErr != nil {
			t.Fatal(connErr)
		}
		defer conn.Close()
		if _, err = conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=ON`); err == nil {
			result, err = insert(conn)
		}
		_, _ = conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=OFF`)
	}
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func loadIssueTrustFacts(t *testing.T, database *sql.DB, issueID int64) trustFacts {
	t.Helper()
	conn, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN DEFERRED`); err != nil {
		t.Fatal(err)
	}
	facts, err := loadTrustFacts(context.Background(), conn, []int64{issueID})
	if err != nil {
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `COMMIT`); err != nil {
		t.Fatal(err)
	}
	return facts
}

func evaluateEstimateSet(t *testing.T, reporter deliverytrust.ReporterKind, estimates []deliverytrust.EstimateFact,
	now time.Time) deliverytrust.Output {
	t.Helper()
	scope := deliverytrust.Scope{AttemptID: "attempt:frontier", PlanID: "plan:frontier",
		ExecutionID: "execution:frontier", AuthorityID: "authority:frontier", ResetID: "reset:frontier",
		ReporterID: "reporter:frontier"}
	if reporter == deliverytrust.ReporterAgentRun {
		scope.RunLinkID = "run-link:frontier"
	}
	input := deliverytrust.Input{DeliveryIdentity: "delivery:frontier", ProjectIdentity: "project:frontier",
		Instrumented: true, CalculatedAt: now, PolicyVersion: deliverytrust.EstimatorPolicyVersion,
		Policy: deliverytrust.DefaultPolicy(), Stages: make([]deliverytrust.StageInput, len(delivery.CanonicalStages))}
	for index, key := range delivery.CanonicalStages {
		input.Stages[index] = deliverytrust.StageInput{Stage: deliverytrust.StageKey(key),
			Scope:      deliverytrust.Scope{AttemptID: scope.AttemptID, PlanID: scope.PlanID},
			Reporter:   deliverytrust.ReporterUnknown,
			Completion: deliverytrust.CompletionInput{Status: deliverytrust.CompletionPending}}
	}
	started := now.Add(-time.Minute)
	input.Stages[0].Scope, input.Stages[0].Reporter, input.Stages[0].ExecutionStartedAt = scope, reporter, &started
	input.Stages[0].Estimates = make([]deliverytrust.EstimateFact, len(estimates))
	for index := range estimates {
		input.Stages[0].Estimates[index] = estimates[index]
		input.Stages[0].Estimates[index].Scope = scope
	}
	output, err := deliverytrust.Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

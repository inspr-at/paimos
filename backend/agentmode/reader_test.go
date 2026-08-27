package agentmode

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	appdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/secretvault"
)

func TestReaderAuthorizationSelectionCanonicalWireAndPrivacy(t *testing.T) {
	database := openAgentModeTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Agent Mode','AM','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,7,'ticket','Safe delivery','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	adminID := insertAgentModeUser(t, database, "agent-admin", "admin", "admin")
	memberID := insertAgentModeUser(t, database, "agent-viewer", "member", "member")
	externalID := insertAgentModeUser(t, database, "agent-external", "external", "external")
	if _, err := database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer'),(?,?,'editor')`,
		projectID, memberID, projectID, externalID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,agent_name,
		delivery_instrumentation_version) VALUES(?,?,?,'running','legacy-private-agent',0)`, issueID, projectID, adminID); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	reader := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	result, err := reader.Read(context.Background(), Request{UserID: memberID, Filters: Filters{Attention: "all", Health: "all"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != SchemaVersion || !result.ServerTime.Equal(now) || len(result.Cursor) != CursorEncodedLength ||
		len(result.Rows) != 1 || result.SelectedDelivery != fmt.Sprintf("issue:%d", issueID) {
		t.Fatalf("unexpected snapshot: %+v", result)
	}
	row := result.Rows[0]
	if row.Capabilities.EditIssue || !row.Capabilities.ViewIssue || row.ProjectID != projectID || row.Health != HealthUnknown ||
		row.Attention.Level != 1 || row.Trust.ReporterKind != string("unknown") {
		t.Fatalf("viewer/safe row mismatch: %+v", row)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"stream_cursor", "selected_outside_results", "legacy-private-agent", "reporter_id", "run_link_id", "reference_value"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("privacy/obsolete field %q leaked in %s", forbidden, raw)
		}
	}

	selected := fmt.Sprintf("issue:%d", issueID)
	outsideFilters := Filters{Attention: "all", Health: "all", Query: "does-not-match", SelectedDelivery: selected}
	outside, err := reader.Read(context.Background(), Request{UserID: memberID, Filters: outsideFilters})
	if err != nil {
		t.Fatal(err)
	}
	if len(outside.Rows) != 0 || outside.SelectedOutside == nil || outside.SelectedOutside.Reason != SelectedFilterExcluded ||
		outside.SelectedOutside.Row.DeliveryID != selected || outside.Aggregates.Root.ActiveTotal != 0 {
		t.Fatalf("selected-outside contract: %+v", outside)
	}
	withoutSelector := outsideFilters
	withoutSelector.SelectedDelivery = ""
	fallback, err := reader.Read(context.Background(), Request{UserID: memberID, Filters: withoutSelector})
	if err != nil {
		t.Fatal(err)
	}
	outsideAggregate, _ := json.Marshal(outside.Aggregates)
	fallbackAggregate, _ := json.Marshal(fallback.Aggregates)
	if string(outsideAggregate) != string(fallbackAggregate) {
		t.Fatalf("selector changed aggregates\nwith=%s\nwithout=%s", outsideAggregate, fallbackAggregate)
	}

	if _, err := reader.Read(context.Background(), Request{UserID: externalID, Filters: Filters{Attention: "all", Health: "all"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("external explicit editor error=%v, want canonical not found", err)
	}
	if _, err := reader.Read(context.Background(), Request{UserID: memberID, DetailDeliveryKey: "delivery:missing",
		Filters: Filters{Attention: "all", Health: "all"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing detail error=%v, want not found", err)
	}
}

func TestReaderSeparatesHumanWaitFromBlockingAcrossExternalAndAgentRunTruth(t *testing.T) {
	database := openAgentModeTestDB(t)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	projectID := insertFixtureProject(t, database, "Attention classification", "ATN")
	adminID := insertAgentModeUser(t, database, "attention-admin", "admin", "admin")

	for index, mode := range []string{"waiting_only", "dependency", "mixed"} {
		issueID := insertFixtureIssue(t, database, projectID, int64(index+1), mode, now.Add(-time.Minute))
		seedCanonicalFixtureDelivery(t, database, adminID, issueID, now.Add(-10*time.Second), mode)
	}
	agentIssueID := insertFixtureIssue(t, database, projectID, 4, "agent_waiting_only", now.Add(-time.Minute))
	seedAgentRunWaitFixture(t, database, adminID, agentIssueID, projectID, now.Add(-10*time.Second))

	reader := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	snapshot, err := reader.Read(context.Background(), Request{UserID: adminID, RouteProjectID: &projectID,
		Filters: Filters{Attention: "all", Health: "all"}})
	if err != nil {
		t.Fatal(err)
	}
	rows := make(map[string]DeliveryRow, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		rows[row.Title] = row
	}
	for _, title := range []string{"waiting_only", "agent_waiting_only"} {
		row := rows[title]
		if row.Attention.Level != 2 || row.Attention.Reason != "waiting_needs_input" || row.Health != HealthAttention ||
			row.attentionFlags.WaitingNeedsInput != 1 || row.attentionFlags.Blocked != 0 {
			t.Errorf("%s projection=%+v flags=%+v", title, row, row.attentionFlags)
		}
	}
	dependency := rows["dependency"]
	if dependency.Attention.Level != 3 || dependency.Attention.Reason != "blocked" || dependency.Health != HealthBlocked ||
		dependency.attentionFlags.WaitingNeedsInput != 0 || dependency.attentionFlags.Blocked != 1 {
		t.Errorf("dependency projection=%+v flags=%+v", dependency, dependency.attentionFlags)
	}
	mixed := rows["mixed"]
	if mixed.Attention.Level != 3 || mixed.Attention.Reason != "blocked" || mixed.Health != HealthBlocked ||
		mixed.attentionFlags.WaitingNeedsInput != 1 || mixed.attentionFlags.Blocked != 1 {
		t.Errorf("mixed projection=%+v flags=%+v", mixed, mixed.attentionFlags)
	}
	if snapshot.Aggregates.Root.ActiveTotal != 4 || snapshot.Aggregates.Root.Flags.Attention != 4 ||
		snapshot.Aggregates.Root.Flags.WaitingNeedsInput != 3 || snapshot.Aggregates.Root.Flags.Blocked != 2 {
		t.Fatalf("all-health aggregate=%+v", snapshot.Aggregates.Root)
	}

	blocked, err := reader.Read(context.Background(), Request{UserID: adminID, RouteProjectID: &projectID,
		Filters: Filters{Attention: "all", Health: "blocked"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked.Rows) != 2 || blocked.Aggregates.Root.ActiveTotal != 2 ||
		blocked.Aggregates.Root.Flags.WaitingNeedsInput != 1 || blocked.Aggregates.Root.Flags.Blocked != 2 {
		t.Fatalf("blocked filter rows=%d aggregate=%+v", len(blocked.Rows), blocked.Aggregates.Root)
	}
	for _, row := range blocked.Rows {
		if row.Title == "waiting_only" || row.Title == "agent_waiting_only" {
			t.Fatalf("human-only wait entered health=blocked filter: %+v", row)
		}
	}
}

func seedAgentRunWaitFixture(t *testing.T, database *sql.DB, userID, issueID, projectID int64, eventAt time.Time) {
	t.Helper()
	current := eventAt
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return current })})
	stamp := eventAt.UTC().Format(time.RFC3339Nano)
	result, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,started_at,
		expects_supervisor_telemetry,delivery_instrumentation_version,created_at) VALUES(?,?,?,'running',?,1,1,?)`,
		issueID, projectID, userID, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects := store.NewEffects()
	if _, err := store.BootstrapRunTx(context.Background(), tx, effects, delivery.RunBootstrap{IssueID: issueID,
		RunID: runID, Mode: "implementation", Actor: delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)},
		IdempotencyKey: fmt.Sprintf("fixture-run-%d-bootstrap", runID)}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())

	current = eventAt.Add(time.Second)
	telemetryAt := current.UTC().Format(time.RFC3339Nano)
	tx, err = database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = tx.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,
		agent_reported_at,server_received_at,kind,heartbeat,phase,activity,needs_input,blocker_state)
		VALUES(?,1,?,'fixture','fixture',?,?,'needs_input',0,'waiting','Waiting for approval',1,'input')`,
		runID, fmt.Sprintf("fixture-run-%d-wait", runID), telemetryAt, telemetryAt)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	telemetryID, _ := result.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO agent_run_telemetry_latest(run_id,telemetry_id,sequence,
		semantic_telemetry_id,latest_event_at,latest_semantic_at) VALUES(?,?,1,?,?,?)`,
		runID, telemetryID, telemetryID, telemetryAt, telemetryAt); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	effects = store.NewEffects()
	if err := store.RecordRunTelemetryChangeTx(context.Background(), tx, effects, runID, 1); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())
}

func TestReaderNormalizesMixedIssueTimesAndRejectsMalformedCatalogTime(t *testing.T) {
	database := openAgentModeTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Time format','TMF','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	adminID := insertAgentModeUser(t, database, "time-format-admin", "admin", "admin")
	want := map[int64]string{}
	for number, raw := range []string{"2026-08-20 12:00:00", "2026-08-20T11:30:00Z"} {
		issue, insertErr := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,updated_at)
			VALUES(?,?,'ticket',?,'in-progress',?)`, projectID, number+1, fmt.Sprintf("Time %d", number+1), raw)
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		issueID, _ := issue.LastInsertId()
		parsed, _ := parseDBTime(raw)
		want[issueID] = parsed.Format(time.RFC3339Nano)
		if _, insertErr = database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
			delivery_instrumentation_version) VALUES(?,?,?,'running',0)`, issueID, projectID, adminID); insertErr != nil {
			t.Fatal(insertErr)
		}
	}
	reader := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time {
		return time.Date(2026, 8, 20, 12, 1, 0, 0, time.UTC)
	})})
	snapshot, err := reader.Read(context.Background(), Request{UserID: adminID,
		Filters: Filters{Attention: "all", Health: "all"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Rows) != 2 {
		t.Fatalf("mixed timestamp rows=%d", len(snapshot.Rows))
	}
	for _, row := range snapshot.Rows {
		if row.UpdatedAt != want[row.IssueID] {
			t.Fatalf("issue %d updated_at=%q want RFC3339 %q", row.IssueID, row.UpdatedAt, want[row.IssueID])
		}
		if _, err := time.Parse(time.RFC3339Nano, row.UpdatedAt); err != nil {
			t.Fatalf("issue %d emitted non-RFC3339 updated_at %q: %v", row.IssueID, row.UpdatedAt, err)
		}
	}
	for issueID := range want {
		if _, err := database.Exec(`UPDATE issues SET updated_at='not-a-time' WHERE id=?`, issueID); err != nil {
			t.Fatal(err)
		}
		break
	}
	if _, err := reader.Read(context.Background(), Request{UserID: adminID,
		Filters: Filters{Attention: "all", Health: "all"}}); !errors.Is(err, ErrInvariant) {
		t.Fatalf("malformed catalog timestamp error=%v, want invariant", err)
	}
}

func TestReaderFailsClosedForAuthorizedUnlinkedV1WithoutHiddenProjectOracle(t *testing.T) {
	database := openAgentModeTestDB(t)
	visibleProject, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Visible v1','VV1','active')`)
	visibleProjectID, _ := visibleProject.LastInsertId()
	hiddenProject, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Hidden v1','HV1','active')`)
	hiddenProjectID, _ := hiddenProject.LastInsertId()
	adminID := insertAgentModeUser(t, database, "v1-invariant-admin", "admin", "admin")
	memberID := insertAgentModeUser(t, database, "v1-invariant-member", "member", "member")
	if _, err := database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES
		(?,?,'viewer'),(?,?,'none')`, visibleProjectID, memberID, hiddenProjectID, memberID); err != nil {
		t.Fatal(err)
	}
	insertIssue := func(projectID int64, number int, title string) int64 {
		t.Helper()
		result, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
			VALUES(?,?,'ticket',?,'in-progress')`, projectID, number, title)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	insertRun := func(issueID, projectID int64, version int) int64 {
		t.Helper()
		result, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
			delivery_instrumentation_version) VALUES(?,?,?,'running',?)`, issueID, projectID, adminID, version)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}

	legacyIssue := insertIssue(visibleProjectID, 1, "Visible v0 control")
	insertRun(legacyIssue, visibleProjectID, 0)
	hiddenBrokenIssue := insertIssue(hiddenProjectID, 1, "Hidden broken v1")
	insertRun(hiddenBrokenIssue, hiddenProjectID, 1)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	reader := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	filters := Filters{Attention: "all", Health: "all"}
	memberRequest := Request{UserID: memberID, Filters: filters}
	memberSnapshot, err := reader.Read(context.Background(), memberRequest)
	if err != nil || len(memberSnapshot.Rows) != 1 || memberSnapshot.Rows[0].IssueID != legacyIssue {
		t.Fatalf("hidden-project corruption affected member: snapshot=%+v err=%v", memberSnapshot, err)
	}
	if _, err := reader.StreamState(context.Background(), memberRequest); err != nil {
		t.Fatalf("hidden-project corruption affected member stream state: %v", err)
	}
	if _, err := reader.Read(context.Background(), Request{UserID: adminID, Filters: filters}); !errors.Is(err, ErrInvariant) {
		t.Fatalf("admin global error=%v, want authorized invariant", err)
	}

	brokenIssue := insertIssue(visibleProjectID, 2, "Visible broken v1")
	insertRun(brokenIssue, visibleProjectID, 1)
	detail := fmt.Sprintf("issue:%d", brokenIssue)
	requests := map[string]Request{
		"global":  memberRequest,
		"project": {UserID: memberID, RouteProjectID: &visibleProjectID, Filters: filters},
		"detail":  {UserID: memberID, DetailDeliveryKey: detail, Filters: filters},
	}
	for name, request := range requests {
		if _, err := reader.Read(context.Background(), request); !errors.Is(err, ErrInvariant) {
			t.Fatalf("%s snapshot error=%v, want invariant", name, err)
		}
		if _, err := reader.StreamState(context.Background(), request); !errors.Is(err, ErrInvariant) {
			t.Fatalf("%s stream state error=%v, want invariant", name, err)
		}
	}
	if _, err := database.Exec(`UPDATE issues SET deleted_at=? WHERE id=?`, now.Format(time.RFC3339Nano), brokenIssue); err != nil {
		t.Fatal(err)
	}

	terminalBrokenIssue := insertIssue(visibleProjectID, 3, "Terminal broken v1")
	terminalBrokenRun := insertRun(terminalBrokenIssue, visibleProjectID, 1)
	if _, err := database.Exec(`UPDATE agent_runs SET status='completed' WHERE id=?`, terminalBrokenRun); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(context.Background(), memberRequest); !errors.Is(err, ErrInvariant) {
		t.Fatalf("terminal unlinked-v1 error=%v, want invariant", err)
	}

	// Linked v1 and synthetic v0 remain valid controls. Hide the deliberately
	// corrupt retained rows, then bootstrap a correct link through the store.
	if _, err := database.Exec(`UPDATE issues SET deleted_at=? WHERE id=?`,
		now.Format(time.RFC3339Nano), terminalBrokenIssue); err != nil {
		t.Fatal(err)
	}
	linkedIssue := insertIssue(visibleProjectID, 4, "Linked v1 control")
	linkedRun := insertRun(linkedIssue, visibleProjectID, 1)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects := store.NewEffects()
	if _, err := store.BootstrapRunTx(context.Background(), tx, effects, delivery.RunBootstrap{
		IssueID: linkedIssue, RunID: linkedRun, Mode: "implementation",
		Actor:          delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", adminID)},
		IdempotencyKey: "linked-v1-control",
	}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())
	baseline, err := reader.Read(context.Background(), memberRequest)
	if err != nil || len(baseline.Rows) != 2 {
		t.Fatalf("linked-v1/v0 controls snapshot=%+v err=%v", baseline, err)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET status='completed' WHERE id=?`, linkedRun); err != nil {
		t.Fatal(err)
	}
	baseline, err = reader.Read(context.Background(), memberRequest)
	if err != nil || len(baseline.Rows) != 2 {
		t.Fatalf("linked terminal-v1 control snapshot=%+v err=%v", baseline, err)
	}

	// Corruption present before Open is a storage invariant, even with a valid
	// resume cursor. Once a session is established, the same invariant becomes
	// an identity-free reset because the HTTP status has already been committed.
	streamer := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	established, err := streamer.Open(context.Background(), memberRequest, baseline.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer established.Close()
	lateIssue := insertIssue(visibleProjectID, 5, "Late broken v1")
	insertRun(lateIssue, visibleProjectID, 1)
	for name, resume := range map[string]string{"fresh": "", "valid-resume": baseline.Cursor} {
		if _, err := streamer.Open(context.Background(), memberRequest, resume); !errors.Is(err, ErrInvariant) {
			t.Fatalf("%s open error=%v, want invariant", name, err)
		}
	}
	if batch, err := established.Drain(context.Background()); !errors.Is(err, ErrReset) || batch.Kind != "" || len(batch.Hints) != 0 {
		t.Fatalf("established invariant batch=%+v err=%v, want identity-free reset", batch, err)
	}
}

func TestReaderUsesOneClockInstantAtEverySupportedScale(t *testing.T) {
	const maxThousandRootQueryLatency = time.Second

	database := openAgentModeTestDB(t)
	adminID := insertAgentModeUser(t, database, "clock-admin", "admin", "admin")
	sizes := []int{1, 10, 100, MaxCandidateRoots}
	projectIDs := make(map[int]int64, len(sizes))
	canonicalIssueIDs := make(map[int]int64, len(sizes))
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range sizes {
		project, insertErr := tx.Exec(`INSERT INTO projects(name,key,status) VALUES(?,?,'active')`,
			fmt.Sprintf("Clock %d", size), fmt.Sprintf("C%d", size))
		if insertErr != nil {
			_ = tx.Rollback()
			t.Fatal(insertErr)
		}
		projectID, _ := project.LastInsertId()
		projectIDs[size] = projectID
		for number := 1; number <= size; number++ {
			issue, insertErr := tx.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
				VALUES(?,?,'ticket',?,'in-progress')`, projectID, number, fmt.Sprintf("Clock delivery %d", number))
			if insertErr != nil {
				_ = tx.Rollback()
				t.Fatal(insertErr)
			}
			issueID, _ := issue.LastInsertId()
			if number == 1 {
				canonicalIssueIDs[size] = issueID
				continue
			}
			if _, insertErr = tx.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,agent_name,
				delivery_instrumentation_version) VALUES(?,?,?,'running',?,0)`, issueID, projectID, adminID,
				fmt.Sprintf("clock-agent-%d-%d", size, number)); insertErr != nil {
				_ = tx.Rollback()
				t.Fatal(insertErr)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time {
		return base.Add(-time.Hour)
	})})
	for _, size := range sizes {
		issueID := canonicalIssueIDs[size]
		attempt, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID,
			Actor: delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", adminID)}, Policies: delivery.DefaultPolicy(),
			ReasonCode: "instrumentation", IdempotencyKey: fmt.Sprintf("clock-%d-attempt", size)})
		if err != nil {
			t.Fatalf("scale %d canonical attempt: %v", size, err)
		}
		reporter := delivery.Actor{Type: "external", OpaqueKey: fmt.Sprintf("external:clock-%d", size)}
		stage, err := store.StartStageRetry(context.Background(), delivery.StageStartRequest{IssueID: issueID,
			AttemptNumber: attempt.AttemptNumber, StageKey: delivery.StageSpecification, Reporter: reporter,
			ReasonCode: "specification_start", IdempotencyKey: fmt.Sprintf("clock-%d-stage", size)})
		if err != nil {
			t.Fatalf("scale %d canonical stage: %v", size, err)
		}
		digestInput := strings.Join([]string{"paimos-issue-spec-v1", "Clock delivery 1", "", ""}, "\x00")
		digest := sha256.Sum256([]byte(digestInput))
		sequence := int64(1)
		if _, err := store.ReportStage(context.Background(), delivery.StageReport{IssueID: issueID,
			AttemptNumber: attempt.AttemptNumber, StageKey: delivery.StageSpecification,
			ExecutionNumber: stage.ExecutionNumber, AuthorityEpoch: stage.AuthorityEpoch,
			Reporter: reporter, IdempotencyKey: fmt.Sprintf("clock-%d-success", size), SourceSequence: &sequence,
			Kind: "semantic", State: "succeeded", Evidence: []delivery.Evidence{{Type: "spec_acceptance",
				Outcome: "passed", ReferenceKind: "digest", DigestSHA256: hex.EncodeToString(digest[:])}}}); err != nil {
			t.Fatalf("scale %d canonical terminal fact: %v", size, err)
		}
	}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("roots_%d", size), func(t *testing.T) {
			calls := 0
			clock := delivery.ClockFunc(func() time.Time {
				calls++
				return base.Add(time.Duration(calls) * time.Second)
			})
			var cursorPlaintext []byte
			codec := NewCursorCodecWithCrypt(clock, time.Minute, func(_ string, plain []byte) ([]byte, error) {
				cursorPlaintext = append([]byte(nil), plain...)
				sealed := make([]byte, cursorCiphertextLength)
				sealed[0] = 1
				return sealed, nil
			}, nil)
			type observedCall struct {
				statement string
				args      []any
			}
			var queryCalls []observedCall
			var execCalls []observedCall
			reader := NewReader(database, ReaderOptions{Clock: clock, Cursor: codec})
			reader.observeDBCall = func(kind, statement string, args []any) {
				if kind == "query" {
					queryCalls = append(queryCalls, observedCall{statement: statement, args: args})
				} else {
					execCalls = append(execCalls, observedCall{statement: statement, args: args})
				}
			}
			projectID := projectIDs[size]
			started := time.Now()
			result, readErr := reader.Read(
				context.Background(), Request{UserID: adminID, RouteProjectID: &projectID,
					Filters: Filters{Attention: "all", Health: "all"}})
			elapsed := time.Since(started)
			if readErr != nil {
				t.Fatal(readErr)
			}
			t.Logf("Agent Mode query latency: roots=%d elapsed=%s budget=%s", size, elapsed, maxThousandRootQueryLatency)
			if size == MaxCandidateRoots && elapsed > maxThousandRootQueryLatency {
				t.Fatalf("1,000-root query latency=%s exceeds budget=%s", elapsed, maxThousandRootQueryLatency)
			}
			captured := base.Add(time.Second)
			if calls != 1 || len(result.Rows) != size || !result.ServerTime.Equal(captured) ||
				!result.Aggregates.CalculatedAt.Equal(captured) {
				t.Fatalf("calls=%d rows=%d server=%s aggregates=%s", calls, len(result.Rows),
					result.ServerTime, result.Aggregates.CalculatedAt)
			}
			if size == 1 {
				var unrelated int
				if err := database.QueryRow(`SELECT COUNT(*) FROM issues WHERE project_id<>? AND deleted_at IS NULL`,
					projectID).Scan(&unrelated); err != nil {
					t.Fatal(err)
				}
				if unrelated <= MaxCandidateRoots {
					t.Fatalf("project-selective scale fixture has only %d unrelated roots", unrelated)
				}
			}
			var canonicalRow *DeliveryRow
			for index := range result.Rows {
				if result.Rows[index].IssueID == canonicalIssueIDs[size] {
					canonicalRow = &result.Rows[index]
					break
				}
			}
			if canonicalRow == nil || canonicalRow.AttemptID == nil ||
				len(canonicalRow.Stages) == 0 || len(canonicalRow.Evidence) == 0 {
				t.Fatalf("scale=%d did not traverse canonical stage/trust branches: %+v", size, canonicalRow)
			}
			var durationFacts int
			if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_stage_durations duration
				JOIN delivery_stage_events event ON event.id=duration.terminal_stage_event_id
				JOIN delivery_attempts attempt ON attempt.id=event.attempt_id
				JOIN deliveries delivery ON delivery.id=attempt.delivery_id WHERE delivery.issue_id=?`,
				canonicalIssueIDs[size]).Scan(&durationFacts); err != nil || durationFacts == 0 {
				t.Fatalf("scale=%d canonical duration facts=%d err=%v", size, durationFacts, err)
			}
			if len(queryCalls) != 4 || len(execCalls) != 2 {
				t.Fatalf("DB call budget queries=%d execs=%d, want 4 data queries + BEGIN/COMMIT", len(queryCalls), len(execCalls))
			}
			for index, want := range []string{"BEGIN DEFERRED", "COMMIT"} {
				if got := strings.Join(strings.Fields(execCalls[index].statement), " "); got != want || len(execCalls[index].args) != 0 {
					t.Fatalf("transaction call %d=(%q,%v), want exact %q without args", index+1, got, execCalls[index].args, want)
				}
			}
			requiredPlanIndexes := [][]string{
				{"idx_issues_project", "sqlite_autoindex_deliveries_1", "idx_agent_runs_active_issue",
					"idx_agent_runs_issue", "idx_issue_relations_target", "sqlite_autoindex_issue_tags_1"},
				{"idx_delivery_attempts_current", "idx_delivery_stage_events_history", "idx_delivery_run_activations_run",
					"sqlite_autoindex_delivery_agent_run_links_2"},
				{"idx_delivery_stage_events_source", "idx_agent_run_telemetry_history", "idx_delivery_stage_events_history",
					"idx_delivery_run_activations_run"},
				{"idx_delivery_stage_duration_history"},
			}
			for index, call := range queryCalls {
				rows, explainErr := database.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+call.statement, call.args...)
				if explainErr != nil {
					t.Fatalf("query %d EXPLAIN: %v", index+1, explainErr)
				}
				var plan strings.Builder
				for rows.Next() {
					var id, parent, unused int
					var detail string
					if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
						rows.Close()
						t.Fatal(err)
					}
					plan.WriteString(detail)
					plan.WriteByte('\n')
				}
				if os.Getenv("PAIMOS_EXPLAIN_AGENT_MODE") == "1" {
					t.Logf("scale=%d query=%d plan:\n%s", size, index+1, plan.String())
				}
				if err := rows.Close(); err != nil {
					t.Fatal(err)
				}
				for _, required := range requiredPlanIndexes[index] {
					if !strings.Contains(plan.String(), required) {
						t.Fatalf("scale=%d query=%d plan lacks %s:\n%s", size, index+1, required, plan.String())
					}
				}
			}
			if len(cursorPlaintext) != cursorPlaintextLength {
				t.Fatalf("cursor plaintext length=%d", len(cursorPlaintext))
			}
			expiresAt := time.Unix(int64(binary.BigEndian.Uint64(cursorPlaintext[17:25])), 0).UTC()
			if !expiresAt.Equal(captured.Add(time.Minute)) {
				t.Fatalf("cursor expiry=%s, want snapshot instant + ttl=%s", expiresAt, captured.Add(time.Minute))
			}
			if result.Aggregates.NextRefreshAt != nil && !result.Aggregates.NextRefreshAt.After(captured) {
				t.Fatalf("next refresh %s is not after captured instant %s", result.Aggregates.NextRefreshAt, captured)
			}
		})
	}
}

func TestReaderScopesDetailBeforeCandidateLimitAndRejectsOversizeSnapshots(t *testing.T) {
	database := openAgentModeTestDB(t)
	adminID := insertAgentModeUser(t, database, "detail-scale-admin", "admin", "admin")
	hiddenMemberID := insertAgentModeUser(t, database, "detail-scale-hidden", "member", "member")
	project, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Detail scale','DSL','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	if _, err := database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'none')`,
		projectID, hiddenMemberID); err != nil {
		t.Fatal(err)
	}

	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var firstIssueID, lastIssueID int64
	// The historical bug filtered detail after LIMIT 1001, so the target must
	// sort strictly after that entire capped prefix.
	for number := 1; number <= MaxCandidateRoots+2; number++ {
		issue, insertErr := tx.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
			VALUES(?,?,'ticket',?,'in-progress')`, projectID, number, fmt.Sprintf("Detail scale %04d", number))
		if insertErr != nil {
			_ = tx.Rollback()
			t.Fatal(insertErr)
		}
		issueID, _ := issue.LastInsertId()
		if number == 1 {
			firstIssueID = issueID
		}
		lastIssueID = issueID
		if _, insertErr = tx.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,agent_name,
			delivery_instrumentation_version) VALUES(?,?,?,'running',?,0)`, issueID, projectID, adminID,
			fmt.Sprintf("detail-scale-%04d", number)); insertErr != nil {
			_ = tx.Rollback()
			t.Fatal(insertErr)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	reader := NewReader(database, ReaderOptions{})
	for name, issueID := range map[string]int64{"first": firstIssueID, "after limit": lastIssueID} {
		t.Run(name, func(t *testing.T) {
			deliveryKey := fmt.Sprintf("issue:%d", issueID)
			result, readErr := reader.Read(context.Background(), Request{UserID: adminID,
				DetailDeliveryKey: deliveryKey, Filters: Filters{Attention: "all", Health: "all"}})
			if readErr != nil {
				t.Fatalf("authorized detail %q: %v", deliveryKey, readErr)
			}
			if len(result.Rows) != 1 || result.Rows[0].DeliveryID != deliveryKey || result.SelectedDelivery != deliveryKey {
				t.Fatalf("detail %q returned rows=%d selected=%q", deliveryKey, len(result.Rows), result.SelectedDelivery)
			}
		})
	}
	firstKey, lastKey := fmt.Sprintf("issue:%d", firstIssueID), fmt.Sprintf("issue:%d", lastIssueID)
	multi, err := reader.Read(context.Background(), Request{UserID: adminID,
		DetailDeliveryKeys: []string{firstKey, lastKey},
		Filters:            Filters{Attention: "all", Health: "all", SelectedDelivery: lastKey}})
	if err != nil {
		t.Fatalf("bounded multi-detail beyond global limit: %v", err)
	}
	if len(multi.Rows) != 2 || multi.SelectedDelivery != lastKey {
		t.Fatalf("multi-detail rows=%d selected=%q", len(multi.Rows), multi.SelectedDelivery)
	}

	requests := map[string]Request{
		"global":  {UserID: adminID, Filters: Filters{Attention: "all", Health: "all"}},
		"project": {UserID: adminID, RouteProjectID: &projectID, Filters: Filters{Attention: "all", Health: "all"}},
	}
	for name, request := range requests {
		if _, err := reader.Read(context.Background(), request); !errors.Is(err, ErrInvalid) ||
			!strings.Contains(err.Error(), "candidate root limit exceeded") {
			t.Fatalf("%s Read error=%v, want explicit candidate limit", name, err)
		}
		if _, err := reader.StreamState(context.Background(), request); !errors.Is(err, ErrInvalid) ||
			!strings.Contains(err.Error(), "candidate root limit exceeded") {
			t.Fatalf("%s StreamState error=%v, want explicit candidate limit", name, err)
		}
	}
	for name, request := range map[string]Request{
		"inaccessible": {UserID: hiddenMemberID, DetailDeliveryKey: fmt.Sprintf("issue:%d", lastIssueID), Filters: Filters{Attention: "all", Health: "all"}},
		"missing":      {UserID: adminID, DetailDeliveryKey: "issue:999999999", Filters: Filters{Attention: "all", Health: "all"}},
	} {
		if _, err := reader.Read(context.Background(), request); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s detail error=%v, want canonical not found", name, err)
		}
	}
}

func TestReaderPinsCatalogBeforeCapturingClockDuringConcurrentEstimateCommit(t *testing.T) {
	database := openAgentModeTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Pin order','PIN','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,1,'ticket','Pinned delivery','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	adminID := insertAgentModeUser(t, database, "pin-admin", "admin", "admin")
	writerNow := time.Date(2026, 8, 20, 11, 59, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return writerNow })})
	userActor := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", adminID)}
	attempt, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID, Actor: userActor,
		Policies: delivery.DefaultPolicy(), ReasonCode: "instrumentation", IdempotencyKey: "pin-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	external := delivery.Actor{Type: "external", OpaqueKey: "external:pin-source"}
	stage, err := store.StartStageRetry(context.Background(), delivery.StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: delivery.StageSpecification, Reporter: external,
		ReasonCode: "stage_start", IdempotencyKey: "pin-stage"})
	if err != nil {
		t.Fatal(err)
	}

	readerTime := time.Date(2026, 8, 20, 12, 0, 1, 0, time.UTC)
	clockCalls := 0
	reader := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time {
		clockCalls++
		return readerTime
	})})
	paused, release := make(chan struct{}), make(chan struct{})
	reader.beforeCatalog = func() {
		close(paused)
		<-release
	}
	type outcome struct {
		snapshot Snapshot
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		result, readErr := reader.Read(context.Background(), Request{UserID: adminID,
			Filters: Filters{Attention: "all", Health: "all"}})
		done <- outcome{snapshot: result, err: readErr}
	}()
	<-paused
	if clockCalls != 0 {
		t.Fatalf("clock called %d times before catalog snapshot pin", clockCalls)
	}
	writerNow = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	revision, sequence, progress, confidence := int64(1), int64(1), 80.0, 0.9
	if _, err := store.ReportStage(context.Background(), delivery.StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: delivery.StageSpecification,
		ExecutionNumber: stage.ExecutionNumber, AuthorityEpoch: stage.AuthorityEpoch, Reporter: external,
		IdempotencyKey: "pin-estimate", SourceSequence: &sequence, Kind: "estimate",
		Estimate: delivery.EstimateEvidence{Revision: &revision, Progress: &progress, Source: "external",
			Confidence: &confidence, Basis: "committed before catalog pin"}}); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := <-done
	if result.err != nil {
		t.Fatalf("coherent post-commit read failed: %v", result.err)
	}
	if clockCalls != 1 || len(result.snapshot.Rows) != 1 || result.snapshot.Rows[0].Progress == nil ||
		result.snapshot.Rows[0].Progress.Percent == nil || *result.snapshot.Rows[0].Progress.Percent != 8 {
		t.Fatalf("clock=%d snapshot=%+v", clockCalls, result.snapshot)
	}
}

func openAgentModeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	oldDir, oldMode := os.Getenv("DATA_DIR"), os.Getenv("PAIMOS_TEST_MODE")
	if err := os.Setenv("DATA_DIR", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("PAIMOS_TEST_MODE", "1"); err != nil {
		t.Fatal(err)
	}
	secretvault.ResetForTest()
	if err := appdb.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = appdb.DB.Close()
		appdb.DB = nil
		secretvault.ResetForTest()
		_ = os.Setenv("DATA_DIR", oldDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", oldMode)
	})
	return appdb.DB
}

func insertAgentModeUser(t *testing.T, database *sql.DB, username, role, roleKey string) int64 {
	t.Helper()
	result, err := database.Exec(`INSERT INTO users(username,password,role,role_key,status) VALUES(?,?,?,?,'active')`,
		username, "hash", role, roleKey)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration145UpgradesPopulatedM144WithoutGuessingAudience(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "populated-m144.db")
	database, err := sql.Open("sqlite", databasePath+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := migrateThrough(database, 144); err != nil {
		t.Fatalf("create exact M144 fixture: %v", err)
	}
	projectID, issueID, deliveryID := seedAgentModeDelivery(t, database, "Upgrade", "UPG", 1)
	appendAgentModeChange(t, database, deliveryID, issueID, projectID, "issue", 1)
	legacyIssueID := insertAgentModeIssue(t, database, projectID, 2, "ticket", "Legacy active")
	if _, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status,delivery_instrumentation_version)
		VALUES(?,?,'running',0)`, legacyIssueID, projectID); err != nil {
		t.Fatalf("plant M144 active legacy run: %v", err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("M144 to M145: %v", err)
	}
	var count int
	var revoked sql.NullInt64
	if err := database.QueryRow(`SELECT COUNT(*),MAX(revoked_project_id) FROM delivery_change_log`).Scan(&count, &revoked); err != nil {
		t.Fatal(err)
	}
	if count != 1 || revoked.Valid {
		t.Fatalf("historical audience was changed: count=%d revoked=%+v", count, revoked)
	}
	if !columnExists(t, database, "delivery_change_log", "revoked_project_id") {
		t.Fatal("M145 revoked_project_id missing after populated upgrade")
	}
	indexes := schemaNames(t, database, `SELECT name FROM sqlite_master WHERE type='index' AND name='idx_delivery_change_revoked_project_tail'`)
	if len(indexes) != 1 {
		t.Fatalf("revoked audience index=%v", indexes)
	}
	var syntheticID, rootProjectID, highWater int64
	var deliveryKey string
	if err := database.QueryRow(`SELECT synthetic_delivery_id,delivery_key,project_id_hint,change_sequence_high_water
		FROM agent_mode_legacy_roots WHERE issue_id=?`, legacyIssueID).
		Scan(&syntheticID, &deliveryKey, &rootProjectID, &highWater); err != nil {
		t.Fatalf("seeded M144 legacy root: %v", err)
	}
	if syntheticID != -legacyIssueID || deliveryKey != fmt.Sprintf("issue:%d", legacyIssueID) ||
		rootProjectID != projectID || highWater != 0 {
		t.Fatalf("seeded legacy root=(%d,%q,%d,%d)", syntheticID, deliveryKey, rootProjectID, highWater)
	}
	if got := agentModeChangeCount(t, database, -legacyIssueID); got != 0 {
		t.Fatalf("migration guessed %d legacy history rows", got)
	}
	for _, needle := range []string{"passwd", "sk"} {
		var patterns int
		if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_forbidden_value_patterns WHERE boundary_needle=?`, needle).Scan(&patterns); err != nil {
			t.Fatal(err)
		}
		if patterns < 2 {
			t.Fatalf("M144 upgrade did not install %q privacy patterns: %d", needle, patterns)
		}
	}
	if _, err := database.Exec(`INSERT INTO delivery_forbidden_value_patterns(pattern,boundary_needle)
		VALUES('*caller-owned*','caller')`); err == nil || !strings.Contains(err.Error(), "migration-owned") {
		t.Fatalf("M145 privacy corpus was not resealed: %v", err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = sql.Open("sqlite", databasePath+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("restart after M145: %v", err)
	}
	if got := agentModeChangeCount(t, database, -legacyIssueID); got != 0 {
		t.Fatalf("restart invented %d legacy history rows", got)
	}
}

func TestMigration145UpgradeDoesNotInventHiddenCascadeRemoval(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "hidden-m144.db")
	database, err := sql.Open("sqlite", databasePath+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 144); err != nil {
		t.Fatalf("create exact M144 fixture: %v", err)
	}
	projectID, issueID, deliveryID := seedAgentModeDelivery(t, database, "Hidden upgrade", "HUP", 1)
	if _, err := database.Exec(`UPDATE issues SET deleted_at='2026-08-20T10:00:00Z' WHERE id=?`, issueID); err != nil {
		t.Fatal(err)
	}
	var highWater int64
	if err := database.QueryRow(`SELECT change_sequence_high_water FROM deliveries WHERE id=?`, deliveryID).Scan(&highWater); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,
		project_id_hint,change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
		VALUES('00000000000000000000000000006661',?,?,?,?,?,0,'lane','relation',?,'2026-08-20T10:00:01Z')`,
		deliveryID, issueID, "delivery:HUP:root", projectID, highWater+1, issueID); err != nil {
		t.Fatalf("plant M144 hidden lane invalidation: %v", err)
	}
	before := agentModeChangeCount(t, database, deliveryID)
	if err := migrate(database); err != nil {
		t.Fatalf("upgrade M144 to M145: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = sql.Open("sqlite", databasePath+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := migrate(database); err != nil {
		t.Fatalf("restart M145: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM issues WHERE id=?`, issueID); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, deliveryID); got != before {
		t.Fatalf("hidden post-upgrade hard delete emitted %d changes", got-before)
	}
}

func TestMigration145AgentRunTelemetrySecretBackstop(t *testing.T) {
	database := openTestDB(t)
	projectID, issueID, _ := seedAgentModeDelivery(t, database, "Telemetry privacy", "TPR", 1)
	user, err := database.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('telemetry-privacy-admin','hash','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,delivery_instrumentation_version)
		VALUES(?,?,?,'running',1)`, issueID, projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	canaries := []string{
		"nul\x00value", "carriage\rreturn", "line\nfeed",
		"passwd=abcdefgh", "DB_PASSWORD=abcdefgh", "GITHUB_TOKEN=abcdefgh", "OPENAI_API_KEY=abcdefgh",
		"sk_abcdefghijklmnopqrst", "sk-ant-abcdefghijklmnopqrst",
		"sk_live_12345678", "sk_test_12345678", "sk_proj_12345678",
		"github_pat_12345678901234567890", "AIza12345678901234567890",
		"eyJabcdefgh.abcdefgh.abcdefgh", "https://runner:password123@example.test/repo",
	}
	for index, canary := range canaries {
		for _, column := range []string{"activity", "estimate_basis"} {
			activity, basis := "safe activity", "safe basis"
			if column == "activity" {
				activity = canary
			} else {
				basis = canary
			}
			_, insertErr := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,
				agent_reported_at,server_received_at,kind,phase,activity,estimate_revision,progress_percent,
				estimate_source,estimate_confidence,estimate_basis)
				VALUES(?,?,?,'test','test','2026-08-20T12:00:00Z','2026-08-20T12:00:00Z','progress',
				'implementing',?,1,20,'agent',.9,?)`, runID, index+1, fmt.Sprintf("privacy-%d", index), activity, basis)
			if insertErr == nil || !strings.Contains(insertErr.Error(), "forbidden agent run telemetry") {
				t.Fatalf("database accepted %s canary %q: %v", column, canary, insertErr)
			}
		}
	}
	if _, err := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,
		agent_reported_at,server_received_at,kind,phase,activity,estimate_revision,progress_percent,
		estimate_source,estimate_confidence,estimate_basis)
		VALUES(?,1,'privacy-safe','test','test','2026-08-20T12:00:00Z','2026-08-20T12:00:00Z','progress',
		'implementing','passwordless auth; sketch approved',1,20,'agent',.9,
		'token budget; credential rotation; sk_abcdefghijklmnopqrs')`, runID); err != nil {
		t.Fatalf("database over-rejected safe telemetry: %v", err)
	}
}

func TestMigration145RebuildsExactM144PrivacyGuardsAndPreservesRetainedBlocker(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "privacy-m144.db")
	database, err := sql.Open("sqlite", databasePath+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 144); err != nil {
		t.Fatalf("create exact M144 fixture: %v", err)
	}
	deliveryID, attemptID, reporterID, startID := seedDeliverySchemaGraph(t, database)
	var revision int64
	if err := database.QueryRow(`SELECT COALESCE(MAX(delivery_revision),0)+1 FROM delivery_events
		WHERE delivery_id=?`, deliveryID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	envelope, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at) VALUES(?,?,?,zeroblob(32),'stage_reported',?,?)`,
		deliveryID, revision, "retained-blocker-envelope", reporterID, "2026-08-20T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	envelopeID, _ := envelope.LastInsertId()
	stage, err := database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,execution_start_stage_event_id,
		semantic_state,activity,declared_blocker_count,current_blocker_count,declared_evidence_count,spec_revision,
		server_received_at) VALUES(?,?,'specification',1,2,1,?,'semantic_report',?,?,'waiting','Waiting',3,3,0,1,?)`,
		deliveryID, attemptID, envelopeID, reporterID, startID, "2026-08-20T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	stageID, _ := stage.LastInsertId()
	for ordinal := 0; ordinal < 3; ordinal++ {
		if _, err := database.Exec(`INSERT INTO delivery_stage_blockers(delivery_id,stage_event_id,ordinal,blocker_key,
			blocker_class,summary,is_current,is_human_wait,interval_started_at,interval_ended_at)
			VALUES(?,?,?,?,'input','Safe approval',1,1,?,?)`, deliveryID, stageID, ordinal,
			fmt.Sprintf("retained-approval-%d", ordinal), "2026-08-20T12:00:00Z", ""); err != nil {
			t.Fatal(err)
		}
	}
	var oldSecretGuard, oldUpdateGuard string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='trg_delivery_blocker_secret_guard'`).
		Scan(&oldSecretGuard); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='trg_delivery_blockers_no_update'`).
		Scan(&oldUpdateGuard); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TRIGGER trg_delivery_blocker_secret_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TRIGGER trg_delivery_blockers_no_update`); err != nil {
		t.Fatal(err)
	}
	retained := []string{"passwd=retained-secret", "DB_PASSWORD=retained-secret", "sk-ant-abcdefghijklmnopqrst"}
	for ordinal, canary := range retained {
		if _, err := database.Exec(`UPDATE delivery_stage_blockers SET summary=? WHERE stage_event_id=? AND ordinal=?`,
			canary, stageID, ordinal); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(oldSecretGuard); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(oldUpdateGuard); err != nil {
		t.Fatal(err)
	}
	var exactM144 string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger'
		AND name='trg_delivery_blocker_secret_guard'`).Scan(&exactM144); err != nil {
		t.Fatal(err)
	}
	if exactM144 != oldSecretGuard || !strings.Contains(exactM144, "*[^0-9A-Za-z_]") ||
		strings.Contains(exactM144, "paimos_contains_secret_like") {
		t.Fatalf("fixture is not exact M144 blocker guard: %q", exactM144)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("upgrade M144 to M145: %v", err)
	}
	rows, err := database.Query(`SELECT summary FROM delivery_stage_blockers WHERE stage_event_id=? ORDER BY ordinal`, stageID)
	if err != nil {
		t.Fatal(err)
	}
	var retainedAfter []string
	for rows.Next() {
		var summary string
		if err := rows.Scan(&summary); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		retainedAfter = append(retainedAfter, summary)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(retainedAfter) != fmt.Sprint(retained) {
		t.Fatalf("retained blockers=%q want=%q", retainedAfter, retained)
	}
	guards := map[string]bool{
		"trg_delivery_reporter_secret_guard": true,
		"trg_delivery_event_secret_guard":    false,
		"trg_delivery_attempt_secret_guard":  false,
		"trg_delivery_policy_secret_guard":   true,
		"trg_delivery_stage_secret_guard":    false,
		"trg_delivery_blocker_secret_guard":  false,
		"trg_delivery_evidence_secret_guard": true,
	}
	for name, strictReference := range guards {
		var sqlText string
		if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&sqlText); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, fragment := range []string{"paimos_contains_secret_like(CAST(value.value AS BLOB))", "char(0)", "char(10)", "char(13)"} {
			if !strings.Contains(sqlText, fragment) {
				t.Fatalf("%s lacks M145 fragment %q: %s", name, fragment, sqlText)
			}
		}
		if strings.Contains(sqlText, "delivery_forbidden_value_patterns") ||
			(strictReference && (!strings.Contains(sqlText, "'?'") || !strings.Contains(sqlText, "'://'"))) {
			t.Fatalf("%s retained stale/weak M144 SQL: %s", name, sqlText)
		}
	}
	// Execute every rebuilt guard on this same upgraded connection. Each write
	// has valid parent rows and a free identity/sequence; the named privacy
	// trigger, rather than an incidental FK/UNIQUE/CHECK, must reject it.
	if _, err := database.Exec(`UPDATE projects SET key='M144DS' WHERE key='DS'`); err != nil {
		t.Fatal(err)
	}
	guardDelivery, guardAttempt, guardReporter, guardStart := seedDeliverySchemaGraph(t, database)
	const envCanary = "DB_PASSWORD=upgrade-secret"
	const genericSK = "sk-ant-abcdefghijklmnopqrst"
	assertForbidden := func(surface string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "forbidden delivery "+surface+" value") {
			t.Fatalf("upgraded %s guard error=%v", surface, err)
		}
	}
	_, err = database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
		VALUES(?,'external',?,'2026-08-20T13:00:00Z')`, guardDelivery, genericSK)
	assertForbidden("reporter", err)
	_, err = database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
		kind,reporter_id,reason_text,server_received_at) VALUES(?,3,'upgrade-event-reject',zeroblob(32),
		'stage_reported',?,?, '2026-08-20T13:00:00Z')`, guardDelivery, guardReporter, envCanary)
	assertForbidden("event", err)
	attemptEnvelope, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at) VALUES(?,3,'upgrade-attempt-envelope',zeroblob(32),
		'attempt_started',?,'2026-08-20T13:00:00Z')`, guardDelivery, guardReporter)
	if err != nil {
		t.Fatal(err)
	}
	attemptEnvelopeID, _ := attemptEnvelope.LastInsertId()
	_, err = database.Exec(`INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,
		previous_attempt_id,start_delivery_event_id,reason_code,reason_text,created_at)
		VALUES(?,2,2,?,?,'retry',?,'2026-08-20T13:00:00Z')`, guardDelivery, guardAttempt, attemptEnvelopeID, envCanary)
	assertForbidden("attempt", err)
	newAttempt, err := database.Exec(`INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,
		previous_attempt_id,start_delivery_event_id,reason_code,reason_text,created_at)
		VALUES(?,2,2,?,?,'retry','safe retry','2026-08-20T13:00:00Z')`, guardDelivery, guardAttempt, attemptEnvelopeID)
	if err != nil {
		t.Fatal(err)
	}
	newAttemptID, _ := newAttempt.LastInsertId()
	_, err = database.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,sort_order,
		applicability,weight,policy_reference,created_at)
		VALUES(?,?,'specification',1,'required',10,?,'2026-08-20T13:00:00Z')`, guardDelivery, newAttemptID, genericSK)
	assertForbidden("policy", err)
	stageEnvelope, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at) VALUES(?,4,'upgrade-stage-envelope',zeroblob(32),
		'stage_reported',?,'2026-08-20T13:00:00Z')`, guardDelivery, guardReporter)
	if err != nil {
		t.Fatal(err)
	}
	stageEnvelopeID, _ := stageEnvelope.LastInsertId()
	_, err = database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,execution_start_stage_event_id,
		semantic_state,activity,spec_revision,server_received_at)
		VALUES(?,?,'specification',1,2,1,?,'semantic_report',?,?,'active',?,1,'2026-08-20T13:00:00Z')`,
		guardDelivery, guardAttempt, stageEnvelopeID, guardReporter, guardStart, envCanary)
	assertForbidden("stage", err)
	childrenEnvelope, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at) VALUES(?,5,'upgrade-children-envelope',zeroblob(32),
		'stage_reported',?,'2026-08-20T13:00:00Z')`, guardDelivery, guardReporter)
	if err != nil {
		t.Fatal(err)
	}
	childrenEnvelopeID, _ := childrenEnvelope.LastInsertId()
	children, err := database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,
		execution_number,event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,
		execution_start_stage_event_id,semantic_state,activity,declared_blocker_count,current_blocker_count,
		declared_evidence_count,spec_revision,server_received_at)
		VALUES(?,?,'specification',1,2,1,?,'semantic_report',?,?,'waiting','Safe waiting',1,1,1,1,
		'2026-08-20T13:00:00Z')`, guardDelivery, guardAttempt, childrenEnvelopeID, guardReporter, guardStart)
	if err != nil {
		t.Fatal(err)
	}
	childrenID, _ := children.LastInsertId()
	_, err = database.Exec(`INSERT INTO delivery_stage_blockers(delivery_id,stage_event_id,ordinal,blocker_key,
		blocker_class,summary,is_current,is_human_wait,interval_started_at,interval_ended_at)
		VALUES(?,?,0,'upgrade-blocker','external',?,1,0,'2026-08-20T13:00:00Z','')`,
		guardDelivery, childrenID, envCanary)
	assertForbidden("blocker", err)
	_, err = database.Exec(`INSERT INTO delivery_evidence(delivery_id,root_issue_id,stage_event_id,ordinal,
		evidence_type,outcome,reference_kind,reference_value,created_at)
		SELECT ?,issue_id,?,0,'spec_acceptance','unknown','external_ref',?,'2026-08-20T13:00:00Z'
		FROM deliveries WHERE id=?`, guardDelivery, childrenID, genericSK, guardDelivery)
	assertForbidden("evidence", err)

	var guardIssue, guardProject int64
	if err := database.QueryRow(`SELECT issue_id,project_id_hint FROM deliveries WHERE id=?`, guardDelivery).
		Scan(&guardIssue, &guardProject); err != nil {
		t.Fatal(err)
	}
	user, err := database.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('upgrade-privacy-user','hash','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'running',1)`, guardIssue, guardProject, userID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	for _, canary := range []string{envCanary, genericSK, "nul\x00value"} {
		for _, column := range []string{"activity", "estimate_basis"} {
			activity, basis := "safe activity", "safe basis"
			if column == "activity" {
				activity = canary
			} else {
				basis = canary
			}
			_, insertErr := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,
				adapter,agent_reported_at,server_received_at,kind,phase,activity,estimate_revision,progress_percent,
				estimate_source,estimate_confidence,estimate_basis)
				VALUES(?,1,'upgrade-privacy','test','test','2026-08-20T13:00:00Z','2026-08-20T13:00:00Z',
				'progress','implementing',?,1,20,'agent',.9,?)`, runID, activity, basis)
			if insertErr == nil || !strings.Contains(insertErr.Error(), "forbidden agent run telemetry value") {
				t.Fatalf("upgraded telemetry %s accepted %q: %v", column, canary, insertErr)
			}
		}
	}
	var telemetryGuard string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger'
		AND name='trg_agent_run_telemetry_delivery_secret_guard'`).Scan(&telemetryGuard); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"CAST(NEW.activity AS BLOB)", "CAST(NEW.estimate_basis AS BLOB)", "char(0)", "char(10)", "char(13)"} {
		if !strings.Contains(telemetryGuard, fragment) {
			t.Fatalf("telemetry guard lacks %q: %s", fragment, telemetryGuard)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = sql.Open("sqlite", databasePath+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := migrate(database); err != nil {
		t.Fatalf("restart after M145 privacy upgrade: %v", err)
	}
}

func TestMigration145RecursiveInvalidationAndAudienceGuards(t *testing.T) {
	database := openTestDB(t)
	projectID, _, _ := seedAgentModeDelivery(t, database, "Recursive", "REC", 1)
	epic := insertAgentModeIssue(t, database, projectID, 2, "epic", "Epic")
	child := insertAgentModeIssue(t, database, projectID, 3, "ticket", "Child")
	leaf := insertAgentModeIssue(t, database, projectID, 4, "ticket", "Leaf")
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent'),(?,?,'parent')`,
		epic, child, child, leaf); err != nil {
		t.Fatal(err)
	}
	leafDelivery := insertAgentModeDelivery(t, database, leaf, projectID, "delivery:recursive-leaf")
	parent := insertAgentModeIssue(t, database, projectID, 5, "epic", "Parent")

	before := agentModeChangeCount(t, database, leafDelivery)
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, parent, epic); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, leafDelivery); got != before+1 {
		t.Fatalf("recursive insert invalidations=%d want %d", got, before+1)
	}
	if _, err := database.Exec(`DELETE FROM issue_relations WHERE source_id=? AND target_id=? AND type='parent'`, parent, epic); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, leafDelivery); got != before+2 {
		t.Fatalf("recursive delete invalidations=%d want %d", got, before+2)
	}

	before = agentModeChangeCount(t, database, leafDelivery)
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, parent, epic); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, leafDelivery); got != before {
		t.Fatalf("rollback leaked %d invalidations", got-before)
	}

	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'blocks')`, parent, epic); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, leafDelivery); got != before {
		t.Fatalf("non-parent relation emitted %d invalidations", got-before)
	}
	if _, err := database.Exec(`UPDATE issues SET title='Renamed epic' WHERE id=?`, epic); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, leafDelivery); got != before+1 {
		t.Fatalf("ancestor metadata invalidations=%d want %d", got, before+1)
	}
	if _, err := database.Exec(`UPDATE projects SET name='Renamed project' WHERE id=?`, projectID); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, leafDelivery); got != before+2 {
		t.Fatalf("project metadata invalidations=%d want %d", got, before+2)
	}

	// A cycle must terminate and touch the delivery once, not recurse to an
	// arbitrary depth or duplicate the same root in one trigger statement.
	laneBefore := agentModeKindCount(t, database, leafDelivery, "lane")
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, leaf, epic); err != nil {
		t.Fatal(err)
	}
	if got := agentModeKindCount(t, database, leafDelivery, "lane"); got != laneBefore+1 {
		t.Fatalf("cycle-safe lane invalidations=%d want %d", got, laneBefore+1)
	}

	// Every post-M145 project move must carry a distinct positive old scope;
	// historical NULL rows are possible only because they predate the trigger.
	var nextSequence int64
	if err := database.QueryRow(`SELECT change_sequence_high_water+1 FROM deliveries WHERE id=?`, leafDelivery).Scan(&nextSequence); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,
		project_id_hint,change_sequence,delivery_revision,kind,source_kind,server_received_at)
		VALUES(lower(hex(randomblob(16))),?,?,?,?,?,0,'project_move','issue','2026-08-20T12:00:00Z')`,
		leafDelivery, leaf, "delivery:recursive-leaf", projectID, nextSequence); err == nil || !strings.Contains(err.Error(), "revoked audience") {
		t.Fatalf("project_move without revoked audience error=%v", err)
	}
}

func TestMigration145LegacyRunMembershipIsDurableExactlyOnce(t *testing.T) {
	database := openTestDB(t)
	projectID := insertAgentModeProject(t, database, "Legacy lifecycle", "LV0")
	issueID := insertAgentModeIssue(t, database, projectID, 1, "ticket", "Legacy lifecycle")
	runID := insertAgentModeLegacyRun(t, database, issueID, projectID, "queued")

	assertLegacyRootState(t, database, issueID, projectID, 1)
	if got := agentModeChangeCount(t, database, -issueID); got != 1 {
		t.Fatalf("initial active membership changes=%d want 1", got)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET status='running' WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, -issueID); got != 1 {
		t.Fatalf("active-to-active emitted %d extra changes", got-1)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET status='completed' WHERE id=?`, runID); err != nil {
		t.Fatalf("active-to-terminal: %v", err)
	}
	assertLegacyRootState(t, database, issueID, projectID, 2)
	if got := agentModeChangeCount(t, database, -issueID); got != 2 {
		t.Fatalf("active-to-terminal changes=%d want 2", got)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET status='queued' WHERE id=?`, runID); err != nil {
		t.Fatalf("inactive-to-active: %v", err)
	}
	assertLegacyRootState(t, database, issueID, projectID, 3)

	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE agent_runs SET status='failed' WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertLegacyRootState(t, database, issueID, projectID, 3)
	if got := agentModeChangeCount(t, database, -issueID); got != 3 {
		t.Fatalf("rollback leaked legacy change: %d", got)
	}

	if _, err := database.Exec(`DELETE FROM agent_runs WHERE id=?`, runID); err != nil {
		t.Fatalf("active delete: %v", err)
	}
	assertLegacyRootState(t, database, issueID, projectID, 4)
	if got := agentModeChangeCount(t, database, -issueID); got != 4 {
		t.Fatalf("active delete changes=%d want 4", got)
	}
}

func TestMigration145LegacyRootAndChangeProvenanceRejectForgery(t *testing.T) {
	database := openTestDB(t)
	projectID := insertAgentModeProject(t, database, "Legacy guard", "LGD")
	otherProjectID := insertAgentModeProject(t, database, "Other", "OTH")
	issueID := insertAgentModeIssue(t, database, projectID, 1, "ticket", "Guarded legacy")
	runID := insertAgentModeLegacyRun(t, database, issueID, projectID, "running")

	rootForgeries := []struct {
		name      string
		issueID   int64
		synthetic int64
		key       string
		projectID int64
		highWater int64
		createdAt string
	}{
		{"negative issue", -issueID, issueID, fmt.Sprintf("issue:%d", -issueID), projectID, 0, "2026-08-20T12:00:00.000Z"},
		{"wrong synthetic", issueID, -issueID - 1, fmt.Sprintf("issue:%d", issueID), projectID, 0, "2026-08-20T12:00:00.000Z"},
		{"wrong project", issueID, -issueID, fmt.Sprintf("issue:%d", issueID), otherProjectID, 0, "2026-08-20T12:00:00.000Z"},
		{"nonzero high-water", issueID, -issueID, fmt.Sprintf("issue:%d", issueID), projectID, 999, "2026-08-20T12:00:00.000Z"},
		{"malformed created-at", issueID, -issueID, fmt.Sprintf("issue:%d", issueID), projectID, 0, "not-a-time"},
	}
	for _, tc := range rootForgeries {
		t.Run(tc.name, func(t *testing.T) {
			_, err := database.Exec(`INSERT OR REPLACE INTO agent_mode_legacy_roots(
				issue_id,synthetic_delivery_id,delivery_key,project_id_hint,change_sequence_high_water,created_at)
				VALUES(?,?,?,?,?,?)`, tc.issueID, tc.synthetic, tc.key, tc.projectID, tc.highWater, tc.createdAt)
			if err == nil || !strings.Contains(err.Error(), "legacy root provenance") {
				t.Fatalf("forgery error=%v", err)
			}
			assertLegacyRootState(t, database, issueID, projectID, 1)
		})
	}

	terminalIssueID := insertAgentModeIssue(t, database, projectID, 2, "ticket", "No active v0")
	_ = insertAgentModeLegacyRun(t, database, terminalIssueID, projectID, "completed")
	if _, err := database.Exec(`INSERT INTO agent_mode_legacy_roots(
		issue_id,synthetic_delivery_id,delivery_key,project_id_hint,created_at)
		VALUES(?,?,?,?,'2026-08-20T12:00:00.000Z')`, terminalIssueID, -terminalIssueID,
		fmt.Sprintf("issue:%d", terminalIssueID), projectID); err == nil || !strings.Contains(err.Error(), "legacy root provenance") {
		t.Fatalf("root without active v0 error=%v", err)
	}
	v1IssueID := insertAgentModeIssue(t, database, projectID, 3, "ticket", "Canonical")
	_ = insertAgentModeDelivery(t, database, v1IssueID, projectID, "delivery:canonical-guard")
	if _, err := database.Exec(`INSERT INTO agent_mode_legacy_roots(
		issue_id,synthetic_delivery_id,delivery_key,project_id_hint,created_at)
		VALUES(?,?,?,?,'2026-08-20T12:00:00.000Z')`, v1IssueID, -v1IssueID,
		fmt.Sprintf("issue:%d", v1IssueID), projectID); err == nil || !strings.Contains(err.Error(), "legacy root provenance") {
		t.Fatalf("root beside canonical delivery error=%v", err)
	}

	var highWater int64
	if err := database.QueryRow(`SELECT change_sequence_high_water FROM agent_mode_legacy_roots WHERE issue_id=?`, issueID).Scan(&highWater); err != nil {
		t.Fatal(err)
	}
	changeForgeries := []struct {
		name       string
		revision   int64
		kind       string
		sourceKind string
		sourceID   any
		revoked    any
	}{
		{"wrong revision", 1, "run", "agent_run", runID, nil},
		{"null source", 0, "run", "agent_run", nil, nil},
		{"wrong source kind", 0, "run", "issue", runID, nil},
		{"wrong run", 0, "run", "agent_run", runID + 1000, nil},
		{"forged move audience", 0, "project_move", "issue", issueID, otherProjectID},
	}
	for index, tc := range changeForgeries {
		t.Run(tc.name, func(t *testing.T) {
			_, err := database.Exec(`INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,
				delivery_key,project_id_hint,revoked_project_id,change_sequence,delivery_revision,kind,
				source_kind,source_id,server_received_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,'2026-08-20T12:00:00Z')`,
				fmt.Sprintf("%032x", 9000+index), -issueID, issueID, fmt.Sprintf("issue:%d", issueID),
				projectID, tc.revoked, highWater+1, tc.revision, tc.kind, tc.sourceKind, tc.sourceID)
			if err == nil || !strings.Contains(err.Error(), "provenance") {
				t.Fatalf("forgery error=%v", err)
			}
			assertLegacyRootState(t, database, issueID, projectID, highWater)
		})
	}
}

func TestMigration145LegacyMoveTracksExactPriorProject(t *testing.T) {
	database := openTestDB(t)
	projectA := insertAgentModeProject(t, database, "Source", "SRC")
	projectB := insertAgentModeProject(t, database, "Target", "TGT")
	issueID := insertAgentModeIssue(t, database, projectA, 1, "ticket", "Moving legacy")
	_ = insertAgentModeLegacyRun(t, database, issueID, projectA, "running")

	if _, err := database.Exec(`UPDATE issues SET project_id=? WHERE id=?`, projectB, issueID); err != nil {
		t.Fatalf("move active legacy issue: %v", err)
	}
	assertLegacyRootState(t, database, issueID, projectB, 2)
	var revoked sql.NullInt64
	var kind string
	if err := database.QueryRow(`SELECT kind,revoked_project_id FROM delivery_change_log
		WHERE delivery_id=? ORDER BY change_sequence DESC LIMIT 1`, -issueID).Scan(&kind, &revoked); err != nil {
		t.Fatal(err)
	}
	if kind != "project_move" || !revoked.Valid || revoked.Int64 != projectA {
		t.Fatalf("move fact kind=%q revoked=%+v", kind, revoked)
	}
	var pending sql.NullInt64
	if err := database.QueryRow(`SELECT pending_revoked_project_id FROM agent_mode_legacy_roots WHERE issue_id=?`, issueID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending.Valid {
		t.Fatalf("consumed prior project remained pending: %+v", pending)
	}
	if _, err := database.Exec(`INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,
		delivery_key,project_id_hint,revoked_project_id,change_sequence,delivery_revision,kind,source_kind,source_id,
		server_received_at) VALUES('00000000000000000000000000009999',?,?,?,?,?,3,0,'project_move','issue',?,
		'2026-08-20T12:00:00Z')`, -issueID, issueID, fmt.Sprintf("issue:%d", issueID), projectB, projectA,
		issueID); err == nil || !strings.Contains(err.Error(), "provenance") {
		t.Fatalf("reused prior audience error=%v", err)
	}
}

func TestMigration145LegacyHardDeleteEmitsOnlyVisibleRemoval(t *testing.T) {
	t.Run("visible exact one", func(t *testing.T) {
		database := openTestDB(t)
		projectID := insertAgentModeProject(t, database, "Delete visible", "DLV")
		issueID := insertAgentModeIssue(t, database, projectID, 1, "ticket", "Delete me")
		_ = insertAgentModeLegacyRun(t, database, issueID, projectID, "running")
		if _, err := database.Exec(`DELETE FROM issues WHERE id=?`, issueID); err != nil {
			t.Fatalf("hard delete: %v", err)
		}
		if got := agentModeChangeCount(t, database, -issueID); got != 2 {
			t.Fatalf("visible hard-delete changes=%d want initial+one removal", got)
		}
		var kind, sourceKind string
		var sourceID int64
		if err := database.QueryRow(`SELECT kind,source_kind,source_id FROM delivery_change_log
			WHERE delivery_id=? ORDER BY change_sequence DESC LIMIT 1`, -issueID).
			Scan(&kind, &sourceKind, &sourceID); err != nil {
			t.Fatal(err)
		}
		if kind != "issue" || sourceKind != "issue" || sourceID != issueID {
			t.Fatalf("removal provenance=(%q,%q,%d)", kind, sourceKind, sourceID)
		}
		var roots int
		if err := database.QueryRow(`SELECT COUNT(*) FROM agent_mode_legacy_roots WHERE issue_id=?`, issueID).Scan(&roots); err != nil {
			t.Fatal(err)
		}
		if roots != 0 {
			t.Fatalf("deleted issue retained %d roots", roots)
		}
		if err := migrate(database); err != nil {
			t.Fatalf("restart migration: %v", err)
		}
		if got := agentModeChangeCount(t, database, -issueID); got != 2 {
			t.Fatalf("restart changed deletion history: %d", got)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		database := openTestDB(t)
		projectID := insertAgentModeProject(t, database, "Delete rollback", "DLR")
		issueID := insertAgentModeIssue(t, database, projectID, 1, "ticket", "Keep me")
		_ = insertAgentModeLegacyRun(t, database, issueID, projectID, "running")
		tx, err := database.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`DELETE FROM issues WHERE id=?`, issueID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		assertLegacyRootState(t, database, issueID, projectID, 1)
		if got := agentModeChangeCount(t, database, -issueID); got != 1 {
			t.Fatalf("rollback leaked deletion changes=%d", got)
		}
	})

	t.Run("already hidden", func(t *testing.T) {
		database := openTestDB(t)
		projectA := insertAgentModeProject(t, database, "Hidden source", "HSA")
		projectB := insertAgentModeProject(t, database, "Hidden target", "HTB")
		terminalIssue := insertAgentModeIssue(t, database, projectA, 1, "ticket", "Terminal")
		terminalRun := insertAgentModeLegacyRun(t, database, terminalIssue, projectA, "running")
		if _, err := database.Exec(`UPDATE agent_runs SET status='completed' WHERE id=?`, terminalRun); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE issues SET project_id=? WHERE id=?`, projectB, terminalIssue); err != nil {
			t.Fatal(err)
		}
		assertLegacyRootState(t, database, terminalIssue, projectB, 2)
		if _, err := database.Exec(`DELETE FROM issues WHERE id=?`, terminalIssue); err != nil {
			t.Fatal(err)
		}
		if got := agentModeChangeCount(t, database, -terminalIssue); got != 2 {
			t.Fatalf("terminal hidden hard-delete added %d changes", got-2)
		}

		softIssue := insertAgentModeIssue(t, database, projectA, 2, "ticket", "Soft deleted")
		_ = insertAgentModeLegacyRun(t, database, softIssue, projectA, "running")
		if _, err := database.Exec(`UPDATE issues SET deleted_at='2026-08-20T12:00:00Z' WHERE id=?`, softIssue); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE issues SET project_id=? WHERE id=?`, projectB, softIssue); err != nil {
			t.Fatal(err)
		}
		assertLegacyRootState(t, database, softIssue, projectB, 2)
		if _, err := database.Exec(`DELETE FROM issues WHERE id=?`, softIssue); err != nil {
			t.Fatal(err)
		}
		if got := agentModeChangeCount(t, database, -softIssue); got != 2 {
			t.Fatalf("soft-deleted hard-delete added %d changes", got-2)
		}
	})
}

func TestMigration145TagInvalidationsAreExactForCanonicalAndLegacyRoots(t *testing.T) {
	database := openTestDB(t)
	projectID := insertAgentModeProject(t, database, "Tags", "TAG")
	canonicalA := insertAgentModeIssue(t, database, projectID, 1, "ticket", "Canonical tag A")
	canonicalB := insertAgentModeIssue(t, database, projectID, 4, "ticket", "Canonical tag B")
	canonicalDeliveryA := insertAgentModeDelivery(t, database, canonicalA, projectID, "delivery:tag-canonical-a")
	canonicalDeliveryB := insertAgentModeDelivery(t, database, canonicalB, projectID, "delivery:tag-canonical-b")
	legacyA := insertAgentModeIssue(t, database, projectID, 2, "ticket", "Legacy tag A")
	legacyB := insertAgentModeIssue(t, database, projectID, 3, "ticket", "Legacy tag B")
	_ = insertAgentModeLegacyRun(t, database, legacyA, projectID, "running")
	_ = insertAgentModeLegacyRun(t, database, legacyB, projectID, "running")
	tagOne := insertAgentModeTag(t, database, "one")
	tagTwo := insertAgentModeTag(t, database, "two")
	tagColor := insertAgentModeTag(t, database, "color-only")

	canonicalABefore := agentModeChangeCount(t, database, canonicalDeliveryA)
	canonicalBBefore := agentModeChangeCount(t, database, canonicalDeliveryB)
	canonicalAIssuesBefore := agentModeKindCount(t, database, canonicalDeliveryA, "issue")
	canonicalBIssuesBefore := agentModeKindCount(t, database, canonicalDeliveryB, "issue")
	if _, err := database.Exec(`INSERT INTO issue_tags(issue_id,tag_id) VALUES(?,?)`, canonicalA, tagOne); err != nil {
		t.Fatal(err)
	}
	canonicalABefore++
	canonicalAIssuesBefore++
	if got := agentModeChangeCount(t, database, canonicalDeliveryA); got != canonicalABefore {
		t.Fatalf("canonical tag insert changes=%d want %d", got, canonicalABefore)
	}
	if got := agentModeKindCount(t, database, canonicalDeliveryA, "issue"); got != canonicalAIssuesBefore {
		t.Fatalf("canonical tag insert issue facts=%d want %d", got, canonicalAIssuesBefore)
	}
	if _, err := database.Exec(`UPDATE issue_tags SET tag_id=? WHERE issue_id=? AND tag_id=?`, tagTwo, canonicalA, tagOne); err != nil {
		t.Fatal(err)
	}
	canonicalABefore++
	canonicalAIssuesBefore++
	if got := agentModeChangeCount(t, database, canonicalDeliveryA); got != canonicalABefore {
		t.Fatalf("canonical same-issue tag swap changes=%d want %d", got, canonicalABefore)
	}
	if _, err := database.Exec(`UPDATE issue_tags SET issue_id=? WHERE issue_id=? AND tag_id=?`, canonicalB, canonicalA, tagTwo); err != nil {
		t.Fatal(err)
	}
	canonicalABefore++
	canonicalBBefore++
	canonicalAIssuesBefore++
	canonicalBIssuesBefore++
	if got := agentModeChangeCount(t, database, canonicalDeliveryA); got != canonicalABefore {
		t.Fatalf("canonical old issue tag move changes=%d want %d", got, canonicalABefore)
	}
	if got := agentModeChangeCount(t, database, canonicalDeliveryB); got != canonicalBBefore {
		t.Fatalf("canonical new issue tag move changes=%d want %d", got, canonicalBBefore)
	}
	if _, err := database.Exec(`UPDATE tags SET name='canonical-renamed' WHERE id=?`, tagTwo); err != nil {
		t.Fatal(err)
	}
	canonicalBBefore++
	canonicalBIssuesBefore++
	if got := agentModeChangeCount(t, database, canonicalDeliveryB); got != canonicalBBefore {
		t.Fatalf("canonical tag rename changes=%d want %d", got, canonicalBBefore)
	}
	if _, err := database.Exec(`DELETE FROM issue_tags WHERE issue_id=? AND tag_id=?`, canonicalB, tagTwo); err != nil {
		t.Fatal(err)
	}
	canonicalBBefore++
	canonicalBIssuesBefore++
	if got := agentModeChangeCount(t, database, canonicalDeliveryB); got != canonicalBBefore {
		t.Fatalf("canonical tag delete changes=%d want %d", got, canonicalBBefore)
	}
	if got := agentModeKindCount(t, database, canonicalDeliveryA, "issue"); got != canonicalAIssuesBefore {
		t.Fatalf("canonical A tag issue facts=%d want %d", got, canonicalAIssuesBefore)
	}
	if got := agentModeKindCount(t, database, canonicalDeliveryB, "issue"); got != canonicalBIssuesBefore {
		t.Fatalf("canonical B tag issue facts=%d want %d", got, canonicalBIssuesBefore)
	}
	canonicalTx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalTx.Exec(`INSERT INTO issue_tags(issue_id,tag_id) VALUES(?,?)`, canonicalA, tagOne); err != nil {
		t.Fatal(err)
	}
	if err := canonicalTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, canonicalDeliveryA); got != canonicalABefore {
		t.Fatalf("canonical tag rollback changed root by %d", got-canonicalABefore)
	}
	legacyColorBefore := agentModeChangeCount(t, database, -legacyA)
	if _, err := database.Exec(`INSERT INTO issue_tags(issue_id,tag_id) VALUES(?,?),(?,?)`,
		canonicalA, tagColor, legacyA, tagColor); err != nil {
		t.Fatal(err)
	}
	canonicalABefore++
	canonicalAIssuesBefore++
	legacyColorBefore++
	if got := agentModeChangeCount(t, database, canonicalDeliveryA); got != canonicalABefore {
		t.Fatalf("canonical color-control assignment changes=%d want %d", got, canonicalABefore)
	}
	if got := agentModeChangeCount(t, database, -legacyA); got != legacyColorBefore {
		t.Fatalf("legacy color-control assignment changes=%d want %d", got, legacyColorBefore)
	}
	if _, err := database.Exec(`UPDATE tags SET color='red' WHERE id=?`, tagColor); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, canonicalDeliveryA); got != canonicalABefore {
		t.Fatalf("canonical color-only tag update emitted %d changes", got-canonicalABefore)
	}
	if got := agentModeChangeCount(t, database, -legacyA); got != legacyColorBefore {
		t.Fatalf("legacy color-only tag update emitted %d changes", got-legacyColorBefore)
	}
	legacyABefore := agentModeChangeCount(t, database, -legacyA)
	if _, err := database.Exec(`INSERT INTO issue_tags(issue_id,tag_id) VALUES(?,?)`, legacyA, tagOne); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, -legacyA); got != legacyABefore+1 {
		t.Fatalf("legacy tag insert changes=%d want %d", got, legacyABefore+1)
	}
	if _, err := database.Exec(`UPDATE issue_tags SET tag_id=? WHERE issue_id=? AND tag_id=?`, tagTwo, legacyA, tagOne); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, -legacyA); got != legacyABefore+2 {
		t.Fatalf("legacy same-issue tag swap changes=%d want %d", got, legacyABefore+2)
	}
	legacyBBefore := agentModeChangeCount(t, database, -legacyB)
	if _, err := database.Exec(`UPDATE issue_tags SET issue_id=? WHERE issue_id=? AND tag_id=?`, legacyB, legacyA, tagTwo); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, -legacyA); got != legacyABefore+3 {
		t.Fatalf("legacy old issue tag move changes=%d want %d", got, legacyABefore+3)
	}
	if got := agentModeChangeCount(t, database, -legacyB); got != legacyBBefore+1 {
		t.Fatalf("legacy new issue tag move changes=%d want %d", got, legacyBBefore+1)
	}

	beforeA, beforeB := agentModeChangeCount(t, database, -legacyA), agentModeChangeCount(t, database, -legacyB)
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE issue_tags SET issue_id=? WHERE issue_id=? AND tag_id=?`, legacyA, legacyB, tagTwo); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, -legacyA); got != beforeA {
		t.Fatalf("tag rollback changed old root by %d", got-beforeA)
	}
	if got := agentModeChangeCount(t, database, -legacyB); got != beforeB {
		t.Fatalf("tag rollback changed new root by %d", got-beforeB)
	}
	if _, err := database.Exec(`UPDATE tags SET name='renamed' WHERE id=?`, tagTwo); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, -legacyB); got != beforeB+1 {
		t.Fatalf("tag rename changes=%d want %d", got, beforeB+1)
	}
}

func TestMigration145RelationUpdateInvalidatesOldAndNewSubtreesOnce(t *testing.T) {
	database := openTestDB(t)
	projectID := insertAgentModeProject(t, database, "Relations", "REL")
	parent := insertAgentModeIssue(t, database, projectID, 1, "epic", "Parent")
	otherParent := insertAgentModeIssue(t, database, projectID, 2, "epic", "Other parent")
	midA := insertAgentModeIssue(t, database, projectID, 3, "epic", "Mid A")
	midB := insertAgentModeIssue(t, database, projectID, 4, "epic", "Mid B")
	leafA := insertAgentModeIssue(t, database, projectID, 5, "ticket", "Leaf A")
	leafB := insertAgentModeIssue(t, database, projectID, 6, "ticket", "Leaf B")
	canonicalLeaf := insertAgentModeIssue(t, database, projectID, 7, "ticket", "Canonical leaf")
	_ = insertAgentModeLegacyRun(t, database, leafA, projectID, "running")
	_ = insertAgentModeLegacyRun(t, database, leafB, projectID, "running")
	canonicalDelivery := insertAgentModeDelivery(t, database, canonicalLeaf, projectID, "delivery:relation-canonical")
	for _, edge := range [][2]int64{{parent, midA}, {midA, leafA}, {midA, canonicalLeaf}, {midB, leafB}} {
		if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, edge[0], edge[1]); err != nil {
			t.Fatal(err)
		}
	}

	beforeA := agentModeChangeCount(t, database, -leafA)
	beforeB := agentModeChangeCount(t, database, -leafB)
	beforeCanonical := agentModeChangeCount(t, database, canonicalDelivery)
	if _, err := database.Exec(`UPDATE issue_relations SET target_id=?
		WHERE source_id=? AND target_id=? AND type='parent'`, midB, parent, midA); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, -leafA); got != beforeA+1 {
		t.Fatalf("old subtree changes=%d want %d", got, beforeA+1)
	}
	if got := agentModeChangeCount(t, database, -leafB); got != beforeB+1 {
		t.Fatalf("new subtree changes=%d want %d", got, beforeB+1)
	}
	if got := agentModeChangeCount(t, database, canonicalDelivery); got != beforeCanonical+1 {
		t.Fatalf("canonical old subtree changes=%d want %d", got, beforeCanonical+1)
	}

	beforeB = agentModeChangeCount(t, database, -leafB)
	if _, err := database.Exec(`UPDATE issue_relations SET type='blocks'
		WHERE source_id=? AND target_id=? AND type='parent'`, parent, midB); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, -leafB); got != beforeB+1 {
		t.Fatalf("parent-to-blocks changes=%d want %d", got, beforeB+1)
	}
	beforeB++
	if _, err := database.Exec(`UPDATE issue_relations SET type='parent'
		WHERE source_id=? AND target_id=? AND type='blocks'`, parent, midB); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, -leafB); got != beforeB+1 {
		t.Fatalf("blocks-to-parent changes=%d want %d", got, beforeB+1)
	}
	beforeB++
	if _, err := database.Exec(`UPDATE issue_relations SET source_id=?
		WHERE source_id=? AND target_id=? AND type='parent'`, otherParent, parent, midB); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, -leafB); got != beforeB+1 {
		t.Fatalf("overlapping source swap changes=%d want %d", got, beforeB+1)
	}
	beforeB++
	if _, err := database.Exec(`UPDATE issue_relations SET rank=rank+1
		WHERE source_id=? AND target_id=? AND type='parent'`, otherParent, midB); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, -leafB); got != beforeB {
		t.Fatalf("rank-only update emitted %d changes", got-beforeB)
	}
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE issue_relations SET source_id=?
		WHERE source_id=? AND target_id=? AND type='parent'`, parent, otherParent, midB); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, -leafB); got != beforeB {
		t.Fatalf("relation rollback emitted %d changes", got-beforeB)
	}
}

func TestMigration145CanonicalInvalidationsAreFieldExactAndNonDuplicating(t *testing.T) {
	database := openTestDB(t)
	projectID := insertAgentModeProject(t, database, "Canonical invalidation", "CIV")

	issueIDs := map[string]int64{}
	deliveryIDs := map[string]int64{}
	for index, name := range []string{
		"source", "other_source", "old_root", "old_leaf", "new_root", "new_leaf", "unrelated",
	} {
		issueID := insertAgentModeIssue(t, database, projectID, int64(index+1), "ticket", name)
		issueIDs[name] = issueID
		deliveryIDs[name] = insertAgentModeDelivery(t, database, issueID, projectID, "delivery:canonical:"+name)
	}
	for _, edge := range [][2]int64{
		{issueIDs["old_root"], issueIDs["old_leaf"]},
		{issueIDs["new_root"], issueIDs["new_leaf"]},
	} {
		if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, edge[0], edge[1]); err != nil {
			t.Fatal(err)
		}
	}

	totals := map[string]int{}
	issues := map[string]int{}
	lanes := map[string]int{}
	refresh := func() {
		for name, deliveryID := range deliveryIDs {
			totals[name] = agentModeChangeCount(t, database, deliveryID)
			issues[name] = agentModeKindCount(t, database, deliveryID, "issue")
			lanes[name] = agentModeKindCount(t, database, deliveryID, "lane")
		}
	}
	refresh()
	assertDeltas := func(label, kind string, want map[string]int) {
		t.Helper()
		for name, deliveryID := range deliveryIDs {
			wantDelta := want[name]
			gotTotal := agentModeChangeCount(t, database, deliveryID)
			if delta := gotTotal - totals[name]; delta != wantDelta {
				t.Fatalf("%s %s total delta=%d want %d", label, name, delta, wantDelta)
			}
			gotIssues := agentModeKindCount(t, database, deliveryID, "issue")
			gotLanes := agentModeKindCount(t, database, deliveryID, "lane")
			switch kind {
			case "issue":
				if delta := gotIssues - issues[name]; delta != wantDelta {
					t.Fatalf("%s %s issue delta=%d want %d", label, name, delta, wantDelta)
				}
				if delta := gotLanes - lanes[name]; delta != 0 {
					t.Fatalf("%s %s unexpected lane delta=%d", label, name, delta)
				}
			case "lane":
				if delta := gotLanes - lanes[name]; delta != wantDelta {
					t.Fatalf("%s %s lane delta=%d want %d", label, name, delta, wantDelta)
				}
				if delta := gotIssues - issues[name]; delta != 0 {
					t.Fatalf("%s %s unexpected issue delta=%d", label, name, delta)
				}
			case "":
				if gotIssues != issues[name] || gotLanes != lanes[name] {
					t.Fatalf("%s %s changed issue/lane counts", label, name)
				}
			default:
				t.Fatalf("unknown expected kind %q", kind)
			}
		}
		refresh()
	}

	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`,
		issueIDs["source"], issueIDs["old_root"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("parent insert", "lane", map[string]int{"old_root": 1, "old_leaf": 1})

	if _, err := database.Exec(`DELETE FROM issue_relations WHERE source_id=? AND target_id=? AND type='parent'`,
		issueIDs["source"], issueIDs["old_root"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("parent delete", "lane", map[string]int{"old_root": 1, "old_leaf": 1})

	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`,
		issueIDs["source"], issueIDs["old_root"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("parent reinsert", "lane", map[string]int{"old_root": 1, "old_leaf": 1})
	if _, err := database.Exec(`UPDATE issue_relations SET target_id=?
		WHERE source_id=? AND target_id=? AND type='parent'`, issueIDs["new_root"],
		issueIDs["source"], issueIDs["old_root"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("parent target update", "lane", map[string]int{
		"old_root": 1, "old_leaf": 1, "new_root": 1, "new_leaf": 1,
	})
	if _, err := database.Exec(`UPDATE issue_relations SET source_id=?
		WHERE source_id=? AND target_id=? AND type='parent'`, issueIDs["other_source"],
		issueIDs["source"], issueIDs["new_root"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("parent source update", "lane", map[string]int{"new_root": 1, "new_leaf": 1})
	if _, err := database.Exec(`UPDATE issue_relations SET type='blocks'
		WHERE source_id=? AND target_id=? AND type='parent'`, issueIDs["other_source"], issueIDs["new_root"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("parent to blocks", "lane", map[string]int{"new_root": 1, "new_leaf": 1})

	if _, err := database.Exec(`UPDATE issue_relations SET target_id=?
		WHERE source_id=? AND target_id=? AND type='blocks'`, issueIDs["old_root"],
		issueIDs["other_source"], issueIDs["new_root"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("blocks endpoint update", "", nil)
	if _, err := database.Exec(`UPDATE issue_relations SET rank=rank+1
		WHERE source_id=? AND target_id=? AND type='blocks'`, issueIDs["other_source"], issueIDs["old_root"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("blocks rank update", "", nil)
	if _, err := database.Exec(`DELETE FROM issue_relations WHERE source_id=? AND target_id=? AND type='blocks'`,
		issueIDs["other_source"], issueIDs["old_root"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("blocks delete", "", nil)

	userResult, err := database.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('canonical-invalidation-worker','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := userResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	timeResult, err := database.Exec(`INSERT INTO time_entries(issue_id,user_id,started_at,override)
		VALUES(?,?,?,?)`, issueIDs["source"], userID, "2026-08-20T10:00:00Z", 1.0)
	if err != nil {
		t.Fatal(err)
	}
	timeEntryID, err := timeResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	assertDeltas("time entry insert", "", nil)
	if _, err := database.Exec(`UPDATE time_entries SET override=2 WHERE id=?`, timeEntryID); err != nil {
		t.Fatal(err)
	}
	assertDeltas("time entry update", "", nil)
	if _, err := database.Exec(`DELETE FROM time_entries WHERE id=?`, timeEntryID); err != nil {
		t.Fatal(err)
	}
	assertDeltas("time entry delete", "", nil)
	if _, err := database.Exec(`UPDATE issues SET content_rev=content_rev+1 WHERE id=?`, issueIDs["source"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("content revision only", "", nil)
	if _, err := database.Exec(`UPDATE issues SET issue_number=issue_number WHERE id=?`, issueIDs["source"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("issue number no-op", "", nil)
	if _, err := database.Exec(`UPDATE issues SET issue_number=100 WHERE id=?`, issueIDs["source"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("issue number", "issue", map[string]int{"source": 1})
	if _, err := database.Exec(`UPDATE issues SET updated_at='2026-08-20T11:00:00Z' WHERE id=?`, issueIDs["source"]); err != nil {
		t.Fatal(err)
	}
	assertDeltas("updated at", "issue", map[string]int{"source": 1})

	cycleRoot := insertAgentModeIssue(t, database, projectID, 101, "ticket", "cycle root")
	cycleChild := insertAgentModeIssue(t, database, projectID, 102, "ticket", "cycle child")
	cycleRootDelivery := insertAgentModeDelivery(t, database, cycleRoot, projectID, "delivery:canonical:cycle-root")
	cycleChildDelivery := insertAgentModeDelivery(t, database, cycleChild, projectID, "delivery:canonical:cycle-child")
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent'),(?,?,'parent')`,
		cycleRoot, cycleChild, cycleChild, cycleRoot); err != nil {
		t.Fatal(err)
	}
	rootTotal := agentModeChangeCount(t, database, cycleRootDelivery)
	rootIssues := agentModeKindCount(t, database, cycleRootDelivery, "issue")
	rootLanes := agentModeKindCount(t, database, cycleRootDelivery, "lane")
	childTotal := agentModeChangeCount(t, database, cycleChildDelivery)
	childIssues := agentModeKindCount(t, database, cycleChildDelivery, "issue")
	childLanes := agentModeKindCount(t, database, cycleChildDelivery, "lane")
	if _, err := database.Exec(`UPDATE issues SET issue_number=103 WHERE id=?`, cycleRoot); err != nil {
		t.Fatal(err)
	}
	if got := agentModeChangeCount(t, database, cycleRootDelivery); got != rootTotal+1 {
		t.Fatalf("cycle root total delta=%d want 1", got-rootTotal)
	}
	if got := agentModeKindCount(t, database, cycleRootDelivery, "issue"); got != rootIssues+1 {
		t.Fatalf("cycle root issue delta=%d want 1", got-rootIssues)
	}
	if got := agentModeKindCount(t, database, cycleRootDelivery, "lane"); got != rootLanes {
		t.Fatalf("cycle root duplicated direct fact with %d lane rows", got-rootLanes)
	}
	if got := agentModeChangeCount(t, database, cycleChildDelivery); got != childTotal+1 {
		t.Fatalf("cycle child total delta=%d want 1", got-childTotal)
	}
	if got := agentModeKindCount(t, database, cycleChildDelivery, "issue"); got != childIssues {
		t.Fatalf("cycle child unexpected issue delta=%d", got-childIssues)
	}
	if got := agentModeKindCount(t, database, cycleChildDelivery, "lane"); got != childLanes+1 {
		t.Fatalf("cycle child lane delta=%d want 1", got-childLanes)
	}
}

func TestMigration145ProjectMoveVisibilityBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		startLive   bool
		endLive     bool
		wantKind    string
		wantRevoked bool
	}{
		{"visible to visible", true, true, "project_move", true},
		{"visible to hidden", true, false, "issue", false},
		{"hidden to visible", false, true, "issue", false},
		{"hidden to hidden", false, false, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			database := openTestDB(t)
			projectA := insertAgentModeProject(t, database, "Boundary A", "BDA")
			projectB := insertAgentModeProject(t, database, "Boundary B", "BDB")
			issueID := insertAgentModeIssue(t, database, projectA, 1, "ticket", tc.name)
			deliveryID := insertAgentModeDelivery(t, database, issueID, projectA, "delivery:boundary")
			if !tc.startLive {
				if _, err := database.Exec(`UPDATE issues SET deleted_at='2026-08-20T10:00:00Z' WHERE id=?`, issueID); err != nil {
					t.Fatal(err)
				}
			}
			before := agentModeChangeCount(t, database, deliveryID)
			var deletedAt any = "2026-08-20T11:00:00Z"
			if tc.endLive {
				deletedAt = nil
			}
			if _, err := database.Exec(`UPDATE issues SET project_id=?,deleted_at=? WHERE id=?`, projectB, deletedAt, issueID); err != nil {
				t.Fatalf("combined transition: %v", err)
			}
			wantDelta := 1
			if tc.wantKind == "" {
				wantDelta = 0
			}
			if got := agentModeChangeCount(t, database, deliveryID); got != before+wantDelta {
				t.Fatalf("change count=%d want %d", got, before+wantDelta)
			}
			var hintProject int64
			var pending sql.NullInt64
			if err := database.QueryRow(`SELECT project_id_hint,pending_revoked_project_id FROM deliveries WHERE id=?`, deliveryID).
				Scan(&hintProject, &pending); err != nil {
				t.Fatal(err)
			}
			if hintProject != projectB || pending.Valid {
				t.Fatalf("stored project=%d pending=%+v want %d/NULL", hintProject, pending, projectB)
			}
			if wantDelta == 0 {
				return
			}
			var kind string
			var target int64
			var revoked sql.NullInt64
			if err := database.QueryRow(`SELECT kind,project_id_hint,revoked_project_id FROM delivery_change_log
				WHERE delivery_id=? ORDER BY change_sequence DESC LIMIT 1`, deliveryID).Scan(&kind, &target, &revoked); err != nil {
				t.Fatal(err)
			}
			wantTarget := projectB
			if tc.startLive && !tc.endLive {
				wantTarget = projectA
			}
			if kind != tc.wantKind || target != wantTarget || revoked.Valid != tc.wantRevoked ||
				(revoked.Valid && revoked.Int64 != projectA) {
				t.Fatalf("boundary fact=(%q,target=%d,revoked=%+v)", kind, target, revoked)
			}
		})
	}

	t.Run("hidden move then restore", func(t *testing.T) {
		database := openTestDB(t)
		projectA := insertAgentModeProject(t, database, "Restore A", "RSA")
		projectB := insertAgentModeProject(t, database, "Restore B", "RSB")
		issueID := insertAgentModeIssue(t, database, projectA, 1, "ticket", "Restore")
		deliveryID := insertAgentModeDelivery(t, database, issueID, projectA, "delivery:restore")
		if _, err := database.Exec(`UPDATE issues SET deleted_at='2026-08-20T10:00:00Z' WHERE id=?`, issueID); err != nil {
			t.Fatal(err)
		}
		before := agentModeChangeCount(t, database, deliveryID)
		if _, err := database.Exec(`UPDATE issues SET project_id=? WHERE id=?`, projectB, issueID); err != nil {
			t.Fatal(err)
		}
		if got := agentModeChangeCount(t, database, deliveryID); got != before {
			t.Fatalf("hidden move emitted %d changes", got-before)
		}
		if _, err := database.Exec(`UPDATE issues SET deleted_at=NULL WHERE id=?`, issueID); err != nil {
			t.Fatal(err)
		}
		if got := agentModeChangeCount(t, database, deliveryID); got != before+1 {
			t.Fatalf("restore changes=%d want %d", got, before+1)
		}
		var kind string
		var target int64
		var revoked sql.NullInt64
		if err := database.QueryRow(`SELECT kind,project_id_hint,revoked_project_id FROM delivery_change_log
			WHERE delivery_id=? ORDER BY change_sequence DESC LIMIT 1`, deliveryID).Scan(&kind, &target, &revoked); err != nil {
			t.Fatal(err)
		}
		if kind != "issue" || target != projectB || revoked.Valid {
			t.Fatalf("restore fact=(%q,target=%d,revoked=%+v)", kind, target, revoked)
		}
	})
}

func TestMigration145AgentRunCreationLineageIsImmutable(t *testing.T) {
	database := openTestDB(t)
	projectID := insertAgentModeProject(t, database, "Immutable runs", "IMR")
	issueID := insertAgentModeIssue(t, database, projectID, 1, "ticket", "Original")
	otherIssueID := insertAgentModeIssue(t, database, projectID, 2, "ticket", "Other")
	runID := insertAgentModeLegacyRun(t, database, issueID, projectID, "running")
	for _, tc := range []struct {
		name string
		sql  string
		args []any
	}{
		{"id", `UPDATE agent_runs SET id=? WHERE id=?`, []any{runID + 1000, runID}},
		{"issue", `UPDATE agent_runs SET issue_id=? WHERE id=?`, []any{otherIssueID, runID}},
		{"instrumentation", `UPDATE agent_runs SET delivery_instrumentation_version=1 WHERE id=?`, []any{runID}},
		{"combined", `UPDATE agent_runs SET id=?,issue_id=?,delivery_instrumentation_version=1 WHERE id=?`, []any{runID + 1000, otherIssueID, runID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := database.Exec(tc.sql, tc.args...); err == nil {
				t.Fatal("mutable creation lineage was accepted")
			}
			var gotIssue, gotVersion int64
			if err := database.QueryRow(`SELECT issue_id,delivery_instrumentation_version FROM agent_runs WHERE id=?`, runID).
				Scan(&gotIssue, &gotVersion); err != nil {
				t.Fatal(err)
			}
			if gotIssue != issueID || gotVersion != 0 {
				t.Fatalf("run lineage=(%d,%d)", gotIssue, gotVersion)
			}
			assertLegacyRootState(t, database, issueID, projectID, 1)
		})
	}
}

func TestMigration145HiddenRootsCannotForgeChangeFacts(t *testing.T) {
	t.Run("canonical soft deleted", func(t *testing.T) {
		database := openTestDB(t)
		projectID := insertAgentModeProject(t, database, "Hidden canonical", "HDC")
		issueID := insertAgentModeIssue(t, database, projectID, 1, "ticket", "Hidden canonical")
		deliveryID := insertAgentModeDelivery(t, database, issueID, projectID, "delivery:hidden-canonical")
		if _, err := database.Exec(`UPDATE issues SET deleted_at='2026-08-20T12:00:00Z' WHERE id=?`, issueID); err != nil {
			t.Fatal(err)
		}
		before := agentModeChangeCount(t, database, deliveryID)
		var highWater int64
		if err := database.QueryRow(`SELECT change_sequence_high_water FROM deliveries WHERE id=?`, deliveryID).Scan(&highWater); err != nil {
			t.Fatal(err)
		}
		_, err := database.Exec(`INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,
			project_id_hint,change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			VALUES('00000000000000000000000000007771',?,?,?,?,?,0,'issue','issue',?,'2026-08-20T12:00:01Z')`,
			deliveryID, issueID, "delivery:hidden-canonical", projectID, highWater+1, issueID)
		if err == nil || !strings.Contains(err.Error(), "provenance") {
			t.Fatalf("hidden canonical forgery error=%v", err)
		}
		if got := agentModeChangeCount(t, database, deliveryID); got != before {
			t.Fatalf("hidden canonical forgery changed log by %d", got-before)
		}
	})

	t.Run("terminal legacy", func(t *testing.T) {
		database := openTestDB(t)
		projectID := insertAgentModeProject(t, database, "Terminal legacy", "TML")
		issueID := insertAgentModeIssue(t, database, projectID, 1, "ticket", "Terminal legacy")
		runID := insertAgentModeLegacyRun(t, database, issueID, projectID, "running")
		if _, err := database.Exec(`UPDATE agent_runs SET status='completed' WHERE id=?`, runID); err != nil {
			t.Fatal(err)
		}
		before := agentModeChangeCount(t, database, -issueID)
		var highWater int64
		if err := database.QueryRow(`SELECT change_sequence_high_water FROM agent_mode_legacy_roots WHERE issue_id=?`, issueID).Scan(&highWater); err != nil {
			t.Fatal(err)
		}
		_, err := database.Exec(`INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,
			project_id_hint,change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			VALUES('00000000000000000000000000007772',?,?,?,?,?,0,'issue','issue',?,'2026-08-20T12:00:01Z')`,
			-issueID, issueID, fmt.Sprintf("issue:%d", issueID), projectID, highWater+1, issueID)
		if err == nil || !strings.Contains(err.Error(), "provenance") {
			t.Fatalf("terminal legacy forgery error=%v", err)
		}
		if got := agentModeChangeCount(t, database, -issueID); got != before {
			t.Fatalf("terminal legacy forgery changed log by %d", got-before)
		}
	})
}

func TestMigration145CanonicalMoveProvenanceIsExactAndOneShot(t *testing.T) {
	database := openTestDB(t)
	projectA := insertAgentModeProject(t, database, "Move A", "MVA")
	projectB := insertAgentModeProject(t, database, "Move B", "MVB")
	projectC := insertAgentModeProject(t, database, "Move C", "MVC")
	issueID := insertAgentModeIssue(t, database, projectA, 1, "ticket", "Move guarded")
	deliveryID := insertAgentModeDelivery(t, database, issueID, projectA, "delivery:move-guarded")

	if _, err := database.Exec(`UPDATE deliveries SET project_id_hint=? WHERE id=?`, projectC, deliveryID); err == nil ||
		!strings.Contains(err.Error(), "project hint") {
		t.Fatalf("arbitrary project hint error=%v", err)
	}
	if _, err := database.Exec(`UPDATE issues SET project_id=? WHERE id=?`, projectB, issueID); err != nil {
		t.Fatal(err)
	}
	var moves, highWater int64
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log WHERE delivery_id=? AND kind='project_move'`, deliveryID).Scan(&moves); err != nil {
		t.Fatal(err)
	}
	if moves != 1 {
		t.Fatalf("legitimate move facts=%d want 1", moves)
	}
	if err := database.QueryRow(`SELECT change_sequence_high_water FROM deliveries WHERE id=?`, deliveryID).Scan(&highWater); err != nil {
		t.Fatal(err)
	}
	for index, revoked := range []int64{projectC, projectA} {
		_, err := database.Exec(`INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,
			project_id_hint,revoked_project_id,change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			VALUES(?,?,?,?,?,?,?,?, 'project_move','issue',?,'2026-08-20T12:00:02Z')`,
			fmt.Sprintf("%032x", 8800+index), deliveryID, issueID, "delivery:move-guarded", projectB, revoked,
			highWater+1, 0, issueID)
		if err == nil || !strings.Contains(err.Error(), "provenance") {
			t.Fatalf("reused/forged revoked project %d error=%v", revoked, err)
		}
	}
}

func seedAgentModeDelivery(t *testing.T, database *sql.DB, name, key string, issueNumber int64) (int64, int64, int64) {
	t.Helper()
	projectID := insertAgentModeProject(t, database, name, key)
	issueID := insertAgentModeIssue(t, database, projectID, issueNumber, "ticket", name+" issue")
	deliveryID := insertAgentModeDelivery(t, database, issueID, projectID, "delivery:"+key+":root")
	return projectID, issueID, deliveryID
}

func insertAgentModeProject(t *testing.T, database *sql.DB, name, key string) int64 {
	t.Helper()
	project, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES(?,?,'active')`, name, key)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	return projectID
}

func insertAgentModeTag(t *testing.T, database *sql.DB, name string) int64 {
	t.Helper()
	result, err := database.Exec(`INSERT INTO tags(name,color,description) VALUES(?,'blue','')`, name)
	if err != nil {
		t.Fatal(err)
	}
	tagID, _ := result.LastInsertId()
	return tagID
}

func insertAgentModeIssue(t *testing.T, database *sql.DB, projectID, number int64, kind, title string) int64 {
	t.Helper()
	result, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status) VALUES(?,?,?,?, 'in-progress')`,
		projectID, number, kind, title)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func insertAgentModeDelivery(t *testing.T, database *sql.DB, issueID, projectID int64, key string) int64 {
	t.Helper()
	result, err := database.Exec(`INSERT INTO deliveries(issue_id,delivery_key,project_id_hint,created_at,updated_at)
		VALUES(?,?,?,'2026-08-20T12:00:00Z','2026-08-20T12:00:00Z')`, issueID, key, projectID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func insertAgentModeLegacyRun(t *testing.T, database *sql.DB, issueID, projectID int64, status string) int64 {
	t.Helper()
	result, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status,delivery_instrumentation_version)
		VALUES(?,?,?,0)`, issueID, projectID, status)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	return runID
}

func assertLegacyRootState(t *testing.T, database *sql.DB, issueID, projectID, highWater int64) {
	t.Helper()
	var syntheticID, gotProjectID, gotHighWater int64
	var deliveryKey string
	if err := database.QueryRow(`SELECT synthetic_delivery_id,delivery_key,project_id_hint,change_sequence_high_water
		FROM agent_mode_legacy_roots WHERE issue_id=?`, issueID).
		Scan(&syntheticID, &deliveryKey, &gotProjectID, &gotHighWater); err != nil {
		t.Fatalf("legacy root %d: %v", issueID, err)
	}
	if syntheticID != -issueID || deliveryKey != fmt.Sprintf("issue:%d", issueID) ||
		gotProjectID != projectID || gotHighWater != highWater {
		t.Fatalf("legacy root %d=(%d,%q,%d,%d), want (%d,%q,%d,%d)", issueID,
			syntheticID, deliveryKey, gotProjectID, gotHighWater,
			-issueID, fmt.Sprintf("issue:%d", issueID), projectID, highWater)
	}
}

func appendAgentModeChange(t *testing.T, database *sql.DB, deliveryID, issueID, projectID int64, kind string, sequence int64) {
	t.Helper()
	token := fmt.Sprintf("%032x", deliveryID*1000+sequence)
	if _, err := database.Exec(`INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,
		project_id_hint,change_sequence,delivery_revision,kind,source_kind,server_received_at)
		SELECT ?,id,issue_id,delivery_key,project_id_hint,?,0,?,'issue','2026-08-20T12:00:00Z'
		FROM deliveries WHERE id=? AND issue_id=? AND project_id_hint=?`, token, sequence, kind,
		deliveryID, issueID, projectID); err != nil {
		t.Fatal(err)
	}
}

func agentModeChangeCount(t *testing.T, database *sql.DB, deliveryID int64) int {
	t.Helper()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log WHERE delivery_id=?`, deliveryID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func agentModeKindCount(t *testing.T, database *sql.DB, deliveryID int64, kind string) int {
	t.Helper()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log WHERE delivery_id=? AND kind=?`, deliveryID, kind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

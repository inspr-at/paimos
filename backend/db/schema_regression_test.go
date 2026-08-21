package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func schemaNames(t *testing.T, database *sql.DB, query string) []string {
	t.Helper()
	rows, err := database.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

const latestSchemaVersion = 148

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	prevDataDir := os.Getenv("DATA_DIR")
	prevTestMode := os.Getenv("PAIMOS_TEST_MODE")
	t.Cleanup(func() {
		_ = DB.Close()
		DB = nil
		_ = os.Setenv("DATA_DIR", prevDataDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", prevTestMode)
	})

	dataDir := t.TempDir()
	if err := os.Setenv("DATA_DIR", dataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("PAIMOS_TEST_MODE", "1"); err != nil {
		t.Fatal(err)
	}
	if err := Open(); err != nil {
		t.Fatalf("open db: %v", err)
	}
	return DB
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	found, err := SchemaHasTable(db, name)
	if err != nil {
		t.Fatalf("schema_has_table %s: %v", name, err)
	}
	return found
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	found, err := SchemaHasColumn(db, table, column)
	if err != nil {
		t.Fatalf("schema_has_column %s.%s: %v", table, column, err)
	}
	return found
}

func TestSchemaMigrationsReachLatestVersion(t *testing.T) {
	db := openTestDB(t)
	maxVersion, err := CurrentSchemaVersion(db)
	if err != nil {
		t.Fatalf("max schema version: %v", err)
	}
	if maxVersion != latestSchemaVersion {
		t.Fatalf("max schema version=%d want %d", maxVersion, latestSchemaVersion)
	}
}

func TestSchemaIntakeTables(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{"status", "language", "transcript", "rev", "pinned_project_id"} {
		if !columnExists(t, db, "intake_sessions", col) {
			t.Fatalf("expected intake_sessions.%s to exist (PAI-704 / M134)", col)
		}
	}
	for _, col := range []string{"session_id", "seq", "kind", "source", "payload_json"} {
		if !columnExists(t, db, "intake_events", col) {
			t.Fatalf("expected intake_events.%s to exist (PAI-704 / M134)", col)
		}
	}
}

func TestSchemaAgentRunsClaimedByColumn(t *testing.T) {
	db := openTestDB(t)
	if !columnExists(t, db, "agent_runs", "claimed_by") {
		t.Fatal("expected agent_runs.claimed_by to exist (PAI-624 / M128)")
	}
}

func TestSchemaAgentRunsCommitEvidenceColumns(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{"repo_url", "branch_name", "commit_base_sha", "commit_sha"} {
		if !columnExists(t, db, "agent_runs", col) {
			t.Fatalf("expected agent_runs.%s to exist (PAI-702 / M140)", col)
		}
	}
}

func TestSchemaAgentRunTelemetryTables(t *testing.T) {
	database := openTestDB(t)
	for _, table := range []string{"agent_run_telemetry", "agent_run_telemetry_latest"} {
		if !tableExists(t, database, table) {
			t.Fatalf("expected %s to exist (PAI-799 / M142)", table)
		}
	}
	for _, col := range []string{"sequence", "correlation_id", "provider", "adapter", "server_received_at", "progress_percent", "estimate_confidence"} {
		if !columnExists(t, database, "agent_run_telemetry", col) {
			t.Fatalf("expected agent_run_telemetry.%s to exist (PAI-799 / M142)", col)
		}
	}
	for _, col := range []string{"heartbeat_telemetry_id", "semantic_telemetry_id", "estimate_telemetry_id", "latest_event_at", "latest_semantic_at", "latest_estimate_at"} {
		if !columnExists(t, database, "agent_run_telemetry_latest", col) {
			t.Fatalf("expected agent_run_telemetry_latest.%s to exist (PAI-801 / M143)", col)
		}
	}
	if !columnExists(t, database, "agent_runs", "expects_supervisor_telemetry") {
		t.Fatal("expected agent_runs.expects_supervisor_telemetry to exist (PAI-801 / M143)")
	}
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Completed','CMP')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title) VALUES(?,1,'ticket','Completed')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status) VALUES(?,?,'completed')`, issueID, projectID); err != nil {
		t.Fatalf("completed must satisfy the M143 status CHECK: %v", err)
	}
	var fkTable string
	if err := database.QueryRow(`SELECT "table" FROM pragma_foreign_key_check LIMIT 1`).Scan(&fkTable); err != sql.ErrNoRows {
		t.Fatalf("M143 foreign-key check found table=%q err=%v", fkTable, err)
	}
}

func TestMigration143UpgradesPopulatedM142WithoutLosingGraphOrTelemetry(t *testing.T) {
	database := openTestDB(t)
	admin, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('m143-admin','hash','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	adminID, _ := admin.LastInsertId()
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M143 fixture','MFX')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status) VALUES(?,1,'ticket','upgrade fixture','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	draft, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,claimed_by,status,agent_name,session_id,version,tests_summary,deploy_target,error,action_key,provider_kind,provider_id,provider_label,model,run_mode,profile_id,effort,prompt_preset_ref,context_pack,context_truncated,context_sources_json,prompt_tokens,completion_tokens,finish_reason,repo_url,branch_name,commit_base_sha,commit_sha)
		VALUES(?,?,?,?, 'drafted','planner','session-draft','1.2.3','draft only','','','openrouter.draft','hosted','openrouter','OpenRouter','model-a','draft','profile-a','high','preset-a','full',1,'[{"source":"issue"}]',11,22,'stop','https://example.test/repo','draft-branch',?,?)`,
		issueID, projectID, adminID, adminID, strings.Repeat("a", 40), strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	draftID, _ := draft.LastInsertId()
	followup, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,claimed_by,status,agent_name,session_id,action_key,provider_kind,provider_id,provider_label,run_mode,source_draft_run_id,repo_url,branch_name,commit_base_sha,commit_sha)
		VALUES(?,?,?,?,'running','implementer','session-run','claude_cli.implement','local_cli','claude_cli','Claude Code','edit',?,'https://example.test/repo','feature/m143',?,?)`,
		issueID, projectID, adminID, adminID, draftID, strings.Repeat("b", 40), strings.Repeat("c", 40))
	if err != nil {
		t.Fatal(err)
	}
	followupID, _ := followup.LastInsertId()
	if _, err := database.Exec(`UPDATE agent_runs SET followup_run_id=? WHERE id=?`, followupID, draftID); err != nil {
		t.Fatal(err)
	}
	telemetry, err := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,heartbeat,phase)
		VALUES(?,1,'fixture-correlation','anthropic','claude-code','2026-08-20T10:00:00Z','2026-08-20T10:00:01Z','heartbeat',1,'implementing')`, followupID)
	if err != nil {
		t.Fatal(err)
	}
	telemetryID, _ := telemetry.LastInsertId()
	semantic, err := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,phase,activity)
		VALUES(?,2,'fixture-correlation','anthropic','claude-code','2026-08-20T10:00:02Z','2026-08-20T10:00:02Z','phase','implementing','editing')`, followupID)
	if err != nil {
		t.Fatal(err)
	}
	semanticID, _ := semantic.LastInsertId()
	estimate, err := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,phase,activity,estimate_revision,progress_percent,eta_seconds,eta_min_seconds,eta_max_seconds,estimate_source,estimate_confidence,estimate_basis)
		VALUES(?,3,'fixture-correlation','anthropic','claude-code','2026-08-20T10:00:03Z','2026-08-20T10:00:03Z','progress','testing','testing',1,50,300,240,360,'adapter',0.8,'half the checks passed')`, followupID)
	if err != nil {
		t.Fatal(err)
	}
	estimateID, _ := estimate.LastInsertId()
	latestHeartbeat, err := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,heartbeat,phase)
		VALUES(?,4,'fixture-correlation','anthropic','claude-code','2026-08-20T10:00:04Z','2026-08-20T10:00:04Z','heartbeat',1,'testing')`, followupID)
	if err != nil {
		t.Fatal(err)
	}
	latestHeartbeatID, _ := latestHeartbeat.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_run_telemetry_latest(run_id,telemetry_id,sequence,last_heartbeat_at,heartbeat_telemetry_id,latest_event_at)
		VALUES(?,?,1,'2026-08-20T10:00:01Z',?,'2026-08-20T10:00:01Z')`, followupID, telemetryID, telemetryID); err != nil {
		t.Fatal(err)
	}

	conn, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture rewinds only the agent-run tables. Drop every later trigger
	// whose SQL references agent_runs before the table rebuild; otherwise an
	// M145 cross-table guard makes SQLite validate a deliberately absent column
	// midway through the exact M142 reconstruction.
	for _, trigger := range schemaNames(t, database, `SELECT name FROM sqlite_master
		WHERE type='trigger' AND sql LIKE '%agent_runs%'`) {
		quoted := strings.ReplaceAll(trigger, `"`, `""`)
		if _, err := tx.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS "`+quoted+`"`); err != nil {
			_ = tx.Rollback()
			t.Fatalf("drop post-M142 trigger %q: %v", trigger, err)
		}
	}
	steps := []string{
		`DROP TRIGGER IF EXISTS trg_agent_run_telemetry_terminal_guard`,
		`DROP INDEX IF EXISTS idx_agent_runs_supervisor_active`,
		`DROP INDEX IF EXISTS idx_agent_run_telemetry_latest_heartbeat`,
		`ALTER TABLE agent_run_telemetry_latest DROP COLUMN latest_estimate_at`,
		`ALTER TABLE agent_run_telemetry_latest DROP COLUMN latest_semantic_at`,
		`ALTER TABLE agent_run_telemetry_latest DROP COLUMN latest_event_at`,
		`ALTER TABLE agent_run_telemetry_latest DROP COLUMN estimate_telemetry_id`,
		`ALTER TABLE agent_run_telemetry_latest DROP COLUMN semantic_telemetry_id`,
		`ALTER TABLE agent_run_telemetry_latest DROP COLUMN heartbeat_telemetry_id`,
		`CREATE TABLE agent_runs_m142_fixture (
			id INTEGER PRIMARY KEY AUTOINCREMENT, issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			project_id INTEGER REFERENCES projects(id) ON DELETE SET NULL, device_id TEXT NOT NULL DEFAULT '',
			requested_by INTEGER REFERENCES users(id) ON DELETE SET NULL, agent_name TEXT NOT NULL DEFAULT '', session_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','tests_passed','tests_failed','deployed','failed','cancelled','drafted')),
			version TEXT NOT NULL DEFAULT '', tests_summary TEXT, deploy_target TEXT NOT NULL DEFAULT '', log_attachment_id INTEGER,
			error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT (datetime('now')), started_at TEXT, finished_at TEXT,
			claimed_by INTEGER REFERENCES users(id) ON DELETE SET NULL, action_key TEXT NOT NULL DEFAULT 'claude_cli.implement',
			provider_kind TEXT NOT NULL DEFAULT 'local_cli', provider_id TEXT NOT NULL DEFAULT 'claude_cli', provider_label TEXT NOT NULL DEFAULT 'Claude Code',
			model TEXT NOT NULL DEFAULT '', run_mode TEXT NOT NULL DEFAULT 'edit', profile_id TEXT NOT NULL DEFAULT '', effort TEXT NOT NULL DEFAULT '',
			prompt_preset_ref TEXT NOT NULL DEFAULT '', context_pack TEXT NOT NULL DEFAULT '', context_truncated INTEGER NOT NULL DEFAULT 0,
			context_sources_json TEXT NOT NULL DEFAULT '', prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0,
			finish_reason TEXT NOT NULL DEFAULT '', source_draft_run_id INTEGER REFERENCES agent_runs_m142_fixture(id) ON DELETE SET NULL,
			followup_run_id INTEGER REFERENCES agent_runs_m142_fixture(id) ON DELETE SET NULL, repo_url TEXT NOT NULL DEFAULT '', branch_name TEXT NOT NULL DEFAULT '',
			commit_base_sha TEXT NOT NULL DEFAULT '', commit_sha TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO agent_runs_m142_fixture SELECT id,issue_id,project_id,device_id,requested_by,agent_name,session_id,status,version,tests_summary,deploy_target,log_attachment_id,error,created_at,started_at,finished_at,claimed_by,action_key,provider_kind,provider_id,provider_label,model,run_mode,profile_id,effort,prompt_preset_ref,context_pack,context_truncated,context_sources_json,prompt_tokens,completion_tokens,finish_reason,source_draft_run_id,followup_run_id,repo_url,branch_name,commit_base_sha,commit_sha FROM agent_runs`,
		`DROP TABLE agent_runs`,
		`ALTER TABLE agent_runs_m142_fixture RENAME TO agent_runs`,
		`CREATE INDEX idx_agent_runs_issue ON agent_runs(issue_id)`,
		`CREATE INDEX idx_agent_runs_status ON agent_runs(status)`,
		`CREATE UNIQUE INDEX idx_agent_runs_active_issue ON agent_runs(issue_id) WHERE status IN ('queued','running')`,
		`CREATE INDEX idx_agent_runs_claimed_by ON agent_runs(claimed_by)`,
		`CREATE INDEX idx_agent_runs_action_key ON agent_runs(action_key)`,
		`CREATE INDEX idx_agent_runs_run_mode ON agent_runs(run_mode)`,
		`CREATE INDEX idx_agent_runs_provider_id ON agent_runs(provider_id)`,
		`CREATE INDEX idx_agent_runs_source_draft ON agent_runs(source_draft_run_id)`,
		`CREATE INDEX idx_agent_runs_followup ON agent_runs(followup_run_id)`,
		`CREATE TRIGGER trg_agent_run_telemetry_terminal_guard BEFORE INSERT ON agent_run_telemetry
		 WHEN (SELECT status FROM agent_runs WHERE id=NEW.run_id) IN ('tests_passed','tests_failed','deployed','failed','cancelled','drafted')
		 BEGIN SELECT RAISE(ABORT, 'terminal run telemetry is immutable'); END`,
		`UPDATE sqlite_sequence SET seq=50 WHERE name='agent_runs'`,
		`DELETE FROM schema_versions WHERE version IN (143,144)`,
	}
	for _, step := range steps {
		if _, err := tx.ExecContext(context.Background(), step); err != nil {
			_ = tx.Rollback()
			t.Fatalf("prepare M142 fixture step %q: %v", step, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	m142Columns := schemaNames(t, database, `SELECT name FROM pragma_table_info('agent_runs')`)
	m142Indexes := schemaNames(t, database, `SELECT name FROM pragma_index_list('agent_runs') WHERE origin='c'`)
	exactM142Columns := []string{
		"id", "issue_id", "project_id", "device_id", "requested_by", "agent_name", "session_id", "status", "version", "tests_summary",
		"deploy_target", "log_attachment_id", "error", "created_at", "started_at", "finished_at", "claimed_by", "action_key", "provider_kind",
		"provider_id", "provider_label", "model", "run_mode", "profile_id", "effort", "prompt_preset_ref", "context_pack", "context_truncated",
		"context_sources_json", "prompt_tokens", "completion_tokens", "finish_reason", "source_draft_run_id", "followup_run_id", "repo_url",
		"branch_name", "commit_base_sha", "commit_sha",
	}
	exactM142Indexes := []string{
		"idx_agent_runs_issue", "idx_agent_runs_status", "idx_agent_runs_active_issue", "idx_agent_runs_claimed_by", "idx_agent_runs_action_key",
		"idx_agent_runs_run_mode", "idx_agent_runs_provider_id", "idx_agent_runs_source_draft", "idx_agent_runs_followup",
	}
	sort.Strings(exactM142Columns)
	sort.Strings(exactM142Indexes)
	if strings.Join(m142Columns, "\x00") != strings.Join(exactM142Columns, "\x00") || strings.Join(m142Indexes, "\x00") != strings.Join(exactM142Indexes, "\x00") {
		t.Fatalf("fixture is not exact M142 schema: columns=%v indexes=%v", m142Columns, m142Indexes)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	DB = nil
	if err := Open(); err != nil {
		t.Fatalf("restart applying M143→M144: %v", err)
	}
	database = DB

	wantColumns := append(append([]string(nil), m142Columns...), "expects_supervisor_telemetry", "delivery_instrumentation_version")
	sort.Strings(wantColumns)
	if got := schemaNames(t, database, `SELECT name FROM pragma_table_info('agent_runs')`); strings.Join(got, "\x00") != strings.Join(wantColumns, "\x00") {
		t.Fatalf("M143→M144 columns=%v want M142+intentional=%v", got, wantColumns)
	}
	wantIndexes := append(append([]string(nil), m142Indexes...), "idx_agent_runs_supervisor_active", "idx_agent_runs_id_issue", "idx_agent_runs_delivery_legacy_active")
	sort.Strings(wantIndexes)
	if got := schemaNames(t, database, `SELECT name FROM pragma_index_list('agent_runs') WHERE origin='c'`); strings.Join(got, "\x00") != strings.Join(wantIndexes, "\x00") {
		t.Fatalf("M143→M144 indexes=%v want M142+intentional=%v", got, wantIndexes)
	}
	var gotFollowup, gotSource sql.NullInt64
	var contextJSON, repoURL, branch, baseSHA, headSHA string
	if err := database.QueryRow(`SELECT followup_run_id,context_sources_json,repo_url,branch_name,commit_base_sha,commit_sha FROM agent_runs WHERE id=?`, draftID).
		Scan(&gotFollowup, &contextJSON, &repoURL, &branch, &baseSHA, &headSHA); err != nil {
		t.Fatal(err)
	}
	if !gotFollowup.Valid || gotFollowup.Int64 != followupID || contextJSON != `[{"source":"issue"}]` || repoURL != "https://example.test/repo" || branch != "draft-branch" || baseSHA != strings.Repeat("a", 40) || headSHA != strings.Repeat("b", 40) {
		t.Fatalf("draft row not preserved: followup=%v context=%q repo=%q branch=%q base=%q head=%q", gotFollowup, contextJSON, repoURL, branch, baseSHA, headSHA)
	}
	if err := database.QueryRow(`SELECT source_draft_run_id FROM agent_runs WHERE id=?`, followupID).Scan(&gotSource); err != nil || !gotSource.Valid || gotSource.Int64 != draftID {
		t.Fatalf("followup source=%v err=%v", gotSource, err)
	}
	var childRun, latestTelemetry, heartbeatTelemetry, semanticTelemetry, estimateTelemetry int64
	if err := database.QueryRow(`SELECT t.run_id,l.telemetry_id,l.heartbeat_telemetry_id,l.semantic_telemetry_id,l.estimate_telemetry_id FROM agent_run_telemetry t JOIN agent_run_telemetry_latest l ON l.run_id=t.run_id WHERE t.id=?`, telemetryID).
		Scan(&childRun, &latestTelemetry, &heartbeatTelemetry, &semanticTelemetry, &estimateTelemetry); err != nil || childRun != followupID || latestTelemetry != latestHeartbeatID || heartbeatTelemetry != latestHeartbeatID || semanticTelemetry != latestHeartbeatID || estimateTelemetry != estimateID {
		t.Fatalf("rebuilt telemetry pointers run=%d latest=%d heartbeat=%d semantic=%d estimate=%d err=%v (seed semantic=%d)", childRun, latestTelemetry, heartbeatTelemetry, semanticTelemetry, estimateTelemetry, err, semanticID)
	}
	var fkTable string
	if err := database.QueryRow(`SELECT "table" FROM pragma_foreign_key_check LIMIT 1`).Scan(&fkTable); err != sql.ErrNoRows {
		t.Fatalf("foreign_key_check table=%q err=%v", fkTable, err)
	}
	newRun, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status) VALUES(?,?,'completed')`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	newID, _ := newRun.LastInsertId()
	if newID <= 50 {
		t.Fatalf("sqlite_sequence regressed: next id=%d", newID)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET status='tests_passed',tests_summary='fixture tests passed' WHERE id=?`, followupID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind)
		VALUES(?,2,'fixture-correlation','anthropic','claude-code','2026-08-20T10:00:02Z','2026-08-20T10:00:02Z','phase')`, followupID); err == nil || !strings.Contains(err.Error(), "terminal run telemetry") {
		t.Fatalf("M143 terminal guard error=%v", err)
	}
}

func TestSchemaAgentRunTelemetryAppendOnlyAndTerminalGuards(t *testing.T) {
	database := openTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name, key) VALUES('Telemetry', 'TEL')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id, issue_number, type, title) VALUES(?, 1, 'ticket', 'Telemetry')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id, project_id, status) VALUES(?, ?, 'running')`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	_, err = database.Exec(`INSERT INTO agent_run_telemetry(
		run_id, sequence, correlation_id, provider, adapter, agent_reported_at, server_received_at, kind, heartbeat)
		VALUES(?, 1, 'run-1', 'anthropic', 'claude-code', '2026-08-20T10:00:00Z', '2026-08-20T10:00:01Z', 'heartbeat', 1)`, runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE agent_run_telemetry SET phase='testing' WHERE run_id=?`, runID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("append-only update error=%v", err)
	}
	if _, err := database.Exec(`INSERT INTO agent_run_telemetry(
		run_id, sequence, correlation_id, provider, adapter, agent_reported_at, server_received_at, kind)
		VALUES(?, 1, 'run-1', 'anthropic', 'claude-code', '2026-08-20T10:00:02Z', '2026-08-20T10:00:02Z', 'progress')`, runID); err == nil || !strings.Contains(err.Error(), "sequence is not monotonic") {
		t.Fatalf("monotonic insert error=%v", err)
	}
	terminalStatuses := []string{"completed", "tests_passed", "tests_failed", "deployed", "failed", "cancelled", "drafted"}
	for i, status := range terminalStatuses {
		terminalRunID := runID
		if i > 0 {
			issue, err := database.Exec(`INSERT INTO issues(project_id, issue_number, type, title) VALUES(?, ?, 'ticket', ?)`, projectID, i+1, "Telemetry "+status)
			if err != nil {
				t.Fatal(err)
			}
			terminalIssueID, _ := issue.LastInsertId()
			run, err := database.Exec(`INSERT INTO agent_runs(issue_id, project_id, status) VALUES(?, ?, 'running')`, terminalIssueID, projectID)
			if err != nil {
				t.Fatal(err)
			}
			terminalRunID, _ = run.LastInsertId()
			if _, err := database.Exec(`INSERT INTO agent_run_telemetry(
				run_id, sequence, correlation_id, provider, adapter, agent_reported_at, server_received_at, kind, heartbeat)
				VALUES(?, 1, 'run-1', 'anthropic', 'claude-code', '2026-08-20T10:00:00Z', '2026-08-20T10:00:01Z', 'heartbeat', 1)`, terminalRunID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := database.Exec(`UPDATE agent_runs SET status=? WHERE id=?`, status, terminalRunID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO agent_run_telemetry(
			run_id, sequence, correlation_id, provider, adapter, agent_reported_at, server_received_at, kind)
			VALUES(?, 2, 'run-1', 'anthropic', 'claude-code', '2026-08-20T10:00:03Z', '2026-08-20T10:00:03Z', 'progress')`, terminalRunID); err == nil || !strings.Contains(err.Error(), "terminal run telemetry") {
			t.Fatalf("status %s terminal insert error=%v", status, err)
		}
	}
}

func TestSchemaAgentRunTelemetryUTF8ByteBounds(t *testing.T) {
	database := openTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Telemetry bytes','TBY')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title) VALUES(?,1,'ticket','UTF-8 bounds')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status) VALUES(?,?,'running')`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	insert := func(sequence int, activity, basis string) error {
		_, err := database.Exec(`INSERT INTO agent_run_telemetry(
			run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,phase,activity,
			estimate_revision,progress_percent,eta_seconds,eta_min_seconds,eta_max_seconds,estimate_source,estimate_confidence,estimate_basis)
			VALUES(?,?,'bytes-1','paimos','test','2026-08-20T10:00:00Z','2026-08-20T10:00:00Z','progress','testing',?, ?,50,300,240,360,'adapter',0.8,?)`,
			runID, sequence, activity, sequence, basis)
		return err
	}
	if err := insert(1, strings.Repeat("é", 140), strings.Repeat("é", 120)); err != nil {
		t.Fatalf("exact UTF-8 byte bounds rejected: %v", err)
	}
	if err := insert(2, strings.Repeat("é", 141), "valid basis"); err == nil {
		t.Fatal("281-byte activity passed the storage boundary")
	}
	if err := insert(2, "valid activity", strings.Repeat("é", 121)); err == nil {
		t.Fatal("242-byte estimate basis passed the storage boundary")
	}
	var tableSQL, triggerSQL string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='agent_run_telemetry'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='trg_agent_run_telemetry_byte_bounds'`).Scan(&triggerSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tableSQL, "length(CAST(activity AS BLOB))") || !strings.Contains(tableSQL, "length(CAST(estimate_basis AS BLOB))") || !strings.Contains(triggerSQL, "CAST(NEW.activity AS BLOB)") {
		t.Fatalf("byte-bound schema missing: table=%q trigger=%q", tableSQL, triggerSQL)
	}
}

func TestMigration143PreconditionRejectsLegacyCodePointBoundRows(t *testing.T) {
	legacy, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	legacy.SetMaxOpenConns(1)
	if _, err := legacy.Exec(`CREATE TABLE agent_run_telemetry(
		id INTEGER PRIMARY KEY, activity TEXT NOT NULL CHECK(length(activity)<=280),
		estimate_basis TEXT NOT NULL CHECK(length(estimate_basis)<=240))`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO agent_run_telemetry(id,activity,estimate_basis) VALUES(1,?,'')`, strings.Repeat("é", 141)); err != nil {
		t.Fatalf("legacy code-point constraint should admit the fixture: %v", err)
	}
	conn, err := legacy.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := checkAgentRunTelemetryByteBounds(context.Background(), conn); err == nil || !strings.Contains(err.Error(), "activity_bytes=282") {
		t.Fatalf("M143 precondition error=%v", err)
	}
}

func TestRebuildAgentRunTelemetryLatestMatchesIncrementalProjection(t *testing.T) {
	database := openTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Projection rebuild','PRB')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	type projectionRow struct {
		runID, telemetryID, sequence               int64
		lastHeartbeatAt                            sql.NullString
		heartbeatID, semanticID, estimateID        sql.NullInt64
		latestEventAt, latestSemanticAt, latestETA sql.NullString
	}
	readProjection := func() []projectionRow {
		rows, err := database.Query(`SELECT run_id,telemetry_id,sequence,last_heartbeat_at,heartbeat_telemetry_id,
			semantic_telemetry_id,estimate_telemetry_id,latest_event_at,latest_semantic_at,latest_estimate_at
			FROM agent_run_telemetry_latest ORDER BY run_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var result []projectionRow
		for rows.Next() {
			var row projectionRow
			if err := rows.Scan(&row.runID, &row.telemetryID, &row.sequence, &row.lastHeartbeatAt,
				&row.heartbeatID, &row.semanticID, &row.estimateID, &row.latestEventAt,
				&row.latestSemanticAt, &row.latestETA); err != nil {
				t.Fatal(err)
			}
			result = append(result, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	type eventSpec struct {
		sequence         int64
		kind, phase      string
		heartbeat        bool
		activity         string
		estimateRevision *int64
	}
	var runIDs []int64
	estimateRevision := int64(1)
	for issueNumber, events := range [][]eventSpec{
		{
			{sequence: 1, kind: "heartbeat", phase: "starting", heartbeat: true},
			{sequence: 2, kind: "phase", phase: "implementing", activity: "editing"},
			{sequence: 3, kind: "progress", phase: "testing", activity: "testing", estimateRevision: &estimateRevision},
			{sequence: 4, kind: "heartbeat", phase: "testing", heartbeat: true},
		},
		{{sequence: 1, kind: "phase", phase: "planning", activity: "planning"}},
	} {
		issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title) VALUES(?,?,'ticket','Projection')`, projectID, issueNumber+1)
		if err != nil {
			t.Fatal(err)
		}
		issueID, _ := issue.LastInsertId()
		run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status) VALUES(?,?,'running')`, issueID, projectID)
		if err != nil {
			t.Fatal(err)
		}
		runID, _ := run.LastInsertId()
		runIDs = append(runIDs, runID)
		for _, event := range events {
			receivedAt := "2026-08-20T10:00:0" + strconv.FormatInt(event.sequence, 10) + "Z"
			res, err := database.Exec(`INSERT INTO agent_run_telemetry(
				run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,heartbeat,phase,activity,
				estimate_revision,progress_percent,eta_seconds,eta_min_seconds,eta_max_seconds,estimate_source,estimate_confidence,estimate_basis)
				VALUES(?,?,'projection-1','paimos','test',?,?,?, ?,?,?, ?,?,?,?,?,?,?,?)`,
				runID, event.sequence, receivedAt, receivedAt, event.kind, event.heartbeat, event.phase, event.activity,
				event.estimateRevision, nullableEstimateFloat(event.estimateRevision, 50), nullableEstimateInt(event.estimateRevision, 300),
				nullableEstimateInt(event.estimateRevision, 240), nullableEstimateInt(event.estimateRevision, 360),
				nullableEstimateString(event.estimateRevision, "adapter"), nullableEstimateFloat(event.estimateRevision, .8), nullableEstimateString(event.estimateRevision, "half"))
			if err != nil {
				t.Fatal(err)
			}
			eventID, _ := res.LastInsertId()
			var heartbeatAt, heartbeatID, semanticID, semanticAt, estimateID, estimateAt any
			if event.heartbeat {
				heartbeatAt, heartbeatID = receivedAt, eventID
			}
			if event.kind == "phase" || event.kind == "needs_input" || event.kind == "blocker" ||
				event.phase != "unknown" || event.activity != "" {
				semanticID, semanticAt = eventID, receivedAt
			}
			if event.estimateRevision != nil {
				estimateID, estimateAt = eventID, receivedAt
			}
			if _, err := database.Exec(`INSERT INTO agent_run_telemetry_latest(
				run_id,telemetry_id,sequence,last_heartbeat_at,heartbeat_telemetry_id,semantic_telemetry_id,estimate_telemetry_id,
				latest_event_at,latest_semantic_at,latest_estimate_at) VALUES(?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(run_id) DO UPDATE SET telemetry_id=excluded.telemetry_id,sequence=excluded.sequence,
				last_heartbeat_at=COALESCE(excluded.last_heartbeat_at,agent_run_telemetry_latest.last_heartbeat_at),
				heartbeat_telemetry_id=COALESCE(excluded.heartbeat_telemetry_id,agent_run_telemetry_latest.heartbeat_telemetry_id),
				semantic_telemetry_id=COALESCE(excluded.semantic_telemetry_id,agent_run_telemetry_latest.semantic_telemetry_id),
				estimate_telemetry_id=COALESCE(excluded.estimate_telemetry_id,agent_run_telemetry_latest.estimate_telemetry_id),
				latest_event_at=excluded.latest_event_at,
				latest_semantic_at=COALESCE(excluded.latest_semantic_at,agent_run_telemetry_latest.latest_semantic_at),
				latest_estimate_at=COALESCE(excluded.latest_estimate_at,agent_run_telemetry_latest.latest_estimate_at)`,
				runID, eventID, event.sequence, heartbeatAt, heartbeatID, semanticID, estimateID, receivedAt, semanticAt, estimateAt); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := readProjection()
	if _, err := database.Exec(`DELETE FROM agent_run_telemetry_latest WHERE run_id=?`, runIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE agent_run_telemetry_latest SET
		telemetry_id=(SELECT MIN(id) FROM agent_run_telemetry WHERE run_id=?),sequence=1,
		last_heartbeat_at=NULL,heartbeat_telemetry_id=NULL,semantic_telemetry_id=NULL,estimate_telemetry_id=NULL,
		latest_event_at='stale',latest_semantic_at=NULL,latest_estimate_at=NULL WHERE run_id=?`, runIDs[0], runIDs[0]); err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rebuildAgentRunTelemetryLatest(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := readProjection(); !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuilt projection=%+v want incremental=%+v", got, want)
	}
}

func nullableEstimateInt(revision *int64, value int64) any {
	if revision == nil {
		return nil
	}
	return value
}

func nullableEstimateFloat(revision *int64, value float64) any {
	if revision == nil {
		return nil
	}
	return value
}

func nullableEstimateString(revision *int64, value string) any {
	if revision == nil {
		return ""
	}
	return value
}

func TestSchemaAgentRunTelemetryTerminalWriteRace(t *testing.T) {
	database := openTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Telemetry Race','TRC')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title) VALUES(?,1,'ticket','Race')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status) VALUES(?,?,'running')`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	start := make(chan struct{})
	var wg sync.WaitGroup
	var terminalErr, telemetryErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, terminalErr = database.Exec(`UPDATE agent_runs SET status='completed',finished_at=datetime('now') WHERE id=? AND status='running'`, runID)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, telemetryErr = database.Exec(`INSERT INTO agent_run_telemetry(
			run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,heartbeat)
			VALUES(?,1,'race-1','paimos','run-agent','2026-08-20T10:00:00Z','2026-08-20T10:00:00Z','heartbeat',1)`, runID)
	}()
	close(start)
	wg.Wait()
	if terminalErr != nil {
		t.Fatalf("terminal writer: %v", terminalErr)
	}
	if telemetryErr != nil && !strings.Contains(telemetryErr.Error(), "terminal run telemetry") {
		t.Fatalf("telemetry writer: %v", telemetryErr)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	if _, err := database.Exec(`INSERT INTO agent_run_telemetry(
		run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind)
		VALUES(?,2,'race-1','paimos','run-agent','2026-08-20T10:00:01Z','2026-08-20T10:00:01Z','phase')`, runID); err == nil || !strings.Contains(err.Error(), "terminal run telemetry") {
		t.Fatalf("late telemetry error=%v", err)
	}
}

func TestProjectLifecycleTriggersRejectNewIssues(t *testing.T) {
	db := openTestDB(t)
	res, err := db.Exec(`INSERT INTO projects(name, key, status) VALUES('Lifecycle', 'LIFE', 'active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO issues(project_id, issue_number, type, title) VALUES(?, 1, 'ticket', 'active is writable')`, projectID); err != nil {
		t.Fatalf("active project insert: %v", err)
	}

	for number, status := range []string{"frozen", "archived", "deleted"} {
		if _, err := db.Exec(`UPDATE projects SET status=? WHERE id=?`, status, projectID); err != nil {
			t.Fatal(err)
		}
		_, err := db.Exec(`INSERT INTO issues(project_id, issue_number, type, title) VALUES(?, ?, 'ticket', 'blocked')`, projectID, number+2)
		if err == nil || !strings.Contains(err.Error(), "project is "+status+"; new issues are disabled") {
			t.Fatalf("status %s insert error = %v", status, err)
		}
	}
}

func TestSchemaAgentRunsProviderColumns(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{"action_key", "provider_kind", "provider_id", "provider_label", "model", "run_mode"} {
		if !columnExists(t, db, "agent_runs", col) {
			t.Fatalf("expected agent_runs.%s to exist (PAI-629 / M129)", col)
		}
	}
	if !columnExists(t, db, "auto_watch_subscriptions", "actions_json") {
		t.Fatal("expected auto_watch_subscriptions.actions_json to exist (PAI-629 / M129)")
	}
}

func TestSchemaAICallsExecutionOptionColumns(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{"profile_id", "effort", "prompt_preset_ref", "context_pack"} {
		if !columnExists(t, db, "ai_calls", col) {
			t.Fatalf("expected ai_calls.%s to exist (PAI-649 / M130)", col)
		}
	}
}

func TestSchemaAgentRunsDraftProviderColumns(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{
		"profile_id", "effort", "prompt_preset_ref", "context_pack",
		"context_truncated", "context_sources_json", "prompt_tokens",
		"completion_tokens", "finish_reason",
	} {
		if !columnExists(t, db, "agent_runs", col) {
			t.Fatalf("expected agent_runs.%s to exist (PAI-657 / M131)", col)
		}
	}
	if _, err := db.Exec(`INSERT INTO projects(name, key) VALUES('Draft Status Project', 'DSP')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	res, err := db.Exec(`INSERT INTO issues(project_id, issue_number, type, title) VALUES(1, 1, 'ticket', 'Draft status')`)
	if err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	issueID, _ := res.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO agent_runs(issue_id, status, run_mode, action_key, provider_kind, provider_id, provider_label)
		VALUES(?, 'drafted', 'draft', 'openrouter_draft.implement', 'hosted_model', 'openrouter', 'OpenRouter Draft')
	`, issueID); err != nil {
		t.Fatalf("expected drafted to be an allowed agent_runs.status: %v", err)
	}
	if !columnExists(t, db, "ai_settings", "base_url") {
		t.Fatal("expected ai_settings.base_url to exist (PAI-658 / M131)")
	}
}

func TestSchemaAIDefaultsAndDraftHandoffColumns(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{"source_draft_run_id", "followup_run_id"} {
		if !columnExists(t, db, "agent_runs", col) {
			t.Fatalf("expected agent_runs.%s to exist (PAI-665 / M132)", col)
		}
	}
	for _, col := range []string{"ai_defaults_json", "ai_policy_json"} {
		if !columnExists(t, db, "projects", col) {
			t.Fatalf("expected projects.%s to exist (PAI-666 / M132)", col)
		}
	}
}

func TestPerConnectionPragmasDoNotTouchJournalMode(t *testing.T) {
	for _, pragma := range perConnectionPragmas {
		if strings.Contains(strings.ToLower(pragma), "journal_mode") {
			t.Fatalf("per-connection pragma %q touches journal_mode; WAL must be set once during Open", pragma)
		}
	}
}

func TestEnableWALModePersistsFileJournalMode(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "wal-mode.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if err := enableWALMode(db); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode=%q, want wal", mode)
	}
}

func TestSchemaContainsCurrentProjectContextAndAIRelationsTables(t *testing.T) {
	db := openTestDB(t)
	for _, table := range []string{
		"project_repos",
		"issue_anchors",
		// PAI-358: project_manifests dropped in M102 — the manifest
		// editor surface was retired with the v3.0 footer bar redesign.
		"project_context_index",
		"entity_relations",
		"entity_embeddings",
		"ai_prompts",
		"ai_calls",
		"mutation_log",
		"app_settings",
		"project_members",
		"project_agents",
		"project_environments",
		"project_deploy_recipes",
		"project_report_permissions",
		"project_report_snapshots",
		"role_permissions",
		"super_admin_audit",
		"project_issue_counters",
		"issue_key_aliases",
	} {
		if !tableExists(t, db, table) {
			t.Fatalf("expected table %s to exist", table)
		}
	}
	if tableExists(t, db, "user_project_access") {
		t.Fatal("user_project_access should be removed after migration 65")
	}
	if tableExists(t, db, "project_manifests") {
		t.Fatal("project_manifests should be removed after migration 102 (PAI-358)")
	}
	if !columnExists(t, db, "ai_prompts", "placement") {
		t.Fatal("expected ai_prompts.placement to exist")
	}
	if !columnExists(t, db, "mutation_log", "after_state") {
		t.Fatal("expected mutation_log.after_state to exist")
	}
	if !columnExists(t, db, "mutation_log", "redoable") {
		t.Fatal("expected mutation_log.redoable to exist")
	}
	if !columnExists(t, db, "sessions", "csrf_token") {
		t.Fatal("expected sessions.csrf_token to exist")
	}
	if !columnExists(t, db, "sessions", "actor_user_id") {
		t.Fatal("expected sessions.actor_user_id to exist (PAI-389 / M106)")
	}
	if !columnExists(t, db, "sessions", "acting_as_user_id") {
		t.Fatal("expected sessions.acting_as_user_id to exist (PAI-389 / M106)")
	}
	if !columnExists(t, db, "users", "issue_auto_refresh_enabled") {
		t.Fatal("expected users.issue_auto_refresh_enabled to exist")
	}
	if !columnExists(t, db, "users", "issue_auto_refresh_interval_seconds") {
		t.Fatal("expected users.issue_auto_refresh_interval_seconds to exist")
	}
	if !columnExists(t, db, "users", "role_key") {
		t.Fatal("expected users.role_key to exist (PAI-336 / M105)")
	}
	if !columnExists(t, db, "project_cooperation", "report_contract_basis") {
		t.Fatal("expected project_cooperation.report_contract_basis to exist (PAI-407 / M107)")
	}
	if !columnExists(t, db, "project_cooperation", "report_customer_responsibilities") {
		t.Fatal("expected project_cooperation.report_customer_responsibilities to exist (PAI-407 / M107)")
	}
	if !columnExists(t, db, "customers", "tax_id") {
		t.Fatal("expected customers.tax_id to exist (PAI-558 / M114)")
	}
	if !columnExists(t, db, "customers", "company_register_number") {
		t.Fatal("expected customers.company_register_number to exist (PAI-558 / M114)")
	}
	// PAI-324 / M93 — agent + session attribution on history snapshots.
	if !columnExists(t, db, "issue_history", "agent_name") {
		t.Fatal("expected issue_history.agent_name to exist")
	}
	if !columnExists(t, db, "issue_history", "session_id") {
		t.Fatal("expected issue_history.session_id to exist")
	}
	// PAI-329 / M95 — agent rendering shape extensions.
	if !columnExists(t, db, "project_agents", "body") {
		t.Fatal("expected project_agents.body to exist")
	}
	if !columnExists(t, db, "project_agents", "bootstrap_steps") {
		t.Fatal("expected project_agents.bootstrap_steps to exist")
	}
	if !columnExists(t, db, "project_agents", "non_negotiable_rules") {
		t.Fatal("expected project_agents.non_negotiable_rules to exist")
	}
	// PAI-338 / M96 — knowledge-plane columns on issues.
	if !columnExists(t, db, "issues", "slug") {
		t.Fatal("expected issues.slug to exist (PAI-338 / M96)")
	}
	if !columnExists(t, db, "issues", "category_metadata") {
		t.Fatal("expected issues.category_metadata to exist (PAI-338 / M96)")
	}
	// PAI-345 / M99 — user_id column for cross-scope memory.
	if !columnExists(t, db, "issues", "user_id") {
		t.Fatal("expected issues.user_id to exist (PAI-345 / M99)")
	}
	// PAI-347 / M100 — memory reference-count tracking.
	if !columnExists(t, db, "issues", "reference_count") {
		t.Fatal("expected issues.reference_count to exist (PAI-347 / M100)")
	}
	if !columnExists(t, db, "issues", "last_referenced_at") {
		t.Fatal("expected issues.last_referenced_at to exist (PAI-347 / M100)")
	}
	// PAI-577 / M115 — issue-list freshness marker for the conditional-GET ETag.
	if !columnExists(t, db, "issues", "content_rev") {
		t.Fatal("expected issues.content_rev to exist (PAI-577 / M115)")
	}
	// PAI-354 / M101 — agent attribution on mutation_log rows.
	// session_id has lived here since M83; agent_name is the new arrival.
	if !columnExists(t, db, "mutation_log", "agent_name") {
		t.Fatal("expected mutation_log.agent_name to exist (PAI-354 / M101)")
	}
	if !columnExists(t, db, "mutation_log", "session_id") {
		t.Fatal("expected mutation_log.session_id to exist (PAI-354 / M101)")
	}
}

func TestSchemaCreatesDatabaseFileInConfiguredDataDir(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		t.Fatal("db is nil")
	}
	dbPath := filepath.Join(os.Getenv("DATA_DIR"), "paimos.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected sqlite file at %s: %v", dbPath, err)
	}
}

func TestSchemaEnablesForeignKeysAndPassesIntegrityCheck(t *testing.T) {
	db := openTestDB(t)
	enabled, err := ForeignKeysEnabled(db)
	if err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if !enabled {
		t.Fatal("expected PRAGMA foreign_keys=ON")
	}
	ok, err := IntegrityCheckOK(db)
	if err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if !ok {
		t.Fatal("expected integrity_check=ok")
	}
}

func TestSchemaContainsCriticalIndexes(t *testing.T) {
	db := openTestDB(t)
	for _, index := range []string{
		"idx_issues_number",
		"idx_issues_deleted_at",
		"idx_project_members_project",
		"idx_project_repos_project",
		"idx_issue_anchors_issue",
		"idx_entity_relations_project_src",
		"idx_entity_relations_project_tgt",
		"idx_ai_prompts_key_enabled",
		"idx_ai_calls_time",
		"idx_ai_calls_issue_time",
		"idx_mutation_log_user_stack",
		"idx_mutation_log_request",
		"idx_documents_project",
		"idx_time_entries_mite_id",
		// PAI-338 / M96 — slug uniqueness for the knowledge plane.
		"idx_issues_type_slug_project",
		// PAI-345 / M99 — user-scoped knowledge lookups.
		"idx_issues_user_type",
		// PAI-336 / M105 — queryable privileged-action audit feed.
		"idx_super_admin_audit_created_at",
		"idx_super_admin_audit_actor",
		"idx_super_admin_audit_target",
		"idx_super_admin_audit_capability",
		"idx_sessions_actor_user",
		"idx_sessions_acting_as_user",
		"idx_project_report_permissions_project",
		"idx_project_report_snapshots_project",
		"idx_project_report_snapshots_code",
		"idx_issues_project_number_unique",
	} {
		found, err := SchemaHasIndex(db, index)
		if err != nil {
			t.Fatalf("schema_has_index %s: %v", index, err)
		}
		if !found {
			t.Fatalf("expected index %s to exist", index)
		}
	}
}

func TestSchemaSeedsSuperAdminCapabilities(t *testing.T) {
	db := openTestDB(t)
	for _, row := range []struct {
		role       string
		capability string
	}{
		{"admin", "security.super_admin_audit.read"},
		{"super_admin", "security.super_admin_audit.read"},
		{"super_admin", "time_entries.write_any_user"},
		{"super_admin", "users.grant_super_admin"},
		{"super_admin", "auth.impersonation.start"},
		{"super_admin", "auth.impersonation.end"},
		{"super_admin", "auth.impersonation.action"},
	} {
		var found int
		if err := db.QueryRow(
			"SELECT 1 FROM role_permissions WHERE role=? AND capability=?",
			row.role, row.capability,
		).Scan(&found); err != nil {
			t.Fatalf("expected role_permissions row (%s, %s): %v", row.role, row.capability, err)
		}
	}
}

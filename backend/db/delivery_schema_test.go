package db

import (
	"database/sql"
	"strings"
	"testing"
)

func seedDeliverySchemaGraph(t *testing.T, database *sql.DB) (deliveryID, attemptID, reporterID, startID int64) {
	t.Helper()
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Delivery schema','DS')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,title) VALUES(?,1,'Schema root')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	result, err := database.Exec(`INSERT INTO deliveries(issue_id,delivery_key,project_id_hint,created_at,updated_at)
		VALUES(?, ?, ?, '2026-08-20T10:00:00Z','2026-08-20T10:00:00Z')`, issueID, "issue:"+itoa(issueID), projectID)
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, _ = result.LastInsertId()
	result, err = database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
		VALUES(?,'system','test','2026-08-20T10:00:00Z')`, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	reporterID, _ = result.LastInsertId()
	event := func(revision int, key, kind string) int64 {
		t.Helper()
		res, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
			kind,reporter_id,server_received_at) VALUES(?,?,?,zeroblob(32),?,?, '2026-08-20T10:00:00Z')`,
			deliveryID, revision, key, kind, reporterID)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	attemptEvent := event(1, "attempt", "attempt_started")
	result, err = database.Exec(`INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,start_delivery_event_id,
		project_id_at_start,reason_code,created_at) VALUES(?,1,1,?,?,'test','2026-08-20T10:00:00Z')`,
		deliveryID, attemptEvent, projectID)
	if err != nil {
		t.Fatal(err)
	}
	attemptID, _ = result.LastInsertId()
	stages := []string{"specification", "implementation", "qa", "deployment", "verification"}
	weights := []int{10, 45, 20, 15, 10}
	for i := range stages {
		if _, err := database.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,sort_order,
			applicability,weight,created_at) VALUES(?,?,?,?,'required',?,'2026-08-20T10:00:00Z')`,
			deliveryID, attemptID, stages[i], i+1, weights[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO delivery_attempt_policy_seals(delivery_id,attempt_id,sealed_at)
		VALUES(?,?,'2026-08-20T10:00:00Z')`, deliveryID, attemptID); err != nil {
		t.Fatal(err)
	}
	startEvent := event(2, "start", "stage_execution_started")
	result, err = database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,semantic_state,
		authority_source_sequence_cutoff,server_received_at)
		VALUES(?,?,'specification',1,1,1,?,'execution_started',?,'active',0,'2026-08-20T10:00:00Z')`,
		deliveryID, attemptID, startEvent, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	startID, _ = result.LastInsertId()
	return
}

func itoa(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var raw [20]byte
	i := len(raw)
	for v > 0 {
		i--
		raw[i] = digits[v%10]
		v /= 10
	}
	return string(raw[i:])
}

func TestM144TypedStageUnionRejectsCrossKindSmuggling(t *testing.T) {
	database := openTestDB(t)
	deliveryID, attemptID, reporterID, startID := seedDeliverySchemaGraph(t, database)
	revision := 2
	newEnvelope := func(key string) int64 {
		t.Helper()
		revision++
		res, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
			kind,reporter_id,server_received_at) VALUES(?,?,?,zeroblob(32),'stage_reported',?,'2026-08-20T10:01:00Z')`,
			deliveryID, revision, key, reporterID)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	cases := []struct {
		name, columns, values string
		args                  []any
	}{
		{"heartbeat estimate", "heartbeat,estimate_revision,estimate_source", "1,1,'agent'", nil},
		{"heartbeat semantic children", "heartbeat,declared_blocker_count,current_blocker_count", "1,1,1", nil},
		{"estimate heartbeat", "heartbeat,estimate_revision,estimate_source", "1,1,'agent'", nil},
		{"semantic estimate", "semantic_state,estimate_revision,estimate_source,ended_at", "'succeeded',1,'agent','2026-08-20T10:01:00Z'", nil},
		{"lifecycle source sequence", "semantic_state,source_sequence,ended_at", "'failed',1,'2026-08-20T10:01:00Z'", nil},
		{"lifecycle blockers", "semantic_state,declared_blocker_count,current_blocker_count,ended_at", "'failed',1,1,'2026-08-20T10:01:00Z'", nil},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			typeName := "heartbeat"
			if strings.HasPrefix(tc.name, "estimate") {
				typeName = "estimate"
			}
			if strings.HasPrefix(tc.name, "semantic") {
				typeName = "semantic_report"
			}
			if strings.HasPrefix(tc.name, "lifecycle") {
				typeName = "lifecycle_normalized"
			}
			eventID := newEnvelope("smuggle-" + itoa(int64(i)))
			query := `INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,event_sequence,
				authority_epoch,delivery_event_id,event_type,reporter_id,execution_start_stage_event_id,server_received_at,` + tc.columns + `)
				VALUES(?,?,'specification',1,?,1,?,?,? ,?,'2026-08-20T10:01:00Z',` + tc.values + `)`
			_, err := database.Exec(query, deliveryID, attemptID, i+2, eventID, typeName, reporterID, startID)
			if err == nil {
				t.Fatal("cross-kind insert unexpectedly succeeded")
			}
		})
	}

	t.Run("required typed fields cannot exploit NULL checks", func(t *testing.T) {
		newKindEnvelope := func(key, kind string) int64 {
			t.Helper()
			revision++
			result, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
				payload_hash,kind,reporter_id,server_received_at)
				VALUES(?,?,?,zeroblob(32),?,?,'2026-08-20T10:02:00Z')`, deliveryID, revision, key, kind, reporterID)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := result.LastInsertId()
			return id
		}
		for _, tc := range []struct {
			name          string
			semanticState any
			cutoff        any
		}{
			{name: "execution semantic state", semanticState: nil, cutoff: int64(0)},
			{name: "execution authority cutoff", semanticState: "active", cutoff: nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				eventID := newKindEnvelope("null-"+strings.ReplaceAll(tc.name, " ", "-"), "stage_execution_started")
				if _, err := database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,
					execution_number,event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,
					semantic_state,authority_source_sequence_cutoff,server_received_at)
					VALUES(?,?,'implementation',1,1,1,?,'execution_started',?,?,?,'2026-08-20T10:02:00Z')`,
					deliveryID, attemptID, eventID, reporterID, tc.semanticState, tc.cutoff); err == nil {
					t.Fatalf("execution_started accepted NULL %s", tc.name)
				}
			})
		}
		if _, err := database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,
			execution_number,event_sequence,authority_epoch,event_type,reporter_id,execution_start_stage_event_id,
			source_idempotency_key,source_payload_hash,heartbeat,server_received_at)
			VALUES(?,?,'specification',1,99,1,'heartbeat',?,?,'null-heartbeat-sequence',zeroblob(32),1,
			'2026-08-20T10:02:00Z')`, deliveryID, attemptID, reporterID, startID); err == nil {
			t.Fatal("heartbeat accepted a NULL source sequence")
		}
	})
}

func TestM144DeliveryIndexesAndForeignKeys(t *testing.T) {
	database := openTestDB(t)
	want := []string{"idx_agent_run_telemetry_estimate", "idx_agent_runs_delivery_legacy_active", "idx_delivery_stage_duration_history",
		"idx_delivery_attempts_current", "idx_delivery_stage_latest_delivery", "idx_delivery_agent_run_execution",
		"idx_delivery_stage_events_history"}
	for _, index := range want {
		var found string
		if err := database.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&found); err != nil {
			t.Fatalf("missing index %s: %v", index, err)
		}
	}
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check returned a violation")
	}
	planCases := []struct {
		query, index string
	}{
		{`EXPLAIN QUERY PLAN SELECT estimate_revision FROM agent_run_telemetry WHERE run_id=1 AND estimate_revision IS NOT NULL ORDER BY estimate_revision DESC,sequence DESC LIMIT 1`, "idx_agent_run_telemetry_estimate"},
		{`EXPLAIN QUERY PLAN SELECT id FROM agent_runs WHERE project_id=1 AND delivery_instrumentation_version=0 AND status IN ('queued','running') ORDER BY id DESC`, "idx_agent_runs_delivery_legacy_active"},
		{`EXPLAIN QUERY PLAN SELECT stage_execution_id FROM delivery_stage_durations WHERE project_id_at_completion=1 AND stage_key='qa' AND estimator_policy_version=1 ORDER BY completed_at DESC,stage_execution_id DESC`, "idx_delivery_stage_duration_history"},
		{`EXPLAIN QUERY PLAN SELECT id FROM delivery_attempts WHERE delivery_id=1 ORDER BY attempt_number DESC LIMIT 1`, "idx_delivery_attempts_current"},
		{`EXPLAIN QUERY PLAN SELECT stage_key FROM delivery_stage_latest WHERE delivery_id=1 AND attempt_id=1 ORDER BY stage_key`, "idx_delivery_stage_latest_delivery"},
		{`EXPLAIN QUERY PLAN SELECT agent_run_id FROM delivery_agent_run_links WHERE attempt_id=1 AND stage_key='implementation' AND execution_number=1`, "sqlite_autoindex_delivery_agent_run_links_2"},
		{`EXPLAIN QUERY PLAN SELECT id FROM delivery_stage_events WHERE attempt_id=1 AND stage_key='implementation' AND execution_number=1 ORDER BY event_sequence DESC LIMIT 1`, "idx_delivery_stage_events_history"},
	}
	for _, tc := range planCases {
		planRows, err := database.Query(tc.query)
		if err != nil {
			t.Fatal(err)
		}
		var detail strings.Builder
		for planRows.Next() {
			var id, parent, unused int
			var line string
			if err := planRows.Scan(&id, &parent, &unused, &line); err != nil {
				planRows.Close()
				t.Fatal(err)
			}
			detail.WriteString(line)
		}
		planRows.Close()
		if !strings.Contains(detail.String(), tc.index) {
			t.Fatalf("query plan %q does not use %s", detail.String(), tc.index)
		}
	}
}

func TestM144LatestAndDeclaredChildGuardsRejectDirectCorruption(t *testing.T) {
	database := openTestDB(t)
	deliveryID, attemptID, reporterID, startID := seedDeliverySchemaGraph(t, database)
	if _, err := database.Exec(`INSERT INTO delivery_stage_latest(delivery_id,attempt_id,stage_key,execution_number,
		authority_epoch,current_reporter_id,execution_start_stage_event_id,authority_stage_event_id,updated_at)
		VALUES(?,?,'specification',1,1,?,?,?,'2026-08-20T10:00:00Z')`, deliveryID, attemptID, reporterID, startID, startID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE delivery_stage_latest SET semantic_stage_event_id=? WHERE attempt_id=? AND stage_key='specification'`, startID, attemptID); err == nil {
		t.Fatal("latest accepted an execution-start event as a semantic pointer")
	}
	other, err := database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
		VALUES(?,'external','other-owner','2026-08-20T10:00:00Z')`, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	otherReporterID, _ := other.LastInsertId()
	if _, err := database.Exec(`UPDATE delivery_stage_latest SET current_reporter_id=?
		WHERE attempt_id=? AND stage_key='specification'`, otherReporterID, attemptID); err == nil {
		t.Fatal("latest allowed reporter replacement without a new authority epoch")
	}
	if _, err := database.Exec(`INSERT INTO delivery_stage_blockers(delivery_id,stage_event_id,ordinal,blocker_key,
		blocker_class,is_current,is_human_wait,interval_started_at,interval_ended_at)
		VALUES(?,?,0,'extra','unknown',1,0,'2026-08-20T10:00:00Z','2026-08-20T10:00:00Z')`, deliveryID, startID); err == nil {
		t.Fatal("blocker exceeded the event's declared count")
	}
	if _, err := database.Exec(`INSERT INTO delivery_evidence(delivery_id,root_issue_id,stage_event_id,ordinal,
		evidence_type,outcome,reference_kind,created_at)
		SELECT ?,issue_id,?,0,'approval','passed','none','2026-08-20T10:00:00Z' FROM deliveries WHERE id=?`,
		deliveryID, startID, deliveryID); err == nil {
		t.Fatal("evidence exceeded the event's declared count")
	}
}

func TestM144SecretAndStableIdentityGuardsRejectDirectCorruption(t *testing.T) {
	database := openTestDB(t)
	deliveryID, attemptID, reporterID, startID := seedDeliverySchemaGraph(t, database)
	for _, forbidden := range []string{
		"api_key=abcdefghijklmnop", "token:abcdefghijklmnop", "secret/abcdefghijklmnop",
		"password_abcdefghijklmnop", "credential-abcdefghijklmnop",
		"ghp_123456789012345678901234567890", "gho_123456789012345678901234567890",
		"ghu_123456789012345678901234567890", "ghs_123456789012345678901234567890",
		"ghr_123456789012345678901234567890", "github_pat_123456789012345678901234567890",
		"xoxb-12345678901234567890", "xoxa-12345678901234567890", "xoxp-12345678901234567890",
		"xoxr-12345678901234567890", "xoxs-12345678901234567890",
		"nul\x00value", "line\nvalue", "carriage\rvalue",
		"https://user@example.com/proof", "https://example.com/proof?signature=redacted",
	} {
		if _, err := database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
			VALUES(?,'external',?,'2026-08-20T10:01:00Z')`, deliveryID, forbidden); err == nil {
			t.Fatalf("secret/control/URL reporter value was accepted by direct SQL: %q", forbidden)
		}
	}
	if _, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
		kind,reporter_id,server_received_at) VALUES(?,3,'sk-live-abcdefghijklmnop',zeroblob(32),
		'stage_reported',?,'2026-08-20T10:01:00Z')`, deliveryID, reporterID); err == nil {
		t.Fatal("secret-like event idempotency key was accepted by direct SQL")
	}
	if _, err := database.Exec(`UPDATE deliveries SET issue_id=issue_id+1 WHERE id=?`, deliveryID); err == nil {
		t.Fatal("delivery issue identity was mutable")
	}
	if _, err := database.Exec(`UPDATE deliveries SET delivery_key=delivery_key||':rewrite' WHERE id=?`, deliveryID); err == nil {
		t.Fatal("delivery key identity was mutable")
	}
	var issueID, projectID int64
	if err := database.QueryRow(`SELECT issue_id,project_id_hint FROM deliveries WHERE id=?`, deliveryID).Scan(&issueID, &projectID); err != nil {
		t.Fatal(err)
	}
	legacy, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status,delivery_instrumentation_version)
		VALUES(?,?,'failed',0)`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	legacyID, _ := legacy.LastInsertId()
	if _, err := database.Exec(`UPDATE agent_runs SET delivery_instrumentation_version=1 WHERE id=?`, legacyID); err == nil {
		t.Fatal("legacy run instrumentation marker was mutable")
	}
	instrumented, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status,delivery_instrumentation_version)
		VALUES(?,?,'failed',1)`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	instrumentedID, _ := instrumented.LastInsertId()
	if _, err := database.Exec(`UPDATE agent_runs SET delivery_instrumentation_version=0 WHERE id=?`, instrumentedID); err == nil {
		t.Fatal("instrumented run marker was mutable")
	}
	event, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
		kind,reporter_id,server_received_at) VALUES(?,3,'safe-semantic',zeroblob(32),
		'stage_reported',?,'2026-08-20T10:01:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	eventID, _ := event.LastInsertId()
	semantic, err := database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,execution_start_stage_event_id,
		semantic_state,declared_evidence_count,spec_revision,server_received_at,ended_at)
		VALUES(?,?,'specification',1,2,1,?,'semantic_report',?,?,'succeeded',1,1,
		'2026-08-20T10:01:00Z','2026-08-20T10:01:00Z')`, deliveryID, attemptID, eventID, reporterID, startID)
	if err != nil {
		t.Fatal(err)
	}
	semanticID, _ := semantic.LastInsertId()
	otherIssue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,title) VALUES(?,2,'Other root')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	otherIssueID, _ := otherIssue.LastInsertId()
	attachment, err := database.Exec(`INSERT INTO attachments(issue_id,object_key,filename,content_type,size_bytes)
		VALUES(?,'other/object','proof.txt','text/plain',1)`, otherIssueID)
	if err != nil {
		t.Fatal(err)
	}
	attachmentID, _ := attachment.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_evidence(delivery_id,root_issue_id,stage_event_id,ordinal,
		evidence_type,outcome,reference_kind,attachment_id,created_at)
		VALUES(?,?,?,0,'spec_acceptance','passed','attachment',?,'2026-08-20T10:01:00Z')`,
		deliveryID, otherIssueID, semanticID, attachmentID); err == nil {
		t.Fatal("cross-root attachment evidence was accepted")
	}
	if _, err := database.Exec(`INSERT INTO delivery_evidence(delivery_id,root_issue_id,stage_event_id,ordinal,
		evidence_type,outcome,reference_kind,reference_value,created_at)
		SELECT ?,issue_id,?,0,'spec_acceptance','passed','external_ref','xoxb-12345678901234567890',
		'2026-08-20T10:01:00Z' FROM deliveries WHERE id=?`, deliveryID, semanticID, deliveryID); err == nil {
		t.Fatal("secret-like evidence reference was accepted by direct SQL")
	}
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check returned a violation after rejected corruption")
	}
}

func TestM144PrivacyBackstopCoversEveryPersistedExternalSurface(t *testing.T) {
	database := openTestDB(t)
	deliveryID, attemptID, reporterID, startID := seedDeliverySchemaGraph(t, database)
	var patternCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_forbidden_value_patterns`).Scan(&patternCount); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_forbidden_value_patterns(pattern) VALUES('*')`); err == nil {
		t.Fatal("post-migration insert could poison the forbidden-value rules")
	}
	if _, err := database.Exec(`UPDATE delivery_forbidden_value_patterns SET pattern='*' WHERE pattern LIKE '*token%'`); err == nil {
		t.Fatal("forbidden-value rules were mutable")
	}
	if _, err := database.Exec(`DELETE FROM delivery_forbidden_value_patterns WHERE pattern LIKE '*token%'`); err == nil {
		t.Fatal("forbidden-value rules were deletable")
	}
	if _, err := database.Exec(`INSERT OR IGNORE INTO delivery_forbidden_value_patterns(
		pattern,normalize_horizontal_whitespace,case_sensitive,boundary_needle,require_bearer_whitespace)
		SELECT pattern,normalize_horizontal_whitespace,case_sensitive,boundary_needle,require_bearer_whitespace
		FROM delivery_forbidden_value_patterns ORDER BY pattern LIMIT 1`); err != nil {
		t.Fatalf("idempotent migration seed replay failed: %v", err)
	}
	var replayPatternCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_forbidden_value_patterns`).Scan(&replayPatternCount); err != nil || replayPatternCount != patternCount {
		t.Fatalf("rule seed replay count=%d/%d err=%v", replayPatternCount, patternCount, err)
	}
	assertForbidden := func(surface string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "forbidden delivery "+surface+" value") {
			t.Fatalf("%s surface did not reject via its privacy backstop: %v", surface, err)
		}
	}
	// Exercise the original bounce directly on every persisted surface. The
	// table-driven Store/DB corpus separately covers tabs, form-feed, Unicode
	// spacing, slash/underscore/dash separators, and exact token thresholds.
	const forbidden = "ToKeN \t= abcdefghijklmnop"
	_, err := database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
		VALUES(?,'external',?,'2026-08-20T10:02:00Z')`, deliveryID, forbidden)
	assertForbidden("reporter", err)

	result, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
		kind,reporter_id,server_received_at) VALUES(?,3,'privacy-attempt',zeroblob(32),
		'attempt_started',?,'2026-08-20T10:02:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	attemptEventID, _ := result.LastInsertId()
	_, err = database.Exec(`INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,previous_attempt_id,
		start_delivery_event_id,reason_code,reason_text,created_at) VALUES(?,2,2,?,?,'retry',?,'2026-08-20T10:02:00Z')`,
		deliveryID, attemptID, attemptEventID, forbidden)
	assertForbidden("attempt", err)

	_, err = database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
		kind,reporter_id,reason_text,server_received_at) VALUES(?,4,'privacy-event',zeroblob(32),
		'stage_reported',?,?,'2026-08-20T10:02:00Z')`, deliveryID, reporterID, forbidden)
	assertForbidden("event", err)
	_, err = database.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,sort_order,
		applicability,weight,policy_reference,created_at) VALUES(?,?,'specification',1,'required',10,?,
		'2026-08-20T10:02:00Z')`, deliveryID, attemptID, forbidden)
	assertForbidden("policy", err)

	result, err = database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
		kind,reporter_id,server_received_at) VALUES(?,4,'privacy-stage-envelope',zeroblob(32),
		'stage_reported',?,'2026-08-20T10:02:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	stageEnvelopeID, _ := result.LastInsertId()
	_, err = database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,execution_start_stage_event_id,
		semantic_state,activity,spec_revision,server_received_at)
		VALUES(?,?,'specification',1,2,1,?,'semantic_report',?,?,'active',?,1,'2026-08-20T10:02:00Z')`,
		deliveryID, attemptID, stageEnvelopeID, reporterID, startID, forbidden)
	assertForbidden("stage", err)

	result, err = database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
		kind,reporter_id,server_received_at) VALUES(?,5,'privacy-children-envelope',zeroblob(32),
		'stage_reported',?,'2026-08-20T10:02:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	childrenEnvelopeID, _ := result.LastInsertId()
	result, err = database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,execution_start_stage_event_id,
		semantic_state,activity,declared_blocker_count,current_blocker_count,declared_evidence_count,spec_revision,
		server_received_at) VALUES(?,?,'specification',1,2,1,?,'semantic_report',?,?,'waiting','Waiting',1,1,1,1,
		'2026-08-20T10:02:00Z')`, deliveryID, attemptID, childrenEnvelopeID, reporterID, startID)
	if err != nil {
		t.Fatal(err)
	}
	childrenStageID, _ := result.LastInsertId()
	_, err = database.Exec(`INSERT INTO delivery_stage_blockers(delivery_id,stage_event_id,ordinal,blocker_key,
		blocker_class,summary,is_current,is_human_wait,interval_started_at,interval_ended_at)
		VALUES(?,?,0,'privacy-blocker','external',?,1,0,'2026-08-20T10:02:00Z','2026-08-20T10:02:00Z')`,
		deliveryID, childrenStageID, forbidden)
	assertForbidden("blocker", err)
	_, err = database.Exec(`INSERT INTO delivery_evidence(delivery_id,root_issue_id,stage_event_id,ordinal,
		evidence_type,outcome,reference_kind,reference_value,created_at)
		SELECT ?,issue_id,?,0,'spec_acceptance','unknown','external_ref',?,'2026-08-20T10:02:00Z'
		FROM deliveries WHERE id=?`, deliveryID, childrenStageID, forbidden, deliveryID)
	assertForbidden("evidence", err)

	for _, unsafeReference := range []string{
		"https://user@example.com/proof", "https://example.com/proof?signature=redacted",
	} {
		_, err = database.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,sort_order,
			applicability,weight,policy_reference,created_at) VALUES(?,?,'specification',1,'required',10,?,
			'2026-08-20T10:02:00Z')`, deliveryID, attemptID, unsafeReference)
		assertForbidden("policy", err)
		_, err = database.Exec(`INSERT INTO delivery_evidence(delivery_id,root_issue_id,stage_event_id,ordinal,
			evidence_type,outcome,reference_kind,reference_value,created_at)
			SELECT ?,issue_id,?,0,'spec_acceptance','unknown','external_ref',?,'2026-08-20T10:02:00Z'
			FROM deliveries WHERE id=?`, deliveryID, childrenStageID, unsafeReference, deliveryID)
		assertForbidden("evidence", err)
	}
}

func TestM144ExternalTextBoundsUseUTF8BytesAndStoreSyntax(t *testing.T) {
	database := openTestDB(t)
	deliveryID, _, reporterID, _ := seedDeliverySchemaGraph(t, database)
	checks := map[string][]string{
		"deliveries":                    {"length(CAST(delivery_key AS BLOB)) BETWEEN 7 AND 80"},
		"delivery_reporters":            {"length(CAST(opaque_key AS BLOB)) BETWEEN 1 AND 128"},
		"delivery_events":               {"length(CAST(idempotency_key AS BLOB)) BETWEEN 1 AND 128", "length(CAST(reason_code AS BLOB)) <= 64", "length(CAST(reason_text AS BLOB)) <= 280"},
		"delivery_attempts":             {"length(CAST(reason_code AS BLOB)) BETWEEN 1 AND 64", "length(CAST(reason_text AS BLOB)) <= 280"},
		"delivery_attempt_stage_policy": {"length(CAST(policy_reference AS BLOB)) <= 160", "length(CAST(reason_code AS BLOB)) <= 64", "length(CAST(reason_text AS BLOB)) <= 280"},
		"delivery_stage_events":         {"length(CAST(source_idempotency_key AS BLOB)) BETWEEN 1 AND 128", "length(CAST(activity AS BLOB)) <= 280", "length(CAST(estimate_basis AS BLOB)) <= 240", "length(CAST(reason_text AS BLOB)) <= 280"},
		"delivery_stage_blockers":       {"length(CAST(blocker_key AS BLOB)) BETWEEN 1 AND 96", "length(CAST(summary AS BLOB)) <= 280"},
		"delivery_evidence":             {"length(CAST(reference_value AS BLOB)) <= 192"},
	}
	for table, fragments := range checks {
		var ddl string
		if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(ddl, fragment) {
				t.Fatalf("%s lacks byte bound %q", table, fragment)
			}
		}
	}

	insertEvent := func(revision int, key, text string) error {
		_, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
			payload_hash,kind,reporter_id,reason_text,server_received_at)
			VALUES(?,?,?,zeroblob(32),'stage_reported',?,?,'2026-08-20T10:03:00Z')`,
			deliveryID, revision, key, reporterID, text)
		return err
	}
	if err := insertEvent(3, "ascii-max", strings.Repeat("a", 280)); err != nil {
		t.Fatalf("280-byte ASCII text rejected: %v", err)
	}
	if err := insertEvent(4, "ascii-over", strings.Repeat("a", 281)); err == nil {
		t.Fatal("281-byte ASCII text accepted")
	}
	if err := insertEvent(4, "utf8-max", strings.Repeat("é", 140)); err != nil {
		t.Fatalf("280-byte UTF-8 text rejected: %v", err)
	}
	if err := insertEvent(5, "utf8-over", strings.Repeat("é", 141)); err == nil {
		t.Fatal("282-byte UTF-8 text accepted")
	}
	if err := insertEvent(5, "safe-question", "Why is this bounded note safe?"); err != nil {
		t.Fatalf("ordinary bounded free text was over-rejected: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
		VALUES(?,'external',?,'2026-08-20T10:03:00Z')`, deliveryID, strings.Repeat("a", 128)); err != nil {
		t.Fatalf("128-byte opaque key rejected: %v", err)
	}
	for _, invalid := range []string{strings.Repeat("a", 129), "has space", "emoji🙂", "_bad-first"} {
		if _, err := database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
			VALUES(?,'external',?,'2026-08-20T10:03:00Z')`, deliveryID, invalid); err == nil {
			t.Fatalf("invalid Store-incompatible opaque key accepted: %q", invalid)
		}
	}
}

func TestM144AttemptPolicySealRejectsPartialAndNonCanonicalPoison(t *testing.T) {
	database := openTestDB(t)
	deliveryID, _, reporterID, _ := seedDeliverySchemaGraph(t, database)
	revision, attemptNumber := 3, int64(2)
	newAttempt := func() int64 {
		t.Helper()
		result, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
			payload_hash,kind,reporter_id,server_received_at)
			VALUES(?,?,?,zeroblob(32),'attempt_started',?,'2026-08-20T10:04:00Z')`,
			deliveryID, revision, "policy-attempt:"+itoa(attemptNumber), reporterID)
		if err != nil {
			t.Fatal(err)
		}
		eventID, _ := result.LastInsertId()
		result, err = database.Exec(`INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,
			start_delivery_event_id,reason_code,created_at) VALUES(?,?,?,?, 'test','2026-08-20T10:04:00Z')`,
			deliveryID, attemptNumber, attemptNumber, eventID)
		if err != nil {
			t.Fatal(err)
		}
		attemptID, _ := result.LastInsertId()
		revision++
		attemptNumber++
		return attemptID
	}
	insertPolicy := func(attemptID int64, stage string, order, weight int, applicability string) error {
		policyRef, reasonCode, reasonText := "", "", ""
		var authorized any
		if applicability == "not_applicable" {
			policyRef, reasonCode, reasonText, authorized = "policy:waiver", "waived", "Explicit policy waiver", reporterID
		}
		_, err := database.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,
			sort_order,applicability,weight,policy_reference,reason_code,reason_text,authorized_by_reporter_id,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?, '2026-08-20T10:04:00Z')`, deliveryID, attemptID, stage, order,
			applicability, weight, policyRef, reasonCode, reasonText, authorized)
		return err
	}
	seal := func(attemptID int64) error {
		_, err := database.Exec(`INSERT INTO delivery_attempt_policy_seals(delivery_id,attempt_id,sealed_at)
			VALUES(?,?,'2026-08-20T10:04:00Z')`, deliveryID, attemptID)
		return err
	}

	empty := newAttempt()
	if err := seal(empty); err == nil {
		t.Fatal("zero-policy attempt was sealed")
	}
	partial := newAttempt()
	for i, stage := range []string{"specification", "implementation", "qa", "deployment"} {
		if err := insertPolicy(partial, stage, i+1, []int{10, 45, 20, 15}[i], "required"); err != nil {
			t.Fatal(err)
		}
	}
	if err := seal(partial); err == nil {
		t.Fatal("four-policy attempt was sealed")
	}
	misordered := newAttempt()
	if err := insertPolicy(misordered, "specification", 2, 100, "required"); err == nil {
		t.Fatal("misordered canonical stage was accepted")
	}
	wrongTotal := newAttempt()
	for i, stage := range []string{"specification", "implementation", "qa", "deployment", "verification"} {
		if err := insertPolicy(wrongTotal, stage, i+1, []int{10, 44, 20, 15, 10}[i], "required"); err != nil {
			t.Fatal(err)
		}
	}
	if err := seal(wrongTotal); err == nil {
		t.Fatal("99-weight policy was sealed")
	}
	allNA := newAttempt()
	for i, stage := range []string{"specification", "implementation", "qa", "deployment", "verification"} {
		if err := insertPolicy(allNA, stage, i+1, []int{10, 45, 20, 15, 10}[i], "not_applicable"); err != nil {
			t.Fatal(err)
		}
	}
	if err := seal(allNA); err == nil {
		t.Fatal("all-N/A policy was sealed")
	}
	valid := newAttempt()
	for i, stage := range []string{"specification", "implementation", "qa", "deployment", "verification"} {
		if err := insertPolicy(valid, stage, i+1, []int{5, 50, 20, 15, 10}[i], "required"); err != nil {
			t.Fatal(err)
		}
	}
	if err := seal(valid); err != nil {
		t.Fatalf("valid custom 100-weight policy did not seal: %v", err)
	}
}

func TestM144InapplicableExecutionAndNeedsInputWitnessGuards(t *testing.T) {
	database := openTestDB(t)
	deliveryID, attemptID, reporterID, startID := seedDeliverySchemaGraph(t, database)
	var projectID int64
	if err := database.QueryRow(`SELECT project_id_hint FROM deliveries WHERE id=?`, deliveryID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	result, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,3,'na-attempt',zeroblob(32),'attempt_started',?,'2026-08-20T10:04:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	attemptEventID, _ := result.LastInsertId()
	result, err = database.Exec(`INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,
		previous_attempt_id,start_delivery_event_id,project_id_at_start,reason_code,created_at)
		VALUES(?,2,2,?,?,?,'policy_change','2026-08-20T10:04:00Z')`, deliveryID, attemptID, attemptEventID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	naAttemptID, _ := result.LastInsertId()
	stages := []string{"specification", "implementation", "qa", "deployment", "verification"}
	weights := []int{10, 45, 20, 15, 10}
	for i, stage := range stages {
		applicability, policyRef, reasonCode, reasonText := "required", "", "", ""
		var authorized any
		if stage == "qa" {
			applicability, policyRef, reasonCode, reasonText, authorized = "not_applicable", "policy:qa-waiver", "waived", "QA covered elsewhere", reporterID
		}
		if _, err := database.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,
			sort_order,applicability,weight,policy_reference,reason_code,reason_text,authorized_by_reporter_id,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?, '2026-08-20T10:04:00Z')`, deliveryID, naAttemptID, stage, i+1,
			applicability, weights[i], policyRef, reasonCode, reasonText, authorized); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO delivery_attempt_policy_seals(delivery_id,attempt_id,sealed_at)
		VALUES(?,?,'2026-08-20T10:04:00Z')`, deliveryID, naAttemptID); err != nil {
		t.Fatal(err)
	}
	result, err = database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,4,'na-execution',zeroblob(32),'stage_execution_started',?,'2026-08-20T10:04:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	naExecutionEventID, _ := result.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,
		execution_number,event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,semantic_state,
		authority_source_sequence_cutoff,server_received_at)
		VALUES(?,?,'qa',1,1,1,?,'execution_started',?,'active',0,'2026-08-20T10:04:00Z')`,
		deliveryID, naAttemptID, naExecutionEventID, reporterID); err == nil {
		t.Fatal("execution_started was accepted for a sealed not_applicable stage")
	}

	if _, err := database.Exec(`INSERT INTO delivery_stage_latest(delivery_id,attempt_id,stage_key,
		execution_number,authority_epoch,current_reporter_id,execution_start_stage_event_id,
		authority_stage_event_id,updated_at)
		VALUES(?,?,'specification',1,1,?,?,?,'2026-08-20T10:00:00Z')`,
		deliveryID, attemptID, reporterID, startID, startID); err != nil {
		t.Fatal(err)
	}
	newSemantic := func(revision int, key string, sequence, blockerCount int) int64 {
		t.Helper()
		result, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
			payload_hash,kind,reporter_id,server_received_at)
			VALUES(?,?,?,zeroblob(32),'stage_reported',?,'2026-08-20T10:05:00Z')`, deliveryID, revision, key, reporterID)
		if err != nil {
			t.Fatal(err)
		}
		envelopeID, _ := result.LastInsertId()
		result, err = database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,
			execution_number,event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,
			execution_start_stage_event_id,semantic_state,needs_input,declared_blocker_count,current_blocker_count,
			spec_revision,server_received_at)
			VALUES(?,?,'specification',1,?,1,?,'semantic_report',?,?,'waiting',1,?,?,1,'2026-08-20T10:05:00Z')`,
			deliveryID, attemptID, sequence, envelopeID, reporterID, startID, blockerCount, blockerCount)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	missingWitnessID := newSemantic(5, "missing-human-witness", 2, 0)
	if _, err := database.Exec(`UPDATE delivery_stage_latest SET semantic_stage_event_id=?,updated_at='2026-08-20T10:05:00Z'
		WHERE delivery_id=? AND attempt_id=? AND stage_key='specification'`, missingWitnessID, deliveryID, attemptID); err == nil {
		t.Fatal("needs_input semantic fact became authoritative without a current human_wait blocker")
	}
	validWitnessID := newSemantic(6, "valid-human-witness", 3, 1)
	if _, err := database.Exec(`INSERT INTO delivery_stage_blockers(delivery_id,stage_event_id,ordinal,
		blocker_key,blocker_class,summary,is_current,is_human_wait,interval_started_at,interval_ended_at)
		VALUES(?,?,0,'approval','input','Awaiting reviewer',1,1,'2026-08-20T10:05:00Z','2026-08-20T10:05:00Z')`,
		deliveryID, validWitnessID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE delivery_stage_latest SET semantic_stage_event_id=?,updated_at='2026-08-20T10:05:00Z'
		WHERE delivery_id=? AND attempt_id=? AND stage_key='specification'`, validWitnessID, deliveryID, attemptID); err != nil {
		t.Fatalf("valid needs_input semantic fact with human_wait witness was rejected: %v", err)
	}
}

func TestM144EnvelopeRetentionProvenanceAndCascadeGuards(t *testing.T) {
	database := openTestDB(t)
	deliveryID, attemptID, reporterID, startID := seedDeliverySchemaGraph(t, database)
	var issueID, projectID int64
	var deliveryKey string
	if err := database.QueryRow(`SELECT d.issue_id,i.project_id,d.delivery_key FROM deliveries d
		JOIN issues i ON i.id=d.issue_id WHERE d.id=?`, deliveryID).Scan(&issueID, &projectID, &deliveryKey); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,3,'null-reporter',zeroblob(32),'stage_reported',NULL,'2026-08-20T10:05:00Z')`, deliveryID); err == nil {
		t.Fatal("unauthored delivery envelope was accepted")
	}
	result, err := database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
		VALUES(?,'external','other:reporter','2026-08-20T10:05:00Z')`, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	otherReporter, _ := result.LastInsertId()
	result, err = database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,3,'wrong-attempt-kind',zeroblob(32),'stage_reported',?,'2026-08-20T10:05:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	wrongAttemptEvent, _ := result.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,
		start_delivery_event_id,reason_code,created_at) VALUES(?,2,2,?,'test','2026-08-20T10:05:00Z')`,
		deliveryID, wrongAttemptEvent); err == nil {
		t.Fatal("attempt bound to wrong envelope kind")
	}
	result, err = database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,4,'reporter-mismatch',zeroblob(32),'stage_reported',?,'2026-08-20T10:05:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	mismatchEvent, _ := result.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,execution_start_stage_event_id,
		semantic_state,spec_revision,server_received_at)
		VALUES(?,?,'specification',1,2,1,?,'semantic_report',?,?,'active',1,'2026-08-20T10:05:00Z')`,
		deliveryID, attemptID, mismatchEvent, otherReporter, startID); err == nil {
		t.Fatal("stage fact consumed another reporter's envelope")
	}
	result, err = database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,5,'wrong-stage-kind',zeroblob(32),'attempt_started',?,'2026-08-20T10:05:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	wrongStageEvent, _ := result.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,execution_start_stage_event_id,
		semantic_state,spec_revision,server_received_at)
		VALUES(?,?,'specification',1,2,1,?,'semantic_report',?,?,'active',1,'2026-08-20T10:05:00Z')`,
		deliveryID, attemptID, wrongStageEvent, reporterID, startID); err == nil {
		t.Fatal("stage fact consumed wrong envelope kind")
	}
	result, err = database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status,delivery_instrumentation_version)
		VALUES(?,?,'failed',1)`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_agent_run_links(agent_run_id,root_issue_id,delivery_id,
		attempt_id,stage_key,execution_number,execution_start_stage_event_id,reporter_id,link_delivery_event_id,created_at)
		VALUES(?,?,?,?,'specification',1,?,?,?,'2026-08-20T10:05:00Z')`, runID, issueID, deliveryID,
		attemptID, startID, reporterID, wrongStageEvent); err == nil {
		t.Fatal("run link consumed wrong envelope kind")
	}
	result, err = database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at)
		VALUES(?,6,'link-reporter-mismatch',zeroblob(32),'run_linked',?,'2026-08-20T10:05:00Z')`, deliveryID, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	runLinkEvent, _ := result.LastInsertId()
	if _, err := database.Exec(`INSERT INTO delivery_agent_run_links(agent_run_id,root_issue_id,delivery_id,
		attempt_id,stage_key,execution_number,execution_start_stage_event_id,reporter_id,link_delivery_event_id,created_at)
		VALUES(?,?,?,?,'specification',1,?,?,?,'2026-08-20T10:05:00Z')`, runID, issueID, deliveryID,
		attemptID, startID, otherReporter, runLinkEvent); err == nil {
		t.Fatal("run link consumed another reporter's envelope")
	}

	if _, err := database.Exec(`DELETE FROM delivery_change_retention`); err == nil {
		t.Fatal("retention root was deletable")
	}
	if _, err := database.Exec(`UPDATE delivery_change_retention SET floor_id=99`); err == nil {
		t.Fatal("retention history was mutable")
	}
	for name, args := range map[string][]any{
		"wrong-root":    {deliveryID, issueID + 999, deliveryKey, projectID},
		"wrong-key":     {deliveryID, issueID, "issue:wrong", projectID},
		"wrong-project": {deliveryID, issueID, deliveryKey, projectID + 999},
	} {
		if _, err := database.Exec(`INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,
			delivery_key,project_id_hint,change_sequence,delivery_revision,kind,source_kind,server_received_at)
			VALUES(lower(hex(randomblob(16))),?,?,?,?,1,0,'issue','issue','2026-08-20T10:05:00Z')`,
			args...); err == nil {
			t.Fatalf("change log accepted %s provenance", name)
		}
	}
	if _, err := database.Exec(`DELETE FROM delivery_stage_events WHERE id=?`, startID); err == nil {
		t.Fatal("append-only child was directly deletable")
	}
	if _, err := database.Exec(`DELETE FROM deliveries WHERE id=?`, deliveryID); err == nil {
		t.Fatal("live delivery was directly deletable")
	}
	var deleteContext int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='delivery_root_delete_context'`).Scan(&deleteContext); err != nil || deleteContext != 0 {
		t.Fatalf("forgeable delete context still exists: count=%d err=%v", deleteContext, err)
	}
	if _, err := database.Exec(`DELETE FROM issues WHERE id=?`, issueID); err != nil {
		t.Fatalf("legitimate root cascade failed: %v", err)
	}
	var tombstones int
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log WHERE delivery_id=? AND kind='root_deleted'`, deliveryID).Scan(&tombstones); err != nil || tombstones != 0 {
		t.Fatalf("cascade tombstone count=%d err=%v", tombstones, err)
	}
	var removalKind, removalSourceKind string
	var removalSourceID int64
	if err := database.QueryRow(`SELECT kind,source_kind,source_id FROM delivery_change_log
		WHERE delivery_id=? ORDER BY change_sequence DESC LIMIT 1`, deliveryID).
		Scan(&removalKind, &removalSourceKind, &removalSourceID); err != nil ||
		removalKind != "issue" || removalSourceKind != "issue" || removalSourceID != issueID {
		t.Fatalf("retained cascade removal=(%q,%q,%d) err=%v", removalKind, removalSourceKind, removalSourceID, err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,
		delivery_key,project_id_hint,change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
		VALUES(lower(hex(randomblob(16))),?,?,?,?,999,0,'root_deleted','issue',?,'2026-08-20T10:06:00Z')`,
		deliveryID, issueID, deliveryKey, projectID, issueID); err == nil {
		t.Fatal("post-delete writer appended a second tombstone")
	}
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check returned a violation after guarded cascade")
	}
}

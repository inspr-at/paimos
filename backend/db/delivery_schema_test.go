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
	startEvent := event(2, "start", "stage_execution_started")
	result, err = database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,semantic_state,server_received_at)
		VALUES(?,?,'specification',1,1,1,?,'execution_started',?,'active','2026-08-20T10:00:00Z')`,
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
	assertForbidden := func(surface string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "forbidden delivery "+surface+" value") {
			t.Fatalf("%s surface did not reject via its privacy backstop: %v", surface, err)
		}
	}
	const forbidden = "token/abcdefghijklmnop"
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

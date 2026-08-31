// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const m167TestShortBodyLimit = 1200

func openM167Fixture(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m167.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := migrateThrough(database, 166); err != nil {
		t.Fatalf("migrate through M166: %v", err)
	}
	conn, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := checkM167SchemaIsUnapplied(context.Background(), conn); err != nil {
		t.Fatalf("M167 precondition: %v", err)
	}
	if err := applyMigrationAtomic(context.Background(), conn, migration{version: 167, steps: migration167StructuredKnowledgeSteps(m167TestShortBodyLimit)}); err != nil {
		t.Fatalf("apply M167: %v", err)
	}
	return database, conn
}

func TestMigration167ProductionRegistrationIsPinnedOrderedAndPreconditioned(t *testing.T) {
	database := openTestDB(t)
	var applied int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_versions WHERE version=167`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("M167 application count=%d err=%v, want exactly one", applied, err)
	}
	var triggerSQL string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='trg_structured_knowledge_issue_update'`).Scan(&triggerSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(triggerSQL, "BETWEEN 1 AND 1200") {
		t.Fatalf("production trigger does not pin the 1,200-byte bound: %s", triggerSQL)
	}

	partial, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m167-partial.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer partial.Close()
	if err := migrateThrough(partial, 166); err != nil {
		t.Fatalf("migrate partial fixture through M166: %v", err)
	}
	if _, err := partial.Exec(`CREATE TABLE structured_knowledge_entries(knowledge_id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(partial, 167); err == nil || !strings.Contains(err.Error(), "M167 schema is partially present") {
		t.Fatalf("partial M167 was not rejected before migration: %v", err)
	}
	var partialApplied int
	if err := partial.QueryRow(`SELECT COUNT(*) FROM schema_versions WHERE version=167`).Scan(&partialApplied); err != nil || partialApplied != 0 {
		t.Fatalf("partial fixture recorded M167=%d err=%v", partialApplied, err)
	}
}

func seedM167Project(t *testing.T, database *sql.DB, key string) (projectID, userID int64, compactID string) {
	t.Helper()
	result, err := database.Exec(`INSERT INTO projects(name,key) VALUES(?,?)`, key+" project", key)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ = result.LastInsertId()
	result, err = database.Exec(`INSERT INTO users(username,password,role,status) VALUES(?,?, 'admin','active')`, strings.ToLower(key)+"-admin", "x")
	if err != nil {
		t.Fatal(err)
	}
	userID, _ = result.LastInsertId()
	suffix := "aaaaaaaaaaaa"
	if strings.HasSuffix(key, "B") {
		suffix = "bbbbbbbbbbbb"
	} else if strings.HasPrefix(key, "LEG") {
		suffix = "cccccccccccc"
	}
	compactID = "11111111-1111-4111-8111-" + suffix
	if _, err := database.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,title,created_by_user_id,updated_by_user_id)
		VALUES(?,?,'paimos','Compact',?,?)`, compactID, projectID, userID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO knowledge_compact_sessions(
		project_id,product_session_id,revision,created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,?,1,?,?, '2026-08-31T10:00:00.000Z','2026-08-31T10:00:00.000Z')`, projectID, compactID, userID, userID); err != nil {
		t.Fatal(err)
	}
	return projectID, userID, compactID
}

func seedM167Knowledge(t *testing.T, database *sql.DB, projectID, userID int64, compactID, slug string) int64 {
	t.Helper()
	var next int64
	if err := database.QueryRow(`SELECT COALESCE(MAX(issue_number),0)+1 FROM issues WHERE project_id=?`, projectID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	result, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata,updated_at)
		VALUES(?,?,'memory',?,'Compact body','backlog','medium',?,?,'{}','2026-08-31T10:00:00.000Z')`, projectID, next, slug, userID, slug)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	if _, err := database.Exec(`INSERT INTO structured_knowledge_entries(
		knowledge_id,level,origin_project_id,purpose,authored_product_session_id,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,'project',?,'Test purpose',?,1,?,?,'2026-08-31T10:00:00.000Z','2026-08-31T10:00:00.000Z')`,
		id, projectID, compactID, userID, userID); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMigration167IsAdditiveAndEnforcesCompactContentScopeAndCanonicalGraph(t *testing.T) {
	database, _ := openM167Fixture(t)
	projectA, userA, compactA := seedM167Project(t, database, "SVA")
	projectB, userB, compactB := seedM167Project(t, database, "SVB")
	entryA := seedM167Knowledge(t, database, projectA, userA, compactA, "entry-a")
	entryB := seedM167Knowledge(t, database, projectA, userA, compactA, "entry-b")
	foreign := seedM167Knowledge(t, database, projectB, userB, compactB, "foreign")
	globalResult, err := database.Exec(`INSERT INTO issues(project_id,user_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata,updated_at)
		VALUES(NULL,NULL,1,'memory','Unpromoted','Compact body','backlog','medium',?,'unpromoted','{}','2026-08-31T10:00:00.000Z')`, userA)
	if err != nil {
		t.Fatal(err)
	}
	globalID, _ := globalResult.LastInsertId()
	if _, err := database.Exec(`INSERT INTO structured_knowledge_entries(
		knowledge_id,level,origin_project_id,purpose,authored_product_session_id,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,'instance',?,'No bypass',?,1,?,?,'2026-08-31T10:00:00.000Z','2026-08-31T10:00:00.000Z')`,
		globalID, projectA, compactA, userA, userA); err == nil || !strings.Contains(err.Error(), "content, scope, or Compact") {
		t.Fatalf("unpromoted higher-level insert error=%v", err)
	}

	if _, err := database.Exec(`UPDATE issues SET description=? WHERE id=?`, strings.Repeat("x", m167TestShortBodyLimit+1), entryA); err == nil || !strings.Contains(err.Error(), "content or scope") {
		t.Fatalf("overlong structured body error=%v", err)
	}
	if _, err := database.Exec(`UPDATE issues SET title='Legacy bypass',updated_at='2026-08-31T10:02:00.000Z' WHERE id=?`, entryA); err == nil || !strings.Contains(err.Error(), "content or scope") {
		t.Fatalf("legacy content bypass error=%v", err)
	}
	if _, err := database.Exec(`UPDATE issues SET title='Timestamp replay bypass',
		updated_at=(SELECT updated_at FROM structured_knowledge_entries WHERE knowledge_id=?) WHERE id=?`, entryA, entryA); err == nil || !strings.Contains(err.Error(), "content or scope") {
		t.Fatalf("current structured timestamp replay error=%v", err)
	}
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE structured_knowledge_entries SET purpose='Revised purpose',revision=2,
		updated_by_user_id=?,updated_at='2026-08-31T10:03:00.000Z' WHERE knowledge_id=?`, userA, entryA); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE issues SET title='Strict update',updated_at='2026-08-31T10:03:00.000Z' WHERE id=?`, entryA); err != nil {
		t.Fatalf("synchronized structured update rejected: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO structured_knowledge_links(source_knowledge_id,target_issue_id,canonical_kind,created_by_user_id)
		VALUES(?,?,'see_also',?)`, entryA, foreign, userA); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("cross-project link error=%v", err)
	}
	if _, err := database.Exec(`INSERT INTO structured_knowledge_links(source_knowledge_id,target_issue_id,canonical_kind,created_by_user_id)
		VALUES(?,?,'parent',?)`, entryA, entryB, userA); err != nil {
		t.Fatalf("same-project parent rejected: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO structured_knowledge_links(source_knowledge_id,target_issue_id,canonical_kind,created_by_user_id)
		VALUES(?,?,'parent',?)`, entryB, entryA, userA); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("parent cycle error=%v", err)
	}
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'related')`, entryA, entryB); err == nil || !strings.Contains(err.Error(), "canonical link table") {
		t.Fatalf("competing graph error=%v", err)
	}
	const mismatchedProposalID = "88888888-8888-4888-8888-888888888888"
	if _, err := database.Exec(`INSERT INTO structured_knowledge_proposals(
		proposal_id,project_id,product_session_id,source_kind,proposed_type,slug,title,purpose,candidate_body,
		created_by_user_id,created_at,updated_at)
		VALUES(?,?,?,'remember','memory','entry-a','Different reviewed title','Test purpose','Compact body',?,
		'2026-08-31T10:00:00.000Z','2026-08-31T10:00:00.000Z')`, mismatchedProposalID, projectA, compactA, userA); err != nil {
		t.Fatalf("seed mismatched proposal: %v", err)
	}
	if _, err := database.Exec(`UPDATE structured_knowledge_proposals SET state='promoted',promoted_knowledge_id=?,
		updated_at='2026-08-31T10:04:00.000Z' WHERE proposal_id=?`, entryA, mismatchedProposalID); err == nil || !strings.Contains(err.Error(), "transition is invalid") {
		t.Fatalf("proposal promoted to a different candidate error=%v", err)
	}
	if _, err := database.Exec(`UPDATE knowledge_compact_sessions SET product_session_id=?,revision=2,updated_by_user_id=?,updated_at='2026-08-31T10:01:00.000Z'
		WHERE project_id=?`, compactB, userA, projectA); err == nil {
		t.Fatal("used Compact binding moved across project")
	}

	var version, structuredCount int
	if err := database.QueryRow(`SELECT MAX(version) FROM schema_versions`).Scan(&version); err != nil || version != 167 {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM structured_knowledge_entries`).Scan(&structuredCount); err != nil || structuredCount != 3 {
		t.Fatalf("structured count=%d err=%v", structuredCount, err)
	}
	var violations int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign_key_check=%d err=%v", violations, err)
	}
}

func TestMigration167LeavesLegacyKnowledgeExplicitlyUnadopted(t *testing.T) {
	database, _ := openM167Fixture(t)
	projectID, userID, compactID := seedM167Project(t, database, "LEG")
	if _, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata)
		VALUES(?,1,'runbook','Legacy essay',?,'backlog','medium',?,'legacy','{}')`, projectID, strings.Repeat("essay ", 1000), userID); err != nil {
		t.Fatal(err)
	}
	var structuredCount, issueCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM structured_knowledge_entries`).Scan(&structuredCount); err != nil || structuredCount != 0 {
		t.Fatalf("legacy row silently adopted count=%d err=%v", structuredCount, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM issues WHERE project_id=? AND slug='legacy' AND deleted_at IS NULL`, projectID).Scan(&issueCount); err != nil || issueCount != 1 {
		t.Fatalf("legacy row lost count=%d err=%v", issueCount, err)
	}
	result, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata,updated_at)
		VALUES(?,2,'memory','Malformed identity','Short body','backlog','medium',?,'Bad Slug','{}','2026-08-31T10:00:00.000Z')`, projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	invalidID, _ := result.LastInsertId()
	if _, err := database.Exec(`INSERT INTO structured_knowledge_entries(
		knowledge_id,level,origin_project_id,purpose,authored_product_session_id,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,'project',?,'Test purpose',?,1,?,?,'2026-08-31T10:00:00.000Z','2026-08-31T10:00:00.000Z')`,
		invalidID, projectID, compactID, userID, userID); err == nil || !strings.Contains(err.Error(), "content, scope, or Compact") {
		t.Fatalf("malformed legacy slug adoption error=%v", err)
	}
	metadataResult, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata,updated_at)
		VALUES(?,3,'memory','Legacy inheritance','Short body','backlog','medium',?,'legacy-inherit','{"inherit":true}','2026-08-31T10:00:00.000Z')`, projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	metadataID, _ := metadataResult.LastInsertId()
	if _, err := database.Exec(`INSERT INTO structured_knowledge_entries(
		knowledge_id,level,origin_project_id,purpose,authored_product_session_id,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,'project',?,'Test purpose',?,1,?,?,'2026-08-31T10:00:00.000Z','2026-08-31T10:00:00.000Z')`,
		metadataID, projectID, compactID, userID, userID); err == nil || !strings.Contains(err.Error(), "content, scope, or Compact") {
		t.Fatalf("legacy inheritance metadata adoption error=%v", err)
	}
	var preservedMetadata string
	if err := database.QueryRow(`SELECT category_metadata FROM issues WHERE id=?`, metadataID).Scan(&preservedMetadata); err != nil || preservedMetadata != `{"inherit":true}` {
		t.Fatalf("ordinary legacy metadata changed=%q err=%v", preservedMetadata, err)
	}
}

func TestMigration167CompactRejectsAgentProductSessionOnInsertAndUpdate(t *testing.T) {
	database, _ := openM167Fixture(t)
	projectResult, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Compact target','CTG')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	userResult, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('compact-admin','x','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := userResult.LastInsertId()
	agentResult, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'compact-agent')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agentResult.LastInsertId()
	const paimosSessionID = "66666666-6666-4666-8666-666666666666"
	const agentSessionID = "77777777-7777-4777-8777-777777777777"
	if _, err := database.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,title,created_by_user_id,updated_by_user_id)
		VALUES(?,?,'paimos','Paimos Compact',?,?)`, paimosSessionID, projectID, userID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,target_project_agent_id,title,created_by_user_id,updated_by_user_id)
		VALUES(?,?,'project_agent',?,'Agent conversation',?,?)`, agentSessionID, projectID, agentID, userID, userID); err != nil {
		t.Fatal(err)
	}
	const now = "2026-08-31T10:00:00.000Z"
	if _, err := database.Exec(`INSERT INTO knowledge_compact_sessions(
		project_id,product_session_id,revision,created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,?,1,?,?,?,?)`, projectID, agentSessionID, userID, userID, now, now); err == nil || !strings.Contains(err.Error(), "Paimos product session") {
		t.Fatalf("agent-target Compact insert error=%v", err)
	}
	if _, err := database.Exec(`INSERT INTO knowledge_compact_sessions(
		project_id,product_session_id,revision,created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,?,1,?,?,?,?)`, projectID, paimosSessionID, userID, userID, now, now); err != nil {
		t.Fatalf("Paimos Compact insert rejected: %v", err)
	}
	if _, err := database.Exec(`UPDATE knowledge_compact_sessions SET product_session_id=?,revision=2,
		updated_by_user_id=?,updated_at='2026-08-31T10:01:00.000Z' WHERE project_id=?`, agentSessionID, userID, projectID); err == nil || !strings.Contains(err.Error(), "Compact binding") {
		t.Fatalf("agent-target Compact update error=%v", err)
	}
}

func TestMigration167RequiresAtomicPromotionEvidenceAndSingleUseSource(t *testing.T) {
	database, _ := openM167Fixture(t)
	projectID, userID, compactID := seedM167Project(t, database, "PRO")
	sourceID := seedM167Knowledge(t, database, projectID, userID, compactID, "promoted-fact")

	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	const now = "2026-08-31T10:10:00.000Z"
	if _, err := tx.Exec(`UPDATE issues SET deleted_at=?,deleted_by=?,updated_at=? WHERE id=?`, now, userID, now, sourceID); err != nil {
		t.Fatalf("soft-delete promotion source: %v", err)
	}
	result, err := tx.Exec(`INSERT INTO issues(project_id,user_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata,updated_at)
		VALUES(NULL,NULL,1,'memory','promoted-fact','Compact body','backlog','medium',?,'promoted-fact','{}',?)`, userID, now)
	if err != nil {
		t.Fatalf("create promotion destination: %v", err)
	}
	destinationID, _ := result.LastInsertId()
	const promotionID = "44444444-4444-4444-8444-444444444444"
	if _, err := tx.Exec(`UPDATE issues SET title='substituted title' WHERE id=?`, destinationID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO structured_knowledge_promotions(
		promotion_id,source_knowledge_id,destination_knowledge_id,from_level,to_level,actor_user_id,created_at)
		VALUES('33333333-3333-4333-8333-333333333333',?,?,'project','instance',?,?)`, sourceID, destinationID, userID, now); err == nil || !strings.Contains(err.Error(), "does not match source and destination") {
		t.Fatalf("promotion evidence accepted substituted issue content: %v", err)
	}
	if _, err := tx.Exec(`UPDATE issues SET title='promoted-fact' WHERE id=?`, destinationID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO structured_knowledge_promotions(
		promotion_id,source_knowledge_id,destination_knowledge_id,from_level,to_level,actor_user_id,created_at)
		VALUES(?,?,?,'project','instance',?,?)`, promotionID, sourceID, destinationID, userID, now); err != nil {
		t.Fatalf("record promotion evidence: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO structured_knowledge_entries(
		knowledge_id,level,origin_project_id,purpose,authored_product_session_id,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,'instance',?,'substituted purpose',?,1,?,?,?,?)`, destinationID, projectID, compactID, userID, userID, now, now); err == nil || !strings.Contains(err.Error(), "content, scope, or Compact session contract") {
		t.Fatalf("promotion overlay accepted substituted source lineage: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO structured_knowledge_entries(
		knowledge_id,level,origin_project_id,purpose,authored_product_session_id,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,'instance',?,'Test purpose',?,1,?,?,?,?)`, destinationID, projectID, compactID, userID, userID, now, now); err != nil {
		t.Fatalf("create evidenced destination overlay: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO structured_knowledge_promotions(
		promotion_id,source_knowledge_id,destination_knowledge_id,from_level,to_level,actor_user_id,created_at)
		VALUES('55555555-5555-4555-8555-555555555555',?,?,'project','instance',?,?)`, sourceID, destinationID, userID, now); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("promotion source reused error=%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE issues SET deleted_at=NULL,deleted_by=NULL WHERE id=?`, sourceID); err == nil || !strings.Contains(err.Error(), "promoted structured knowledge source is immutable") {
		t.Fatalf("promoted source restore error=%v", err)
	}
	if _, err := database.Exec(`UPDATE issues SET title='rewritten evidence' WHERE id=?`, sourceID); err == nil || !strings.Contains(err.Error(), "promoted structured knowledge source is immutable") {
		t.Fatalf("promoted source content rewrite error=%v", err)
	}
	if _, err := database.Exec(`UPDATE structured_knowledge_entries SET purpose='rewritten purpose',revision=revision+1,
		updated_by_user_id=?,updated_at='2026-08-31T10:11:00.000Z' WHERE knowledge_id=?`, userID, sourceID); err == nil || !strings.Contains(err.Error(), "source overlay is immutable") {
		t.Fatalf("promoted source purpose rewrite error=%v", err)
	}
	if _, err := database.Exec(`DELETE FROM structured_knowledge_entries WHERE knowledge_id=?`, sourceID); err == nil || !strings.Contains(err.Error(), "source overlay is immutable") {
		t.Fatalf("promoted source overlay delete error=%v", err)
	}
	if _, err := database.Exec(`UPDATE structured_knowledge_entries SET purpose='Destination remains editable',revision=revision+1,
		updated_by_user_id=?,updated_at='2026-08-31T10:11:00.000Z' WHERE knowledge_id=?`, userID, destinationID); err != nil {
		t.Fatalf("promotion destination overlay became immutable: %v", err)
	}
}

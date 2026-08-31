// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

func TestStructuredKnowledgePromotionTransactionConcealsAndCommitsAtomicDrop(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})
	if err := db.ApplyStructuredKnowledgeMigrationForTest(context.Background(), db.DB, 1200); err != nil {
		t.Fatal(err)
	}

	seedUser := func(username, role string) int64 {
		result, err := db.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES(?, 'x',?,'active')`, username, role)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	memberID := seedUser("promotion-member", "member")
	adminID := seedUser("promotion-admin", "admin")
	superID := seedUser("promotion-super", "admin")
	if _, err := db.DB.Exec(`UPDATE users SET role_key='super_admin',is_super_admin=1 WHERE id=?`, superID); err != nil {
		t.Fatal(err)
	}
	projectResult, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Promotion project','PRM','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'none')`, memberID, projectID); err != nil {
		t.Fatal(err)
	}
	compactID := uuid.NewString()
	if _, err := db.DB.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,title,created_by_user_id,updated_by_user_id)
		VALUES(?,?,'paimos','Promotion Compact',?,?)`, compactID, projectID, adminID, adminID); err != nil {
		t.Fatal(err)
	}
	const now = "2026-08-31T11:00:00.000Z"
	if _, err := db.DB.Exec(`INSERT INTO knowledge_compact_sessions(
		project_id,product_session_id,revision,created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,?,1,?,?,?,?)`, projectID, compactID, adminID, adminID, now, now); err != nil {
		t.Fatal(err)
	}
	sourceResult, err := db.DB.Exec(`INSERT INTO issues(
		project_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata,updated_at)
		VALUES(?,1,'memory','Promote fact','Compact promotion body','backlog','medium',?,'promote-fact','{}',?)`, projectID, adminID, now)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, _ := sourceResult.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO structured_knowledge_entries(
		knowledge_id,level,origin_project_id,purpose,authored_product_session_id,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,'project',?,'Prove atomic promotion.',?,1,?,?,?,?)`, sourceID, projectID, compactID, adminID, adminID, now, now); err != nil {
		t.Fatal(err)
	}
	targetResult, err := db.DB.Exec(`INSERT INTO issues(
		project_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata)
		VALUES(?,2,'ticket','Local target','Work node','backlog','medium',?,NULL,'{}')`, projectID, adminID)
	if err != nil {
		t.Fatal(err)
	}
	targetID, _ := targetResult.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO structured_knowledge_links(
		source_knowledge_id,target_issue_id,canonical_kind,created_by_user_id) VALUES(?,?,'about',?)`, sourceID, targetID, adminID); err != nil {
		t.Fatal(err)
	}

	requestFor := func(userID int64, username string) *http.Request {
		credentialID := uuid.NewString()
		sessionID := uuid.NewString()
		if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
			VALUES(?,?,datetime('now','+1 hour'),datetime('now'),?)`, sessionID, userID, credentialID); err != nil {
			t.Fatal(err)
		}
		principal, err := auth.NewSessionPrincipal(credentialID, userID, userID, false)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/structured-knowledge/v1/entries/%d/promote", sourceID), nil)
		request.Header.Set("X-Test-Actor", username)
		return request.WithContext(auth.WithPrincipal(request.Context(), principal))
	}
	policy := structuredKnowledgePromotionPolicyV1{
		ProjectToInstanceAdmin:       true,
		InstanceToTerminalSuperAdmin: true,
	}

	// Establish an instance-level identity, then recreate its project-level
	// source identity. Promoting the focal row can now prove both outcomes in
	// one transaction: remap this canonical edge and drop the unstructured one.
	const counterpartTime = "2026-08-31T11:01:00.000Z"
	counterpartResult, err := db.DB.Exec(`INSERT INTO issues(
		project_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata,updated_at)
		VALUES(?,3,'memory','Counterpart fact','Counterpart body','backlog','medium',?,'counterpart-fact','{}',?)`, projectID, adminID, counterpartTime)
	if err != nil {
		t.Fatal(err)
	}
	counterpartSourceID, _ := counterpartResult.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO structured_knowledge_entries(
		knowledge_id,level,origin_project_id,purpose,authored_product_session_id,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,'project',?,'Provide remap identity.',?,1,?,?,?,?)`, counterpartSourceID, projectID, compactID, adminID, adminID, counterpartTime, counterpartTime); err != nil {
		t.Fatal(err)
	}
	if _, _, err := promoteStructuredKnowledgeTx(context.Background(), requestFor(adminID, "counterpart-admin"), counterpartSourceID, "instance", policy); err != nil {
		t.Fatalf("seed counterpart promotion: %v", err)
	}
	const replacementTime = "2026-08-31T11:02:00.000Z"
	replacementResult, err := db.DB.Exec(`INSERT INTO issues(
		project_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata,updated_at)
		VALUES(?,4,'memory','Counterpart fact','Counterpart body','backlog','medium',?,'counterpart-fact','{}',?)`, projectID, adminID, replacementTime)
	if err != nil {
		t.Fatal(err)
	}
	replacementID, _ := replacementResult.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO structured_knowledge_entries(
		knowledge_id,level,origin_project_id,purpose,authored_product_session_id,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,'project',?,'Provide remap identity.',?,1,?,?,?,?)`, replacementID, projectID, compactID, adminID, adminID, replacementTime, replacementTime); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO structured_knowledge_links(
		source_knowledge_id,target_issue_id,canonical_kind,created_by_user_id) VALUES(?,?,'see_also',?)`, sourceID, replacementID, adminID); err != nil {
		t.Fatal(err)
	}

	if _, _, err := promoteStructuredKnowledgeTx(context.Background(), requestFor(memberID, "foreign"), sourceID, "instance", policy); !errors.Is(err, errStructuredPromotionNotFound) {
		t.Fatalf("foreign member promotion error=%v", err)
	}
	var promotionCount int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM structured_knowledge_promotions`).Scan(&promotionCount)
	if promotionCount != 1 {
		t.Fatalf("concealed promotion mutated rows=%d", promotionCount)
	}
	if _, err := db.DB.Exec(`UPDATE project_members SET access_level='editor' WHERE user_id=? AND project_id=?`, memberID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := promoteStructuredKnowledgeTx(context.Background(), requestFor(memberID, "same-project"), sourceID, "instance", policy); !errors.Is(err, errStructuredPromotionForbidden) {
		t.Fatalf("same-project member promotion error=%v", err)
	}

	projectResultValue, originProjectID, err := promoteStructuredKnowledgeTx(context.Background(), requestFor(adminID, "admin"), sourceID, "instance", policy)
	if err != nil {
		t.Fatalf("project promotion: %v", err)
	}
	if originProjectID != projectID || projectResultValue.ActorUserID != adminID || len(projectResultValue.Links) != 2 {
		t.Fatalf("project promotion result=%+v origin=%d", projectResultValue, originProjectID)
	}
	outcomes := map[string]bool{}
	for _, link := range projectResultValue.Links {
		outcomes[link.Outcome+":"+link.Reason] = true
	}
	if !outcomes["dropped:node_target_dropped"] || !outcomes["remapped:same_scope_identity"] {
		t.Fatalf("promotion link outcomes=%v", outcomes)
	}
	instanceID := projectResultValue.Entry.KnowledgeID
	var sourceDeleted string
	if err := db.DB.QueryRow(`SELECT deleted_at FROM issues WHERE id=?`, sourceID).Scan(&sourceDeleted); err != nil || sourceDeleted == "" {
		t.Fatalf("source deletion=%q err=%v", sourceDeleted, err)
	}
	var instanceLevel, linkOutcome string
	if err := db.DB.QueryRow(`SELECT level FROM structured_knowledge_entries WHERE knowledge_id=?`, instanceID).Scan(&instanceLevel); err != nil || instanceLevel != "instance" {
		t.Fatalf("destination level=%q err=%v", instanceLevel, err)
	}
	if err := db.DB.QueryRow(`SELECT group_concat(outcome,',') FROM structured_knowledge_promotion_links WHERE promotion_id=?
		ORDER BY original_link_id`, projectResultValue.PromotionID).Scan(&linkOutcome); err != nil || !strings.Contains(linkOutcome, "dropped") || !strings.Contains(linkOutcome, "remapped") {
		t.Fatalf("link evidence=%q err=%v", linkOutcome, err)
	}
	var adminMutationCount int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM mutation_log WHERE user_id=? AND subject_type='issue'
		AND subject_id IN (?,?)`, adminID, sourceID, instanceID).Scan(&adminMutationCount); err != nil || adminMutationCount != 2 {
		t.Fatalf("admin mutation attribution=%d err=%v", adminMutationCount, err)
	}

	if _, _, err := promoteStructuredKnowledgeTx(context.Background(), requestFor(adminID, "instance-admin"), instanceID, "kernel", policy); !errors.Is(err, errStructuredPromotionNotFound) {
		t.Fatalf("instance source leaked to non-super-admin: %v", err)
	}
	terminalResult, _, err := promoteStructuredKnowledgeTx(context.Background(), requestFor(superID, "super"), instanceID, "vision", policy)
	if err != nil {
		t.Fatalf("terminal promotion: %v", err)
	}
	if terminalResult.ToLevel != "vision" || terminalResult.ActorUserID != superID {
		t.Fatalf("terminal promotion result=%+v", terminalResult)
	}
}

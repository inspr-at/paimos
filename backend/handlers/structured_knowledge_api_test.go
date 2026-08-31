// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/db"
)

const structuredKnowledgeTestLimit = 1200

type structuredKnowledgeTestEntry struct {
	KnowledgeID int64 `json:"knowledge_id"`
}

func setupStructuredKnowledgeAPI(t *testing.T) (*testServer, int64, string, int64, int64) {
	t.Helper()
	t.Setenv("PAIMOS_TEST_PENDING_STRUCTURED_KNOWLEDGE", "1")
	ts := newTestServer(t)
	if err := db.ApplyStructuredKnowledgeMigrationForTest(context.Background(), db.DB, structuredKnowledgeTestLimit); err != nil {
		t.Fatalf("apply isolated M167: %v", err)
	}
	projectID := seedBatchProject(t, "Structured knowledge", "SKA")
	var adminID, memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')
		ON CONFLICT(user_id,project_id) DO UPDATE SET access_level='editor'`, memberID, projectID); err != nil {
		t.Fatal(err)
	}
	compactID := uuid.NewString()
	if _, err := db.DB.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,title,created_by_user_id,updated_by_user_id)
		VALUES(?,?,'paimos','Knowledge Compact',?,?)`, compactID, projectID, adminID, adminID); err != nil {
		t.Fatal(err)
	}
	response := ts.put(t, fmt.Sprintf("/api/projects/%d/structured-knowledge/v1/compact", projectID), ts.adminCookie,
		map[string]any{"product_session_id": compactID, "expected_revision": 0})
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()
	return ts, projectID, compactID, adminID, memberID
}

func TestStructuredKnowledgeAPIScopeProposalWritesLinksAndAttribution(t *testing.T) {
	ts, projectID, compactID, adminID, memberID := setupStructuredKnowledgeAPI(t)
	base := fmt.Sprintf("/api/projects/%d/structured-knowledge/v1", projectID)

	// A project-agent conversation is a valid product session but never the
	// Paimos-target Compact session.
	agentResult, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'knowledge-agent')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agentResult.LastInsertId()
	agentSessionID := uuid.NewString()
	if _, err := db.DB.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,target_project_agent_id,title,created_by_user_id,updated_by_user_id)
		VALUES(?,?,'project_agent',?,'Agent conversation',?,?)`, agentSessionID, projectID, agentID, adminID, adminID); err != nil {
		t.Fatal(err)
	}
	agentBind := ts.put(t, base+"/compact", ts.adminCookie,
		map[string]any{"product_session_id": agentSessionID, "expected_revision": 1})
	assertStatus(t, agentBind, http.StatusUnprocessableEntity)
	agentBind.Body.Close()

	candidate := map[string]any{
		"type": "memory", "slug": "reviewed-fact", "title": "Reviewed fact",
		"purpose": "Keep one reviewed durable fact.", "short_body": "A compact reviewed body.",
	}
	remember := ts.post(t, base+"/remember", ts.memberCookie, candidate)
	assertStatus(t, remember, http.StatusCreated)
	var proposal struct {
		ProposalID       string `json:"proposal_id"`
		ProductSessionID string `json:"product_session_id"`
		State            string `json:"state"`
	}
	decode(t, remember, &proposal)
	if proposal.State != "proposed" || proposal.ProductSessionID != compactID {
		t.Fatalf("remember response=%+v", proposal)
	}
	var issueCount, proposalCount int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM issues WHERE project_id=? AND slug='reviewed-fact'`, projectID).Scan(&issueCount)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM structured_knowledge_proposals WHERE project_id=?`, projectID).Scan(&proposalCount)
	if issueCount != 0 || proposalCount != 1 {
		t.Fatalf("remember durability issues=%d proposals=%d", issueCount, proposalCount)
	}

	memberCreateBody := map[string]any{}
	for key, value := range candidate {
		memberCreateBody[key] = value
	}
	memberCreateBody["proposal_id"] = proposal.ProposalID
	forbiddenCreate := ts.post(t, base+"/entries", ts.memberCookie, memberCreateBody)
	assertStatus(t, forbiddenCreate, http.StatusForbidden)
	forbiddenCreate.Body.Close()
	created := ts.post(t, base+"/entries", ts.adminCookie, memberCreateBody)
	assertStatus(t, created, http.StatusCreated)
	var durable structuredKnowledgeTestEntry
	decode(t, created, &durable)
	if durable.KnowledgeID <= 0 {
		t.Fatalf("created entry=%+v", durable)
	}

	// Write evidence uses the actor reauthorized in the transaction, not an
	// outer request-context lookup.
	var mutationActor, historyActor int64
	if err := db.DB.QueryRow(`SELECT user_id FROM mutation_log WHERE subject_type='issue' AND subject_id=?
		ORDER BY id DESC LIMIT 1`, durable.KnowledgeID).Scan(&mutationActor); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT changed_by FROM issue_history WHERE issue_id=? ORDER BY id DESC LIMIT 1`, durable.KnowledgeID).Scan(&historyActor); err != nil {
		t.Fatal(err)
	}
	if mutationActor != adminID || historyActor != adminID {
		t.Fatalf("reauthorized attribution mutation/history=%d/%d, want %d", mutationActor, historyActor, adminID)
	}

	legacyResult, err := db.DB.Exec(`INSERT INTO issues(
		project_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata)
		VALUES(?,2,'memory','Legacy inheritance','Short legacy body','backlog','medium',?,'legacy-inherit','{"inherit":true}')`, projectID, adminID)
	if err != nil {
		t.Fatal(err)
	}
	legacyID, _ := legacyResult.LastInsertId()
	unsafeAdopt := ts.post(t, fmt.Sprintf("%s/entries/%d/adopt", base, legacyID), ts.adminCookie,
		map[string]any{"purpose": "Normalize legacy fact."})
	assertStatus(t, unsafeAdopt, http.StatusUnprocessableEntity)
	unsafeAdopt.Body.Close()
	if _, err := db.DB.Exec(`UPDATE issues SET category_metadata='{}' WHERE id=?`, legacyID); err != nil {
		t.Fatal(err)
	}
	memberAdopt := ts.post(t, fmt.Sprintf("%s/entries/%d/adopt", base, legacyID), ts.memberCookie,
		map[string]any{"purpose": "Normalize legacy fact."})
	assertStatus(t, memberAdopt, http.StatusForbidden)
	memberAdopt.Body.Close()
	adopted := ts.post(t, fmt.Sprintf("%s/entries/%d/adopt", base, legacyID), ts.adminCookie,
		map[string]any{"purpose": "Normalize legacy fact."})
	assertStatus(t, adopted, http.StatusOK)
	adopted.Body.Close()

	memberLink := ts.post(t, fmt.Sprintf("%s/entries/%d/links", base, durable.KnowledgeID), ts.memberCookie,
		map[string]any{"relation": "parent", "target_issue_id": legacyID})
	assertStatus(t, memberLink, http.StatusForbidden)
	memberLink.Body.Close()
	adminLink := ts.post(t, fmt.Sprintf("%s/entries/%d/links", base, durable.KnowledgeID), ts.adminCookie,
		map[string]any{"relation": "parent", "target_issue_id": legacyID})
	assertStatus(t, adminLink, http.StatusCreated)
	adminLink.Body.Close()

	foreignProjectID := seedBatchProject(t, "Foreign structured knowledge", "SKF")
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'none')`, memberID, foreignProjectID); err != nil {
		t.Fatal(err)
	}
	foreignResult, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,description,status,priority,created_by,slug,category_metadata)
		VALUES(?,1,'memory','Foreign secret','Never expose this body','backlog','medium',?,'foreign-secret','{}')`, foreignProjectID, adminID)
	if err != nil {
		t.Fatal(err)
	}
	foreignID, _ := foreignResult.LastInsertId()
	crossLink := ts.post(t, fmt.Sprintf("%s/entries/%d/links", base, durable.KnowledgeID), ts.adminCookie,
		map[string]any{"relation": "about", "target_issue_id": foreignID})
	assertStatus(t, crossLink, http.StatusNotFound)
	crossLink.Body.Close()

	snapshot := ts.get(t, base, ts.memberCookie)
	assertStatus(t, snapshot, http.StatusOK)
	snapshotBytes, _ := io.ReadAll(snapshot.Body)
	snapshot.Body.Close()
	if !strings.Contains(string(snapshotBytes), "Reviewed fact") || strings.Contains(string(snapshotBytes), "Foreign secret") {
		t.Fatalf("project snapshot leaked or omitted scope: %s", snapshotBytes)
	}
	foreignSnapshot := ts.get(t, fmt.Sprintf("/api/projects/%d/structured-knowledge/v1", foreignProjectID), ts.memberCookie)
	if foreignSnapshot.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(foreignSnapshot.Body)
		t.Fatalf("foreign snapshot status=%d body=%s", foreignSnapshot.StatusCode, body)
	}
	foreignBody, _ := io.ReadAll(foreignSnapshot.Body)
	foreignSnapshot.Body.Close()
	if strings.Contains(string(foreignBody), "Foreign secret") {
		t.Fatalf("foreign snapshot leaked title: %s", foreignBody)
	}
}

func TestStructuredKnowledgeProposalHTTPRejectsCandidateSubstitution(t *testing.T) {
	ts, projectID, _, _, _ := setupStructuredKnowledgeAPI(t)
	base := fmt.Sprintf("/api/projects/%d/structured-knowledge/v1", projectID)
	candidate := map[string]any{"type": "memory", "slug": "exact-review", "title": "Exact review", "purpose": "Bind review.", "short_body": "Exact body."}
	response := ts.post(t, base+"/remember", ts.memberCookie, candidate)
	assertStatus(t, response, http.StatusCreated)
	var proposal map[string]json.RawMessage
	decode(t, response, &proposal)
	var proposalID string
	if err := json.Unmarshal(proposal["proposal_id"], &proposalID); err != nil {
		t.Fatal(err)
	}
	candidate["proposal_id"] = proposalID
	candidate["title"] = "Substituted title"
	create := ts.post(t, base+"/entries", ts.adminCookie, candidate)
	assertStatus(t, create, http.StatusConflict)
	create.Body.Close()
}

package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
)

func denyMemberProject(t *testing.T, projectID int64) int64 {
	t.Helper()
	var userID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`
		INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'none')
		ON CONFLICT(user_id,project_id) DO UPDATE SET access_level='none'`, userID, projectID); err != nil {
		t.Fatal(err)
	}
	return userID
}

func liveKnowledgeCount(t *testing.T, where string, args ...any) int {
	t.Helper()
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM issues WHERE deleted_at IS NULL AND `+where, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestKnowledgeSecurity_PromotionSourcePermissionIsNonEnumerating(t *testing.T) {
	ts := newTestServer(t)
	projectID := createTestProject(t, ts, "Secret source", "SECS")
	seedMemory(t, projectID, "secret_slug", "Secret title", "Secret body", `{}`)
	denyMemberProject(t, projectID)

	denied := ts.post(t, "/api/memory/secret_slug/promote", ts.memberCookie, map[string]any{
		"to": "user", "from_project_id": projectID,
	})
	missing := ts.post(t, "/api/memory/missing_slug/promote", ts.memberCookie, map[string]any{
		"to": "user", "from_project_id": projectID + 999999,
	})
	assertStatus(t, denied, http.StatusNotFound)
	assertStatus(t, missing, http.StatusNotFound)
	var deniedBody, missingBody map[string]any
	decode(t, denied, &deniedBody)
	decode(t, missing, &missingBody)
	for _, field := range []string{"status", "code", "detail"} {
		if deniedBody[field] != missingBody[field] {
			t.Fatalf("non-enumerating field %s differs: denied=%v missing=%v", field, deniedBody[field], missingBody[field])
		}
	}
	if got := liveKnowledgeCount(t, `project_id=? AND type='memory' AND slug='secret_slug'`, projectID); got != 1 {
		t.Fatalf("denied promotion changed source count: %d", got)
	}
}

func TestKnowledgeSecurity_PromotionDestinationFailurePreservesSource(t *testing.T) {
	ts := newTestServer(t)
	destinationID := createTestProject(t, ts, "Denied destination", "DEND")
	denyMemberProject(t, destinationID)
	created := ts.post(t, userMemoryURL, ts.memberCookie, map[string]any{
		"slug": "keep_source", "title": "Keep source",
	})
	assertStatus(t, created, http.StatusCreated)

	denied := ts.post(t, "/api/memory/keep_source/promote", ts.memberCookie, map[string]any{
		"to": "project", "to_project_id": destinationID,
	})
	assertStatus(t, denied, http.StatusNotFound)
	if got := liveKnowledgeCount(t, `project_id IS NULL AND user_id=(SELECT id FROM users WHERE username='member') AND slug='keep_source'`); got != 1 {
		t.Fatalf("denied destination removed source: %d", got)
	}
	if got := liveKnowledgeCount(t, `project_id=? AND slug='keep_source'`, destinationID); got != 0 {
		t.Fatalf("denied destination received copy: %d", got)
	}
}

func TestKnowledgeSecurity_PromotionCollisionPreservesSource(t *testing.T) {
	ts := newTestServer(t)
	projectID := createTestProject(t, ts, "Collision source", "COLS")
	seedMemory(t, projectID, "same_slug", "Project source", "source", `{}`)
	created := ts.post(t, userMemoryURL, ts.adminCookie, map[string]any{
		"slug": "same_slug", "title": "Existing destination",
	})
	assertStatus(t, created, http.StatusCreated)

	resp := ts.post(t, "/api/memory/same_slug/promote", ts.adminCookie, map[string]any{
		"to": "user", "from_project_id": projectID,
	})
	assertStatus(t, resp, http.StatusConflict)
	if got := liveKnowledgeCount(t, `project_id=? AND slug='same_slug'`, projectID); got != 1 {
		t.Fatalf("destination conflict removed source: %d", got)
	}
}

func TestKnowledgeSecurity_RelationAuthorizesTargetAndWritesNothing(t *testing.T) {
	ts := newTestServer(t)
	projectA := createTestProject(t, ts, "Allowed", "RALA")
	projectB := createTestProject(t, ts, "Denied", "RALB")
	ticketID := seedTicketRow(t, projectA, 1, "Visible ticket", nil, "")
	memoryID := seedMemory(t, projectB, "hidden_memory", "Hidden metadata", "secret", `{}`)
	denyMemberProject(t, projectB)

	resp := ts.post(t, "/api/issues/"+itoa(ticketID)+"/relations", ts.memberCookie, map[string]any{
		"target_id": memoryID, "type": "applies_to_memory",
	})
	assertStatus(t, resp, http.StatusNotFound)
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM issue_relations WHERE source_id=? AND target_id=?`, ticketID, memoryID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("unauthorized target relation was persisted")
	}
}

func TestKnowledgeSecurity_CrossUserRelationMetadataIsNotEnumerable(t *testing.T) {
	ts := newTestServer(t)
	adminCreate := ts.post(t, userMemoryURL, ts.adminCookie, map[string]any{
		"slug": "admin_memory", "title": "Admin memory",
	})
	memberCreate := ts.post(t, userMemoryURL, ts.memberCookie, map[string]any{
		"slug": "member_memory", "title": "DO NOT LEAK USER MEMORY",
	})
	assertStatus(t, adminCreate, http.StatusCreated)
	assertStatus(t, memberCreate, http.StatusCreated)
	var adminMemory, memberMemory knowledgeEntry
	decode(t, adminCreate, &adminMemory)
	decode(t, memberCreate, &memberMemory)
	// Seed a legacy row directly: the write endpoint now rejects this link,
	// while the read path must remain safe for already-populated databases.
	if _, err := db.DB.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'related')`, adminMemory.ID, memberMemory.ID); err != nil {
		t.Fatal(err)
	}

	resp := ts.get(t, "/api/issues/"+itoa(adminMemory.ID)+"/relations", ts.adminCookie)
	assertStatus(t, resp, http.StatusOK)
	var relations []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&relations); err != nil {
		t.Fatal(err)
	}
	if len(relations) != 0 {
		t.Fatalf("cross-user knowledge relation was enumerable: %#v", relations)
	}
}

func TestKnowledgeSecurity_GraphDropsCrossProjectMetadata(t *testing.T) {
	ts := newTestServer(t)
	projectA := createTestProject(t, ts, "Graph visible", "GVA")
	projectB := createTestProject(t, ts, "Graph hidden", "GHB")
	visibleID := seedMemory(t, projectA, "visible", "Visible", "x", `{}`)
	hiddenID := seedMemory(t, projectB, "hidden", "DO NOT LEAK", "secret", `{}`)
	if _, err := db.DB.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'depends_on')`, visibleID, hiddenID); err != nil {
		t.Fatal(err)
	}

	resp := ts.get(t, fmt.Sprintf("/api/projects/%d/knowledge/graph", projectA), ts.adminCookie)
	assertStatus(t, resp, http.StatusOK)
	var graph struct {
		Nodes []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"nodes"`
		Edges []map[string]any `json:"edges"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&graph); err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.ID == hiddenID || node.Title == "DO NOT LEAK" {
			t.Fatalf("cross-project metadata leaked: %#v", node)
		}
	}
	if len(graph.Edges) != 0 {
		t.Fatalf("edge to restricted node leaked: %#v", graph.Edges)
	}
}

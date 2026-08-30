package handlers_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/db"
)

type productSessionResponse struct {
	ProductSessionID     string `json:"product_session_id"`
	ProjectID            int64  `json:"project_id"`
	TargetKind           string `json:"target_kind"`
	TargetProjectAgentID *int64 `json:"target_project_agent_id"`
	TargetAgentName      string `json:"target_agent_name"`
	NodeID               *int64 `json:"node_id"`
	Revision             int64  `json:"revision"`
	CreatedByUserID      *int64 `json:"created_by_user_id"`
}

type productSessionEventResponse struct {
	EventSequence  int64  `json:"event_sequence"`
	Operation      string `json:"operation"`
	ActorUserID    int64  `json:"actor_user_id"`
	BeforeNodeID   *int64 `json:"before_node_id"`
	AfterNodeID    *int64 `json:"after_node_id"`
	BeforeRevision int64  `json:"before_revision"`
	AfterRevision  int64  `json:"after_revision"`
}

func seedSessionNode(t *testing.T, projectID int64, number int, issueType string) int64 {
	t.Helper()
	result, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,priority)
		VALUES(?,?,?,'Session node','backlog','medium')`, projectID, number, issueType)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func TestProductSessionLifecycleUsesTypedIdentityAndRevisionCAS(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Product sessions", "PSS")
	otherProjectID := seedBatchProject(t, "Other sessions", "OPS")
	nodeA := seedSessionNode(t, projectID, 1, "ticket")
	nodeB := seedSessionNode(t, projectID, 2, "task")
	otherNode := seedSessionNode(t, otherProjectID, 1, "ticket")
	agent, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agent.LastInsertId()
	otherAgent, _ := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, otherProjectID)
	otherAgentID, _ := otherAgent.LastInsertId()

	base := fmt.Sprintf("/api/projects/%d/product-sessions", projectID)
	created := ts.post(t, base, ts.adminCookie, map[string]any{
		"target_kind": "paimos", "title": "Loose planning session",
	})
	assertStatus(t, created, http.StatusCreated)
	var raw map[string]json.RawMessage
	decode(t, created, &raw)
	for _, ambiguous := range []string{"id", "session_id", "harness_session_id", "agent_run_id", "delivery_id", "intake_session_id"} {
		if _, found := raw[ambiguous]; found {
			t.Fatalf("product session response exposes ambiguous identity field %q", ambiguous)
		}
	}
	var session productSessionResponse
	encoded, _ := json.Marshal(raw)
	if err := json.Unmarshal(encoded, &session); err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(session.ProductSessionID); err != nil || session.Revision != 1 || session.NodeID != nil || session.TargetAgentName != "paimos" {
		t.Fatalf("unexpected created product session: %+v parseErr=%v", session, err)
	}
	var adminID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if session.CreatedByUserID == nil || *session.CreatedByUserID != adminID {
		t.Fatalf("created_by_user_id=%v, want actor %d", session.CreatedByUserID, adminID)
	}

	// Product sessions are not harness-control sessions: the same registered
	// project agent may be selected by any number of product sessions.
	for i := 0; i < 2; i++ {
		resp := ts.post(t, base, ts.adminCookie, map[string]any{
			"target_kind": "project_agent", "target_project_agent_id": agentID,
			"title": fmt.Sprintf("Worker conversation %d", i),
		})
		assertStatus(t, resp, http.StatusCreated)
		resp.Body.Close()
	}
	for table, want := range map[string]int{
		"product_sessions": 3,
		"harness_sessions": 0,
		"agent_runs":       0,
		"intake_sessions":  0,
	} {
		var count int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != want { // #nosec G202 -- table names are fixed test constants.
			t.Fatalf("%s rows=%d err=%v, want %d", table, count, err, want)
		}
	}

	attachURL := fmt.Sprintf("%s/%s/attach-node", base, session.ProductSessionID)
	resp := ts.post(t, attachURL, ts.adminCookie, map[string]any{"node_id": nodeA, "expected_revision": 1})
	assertStatus(t, resp, http.StatusOK)
	decode(t, resp, &session)
	if session.NodeID == nil || *session.NodeID != nodeA || session.Revision != 2 {
		t.Fatalf("attach result=%+v", session)
	}

	stale := ts.post(t, attachURL, ts.adminCookie, map[string]any{"node_id": nodeB, "expected_revision": 1})
	assertStatus(t, stale, http.StatusConflict)
	stale.Body.Close()
	get := ts.get(t, fmt.Sprintf("%s/%s", base, session.ProductSessionID), ts.adminCookie)
	assertStatus(t, get, http.StatusOK)
	decode(t, get, &session)
	if session.NodeID == nil || *session.NodeID != nodeA || session.Revision != 2 {
		t.Fatalf("stale CAS changed session: %+v", session)
	}

	reattach := ts.post(t, attachURL, ts.adminCookie, map[string]any{"node_id": nodeB, "expected_revision": 2})
	assertStatus(t, reattach, http.StatusOK)
	decode(t, reattach, &session)
	if session.NodeID == nil || *session.NodeID != nodeB || session.Revision != 3 {
		t.Fatalf("reattach result=%+v", session)
	}
	detach := ts.post(t, fmt.Sprintf("%s/%s/detach-node", base, session.ProductSessionID), ts.adminCookie,
		map[string]any{"expected_revision": 3})
	assertStatus(t, detach, http.StatusOK)
	decode(t, detach, &session)
	if session.NodeID != nil || session.Revision != 4 {
		t.Fatalf("detach result=%+v", session)
	}
	eventsResp := ts.get(t, fmt.Sprintf("%s/%s/events", base, session.ProductSessionID), ts.adminCookie)
	assertStatus(t, eventsResp, http.StatusOK)
	var events []productSessionEventResponse
	decode(t, eventsResp, &events)
	if len(events) != 4 || events[0].Operation != "create" || events[1].Operation != "attach" ||
		events[2].Operation != "reattach" || events[3].Operation != "detach" ||
		events[3].BeforeNodeID == nil || *events[3].BeforeNodeID != nodeB || events[3].AfterNodeID != nil ||
		events[3].BeforeRevision != 3 || events[3].AfterRevision != 4 || events[3].ActorUserID != adminID {
		t.Fatalf("unexpected immutable lifecycle evidence: %+v", events)
	}
	if _, err := db.DB.Exec(`UPDATE product_session_events SET operation='attach' WHERE product_session_id=? AND event_sequence=4`, session.ProductSessionID); err == nil {
		t.Fatal("immutable event accepted an update")
	}

	for name, body := range map[string]map[string]any{
		"cross-project agent": {"target_kind": "project_agent", "target_project_agent_id": otherAgentID, "title": "Wrong agent"},
		"cross-project node":  {"target_kind": "paimos", "node_id": otherNode, "title": "Wrong node"},
	} {
		t.Run(name, func(t *testing.T) {
			resp := ts.post(t, base, ts.adminCookie, body)
			assertStatus(t, resp, http.StatusUnprocessableEntity)
			resp.Body.Close()
		})
	}

	// Viewers can inspect the resource but cannot create or attach it.
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level)
		VALUES(?,?,'viewer') ON CONFLICT(user_id,project_id) DO UPDATE SET access_level='viewer'`, memberID, projectID); err != nil {
		t.Fatal(err)
	}
	list := ts.get(t, base, ts.memberCookie)
	assertStatus(t, list, http.StatusOK)
	list.Body.Close()
	forbidden := ts.post(t, base, ts.memberCookie, map[string]any{"target_kind": "paimos", "title": "No write"})
	assertStatus(t, forbidden, http.StatusForbidden)
	forbidden.Body.Close()
}

func TestConcurrentProductSessionCASCommitsOneActorNodeRevisionAndEvent(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Concurrent product session", "CPS")
	nodeA := seedSessionNode(t, projectID, 1, "ticket")
	nodeB := seedSessionNode(t, projectID, 2, "ticket")
	var memberID, adminID int64
	_ = db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID)
	_ = db.DB.QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&adminID)
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')
		ON CONFLICT(user_id,project_id) DO UPDATE SET access_level='editor'`, memberID, projectID); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/api/projects/%d/product-sessions", projectID)
	created := ts.post(t, base, ts.adminCookie, map[string]any{"target_kind": "paimos", "title": "Concurrent CAS"})
	assertStatus(t, created, http.StatusCreated)
	var session productSessionResponse
	decode(t, created, &session)
	attachURL := fmt.Sprintf("%s/%s/attach-node", base, session.ProductSessionID)

	type outcome struct {
		status int
		body   productSessionResponse
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, attempt := range []struct {
		nodeID int64
		cookie string
	}{{nodeA, ts.adminCookie}, {nodeB, ts.memberCookie}} {
		wg.Add(1)
		go func(nodeID int64, cookie string) {
			defer wg.Done()
			<-start
			payload := strings.NewReader(fmt.Sprintf(`{"node_id":%d,"expected_revision":1}`, nodeID))
			req, _ := http.NewRequest(http.MethodPost, ts.srv.URL+attachURL, payload)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", cookie)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- outcome{status: 0}
				return
			}
			defer resp.Body.Close()
			var body productSessionResponse
			_ = json.NewDecoder(resp.Body).Decode(&body)
			results <- outcome{status: resp.StatusCode, body: body}
		}(attempt.nodeID, attempt.cookie)
	}
	close(start)
	wg.Wait()
	close(results)
	var winner productSessionResponse
	statuses := map[int]int{}
	for result := range results {
		statuses[result.status]++
		if result.status == http.StatusOK {
			winner = result.body
		}
	}
	if statuses[http.StatusOK] != 1 || statuses[http.StatusConflict] != 1 || winner.Revision != 2 || winner.NodeID == nil {
		t.Fatalf("CAS statuses=%v winner=%+v, want one success and one conflict", statuses, winner)
	}
	var storedNode, revision, updatedBy int64
	if err := db.DB.QueryRow(`SELECT node_id,revision,updated_by_user_id FROM product_sessions WHERE product_session_id=?`, session.ProductSessionID).
		Scan(&storedNode, &revision, &updatedBy); err != nil || storedNode != *winner.NodeID || revision != 2 {
		t.Fatalf("durable winner node=%d revision=%d actor=%d err=%v response=%+v", storedNode, revision, updatedBy, err, winner)
	}
	wantActor := adminID
	if storedNode == nodeB {
		wantActor = memberID
	}
	var eventCount, eventActor, eventNode int64
	if err := db.DB.QueryRow(`SELECT COUNT(*),MAX(actor_user_id),MAX(after_node_id) FROM product_session_events
		WHERE product_session_id=? AND event_sequence=2`, session.ProductSessionID).Scan(&eventCount, &eventActor, &eventNode); err != nil ||
		eventCount != 1 || eventActor != wantActor || eventNode != storedNode || updatedBy != wantActor {
		t.Fatalf("winning event count=%d actor=%d node=%d updatedBy=%d wantActor=%d err=%v", eventCount, eventActor, eventNode, updatedBy, wantActor, err)
	}
}

func TestAttachedProductSessionRejectsMoveDeleteAndQualifiedReadCannotLeak(t *testing.T) {
	ts := newTestServer(t)
	oldProject := seedBatchProject(t, "Old project", "OLD")
	newProject := seedBatchProject(t, "Secret project", "SEC")
	nodeID := seedSessionNode(t, oldProject, 1, "ticket")
	if _, err := db.DB.Exec(`UPDATE issues SET title='Old visible node' WHERE id=?`, nodeID); err != nil {
		t.Fatal(err)
	}
	var memberID int64
	_ = db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID)
	_, _ = db.DB.Exec(`DELETE FROM project_members WHERE user_id=?`, memberID)
	_, _ = db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'viewer')`, memberID, oldProject)
	base := fmt.Sprintf("/api/projects/%d/product-sessions", oldProject)
	created := ts.post(t, base, ts.adminCookie, map[string]any{"target_kind": "paimos", "node_id": nodeID, "title": "Bound"})
	assertStatus(t, created, http.StatusCreated)
	var session productSessionResponse
	decode(t, created, &session)
	move := ts.post(t, fmt.Sprintf("/api/issues/%d/move", nodeID), ts.adminCookie, map[string]any{"project_id": newProject})
	assertStatus(t, move, http.StatusConflict)
	move.Body.Close()
	deleted := ts.del(t, fmt.Sprintf("/api/issues/%d", nodeID), ts.adminCookie)
	assertStatus(t, deleted, http.StatusConflict)
	deleted.Body.Close()

	// Simulate legacy corruption after separately proving the DB lifecycle
	// trigger: the read join must still be project-qualified defense in depth.
	if _, err := db.DB.Exec(`DROP TRIGGER trg_issues_attached_session_move`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE issues SET project_id=?,title='Secret node title' WHERE id=?`, newProject, nodeID); err != nil {
		t.Fatal(err)
	}
	read := ts.get(t, fmt.Sprintf("%s/%s", base, session.ProductSessionID), ts.memberCookie)
	assertStatus(t, read, http.StatusOK)
	var raw struct {
		Node any `json:"node"`
	}
	decode(t, read, &raw)
	if raw.Node != nil {
		t.Fatalf("old-project-only viewer received cross-project node projection: %#v", raw.Node)
	}
}

func TestProductSessionAgentDriftRejectedAndQualifiedReadConcealsName(t *testing.T) {
	ts := newTestServer(t)
	oldProject := seedBatchProject(t, "Agent owner", "AGO")
	newProject := seedBatchProject(t, "Agent secret", "AGS")
	agent, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'Secret rehomed agent')`, oldProject)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agent.LastInsertId()
	var memberID int64
	_ = db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID)
	_, _ = db.DB.Exec(`DELETE FROM project_members WHERE user_id=?`, memberID)
	_, _ = db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'viewer')`, memberID, oldProject)
	base := fmt.Sprintf("/api/projects/%d/product-sessions", oldProject)
	created := ts.post(t, base, ts.adminCookie, map[string]any{
		"target_kind": "project_agent", "target_project_agent_id": agentID, "title": "Agent bound",
	})
	assertStatus(t, created, http.StatusCreated)
	var session productSessionResponse
	decode(t, created, &session)

	if _, err := db.DB.Exec(`UPDATE project_agents SET project_id=? WHERE id=?`, newProject, agentID); err == nil ||
		!strings.Contains(err.Error(), "product session target prevents agent project move") {
		t.Fatalf("referenced agent drift error=%v, want database rejection", err)
	}
	if _, err := db.DB.Exec(`DROP TRIGGER trg_project_agents_product_session_move`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE project_agents SET project_id=? WHERE id=?`, newProject, agentID); err != nil {
		t.Fatal(err)
	}
	read := ts.get(t, fmt.Sprintf("%s/%s", base, session.ProductSessionID), ts.memberCookie)
	assertStatus(t, read, http.StatusOK)
	var raw map[string]json.RawMessage
	decode(t, read, &raw)
	encoded, _ := json.Marshal(raw)
	if strings.Contains(string(encoded), "Secret rehomed agent") {
		t.Fatalf("old-project-only viewer received rehomed agent name: %s", encoded)
	}
	if err := json.Unmarshal(encoded, &session); err != nil || session.TargetAgentName != "" {
		t.Fatalf("drifted target concealment session=%+v err=%v", session, err)
	}
}

func TestNodeLabelDefaultsProjectPrecedenceAndLegacyTypePreserved(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Configured labels", "LBL")
	nodeID := seedSessionNode(t, projectID, 1, "epic")
	global := map[string]string{
		"epic": "Initiative", "task": "Task", "ticket": "Ticket", "cost_unit": "Cost unit", "release": "Release",
		"sprint": "Sprint", "memory": "Memory", "runbook": "Runbook", "external_system": "External system",
		"related_project": "Related project", "guideline": "Guideline",
	}
	putGlobal := ts.put(t, "/api/node-labels", ts.adminCookie, global)
	assertStatus(t, putGlobal, http.StatusOK)
	putGlobal.Body.Close()
	forbiddenGlobal := ts.put(t, "/api/node-labels", ts.memberCookie, global)
	assertStatus(t, forbiddenGlobal, http.StatusForbidden)
	forbiddenGlobal.Body.Close()
	incompleteGlobal := ts.put(t, "/api/node-labels", ts.adminCookie, map[string]string{"epic": "Only one"})
	assertStatus(t, incompleteGlobal, http.StatusBadRequest)
	incompleteGlobal.Body.Close()
	projectConfig := ts.put(t, fmt.Sprintf("/api/projects/%d/node-labels", projectID), ts.adminCookie, map[string]string{"epic": "Program"})
	assertStatus(t, projectConfig, http.StatusOK)
	var config struct {
		Precedence       string            `json:"precedence"`
		GlobalDefaults   map[string]string `json:"global_defaults"`
		ProjectOverrides map[string]string `json:"project_overrides"`
		Resolved         map[string]string `json:"resolved"`
	}
	decode(t, projectConfig, &config)
	if config.Precedence != "project_override_then_global_default" || config.GlobalDefaults["epic"] != "Initiative" ||
		config.ProjectOverrides["epic"] != "Program" || config.Resolved["epic"] != "Program" || config.Resolved["ticket"] != "Ticket" {
		t.Fatalf("label precedence contract=%+v", config)
	}
	node := ts.get(t, fmt.Sprintf("/api/projects/%d/nodes/%d", projectID, nodeID), ts.adminCookie)
	assertStatus(t, node, http.StatusOK)
	var rendered nodeResponse
	decode(t, node, &rendered)
	var storedType string
	_ = db.DB.QueryRow(`SELECT type FROM issues WHERE id=?`, nodeID).Scan(&storedType)
	if rendered.CosmeticTypeLabel != "Program" || storedType != "epic" {
		t.Fatalf("rendered label=%q stored issue type=%q", rendered.CosmeticTypeLabel, storedType)
	}
	invalid := ts.put(t, fmt.Sprintf("/api/projects/%d/node-labels", projectID), ts.adminCookie, map[string]string{"epic": " bad\nlabel"})
	assertStatus(t, invalid, http.StatusBadRequest)
	invalid.Body.Close()
}

func TestInvalidReparentPreservesOldParentEdge(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Parent transaction", "PTX")
	otherProjectID := seedBatchProject(t, "Other parent", "POX")
	oldParent := seedSessionNode(t, projectID, 1, "epic")
	child := seedSessionNode(t, projectID, 2, "ticket")
	invalidParent := seedSessionNode(t, otherProjectID, 1, "epic")
	if _, err := db.DB.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, oldParent, child); err != nil {
		t.Fatal(err)
	}
	resp := ts.put(t, fmt.Sprintf("/api/projects/%d/nodes/%d/parent", projectID, child), ts.adminCookie, map[string]any{"parent_node_id": invalidParent})
	assertStatus(t, resp, http.StatusUnprocessableEntity)
	resp.Body.Close()
	var durableParent int64
	if err := db.DB.QueryRow(`SELECT source_id FROM issue_relations WHERE target_id=? AND type='parent'`, child).Scan(&durableParent); err != nil || durableParent != oldParent {
		t.Fatalf("invalid reparent lost old edge: parent=%d err=%v want=%d", durableParent, err, oldParent)
	}
}

type nodeResponse struct {
	NodeID            int64  `json:"node_id"`
	ProjectID         int64  `json:"project_id"`
	Kind              string `json:"kind"`
	CosmeticTypeLabel string `json:"cosmetic_type_label"`
	ParentNodeID      *int64 `json:"parent_node_id"`
}

func TestNodeCompatibilityAPIAndProjectDepth(t *testing.T) {
	ts := newTestServer(t)
	nestedProject := seedBatchProject(t, "Nested nodes", "NOD")
	epicID := seedSessionNode(t, nestedProject, 1, "epic")
	base := fmt.Sprintf("/api/projects/%d/nodes", nestedProject)

	legacy := ts.get(t, fmt.Sprintf("%s/%d", base, epicID), ts.adminCookie)
	assertStatus(t, legacy, http.StatusOK)
	var raw map[string]json.RawMessage
	decode(t, legacy, &raw)
	if _, exposesLegacyType := raw["type"]; exposesLegacyType {
		t.Fatal("node DTO exposes the legacy issue type discriminator")
	}
	var epic nodeResponse
	encoded, _ := json.Marshal(raw)
	_ = json.Unmarshal(encoded, &epic)
	if epic.Kind != "node" || epic.CosmeticTypeLabel != "Epic" {
		t.Fatalf("legacy issue node view=%+v", epic)
	}

	created := ts.post(t, base, ts.adminCookie, map[string]any{
		"title": "Child without a type choice", "parent_node_id": epicID,
	})
	assertStatus(t, created, http.StatusCreated)
	var child nodeResponse
	decode(t, created, &child)
	if child.Kind != "node" || child.CosmeticTypeLabel != "Ticket" || child.ParentNodeID == nil || *child.ParentNodeID != epicID {
		t.Fatalf("created node=%+v", child)
	}
	var storedType string
	if err := db.DB.QueryRow(`SELECT type FROM issues WHERE id=?`, child.NodeID).Scan(&storedType); err != nil || storedType != "ticket" {
		t.Fatalf("underlying compatibility type=%q err=%v, want ticket", storedType, err)
	}

	project := ts.get(t, fmt.Sprintf("/api/projects/%d", nestedProject), ts.adminCookie)
	assertStatus(t, project, http.StatusOK)
	var projectBody struct {
		NodeDepth string `json:"node_depth"`
	}
	decode(t, project, &projectBody)
	if projectBody.NodeDepth != "nested" {
		t.Fatalf("existing project node_depth=%q, want nested", projectBody.NodeDepth)
	}
	conflict := ts.put(t, fmt.Sprintf("/api/projects/%d", nestedProject), ts.adminCookie, map[string]any{"node_depth": "1"})
	assertStatus(t, conflict, http.StatusConflict)
	conflict.Body.Close()

	flatProject := ts.post(t, "/api/projects", ts.adminCookie, map[string]any{
		"name": "Flat nodes", "key": "FLT", "node_depth": "1",
	})
	assertStatus(t, flatProject, http.StatusCreated)
	var createdProject struct {
		ID        int64  `json:"id"`
		NodeDepth string `json:"node_depth"`
	}
	decode(t, flatProject, &createdProject)
	if createdProject.ID <= 0 || createdProject.NodeDepth != "1" {
		t.Fatalf("created flat project=%+v", createdProject)
	}
	depthOneProject := createdProject.ID
	flatBase := fmt.Sprintf("/api/projects/%d/nodes", depthOneProject)
	root := ts.post(t, flatBase, ts.adminCookie, map[string]any{"title": "Root"})
	assertStatus(t, root, http.StatusCreated)
	var rootNode nodeResponse
	decode(t, root, &rootNode)
	childRejected := ts.post(t, flatBase, ts.adminCookie, map[string]any{"title": "Forbidden child", "parent_node_id": rootNode.NodeID})
	assertStatus(t, childRejected, http.StatusUnprocessableEntity)
	childRejected.Body.Close()
	legacyRejected := ts.post(t, fmt.Sprintf("/api/projects/%d/issues", depthOneProject), ts.adminCookie,
		map[string]any{"title": "Legacy forbidden child", "type": "ticket", "parent_id": rootNode.NodeID})
	assertStatus(t, legacyRejected, http.StatusUnprocessableEntity)
	legacyRejected.Body.Close()
	var forbiddenCount int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM issues WHERE project_id=? AND title LIKE '%forbidden child'`, depthOneProject).Scan(&forbiddenCount); err != nil || forbiddenCount != 0 {
		t.Fatalf("rejected parent writes committed %d issue rows, err=%v", forbiddenCount, err)
	}
}

func TestConcurrentOpposingParentWritesCannotCreateCycle(t *testing.T) {
	_ = newTestServer(t)
	projectID := seedBatchProject(t, "Concurrent parents", "CPR")
	a := seedSessionNode(t, projectID, 1, "ticket")
	b := seedSessionNode(t, projectID, 2, "ticket")

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, edge := range [][2]int64{{a, b}, {b, a}} {
		wg.Add(1)
		go func(parent, child int64) {
			defer wg.Done()
			<-start
			tx, err := db.DB.BeginTx(context.Background(), &sql.TxOptions{})
			if err == nil {
				_, err = tx.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, parent, child)
			}
			if err == nil {
				err = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			errs <- err
		}(edge[0], edge[1])
	}
	close(start)
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("opposing concurrent parent writes successes=%d, want exactly 1", successes)
	}
	var edgeCount int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM issue_relations WHERE type='parent' AND source_id IN (?,?) AND target_id IN (?,?)`, a, b, a, b).Scan(&edgeCount); err != nil || edgeCount != 1 {
		t.Fatalf("committed opposing edges=%d err=%v, want 1", edgeCount, err)
	}
}

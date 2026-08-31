package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

func seedSessionHomeAgent(t *testing.T, projectID int64, name string) int64 {
	t.Helper()
	result, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func seedSessionHomeProductSession(t *testing.T, projectID int64, targetKind string, agentID, nodeID *int64, title, updatedAt string) string {
	t.Helper()
	var actorID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	_, err := db.DB.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,target_project_agent_id,node_id,title,summary,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?, ?,?,?,?)`, id, projectID, targetKind, agentID, nodeID, title, "Summary for "+title,
		actorID, actorID, updatedAt, updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func seedSessionHomeHarness(t *testing.T, projectID, agentID int64, agentName, harness, management, phase string, caps models.HarnessCapabilities, fresh bool) string {
	t.Helper()
	id := uuid.NewString()
	heartbeat := "strftime('%Y-%m-%dT%H:%M:%fZ','now')"
	if !fresh {
		heartbeat = "strftime('%Y-%m-%dT%H:%M:%fZ','now','-91 seconds')"
	}
	query := fmt.Sprintf(`INSERT INTO harness_sessions(
		id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,management_mode,role,steer_mode,
		advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase,heartbeat_at,updated_at)
		VALUES(?,?,?,?,?,'test-host-'||?,randomblob(32),?,'worker',?, ?,?,?,?,?,?,%s,%s)`, heartbeat, heartbeat)
	steerMode := "none"
	if caps.Steer && management == "managed" {
		steerMode = "owned"
	} else if caps.Steer {
		steerMode = "codex_external"
	}
	_, err := db.DB.Exec(query, id, projectID, agentID, agentName, harness, harness, management, steerMode,
		caps.Inbox, caps.Status, caps.Steer, caps.Interrupt, caps.Stop, phase)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func seedSessionHomeMessage(t *testing.T, projectID, fromID, toID int64, toAddress string, delivered, action bool, body string) {
	t.Helper()
	var projectKey, fromName string
	if err := db.DB.QueryRow(`SELECT key FROM projects WHERE id=?`, projectID).Scan(&projectKey); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT name FROM project_agents WHERE id=? AND project_id=?`, fromID, projectID).Scan(&fromName); err != nil {
		t.Fatal(err)
	}
	heldReason := ""
	var deliveredAt any
	if delivered {
		deliveredAt = "2026-08-30T11:58:00Z"
	} else if action {
		heldReason = "action request - requires human approval"
	} else {
		heldReason = "sender not in receiver allowlist"
	}
	messageID := uuid.NewString()
	_, err := db.DB.Exec(`INSERT INTO agent_messages(
		from_agent_id,to_agent_id,body,is_action_request,delivered,held_reason,delivered_at,message_id,context_id,
		parts_json,metadata_json,from_address,to_address,thread_id,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,json_array(json_object('kind','text','text',?)),'{}',?,?,?,?)`,
		fromID, toID, body, action, delivered, heldReason, deliveredAt, messageID, projectKey, body,
		"paimos:"+fromName, toAddress, messageID, "2026-08-30T11:58:00Z")
	if err != nil {
		t.Fatal(err)
	}
}

func getSessionHome(t *testing.T, ts *testServer, projectID int64, cookie string) (*http.Response, models.SessionHomeSnapshot) {
	t.Helper()
	response := ts.get(t, fmt.Sprintf("/api/projects/%d/session-home/v1", projectID), cookie)
	var snapshot models.SessionHomeSnapshot
	if response.StatusCode == http.StatusOK {
		decode(t, response, &snapshot)
	}
	return response, snapshot
}

func TestSessionHomeV1ExactShapeOrderingEmptyLooseAndManyPerNode(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Session home", "SHM")

	emptyResponse, empty := getSessionHome(t, ts, projectID, ts.adminCookie)
	assertStatus(t, emptyResponse, http.StatusOK)
	if emptyResponse.Header.Get("Cache-Control") != "private, no-store" || empty.SchemaVersion != 1 || empty.ProjectID != projectID ||
		empty.Sessions == nil || len(empty.Sessions) != 0 || empty.Totals != (models.SessionHomeTotals{}) {
		t.Fatalf("empty contract response=%+v cache=%q", empty, emptyResponse.Header.Get("Cache-Control"))
	}

	nodeID := seedSessionNode(t, projectID, 1, "ticket")
	if _, err := db.DB.Exec(`UPDATE issues SET title='Shared node' WHERE id=?`, nodeID); err != nil {
		t.Fatal(err)
	}
	olderID := seedSessionHomeProductSession(t, projectID, "paimos", nil, &nodeID, "Older attached", "2026-08-30T10:00:00.000Z")
	newerID := seedSessionHomeProductSession(t, projectID, "paimos", nil, &nodeID, "Newer attached", "2026-08-30T12:00:00.000Z")
	looseID := seedSessionHomeProductSession(t, projectID, "paimos", nil, nil, "Loose", "2026-08-30T11:00:00.000Z")

	response, snapshot := getSessionHome(t, ts, projectID, ts.adminCookie)
	assertStatus(t, response, http.StatusOK)
	if len(snapshot.Sessions) != 3 || snapshot.Sessions[0].ProductSessionID != newerID ||
		snapshot.Sessions[1].ProductSessionID != looseID || snapshot.Sessions[2].ProductSessionID != olderID {
		t.Fatalf("deterministic session ordering=%+v", snapshot.Sessions)
	}
	if snapshot.Sessions[0].Node == nil || snapshot.Sessions[2].Node == nil ||
		snapshot.Sessions[0].Node.NodeID != nodeID || snapshot.Sessions[2].Node.NodeID != nodeID || snapshot.Sessions[1].Node != nil {
		t.Fatalf("loose/many-per-node projection=%+v", snapshot.Sessions)
	}
	for _, item := range snapshot.Sessions {
		if item.Target.Kind != "paimos" || item.Target.ProjectAgentID != nil || item.Target.AgentName != nil ||
			item.Target.Address == nil || *item.Target.Address != "paimos" || item.Harness != nil ||
			item.Status != (models.SessionHomeStatus{Phase: "paimos", Reason: "paimos_target"}) ||
			item.Controls != (models.SessionHomeControls{Steer: "paimos_nudge"}) {
			t.Fatalf("Paimos target invented agent/harness truth: %+v", item)
		}
	}

	raw := ts.get(t, fmt.Sprintf("/api/projects/%d/session-home/v1", projectID), ts.adminCookie)
	assertStatus(t, raw, http.StatusOK)
	var top map[string]json.RawMessage
	decode(t, raw, &top)
	if len(top) != 4 || top["schema_version"] == nil || top["project_id"] == nil || top["sessions"] == nil || top["totals"] == nil {
		t.Fatalf("unexpected top-level JSON shape: %v", top)
	}
}

func TestSessionHomeV1ComposesManagedUnmanagedInboxAttentionAndFailClosedHarnessTruth(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Harness truth", "HST")
	senderID := seedSessionHomeAgent(t, projectID, "sender")
	managedID := seedSessionHomeAgent(t, projectID, "managed")
	unmanagedID := seedSessionHomeAgent(t, projectID, "unmanaged")
	staleID := seedSessionHomeAgent(t, projectID, "stale")
	ambiguousID := seedSessionHomeAgent(t, projectID, "ambiguous")
	for i, target := range []int64{managedID, unmanagedID, staleID, ambiguousID} {
		seedSessionHomeProductSession(t, projectID, "project_agent", &target, nil, fmt.Sprintf("Agent %d", i), fmt.Sprintf("2026-08-30T10:0%d:00.000Z", i))
	}
	seedSessionHomeProductSession(t, projectID, "project_agent", &managedID, nil, "Second managed conversation", "2026-08-30T10:04:00.000Z")
	seedSessionHomeHarness(t, projectID, managedID, "managed", "codex", "managed", "working",
		models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true}, true)
	seedSessionHomeHarness(t, projectID, unmanagedID, "unmanaged", "codex", "unmanaged", "working",
		models.HarnessCapabilities{Inbox: true, Status: true}, true)
	seedSessionHomeHarness(t, projectID, staleID, "stale", "codex", "managed", "working",
		models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true}, false)
	seedSessionHomeHarness(t, projectID, ambiguousID, "ambiguous", "codex", "managed", "working",
		models.HarnessCapabilities{Inbox: true, Status: true}, true)
	seedSessionHomeHarness(t, projectID, ambiguousID, "ambiguous", "claude", "managed", "working",
		models.HarnessCapabilities{Inbox: true, Status: true}, true)

	seedSessionHomeMessage(t, projectID, senderID, managedID, "codex:managed", true, false, "Unread note")
	seedSessionHomeMessage(t, projectID, senderID, managedID, "codex:managed", false, false, "Held sender")
	seedSessionHomeMessage(t, projectID, senderID, managedID, "codex:managed", false, true, "Please approve this")

	response, snapshot := getSessionHome(t, ts, projectID, ts.adminCookie)
	assertStatus(t, response, http.StatusOK)
	byAgent := map[string]models.SessionHomeSession{}
	for _, item := range snapshot.Sessions {
		if item.Target.AgentName != nil {
			byAgent[*item.Target.AgentName] = item
		}
	}
	managed := byAgent["managed"]
	if managed.Harness == nil || managed.Harness.ManagementMode != "managed" || managed.Status.Phase != "working" ||
		managed.Controls != (models.SessionHomeControls{Steer: "direct", Interrupt: true, Stop: true}) ||
		managed.Inbox.UnreadCount != 1 || managed.Inbox.LatestUnreadAt == nil || !managed.Attention.Required ||
		managed.Attention.ExceptionCount != 2 || managed.Attention.ActionRequestCount != 1 || managed.Attention.Reason == nil || *managed.Attention.Reason != "action_request" {
		t.Fatalf("managed inbox/attention/capability composition=%+v", managed)
	}
	unmanaged := byAgent["unmanaged"]
	if unmanaged.Harness == nil || unmanaged.Harness.ManagementMode != "unmanaged" ||
		unmanaged.Controls != (models.SessionHomeControls{Steer: "paimos_nudge"}) ||
		unmanaged.Harness.Capabilities.Interrupt || unmanaged.Harness.Capabilities.Stop {
		t.Fatalf("unmanaged row advertised owned controls: %+v", unmanaged)
	}
	for name, reason := range map[string]string{"stale": "stale_harness", "ambiguous": "ambiguous_harness"} {
		item := byAgent[name]
		if item.Harness != nil || item.Target.Address != nil || item.Status != (models.SessionHomeStatus{Phase: "unavailable", Reason: reason}) ||
			item.Controls != (models.SessionHomeControls{Steer: "paimos_nudge"}) {
			t.Fatalf("%s harness did not fail closed: %+v", name, item)
		}
	}
	if snapshot.Totals.Sessions != 5 || snapshot.Totals.Unread != 1 || snapshot.Totals.Attention != 2 {
		t.Fatalf("totals=%+v", snapshot.Totals)
	}
}

func TestSessionHomeV1ConcealsDeniedProjectAndNeverJoinsForeignHarness(t *testing.T) {
	ts := newTestServer(t)
	visibleProject := seedBatchProject(t, "Visible", "VIS")
	foreignProject := seedBatchProject(t, "Foreign", "FOR")
	visibleAgent := seedSessionHomeAgent(t, visibleProject, "same-name")
	foreignAgent := seedSessionHomeAgent(t, foreignProject, "same-name")
	seedSessionHomeProductSession(t, visibleProject, "project_agent", &visibleAgent, nil, "Visible session", "2026-08-30T12:00:00.000Z")
	seedSessionHomeProductSession(t, foreignProject, "project_agent", &foreignAgent, nil, "SECRET FOREIGN TITLE", "2026-08-30T12:00:00.000Z")
	seedSessionHomeHarness(t, foreignProject, foreignAgent, "same-name", "foreign-secret-harness", "managed", "working",
		models.HarnessCapabilities{Inbox: true, Status: true, Steer: true}, true)

	allowed, snapshot := getSessionHome(t, ts, visibleProject, ts.memberCookie)
	assertStatus(t, allowed, http.StatusOK)
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "foreign-secret-harness") || strings.Contains(string(encoded), "SECRET FOREIGN TITLE") ||
		len(snapshot.Sessions) != 1 || snapshot.Sessions[0].Status.Reason != "no_active_harness" {
		t.Fatalf("foreign project truth leaked into visible projection: %s", encoded)
	}

	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'none')
		ON CONFLICT(user_id,project_id) DO UPDATE SET access_level='none'`, memberID, foreignProject); err != nil {
		t.Fatal(err)
	}
	denied := ts.get(t, fmt.Sprintf("/api/projects/%d/session-home/v1", foreignProject), ts.memberCookie)
	assertStatus(t, denied, http.StatusNotFound)
	defer denied.Body.Close()
	var problem map[string]any
	if err := json.NewDecoder(denied.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if denied.Header.Get("Cache-Control") != "private, no-store" || len(problem) != 1 || problem["error"] != "not found" {
		t.Fatalf("denied response leaked metadata: cache=%q body=%v", denied.Header.Get("Cache-Control"), problem)
	}
}

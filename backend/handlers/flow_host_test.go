package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/baselinebatch"
)

func TestFlowHostIssuesOpaqueLocalHostContext(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Flow stream", "key": "FLW"}))
	otherID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Other stream", "key": "OTH"}))

	unauth := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/flow-state", projectID), "")
	if unauth.StatusCode != 401 {
		t.Fatalf("unauth=%d %s", unauth.StatusCode, baselineReadBody(unauth))
	}
	optedOut := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/flow-state", projectID), ts.adminCookie)
	if optedOut.StatusCode != 404 {
		t.Fatalf("opted-out=%d %s", optedOut.StatusCode, baselineReadBody(optedOut))
	}

	optIn(t, ts, projectID)
	optIn(t, ts, otherID)
	outsider := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/flow-state", projectID), ts.externalCookie)
	if outsider.StatusCode != 404 && outsider.StatusCode != 403 {
		t.Fatalf("outsider=%d %s", outsider.StatusCode, baselineReadBody(outsider))
	}

	handover, _ := validHandover(t)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID), map[string]any{
		"handover": json.RawMessage(handover),
	})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}

	resp := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/flow-state", projectID), ts.adminCookie)
	if resp.StatusCode != 200 {
		t.Fatalf("flow-state=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	var state map[string]any
	decode(t, resp, &state)
	walkFlowForbidden(t, state)
	identity := state["identityContext"].(map[string]any)
	if identity["host_id"] != "paimos" || identity["principal_kind"] != "local_host" {
		t.Fatalf("identity host/kind %+v", identity)
	}
	if header, _ := state["header"].(map[string]any); fmt.Sprint(header["version"]) == "1.0.0" {
		t.Fatalf("frontend package placeholder leaked as app version: %+v", header)
	}
	if identity["organization_ref"] != nil {
		t.Fatalf("invented org %v", identity["organization_ref"])
	}
	if _, ok := identity["issuer_descriptor"]; ok {
		t.Fatalf("local_host claimed issuer")
	}
	prereq := state["prerequisites"].(map[string]any)
	reqGate := prereq["requirementsBaseline"].(map[string]any)
	if reqGate["status"] != "unknown" {
		t.Fatalf("imported claim treated as live gate: %+v", reqGate)
	}
	if prereq["pharosTarget"].(map[string]any)["status"] != "unknown" || prereq["janusGate"].(map[string]any)["status"] != "unknown" {
		t.Fatalf("invented downstream %+v", prereq)
	}

	other := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/flow-state", otherID), ts.adminCookie)
	var otherState map[string]any
	decode(t, other, &otherState)
	startWrong := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/flow-intents", otherID), map[string]any{
		"type":     "flow:start-intent",
		"identity": flowBinding(identity),
	})
	if startWrong.StatusCode != 409 {
		t.Fatalf("wrong project=%d %s", startWrong.StatusCode, baselineReadBody(startWrong))
	}
	var wrongBody map[string]any
	decode(t, startWrong, &wrongBody)
	if wrongBody["executed"] != false {
		t.Fatalf("wrong project executed: %+v", wrongBody)
	}

	start := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/flow-intents", projectID), map[string]any{
		"type":     "flow:start-intent",
		"identity": flowBinding(identity),
	})
	if start.StatusCode != 200 {
		t.Fatalf("start=%d %s", start.StatusCode, baselineReadBody(start))
	}
	var started map[string]any
	decode(t, start, &started)
	if started["executed"] != false || started["location"] != fmt.Sprintf("/projects/%d?tab=overview#baseline-batch", projectID) {
		t.Fatalf("start routed %+v", started)
	}
	if strings.Contains(fmt.Sprint(started["notice"]), "delivery succeeded") {
		t.Fatalf("start claimed success: %+v", started)
	}

	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/flow-intents", projectID), map[string]any{
		"type":     "flow:review-batch",
		"identity": flowBinding(identity),
	})
	if review.StatusCode != 200 {
		t.Fatalf("review=%d %s", review.StatusCode, baselineReadBody(review))
	}

	key := mintHTTPAPIKey(t, ts, ts.adminCookie, "flow-bot", []string{auth.ScopeAll})
	agentState := ts.getBearer(t, fmt.Sprintf("/api/projects/%d/baseline-batches/flow-state", projectID), key)
	if agentState.StatusCode != 200 {
		t.Fatalf("agent state=%d %s", agentState.StatusCode, baselineReadBody(agentState))
	}
	var agent map[string]any
	decode(t, agentState, &agent)
	agentIdentity := agent["identityContext"].(map[string]any)
	if agentIdentity["actor_kind"] != "agent" {
		t.Fatalf("actor_kind=%v", agentIdentity["actor_kind"])
	}
	agentStart := postBearerFlowIntent(t, ts, fmt.Sprintf("/api/projects/%d/baseline-batches/flow-intents", projectID), key, map[string]any{
		"type":     "flow:start-intent",
		"identity": flowBinding(agentIdentity),
	})
	if agentStart.StatusCode != 403 {
		t.Fatalf("agent start=%d %s", agentStart.StatusCode, baselineReadBody(agentStart))
	}
	var denied map[string]any
	decode(t, agentStart, &denied)
	if denied["executed"] != false {
		t.Fatalf("agent executed %+v", denied)
	}

	afterReview := workflowOf(t, ts, projectID)
	if afterReview.ActiveBatch != nil {
		t.Fatalf("flow start created a batch: %+v", afterReview.ActiveBatch)
	}
	_ = otherState
	_ = baselinebatch.ImportedClaimAuthenticity
}

func flowBinding(identity map[string]any) map[string]any {
	return map[string]any{
		"status":          "present",
		"principalRef":    identity["principal_ref"],
		"projectRef":      identity["project_ref"],
		"actorKind":       identity["actor_kind"],
		"bindingRef":      identity["binding_ref"],
		"contextRevision": identity["context_revision"],
		"issuedAt":        identity["issued_at"],
		"expiresAt":       identity["expires_at"],
		"freshUntil":      identity["fresh_until"],
	}
}

func postBearerFlowIntent(t *testing.T, ts *testServer, path, token string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func walkFlowForbidden(t *testing.T, value any) {
	t.Helper()
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			walkFlowForbidden(t, child)
		}
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			if lower == "token" || lower == "secret" || lower == "cookie" || lower == "email" ||
				lower == "session" || lower == "subject" || lower == "sub" || lower == "role" ||
				lower == "roles" || strings.Contains(lower, "token") || strings.Contains(lower, "email") {
				t.Fatalf("forbidden key %s", key)
			}
			walkFlowForbidden(t, child)
		}
	case string:
		if strings.Contains(typed, "@") || strings.Contains(typed, "party:forged") ||
			strings.Count(typed, ".") >= 2 && strings.Contains(typed, "eyJ") {
			t.Fatalf("forbidden string %q", typed)
		}
	}
}

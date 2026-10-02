// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/internal/questions"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAskCLIAndMCPParity(t *testing.T) {
	isolate(t)
	const project = "a0000000-0000-4000-8000-000000000001"
	const sessionID = "a0000000-0000-4000-8000-000000000002"
	const requestID = "a0000000-0000-4000-8000-000000000003"
	const questionID = "a0000000-0000-4000-8000-000000000004"
	const ticketID = "a0000000-0000-4000-8000-000000000005"
	in := questions.Input{RequestID: requestID, Question: "Which option?", Context: "The context", Options: []questions.Option{{ID: "1", Title: "Local", Description: "Simple", Answer: "Use local."}}, Recommend: "1", Why: "Small", Meanwhile: "parked", MeanwhileText: "Working on docs", SuggestedOutcome: "once", TicketID: ticketID, SessionID: sessionID, AnywayReason: "New evidence"}
	var mu sync.Mutex
	var inputs []questions.Input
	statusCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Error("missing configured authorization")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/projects/" + project + "/questions":
			var got questions.Input
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			inputs = append(inputs, got)
			mu.Unlock()
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(questions.Question{ID: questionID, ProjectID: project, State: "open", Revision: 1, Input: got, Askers: []questions.Asker{{ID: questionID, RequestID: got.RequestID, CommentNodeID: ticketID, ReplyRootID: requestID, Input: got}}, Pending: []questions.Pending{}})
		case "GET /api/questions/" + questionID + "/status":
			mu.Lock()
			statusCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(questions.Question{ID: questionID, ProjectID: project, State: "open", Revision: 1, Askers: []questions.Asker{}, Pending: []questions.Pending{}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, out, errOut := runCLI([]string{"aeon", "ask", "--project", project, "--ticket", ticketID, "Which option?", "--context-file", "-", "--option", `["Local","Simple","Use local."]`, "--recommend", "1", "--why", "Small", "--meanwhile", "parked", "--meanwhile-text", "Working on docs", "--keep", "once", "--anyway", "New evidence", "--request-id", requestID, "--session", sessionID, "--json"}, "The context")
	assertNoSecret(t, out+errOut)
	if code != 0 {
		t.Fatalf("ask: %d %s", code, errOut)
	}
	if !strings.Contains(out, questionID) || !strings.Contains(errOut, requestID) {
		t.Fatal("missing durable question or retry ID")
	}
	rt := &runtime{program: "aeon", stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	left, right := mcp.NewInMemoryTransports()
	server, err := rt.mcpServer().Connect(t.Context(), left, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "ask-test", Version: "dev"}, nil).Connect(t.Context(), right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "ask", Arguments: askToolArgs{Project: project, Input: in}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(toolText(result), questionID) {
		t.Fatalf("ask tool: %s", toolText(result))
	}
	if len(inputs) != 2 || !reflect.DeepEqual(inputs[0], inputs[1]) || !reflect.DeepEqual(inputs[0], in) {
		t.Fatalf("transport inputs differ: %+v", inputs)
	}
	code, out, errOut = runCLI([]string{"paimos", "ask", "status", questionID, "--json"}, "")
	if code != 0 {
		t.Fatalf("status: %s", errOut)
	}
	assertNoSecret(t, out+errOut)
	result, err = client.CallTool(t.Context(), &mcp.CallToolParams{Name: "ask_status", Arguments: askStatusToolArgs{QuestionID: questionID}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || statusCalls != 2 {
		t.Fatal("status tool did not perform the same read")
	}
	// Required request IDs and typed options are enforced by the functional tool.
	bad := in
	bad.RequestID = ""
	result, err = client.CallTool(t.Context(), &mcp.CallToolParams{Name: "ask", Arguments: askToolArgs{Project: project, Input: bad}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(inputs) != 2 {
		t.Fatal("invalid MCP ask reached HTTP")
	}
}
func TestAskOptionAndContextBounds(t *testing.T) {
	isolate(t)
	for _, raw := range []string{`["one","two"]`, `{"id":"a","title":"X","description":"","answer":"Y","principal_id":"forged"}`, `{"id":"a"} {}`, `not json`} {
		if _, err := parseQuestionOption(raw, 1); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	code, _, errOut := runCLI([]string{"aeon", "ask", "--project", "AEON", "Question", "--option", `["A","B","C"]`, "--context-file", "-"}, strings.Repeat("x", 16001))
	if code == 0 || !strings.Contains(errOut, "context exceeds") {
		t.Fatal("context not bounded before network")
	}
}

func TestQuestionStatusExplainsFailedEffectsAndPendingDoctrine(t *testing.T) {
	out := &bytes.Buffer{}
	rt := &runtime{stdout: out}
	q := questions.Question{ID: "question", Answer: &questions.Answer{Outcome: "doctrine"}, Pending: []questions.Pending{{Kind: "outcome", State: "delivered", EffectRef: "draft-reference", DoctrineState: "pending"}}}
	if err := rt.printQuestion(q); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "draft saved; waiting for a person") || strings.Contains(out.String(), "outcome: delivered") {
		t.Fatalf("misleading doctrine result: %s", out)
	}
	out.Reset()
	q.Pending = []questions.Pending{{Kind: "outcome", State: "failed", ErrorCode: "ticket_revision_conflict", ErrorMessage: "Review the changed ticket before deciding again."}}
	if err := rt.printQuestion(q); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "failed: ticket_revision_conflict") || !strings.Contains(out.String(), "Review the changed ticket") {
		t.Fatalf("missing failure explanation: %s", out)
	}
}

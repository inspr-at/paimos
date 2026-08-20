package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/cmd/paimos-mcp/mcpclient"
)

func TestToolsCallRoundTripRetrieveAndBlastRadius(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":7,"key":"PAI"}]`))
	})
	mux.HandleFunc("/api/projects/7/retrieve", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hits":[{"entity_type":"symbol","entity_id":42,"title":"function RetrieveProjectContext"}],"strategy":{"fusion":"rrf"}}`))
	})
	mux.HandleFunc("/api/projects/7/graph/blast-radius", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"root":{"entity_type":"issue","entity_id":11},"reached":{"symbol":[{"id":42,"title":"RetrieveProjectContext"}]}}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := testMCPClient(t, ts.URL)
	srv := &Server{
		client: client,
		logger: func(string, ...any) {},
	}

	retrieveReq := rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params: mustJSON(t, map[string]any{
			"name": "paimos_retrieve",
			"arguments": map[string]any{
				"project": "PAI",
				"q":       "retrieve symbol context",
				"k":       5,
			},
		}),
	}
	retrieveResp := srv.dispatch(&retrieveReq)
	if retrieveResp.Error != nil {
		t.Fatalf("retrieve error: %#v", retrieveResp.Error)
	}
	retrieveText := extractToolText(t, retrieveResp.Result)
	if !strings.Contains(retrieveText, `"fusion":"rrf"`) {
		t.Fatalf("retrieve result missing rrf payload: %s", retrieveText)
	}

	blastReq := rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/call",
		Params: mustJSON(t, map[string]any{
			"name": "paimos_blast_radius",
			"arguments": map[string]any{
				"project": "PAI",
				"issue":   "PAI-79",
				"depth":   3,
			},
		}),
	}
	blastResp := srv.dispatch(&blastReq)
	if blastResp.Error != nil {
		t.Fatalf("blast radius error: %#v", blastResp.Error)
	}
	blastText := extractToolText(t, blastResp.Result)
	if !strings.Contains(blastText, `"symbol"`) {
		t.Fatalf("blast radius result missing symbol payload: %s", blastText)
	}
}

func TestToolReportProgressForwardsOnlyAllowlistedRunFacts(t *testing.T) {
	var received map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/runs/799/telemetry", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"accepted":true,"duplicate":false}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	srv := &Server{client: testMCPClient(t, ts.URL), logger: func(string, ...any) {}}

	args := map[string]any{
		"run_id": float64(799), "sequence": float64(4),
		"correlation_id": "claude-session-abc", "provider": "anthropic", "adapter": "claude-code",
		"agent_reported_at": "2026-08-20T10:00:00Z", "kind": "progress",
		"phase": "testing", "progress_percent": 80.0, "estimate_revision": float64(3),
		"estimate_source": "adapter", "estimate_confidence": .8,
		"estimate_basis": "Four of five named checks complete",
	}
	withPayload := make(map[string]any, len(args)+1)
	for key, value := range args {
		withPayload[key] = value
	}
	withPayload["provider_payload"] = map[string]any{"prompt": "must not pass through"}
	if _, err := srv.toolReportProgress(withPayload); err == nil || !strings.Contains(err.Error(), "unknown telemetry field") {
		t.Fatalf("unknown provider payload error=%v", err)
	}

	text, err := srv.toolReportProgress(args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"accepted":true`) {
		t.Fatalf("result=%s", text)
	}
	if received["provider"] != "anthropic" || received["adapter"] != "claude-code" || received["sequence"] != float64(4) {
		t.Fatalf("received=%+v", received)
	}
}

// TestToolsCallAgentCRUD exercises the PAI-506 project-agent tools end
// to end against a stub server: create → get (.json artifact, peeled) →
// list → delete. Mirrors the issue/retrieve round-trip pattern above.
func TestToolsCallAgentCRUD(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":7,"key":"PAI"}]`))
	})
	mux.HandleFunc("/api/projects/7/agents", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":11,"project_id":7,"name":"builder","lane_tags":[],"metadata":{},"bootstrap_steps":[],"non_negotiable_rules":[]}`))
		default: // GET (list)
			_, _ = w.Write([]byte(`[{"id":11,"project_id":7,"name":"builder","lane_tags":[]}]`))
		}
	})
	mux.HandleFunc("/api/projects/7/agents/builder.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"project":{"id":7,"name":"Paimos","key":"PAI"},"agent":{"id":11,"project_id":7,"name":"builder","description":"the builder","lane_tags":["dev"]},"repos":[],"environments":[],"deploy_recipes":[]}`))
	})
	mux.HandleFunc("/api/projects/7/agents/builder", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := testMCPClient(t, ts.URL)
	srv := &Server{client: client, logger: func(string, ...any) {}}

	call := func(id int, name string, args map[string]any) string {
		t.Helper()
		req := rpcRequest{
			JSONRPC: "2.0",
			ID:      json.RawMessage([]byte(itoa(id))),
			Method:  "tools/call",
			Params:  mustJSON(t, map[string]any{"name": name, "arguments": args}),
		}
		resp := srv.dispatch(&req)
		if resp.Error != nil {
			t.Fatalf("%s error: %#v", name, resp.Error)
		}
		return extractToolText(t, resp.Result)
	}

	createText := call(1, "paimos_agent_create", map[string]any{
		"project_key": "PAI",
		"name":        "builder",
	})
	if !strings.Contains(createText, `"name":"builder"`) {
		t.Fatalf("create result missing agent: %s", createText)
	}

	getText := call(2, "paimos_agent_get", map[string]any{
		"project_key": "PAI",
		"name":        "builder",
	})
	// `get` must return the peeled .agent object, NOT the whole artifact.
	if !strings.Contains(getText, `"description":"the builder"`) {
		t.Fatalf("get result missing peeled agent: %s", getText)
	}
	if strings.Contains(getText, `"deploy_recipes"`) {
		t.Fatalf("get result leaked the artifact wrapper: %s", getText)
	}

	listText := call(3, "paimos_agent_list", map[string]any{"project_key": "PAI"})
	if !strings.Contains(listText, `"name":"builder"`) {
		t.Fatalf("list result missing agent: %s", listText)
	}

	delText := call(4, "paimos_agent_delete", map[string]any{
		"project_key": "PAI",
		"name":        "builder",
	})
	if !strings.Contains(delText, "deleted agent") {
		t.Fatalf("delete result missing success message: %s", delText)
	}
}

// itoa is a tiny dependency-free int→string for building JSON-RPC ids
// in the agent CRUD test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func testMCPClient(t *testing.T, baseURL string) *mcpclient.Client {
	t.Helper()
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.yaml")
	config := "default_instance: test\ninstances:\n  test:\n    url: " + baseURL + "\n    api_key: test-key\n"
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	prevConfig := os.Getenv("PAIMOS_CONFIG")
	prevInstance := os.Getenv("PAIMOS_INSTANCE")
	t.Cleanup(func() {
		_ = os.Setenv("PAIMOS_CONFIG", prevConfig)
		_ = os.Setenv("PAIMOS_INSTANCE", prevInstance)
	})
	if err := os.Setenv("PAIMOS_CONFIG", configPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("PAIMOS_INSTANCE", "test"); err != nil {
		t.Fatal(err)
	}
	client, err := mcpclient.NewFromConfig()
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func extractToolText(t *testing.T, result any) string {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Content) == 0 {
		t.Fatalf("missing content in result: %s", string(raw))
	}
	return payload.Content[0].Text
}

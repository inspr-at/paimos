package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeMessageCLIUsesPrivateStdinAndFixedRoute(t *testing.T) {
	const session = "11111111-1111-4111-8111-111111111111"
	const key = "22222222-2222-4222-8222-222222222222"
	const lease = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/projects/6/harness-sessions/"+session+"/messages" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		if r.Header.Get(agentAttrHeader) != "worker" || r.Header.Get(sessionAttrHeader) != session || r.Header.Get(harnessWorkerLeaseHeader) != lease || r.Header.Get(idempotencyHeader) != key {
			t.Error("owner headers missing")
		}
		var payload map[string]any
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload["body"] != "bounded content" || len(payload) != 5 {
			t.Error("unexpected model body")
		}
		fmt.Fprint(w, `{"message_id":"33333333-3333-4333-8333-333333333333","thread_id":"33333333-3333-4333-8333-333333333333","delivered":true,"private":"must not escape"}`)
	}))
	defer srv.Close()
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")
	var out bytes.Buffer
	previous := stdout
	stdout = &out
	t.Cleanup(func() { stdout = previous })
	frame := `{"worker_lease":"` + lease + `","message":{"to":"claude:peer","body":"bounded content"}}`
	for range 2 {
		cmd := nativeMessageCmd()
		cmd.SetArgs([]string{"--project-id", "6", "--session", session, "--agent", "worker", "--idempotency-key", key})
		cmd.SetIn(strings.NewReader(frame))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 || strings.Contains(out.String(), "must not escape") || strings.Contains(out.String(), lease) || strings.Contains(out.String(), "bounded content") {
		t.Fatal("private payload escaped receipt")
	}
	cmd := nativeMessageCmd()
	cmd.SetArgs([]string{"--project-id", "6", "--session", session, "--agent", "worker", "--idempotency-key", key})
	cmd.SetIn(strings.NewReader(strings.Replace(frame, `"to":"claude:peer"`, `"to":"claude:peer","sender":"forged"`, 1)))
	if err := cmd.Execute(); err == nil || calls != 2 {
		t.Fatal("model identity field reached server")
	}
}

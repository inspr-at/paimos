package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMessageListRequiresAddresseeBeforeNetwork(t *testing.T) {
	cmd := messageListCmd()
	cmd.SetArgs([]string{"--project", "PAI"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--to is required") {
		t.Fatalf("error=%v", err)
	}
}

func TestTellRequiresExactlyOneMessageSource(t *testing.T) {
	cmd := tellCmd()
	cmd.SetArgs([]string{"codex:reviewer", "--project", "PAI", "--message", "one", "--message-file", "two"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error=%v", err)
	}
}

func TestTellActionRequestSendsExplicitHumanGateMarker(t *testing.T) {
	var payload map[string]any
	var handlerErr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_, _ = w.Write([]byte(`[{"id":6,"key":"PAI","name":"PAIMOS"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/6/messages":
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				handlerErr = err.Error()
			}
			_, _ = w.Write([]byte(`{"message_id":"m1","thread_id":"m1","delivered":false,"held_reason":"action request - requires human approval"}`))
		default:
			handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	if _, _, err := executeCLIForTest(t, "--json", "tell", "codex:reviewer", "--project", "PAI", "--message", "Restart the service", "--action-request"); err != nil {
		t.Fatal(err)
	}
	if handlerErr != "" {
		t.Fatal(handlerErr)
	}
	if marked, ok := payload["is_action_request"].(bool); !ok || !marked {
		t.Fatalf("payload=%#v, want explicit is_action_request=true", payload)
	}
}

func TestTellPersistsRequestedDeliveryLevel(t *testing.T) {
	var payload map[string]any
	var handlerErr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_, _ = w.Write([]byte(`[{"id":6,"key":"PAI","name":"PAIMOS"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/6/messages":
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				handlerErr = err.Error()
			}
			_, _ = w.Write([]byte(`{"message_id":"m1","thread_id":"m1","delivered":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	if _, _, err := executeCLIForTest(t, "--json", "tell", "codex:reviewer", "--project", "PAI", "--level", "steer", "--message", "status update"); err != nil {
		t.Fatal(err)
	}
	if handlerErr != "" {
		t.Fatal(handlerErr)
	}
	if payload["delivery_level"] != "steer" {
		t.Fatalf("payload=%#v, want delivery_level=steer", payload)
	}
}

func TestWebhookTargetRefMustNotEnterProcessArguments(t *testing.T) {
	command := messageTargetSetCmd()
	command.SetArgs([]string{
		"--project", "PAI", "--address", "grok_bot:amy", "--adapter", "grok_bot_routine",
		"--kind", "https_webhook", "--target-ref", "https://routine.example/capability",
	})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "--target-ref-file") {
		t.Fatalf("error=%v", err)
	}
}

func TestWebhookAdapterTargetRefMustNotEnterProcessArgumentsWhenKindOmitted(t *testing.T) {
	for _, args := range [][]string{
		{"--project", "PAI", "--address", "grok_bot:amy", "--adapter", "grok_bot_routine", "--target-ref", "https://routine.example/capability"},
		{"--project", "PAI", "--address", "grok_bot:amy", "--adapter", "grok_bot_routine", "--kind", "wrong_kind", "--target-ref", "https://routine.example/capability"},
	} {
		command := messageTargetSetCmd()
		command.SetArgs(args)
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "--target-ref-file") {
			t.Fatalf("args=%v error=%v", args, err)
		}
	}
}

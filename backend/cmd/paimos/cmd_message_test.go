package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func writeSecretFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWebhookTargetSetSendsSenderKeyFromFileOnly(t *testing.T) {
	const capability = "https://routine.example/automations/webhook/fixture-capability"
	const senderKey = "crsr_fixture_sender_key_0001"
	var payload map[string]any
	var handlerErr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_, _ = w.Write([]byte(`[{"id":17,"key":"PHAROS","name":"Pharos"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/17/message-targets":
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				handlerErr = err.Error()
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"t-amy","version":1,"address":"grok_bot:amy","adapter":"grok_bot_routine","target_kind":"https_webhook","maximum_level":"simple","role":"primary","enabled":true,"has_secret":true}`))
		default:
			handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")
	refFile := writeSecretFixture(t, "target-ref.txt", capability+"\n")
	keyFile := writeSecretFixture(t, "target-key.txt", senderKey+"\n")

	out, errOut, err := executeCLIForTest(t, "message", "target", "set", "--project", "PHAROS", "--address", "grok_bot:amy",
		"--adapter", "grok_bot_routine", "--kind", "https_webhook", "--target-ref-file", refFile, "--target-key-file", keyFile,
		"--maximum-level", "simple")
	if err != nil {
		t.Fatalf("err=%v stderr=%s", err, errOut)
	}
	if handlerErr != "" {
		t.Fatal(handlerErr)
	}
	if payload["target_ref"] != capability || payload["target_secret"] != senderKey || payload["adapter"] != "grok_bot_routine" {
		t.Fatalf("payload=%#v", payload)
	}
	if strings.Contains(out+errOut, senderKey) || strings.Contains(out+errOut, capability) {
		t.Fatalf("CLI output exposed a secret: %q %q", out, errOut)
	}
	if !strings.Contains(out, "sender key stored") {
		t.Fatalf("stdout=%q", out)
	}
}

func TestWebhookTargetSetRequiresSenderKeyFileBeforeNetwork(t *testing.T) {
	refFile := writeSecretFixture(t, "target-ref.txt", "https://routine.example/automations/webhook/fixture\n")
	command := messageTargetSetCmd()
	command.SetArgs([]string{"--project", "PHAROS", "--address", "grok_bot:amy", "--adapter", "grok_bot_routine",
		"--kind", "https_webhook", "--target-ref-file", refFile})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "--target-key-file") {
		t.Fatalf("error=%v", err)
	}
}

func TestTargetSetRejectsSenderKeyForAdaptersWithoutSecretHeader(t *testing.T) {
	keyFile := writeSecretFixture(t, "target-key.txt", "crsr_fixture_sender_key_0001\n")
	command := messageTargetSetCmd()
	command.SetArgs([]string{"--project", "PAI", "--address", "codex:codex", "--adapter", "codex", "--kind", "codex_thread",
		"--target-ref", "019d-codex-thread", "--target-key-file", keyFile})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "no sender key") {
		t.Fatalf("error=%v", err)
	}
}

func TestTargetSetRejectsStdinForBothSecretFiles(t *testing.T) {
	command := messageTargetSetCmd()
	command.SetArgs([]string{"--project", "PHAROS", "--address", "grok_bot:amy", "--adapter", "grok_bot_routine",
		"--kind", "https_webhook", "--target-ref-file", "-", "--target-key-file", "-"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "stdin") {
		t.Fatalf("error=%v", err)
	}
}

func TestTargetSetRejectsMalformedSenderKeyFiles(t *testing.T) {
	refFile := writeSecretFixture(t, "target-ref.txt", "https://routine.example/automations/webhook/fixture\n")
	for name, content := range map[string]string{"empty": "\n", "multiline": "crsr_line_one\ncrsr_line_two\n"} {
		keyFile := writeSecretFixture(t, name+".txt", content)
		command := messageTargetSetCmd()
		command.SetArgs([]string{"--project", "PHAROS", "--address", "grok_bot:amy", "--adapter", "grok_bot_routine",
			"--kind", "https_webhook", "--target-ref-file", refFile, "--target-key-file", keyFile})
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "--target-key-file") || strings.Contains(err.Error(), "crsr_line") {
			t.Fatalf("%s: error=%v", name, err)
		}
	}
}

func TestTargetSetHasNoArgvSenderKeyFlag(t *testing.T) {
	command := messageTargetSetCmd()
	if command.Flags().Lookup("target-key") != nil || command.Flags().Lookup("target-secret") != nil {
		t.Fatal("a sender key must never be accepted as a process argument")
	}
	if command.Flags().Lookup("target-key-file") == nil {
		t.Fatal("--target-key-file is missing")
	}
}

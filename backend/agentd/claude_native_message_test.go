package agentd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeNativeMessageUsesOnlyBoundedCustomTool(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "native_message")
	logPath := filepath.Join(t.TempDir(), "events")
	t.Setenv("PAIMOS_CLAUDE_TEST_LOG", logPath)
	adapter := newTestClaudeAdapter(t, node)
	// The fake SDK only needs chainable field declarations. Production resolves
	// the real Zod peer dependency alongside the operator's pinned SDK.
	zod := filepath.Join(filepath.Dir(adapter.sdkPath), "node_modules", "zod")
	if err := os.MkdirAll(zod, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zod, "index.js"), []byte(`const field={min(){return this},max(){return this}};exports.z={string:()=>field,boolean:()=>field};`), 0600); err != nil {
		t.Fatal(err)
	}
	sent := make(chan NativeMessage, 1)
	events := make(chan AdapterEvent, 32)
	process, err := adapter.Start(context.Background(), StartRequest{Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:fixture", Prompt: "fixture", sendNativeMessage: func(_ context.Context, id string, message NativeMessage) NativeMessageReceipt {
		if !validOpaqueID(id) {
			t.Error("unbounded call identity")
		}
		sent <- message
		return NativeMessageReceipt{MessageID: "receipt", Delivered: true}
	}}, func(e AdapterEvent) { events <- e })
	if err != nil {
		t.Fatal(err)
	}
	defer process.Stop(context.Background(), ControlRequest{CorrelationID: "cleanup"})
	select {
	case message := <-sent:
		if message.To != "codex:peer" || message.Body != "model-originated fixture" {
			t.Fatal("tool argument mismatch")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native send missing")
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-events:
			raw, _ := json.Marshal(event)
			if strings.Contains(string(raw), "model-originated") {
				t.Fatal("message body in activity journal")
			}
			if event.Kind == EventTurnCompleted {
				raw, err := os.ReadFile(logPath)
				if err != nil || !strings.Contains(string(raw), "native receipt accepted") {
					t.Fatal("model did not consume receipt")
				}
				return
			}
		case <-deadline:
			t.Fatal("native tool result did not finish turn")
		}
	}
}

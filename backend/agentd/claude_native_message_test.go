//go:build !paimos_test_unsupported

package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeNativeSchemaShipsPinnedRuntimeAndLicense(t *testing.T) {
	digest := sha256.Sum256(claudeNativeMessageSchema)
	if hex.EncodeToString(digest[:]) != claudeMessageSchemaSHA256 {
		t.Fatal("embedded schema digest differs from reviewed pin")
	}
	var manifest struct {
		Version      string `json:"version"`
		BundleSHA256 string `json:"bundle_sha256"`
		SourceSHA256 string `json:"source_sha256"`
	}
	raw, err := os.ReadFile("claudeassets/native-message-schema.json")
	if err != nil || json.Unmarshal(raw, &manifest) != nil {
		t.Fatal("schema provenance unavailable")
	}
	source, err := os.ReadFile("claudeassets/native-message-schema.src.mjs")
	if err != nil {
		t.Fatal(err)
	}
	sourceDigest := sha256.Sum256(source)
	if manifest.Version != "4.0.17" || manifest.BundleSHA256 != claudeMessageSchemaSHA256 || manifest.SourceSHA256 != hex.EncodeToString(sourceDigest[:]) {
		t.Fatal("schema provenance mismatch")
	}
	if !strings.Contains(string(claudeNativeMessageSchema), "MIT License") || !strings.Contains(string(claudeNativeMessageSchema), "Colin McDonnell") {
		t.Fatal("bundled dependency license missing")
	}
	dir, _, err := materializeClaudeBridge(claudeAgentSDKBridge, claudeBridgeSHA256)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeClaudeRuntime(dir) })
	info, err := os.Stat(filepath.Join(dir, "native-message-schema.mjs"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("schema runtime is not owner-only")
	}
	removeClaudeRuntime(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("schema runtime was not cleaned up")
	}
}

func TestClaudeNativeMessageUsesOnlyBoundedCustomTool(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "native_message")
	logPath := filepath.Join(t.TempDir(), "events")
	t.Setenv("PAIMOS_CLAUDE_TEST_LOG", logPath)
	adapter := newTestClaudeAdapter(t, node)
	// The installed SDK can have no Zod peer. The bridge must carry its own
	// pinned schema runtime and work in a workspace with no node_modules.
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

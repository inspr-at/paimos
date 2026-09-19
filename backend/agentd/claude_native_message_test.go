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

// Exercise the owned supervisor snapshot and the live bridge's second-turn
// control path, including a tool result needed before Query acknowledges inbox.
func TestClaudeNativeReplyCompletesBeforeInboxAcknowledgement(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "native_reply_before_input_receipt")
	logPath := filepath.Join(t.TempDir(), "events")
	t.Setenv("PAIMOS_CLAUDE_TEST_LOG", logPath)
	reporter := &claudeReplyReporter{sessions: make(chan Session, 1)}
	s, err := NewSupervisor(SupervisorConfig{Instance: "fixture", StateRoot: t.TempDir(), Reporter: reporter, Adapters: []Adapter{newTestClaudeAdapter(t, node)}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	session, err := s.Start(context.Background(), StartRequest{Adapter: AdapterClaude, Identity: "claude:worker", ProjectID: 6, Workspace: t.TempDir(), Prompt: "initial turn"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	receipt, err := s.Inbox(ctx, session.ID, ControlRequest{Instance: "fixture", ProjectID: 6, Identity: "claude:worker", CorrelationID: "inbox-native-reply", Text: "bounded fixture inbox"})
	if err != nil || receipt.CorrelationID != "inbox-native-reply" {
		t.Fatalf("inbox receipt missing: %v", err)
	}
	select {
	case snapshot := <-reporter.sessions:
		if snapshot.ID != session.ID || snapshot.State != StateRunning || !snapshot.Managed || snapshot.Identity != "claude:worker" {
			t.Fatal("native reply lost owned sender scope")
		}
	case <-ctx.Done():
		t.Fatal("native reply blocked behind inbox acknowledgement")
	}
	raw, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(raw), "native reply accepted before input receipt") {
		t.Fatal("second-turn native receipt missing")
	}
}

type claudeReplyReporter struct{ sessions chan Session }

func (*claudeReplyReporter) ReportStatus(context.Context, Status) error { return nil }
func (r *claudeReplyReporter) SendNativeMessage(ctx context.Context, s Session, _ string, m NativeMessage) NativeMessageReceipt {
	if ctx.Err() != nil || m.ReplyTo != "incoming-message" || m.Body != "native inbox reply" {
		return NativeMessageReceipt{Error: "sender_unavailable"}
	}
	r.sessions <- s
	return NativeMessageReceipt{MessageID: "receipt", Delivered: true}
}

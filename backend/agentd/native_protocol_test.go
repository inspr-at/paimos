package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const nativeProtocolSentinel = "private native tool argument sentinel"

type nativeProtocolReporter struct{ sent chan NativeMessage }

func (*nativeProtocolReporter) ReportStatus(context.Context, Status) error { return nil }
func (r *nativeProtocolReporter) SendNativeMessage(_ context.Context, _ Session, _ string, m NativeMessage) NativeMessageReceipt {
	r.sent <- m
	return NativeMessageReceipt{MessageID: "receipt", Delivered: true}
}

func TestCodexNativeProtocolAndJournalExcludeVendorEchoes(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			adapter := NewCodexAdapter(os.Args[0], "test")
			adapter.command = func(string, ...string) *exec.Cmd {
				cmd := exec.Command(os.Args[0], "-test.run=^TestNativeCodexProtocolHelper$")
				mode := "disabled"
				if enabled {
					mode = "enabled"
				}
				cmd.Env = append(os.Environ(), "PAIMOS_NATIVE_PROTOCOL_HELPER="+mode)
				return cmd
			}
			root := t.TempDir()
			reporter := &nativeProtocolReporter{sent: make(chan NativeMessage, 1)}
			config := SupervisorConfig{Instance: "fixture", StateRoot: root, Adapters: []Adapter{adapter}}
			if enabled {
				config.Reporter = reporter
			}
			supervisor, err := NewSupervisor(config)
			if err != nil {
				t.Fatal(err)
			}
			defer supervisor.Close(context.Background())
			session, err := supervisor.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Identity: "codex:fixture", ProjectID: 6, Workspace: t.TempDir(), Prompt: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				state := supervisor.Status().Sessions[0]
				if state.ActivitySequence > 0 && state.LastEventKind == EventTurnCompleted {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("native turn did not finish: %s %s", state.State, state.LastEventKind)
				}
				time.Sleep(5 * time.Millisecond)
			}
			if enabled {
				select {
				case message := <-reporter.sent:
					if message.Body != nativeProtocolSentinel {
						t.Fatal("wrong native arguments")
					}
				default:
					t.Fatal("request did not reach sender")
				}
			}
			if err := supervisor.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.Type().IsRegular() {
					raw, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if strings.Contains(string(raw), nativeProtocolSentinel) {
						t.Errorf("vendor payload journaled in %s", filepath.Base(path))
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			_ = session
		})
	}
}

func TestNativeCodexProtocolHelper(t *testing.T) {
	mode := os.Getenv("PAIMOS_NATIVE_PROTOCOL_HELPER")
	if mode == "" {
		t.Skip("helper")
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	initialized := false
	emit := func(value any) {
		if encoder.Encode(value) != nil {
			os.Exit(2)
		}
	}
	for scanner.Scan() {
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			os.Exit(2)
		}
		respond := func(result any) { emit(map[string]any{"id": frame.ID, "result": result}) }
		switch frame.Method {
		case "initialize":
			var p struct {
				Capabilities struct {
					Experimental bool `json:"experimentalApi"`
				} `json:"capabilities"`
			}
			if json.Unmarshal(frame.Params, &p) != nil || !p.Capabilities.Experimental {
				os.Exit(3)
			}
			initialized = true
			respond(map[string]any{})
		case "initialized":
		case "thread/start":
			var p struct {
				Tools []struct {
					Type, Name, Description string
					Schema                  map[string]any `json:"inputSchema"`
				} `json:"dynamicTools"`
			}
			if !initialized || json.Unmarshal(frame.Params, &p) != nil {
				os.Exit(4)
			}
			if mode == "enabled" {
				if len(p.Tools) != 1 || p.Tools[0].Type != "function" || p.Tools[0].Name != NativeMessageTool || p.Tools[0].Schema["additionalProperties"] != false {
					os.Exit(5)
				}
			} else if len(p.Tools) != 0 {
				os.Exit(6)
			}
			respond(map[string]any{"thread": map[string]any{"id": "thread-native"}})
		case "turn/start":
			respond(map[string]any{"turn": map[string]any{"id": "turn-native", "status": "inProgress"}})
			if mode == "disabled" {
				emit(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-native", "turn": map[string]any{"id": "turn-native", "status": "completed"}}})
				continue
			}
			arguments := NativeMessage{To: "claude:peer", Body: nativeProtocolSentinel}
			emit(map[string]any{"method": "item/started", "params": map[string]any{"threadId": "thread-native", "turnId": "turn-native", "item": map[string]any{"id": "call-native", "type": "dynamicToolCall", "tool": NativeMessageTool, "arguments": arguments}}})
			emit(map[string]any{"id": "native-request", "method": "item/tool/call", "params": map[string]any{"threadId": "thread-native", "turnId": "turn-native", "callId": "call-native", "tool": NativeMessageTool, "arguments": arguments}})
		case "":
			var result struct {
				Success bool                          `json:"success"`
				Content []struct{ Type, Text string } `json:"contentItems"`
			}
			if string(frame.ID) != `"native-request"` || json.Unmarshal(frame.Result, &result) != nil || !result.Success || len(result.Content) != 1 || result.Content[0].Type != "inputText" || !strings.Contains(result.Content[0].Text, `"message_id":"receipt"`) {
				os.Exit(7)
			}
			emit(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-native", "turnId": "turn-native", "item": map[string]any{"id": "call-native", "type": "dynamicToolCall", "arguments": nativeProtocolSentinel, "contentItems": []any{map[string]any{"type": "inputText", "text": nativeProtocolSentinel}}}}})
			emit(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-native", "turn": map[string]any{"id": "turn-native", "status": "completed"}}})
		case "turn/interrupt":
			respond(map[string]any{})
		default:
			os.Exit(8)
		}
	}
	os.Exit(0)
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrokConversationBinaryVariantsAreExplicitAndPinned(t *testing.T) {
	source, err := grokVariantSpecFor(GrokBinarySourceBuilt1032)
	if err != nil {
		t.Fatal(err)
	}
	if source.binaryName != "xai-grok-pager" || source.executableSHA != "6294a6bc10304e3d3b5896194277f6d24fca643b0605d650ca39bad5b40a6d14" ||
		source.sourceCommit != "482711333c7195dc16a272777f86086d615e2afb" {
		t.Fatalf("source-built identity changed: %+v", source)
	}
	npm, err := grokVariantSpecFor(GrokBinaryNPM1030)
	if err != nil {
		t.Fatal(err)
	}
	if npm.binaryName != "grok-native" || npm.executableSHA == source.executableSHA || npm.sourceCommit != "" {
		t.Fatalf("npm identity overlaps source-built variant: %+v", npm)
	}
	if _, err := grokVariantSpecFor(GrokBinaryVariant("unrecognized")); err == nil {
		t.Fatal("unrecognized executable variant accepted")
	}
}

func TestGrokConversationSourceBuiltBindingPathAndVariantRejection(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	binary := filepath.Join(root, "target", "release", "xai-grok-pager")
	auth := filepath.Join(root, "auth.json")
	scratch := filepath.Join(root, "scratch")
	binding := GrokConversationBinding{
		BinaryVariant:   GrokBinarySourceBuilt1032,
		BinaryPath:      binary,
		AuthPath:        auth,
		AccountKey:      "grok-codex",
		PrincipalSHA256: strings.Repeat("0", 64),
		ScratchRoot:     scratch,
	}
	if _, err := NewGrokConversationAdapter(binding); err != nil {
		t.Fatalf("source-built binding rejected: %v", err)
	}
	if _, err := grokBinaryRoot(GrokBinaryNPM1030, binary, auth, scratch); err == nil {
		t.Fatal("source-built layout accepted under npm variant")
	}
	if _, err := grokBinaryRoot(GrokBinarySourceBuilt1032, filepath.Join(root, "target", "release", "grok-native"), auth, scratch); err == nil {
		t.Fatal("npm executable name accepted under source-built variant")
	}
	if _, err := grokBinaryRoot(GrokBinarySourceBuilt1032, binary, filepath.Join(root, "target", "release", "auth.json"), scratch); err == nil {
		t.Fatal("source-built auth inside executable read root accepted")
	}
	if _, err := grokBinaryRoot(GrokBinarySourceBuilt1032, binary, auth, filepath.Join(root, "target", "release", "scratch")); err == nil {
		t.Fatal("source-built scratch inside executable read root accepted")
	}
	binding.BinaryVariant = GrokBinaryVariant("unrecognized")
	if _, err := NewGrokConversationAdapter(binding); err == nil {
		t.Fatal("unrecognized binding variant accepted")
	}
	binding.BinaryVariant = ""
	if _, err := NewGrokConversationAdapter(binding); err == nil {
		t.Fatal("zero binding variant accepted")
	}
}

func grokFixture(t *testing.T, frames ...string) (CodexConversationResult, []CodexConversationDelta, string) {
	t.Helper()
	var sent bytes.Buffer
	wire := newGrokACPWire(strings.NewReader(strings.Join(frames, "\n")+"\n"), &sent)
	result, deltas := wire.prompt("owned-session", "turn-1", "synthetic", GrokConversationOptions{MaxOutputBytes: 32, MaxEvents: 8})
	return result, deltas, sent.String()
}

func TestGrokConversationSyntheticTerminalOutcomes(t *testing.T) {
	update := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"owned-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer"}}}}`
	for _, test := range []struct {
		name, terminal string
		outcome        ConversationOutcome
		failure        ConversationFailure
		text           string
	}{
		{"success", "end_turn", ConversationCompleted, ConversationFailureNone, "answer"},
		{"refusal", "refusal", ConversationFailed, ConversationFailureRefusal, ""},
		{"truncation", "max_tokens", ConversationFailed, ConversationFailureTruncated, ""},
		{"max requests", "max_turn_requests", ConversationFailed, ConversationFailureTruncated, ""},
		{"cancellation", "cancelled", ConversationCancelled, ConversationFailureCancelled, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			terminal := `{"jsonrpc":"2.0","id":1,"result":{"stopReason":"` + test.terminal + `"}}`
			result, deltas, sent := grokFixture(t, update, terminal)
			wantDeltas := 0
			if test.outcome == ConversationCompleted {
				wantDeltas = 1
			}
			if result.Outcome != test.outcome || result.Failure != test.failure || result.Text != test.text || len(deltas) != wantDeltas {
				t.Fatalf("outcome=%s failure=%s text=%q deltas=%d", result.Outcome, result.Failure, result.Text, len(deltas))
			}
			if strings.Count(sent, `"method":"session/prompt"`) != 1 {
				t.Fatalf("prompt count != 1: %q", sent)
			}
			replayed, err := replayGrokDeltas(deltas, 0)
			if err != nil || len(replayed) != wantDeltas {
				t.Fatalf("replay=%v err=%v", replayed, err)
			}
			if wantDeltas == 1 && replayed[0].Text != "answer" {
				t.Fatal("completed replay mismatch")
			}
			if _, err := replayGrokDeltas(deltas, 2); err == nil {
				t.Fatal("future replay cursor accepted")
			}
		})
	}
}

func TestGrokConversationRejectsToolsAndClientRequests(t *testing.T) {
	for _, frame := range []string{
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"owned-session","update":{"sessionUpdate":"tool_call","toolCallId":"one"}}}`,
		`{"jsonrpc":"2.0","method":"session/request_permission","id":9,"params":{}}`,
		`{"jsonrpc":"2.0","method":"session/update","id":1,"result":{"stopReason":"end_turn"},"params":{}}`,
		`{"jsonrpc":"2.0","method":"_x.ai/hosted_search","params":{"name":"x_search"}}`,
		`{"jsonrpc":"2.0","method":"_x.ai/mcp_initialized","params":{"mcpToolCount":1}}`,
		`{"jsonrpc":"2.0","method":"_x.ai/mcp/servers_updated","params":{"mcpServers":[{}]}}`,
	} {
		result, _, _ := grokFixture(t, frame)
		if result.Outcome != ConversationFailed || result.Failure != ConversationFailureProtocol || result.Text != "" {
			t.Fatalf("unsafe frame accepted: %q: %+v", frame, result)
		}
	}
}

func TestGrokConversationRejectsForeignStreamAndBounds(t *testing.T) {
	foreign := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"other","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"secret"}}}}`
	result, _, _ := grokFixture(t, foreign)
	if result.Outcome != ConversationFailed || result.Text != "" {
		t.Fatal("foreign session update accepted")
	}
	big := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"owned-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + strings.Repeat("x", 33) + `"}}}}`
	result, _, _ = grokFixture(t, big)
	if result.Failure != ConversationFailureOutputBound || result.Text != "" {
		t.Fatalf("output bound result=%+v", result)
	}
	thought := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"owned-session","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"private"}}}}`
	frames := make([]string, 9)
	for i := range frames {
		frames[i] = thought
	}
	result, _, _ = grokFixture(t, frames...)
	if result.Failure != ConversationFailureEventBound || result.Text != "" {
		t.Fatalf("event bound result=%+v", result)
	}
}

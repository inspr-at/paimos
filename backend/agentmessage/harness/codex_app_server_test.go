// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The rejection fixtures below are the exact shapes the app-server emits
// (codex-rs app-server turn_processor `turn_steer_inner`): every documented
// precondition failure is JSON-RPC -32600 `invalid request`, the
// not-steerable case additionally serializes a TurnError with
// `codexErrorInfo.activeTurnNotSteerable` into `error.data`.
const (
	codexRejectionReviewTurn = `{"code":-32600,"message":"cannot steer a review turn","data":{"message":"cannot steer a review turn","codexErrorInfo":{"activeTurnNotSteerable":{"turnKind":"review"}},"additionalDetails":null,"misalignment":null}}`
	codexRejectionTurnRace   = "{\"code\":-32600,\"message\":\"expected active turn id `turn-raced` but found `turn-next`\"}"
	codexRejectionNoTurn     = `{"code":-32600,"message":"no active turn to steer"}`
)

func TestClassifyCodexSteerRejection(t *testing.T) {
	for _, tc := range []struct {
		name, rejection, reason string
		documented              bool
	}{
		{"review turn with structured data", codexRejectionReviewTurn, "not_steerable", true},
		{"structured data under another code stays unknown", `{"code":-32603,"message":"steer failed","data":{"codexErrorInfo":{"activeTurnNotSteerable":{"turnKind":"compact"}}}}`, "", false},
		{"marker text in unrelated data", `{"code":-32600,"message":"steer failed","data":{"additionalDetails":"activeTurnNotSteerable"}}`, "", false},
		{"marker at wrong path", `{"code":-32600,"message":"steer failed","data":{"activeTurnNotSteerable":{"turnKind":"review"}}}`, "", false},
		{"marker has wrong type", `{"code":-32600,"message":"steer failed","data":{"codexErrorInfo":{"activeTurnNotSteerable":"review"}}}`, "", false},
		{"marker is empty object", `{"code":-32600,"message":"steer failed","data":{"codexErrorInfo":{"activeTurnNotSteerable":{}}}}`, "", false},
		{"marker misses turn kind", `{"code":-32600,"message":"steer failed","data":{"codexErrorInfo":{"activeTurnNotSteerable":{"kind":"review"}}}}`, "", false},
		{"marker has foreign turn kind", `{"code":-32600,"message":"steer failed","data":{"codexErrorInfo":{"activeTurnNotSteerable":{"turnKind":"foreign"}}}}`, "", false},
		{"compact turn message only", `{"code":-32600,"message":"cannot steer a compact turn"}`, "not_steerable", true},
		{"unknown turn kind", `{"code":-32600,"message":"cannot steer a foreign turn"}`, "", false},
		{"expected turn mismatch", codexRejectionTurnRace, "not_steerable", true},
		{"expected turn mismatch with suffix", "{\"code\":-32600,\"message\":\"expected active turn id `a` but found `b` trailing\"}", "", false},
		{"expected turn mismatch with empty id", "{\"code\":-32600,\"message\":\"expected active turn id `` but found `b`\"}", "", false},
		{"no active turn", codexRejectionNoTurn, "idle", true},
		{"no active turn with surrounding whitespace", `{"code":-32600,"message":" no active turn to steer\n"}`, "idle", true},
		{"unknown variant", "{\"code\":-32600,\"message\":\"Invalid request: unknown variant `turn/steer`\"}", "", false},
		{"method not found", `{"code":-32601,"message":"Method not found"}`, "", false},
		{"ownership rejection", `{"code":-32600,"message":"direct app-server input is not allowed for multi-agent v2 sub-agents"}`, "", false},
		{"output schema mismatch", `{"code":-32600,"message":"active turn uses a different output schema"}`, "", false},
		{"documented text under a foreign code", "{\"code\":-32603,\"message\":\"expected active turn id `a` but found `b`\"}", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var remote codexRPCError
			if err := json.Unmarshal([]byte(tc.rejection), &remote); err != nil {
				t.Fatal(err)
			}
			reason, documented := classifyCodexSteerRejection(&remote)
			if reason != tc.reason || documented != tc.documented {
				t.Fatalf("classify(%s)=%q,%v want %q,%v", tc.rejection, reason, documented, tc.reason, tc.documented)
			}
		})
	}
	if reason, documented := classifyCodexSteerRejection(nil); reason != "" || documented {
		t.Fatalf("nil error classified as %q,%v", reason, documented)
	}
}

func TestCodexProxyTimeoutErrorPreservesTransportDiagnostics(t *testing.T) {
	err := &CodexProxyTimeoutError{Phase: "websocket handshake", Timeout: 2 * time.Second, Daemon: "codex cli=x app-server=y status=running"}
	for _, required := range []string{"no response to websocket handshake within 2s", "WebSocket byte pipe", "codex cli=x app-server=y"} {
		if !strings.Contains(err.Error(), required) {
			t.Fatalf("error=%q missing %q", err, required)
		}
	}
	if ErrorCode(err) != "" {
		t.Fatalf("transport timeout must not carry a plugin error code: %q", ErrorCode(err))
	}
}

// TestCodexAppServerProxyReadOnlyProbe is opt-in evidence gathering against
// the real local daemon: it performs the documented handshake through the
// vendor proxy and reads the named thread's status without steering it.
func TestCodexAppServerProxyReadOnlyProbe(t *testing.T) {
	threadID := strings.TrimSpace(os.Getenv("PAIMOS_CODEX_PROBE_THREAD"))
	if threadID == "" {
		t.Skip("set PAIMOS_CODEX_PROBE_THREAD to a real local Codex thread id")
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex CLI not in PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), CodexSteerTimeout)
	defer cancel()
	started := time.Now()
	session, err := openCodexAppServerSession(ctx, path, "probe")
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	handshake := time.Since(started)
	turnID, reason, err := session.activeTurn(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PAI-825_PROBE thread=%s handshake_ms=%d total_ms=%d in_progress_turn=%q fallback_reason=%q",
		threadID, handshake.Milliseconds(), time.Since(started).Milliseconds(), turnID, reason)
}

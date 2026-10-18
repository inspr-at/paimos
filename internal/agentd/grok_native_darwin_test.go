// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/sessionusage"
)

func TestGrokAssetsStayPinned(t *testing.T) {
	config, err := grokAssets.ReadFile("grokassets/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := grokAssets.ReadFile("grokassets/conversation.txt")
	if err != nil {
		t.Fatal(err)
	}
	if sha256Hex(config) != grokConfigSHA256 || sha256Hex(profile) != grokProfileSHA256 {
		t.Fatal("Grok isolation assets changed")
	}
	if _, err := NewGrokAdapter(map[string]GrokBinding{"account": {Variant: "invalid"}}).Start(context.Background(), StartRequest{
		Profile: Profile{Harness: Grok, Model: grokModel, Effort: grokEffort, Family: "xai"}, AccountKey: "account"}, func(AdapterEvent) {}); err == nil {
		t.Fatal("unknown native variant accepted")
	}
}

func TestGrokQualifiedVerificationReachesPinnedNativePreflight(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("native Grok verification is qualified only on darwin/arm64")
	}
	r := verificationRequest(t)
	r.Profile = Profile{Harness: Grok, Model: grokModel, Effort: grokEffort, Family: "xai"}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "node_modules", "@xai-official", "grok", "bin", "grok-native")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("synthetic native executable; never launch"), 0700); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(root, "private-scratch")
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	binding := GrokBinding{
		Variant:         "npm-grok-1.0.30",
		BinaryPath:      binary,
		AuthPath:        filepath.Join(root, "missing-synthetic-auth.json"),
		ScratchRoot:     scratch,
		PrincipalSHA256: strings.Repeat("a", 64),
	}
	a := NewGrokAdapter(map[string]GrokBinding{"account": binding})
	if !a.VerificationSupported() {
		t.Fatal("qualified native Grok verification unavailable")
	}
	if err := validExecutionMode(r.Run, a); err != nil {
		t.Fatalf("qualified verification refused by execution mode: %v", err)
	}
	observed := false
	p, err := a.Start(t.Context(), r, func(AdapterEvent) { observed = true })
	if p != nil || observed || err == nil || err.Error() != "native Grok binary hash mismatch" {
		t.Fatalf("verification did not reach pinned native preflight: process=%t observed=%t err=%v", p != nil, observed, err)
	}
	if errors.Is(err, ErrVerificationUnavailable) {
		t.Fatal("qualified Grok was stopped by execution mode gate")
	}
	if _, err := os.Stat(binding.AuthPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("synthetic auth path unexpectedly appeared: %v", err)
	}
	if p, err := NewGrokAdapter().Start(t.Context(), r, func(AdapterEvent) {}); p != nil || err == nil || err.Error() != "native Grok account unavailable" {
		t.Fatalf("missing private binding did not fail closed: process=%t err=%v", p != nil, err)
	}
}

func TestGrokNativeUsageUpdate(t *testing.T) {
	var got *sessionusage.UsageReport
	p := &grokProcess{sessionID: "session", model: grokModel, observe: func(ev AdapterEvent) {
		if ev.SessionUsage != nil {
			report := *ev.SessionUsage
			got = &report
		}
	}}
	p.onEvent([]byte(`{"method":"session/update","params":{"sessionId":"session","update":{"sessionUpdate":"usage_update"}}}`))
	if p.violation.Load() || got != nil {
		t.Fatal("token-free usage update became tokens or a violation")
	}
	p.onEvent([]byte(`{"method":"session/update","params":{"sessionId":"session","update":{"sessionUpdate":"usage_update","inputTokens":11,"outputTokens":3,"cachedReadTokens":4,"cacheCreationTokens":2}}}`))
	if p.violation.Load() || got == nil || got.Model != grokModel || got.InputTokens == nil || *got.InputTokens != 17 || got.OutputTokens == nil || *got.OutputTokens != 3 || got.CachedInputTokens == nil || *got.CachedInputTokens != 4 || got.BillingMode != "unknown" {
		t.Fatalf("native usage %+v violation %t", got, p.violation.Load())
	}
}

func TestGrokNotificationsRejectToolsAndBoundAnswer(t *testing.T) {
	var updates []ChatUpdate
	p := &grokProcess{sessionID: "session", wireProcess: &wireProcess{sessionID: "session", observe: func(ev AdapterEvent) {
		if ev.Chat != nil {
			updates = append(updates, *ev.Chat)
		}
	}}}
	valid := map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "session", "update": map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "answer"}}}}
	raw, _ := json.Marshal(valid)
	p.onEvent(raw)
	if p.violation.Load() || p.Evidence() != "answer" {
		t.Fatal("bounded answer event rejected")
	}
	if !reflect.DeepEqual(updates, []ChatUpdate{chatText("answer")}) {
		t.Fatal("native Grok dropped the live chat chunk")
	}
	// Keep the synthetic connection unowned so the refusal never signals a PID.
	p.wireProcess = nil
	p.onEvent([]byte(`{"method":"session/request_permission","params":{"sessionId":"session"}}`))
	if !p.violation.Load() {
		t.Fatal("tool permission request accepted")
	}
	p2 := &grokProcess{sessionID: "session"}
	valid["params"] = map[string]any{"sessionId": "other", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "wrong"}}}
	raw, _ = json.Marshal(valid)
	p2.onEvent(raw)
	if !p2.violation.Load() {
		t.Fatal("foreign session update accepted")
	}
}

func TestGrokProxyRejectsOtherTargets(t *testing.T) {
	p, err := startGrokProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop()
	conn, err := net.DialTimeout("tcp", p.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, err = conn.Write([]byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil || !bytes.Contains(line, []byte("403")) {
		t.Fatalf("proxy did not reject foreign target: %q %v", line, err)
	}
}

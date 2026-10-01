// SPDX-License-Identifier: AGPL-3.0-only

package modelprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestEndpointAndAddressPolicy(t *testing.T) {
	for _, base := range []string{"http://localhost:11434/v1", "https://model.example/api/v1/", "http://192.168.2.3:8080/v1", "http://[::1]:8080/v1"} {
		got, err := endpoint(base, "chat/completions")
		if err != nil || !strings.HasSuffix(got, "/chat/completions") {
			t.Fatalf("valid base refused: %s", base)
		}
	}
	for _, base := range []string{"", "file:///tmp/socket", "http://user:pass@example.com/v1", "https://example.com/v1?key=fixture", "https://example.com/v1#fragment", "http://169.254.169.254/v1", "http://0.0.0.0/v1", "http://[fd00:ec2::254]/v1", "http://[::ffff:169.254.169.254]/v1"} {
		if _, err := endpoint(base, "chat/completions"); err == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	for _, raw := range []string{"169.254.169.254", "fe80::1", "::", "224.0.0.1", "255.255.255.255"} {
		if allowedIP(netip.MustParseAddr(raw)) {
			t.Fatal("unsafe resolved address accepted")
		}
	}
}

func TestCompatibleChatAndSanitizedFailures(t *testing.T) {
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-only-provider-value" {
			t.Error("wrong compatible request")
		}
		var in struct {
			Model     string    `json:"model"`
			Messages  []Message `json:"messages"`
			MaxTokens int       `json:"max_tokens"`
			Stream    bool      `json:"stream"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Model != "fixture-chat" || len(in.Messages) != 1 || in.MaxTokens != 8 || in.Stream {
			t.Error("wrong chat payload")
		}
		switch mode {
		case "status":
			w.WriteHeader(401)
			fmt.Fprint(w, "test-only-provider-value")
		case "invalid":
			fmt.Fprint(w, `{"choices":[]}`)
		case "truncated":
			fmt.Fprint(w, `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`)
		case "oversize":
			fmt.Fprint(w, strings.Repeat("x", (1<<20)+1))
		default:
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1}}`)
		}
	}))
	defer srv.Close()
	s := New(nil, nil)
	c := Config{Settings: Settings{BaseURL: srv.URL + "/v1", ChatModel: "fixture-chat"}}
	for _, m := range []string{"ok", "status", "invalid", "truncated", "oversize"} {
		mode = m
		got, err := s.chat(t.Context(), c, "test-only-provider-value", []Message{{Role: "user", Content: "Reply OK."}}, 8)
		if m == "ok" {
			if err != nil || got.Text != "OK" || got.InputTokens != 4 || got.OutputTokens != 1 {
				t.Fatal("compatible chat failed")
			}
		} else if err == nil || strings.Contains(err.Error(), "test-only-provider-value") {
			t.Fatal("provider error was not safely rejected")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.chat(ctx, c, "", nil, 8); err == nil {
		t.Fatal("canceled request succeeded")
	}
}

func TestChatNeverFollowsRedirect(t *testing.T) {
	calls := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 307) }))
	defer srv.Close()
	s := New(nil, nil)
	_, err := s.chat(t.Context(), Config{Settings: Settings{BaseURL: srv.URL + "/v1", ChatModel: "fixture"}}, "", nil, 8)
	if err == nil || calls != 0 {
		t.Fatal("redirect was followed")
	}
}

func TestSecretFormattingIsMasked(t *testing.T) {
	value := Secret("test-only-provider-value")
	raw, _ := json.Marshal(value)
	for _, out := range []string{string(raw), fmt.Sprint(value), fmt.Sprintf("%#v", value)} {
		if strings.Contains(out, "test-only-provider-value") {
			t.Fatal("secret formatter disclosed value")
		}
	}
}

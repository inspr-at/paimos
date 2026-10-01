// SPDX-License-Identifier: AGPL-3.0-only

package modelprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
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

func TestSpecialUseAddressesRefusedBeforeDial(t *testing.T) {
	client := newClient()
	transport := client.Transport.(*http.Transport)
	defer transport.CloseIdleConnections()
	for _, raw := range []string{
		"0.1.2.3", "0.255.255.255", "100.64.0.0", "100.100.100.200", "100.127.255.255",
		"168.63.129.16", "169.254.169.254", "192.0.0.1", "192.0.2.1", "192.88.99.1",
		"198.18.0.1", "198.19.255.255", "198.51.100.1", "203.0.113.1", "240.0.0.1", "255.255.255.255",
		"100::1", "2001:db8::1", "64:ff9b::a9fe:a9fe", "64:ff9b:1::a9fe:a9fe", "fec0::1", "fd00:ec2::254",
	} {
		ip := netip.MustParseAddr(raw)
		addresses := []netip.Addr{ip}
		if ip.Is4() {
			addresses = append(addresses, netip.AddrFrom16(ip.As16()))
		}
		for _, addr := range addresses {
			t.Run(addr.String(), func(t *testing.T) {
				if allowedIP(addr) {
					t.Fatal("unsafe resolved address accepted")
				}
				base := "http://" + net.JoinHostPort(addr.String(), "11434") + "/v1"
				for _, path := range []string{"chat/completions", "embeddings"} {
					if _, err := endpoint(base, path); err == nil {
						t.Fatal("unsafe literal endpoint accepted")
					}
				}
				// Cancellation prevents real network traffic even if the guard regresses.
				// Address refusal must still happen before any attempt to dial.
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				conn, err := transport.DialContext(ctx, "tcp", net.JoinHostPort(addr.String(), "11434"))
				if conn != nil {
					conn.Close()
				}
				if err == nil || err.Error() != "provider address refused" {
					t.Fatalf("unsafe address reached dial: %v", err)
				}
			})
		}
	}
}

func TestLocalAndPublicModelAddressesRemainAllowed(t *testing.T) {
	for _, raw := range []string{
		"127.0.0.1", "127.0.0.2", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.2.3",
		"::1", "fc00::1", "fd00::1", "fd00:ec2::253", "fd00:ec2::255",
		"8.8.8.8", "2606:4700:4700::1111", "100.63.255.255", "100.128.0.0", "168.63.129.15", "168.63.129.17",
		"::ffff:127.0.0.1", "::ffff:192.168.2.3",
	} {
		t.Run(raw, func(t *testing.T) {
			addr := netip.MustParseAddr(raw)
			if !allowedIP(addr) {
				t.Fatal("supported model address refused")
			}
			if _, err := endpoint("http://"+net.JoinHostPort(raw, "11434")+"/v1", "chat/completions"); err != nil {
				t.Fatal("supported model endpoint refused")
			}
		})
	}
	if allowedIP(netip.Addr{}) {
		t.Fatal("invalid address accepted")
	}
}

func TestOutboundInventoryUsesWorkspaceModelOptIn(t *testing.T) {
	raw, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, inventory, found := strings.Cut(string(raw), "## Server outbound calls")
	if !found {
		t.Fatal("server outbound inventory missing")
	}
	if strings.Contains(inventory, "AEON_EMBEDDING_URL") {
		t.Fatal("server outbound inventory names the retired host embedding switch")
	}
	for _, row := range strings.Split(inventory, "\n") {
		if strings.HasPrefix(row, "| Workspace model chat and embedding requests ") {
			for _, term := range []string{"Off by default", "settings.manage", "feature", "Test connection", "disabled"} {
				if !strings.Contains(row, term) {
					t.Fatalf("workspace model egress inventory omits %q", term)
				}
			}
			return
		}
	}
	t.Fatal("workspace model provider missing from server outbound inventory")
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

// SPDX-License-Identifier: AGPL-3.0-only
package usageprobe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureHome(t *testing.T, name, login string) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, name), []byte(login), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

// Risk: endpoint credentials or arbitrary vendor strings must never survive
// normalization; offline idle capture must retain every supported quota window.
func TestHostLoginProbesNormalizeFixturesWithoutLeakingCredentials(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		harness, file, login, fixture, host, header, value string
		want                                               int
	}{
		{"grok", "auth.json", `{"https://auth.x.ai::fixture":{"key":"synthetic-login-value"}}`, "grok.json", "cli-chat-proxy.grok.com", "x-xai-token-auth", "xai-grok-cli", 1},
		{"claude", ".credentials.json", `{"claudeAiOauth":{"accessToken":"synthetic-login-value","scopes":["user:profile"],"expiresAt":2000000000000}}`, "claude.json", "api.anthropic.com", "anthropic-beta", "oauth-2025-04-20", 4},
		{"codex", "auth.json", `{"tokens":{"access_token":"synthetic-login-value","account_id":"fixture-account"}}`, "codex.json", "chatgpt.com", "ChatGPT-Account-Id", "fixture-account", 2},
		{"pi", "auth.json", `{"openrouter":{"type":"api_key","key":"synthetic-login-value"}}`, "openrouter-key.json", "openrouter.ai", "Accept", "application/json", 0},
	} {
		t.Run(tc.harness, func(t *testing.T) {
			home := fixtureHome(t, tc.file, tc.login)
			path := filepath.Join(home, tc.file)
			before, _ := os.Stat(path)
			calls := 0
			client := Client{HTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || r.URL.Host != tc.host || r.Header.Get("Authorization") != "Bearer synthetic-login-value" || r.Header.Get(tc.header) != tc.value {
					t.Fatal("wrong vendor-only request")
				}
				fixture := tc.fixture
				if r.URL.Path == "/api/v1/credits" {
					fixture = "openrouter-credits.json"
				}
				raw, err := os.ReadFile(filepath.Join("testdata", fixture))
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
			})}}
			result := client.Capture(t.Context(), Target{Harness: tc.harness, Home: home, Provider: "openrouter"}, now)
			if result.Cause != "" || len(result.Readings) != tc.want {
				t.Fatalf("normalization failed: %s, windows %d", result.Cause, len(result.Readings))
			}
			for _, v := range result.Readings {
				if v.Source != "agentd" || v.Validate(now) != nil || v.Plan != "" {
					t.Fatal("unsafe normalized reading")
				}
			}
			if tc.harness == "pi" && (calls != 2 || result.Budget == nil || result.Budget.KeyUsageUSD != 12.5 || *result.Budget.KeyLimitUSD != 50 || *result.Budget.BalanceUSD != 80 || len(result.Readings) != 0) {
				t.Fatal("dollar budget mixed with quota or lost balance")
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "synthetic-login-value") || strings.Contains(string(raw), "ignored-vendor-text") || strings.Contains(string(raw), home) {
				t.Fatal("login or raw payload escaped projection")
			}
			after, _ := os.Stat(path)
			stored, _ := os.ReadFile(path)
			if string(stored) != tc.login || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("probe wrote login")
			}
		})
	}
}

// Risk: error text, redirects and oversized responses could leak login material;
// missing quota fields could masquerade as measured zero or successful refresh.
func TestProbeFailuresAreBoundedAndNeverRefreshOrFollowRedirects(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	home := fixtureHome(t, "auth.json", `{"tokens":{"access_token":"synthetic-login-value","account_id":"fixture-account"}}`)
	for _, tc := range []struct {
		status      int
		body, cause string
	}{
		{429, `{"error":"synthetic-login-value"}`, "rate_limited"},
		{401, `{"error":"synthetic-login-value"}`, "authentication_failed"},
		{302, "", "unavailable"},
		{200, strings.Repeat("x", maximum+1), "protocol"},
		{200, `{"rate_limit":{"primary_window":{"reset_at":2000000000,"limit_window_seconds":18000}}}`, "protocol"},
	} {
		calls := 0
		client := Client{HTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{"Location": []string{"https://example.invalid/stolen"}}}, nil
		})}}
		got := client.Capture(t.Context(), Target{Harness: "codex", Home: home}, now)
		if got.Cause != tc.cause || len(got.Readings) != 0 || got.Budget != nil || calls != 1 {
			t.Fatal("unsafe or incorrect error classification")
		}
	}
	bad := fixtureHome(t, "auth.json", `{"tokens":{"access_token":"synthetic-login-value"}}`)
	client := Client{HTTP: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("ambiguous account reached network")
		return nil, nil
	})}}
	if got := client.Capture(context.Background(), Target{Harness: "codex", Home: bad}, now); got.Cause != "authentication_failed" {
		t.Fatal("missing account accepted")
	}
	if got := client.Capture(t.Context(), Target{Harness: "cursor", Home: bad}, now); !reflect.DeepEqual(got, Result{Cause: "unsupported"}) {
		t.Fatal("headless Cursor guessed browser identity")
	}
}

func TestOpenRouterUnknownBalanceDoesNotInventCredit(t *testing.T) {
	now := time.Now().UTC()
	home := fixtureHome(t, "auth.json", `{"openrouter":{"type":"api_key","key":"synthetic-login-value"}}`)
	client := Client{HTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		status, body := 200, `{"data":{"usage":0,"limit":null,"limit_remaining":999}}`
		if r.URL.Path == "/api/v1/credits" {
			status, body = 403, `{"error":"management key needed"}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	got := client.Capture(t.Context(), Target{Harness: "pi", Home: home, Provider: "openrouter"}, now)
	if got.Cause != "" || got.Budget == nil || got.Budget.KeyRemainingUSD != nil || got.Budget.BalanceUSD != nil {
		t.Fatal("unknown cap or balance inferred from unrelated field")
	}
}

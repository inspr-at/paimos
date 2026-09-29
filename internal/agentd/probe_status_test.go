// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeStatus writes a vendor stand-in that prints out and exits with code.
func fakeStatus(t *testing.T, out string, code int) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "vendor")
	script := "#!/bin/sh\ncat <<'EOF'\n" + out + "\nEOF\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func privateHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

// AEON-299 review: only a confirmed sign-out is a sign-in problem; a probe that
// cannot run or cannot be read is "unavailable".
func TestProbeStatusSeparatesSignOutFromUnavailable(t *testing.T) {
	ctx := context.Background()
	home := privateHome(t)
	codex := func(out string, code int) ProbeStatus {
		a := &CodexAdapter{Path: fakeStatus(t, out, code), Homes: map[string]string{"k": home}, Emails: map[string]string{"k": "a@example.com"}}
		return a.ProbeStatus(ctx, "k")
	}
	claude := func(out string, code int) ProbeStatus {
		a := &ClaudeAdapter{ClaudePath: fakeStatus(t, out, code), Homes: map[string]string{"k": home}, Emails: map[string]string{"k": "a@example.com"}}
		return a.ProbeStatus(ctx, "k")
	}
	cursor := func(out string, code int) ProbeStatus {
		a := &CursorAdapter{Path: fakeStatus(t, out, code), Identities: map[string]string{"k": "42"}}
		return a.ProbeStatus(ctx, "k")
	}
	cases := []struct {
		name string
		got  ProbeStatus
		want ProbeStatus
	}{
		{"codex signed in", codex("Logged in using ChatGPT", 0), ProbeStatus{OK: true}},
		{"codex signed out", codex("Not logged in", 1), ProbeStatus{Failure: ProbeAuthFailed}},
		{"codex unreadable", codex("segmentation fault", 2), ProbeStatus{Failure: ProbeUnavailable}},
		{"codex signed in but failing", codex("Logged in using ChatGPT", 1), ProbeStatus{Failure: ProbeUnavailable}},
		{"claude signed in", claude(`{"loggedIn":true,"email":"a@example.com","authMethod":"claude.ai"}`, 0), ProbeStatus{OK: true}},
		{"claude signed out", claude(`{"loggedIn":false}`, 1), ProbeStatus{Failure: ProbeAuthFailed}},
		{"claude other identity", claude(`{"loggedIn":true,"email":"b@example.com","authMethod":"claude.ai"}`, 0), ProbeStatus{Failure: ProbeAuthFailed}},
		{"claude empty object", claude(`{}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"claude malformed", claude(`not json`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"cursor signed in", cursor(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":"42"}}`, 0), ProbeStatus{OK: true}},
		{"cursor signed out", cursor(`{"status":"unauthenticated","isAuthenticated":false}`, 1), ProbeStatus{Failure: ProbeAuthFailed}},
		{"cursor other user", cursor(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":"7"}}`, 0), ProbeStatus{Failure: ProbeAuthFailed}},
		{"cursor empty object", cursor(`{}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		// AEON-299 re-review: incomplete or unknown answers are never a sign-out.
		{"claude signed in without email", claude(`{"loggedIn":true}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"claude signed in with empty email", claude(`{"loggedIn":true,"email":"","authMethod":"claude.ai"}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"claude api key login", claude(`{"loggedIn":true,"email":"a@example.com","authMethod":"api_key"}`, 0), ProbeStatus{Failure: ProbeAuthFailed}},
		{"claude loggedIn null", claude(`{"loggedIn":null}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"cursor unknown status", cursor(`{"status":"refreshing","isAuthenticated":false}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"cursor logged out word", cursor(`{"status":"logged_out","isAuthenticated":false}`, 1), ProbeStatus{Failure: ProbeAuthFailed}},
		{"cursor contradictory signed in", cursor(`{"status":"unauthenticated","isAuthenticated":true,"userInfo":{"userId":"42"}}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"cursor contradictory signed out", cursor(`{"status":"authenticated","isAuthenticated":false}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"cursor null identity", cursor(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":null}}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"cursor missing identity", cursor(`{"status":"authenticated","isAuthenticated":true}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"cursor empty identity", cursor(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":""}}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"codex other message", codex("Logged in using an API key", 0), ProbeStatus{Failure: ProbeUnavailable}},
		// Round 3: the whole Codex answer must be one recognized line.
		{"codex contradictory lines", codex("Not logged in\nLogged in using ChatGPT", 1), ProbeStatus{Failure: ProbeUnavailable}},
		{"codex signed in then noise", codex("Logged in using ChatGPT\nwarning: update available", 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"codex signed out then noise", codex("Not logged in yet?", 1), ProbeStatus{Failure: ProbeUnavailable}},
		{"codex signed out with spaces", codex("  Not logged in  ", 1), ProbeStatus{Failure: ProbeAuthFailed}},
		// Round 3: Cursor identities are decoded and typed.
		{"cursor object identity", cursor(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":{}}}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"cursor array identity", cursor(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":[]}}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"cursor boolean identity", cursor(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":false}}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"cursor blank identity", cursor(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":"   "}}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"cursor fractional identity", cursor(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":42.5}}`, 0), ProbeStatus{Failure: ProbeUnavailable}},
		{"cursor numeric identity matches", cursor(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":42}}`, 0), ProbeStatus{OK: true}},
		{"cursor string identity matches", cursor(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":" 42 "}}`, 0), ProbeStatus{OK: true}},
		{"cursor numeric other account", cursor(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":7}}`, 0), ProbeStatus{Failure: ProbeAuthFailed}},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, c.got, c.want)
		}
	}
	missing := &CodexAdapter{Path: filepath.Join(home, "no-such-binary"), Homes: map[string]string{"k": home}, Emails: map[string]string{"k": "a@example.com"}}
	if got := missing.ProbeStatus(ctx, "k"); got.Failure != ProbeUnavailable {
		t.Fatalf("missing binary is not a sign-out: %+v", got)
	}
	// Adapters without a status prober never claim a sign-in problem.
	if got := probeAccount(ctx, &fakeAdapter{}, "k"); !got.OK {
		t.Fatalf("boolean prober ok: %+v", got)
	}
	if got := probeAccount(ctx, falseProber{}, "k"); got.Failure != ProbeUnavailable {
		t.Fatalf("boolean prober failure: %+v", got)
	}
}

type falseProber struct{}

func (falseProber) Probe(context.Context, string) bool { return false }

func TestRemoteProbeStatusSendsOnlyTheCause(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	remote := NewRemote(server.URL, "key")
	ctx := context.Background()
	if err := remote.ProbeStatus(ctx, "a", "d", "g", ProbeStatus{Failure: ProbeAuthFailed}); err != nil {
		t.Fatal(err)
	}
	if err := remote.Probe(ctx, "a", "d", "g", true); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0]["failure"] != ProbeAuthFailed || bodies[0]["available"] != false || bodies[1]["failure"] != nil || bodies[1]["available"] != true {
		t.Fatalf("probe bodies %#v", bodies)
	}
}

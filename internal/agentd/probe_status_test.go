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

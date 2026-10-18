// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestKeyScopesPersonSessionAndAtomicDelta(t *testing.T) {
	isolate(t)
	const keyID = "66666666-6666-4666-8666-666666666666"
	const session = "synthetic-person-session"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		cookie, err := r.Cookie("aeon_session")
		if err != nil || cookie.Value != session || r.Header.Get("Authorization") != "" {
			t.Error("person credentials not isolated from bearer credentials")
		}
		if r.Method != "PATCH" || r.URL.Path != "/api/agent-keys/"+keyID+"/scopes" {
			t.Error("incorrect endpoint")
		}
		var delta map[string][]string
		if json.NewDecoder(r.Body).Decode(&delta) != nil || !reflect.DeepEqual(delta, map[string][]string{"add": {"harness.worker", "harness.read", "events.read"}, "remove": {"nodes.write"}}) {
			t.Error("incorrect scope delta")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": keyID, "scopes": []string{"harness.worker"}, "token": "synthetic-response-secret"})
	}))
	defer srv.Close()
	code, out, stderr := runCLI([]string{"aeon", "--json", "keys", "scopes", keyID, "--add", "harness.worker,harness.read", "--add", "events.read", "--remove", "nodes.write", "--session-file", "-", "--url", srv.URL}, session)
	if code != 0 || stderr != "" || calls != 1 || !strings.Contains(out, "harness.worker") || strings.Contains(out, "secret") || strings.Contains(out, session) {
		t.Fatalf("scope command failed or exposed credentials (exit %d, calls %d)", code, calls)
	}
}

func TestKeyScopesRefusesRedirectAndRedactsErrors(t *testing.T) {
	isolate(t)
	const session = "synthetic-person-session"
	for _, status := range []int{403, 302} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Location", "/redirect-target")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": session})
		}))
		code, out, stderr := runCLI([]string{"aeon", "keys", "scopes", tagTranscriptID, "--add", "harness.worker", "--session-file", "-", "--url", srv.URL}, session)
		srv.Close()
		if code != 1 || calls != 1 || out != "" || strings.Contains(stderr, session) {
			t.Fatal("failed command followed redirect or exposed session")
		}
	}
}

func TestKeyScopesUsage(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{{"bad-id", "--add", "harness.worker"}, {tagTranscriptID}, {tagTranscriptID, "--add", "harness.worker", "--url", "https://example.invalid"}} {
		code, _, _ := runCLI(append([]string{"aeon", "keys", "scopes"}, args...), "")
		if code != 2 {
			t.Fatalf("usage exit %d", code)
		}
	}
}

func TestKeyScopesStdinErrorsNameSessionCookie(t *testing.T) {
	isolate(t)
	for _, input := range []string{"", strings.Repeat("x", 8<<10)} {
		code, _, stderr := runCLI([]string{"aeon", "keys", "scopes", tagTranscriptID, "--add", "nodes.read", "--session-file", "-", "--url", "https://example.invalid"}, input)
		if code != 2 || !strings.Contains(stderr, "session cookie") || strings.Contains(stderr, "API key") {
			t.Fatalf("session input error has the wrong credential label: exit=%d stderr=%s", code, stderr)
		}
	}
}

func TestKeyAdoptPersonSessionMetadataAndRefusal(t *testing.T) {
	isolate(t)
	const session = "synthetic-person-session"
	const owner = "11111111-1111-4111-8111-111111111111"
	for _, status := range []int{200, 403, 409, 302} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			cookie, err := r.Cookie("aeon_session")
			if err != nil || cookie.Value != session || r.Header.Get("Authorization") != "" || r.Header.Get("Origin") == "" || r.Method != "POST" || r.URL.Path != "/api/agent-keys/"+tagTranscriptID+"/adopt" {
				t.Error("incorrect adoption request")
			}
			w.Header().Set("Location", "/redirect")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": tagTranscriptID, "created_by_principal_id": owner, "token": "synthetic-response-secret", "error": session})
		}))
		code, out, stderr := runCLI([]string{"aeon", "--json", "keys", "adopt", tagTranscriptID, "--session-file", "-", "--url", srv.URL}, session)
		srv.Close()
		if calls != 1 || strings.Contains(out, "secret") || strings.Contains(out, session) || strings.Contains(stderr, session) {
			t.Fatal("adopt disclosed credentials or followed redirect")
		}
		if status == 200 {
			if code != 0 || !strings.Contains(out, owner) {
				t.Fatal("adopt failed or lost owner metadata")
			}
		} else if code != 1 || out != "" {
			t.Fatal("adopt falsely reported success")
		}
	}
	for _, args := range [][]string{{tagTranscriptID}, {"bad-id", "--session-file", "-"}} {
		code, _, _ := runCLI(append([]string{"aeon", "keys", "adopt"}, args...), session)
		if code != 2 {
			t.Fatalf("usage exit=%d", code)
		}
	}
}

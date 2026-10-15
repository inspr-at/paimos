// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Risk: a one-time assistant secret is echoed with JSON/errors, written into an
// existing/symlink file or a world-readable file, or confused with rotation ID.
func TestMessagingAssistantCLISecretOutput(t *testing.T) {
	isolate(t)
	const secret = "aeon_fixture_once-only-secret"
	const id = "11111111-1111-4111-8111-111111111111"
	var calls int
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if fail {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"` + secret + `"}`))
			return
		}
		if r.Method == "DELETE" {
			if r.URL.Path != "/api/agents/assistants/"+id {
				t.Error("wrong revoke route")
			}
			w.WriteHeader(204)
			return
		}
		var body struct {
			Name    string    `json:"name"`
			Expires time.Time `json:"expires_at"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		if body.Expires.IsZero() || body.Expires.After(time.Now().Add(30*24*time.Hour)) {
			t.Error("expiry omitted or too long")
		}
		if r.URL.Path == "/api/agents/assistants" {
			if body.Name != "Amy" {
				t.Error("name missing")
			}
		} else if r.URL.Path != "/api/agents/assistants/"+id+"/rotate" || body.Name != "" {
			t.Error("wrong renewal request")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "principal_id": id, "token": secret, "scopes": []string{"inbox.read", "inbox.send", "inbox.receipt"}, "expires_at": body.Expires})
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	base := []string{"aeon", "agents", "assistant", "create", "--name", "Amy", "--expires", "30d", "--key-out"}
	code, out, errOut := runCLI(append(assistantArgs(base), "-", "--json"), "")
	if code != 0 || out != secret+"\n" || strings.Contains(errOut, secret) {
		t.Fatalf("pipe output failed (code %d)", code)
	}
	file := filepath.Join(t.TempDir(), "assistant-token")
	code, out, errOut = runCLI(append(assistantArgs(base), file, "--json"), "")
	if code != 0 || strings.Contains(out+errOut, secret) || !strings.Contains(out, id) {
		t.Fatalf("file metadata failed (code %d)", code)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != secret+"\n" {
		t.Fatal("file did not receive key")
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("key file permissions differ from 0600")
	}
	before := calls
	if code, _, _ := runCLI(append(assistantArgs(base), file), ""); code == 0 || calls != before {
		t.Fatal("existing file was overwritten or request issued")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCLI(append(assistantArgs(base), link), ""); code == 0 || calls != before {
		t.Fatal("symlink accepted")
	}
	for _, expiry := range []string{"", "31d", "0d", "999999999999999999999d", "721h"} {
		args := assistantArgs(base)
		args[7] = expiry
		if code, _, _ := runCLI(append(args, "-"), ""); code != 2 {
			t.Fatalf("invalid expiry accepted: %q (%d)", expiry, code)
		}
	}
	code, out, errOut = runCLI([]string{"aeon", "agents", "assistant", "rotate", id, "--expires", "7d", "--key-out", "-"}, "")
	if code != 0 || out != secret+"\n" || strings.Contains(errOut, secret) {
		t.Fatal("rotation output failed")
	}
	code, _, _ = runCLI([]string{"aeon", "agents", "assistant", "revoke", id}, "")
	if code != 0 {
		t.Fatal("revoke failed")
	}
	fail = true
	code, out, errOut = runCLI(append(assistantArgs(base), "-"), "")
	if code == 0 || strings.Contains(out+errOut, secret) {
		t.Fatal("server error echoed secret")
	}
}

func assistantArgs(in []string) []string { return append([]string{}, in...) }

// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestAEON1254HookDoctorClassifiesCLIAndInstance(t *testing.T) {
	isolate(t)
	work, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(work)
	executable, _ := os.Executable()
	settings := filepath.Join(work, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ binary, instance, want string }{
		{executable, "ppm", ""}, {filepath.Join(work, "older-aeon"), "ppm", "hook_cli_mismatch"}, {executable, "other", "hook_instance_mismatch"},
	} {
		command := shellHookQuote(tc.binary) + " --instance " + shellHookQuote(tc.instance) + " hook claude Stop" + inboxHookMarker
		raw, _ := json.Marshal(map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}}})
		if err := os.WriteFile(settings, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if got := inboxHookCheck(work, home, executable, "ppm"); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
	for _, tc := range []struct{ name, settings, code, status string }{
		{"rules_only", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"aeon session start --rules-out rules.txt"}]}]}}`, "hook_unavailable", "ok"},
		{"malformed_settings", `{"hooks":`, "hook_unavailable", "warn"},
		{"relative_cli", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"aeon hook claude Stop"}]}]}}`, "hook_unavailable", "warn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(settings, []byte(tc.settings), 0600); err != nil {
				t.Fatal(err)
			}
			if got := inboxHookCheck(work, home, executable, "ppm"); got != tc.code {
				t.Fatalf("heartbeat refusal = %q, want %q", got, tc.code)
			}
			check := new(runtime).inboxDoctorCheck("ppm", "https://fixture.invalid", indexAeonID)
			if check.Status != tc.status {
				t.Fatalf("doctor status = %q, want %q: %s", check.Status, tc.status, check.Detail)
			}
			if tc.status == "ok" && check.Detail != "generic Claude inbox hook not installed" {
				t.Fatalf("doctor claimed a rules-only setup was bound: %s", check.Detail)
			}
		})
	}
}

func TestAEON1254GenericHookUsesIndexedAccountAcrossSuccessor(t *testing.T) {
	// Risk: a hook without --instance follows the default account, and a
	// failed paired binding intercepts the session's own inbox. The index
	// names the successor, not the predecessor, and stdout precedes ack.
	isolate(t)
	useIndexHome(t)
	t.Setenv("HOME", canonicalPrivatePath(os.Getenv("HOME")))
	var output bytes.Buffer
	acked, reads := false, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"principal": map[string]any{"id": indexAeonAlt}})
		case "/api/inbox/messages":
			reads++
			if r.URL.Query().Get("session") != indexAeonID || r.URL.Query().Get("exact_session") != "true" {
				t.Error("hook lost the successor's exact binding")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": indexMessage, "body": "What are you working on?", "recipient_session_id": indexAeonID}}, "next_after": 0})
		case "/api/inbox/messages/" + indexMessage + "/ack":
			if !strings.Contains(output.String(), "What are you working on?") {
				t.Error("ack preceded foreground stdout handoff")
			}
			acked = true
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected hook call: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	wrong := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("hook used the default instance") }))
	defer wrong.Close()
	config := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(config, []byte("default_instance: other\ninstances:\n  ppm:\n    url: "+srv.URL+"\n  other:\n    url: "+wrong.URL+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeKey(keyPath(config, "ppm"), testKey); err != nil {
		t.Fatal(err)
	}
	if err := writeKey(keyPath(config, "other"), testKey); err != nil {
		t.Fatal(err)
	}
	predecessor := t.TempDir()
	writeIndexState(t, predecessor, indexOtherID, "Predecessor", "")
	publishIndex(t, indexSourceID, predecessor)
	writeIndexState(t, predecessor, indexOtherID, "Predecessor", `"closed":true`)
	dir := t.TempDir()
	metadata, _ := json.Marshal(map[string]string{"hook_origin": srv.URL, "hook_instance": "ppm", "hook_config": config, "hook_principal": indexAeonAlt})
	writeIndexState(t, dir, indexAeonID, "Successor", strings.TrimSuffix(strings.TrimPrefix(string(metadata), "{"), "}"))
	publishIndex(t, indexSourceID, dir)
	home, _ := os.UserHomeDir()
	root, _ := agentsetup.DefaultStateRoot(goruntime.GOOS, home, os.Getenv("XDG_STATE_HOME"))
	store, err := agentsetup.OpenStore(root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	paired, _ := json.Marshal(map[string]string{"origin": srv.URL, "principal_id": indexOtherID})
	if err := store.Write("runtime.json", paired, false); err != nil {
		t.Fatal(err)
	}
	if got := pairedBindingCheck(srv.URL, indexAeonAlt); got != "principal_mismatch" {
		t.Fatalf("paired refusal %q", got)
	}
	if got := pairedBindingCheck(wrong.URL, indexAeonAlt); got != "instance_mismatch" {
		t.Fatalf("instance refusal %q", got)
	}
	rt := &runtime{program: "aeon", configPath: config, stdin: strings.NewReader(`{"session_id":"` + indexSourceID + `","hook_event_name":"Stop"}`), stdout: &output}
	if err := rt.runInboxHook(t.Context(), "Stop"); err != nil {
		t.Fatal(err)
	}
	if !acked || reads != 1 || !strings.Contains(output.String(), `"decision":"block"`) {
		t.Fatal("generic hook did not hand off the successor's inbox")
	}
	other := &runtime{program: "aeon", configPath: config, instance: "other"}
	if _, err := other.selectIndexedHook(t.Context(), indexSourceID, indexAeonID); err == nil {
		t.Fatal("explicit conflicting instance was silently replaced")
	}
}

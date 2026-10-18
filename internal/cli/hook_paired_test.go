// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type unreadablePairedInput struct{ t *testing.T }

func (r unreadablePairedInput) Read([]byte) (int, error) {
	r.t.Fatal("unqualified paired hook read stdin")
	return 0, nil
}

func TestLegacyInstallerCannotAddConsumerBesidePairedHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"aeon hook claude Stop --paired # aeon-inbox-hook-v2"}]}]}}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rt := &runtime{stdout: &out}
	if rt.mergeInboxHooks(path, "aeon hook claude ", "claude", false, false) == nil {
		t.Fatal("legacy installer bypassed ownership")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(raw, after) {
		t.Fatal("paired entry overwritten")
	}
}

func TestPairedHookNeverFallsBackToLegacyAPI(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("paired hook used network credentials") }))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", "fixture-private-canary-NOT-A-CREDENTIAL-391")
	t.Setenv("AEON_SESSION_ID", hookSessionID)
	for _, h := range []string{"claude", "codex"} {
		var out, diag bytes.Buffer
		code := RunMessaging([]string{"aeon", "--config", "/must-not-read-fixture", "hook", h, "PostToolUse", "--paired"}, unreadablePairedInput{t}, &out, &diag)
		if code != 0 || out.Len() != 0 || !strings.Contains(diag.String(), "qualification_pending") || strings.Contains(diag.String(), "canary") {
			t.Fatal("paired no-op did not fail closed", code, diag.String())
		}
	}
}

func TestPairedHookInstallRefusesProjectBeforeReadingState(t *testing.T) {
	var out, diag bytes.Buffer
	code := RunMessaging([]string{"aeon", "hook", "install", "--paired", "--harness", "claude", "--scope", "project", "--setup-root", "/must-not-read-fixture"}, strings.NewReader(""), &out, &diag)
	if code == 0 || !strings.Contains(diag.String(), "project_scope") || out.Len() != 0 {
		t.Fatal("project scope not refused", diag.String())
	}
}

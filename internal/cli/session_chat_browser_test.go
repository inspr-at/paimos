// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/nodes"
)

func TestSessionChatCLIHelper(t *testing.T) {
	if os.Getenv("AEON_CHAT_CLI_HELPER") != "1" {
		t.Skip("child CLI for session chat browser test")
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(RunMessaging(append([]string{"aeon"}, os.Args[i+1:]...), os.Stdin, os.Stdout, os.Stderr))
		}
	}
	t.Fatal("missing CLI arguments")
}

// Opt in with AEON_SESSION_CHAT_BROWSER=1. The UI scaffold is deterministic;
// sends, CLI binding, replies, reads and receipts use real handlers and Postgres.
func TestSessionChatBrowser(t *testing.T) {
	if os.Getenv("AEON_SESSION_CHAT_BROWSER") != "1" {
		t.Skip("opt-in Playwright integration")
	}
	isolate(t)
	opened := dbtest.Open(t)
	if err := db.EnsureTenant(t.Context(), opened.App, "aeon", "Aeon"); err != nil {
		t.Fatal(err)
	}
	authMod, err := auth.New(auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{9}, 32), PublicURL: "http://127.0.0.1", BootstrapTenantSlug: "aeon", BootstrapAdminEmail: "admin@example.com"}, opened.App)
	if err != nil {
		t.Fatal(err)
	}
	messaging, err := inbox.NewMessaging(opened.App, bytes.Repeat([]byte{6}, 32))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&httpapi.Server{Pool: opened.App, Modules: []httpapi.Module{authMod, nodes.New(opened.App, nodes.SQLWriter{}), harness.New(opened.App), inbox.New(opened.App), messaging}, Middleware: []func(http.Handler) http.Handler{authMod.Middleware}}).Handler())
	t.Cleanup(srv.Close)
	agent := mintAgent(t, srv.URL, "session-chat-worker")
	seedProject(t, srv.URL, agent.Token)
	var project string
	if err := opened.Admin.QueryRow(t.Context(), `SELECT id::text FROM nodes WHERE tenant_id=$1 AND fields->>'project_key'='AEON'`, agent.TenantID).Scan(&project); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	wrapper := filepath.Join(dir, "aeon")
	if err := os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nexec %s -test.run='^TestSessionChatCLIHelper$' -- --config %s \"$@\"\n", quote(self), quote(filepath.Join(dir, "missing-config")))), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", agent.Token)
	t.Setenv("AEON_CHAT_CLI_HELPER", "1")
	t.Setenv("AEON_CHAT_CLI", wrapper)
	t.Setenv("AEON_CHAT_DIR", dir)
	t.Setenv("AEON_CHAT_PROJECT", project)
	t.Setenv("AEON_CHAT_AGENT", agent.PrincipalID)
	cmd := exec.CommandContext(t.Context(), "npx", "playwright", "test", "-c", "playwright.ui.config.ts", "tests/session-chat-cli.spec.ts", "--workers=1")
	cmd.Dir = filepath.Join("..", "..", "web")
	output, err := cmd.CombinedOutput()
	if bytes.Contains(output, []byte(agent.Token)) {
		t.Fatal("browser output contained a test credential; suppressed")
	}
	t.Log(string(output))
	if err != nil {
		t.Fatal("session chat browser check failed")
	}
}

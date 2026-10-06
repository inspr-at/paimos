// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/activity"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nodes"
)

func TestMigratedWorkCLICompletionGate(t *testing.T) {
	isolate(t)
	opened := dbtest.Open(t)
	if err := db.EnsureTenant(t.Context(), opened.App, "aeon", "Aeon"); err != nil {
		t.Fatal(err)
	}
	authMod, err := auth.New(auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{9}, 32), PublicURL: "http://127.0.0.1", BootstrapTenantSlug: "aeon", BootstrapAdminEmail: "admin@example.com"}, opened.App)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&httpapi.Server{Pool: opened.App, Modules: []httpapi.Module{authMod, nodes.New(opened.App, nodes.SQLWriter{}), activity.New(opened.App)}, Middleware: []func(http.Handler) http.Handler{authMod.Middleware}}).Handler())
	t.Cleanup(srv.Close)
	worker := mintAgent(t, srv.URL, "completion-worker")
	seedProject(t, srv.URL, worker.Token)
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", worker.Token)
	missing := filepath.Join(t.TempDir(), "no-config.yaml")
	invoke := func(args ...string) (int, string, string) {
		t.Helper()
		return runCLI(append([]string{"paimos", "--config", missing, "--json", "issue"}, args...), "")
	}
	read := func(t *testing.T, key string) issueView {
		t.Helper()
		code, out, stderr := invoke("get", key)
		var got issueView
		if code != 0 || json.Unmarshal([]byte(out), &got) != nil {
			t.Fatalf("get work: code %d, %s", code, stderr)
		}
		return got
	}
	for _, alias := range []string{"ticket", "task", "epic"} {
		for _, state := range []string{"done", "accepted", "delivered"} {
			t.Run(alias+"/"+state, func(t *testing.T) {
				code, out, stderr := invoke("create", "--project", "AEON", "--type", alias, "--title", "Completion "+alias+" "+state, "--estimate", "1h")
				var created issueView
				if code != 0 || json.Unmarshal([]byte(out), &created) != nil || created.IssueKey == "" {
					t.Fatalf("create alias: code %d, %s", code, stderr)
				}
				before := read(t, created.IssueKey)
				if before.Type != "work" || before.Status != "open" {
					t.Fatalf("alias did not create an open work item: type %s, state %s", before.Type, before.Status)
				}
				code, _, stderr = invoke("update", created.IssueKey, "--status", state, "--hide-from-release-notes", "true")
				if code == 0 || !strings.Contains(stderr, "before done:") || !strings.Contains(stderr, "benefit_de is required") {
					t.Fatalf("missing benefits were not refused: code %d, %s", code, stderr)
				}
				after := read(t, created.IssueKey)
				if after.Status != before.Status || after.Hide != before.Hide {
					t.Fatal("rejected completion partially changed the work item")
				}
				code, _, stderr = invoke("update", created.IssueKey, "--status", state, "--pill-en", "Clear release notes", "--pill-de", "Klare Release Notes", "--benefit-en", "Work explains its benefit.", "--benefit-de", "Arbeit erklärt ihren Nutzen.", "--hide-from-release-notes", "true")
				if code != 0 {
					t.Fatalf("valid completion refused: code %d, %s", code, stderr)
				}
				after = read(t, created.IssueKey)
				if after.Status != state || after.BenefitDE != "Arbeit erklärt ihren Nutzen." || !after.Hide {
					t.Fatal("completed work lost its status or benefit fields")
				}
			})
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/activity"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/workorders"
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
	seedCompletionDefinition(t, srv.URL)
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
				code, _, stderr = invoke("update", created.IssueKey, "--status", state, "--hide-from-release-notes", "false")
				if code == 0 || !strings.Contains(stderr, "entry_checks_failed") || !strings.Contains(stderr, "benefit_de is required") {
					t.Fatalf("missing benefits were not refused: code %d, %s", code, stderr)
				}
				after := read(t, created.IssueKey)
				if after.Status != before.Status || after.Hide != before.Hide {
					t.Fatal("rejected completion partially changed the work item")
				}
				// The approved hidden exemption needs no filler release copy.
				code, _, stderr = invoke("update", created.IssueKey, "--status", state, "--hide-from-release-notes", "true")
				if code != 0 {
					t.Fatalf("hidden exemption refused: code %d, %s", code, stderr)
				}
				if hidden := read(t, created.IssueKey); hidden.Status != state || !hidden.Hide || hidden.BenefitDE != "" {
					t.Fatal("hidden completion added filler copy or lost its state")
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
	t.Run("no-release-needed", func(t *testing.T) {
		code, out, stderr := invoke("create", "--project", "AEON", "--title", "Scene edit", "--estimate", "1h", "--no-release-needed", "true")
		var created issueView
		if code != 0 || json.Unmarshal([]byte(out), &created) != nil || !created.NoReleaseNeeded {
			t.Fatalf("content create: %d %s %s", code, out, stderr)
		}
		if got := read(t, created.IssueKey); !got.NoReleaseNeeded || got.Hide {
			t.Fatalf("content flag round trip: %+v", got)
		}
		code, _, stderr = invoke("update", created.IssueKey, "--no-release-needed", "invalid")
		if code == 0 || !strings.Contains(stderr, "--no-release-needed must be true or false") {
			t.Fatalf("invalid mark: %d %s", code, stderr)
		}
		code, _, stderr = invoke("update", created.IssueKey, "--no-release-needed", "false")
		if code != 0 {
			t.Fatalf("clear content mark: %d %s", code, stderr)
		}
		if got := read(t, created.IssueKey); got.NoReleaseNeeded || got.Hide || got.Title != created.Title {
			t.Fatalf("clear lost fields: %+v", got)
		}
	})

}

// Risk: refused work hides repairable rules, or a writer's preview leaks another
// project's definition. Exercise bearer auth, the write and preview transactions,
// and both shipped CLI renderers against the same real entry-check definition.
func TestEntryRefusalReasonsAndWriterPreview(t *testing.T) {
	isolate(t)
	opened := dbtest.Open(t)
	if err := db.EnsureTenant(t.Context(), opened.App, "aeon", "Aeon"); err != nil {
		t.Fatal(err)
	}
	authMod, err := auth.New(auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{9}, 32), PublicURL: "http://127.0.0.1", BootstrapTenantSlug: "aeon", BootstrapAdminEmail: "admin@example.com"}, opened.App)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&httpapi.Server{Pool: opened.App, Modules: []httpapi.Module{authMod, nodes.New(opened.App, nodes.SQLWriter{})}, Middleware: []func(http.Handler) http.Handler{authMod.Middleware}}).Handler())
	t.Cleanup(srv.Close)
	worker := mintAgent(t, srv.URL, "entry-worker")
	seedProject(t, srv.URL, worker.Token)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	person := &http.Client{Jar: jar}
	request := func(hc *http.Client, token, method, path, body string, want int) []byte {
		t.Helper()
		header := http.Header{}
		if token != "" {
			header.Set("Authorization", "Bearer "+token)
		}
		code, raw := doJSON(t, hc, method, srv.URL+path, body, header)
		if code != want {
			t.Fatalf("%s %s: status %d, want %d, body %s", method, path, code, want, raw)
		}
		return raw
	}
	request(person, "", "POST", "/api/auth/dev-login", `{"email":"admin@example.com","tenant":"aeon"}`, 200)
	// Include a passing rule and an inapplicable rule to reject extra checks.
	request(person, "", "PUT", "/api/settings/definitions", `{"expected_revision":null,"checks":{"in_progress":[{"id":"estimate_set"},{"id":"acceptance_present"},{"id":"blocker_named","condition":"all"},{"id":"design_attached"}],"done":[{"id":"pills_benefits_set"},{"id":"manual:person-signoff","label":"Person sign-off","label_de":"Persönliche Freigabe"}]}}`, 200)
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", worker.Token)
	missing := filepath.Join(t.TempDir(), "no-config.yaml")
	invoke := func(jsonOut bool, args ...string) (int, string, string) {
		t.Helper()
		argv := []string{"aeon", "--config", missing}
		if jsonOut {
			argv = append(argv, "--json")
		}
		argv = append(argv, "issue")
		argv = append(argv, args...)
		var out, stderr bytes.Buffer
		code := RunMessaging(argv, strings.NewReader(""), &out, &stderr)
		return code, out.String(), stderr.String()
	}
	code, out, stderr := invoke(true, "create", "--project", "AEON", "--title", "Explain every refusal")
	var created issueView
	if code != 0 || json.Unmarshal([]byte(out), &created) != nil || created.ID == "" {
		t.Fatalf("create: %d %s %s", code, out, stderr)
	}
	path := "/api/nodes/" + created.ID
	raw := request(person, "", "GET", path, "", 200)
	var node struct {
		ProjectID string `json:"project_id"`
		KindID    string `json:"kind_id"`
	}
	if err := json.Unmarshal(raw, &node); err != nil || node.ProjectID == "" {
		t.Fatal("ticket has no project", err)
	}
	request(person, "", "PATCH", path, `{"fields":{"blocker":"Follow-up ticket named"}}`, 200)
	// A key with write authority only in this project must still read the
	// preview after being refused. No general nodes.read scope is added.
	raw = request(person, "", "POST", "/api/agent-keys", `{"name":"entry-writer","scopes":["nodes.write"]}`, 201)
	var writer struct {
		Token       string `json:"token"`
		PrincipalID string `json:"principal_id"`
	}
	if err := json.Unmarshal(raw, &writer); err != nil || writer.PrincipalID == "" {
		t.Fatal("writer key fixture missing", err)
	}
	if _, err := opened.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, worker.TenantID, writer.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err := opened.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, worker.TenantID, writer.PrincipalID, node.ProjectID); err != nil {
		t.Fatal(err)
	}
	assertRefusal := func(raw []byte, state string, ids ...string) []workorders.EntryResult {
		t.Helper()
		var failure struct {
			Error           string                   `json:"error"`
			Code            string                   `json:"code"`
			ToStatus        string                   `json:"to_status"`
			Checks          []workorders.EntryResult `json:"checks"`
			Failed          []workorders.EntryResult `json:"failed"`
			OverrideAllowed bool                     `json:"override_allowed"`
		}
		if json.Unmarshal(raw, &failure) != nil || failure.Error != "entry_checks_failed" || failure.Code != failure.Error || failure.ToStatus != state || failure.OverrideAllowed || len(failure.Checks) != len(ids) || !reflect.DeepEqual(failure.Checks, failure.Failed) {
			t.Fatalf("wrong refusal: %s", raw)
		}
		for i, check := range failure.Checks {
			if check.ID != ids[i] || check.Label == "" || check.LabelDE == "" || check.Reason == "" || !check.Applicable || check.Passed || check.CanTick {
				t.Fatalf("missing or extra check details: %+v", check)
			}
		}
		return failure.Checks
	}
	httpClient := &http.Client{}
	for _, tc := range []struct {
		state string
		ids   []string
	}{
		{"in_progress", []string{"estimate_set", "acceptance_present"}},
		{"done", []string{"pills_benefits_set", "manual:person-signoff"}},
	} {
		t.Run(tc.state, func(t *testing.T) {
			body := fmt.Sprintf(`{"state":%q}`, tc.state)
			checks := assertRefusal(request(httpClient, writer.Token, "PATCH", path, body, 409), tc.state, tc.ids...)
			var preview workorders.EntryChecks
			raw := request(httpClient, writer.Token, "GET", path+"/entry-checks", "", 200)
			if json.Unmarshal(raw, &preview) != nil || preview.NodeID != created.ID {
				t.Fatalf("wrong ticket preview: %s", raw)
			}
			var failing []workorders.EntryResult
			for _, block := range preview.Blocks {
				if block.ToStatus == tc.state {
					for _, check := range block.Checks {
						if check.Applicable && !check.Passed {
							failing = append(failing, check)
						}
					}
				}
			}
			if !reflect.DeepEqual(checks, failing) {
				t.Fatalf("refusal differs from live preview: %+v / %+v", checks, failing)
			}
			for _, jsonOut := range []bool{false, true} {
				code, out, stderr := invoke(jsonOut, "update", created.IssueKey, "--status", tc.state)
				if code != 1 || out != "" {
					t.Fatalf("refusal exit/output: %d %s %s", code, out, stderr)
				}
				if jsonOut {
					cliChecks := assertRefusal([]byte(stderr), tc.state, tc.ids...)
					if !reflect.DeepEqual(cliChecks, checks) {
						t.Fatal("JSON lost full check details")
					}
				} else {
					if !strings.HasPrefix(stderr, "aeon: api 409: entry_checks_failed\n") {
						t.Fatalf("missing refusal: %s", stderr)
					}
					lines := strings.Split(strings.TrimSpace(stderr), "\n")
					if len(lines) != len(checks)+1 {
						t.Fatalf("checks not on separate lines: %s", stderr)
					}
					for i, check := range checks {
						if lines[i+1] != "✗ "+check.Label+": "+check.Reason {
							t.Fatalf("check missing from CLI: %s", stderr)
						}
					}
				}
			}
			if tc.state == "done" && (checks[1].Owner == nil || checks[1].Owner.Name == "") {
				t.Fatal("person-only check lost its owner")
			}
		})
	}
	t.Run("structured-error-redaction", func(t *testing.T) {
		const cookie = "synthetic-entry-session"
		refusal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "entry_checks_failed", "code": "entry_checks_failed", "to_status": "done",
				"checks": []map[string]string{{"id": "manual:person-signoff", "label": "Person sign-off", "label_de": "Persönliche Freigabe", "reason": "Needs " + cookie}},
				"extra":  "Retain full body; redact " + cookie,
			})
		}))
		defer refusal.Close()
		for _, jsonOut := range []bool{false, true} {
			var stderr bytes.Buffer
			rt := &runtime{program: "aeon", stderr: &stderr, jsonOut: jsonOut, personClient: client.NewSession(refusal.URL, cookie)}
			err := rt.do(http.MethodPatch, path, map[string]string{"state": "done"}, nil)
			exit, ok := err.(*exitError)
			if !ok || exit.code != 1 || exit.apiStatus != 409 {
				t.Fatal("structured refusal lost its failure status")
			}
			rt.printErr(exit)
			if strings.Contains(stderr.String(), cookie) || !strings.Contains(stderr.String(), "[redacted]") {
				t.Fatal("structured refusal exposed its credential")
			}
			if jsonOut {
				var body map[string]json.RawMessage
				if json.Unmarshal(stderr.Bytes(), &body) != nil || !strings.Contains(string(body["extra"]), "Retain full body") || !strings.Contains(string(body["checks"]), "Persönliche Freigabe") {
					t.Fatal("JSON refusal lost full body fields")
				}
			}
		}
	})
	// Ordinary reads, governance reads and ticking keep their existing gates.
	for _, other := range []string{path, "/api/projects/" + node.ProjectID + "/definitions", path + "/person-steps"} {
		request(httpClient, writer.Token, "GET", other, "", 403)
	}
	request(httpClient, writer.Token, "PUT", path+"/entry-checks/manual:person-signoff", `{"checked":true,"expected_revision":"2026-01-01T00:00:00Z"}`, 403)
	var projectKind string
	if err := opened.Admin.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE tenant_id=$1 AND slug='project'`, worker.TenantID).Scan(&projectKind); err != nil {
		t.Fatal(err)
	}
	raw = request(person, "", "POST", "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Other project","fields":{"project_key":"OTHER"}}`, projectKind), 201)
	var otherProject struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &otherProject); err != nil {
		t.Fatal(err)
	}
	raw = request(person, "", "POST", "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"title":"Other ticket"}`, node.KindID, otherProject.ID), 201)
	var other struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &other); err != nil || other.ID == "" {
		t.Fatal("other ticket fixture missing", err)
	}
	request(httpClient, writer.Token, "PATCH", "/api/nodes/"+other.ID, `{"state":"in_progress"}`, 403)
	raw = request(httpClient, writer.Token, "GET", "/api/nodes/"+other.ID+"/entry-checks", "", 403)
	if strings.Contains(string(raw), "estimate_set") || strings.Contains(string(raw), "Other ticket") {
		t.Fatal("forbidden preview leaked ticket checks")
	}
	raw = request(person, "", "GET", path, "", 200)
	var after struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &after) != nil || after.State != "open" {
		t.Fatal("refused write changed the ticket")
	}
}

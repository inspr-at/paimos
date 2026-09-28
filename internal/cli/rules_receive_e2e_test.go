// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/jackc/pgx/v5"
)

// Real auth/RLS, publication, CLI registration and receipt append/CAS, entirely
// on the workstation's disposable database. Simulate a committed but lost reply.
func TestRulesReceiveRegisteredGenerationEndToEnd(t *testing.T) {
	isolate(t)
	d := dbtest.Open(t)
	if err := db.EnsureTenant(t.Context(), d.App, "aeon", "Aeon"); err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{9}, 32), PublicURL: "http://127.0.0.1", BootstrapTenantSlug: "aeon", BootstrapAdminEmail: "admin@example.com"}, d.App)
	if err != nil {
		t.Fatal(err)
	}
	api := &httpapi.Server{Pool: d.App, Modules: []httpapi.Module{a, nodes.New(d.App, nodes.SQLWriter{}), harness.New(d.App), rules.New(d.App)}, Middleware: []func(http.Handler) http.Handler{a.Middleware}}
	handler := api.Handler()
	var drop atomic.Bool
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rules-receipts") {
			posts.Add(1)
			if drop.Swap(false) {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, r)
				if rec.Code != 200 {
					t.Errorf("real receipt write failed: %d", rec.Code)
				}
				w.WriteHeader(200)
				w.Write([]byte("lost response"))
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	call := func(method, path string, body any, headers http.Header) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		code, out := doJSON(t, hc, method, srv.URL+path, string(raw), headers)
		if code < 200 || code >= 300 {
			t.Fatalf("fixture %s %s: %d %s", method, path, code, out)
		}
		return out
	}
	var owner client.Me
	json.Unmarshal(call("POST", "/api/auth/dev-login", map[string]string{"email": "admin@example.com", "tenant": "aeon"}, nil), &owner)
	var key struct {
		Token       string `json:"token"`
		PrincipalID string `json:"principal_id"`
	}
	json.Unmarshal(call("POST", "/api/agent-keys", map[string]any{"name": "receive-worker", "scopes": []string{"account.manage", "kinds.read", "nodes.read", "nodes.write", "harness.read", "harness.write", "harness.worker", "rules.read"}}, nil), &key)
	seedProject(t, srv.URL, key.Token)
	var project string
	if err = db.InTenant(dbtest.Seed(t.Context()), d.App, owner.Tenant.ID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE k.slug='project'`).Scan(&project)
	}); err != nil {
		t.Fatal(err)
	}
	var layer rules.Layer
	json.Unmarshal(call("POST", "/api/rules/layers", rules.Scope{Layer: "company"}, nil), &layer)
	var set rules.Set
	json.Unmarshal(call("POST", "/api/rules/sets", map[string]any{"layer_id": layer.ID, "name": "Synthetic floor"}, nil), &set)
	call("PUT", "/api/rules/sets/"+set.ID+"/draft", map[string]any{"expected_revision": 1, "name": "Synthetic floor", "rules": []rules.Rule{{Identity: "safety", Text: "Preserve safety.", Why: "fixture", Strength: "locked", Enabled: true, Source: rules.Source{Reference: "synthetic"}}}}, nil)
	call("POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260928100000.0.0"}, nil)
	dir := t.TempDir()
	leaseFile, refFile := filepath.Join(dir, "lease"), filepath.Join(dir, "ref")
	os.WriteFile(leaseFile, []byte(receiveLease), 0600)
	os.WriteFile(refFile, []byte("synthetic-receive-harness-reference"), 0600)
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", key.Token)
	base := []string{"paimos", "--config", filepath.Join(dir, "missing"), "--json"}
	code, out, stderr := runCLI(append(append([]string{}, base...), "harness", "register", "--project", "AEON", "--agent", "receive-worker", "--harness", "codex", "--host", "workstation-fixture", "--harness-session-file", refFile, "--worker-lease-file", leaseFile), "")
	if code != 0 {
		t.Fatalf("registration: %s", stderr)
	}
	var session harness.Session
	if err = json.Unmarshal([]byte(out), &session); err != nil || session.ID == "" {
		t.Fatal("registration response", err)
	}
	clientAPI := client.New(srv.URL, key.Token)
	path := harnessPath(project, session.ID)
	headers := map[string]string{"X-Aeon-Worker-Lease": receiveLease}
	items := []map[string]any{}
	for _, v := range [][2]string{{"agents", "AGENTS.md"}, {"claude", "CLAUDE.md"}, {"skill", "demo/SKILL.md"}} {
		items = append(items, map[string]any{"kind": v[0], "logical_name": v[1], "hash_kind": "content", "content_sha256": strings.Repeat("b", 64), "byte_size": 12})
	}
	if err = clientAPI.DoWithHeaders(t.Context(), "POST", path+"/provenance", map[string]any{"items": items}, nil, headers); err != nil {
		t.Fatal(err)
	}
	var before json.RawMessage
	if err = clientAPI.Do(t.Context(), "GET", path+"/provenance", nil, &before); err != nil {
		t.Fatal(err)
	}
	var m rules.Merged
	mergedPath := "/api/rules/merged?project_id=" + project + "&person_id=" + owner.Principal.ID + "&agent_id=" + key.PrincipalID + "&role=builder&harness=codex"
	if err = clientAPI.Do(t.Context(), "GET", mergedPath, nil, &m); err != nil {
		t.Fatal(err)
	}
	floor := filepath.Join(dir, "floor.txt")
	if err = rules.WriteFile(floor, []byte(m.Floor), false); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(m.Floor))
	args := append(append([]string{}, base...), "session", "start", "--project", project, "--agent", "receive-worker", "--rules-receive", "--session", session.ID, "--worker-lease-file", leaseFile, "--rules-request-id", receiveRequest, "--rules-expected-revision", "0", "--rules-state", filepath.Join(dir, "state.json"), "--rules-out", filepath.Join(dir, "received.txt"), "--rules-cache", filepath.Join(dir, "cache.json"), "--rules-floor", floor, "--rules-floor-sha256", hex.EncodeToString(sum[:]), "--rules-tenant", owner.Tenant.ID, "--rules-person", owner.Principal.ID, "--rules-agent", key.PrincipalID, "--rules-role", "builder", "--rules-harness", "codex")
	drop.Store(true)
	code, out, stderr = runCLI(args, "")
	if code == 0 || !strings.Contains(out, `"receipt_status":"unconfirmed"`) {
		t.Fatal("lost reply misreported", code, out, stderr)
	}
	code, out, stderr = runCLI(append(append([]string{}, args...), "--rules-retry"), "")
	if code != 0 || !strings.Contains(out, `"replayed":true`) || !strings.Contains(out, `"complete":true`) {
		t.Fatal("replay", code, out, stderr)
	}
	if strings.Contains(out+stderr, key.Token) || strings.Contains(out+stderr, receiveLease) {
		t.Fatal("credential leaked")
	}
	var history struct {
		Receipts []harness.RulesReceipt `json:"receipts"`
	}
	if err = clientAPI.Do(t.Context(), "GET", path+"/rules-receipts", nil, &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Receipts) != 1 || history.Receipts[0].Revision != 1 || history.Receipts[0].Request.BodySHA256 != m.SHA256 {
		t.Fatal("duplicate or inexact receipt")
	}
	var after json.RawMessage
	if err = clientAPI.Do(t.Context(), "GET", path+"/provenance", nil, &after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("real instruction provenance changed")
	}
	// A changed credential still fails before replay, with no new receipt.
	os.WriteFile(leaseFile, []byte("wrong-but-long-synthetic-generation-lease"), 0600)
	code, out, stderr = runCLI(append(append([]string{}, args...), "--rules-retry"), "")
	if code == 0 || !strings.Contains(out, `"receipt_status":"rejected"`) || !strings.Contains(stderr, "403") {
		t.Fatal("invalid lease replay", code, out, stderr)
	}
	if posts.Load() != 3 {
		t.Fatal("unexpected receipt submission count")
	}
}

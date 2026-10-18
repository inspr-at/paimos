// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
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
	"github.com/inspr-at/paimos/internal/nodes"
)

// Native Codex thread ids in the shape `codex exec --json` documents.
const (
	renameSourceA       = "0199a213-81c0-7800-8aa1-bbab2a035a53"
	renameSourceB       = "0199a213-81c0-7800-8aa1-bbab2a035a54"
	renameSourceUnnamed = "0199a213-81c0-7800-8aa1-bbab2a035a55"
)

// TestHarnessRenameCLIHelper is the real CLI, run as a child of the Python
// producer through the wrapper written by renameCLIWrapper.
func TestHarnessRenameCLIHelper(t *testing.T) {
	if os.Getenv("AEON_RENAME_CLI_HELPER") != "1" {
		t.Skip("CLI child process of TestHarnessRenameProducerEndToEnd")
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Exit(Run(append([]string{"aeon"}, args...), os.Stdin, os.Stdout, os.Stderr))
}

type renameChange struct {
	Field    string  `json:"field"`
	Previous *string `json:"previous_value"`
	Value    *string `json:"value"`
}

type renameSession struct {
	ID               string         `json:"id"`
	ProjectID        string         `json:"project_id"`
	DisplayLabel     *string        `json:"display_label"`
	ActivitySequence int64          `json:"activity_sequence"`
	MetadataHistory  []renameChange `json:"metadata_history"`
}

type renameBeat struct {
	Beat      int    `json:"beat"`
	Heartbeat string `json:"heartbeat"`
	Label     string `json:"label"`
}

type renameWorker struct {
	id, project, lease, binding string
}

// A session renamed in its harness reaches the same Aeon session through the
// in-repo producer, the real CLI and the real API, and nothing else moves.
func TestHarnessRenameProducerEndToEnd(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 runs scripts/session-heartbeat.py")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "session-heartbeat.py"))
	if err != nil {
		t.Fatal(err)
	}
	isolate(t)
	opened := dbtest.Open(t)
	for _, slug := range []string{"aeon", "foreign"} {
		if err := db.EnsureTenant(t.Context(), opened.App, slug, strings.ToUpper(slug[:1])+slug[1:]); err != nil {
			t.Fatal(err)
		}
	}
	base := renameServer(t, opened, "aeon", true)
	coordinator := mintAgent(t, base, "aeon-coordinator")
	seedProject(t, base, coordinator.Token)
	missing := filepath.Join(t.TempDir(), "no-config.yaml")
	wrapper := renameCLIWrapper(t, missing)
	home := t.TempDir()
	codexHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(codexHome, "session_index.jsonl")
	var names []string
	writeIndex := func() {
		t.Helper()
		if err := os.WriteFile(index, []byte(strings.Join(names, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rename := func(source, name string) {
		t.Helper()
		line, _ := json.Marshal(map[string]string{"id": source, "thread_name": name, "updated_at": "2026-09-28T07:00:00Z"})
		names = append(names, string(line))
		writeIndex()
	}

	t.Setenv("AEON_URL", base)
	register := func(token, label, parent, role string) renameWorker {
		t.Helper()
		t.Setenv("AEON_API_KEY", token)
		dir := t.TempDir()
		ref, lease := filepath.Join(dir, "session.ref"), filepath.Join(dir, "lease.key")
		if err := os.WriteFile(ref, []byte("codex:"+label+":rename-e2e-reference\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lease, []byte(label+"-rename-e2e-synthetic-worker-lease-000000\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		args := []string{"aeon", "--config", missing, "--json", "harness", "register", "--project", "AEON", "--agent", "aeon-coordinator",
			"--harness", "codex", "--host", "rename-host", "--management", "unmanaged", "--role", role, "--label", label,
			"--harness-session-file", ref, "--worker-lease-file", lease}
		if parent != "" {
			args = append(args, "--parent-session", parent)
		}
		code, out, errOut := runCLI(args, "")
		var s renameSession
		if code != 0 || json.Unmarshal([]byte(out), &s) != nil || !validUUID(s.ID) {
			t.Fatalf("register %s: code %d out %s err %s", label, code, out, errOut)
		}
		return renameWorker{id: s.ID, project: s.ProjectID, lease: lease, binding: filepath.Join(dir, "native-session.json")}
	}
	bind := func(w renameWorker, aeonSession, source string, replace bool) {
		t.Helper()
		args := []string{script, "bind", "--binding", w.binding, "--aeon-session", aeonSession, "--source-session", source}
		if replace {
			args = append(args, "--replace")
		}
		if out, err := exec.Command(python, args...).CombinedOutput(); err != nil {
			t.Fatalf("bind: %v %s", err, out)
		}
	}
	produce := func(token string, w renameWorker, lease, binding string) renameBeat {
		t.Helper()
		cmd := exec.Command(python, script, "run", "--aeon", wrapper, "--project", "AEON", "--session", w.id,
			"--agent", "aeon-coordinator", "--worker-lease-file", lease, "--binding", binding, "--codex-index", index, "--once")
		// Preserve race-detector settings even in this otherwise isolated helper.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GORACE=" + os.Getenv("GORACE"), "AEON_RENAME_CLI_HELPER=1",
			"AEON_URL=" + base, "AEON_API_KEY=" + token}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		_ = cmd.Run() // A rejected or skipped beat exits 1; the record says which.
		var beat renameBeat
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &beat); err != nil || beat.Beat != 1 {
			t.Fatalf("producer output %q stderr %q: %v", stdout.String(), stderr.String(), err)
		}
		if text := stdout.String() + stderr.String(); strings.Contains(text, token) || strings.Contains(text, "synthetic-worker-lease") {
			t.Fatal("producer output contained a credential")
		}
		return beat
	}
	read := func(token string, w renameWorker) renameSession {
		t.Helper()
		status, raw := doJSON(t, &http.Client{}, http.MethodGet, base+"/api/projects/"+w.project+"/harness-sessions/"+w.id, "",
			http.Header{"Authorization": {"Bearer " + token}})
		var s renameSession
		if status != http.StatusOK || json.Unmarshal(raw, &s) != nil {
			t.Fatalf("detail %s: %d %s", w.id, status, raw)
		}
		return s
	}
	// expect checks the current name and the name history, newest first.
	expect := func(token string, w renameWorker, label string, history ...string) renameSession {
		t.Helper()
		s := read(token, w)
		var got []string
		for _, change := range s.MetadataHistory {
			if change.Field == "display_label" {
				got = append(got, renameText(change.Previous)+" -> "+renameText(change.Value))
			}
		}
		if renameText(s.DisplayLabel) != label || strings.Join(got, "|") != strings.Join(history, "|") {
			t.Fatalf("session %s: label %q history %q, want %q %q", w.id, renameText(s.DisplayLabel), got, label, history)
		}
		return s
	}
	agentsPage := func(token, path string) map[string]string {
		t.Helper()
		status, raw := doJSON(t, &http.Client{}, http.MethodGet, base+path, "", http.Header{"Authorization": {"Bearer " + token}})
		var page struct {
			Items []struct {
				ID           string  `json:"id"`
				SessionID    string  `json:"session_id"`
				DisplayLabel *string `json:"display_label"`
			} `json:"items"`
		}
		if status != http.StatusOK || json.Unmarshal(raw, &page) != nil {
			t.Fatalf("%s: %d %s", path, status, raw)
		}
		labels := map[string]string{}
		for _, item := range page.Items {
			id := item.ID
			if id == "" {
				id = item.SessionID // The live view keys sessions by session_id.
			}
			if id == "" {
				t.Fatalf("%s: item without a session id: %s", path, raw)
			}
			labels[id] = renameText(item.DisplayLabel)
		}
		return labels
	}
	beatIs := func(got renameBeat, heartbeat, label string) {
		t.Helper()
		if got.Heartbeat != heartbeat || got.Label != label {
			t.Fatalf("beat %+v, want heartbeat %s label %s", got, heartbeat, label)
		}
	}

	// All coordinator workers share one agent principal; only the binding and
	// the per-generation lease tell them apart.
	lead := register(coordinator.Token, "aeon-lead", "", "coordinator")
	a := register(coordinator.Token, "worker-a", lead.id, "worker")
	b := register(coordinator.Token, "worker-b", lead.id, "worker")
	bind(a, a.id, renameSourceA, false)
	bind(b, b.id, renameSourceB, false)

	// 1. No name index yet: the beat keeps the session alive and names nothing.
	beatIs(produce(coordinator.Token, a, a.lease, a.binding), "accepted", "unavailable")
	start := expect(coordinator.Token, a, "worker-a")

	// 2. A rename in the harness reaches this session, list, live view and history.
	rename(renameSourceB, "Sibling thread, not bound to A")
	rename(renameSourceA, "Alpha renamed")
	beatIs(produce(coordinator.Token, a, a.lease, a.binding), "accepted", "included")
	expect(coordinator.Token, a, "Alpha renamed", "worker-a -> Alpha renamed")
	expect(coordinator.Token, b, "worker-b")
	for _, path := range []string{"/api/harness-sessions?limit=200", "/api/harness-sessions/live?include_inactive=true"} {
		if labels := agentsPage(coordinator.Token, path); labels[a.id] != "Alpha renamed" || labels[b.id] != "worker-b" {
			t.Fatalf("%s labels %v", path, labels)
		}
	}

	// 3. Repeated beats with the same name add no history.
	beatIs(produce(coordinator.Token, a, a.lease, a.binding), "accepted", "included")
	beatIs(produce(coordinator.Token, a, a.lease, a.binding), "accepted", "included")
	if s := expect(coordinator.Token, a, "Alpha renamed", "worker-a -> Alpha renamed"); s.ActivitySequence != start.ActivitySequence+3 {
		t.Fatalf("sequence %d after three beats from %d", s.ActivitySequence, start.ActivitySequence)
	}

	// 4. The sibling's own rename reaches only the sibling.
	rename(renameSourceB, "Beta named")
	beatIs(produce(coordinator.Token, b, b.lease, b.binding), "accepted", "included")
	expect(coordinator.Token, b, "Beta named", "worker-b -> Beta named")
	expect(coordinator.Token, a, "Alpha renamed", "worker-a -> Alpha renamed")

	// 5. A second rename keeps the earlier name in history.
	rename(renameSourceA, "Alpha final")
	beatIs(produce(coordinator.Token, a, a.lease, a.binding), "accepted", "included")
	expect(coordinator.Token, a, "Alpha final", "Alpha renamed -> Alpha final", "worker-a -> Alpha renamed")
	aHistory := []string{"Alpha renamed -> Alpha final", "worker-a -> Alpha renamed"}

	// 6. Missing, blank, refused or foreign sources never mutate or clear a name.
	rename(renameSourceA, "   ")
	beatIs(produce(coordinator.Token, a, a.lease, a.binding), "accepted", "unnamed")
	expect(coordinator.Token, a, "Alpha final", aHistory...)
	bind(a, a.id, renameSourceUnnamed, true)
	beatIs(produce(coordinator.Token, a, a.lease, a.binding), "accepted", "unnamed")
	bind(a, a.id, renameSourceA, true)
	beatIs(produce(coordinator.Token, a, a.lease, b.binding), "accepted", "bound-elsewhere")
	rename(renameSourceA, "Alpha refused")
	names = append(names, `{"id":"`+renameSourceA+`","thread_name":"Transcript-shaped","prompt":"synthetic"}`)
	writeIndex()
	beatIs(produce(coordinator.Token, a, a.lease, a.binding), "accepted", "unavailable")
	names = names[:len(names)-3] // Back to "Alpha final" as A's current name.
	writeIndex()
	expect(coordinator.Token, a, "Alpha final", aHistory...)
	expect(coordinator.Token, b, "Beta named", "worker-b -> Beta named")

	// 7. A sibling's lease cannot carry A's name: the beat is rejected unchanged.
	before := read(coordinator.Token, a)
	beatIs(produce(coordinator.Token, a, b.lease, a.binding), "rejected", "included")
	if s := expect(coordinator.Token, a, "Alpha final", aHistory...); s.ActivitySequence != before.ActivitySequence {
		t.Fatal("a rejected beat advanced the session")
	}
	expect(coordinator.Token, b, "Beta named", "worker-b -> Beta named")

	// 8. A new generation takes the binding; the old one can no longer be named.
	a2 := register(coordinator.Token, "worker-a2", lead.id, "worker")
	a2.binding = a.binding
	bind(a, a2.id, renameSourceA, true)
	beatIs(produce(coordinator.Token, a, a.lease, a.binding), "accepted", "bound-elsewhere")
	beatIs(produce(coordinator.Token, a2, a2.lease, a2.binding), "accepted", "included")
	expect(coordinator.Token, a2, "Alpha final", "worker-a2 -> Alpha final")
	t.Setenv("AEON_API_KEY", coordinator.Token)
	if code, out, errOut := runCLI([]string{"aeon", "--config", missing, "--json", "harness", "mark-stopped", "--project", "AEON",
		"--session", a.id, "--agent", "aeon-coordinator", "--worker-lease-file", a.lease}, ""); code != 0 {
		t.Fatalf("mark-stopped code %d out %s err %s", code, out, errOut)
	}
	rename(renameSourceA, "Alpha after stop")
	beatIs(produce(coordinator.Token, a, a.lease, a2.binding), "stopped", "bound-elsewhere")
	expect(coordinator.Token, a, "Alpha final", aHistory...)

	// 9. Another tenant, with the same agent name and the same native thread id,
	// names only its own session and cannot see or beat this tenant's sessions.
	foreignAuth := renameServer(t, opened, "foreign", false)
	foreign := mintAgentIn(t, foreignAuth, "foreign", "aeon-coordinator")
	if status, raw := doJSON(t, &http.Client{}, http.MethodGet, base+"/api/me", "", http.Header{"Authorization": {"Bearer " + foreign.Token}}); status != http.StatusOK || !strings.Contains(string(raw), `"slug":"foreign"`) {
		t.Fatalf("foreign key: %d %s", status, raw)
	}
	seedProject(t, base, foreign.Token)
	f := register(foreign.Token, "foreign-a", "", "worker")
	bind(f, f.id, renameSourceA, false)
	beatIs(produce(foreign.Token, f, f.lease, f.binding), "accepted", "included")
	expect(foreign.Token, f, "Alpha after stop", "foreign-a -> Alpha after stop")
	beatIs(produce(foreign.Token, a2, a2.lease, a2.binding), "skipped", "included")
	status, raw := doJSON(t, &http.Client{}, http.MethodPost, base+"/api/projects/"+a2.project+"/harness-sessions/"+a2.id+"/heartbeat",
		`{"phase":"working","activity":"busy","activity_sequence":99,"display_label":"Foreign clobber"}`,
		http.Header{"Authorization": {"Bearer " + foreign.Token}, "X-Aeon-Worker-Lease": {strings.TrimSpace(renameRead(t, a2.lease))}})
	if status != http.StatusForbidden && status != http.StatusNotFound {
		t.Fatalf("foreign heartbeat on a local session: %d %s", status, raw)
	}
	if status, _ := doJSON(t, &http.Client{}, http.MethodGet, base+"/api/projects/"+a2.project+"/harness-sessions/"+a2.id, "",
		http.Header{"Authorization": {"Bearer " + foreign.Token}}); status != http.StatusNotFound {
		t.Fatalf("foreign detail of a local session: %d", status)
	}
	if labels := agentsPage(foreign.Token, "/api/harness-sessions/live?include_inactive=true"); len(labels) != 1 || labels[f.id] == "" {
		t.Fatalf("foreign live view %v", labels)
	}
	if labels := agentsPage(coordinator.Token, "/api/harness-sessions/live?include_inactive=true"); labels[f.id] != "" || len(labels) != 4 {
		t.Fatalf("local live view %v", labels)
	}
	expect(coordinator.Token, a2, "Alpha final", "worker-a2 -> Alpha final")
	expect(coordinator.Token, a, "Alpha final", aHistory...)
	expect(coordinator.Token, b, "Beta named", "worker-b -> Beta named")
}

func renameText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func renameRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func renameServer(t *testing.T, opened *dbtest.DB, slug string, full bool) string {
	t.Helper()
	authMod, err := auth.New(auth.Config{
		Env:                 "dev",
		SessionKey:          bytes.Repeat([]byte{9}, 32),
		PublicURL:           "http://127.0.0.1",
		BootstrapTenantSlug: slug,
		BootstrapAdminEmail: "admin@example.com",
	}, opened.App)
	if err != nil {
		t.Fatal(err)
	}
	modules := []httpapi.Module{authMod}
	if full {
		modules = append(modules, nodes.New(opened.App, nodes.SQLWriter{}), harness.New(opened.App))
	}
	api := &httpapi.Server{Pool: opened.App, Modules: modules, Middleware: []func(http.Handler) http.Handler{authMod.Middleware}}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// renameCLIWrapper writes an executable that runs this test binary as the CLI.
func renameCLIWrapper(t *testing.T, config string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	path := filepath.Join(t.TempDir(), "aeon")
	body := fmt.Sprintf("#!/bin/sh\nexec %s -test.run='^TestHarnessRenameCLIHelper$' -- --config %s \"$@\"\n", quote(self), quote(config))
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// mintAgentIn is mintAgent for a named tenant, signing in as its bootstrap admin.
func mintAgentIn(t *testing.T, base, tenantSlug, name string) mintedKey {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Jar: jar}
	if status, raw := doJSON(t, hc, http.MethodPost, base+"/api/auth/dev-login", `{"email":"admin@example.com","tenant":"`+tenantSlug+`"}`, nil); status != http.StatusOK {
		t.Fatalf("dev login %s: %d %s", tenantSlug, status, raw)
	}
	status, raw := doJSON(t, hc, http.MethodPost, base+"/api/agent-keys", `{"name":"`+name+`","scopes":["account.manage","kinds.read","nodes.read","nodes.write","harness.read","harness.write","harness.worker"]}`, nil)
	if status != http.StatusCreated {
		t.Fatalf("agent key %s: %d %s", tenantSlug, status, raw)
	}
	var created struct {
		Token       string `json:"token"`
		PrincipalID string `json:"principal_id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	return mintedKey{Token: created.Token, PrincipalID: created.PrincipalID}
}

// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/rules"
)

const receiveSession = "10000000-0000-4000-8000-000000000006"
const receiveRequest = "10000000-0000-4000-8000-000000000007"
const receiveLease = "synthetic-receive-generation-lease-0001"

type receiveFixture struct {
	onSession          func()
	args               []string
	dir                string
	bundle             rules.Merged
	me                 client.Me
	session            harness.Session
	server             *httptest.Server
	status, postStatus int
	posts, gets        int
	lost, badReply     bool
	requests           []harness.RulesReceiptWrite
}

func receivingFixture(t *testing.T) *receiveFixture {
	t.Helper()
	isolate(t)
	f := &receiveFixture{dir: t.TempDir()}
	c := rules.Context{TenantID: "10000000-0000-4000-8000-000000000001", ProjectID: "10000000-0000-4000-8000-000000000002", PersonID: "10000000-0000-4000-8000-000000000003", AgentID: "10000000-0000-4000-8000-000000000004", Role: "builder", Harness: "codex"}
	snap := rules.Snapshot{SetID: "10000000-0000-4000-8000-000000000005", Scope: rules.Scope{Layer: "company"}, Name: "Floor", Revision: 1, Version: "260928100000.0.0", Rules: []rules.Rule{{Identity: "safety", Text: "Preserve safety.", Why: "fixture", Strength: "locked", Enabled: true, Source: rules.Source{Reference: "test"}}}}
	snap.SHA256 = rules.SnapshotDigest(snap)
	var err error
	f.bundle, err = rules.Merge(c, []rules.Snapshot{snap}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.me = client.Me{Principal: client.Principal{ID: c.AgentID, Name: "worker", Kind: "agent", TenantID: c.TenantID}, Tenant: client.Tenant{ID: c.TenantID}}
	f.session = harness.Session{ID: receiveSession, ProjectID: c.ProjectID, AgentPrincipalID: c.AgentID, Harness: c.Harness}
	floorHash := sha256.Sum256([]byte(f.bundle.Floor))
	for name, body := range map[string]string{"floor.txt": f.bundle.Floor, "lease": receiveLease, "AGENTS.md": "original agents", "CLAUDE.md": "original claude"} {
		if err = os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Error("lost authentication")
		}
		if r.Method == http.MethodGet {
			f.gets++
			if r.Header.Get("X-Aeon-Worker-Lease") != "" {
				t.Error("lease on GET")
			}
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/rules/merged":
			if f.status != 0 {
				w.WriteHeader(f.status)
				return
			}
			q := r.URL.Query()
			if q.Get("project_id") != c.ProjectID || q.Get("person_id") != c.PersonID || q.Get("agent_id") != c.AgentID || q.Get("role") != c.Role || q.Get("harness") != c.Harness {
				t.Error("lost rules selectors")
			}
			json.NewEncoder(w).Encode(f.bundle)
		case r.Method == http.MethodGet && r.URL.Path == "/api/me":
			json.NewEncoder(w).Encode(f.me)
		case r.Method == http.MethodGet && r.URL.Path == harnessPath(c.ProjectID, receiveSession):
			if f.onSession != nil {
				f.onSession()
			}
			json.NewEncoder(w).Encode(f.session)
		case r.Method == http.MethodPost && r.URL.Path == harnessPath(c.ProjectID, receiveSession)+"/rules-receipts":
			f.posts++
			if r.Header.Get("X-Aeon-Worker-Lease") != receiveLease {
				t.Error("lost lease")
			}
			var in harness.RulesReceiptWrite
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			f.requests = append(f.requests, in)
			if f.postStatus != 0 {
				w.WriteHeader(f.postStatus)
				json.NewEncoder(w).Encode(map[string]string{"error": testKey + receiveLease})
				return
			}
			if f.lost {
				f.lost = false
				w.Write([]byte("truncated"))
				return
			}
			a := harness.RulesReceipt{Request: in, ID: "10000000-0000-4000-8000-000000000008", SessionID: receiveSession, Revision: *in.ExpectedRevision + 1, RecordedBy: c.AgentID, RecordedAt: time.Now().UTC(), Evidence: "worker_reported_received"}
			if f.badReply {
				a.AuthorityGranted = true
			}
			json.NewEncoder(w).Encode(map[string]any{"receipt": a, "replayed": f.posts > 1})
		default:
			t.Errorf("unexpected route/mutation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	t.Setenv("PAIMOS_URL", f.server.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	f.args = []string{"paimos", "--config", filepath.Join(f.dir, "missing"), "session", "start", "--project", c.ProjectID, "--agent", "worker", "--rules-receive", "--session", receiveSession, "--worker-lease-file", filepath.Join(f.dir, "lease"), "--rules-request-id", receiveRequest, "--rules-expected-revision", "0", "--rules-state", filepath.Join(f.dir, "state.json"), "--rules-out", filepath.Join(f.dir, "received.txt"), "--rules-cache", filepath.Join(f.dir, "cache.json"), "--rules-floor", filepath.Join(f.dir, "floor.txt"), "--rules-floor-sha256", hex.EncodeToString(floorHash[:]), "--rules-tenant", c.TenantID, "--rules-person", c.PersonID, "--rules-agent", c.AgentID, "--rules-role", c.Role, "--rules-harness", c.Harness}
	return f
}

func (f *receiveFixture) run(t *testing.T, retry bool) (int, map[string]any, string) {
	t.Helper()
	args := append([]string{}, f.args...)
	if retry {
		args = append(args, "--rules-retry")
	}
	code, out, stderr := runCLI(args, "")
	assertNoSecret(t, out+stderr)
	if strings.Contains(out+stderr, receiveLease) {
		t.Fatal("lease leaked")
	}
	result := map[string]any{}
	if out != "" {
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
	}
	return code, result, stderr
}
func (f *receiveFixture) flag(name, value string) {
	for i := range f.args {
		if f.args[i] == name {
			f.args[i+1] = value
			return
		}
	}
}

func TestRulesReceiveOnlineAndExplicitReplay(t *testing.T) {
	f := receivingFixture(t)
	code, out, stderr := f.run(t, false)
	if code != 0 || out["complete"] != true || out["receipt_recorded"] != true || out["source"] != "online" || out["stale"] != false || f.posts != 1 {
		t.Fatalf("%d %v %s", code, out, stderr)
	}
	if out["provenance_recorded"] != true {
		t.Fatal("receipt did not record instruction provenance", out["provenance_recorded"])
	}
	for _, field := range []string{"publication_verified", "load_verified", "execution_verified", "authority_granted"} {
		if out[field] != false {
			t.Fatal(field, out)
		}
	}
	request := f.requests[0]
	if request.Context != f.bundle.Context || request.BodySHA256 != f.bundle.SHA256 || request.Version != f.bundle.Version || *request.ByteSize != len(f.bundle.Body) || *request.ExpectedRevision != 0 || request.RequestID != receiveRequest || request.Source != "online" {
		t.Fatal("inexact receipt", request)
	}
	output := filepath.Join(f.dir, "received.txt")
	before, _ := os.Stat(output)
	raw, err := rules.ReadFile(output, rules.MaxBytes)
	if err != nil || string(raw) != f.bundle.Body || before.Mode().Perm() != 0600 {
		t.Fatal("output", err)
	}
	state, _ := rules.ReadFile(filepath.Join(f.dir, "state.json"), rules.MaxCacheBytes)
	if strings.Contains(string(state), receiveLease) || strings.Contains(string(state), testKey) {
		t.Fatal("credential in checkpoint")
	}
	code, _, _ = f.run(t, false)
	if code == 0 || f.posts != 1 {
		t.Fatal("implicit duplicate accepted")
	}
	// Publication can advance; explicit retry still reports original bytes.
	f.bundle.Version = "260928110000.0.0"
	code, out, stderr = f.run(t, true)
	if code != 0 || out["replayed"] != true || !reflect.DeepEqual(f.requests[0], f.requests[1]) {
		t.Fatalf("retry %d %v %s", code, out, stderr)
	}
	after, _ := os.Stat(output)
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("retry replaced output")
	}
	for name, want := range map[string]string{"AGENTS.md": "original agents", "CLAUDE.md": "original claude"} {
		b, _ := os.ReadFile(filepath.Join(f.dir, name))
		if string(b) != want {
			t.Fatal("active file changed")
		}
	}
}

func TestRulesReceivePartialAndRejectedReceipts(t *testing.T) {
	for _, status := range []int{0, 401, 403, 404, 409, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := receivingFixture(t)
			f.postStatus = status
			f.lost = status == 0
			code, out, _ := f.run(t, false)
			if code == 0 || out["complete"] != false || out["receipt_recorded"] != false || out["bytes_received"] != true {
				t.Fatal(code, out)
			}
			f.postStatus = 0
			code, out, stderr := f.run(t, true)
			if code != 0 || out["replayed"] != true || !reflect.DeepEqual(f.requests[0], f.requests[1]) {
				t.Fatal(code, out, stderr)
			}
		})
	}
	f := receivingFixture(t)
	f.badReply = true
	if code, out, _ := f.run(t, false); code == 0 || out["complete"] != false {
		t.Fatal("unverified response accepted")
	}
}

func TestRulesReceiveOfflineNeverPostsOrQueues(t *testing.T) {
	for _, cache := range []string{"valid", "corrupt", "wrong-context", "expired", "absent"} {
		t.Run(cache, func(t *testing.T) {
			f := receivingFixture(t)
			m := f.bundle
			if cache == "wrong-context" {
				m.Context.PersonID = m.Context.TenantID
			}
			raw, err := rules.EncodeCache(f.server.URL, m, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if cache == "expired" {
				var c rules.Cache
				json.Unmarshal(raw, &c)
				exp := time.Now().Add(-time.Minute)
				c.Bundle.ValidUntil = &exp
				// Encode a formerly valid cache before its expiry.
				raw, err = rules.EncodeCache(f.server.URL, c.Bundle, exp.Add(-time.Minute))
				if err != nil {
					t.Fatal(err)
				}
			}
			if cache == "corrupt" {
				raw = []byte("corrupt")
			}
			if cache != "absent" {
				if err = rules.WriteFile(filepath.Join(f.dir, "cache.json"), raw, false); err != nil {
					t.Fatal(err)
				}
			}
			f.status = 503
			code, out, stderr := f.run(t, false)
			want := "floor-only"
			if cache == "valid" {
				want = "cache"
			}
			if code != 0 || out["source"] != want || out["stale"] != true || out["gap"] == "" || out["receipt_recorded"] != false || out["complete"] != false || f.posts != 0 {
				t.Fatal(code, out, stderr)
			}
			f.status = 0
			code, _, _ = f.run(t, true)
			if code == 0 || f.posts != 0 {
				t.Fatal("offline state replayed online")
			}
		})
	}
}

func TestRulesReceiveFailClosed(t *testing.T) {
	for _, change := range []string{"401", "403", "404", "tenant", "project", "person", "agent", "harness", "role", "task", "hash", "expired", "floor-addition", "floor-pin", "caller", "session", "stopped", "registered-harness", "registered-agent", "registered-project", "credential", "large-credential", "lease-symlink", "large-state", "output", "symlink", "parent-symlink", "state-symlink", "active-file", "alias", "preview"} {
		t.Run(change, func(t *testing.T) {
			f := receivingFixture(t)
			old := f.bundle
			cache, _ := rules.EncodeCache(f.server.URL, old, time.Now())
			if err := rules.WriteFile(filepath.Join(f.dir, "cache.json"), cache, false); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "401":
				f.status = 401
			case "403":
				f.status = 403
			case "404":
				f.status = 404
			case "tenant":
				f.bundle.Context.TenantID = receiveSession
			case "project":
				f.bundle.Context.ProjectID = receiveSession
			case "person":
				f.bundle.Context.PersonID = receiveSession
			case "agent":
				f.bundle.Context.AgentID = receiveSession
			case "harness":
				f.bundle.Context.Harness = "pi"
			case "role":
				f.bundle.Context.Role = "reviewer"
			case "task":
				f.bundle.Context.TaskID = receiveRequest
			case "hash":
				f.bundle.SHA256 = strings.Repeat("0", 64)
			case "expired":
				v := time.Now().Add(-time.Minute)
				f.bundle.ValidUntil = &v
			case "floor-addition":
				f.bundle.Floor += "- [new] New safety.\n"
				f.bundle.Body += "- [new] New safety.\n"
				f.bundle.ByteSize = len(f.bundle.Body)
				v := sha256.Sum256([]byte(f.bundle.Body))
				f.bundle.SHA256 = hex.EncodeToString(v[:])
			case "floor-pin":
				f.flag("--rules-floor-sha256", strings.Repeat("0", 64))
			case "caller":
				f.me.Principal.Name = "other"
			case "session":
				f.session.ID = receiveRequest
			case "stopped":
				v := time.Now()
				f.session.StoppedAt = &v
			case "registered-harness":
				f.session.Harness = "pi"
			case "registered-agent":
				f.session.AgentPrincipalID = receiveRequest
			case "registered-project":
				f.session.ProjectID = receiveRequest
			case "credential":
				os.Chmod(filepath.Join(f.dir, "lease"), 0644)
			case "large-credential":
				os.WriteFile(filepath.Join(f.dir, "lease"), []byte(strings.Repeat("x", 8193)), 0600)
			case "lease-symlink":
				os.Rename(filepath.Join(f.dir, "lease"), filepath.Join(f.dir, "lease-original"))
				os.Symlink(filepath.Join(f.dir, "lease-original"), filepath.Join(f.dir, "lease"))
			case "large-state":
				os.WriteFile(filepath.Join(f.dir, "state.json"), []byte(strings.Repeat("x", rules.MaxCacheBytes+1)), 0600)
				f.args = append(f.args, "--rules-retry")
			case "output":
				os.WriteFile(filepath.Join(f.dir, "received.txt"), []byte("keep"), 0600)
			case "symlink":
				os.Symlink(filepath.Join(f.dir, "floor.txt"), filepath.Join(f.dir, "received.txt"))
			case "parent-symlink":
				os.Symlink(f.dir, filepath.Join(f.dir, "alias"))
				f.flag("--rules-out", filepath.Join(f.dir, "alias", "received.txt"))
			case "state-symlink":
				os.Symlink(filepath.Join(f.dir, "cache.json"), filepath.Join(f.dir, "state.json"))
			case "active-file":
				f.flag("--rules-out", filepath.Join(f.dir, "AGENTS.md"))
			case "alias":
				f.flag("--rules-state", filepath.Join(f.dir, "cache.json"))
			case "preview":
				f.args = append(f.args, "--rules-preview")
			}
			code, _, _ := f.run(t, false)
			if code == 0 || f.posts != 0 {
				t.Fatal("fail open", change, code)
			}
			if change == "output" {
				b, _ := os.ReadFile(filepath.Join(f.dir, "received.txt"))
				if string(b) != "keep" {
					t.Fatal("overwritten")
				}
			}
		})
	}
}

func TestRulesReceiveRetryBindsCheckpointAndOutput(t *testing.T) {
	for _, change := range []string{"session", "request", "revision", "person", "floor", "instance", "output", "corrupt", "expired", "denied", "offline", "missing-output", "symlink"} {
		t.Run(change, func(t *testing.T) {
			f := receivingFixture(t)
			f.lost = true
			if code, _, _ := f.run(t, false); code == 0 {
				t.Fatal("expected lost receipt response")
			}
			switch change {
			case "session":
				f.flag("--session", receiveRequest)
			case "request":
				f.flag("--rules-request-id", receiveSession)
			case "revision":
				f.flag("--rules-expected-revision", "1")
			case "person":
				f.flag("--rules-person", receiveRequest)
			case "floor":
				f.flag("--rules-floor-sha256", strings.Repeat("0", 64))
			case "instance":
				t.Setenv("PAIMOS_URL", f.server.URL+"/other")
			case "output":
				os.WriteFile(filepath.Join(f.dir, "received.txt"), []byte("changed"), 0600)
			case "corrupt":
				os.WriteFile(filepath.Join(f.dir, "state.json"), []byte("corrupt"), 0600)
			case "expired":
				raw, _ := rules.ReadFile(filepath.Join(f.dir, "state.json"), rules.MaxCacheBytes)
				var s rulesReceiveState
				json.Unmarshal(raw, &s)
				v := time.Now().Add(-time.Minute)
				s.Bundle.ValidUntil = &v
				s.SHA256 = s.digest()
				raw, _ = json.Marshal(s)
				os.WriteFile(filepath.Join(f.dir, "state.json"), raw, 0600)
			case "denied":
				f.status = 403
			case "offline":
				f.status = 503
			case "missing-output":
				os.Rename(filepath.Join(f.dir, "received.txt"), filepath.Join(f.dir, "retained.txt"))
			case "symlink":
				os.Rename(filepath.Join(f.dir, "received.txt"), filepath.Join(f.dir, "retained.txt"))
				os.Symlink(filepath.Join(f.dir, "retained.txt"), filepath.Join(f.dir, "received.txt"))
			}
			code, out, stderr := f.run(t, true)
			if change == "missing-output" {
				if code != 0 || f.posts != 2 || out["replayed"] != true {
					t.Fatal(code, out, stderr)
				}
			} else if code == 0 || f.posts != 1 {
				t.Fatal("bad retry submitted", change, code)
			}
		})
	}
}

func TestRulesReceiveNeedsExplicitMode(t *testing.T) {
	f := receivingFixture(t)
	for i, v := range f.args {
		if v == "--rules-receive" {
			f.args = append(f.args[:i], f.args[i+1:]...)
			break
		}
	}
	if code, _, _ := f.run(t, false); code == 0 || f.gets != 0 || f.posts != 0 {
		t.Fatal("implicit receiving or attribution mint")
	}
}

func TestRulesReceiveExpiryDuringIdentityCheckAndOutputRace(t *testing.T) {
	for _, scenario := range []string{"expiry", "output-race"} {
		t.Run(scenario, func(t *testing.T) {
			f := receivingFixture(t)
			if scenario == "expiry" {
				expires := time.Now().Add(100 * time.Millisecond)
				f.bundle.ValidUntil = &expires
				f.onSession = func() { time.Sleep(150 * time.Millisecond) }
			} else {
				f.onSession = func() {
					if err := os.WriteFile(filepath.Join(f.dir, "received.txt"), []byte("racing file"), 0600); err != nil {
						t.Error(err)
					}
				}
			}
			if code, _, _ := f.run(t, false); code == 0 || f.posts != 0 {
				t.Fatal("expired or racing output accepted")
			}
		})
	}
}

func TestRulesReceiveRefusesRedirects(t *testing.T) {
	f := receivingFixture(t)
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true; w.WriteHeader(503) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	t.Setenv("PAIMOS_URL", origin.URL)
	if code, _, _ := f.run(t, false); code == 0 || followed || f.posts != 0 {
		t.Fatal("redirect followed or allowed fallback")
	}
}

//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/inbox"
)

const (
	indexSourceID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	indexOtherID  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	indexAeonID   = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	indexAeonAlt  = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	indexMessage  = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
)

func writeIndexState(t *testing.T, dir, aeonID, label, extra string) {
	t.Helper()
	// t.TempDir's leaf is 0755; heartbeat state directories are 0700.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.id"), []byte(aeonID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"schema":"` + heartbeatSchema + `","session_id":"` + aeonID + `","sent_label":"` + label + `"`
	if extra != "" {
		body += "," + extra
	}
	body += "}\n"
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func useIndexHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestSessionIndexRejectsStaleSymlinkAndLooseMode(t *testing.T) {
	useIndexHome(t)
	live := t.TempDir()
	writeIndexState(t, live, indexAeonID, "Website", "")
	if err := writeSessionIndex(indexSourceID, live); err != nil {
		t.Fatal(err)
	}
	id, label, result := lookupSessionIndex(indexSourceID)
	if result != sessionIndexBound || id != indexAeonID || label != "Website" {
		t.Fatalf("lookup %s %s %d", id, label, result)
	}
	info, err := os.Stat(filepath.Join(mustIndexRoot(t), indexSourceID))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("index mode %v %v", info, err)
	}
	rootInfo, err := os.Stat(mustIndexRoot(t))
	if err != nil || rootInfo.Mode().Perm() != 0o700 {
		t.Fatalf("index directory mode %v %v", rootInfo, err)
	}

	other := t.TempDir()
	writeIndexState(t, other, indexAeonAlt, "Other", "")
	if err := writeSessionIndex(indexOtherID, other); err != nil {
		t.Fatal(err)
	}
	removeSessionIndexForState(live)
	if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexAbsent {
		t.Fatal("stopped state dir left its index entry")
	}
	if id, _, result := lookupSessionIndex(indexOtherID); result != sessionIndexBound || id != indexAeonAlt {
		t.Fatal("removal deleted another generation")
	}

	for _, extra := range []string{`"closed":true`, `"terminal":true`} {
		t.Run(extra, func(t *testing.T) {
			useIndexHome(t)
			dir := t.TempDir()
			writeIndexState(t, dir, indexAeonID, "Website", extra)
			if err := writeSessionIndex(indexSourceID, dir); err != nil {
				t.Fatal(err)
			}
			if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexRejected {
				t.Fatalf("stale index result %d", result)
			}
		})
	}

	t.Run("symlink index", func(t *testing.T) {
		useIndexHome(t)
		dir := t.TempDir()
		writeIndexState(t, dir, indexAeonID, "Website", "")
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte(canonicalPrivatePath(dir)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(mustIndexRoot(t), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(mustIndexRoot(t), indexSourceID)); err != nil {
			t.Fatal(err)
		}
		if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexRejected {
			t.Fatalf("followed symlink index: %d", result)
		}
	})

	t.Run("symlink state", func(t *testing.T) {
		useIndexHome(t)
		dir := t.TempDir()
		writeIndexState(t, dir, indexAeonID, "Website", "")
		link := filepath.Join(t.TempDir(), "state-link")
		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}
		if err := writeSessionIndex(indexSourceID, link); err == nil {
			t.Fatal("recorded a symlinked state directory")
		}
		if _, err := os.Lstat(filepath.Join(mustIndexRoot(t), indexSourceID)); !os.IsNotExist(err) {
			t.Fatal("symlink state created an index entry")
		}
		if err := os.MkdirAll(mustIndexRoot(t), 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(mustIndexRoot(t), indexSourceID)
		if err := os.WriteFile(path, []byte(link+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexRejected {
			t.Fatalf("followed symlinked state directory: %d", result)
		}
	})

	t.Run("loose mode and hard link", func(t *testing.T) {
		useIndexHome(t)
		dir := t.TempDir()
		writeIndexState(t, dir, indexAeonID, "Website", "")
		if err := writeSessionIndex(indexSourceID, dir); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(mustIndexRoot(t), indexSourceID)
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexRejected {
			t.Fatalf("accepted group-readable index: %d", result)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(path, filepath.Join(mustIndexRoot(t), indexOtherID)); err != nil {
			t.Fatal(err)
		}
		if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexRejected {
			t.Fatalf("accepted hard-linked index: %d", result)
		}
	})
}

func TestInboxHookIndexPrecedence(t *testing.T) {
	config := setupHookTest(t)
	useIndexHome(t)
	dir := t.TempDir()
	writeIndexState(t, dir, indexAeonID, "Website", "")
	if err := writeSessionIndex(indexSourceID, dir); err != nil {
		t.Fatal(err)
	}
	var lookups int
	var pulls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/inbox/session-binding":
			lookups++
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/inbox/messages":
			pulls = append(pulls, r.URL.Query().Get("session"))
			session := r.URL.Query().Get("session")
			if session != indexAeonID && session != indexAeonAlt {
				fmt.Fprint(w, `{"items":[],"next_after":0}`)
				return
			}
			msg := hookFixtureMessage()
			msg.ID = indexMessage
			msg.RecipientSessionID = ptrHook(session)
			msg.Body = "indexed " + session
			_ = json.NewEncoder(w).Encode(inbox.Page{Items: []inbox.Message{msg}, NextAfter: 42})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ack"):
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", "fixture-hook-key")
	input := func(session string) string {
		return `{"hook_event_name":"UserPromptSubmit","session_id":"` + session + `"}`
	}
	run := func(raw string) (string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := RunMessaging([]string{"aeon", "--config", config, "hook", "claude", "UserPromptSubmit"}, strings.NewReader(raw), &out, &errOut)
		if code != 0 {
			t.Fatalf("hook exit %d %s", code, errOut.String())
		}
		return out.String(), errOut.String()
	}

	out, errOut := run(input(indexSourceID))
	if !strings.Contains(out, "indexed "+indexAeonID) || errOut != "" || lookups != 0 || len(pulls) != 1 || pulls[0] != indexAeonID {
		t.Fatalf("index lookup out %q err %q lookups %d pulls %v", out, errOut, lookups, pulls)
	}

	before := lookups
	out, errOut = run(input("ffffffff-ffff-4fff-8fff-ffffffffffff"))
	if out != "" || errOut != "" || lookups != before+1 || len(pulls) != 1 {
		t.Fatalf("unknown session was not a quiet miss: out %q err %q lookups %d pulls %v", out, errOut, lookups, pulls)
	}

	closed := t.TempDir()
	writeIndexState(t, closed, indexAeonAlt, "Stopped", `"closed":true`)
	if err := writeSessionIndex(indexOtherID, closed); err != nil {
		t.Fatal(err)
	}
	before = lookups
	out, errOut = run(input(indexOtherID))
	if out != "" || errOut != "" || lookups != before || len(pulls) != 1 {
		t.Fatalf("stale index fell through: out %q err %q lookups %d pulls %v", out, errOut, lookups, pulls)
	}

	t.Setenv("AEON_SESSION_ID", indexAeonAlt)
	before = lookups
	out, errOut = run(input(indexSourceID))
	if !strings.Contains(out, "indexed "+indexAeonAlt) || strings.Contains(out, indexAeonID) || lookups != before || pulls[len(pulls)-1] != indexAeonAlt {
		t.Fatalf("env lost to index: out %q lookups %d pulls %v", out, lookups, pulls)
	}

	t.Setenv("AEON_SESSION_ID", "")
	t.Setenv("AEON_SESSION_FILE", filepath.Join(t.TempDir(), "missing-session"))
	before = len(pulls)
	out, errOut = run(input(indexSourceID))
	if out != "" || errOut != "" || len(pulls) != before {
		t.Fatalf("missing env file fell through to the index: out %q err %q pulls %d", out, errOut, len(pulls))
	}

	t.Setenv("AEON_SESSION_FILE", "")
	t.Setenv("AEON_SESSION_ID", "not-a-uuid")
	before = len(pulls)
	out, errOut = run(input(indexSourceID))
	if out != "" || !strings.Contains(errOut, "inbox handoff incomplete") || len(pulls) != before {
		t.Fatalf("invalid env fell through: out %q err %q pulls %d", out, errOut, len(pulls))
	}
}

func TestHookInstallPrintsBinding(t *testing.T) {
	setupHookTest(t)
	useIndexHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	config := filepath.Join(t.TempDir(), "config.yaml")
	args := []string{"aeon", "--config", config, "hook", "install", "--harness", "claude", "--dry-run"}
	code, out, stderr := runCLI(args, "")
	if code != 0 || stderr != "" || bindingLine(out) != "not bound: run harness run-heartbeat with --source-session" {
		t.Fatalf("unbound install code %d out %q err %q", code, out, stderr)
	}
	dir := t.TempDir()
	writeIndexState(t, dir, indexAeonID, "Website", "")
	if err := writeSessionIndex(indexSourceID, dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", indexSourceID)
	code, out, stderr = runCLI(args, "")
	if code != 0 || stderr != "" || bindingLine(out) != "bound: Website" {
		t.Fatalf("bound install code %d out %q err %q", code, out, stderr)
	}
}

func bindingLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "bound: ") || strings.HasPrefix(line, "not bound: ") {
			return line
		}
	}
	return ""
}

func TestRunHeartbeatMaintainsSessionIndex(t *testing.T) {
	dir := t.TempDir()
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	other := t.TempDir()
	writeIndexState(t, other, indexAeonAlt, "Other", "")
	if err := writeSessionIndex(indexOtherID, other); err != nil {
		t.Fatal(err)
	}
	o := heartbeatTestOptions(dir)
	o.SourceSession = indexSourceID
	o.Label = "Website"
	seen := false
	err := rt.runHeartbeat(context.Background(), o, heartbeatDeps{
		alive: func(int) bool { return true },
		wait: func(context.Context, int, time.Duration) error {
			id, label, result := lookupSessionIndex(indexSourceID)
			if result != sessionIndexBound || id != transcriptSessionID || label != "Website" {
				t.Fatalf("during heartbeat %s %s %d", id, label, result)
			}
			info, statErr := os.Stat(filepath.Join(mustIndexRoot(t), indexSourceID))
			if statErr != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("index file %v %v", info, statErr)
			}
			raw, readErr := os.ReadFile(filepath.Join(mustIndexRoot(t), indexSourceID))
			if readErr != nil || string(raw) != canonicalPrivatePath(o.StateDir)+"\n" {
				t.Fatalf("index text %q err %v", raw, readErr)
			}
			seen = true
			return errOwnerExited
		},
	})
	if err != nil || !seen {
		t.Fatalf("run err %v seen %v stderr %s", err, seen, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr %s", stderr.String())
	}
	if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexAbsent {
		t.Fatal("index remained after the generation stopped")
	}
	if id, _, result := lookupSessionIndex(indexOtherID); result != sessionIndexBound || id != indexAeonAlt {
		t.Fatal("stop removed a different generation")
	}
	if len(hbWhere(calls, http.MethodPost, "/stop")) != 1 {
		t.Fatal("heartbeat did not stop")
	}
}

func mustIndexRoot(t *testing.T) string {
	t.Helper()
	root, err := sessionIndexRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

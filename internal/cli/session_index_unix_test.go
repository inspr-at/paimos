//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/inbox"
	"golang.org/x/sys/unix"
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

func currentOwner(t *testing.T) (int, string) {
	t.Helper()
	stamp, err := readOwnerStamp(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return stamp.PID, stamp.Start
}

func publishIndex(t *testing.T, source, dir string) {
	t.Helper()
	pid, start := currentOwner(t)
	if err := writeSessionIndex(source, dir, pid, start); err != nil {
		t.Fatal(err)
	}
}

func TestSessionIndexRejectsStaleSymlinkAndLooseMode(t *testing.T) {
	useIndexHome(t)
	live := t.TempDir()
	writeIndexState(t, live, indexAeonID, "Website", "")
	publishIndex(t, indexSourceID, live)
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
	publishIndex(t, indexOtherID, other)
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
			publishIndex(t, indexSourceID, dir)
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
		pid, start := currentOwner(t)
		if err := writeSessionIndex(indexSourceID, link, pid, start); err == nil {
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
		publishIndex(t, indexSourceID, dir)
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
	publishIndex(t, indexSourceID, dir)
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
	if out != "" || errOut != "" || lookups != before || len(pulls) != 1 {
		t.Fatalf("unknown session was not a quiet miss: out %q err %q lookups %d pulls %v", out, errOut, lookups, pulls)
	}

	closed := t.TempDir()
	writeIndexState(t, closed, indexAeonAlt, "Stopped", `"closed":true`)
	publishIndex(t, indexOtherID, closed)
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
	publishIndex(t, indexSourceID, dir)
	t.Setenv("CLAUDE_CODE_SESSION_ID", indexSourceID)
	code, out, stderr = runCLI(args, "")
	if code != 0 || stderr != "" || bindingLine(out) != "bound: Website" {
		t.Fatalf("bound install code %d out %q err %q", code, out, stderr)
	}

	t.Setenv("AEON_SESSION_ID", indexAeonAlt)
	code, out, stderr = runCLI(args, "")
	if code != 0 || stderr != "" || bindingLine(out) != "bound: "+indexAeonAlt || !strings.Contains(out, "conflict: session index binds a different generation") || strings.Contains(out, "bound: Website") {
		t.Fatalf("env/index conflict code %d out %q err %q", code, out, stderr)
	}

	t.Setenv("AEON_SESSION_ID", indexAeonID)
	code, out, stderr = runCLI(args, "")
	if code != 0 || stderr != "" || bindingLine(out) != "bound: Website" || strings.Contains(out, "conflict:") {
		t.Fatalf("matching env code %d out %q err %q", code, out, stderr)
	}

	t.Setenv("AEON_SESSION_ID", "not-a-uuid")
	code, out, stderr = runCLI(args, "")
	if code != 0 || stderr != "" || bindingLine(out) != "not bound: explicit session binding is invalid" || strings.Contains(out, "bound: Website") {
		t.Fatalf("invalid env code %d out %q err %q", code, out, stderr)
	}

	t.Setenv("AEON_SESSION_ID", "")
	t.Setenv("AEON_SESSION_FILE", filepath.Join(t.TempDir(), "missing-session"))
	code, out, stderr = runCLI(args, "")
	if code != 0 || stderr != "" || bindingLine(out) != "not bound: explicit session binding is unavailable" || strings.Contains(out, "bound: Website") {
		t.Fatalf("missing env file code %d out %q err %q", code, out, stderr)
	}

	t.Setenv("AEON_SESSION_FILE", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("AEON_SESSION_STATE_DIR", dir)
	code, out, stderr = runCLI(args, "")
	if code != 0 || stderr != "" || bindingLine(out) != "bound: Website" || strings.Contains(out, "conflict:") {
		t.Fatalf("state dir env code %d out %q err %q", code, out, stderr)
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
	publishIndex(t, indexOtherID, other)
	o := heartbeatTestOptions(dir)
	o.SourceSession = indexSourceID
	o.Label = "Website"
	o.OwnerPID = os.Getpid()
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
			entry, ok := parseSessionIndexEntry(raw)
			if readErr != nil || !ok || entry.StateDir != canonicalPrivatePath(o.StateDir) || !ownerAlive(entry.OwnerPID, entry.OwnerStart) {
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

func TestSessionIndexRefusesLiveConflict(t *testing.T) {
	useIndexHome(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeIndexState(t, dirA, indexAeonID, "Website", "")
	writeIndexState(t, dirB, indexAeonAlt, "Other", "")
	publishIndex(t, indexSourceID, dirA)
	pid, start := currentOwner(t)
	err := writeSessionIndex(indexSourceID, dirB, pid, start)
	if !errors.Is(err, errSessionIndexConflict) || !strings.Contains(err.Error(), indexSourceID) {
		t.Fatalf("conflict: %v", err)
	}
	if id, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexBound || id != indexAeonID {
		t.Fatalf("live binding replaced: %s %d", id, result)
	}
	// Same directory may refresh. A dead owner is not a live binding and can be replaced.
	if err := writeSessionIndex(indexSourceID, dirA, 1<<30, "9.9"); err != nil {
		t.Fatal(err)
	}
	if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexRejected {
		t.Fatalf("dead owner stayed bound: %d", result)
	}
	if err := writeSessionIndex(indexSourceID, dirB, pid, start); err != nil {
		t.Fatal(err)
	}
	if id, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexBound || id != indexAeonAlt {
		t.Fatalf("stale binding was not replaced: %s %d", id, result)
	}
}

func TestSessionIndexRejectsReusedOwner(t *testing.T) {
	useIndexHome(t)
	dir := t.TempDir()
	writeIndexState(t, dir, indexAeonID, "Website", "")
	if err := writeSessionIndex(indexSourceID, dir, os.Getpid(), "9.9"); err != nil {
		t.Fatal(err)
	}
	if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexRejected {
		t.Fatalf("reused pid accepted: %d", result)
	}
}

func TestSessionIndexRemovalKeepsReplacement(t *testing.T) {
	if os.Getenv("AEON_INDEX_LOCK_CHILD") == "1" {
		holdIndexLockChild()
		os.Exit(0)
	}
	useIndexHome(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeIndexState(t, dirA, indexAeonID, "Website", "")
	writeIndexState(t, dirB, indexAeonAlt, "Other", "")
	publishIndex(t, indexSourceID, dirA)
	pid, start := currentOwner(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	side := t.TempDir()
	ready := filepath.Join(side, "ready")
	release := filepath.Join(side, "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionIndexRemovalKeepsReplacement$")
	cmd.Env = append(os.Environ(),
		"AEON_INDEX_LOCK_CHILD=1",
		"HOME="+home,
		"AEON_INDEX_READY="+ready,
		"AEON_INDEX_RELEASE="+release,
		"AEON_INDEX_SOURCE="+indexSourceID,
		"AEON_INDEX_DIR="+canonicalPrivatePath(dirB),
		"AEON_INDEX_PID="+strconv.Itoa(pid),
		"AEON_INDEX_START="+start,
	)
	cmd.Stdout = &bytes.Buffer{}
	var childErr bytes.Buffer
	cmd.Stderr = &childErr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, statErr := os.Stat(ready); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock holder did not start: %s", childErr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() {
		removeSessionIndexForState(dirA)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("removal did not wait for the index lock")
	case <-time.After(time.Second):
	}
	if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("lock holder: %v %s", err, childErr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lock holder did not exit")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("removal did not finish")
	}
	if id, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexBound || id != indexAeonAlt {
		t.Fatalf("removal unlinked the replacement: %s %d", id, result)
	}
	removeSessionIndexForState(dirB)
	if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexAbsent {
		t.Fatalf("owned removal left the entry: %d", result)
	}
}

func holdIndexLockChild() {
	pid, err := strconv.Atoi(os.Getenv("AEON_INDEX_PID"))
	if err != nil || pid <= 0 || !validOwnerStart(os.Getenv("AEON_INDEX_START")) {
		fmt.Fprintln(os.Stderr, "lock holder owner is invalid")
		os.Exit(1)
	}
	err = withSessionIndexLock(false, func(dirfd int) error {
		if err := os.WriteFile(os.Getenv("AEON_INDEX_READY"), []byte("ready\n"), 0o600); err != nil {
			return err
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if _, statErr := os.Stat(os.Getenv("AEON_INDEX_RELEASE")); statErr == nil {
				return storeSessionIndexEntry(dirfd, os.Getenv("AEON_INDEX_SOURCE"), os.Getenv("AEON_INDEX_DIR"), pid, os.Getenv("AEON_INDEX_START"))
			}
			time.Sleep(20 * time.Millisecond)
		}
		return errors.New("lock holder timed out")
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "lock holder:", err.Error())
		os.Exit(1)
	}
}

func TestRunHeartbeatRefusesDuplicateSource(t *testing.T) {
	dir := t.TempDir()
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	live := t.TempDir()
	writeIndexState(t, live, indexAeonID, "Website", "")
	publishIndex(t, indexSourceID, live)
	o := heartbeatTestOptions(dir)
	o.SourceSession = indexSourceID
	o.OwnerPID = os.Getpid()
	err := rt.runHeartbeat(context.Background(), o, heartbeatDeps{
		alive: func(int) bool { return true },
		wait:  func(context.Context, int, time.Duration) error { return errOwnerExited },
	})
	if !errors.Is(err, errSessionIndexConflict) || !strings.Contains(err.Error(), indexSourceID) {
		t.Fatalf("startup err %v", err)
	}
	if !strings.Contains(stderr.String(), indexSourceID) || !strings.Contains(stderr.String(), "another live state directory") {
		t.Fatalf("stderr %s", stderr.String())
	}
	if id, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexBound || id != indexAeonID {
		t.Fatalf("conflict overwrote the live binding: %s %d", id, result)
	}
}

func TestRunHeartbeatDropsIndexWhenStopFails(t *testing.T) {
	for _, name := range []string{"owner exit", "signal"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			var calls []hbCall
			inner := heartbeatFixture(t, &calls, "", "")
			defer inner.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop") {
					http.Error(w, `{"error":"stop failed"}`, http.StatusInternalServerError)
					return
				}
				inner.Config.Handler.ServeHTTP(w, r)
			}))
			defer srv.Close()
			rt, _, _ := heartbeatRuntime(t, srv)
			o := heartbeatTestOptions(dir)
			o.SourceSession = indexSourceID
			o.Label = "Website"
			o.OwnerPID = os.Getpid()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := rt.runHeartbeat(ctx, o, heartbeatDeps{
				alive: func(int) bool { return true },
				wait: func(context.Context, int, time.Duration) error {
					if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexBound {
						t.Fatalf("index missing while the owner is alive: %d", result)
					}
					if name == "signal" {
						cancel()
						return context.Canceled
					}
					return errOwnerExited
				},
			})
			if err == nil {
				t.Fatal("failed stop returned success")
			}
			if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexAbsent {
				t.Fatalf("index remained after %s: %d", name, result)
			}
			if !ownerAlive(os.Getpid(), func() string { _, start := currentOwner(t); return start }()) {
				t.Fatal("owner was not alive")
			}
		})
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

func TestCurrentOwnerStampIsPublishable(t *testing.T) {
	_, start := currentOwner(t)
	if !validOwnerStart(start) {
		t.Fatalf("live owner stamp rejected: %q", start)
	}
}

func TestValidOwnerStartAcceptsBothPlatformStamps(t *testing.T) {
	const linuxStamp = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee:42"
	for _, start := range []string{"1.2", "0.1", "1690000000.123456", linuxStamp, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee:0"} {
		if !validOwnerStart(start) {
			t.Fatalf("rejected %q", start)
		}
	}
	for _, start := range []string{
		"", ".", "1.", ".1", "1", "1.2.3", "1..2", "../x", "/tmp/x",
		"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee:",
		"AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE:1",
		"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee:1:2",
		"not-a-boot-id:1",
		"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee:01x",
		"1.2\n3",
		strings.Repeat("1", 21) + ".2",
		"18446744073709551616.1",
		"1.18446744073709551616",
		"99999999999999999999.1",
		"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee:18446744073709551616",
		"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee:99999999999999999999",
	} {
		if validOwnerStart(start) {
			t.Fatalf("accepted %q", start)
		}
	}
	const maxUint = "18446744073709551615"
	for _, start := range []string{maxUint + ".0", "0." + maxUint, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee:" + maxUint} {
		if !validOwnerStart(start) {
			t.Fatalf("rejected uint64 max %q", start)
		}
	}
}

func TestSessionIndexStoresLinuxOwnerStampWithoutBindingIt(t *testing.T) {
	useIndexHome(t)
	dir := t.TempDir()
	writeIndexState(t, dir, indexAeonID, "Website", "")
	const start = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee:42"
	if err := writeSessionIndex(indexSourceID, dir, os.Getpid(), start); err != nil {
		t.Fatal(err)
	}
	raw, err := readOwnerFile(filepath.Join(mustIndexRoot(t), indexSourceID), 4096)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := parseSessionIndexEntry(raw)
	if !ok || entry.OwnerStart != start || entry.OwnerPID != os.Getpid() {
		t.Fatalf("stored entry %+v ok %v", entry, ok)
	}
	if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexRejected {
		t.Fatalf("synthetic linux stamp bound: %d", result)
	}
	rawSource, err := os.ReadFile(filepath.Join(dir, sessionIndexSourceFile))
	if err != nil || string(rawSource) != indexSourceID+"\n" {
		t.Fatalf("recorded source %q err %v", rawSource, err)
	}
}

func TestOwnerStampUintRejectsOverflow(t *testing.T) {
	max := strconv.FormatUint(^uint64(0), 10)
	overflow := "18446744073709551616"
	if !ownerStampUint(max) || !ownerStampUint("0") {
		t.Fatal("uint64 range rejected")
	}
	for _, token := range []string{overflow, "99999999999999999999", "18446744073709551616", ""} {
		if ownerStampUint(token) {
			t.Fatalf("accepted %q", token)
		}
	}
	boot := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	if validOwnerStart(max+"."+max) == false || validOwnerStart(boot+":"+max) == false {
		t.Fatal("max stamps rejected")
	}
	if validOwnerStart(overflow+".1") || validOwnerStart("1."+overflow) || validOwnerStart(boot+":"+overflow) {
		t.Fatal("overflow stamps accepted")
	}
}

func setStatMode[M ~uint16 | ~uint32](mode *M, perm uint32) {
	*mode = M(perm)
}

func TestSessionIndexTrustRejectsForeignAndLooseModes(t *testing.T) {
	own := unix.Stat_t{Mode: unix.S_IFDIR | 0o700, Uid: uint32(os.Getuid())}
	if !sessionIndexDirTrusted(own) {
		t.Fatal("owned 0700 directory rejected")
	}
	foreign := own
	foreign.Uid ^= 1
	if foreign.Uid == own.Uid || sessionIndexDirTrusted(foreign) {
		t.Fatal("foreign-owned directory accepted")
	}
	for _, mode := range []uint32{0o755, 0o775, 0o770, 0o707, 0o750} {
		loose := own
		setStatMode(&loose.Mode, unix.S_IFDIR|mode)
		if sessionIndexDirTrusted(loose) {
			t.Fatalf("index mode %o accepted", mode)
		}
	}
	parent := unix.Stat_t{Mode: unix.S_IFDIR | 0o755, Uid: own.Uid}
	if !sessionIndexParentTrusted(parent) {
		t.Fatal("0755 parent rejected")
	}
	setStatMode(&parent.Mode, unix.S_IFDIR|0o775)
	if sessionIndexParentTrusted(parent) {
		t.Fatal("group-writable parent accepted")
	}
	setStatMode(&parent.Mode, unix.S_IFDIR|0o707)
	if sessionIndexParentTrusted(parent) {
		t.Fatal("world-writable parent accepted")
	}
	setStatMode(&parent.Mode, unix.S_IFDIR|0o755)
	parent.Uid = foreign.Uid
	if sessionIndexParentTrusted(parent) {
		t.Fatal("foreign-owned parent accepted")
	}
}

func TestSessionIndexRejectsLooseDirectoryAtLookupLockAndRemoval(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o770, 0o775, 0o707} {
		t.Run(fmt.Sprintf("index-%o", mode), func(t *testing.T) {
			refuseLooseIndex(t, func(root string) {
				if err := os.Chmod(root, mode); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
	t.Run("group-writable sessions", func(t *testing.T) {
		refuseLooseIndex(t, func(root string) {
			if err := os.Chmod(filepath.Dir(root), 0o775); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("group-writable home", func(t *testing.T) {
		refuseLooseIndex(t, func(string) {
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			home = canonicalPrivatePath(home)
			if err := os.Chmod(home, 0o775); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("private parent still binds", func(t *testing.T) {
		useIndexHome(t)
		dir := t.TempDir()
		writeIndexState(t, dir, indexAeonID, "Website", "")
		publishIndex(t, indexSourceID, dir)
		if err := os.Chmod(filepath.Dir(mustIndexRoot(t)), 0o755); err != nil {
			t.Fatal(err)
		}
		if id, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexBound || id != indexAeonID {
			t.Fatalf("0755 parent refused a private index: %s %d", id, result)
		}
	})
}

func refuseLooseIndex(t *testing.T, loosen func(root string)) {
	t.Helper()
	useIndexHome(t)
	dir := t.TempDir()
	writeIndexState(t, dir, indexAeonID, "Website", "")
	publishIndex(t, indexSourceID, dir)
	root := mustIndexRoot(t)
	entry := filepath.Join(root, indexSourceID)
	loosen(root)
	if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexRejected {
		t.Fatalf("lookup accepted an unsafe index: %d", result)
	}
	other := t.TempDir()
	writeIndexState(t, other, indexAeonAlt, "Other", "")
	pid, start := currentOwner(t)
	if err := writeSessionIndex(indexOtherID, other, pid, start); err == nil {
		t.Fatal("publish accepted an unsafe index")
	}
	removeSessionIndexForState(dir)
	if _, err := os.Lstat(entry); err != nil {
		t.Fatalf("removal unlinked through an unsafe index: %v", err)
	}
}

func TestSessionIndexRejectsRenamedEntryInLooseDirectory(t *testing.T) {
	useIndexHome(t)
	dirB := t.TempDir()
	writeIndexState(t, dirB, indexAeonAlt, "Other", "")
	publishIndex(t, indexOtherID, dirB)
	root := mustIndexRoot(t)
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(root, indexSourceID)
	if err := os.Rename(filepath.Join(root, indexOtherID), renamed); err != nil {
		t.Fatal(err)
	}
	id, _, result := lookupSessionIndex(indexSourceID)
	if result == sessionIndexBound || id != "" {
		t.Fatalf("rename attack bound %s result %d", id, result)
	}
	dirA := t.TempDir()
	writeIndexState(t, dirA, indexAeonID, "Website", "")
	pid, start := currentOwner(t)
	if err := writeSessionIndex(indexSourceID, dirA, pid, start); err == nil {
		t.Fatal("publish accepted the renamed loose directory")
	}
	removeSessionIndexForState(dirB)
	if _, err := os.Lstat(renamed); err != nil {
		t.Fatalf("removal deleted the renamed entry: %v", err)
	}
}

func TestSessionIndexRejectsForeignOwnedDirectory(t *testing.T) {
	useIndexHome(t)
	dir := t.TempDir()
	writeIndexState(t, dir, indexAeonID, "Website", "")
	publishIndex(t, indexSourceID, dir)
	root := mustIndexRoot(t)
	other := os.Getuid() ^ 1
	if err := unix.Lchown(root, other, os.Getgid()); err != nil {
		if os.Getenv("AEON_REQUIRE_CHOWN") == "1" {
			t.Fatalf("chown: %v", err)
		}
		t.Logf("chown unavailable: %v", err)
		return
	}
	t.Cleanup(func() { _ = unix.Lchown(root, os.Getuid(), os.Getgid()) })
	if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexRejected {
		t.Fatalf("lookup foreign owner: %d", result)
	}
	otherDir := t.TempDir()
	writeIndexState(t, otherDir, indexAeonAlt, "Other", "")
	pid, start := currentOwner(t)
	if err := writeSessionIndex(indexOtherID, otherDir, pid, start); err == nil {
		t.Fatal("publish accepted a foreign-owned index")
	}
	removeSessionIndexForState(dir)
	if err := unix.Lchown(root, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, indexSourceID)); err != nil {
		t.Fatalf("removal unlinked through a foreign-owned index: %v", err)
	}
}

func TestSessionIndexUsesLockedDirectoryAfterReplacement(t *testing.T) {
	t.Cleanup(func() { sessionIndexHeld = nil })

	t.Run("publish", func(t *testing.T) {
		useIndexHome(t)
		live := t.TempDir()
		other := t.TempDir()
		writeIndexState(t, live, indexAeonID, "Website", "")
		writeIndexState(t, other, indexAeonAlt, "Other", "")
		publishIndex(t, indexSourceID, live)
		root := mustIndexRoot(t)
		before, err := os.ReadFile(filepath.Join(root, indexSourceID))
		if err != nil {
			t.Fatal(err)
		}
		swapErr := swapIndexAfterLock(t, root, func(string) {})
		pid, start := currentOwner(t)
		err = writeSessionIndex(indexSourceID, other, pid, start)
		sessionIndexHeld = nil
		if swapErr() != nil {
			t.Fatal(swapErr())
		}
		if !errors.Is(err, errSessionIndexConflict) {
			t.Fatalf("missed live binding after replacement: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(root+".aside", indexSourceID))
		if err != nil || !bytes.Equal(got, before) {
			t.Fatalf("locked entry changed %q %v", got, err)
		}
		if _, err := os.Lstat(filepath.Join(root, indexSourceID)); !os.IsNotExist(err) {
			t.Fatalf("replacement directory was written: %v", err)
		}
	})

	t.Run("removal", func(t *testing.T) {
		useIndexHome(t)
		live := t.TempDir()
		keep := t.TempDir()
		writeIndexState(t, live, indexAeonID, "Website", "")
		writeIndexState(t, keep, indexAeonAlt, "Other", "")
		publishIndex(t, indexSourceID, live)
		publishIndex(t, indexOtherID, keep)
		root := mustIndexRoot(t)
		keepBefore, err := os.ReadFile(filepath.Join(root, indexOtherID))
		if err != nil {
			t.Fatal(err)
		}
		swapErr := swapIndexAfterLock(t, root, func(repl string) {
			if err := os.WriteFile(filepath.Join(repl, indexSourceID), legacyIndexLine(t, keep), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repl, indexOtherID), legacyIndexLine(t, live), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		removeSessionIndexForState(live)
		sessionIndexHeld = nil
		if swapErr() != nil {
			t.Fatal(swapErr())
		}
		if _, err := os.Lstat(filepath.Join(root+".aside", indexSourceID)); !os.IsNotExist(err) {
			t.Fatalf("live entry remained in the locked directory: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(root+".aside", indexOtherID))
		if err != nil || !bytes.Equal(got, keepBefore) {
			t.Fatalf("removal unlinked the wrong entry %q %v", got, err)
		}
		replSource, err := os.ReadFile(filepath.Join(root, indexSourceID))
		if err != nil || !bytes.Equal(replSource, legacyIndexLine(t, keep)) {
			t.Fatalf("replacement source changed %q %v", replSource, err)
		}
		replKeep, err := os.ReadFile(filepath.Join(root, indexOtherID))
		if err != nil || !bytes.Equal(replKeep, legacyIndexLine(t, live)) {
			t.Fatalf("replacement victim changed %q %v", replKeep, err)
		}
	})
}

func swapIndexAfterLock(t *testing.T, root string, plant func(repl string)) func() error {
	t.Helper()
	repl := filepath.Join(t.TempDir(), "repl")
	if err := os.Mkdir(repl, 0o700); err != nil {
		t.Fatal(err)
	}
	plant(repl)
	aside := root + ".aside"
	var swapErr error
	swapped := false
	sessionIndexHeld = func(dirfd int, got string) {
		if swapped {
			return
		}
		swapped = true
		if got != root {
			swapErr = fmt.Errorf("locked root %s", got)
			return
		}
		if err := os.Rename(got, aside); err != nil {
			swapErr = err
			return
		}
		swapErr = os.Rename(repl, got)
		_ = dirfd
	}
	t.Cleanup(func() { sessionIndexHeld = nil })
	return func() error { return swapErr }
}

func legacyIndexLine(t *testing.T, dir string) []byte {
	t.Helper()
	abs := canonicalPrivatePath(dir)
	if abs == "" {
		t.Fatal("empty state path")
	}
	return []byte(abs + "\n")
}

func TestSessionIndexRemovalCoversEveryEntry(t *testing.T) {
	useIndexHome(t)
	dir := t.TempDir()
	writeIndexState(t, dir, indexAeonID, "Website", "")
	abs := canonicalPrivatePath(dir)
	root := mustIndexRoot(t)
	if err := mkdirPrivate(root); err != nil {
		t.Fatal(err)
	}
	const n = 4097
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("11111111-1111-4111-8111-%012x", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte(abs+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	recorded := fmt.Sprintf("11111111-1111-4111-8111-%012x", n-1)
	if err := os.WriteFile(filepath.Join(dir, sessionIndexSourceFile), []byte(recorded+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	writeIndexState(t, other, indexAeonAlt, "Other", "")
	publishIndex(t, indexOtherID, other)
	removeSessionIndexForState(dir)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, entry := range entries {
		name := entry.Name()
		if name == indexOtherID || name == sessionIndexLockFile {
			continue
		}
		left = append(left, name)
	}
	if len(left) != 0 {
		t.Fatalf("removal left %d entries, first %q", len(left), left[0])
	}
	if id, _, result := lookupSessionIndex(indexOtherID); result != sessionIndexBound || id != indexAeonAlt {
		t.Fatalf("bulk removal deleted another generation: %s %d", id, result)
	}
}

func TestStartupFailureWithdrawsIndexBinding(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	state := t.TempDir()
	writeIndexState(t, state, indexAeonID, "Website", "")
	if err := os.WriteFile(filepath.Join(state, "lease.key"), []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	publishIndex(t, indexSourceID, state)
	other := t.TempDir()
	writeIndexState(t, other, indexAeonAlt, "Other", "")
	publishIndex(t, indexOtherID, other)
	if _, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexBound {
		t.Fatal("precondition: binding was not live")
	}
	o := heartbeatTestOptions(t.TempDir())
	o.StateDir = state
	o.SourceSession = indexSourceID
	o.OwnerPID = os.Getpid()
	err := rt.runHeartbeat(context.Background(), o, heartbeatDeps{
		alive: func(int) bool { return true },
		wait:  func(context.Context, int, time.Duration) error { return errOwnerExited },
	})
	if err == nil || errors.Is(err, errHeartbeatBusy) {
		t.Fatalf("startup err %v stderr %s", err, stderr.String())
	}
	if _, err := os.Lstat(filepath.Join(mustIndexRoot(t), indexSourceID)); !os.IsNotExist(err) {
		t.Fatalf("startup failure left the index entry: %v", err)
	}
	if id, _, result := lookupSessionIndex(indexOtherID); result != sessionIndexBound || id != indexAeonAlt {
		t.Fatalf("startup failure removed a different generation: %s %d", id, result)
	}
}

func TestBusyStateLockPreservesIndexBinding(t *testing.T) {
	if os.Getenv("AEON_INDEX_STATE_LOCK_CHILD") == "1" {
		holdStateLockChild()
		os.Exit(0)
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	state := t.TempDir()
	writeIndexState(t, state, indexAeonID, "Website", "")
	publishIndex(t, indexSourceID, state)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	side := t.TempDir()
	ready := filepath.Join(side, "ready")
	release := filepath.Join(side, "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestBusyStateLockPreservesIndexBinding$")
	cmd.Env = append(os.Environ(),
		"AEON_INDEX_STATE_LOCK_CHILD=1",
		"HOME="+home,
		"AEON_INDEX_STATE_DIR="+state,
		"AEON_INDEX_READY="+ready,
		"AEON_INDEX_RELEASE="+release,
	)
	var childErr bytes.Buffer
	cmd.Stderr = &childErr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, statErr := os.Stat(ready); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state lock holder did not start: %s", childErr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	o := heartbeatTestOptions(t.TempDir())
	o.StateDir = state
	o.SourceSession = indexSourceID
	o.OwnerPID = os.Getpid()
	err = rt.runHeartbeat(context.Background(), o, heartbeatDeps{
		alive: func(int) bool { return true },
		wait:  func(context.Context, int, time.Duration) error { return errOwnerExited },
	})
	if !errors.Is(err, errHeartbeatBusy) {
		t.Fatalf("busy lock: %v", err)
	}
	if id, _, result := lookupSessionIndex(indexSourceID); result != sessionIndexBound || id != indexAeonID {
		t.Fatalf("busy lock withdrew the binding: %s %d", id, result)
	}
	if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("lock holder: %v %s", err, childErr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lock holder did not exit")
	}
}

func TestSessionIndexEnumerationFdNotInherited(t *testing.T) {
	if os.Getenv("AEON_INDEX_FD_CHILD") == "1" {
		fd, dev, ino, err := indexChildIdentity()
		if err != nil {
			t.Fatal(err)
		}
		if how := inheritedIndexFd(fd, dev, ino); how != "" {
			t.Fatalf("enumeration descriptor inherited via %s", how)
		}
		fmt.Fprintln(os.Stdout, "index-fd-closed")
		return
	}

	useIndexHome(t)
	root := mustIndexRoot(t)
	if err := mkdirPrivate(root); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, indexSourceID)
	if err := os.WriteFile(marker, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirfd, err := openValidatedIndexDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dirfd)
	var st unix.Stat_t
	if err := unix.Fstat(dirfd, &st); err != nil {
		t.Fatal(err)
	}

	probed := false
	sessionIndexEnumerating = func(dir *os.File) {
		probed = true
		fd := int(dir.Fd())
		flags, ferr := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if ferr != nil {
			t.Fatalf("F_GETFD: %v", ferr)
		}
		if flags&unix.FD_CLOEXEC == 0 {
			t.Fatal("enumeration descriptor is missing FD_CLOEXEC")
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestSessionIndexEnumerationFdNotInherited$")
		cmd.Env = append(os.Environ(),
			"AEON_INDEX_FD_CHILD=1",
			"AEON_INDEX_FD="+strconv.Itoa(fd),
			"AEON_INDEX_DEV="+strconv.FormatUint(uint64(st.Dev), 10),
			"AEON_INDEX_INO="+strconv.FormatUint(st.Ino, 10),
		)
		out, err := cmd.CombinedOutput()
		if err != nil || !bytes.Contains(out, []byte("index-fd-closed")) {
			t.Fatalf("child: %v %s", err, out)
		}
	}
	t.Cleanup(func() { sessionIndexEnumerating = nil })

	names := indexEntryNames(dirfd)
	if !probed {
		t.Fatal("enumeration did not duplicate the directory")
	}
	found := false
	for _, name := range names {
		if name == indexSourceID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("enumeration missed %s in %v", indexSourceID, names)
	}
}

func indexChildIdentity() (int, uint64, uint64, error) {
	fd, err := strconv.Atoi(os.Getenv("AEON_INDEX_FD"))
	if err != nil || fd < 0 {
		return 0, 0, 0, errors.New("enumeration child fd")
	}
	dev, err := strconv.ParseUint(os.Getenv("AEON_INDEX_DEV"), 10, 64)
	if err != nil {
		return 0, 0, 0, errors.New("enumeration child dev")
	}
	ino, err := strconv.ParseUint(os.Getenv("AEON_INDEX_INO"), 10, 64)
	if err != nil {
		return 0, 0, 0, errors.New("enumeration child ino")
	}
	return fd, dev, ino, nil
}

// inheritedIndexFd reports how a child still sees the parent's directory
// descriptor. fcntl identifies the fd number; /dev/fd is the path check.
func inheritedIndexFd(fd int, dev, ino uint64) string {
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil && sameIndexFile(fd, dev, ino) {
		return "fcntl"
	}
	f, err := os.Open("/dev/fd/" + strconv.Itoa(fd))
	if err != nil {
		return ""
	}
	defer f.Close()
	if sameIndexFile(int(f.Fd()), dev, ino) {
		return "/dev/fd"
	}
	return ""
}

func sameIndexFile(fd int, dev, ino uint64) bool {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		return false
	}
	return uint64(st.Dev) == dev && st.Ino == ino
}

func holdStateLockChild() {
	hold, err := openHeartbeatHold(os.Getenv("AEON_INDEX_STATE_DIR"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "state lock:", err.Error())
		os.Exit(1)
	}
	defer hold.release()
	if err := os.WriteFile(os.Getenv("AEON_INDEX_READY"), []byte("ready\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "ready:", err.Error())
		os.Exit(1)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(os.Getenv("AEON_INDEX_RELEASE")); statErr == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "state lock holder timed out")
	os.Exit(1)
}

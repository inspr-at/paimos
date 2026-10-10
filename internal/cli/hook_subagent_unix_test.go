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
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
)

type subagentFixture struct {
	dir                                string
	config                             string
	srv                                *httptest.Server
	mu                                 sync.Mutex
	registrations                      []map[string]any
	childSessions                      map[string]map[string]any
	childSequences                     map[string]int
	beats, stops, reads                int
	failStart, failStop, parentStopped bool
	shape                              string
	entered, release                   chan struct{}
}

func newSubagentFixture(t *testing.T, shape string) *subagentFixture {
	t.Helper()
	f := &subagentFixture{config: setupHookTest(t), dir: t.TempDir(), shape: shape, childSessions: make(map[string]map[string]any), childSequences: make(map[string]int)}
	useIndexHome(t)
	writeIndexState(t, f.dir, indexAeonID, "Parent", `"project_id":"`+transcriptProjectID+`"`)
	publishIndex(t, indexSourceID, f.dir)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Method == http.MethodPost && json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request body")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET " + harnessPath(transcriptProjectID, indexAeonID):
			f.reads++
			p := harness.Session{ID: indexAeonID, ProjectID: transcriptProjectID, AgentPrincipalID: hookMessageID, Host: "fixture-host", TicketNodeID: ptrHook(indexMessage), WorkShape: f.shape}
			if f.parentStopped {
				now := time.Now()
				p.StoppedAt = &now
			}
			json.NewEncoder(w).Encode(p)
		case "POST " + harnessPath(transcriptProjectID, ""):
			f.registrations = append(f.registrations, body)
			if f.entered != nil {
				entered, release := f.entered, f.release
				f.entered = nil
				f.mu.Unlock()
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
				}
				f.mu.Lock()
			}
			if f.failStart {
				http.Error(w, `{"error":"private fixture failure"}`, 503)
				return
			}
			id := ""
			for candidate, registered := range f.childSessions {
				if registered["harness_session_ref"] == body["harness_session_ref"] {
					if !reflect.DeepEqual(registered, body) {
						t.Error("registration replay changed metadata or proof")
					}
					id = candidate
				}
			}
			if id == "" {
				id = indexAeonAlt
				if len(f.childSessions) != 0 {
					id = hookMessageID
				}
				f.childSessions[id] = body
			}
			fmt.Fprintf(w, `{"id":%q}`, id)
		case "POST " + harnessPath(transcriptProjectID, indexAeonAlt) + "/heartbeat", "POST " + harnessPath(transcriptProjectID, hookMessageID) + "/heartbeat":
			f.beats++
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, harnessPath(transcriptProjectID, "")+"/"), "/heartbeat")
			f.childSequences[id]++
			if body["activity_sequence"] != float64(f.childSequences[id]) || body["phase"] != "working" {
				t.Error("heartbeat did not use a monotonically increasing activity sequence")
			}
			if f.childSessions[id] == nil || r.Header.Get("X-Aeon-Worker-Lease") != f.childSessions[id]["worker_lease"] {
				t.Error("heartbeat lost child proof")
			}
			fmt.Fprint(w, `{}`)
		case "POST " + harnessPath(transcriptProjectID, indexAeonAlt) + "/stop", "POST " + harnessPath(transcriptProjectID, hookMessageID) + "/stop":
			f.stops++
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, harnessPath(transcriptProjectID, "")+"/"), "/stop")
			if f.childSessions[id] == nil || r.Header.Get("X-Aeon-Worker-Lease") != f.childSessions[id]["worker_lease"] {
				t.Error("stop lost child proof")
			}
			if f.failStop {
				http.Error(w, `{"error":"private fixture failure"}`, 503)
				return
			}
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected endpoint %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	t.Setenv("AEON_URL", f.srv.URL)
	t.Setenv("AEON_API_KEY", "fixture-subagent-key")
	return f
}

func (f *subagentFixture) invoke(t *testing.T, event string, extra string) string {
	t.Helper()
	var out, stderr bytes.Buffer
	body := `{"hook_event_name":"` + event + `","session_id":"` + indexSourceID + `","agent_id":"agent-fixture","agent_type":"Explore"` + extra + `}`
	code := RunMessaging([]string{"aeon", "--config", f.config, "hook", "claude", event}, strings.NewReader(body), &out, &stderr)
	if code != 0 || out.Len() != 0 || strings.Contains(stderr.String(), "private fixture failure") {
		t.Fatal("lifecycle hook blocked, emitted context or exposed an API error")
	}
	return stderr.String()
}

func (f *subagentFixture) parent(t *testing.T) *heartbeatSession {
	t.Helper()
	dir, err := openSubagentParent(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	p := &heartbeatSession{id: indexAeonID, disk: heartbeatDisk{ProjectID: transcriptProjectID}, hold: heartbeatHold{dir: dir}}
	t.Cleanup(func() {
		if p.subagentScan != nil {
			p.subagentScan.Close()
		}
		dir.Close()
	})
	return p
}

func (f *subagentFixture) rt() *runtime {
	return &runtime{personClient: client.New(f.srv.URL, "fixture-subagent-key"), stdout: new(bytes.Buffer), stderr: new(bytes.Buffer)}
}

func (f *subagentFixture) childDisk(t *testing.T) heartbeatDisk {
	t.Helper()
	path := filepath.Join(f.dir, "subagents", subagentName("agent-fixture"), "state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d heartbeatDisk
	if json.Unmarshal(raw, &d) != nil {
		t.Fatal("invalid child state")
	}
	return d
}

// Risk: invisible children, wrong parent/ticket/shape, parent inbox theft or
// stop mistakenly reported successful without closing the child generation.
func TestClaudeSubagentHookLifecycle(t *testing.T) {
	for _, shape := range []string{"ship", "scout", "unknown"} {
		t.Run(shape, func(t *testing.T) {
			f := newSubagentFixture(t, shape)
			parentBefore, err := os.ReadFile(filepath.Join(f.dir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if stderr := f.invoke(t, "SubagentStart", `,"description":"inspect hook lifecycle","model":"claude-opus-4-6"`); stderr != "" {
				t.Fatal(stderr)
			}
			f.invoke(t, "SubagentStart", "")
			p := f.parent(t)
			f.rt().heartbeatSubagents(t.Context(), p, false)
			if len(f.registrations) != 1 || f.beats != 3 {
				t.Fatal("duplicate registration or missing maintained heartbeat")
			}
			b := f.registrations[0]
			wantShape := "scout"
			if shape == "ship" {
				wantShape = "ship"
			}
			if b["parent_harness_session_id"] != indexAeonID || b["ticket_node_id"] != indexMessage || b["work_shape"] != wantShape || b["role"] != "worker" || b["harness"] != "claude" || b["agent_principal_id"] != hookMessageID || b["display_label"] != "Explore: inspect hook lifecycle" || b["model"] != "claude-opus-4-6" {
				t.Fatal("child registration did not inherit the intended metadata")
			}
			if !reflect.DeepEqual(b["advertised_capabilities"], []any{"status"}) {
				t.Fatal("child advertised inbox or process control")
			}
			f.invoke(t, "SubagentStop", `,"stop_hook_active":true`)
			f.invoke(t, "SubagentStop", "")
			if f.stops != 1 || !f.childDisk(t).Closed {
				t.Fatal("child not stopped exactly once")
			}
			parentAfter, _ := os.ReadFile(filepath.Join(f.dir, "state.json"))
			if !bytes.Equal(parentBefore, parentAfter) {
				t.Fatal("hook changed the parent's generation")
			}
			root := filepath.Join(f.dir, "subagents", subagentName("agent-fixture"))
			info, err := os.Stat(root)
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatal("child directory is not private")
			}
			files, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range files {
				info, err := entry.Info()
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
					t.Fatal("child file is not private and regular")
				}
			}
		})
	}
}

// Risk: Claude resumes the same agent_id after completion, leaving it invisible
// or immediately stopping it with the previous run's proof and stop intent.
func TestClaudeSubagentHookResume(t *testing.T) {
	f := newSubagentFixture(t, "ship")
	f.invoke(t, "SubagentStart", `,"description":"first run"`)
	first := f.childDisk(t)
	f.invoke(t, "SubagentStop", "")
	if f.stops != 1 || !f.childDisk(t).Closed {
		t.Fatal("first generation was not stopped")
	}
	// Parent maintenance must not revive a closed generation by itself.
	p := f.parent(t)
	f.rt().heartbeatSubagents(t.Context(), p, false)
	if len(f.registrations) != 1 || f.beats != 1 {
		t.Fatal("parent maintenance revived a closed child")
	}
	if stderr := f.invoke(t, "SubagentStart", `,"description":"resumed run"`); stderr != "" {
		t.Fatal(stderr)
	}
	resumed := f.childDisk(t)
	if len(f.registrations) != 2 || resumed.SessionID == first.SessionID || resumed.Closed || resumed.Terminal || resumed.Sequence != 1 || f.beats != 2 || f.stops != 1 {
		t.Fatal("resume did not register and heartbeat a fresh active generation")
	}
	a, b := f.registrations[0], f.registrations[1]
	if a["harness_session_ref"] == b["harness_session_ref"] || a["worker_lease"] == b["worker_lease"] {
		t.Fatal("resume reused the completed generation's reference or proof")
	}
	if b["parent_harness_session_id"] != indexAeonID || b["ticket_node_id"] != indexMessage || b["work_shape"] != "ship" || b["display_label"] != "Explore: resumed run" {
		t.Fatal("resumed generation lost its inherited metadata or new label")
	}
	f.invoke(t, "SubagentStart", `,"description":"resumed run"`)
	f.rt().heartbeatSubagents(t.Context(), p, false)
	if len(f.registrations) != 2 || f.beats != 4 || f.childDisk(t).Sequence != 3 {
		t.Fatal("active resume was duplicated or its heartbeat sequence was lost")
	}
	f.invoke(t, "SubagentStop", "")
	f.invoke(t, "SubagentStop", "")
	if f.stops != 2 || !f.childDisk(t).Closed {
		t.Fatal("resumed generation was not stopped exactly once")
	}
}

// Risk: a valid final response over 64 KiB discards the lifecycle stop, so the
// parent keeps heartbeating a child that has already finished.
func TestClaudeSubagentHookLargeStopPayload(t *testing.T) {
	f := newSubagentFixture(t, "scout")
	if stderr := f.invoke(t, "SubagentStart", ""); stderr != "" {
		t.Fatal(stderr)
	}
	if len(f.registrations) != 1 || f.beats != 1 || f.childDisk(t).Closed {
		t.Fatal("large-stop fixture did not start an active child")
	}
	response := strings.Repeat("final response content ", 8192)
	if stderr := f.invoke(t, "SubagentStop", `,"last_assistant_message":"`+response+`","agent_transcript_path":"/unused/transcript.jsonl"`); stderr != "" {
		t.Fatal(stderr)
	}
	if f.stops != 1 || !f.childDisk(t).Closed {
		t.Fatal("large stop payload did not close the child generation")
	}
	f.rt().heartbeatSubagents(t.Context(), f.parent(t), false)
	if f.beats != 1 || f.stops != 1 {
		t.Fatal("parent continued heartbeating a finished child")
	}
	root := filepath.Join(f.dir, "subagents", subagentName("agent-fixture"))
	files, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join(root, file.Name()))
		if err != nil || bytes.Contains(raw, []byte("final response content")) || bytes.Contains(raw, []byte("/unused/transcript.jsonl")) {
			t.Fatal("stop response or transcript path was persisted")
		}
	}
}

func TestClaudeSubagentHookNoOp(t *testing.T) {
	for _, kind := range []string{"no-index", "local-stopped", "server-stopped", "unsafe-directory"} {
		t.Run(kind, func(t *testing.T) {
			f := newSubagentFixture(t, "scout")
			switch kind {
			case "no-index":
				removeSessionIndexForState(f.dir)
				t.Setenv("AEON_SESSION_ID", indexAeonID)
			case "local-stopped":
				writeIndexState(t, f.dir, indexAeonID, "Parent", `"closed":true,"project_id":"`+transcriptProjectID+`"`)
			case "server-stopped":
				f.parentStopped = true
			case "unsafe-directory":
				if err := os.Symlink(t.TempDir(), filepath.Join(f.dir, "subagents")); err != nil {
					t.Fatal(err)
				}
			}
			stderr := f.invoke(t, "SubagentStart", "")
			f.invoke(t, "SubagentStop", "")
			if len(f.registrations) != 0 || f.stops != 0 || f.beats != 0 {
				t.Fatal("unbound, stopped or unsafe parent created a child")
			}
			if (kind == "no-index" || kind == "local-stopped") && (f.reads != 0 || stderr != "") {
				t.Fatal("missing/stopped index was not a quiet no-op")
			}
		})
	}
}

func TestClaudeSubagentHookStopDuringRegistration(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(fmt.Sprintf("resumed=%t", resumed), func(t *testing.T) {
			testClaudeSubagentHookStopDuringRegistration(t, resumed)
		})
	}
}

func testClaudeSubagentHookStopDuringRegistration(t *testing.T, resumed bool) {
	f := newSubagentFixture(t, "scout")
	wantStops, wantBeats := 1, 0
	if resumed {
		f.invoke(t, "SubagentStart", "")
		f.invoke(t, "SubagentStop", "")
		if f.stops != 1 || !f.childDisk(t).Closed {
			t.Fatal("previous generation did not stop")
		}
		wantStops, wantBeats = 2, 1
	}
	f.entered, f.release = make(chan struct{}), make(chan struct{})
	entered := f.entered
	done := make(chan string, 1)
	go func() { done <- f.invoke(t, "SubagentStart", "") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not reach the barrier")
	}
	if stderr := f.invoke(t, "SubagentStop", ""); stderr != "" {
		t.Fatal(stderr)
	}
	close(f.release)
	select {
	case stderr := <-done:
		if stderr != "" {
			t.Fatal(stderr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("start did not finish")
	}
	if f.stops != wantStops || f.beats != wantBeats || !f.childDisk(t).Closed {
		t.Fatal("stop arriving during registration was lost")
	}
}

func TestClaudeSubagentHookRetries(t *testing.T) {
	f := newSubagentFixture(t, "scout")
	f.failStart = true
	if stderr := f.invoke(t, "SubagentStart", ""); stderr == "" {
		t.Fatal("registration failure was not surfaced")
	}
	f.failStart = false
	p := f.parent(t)
	f.rt().heartbeatSubagents(t.Context(), p, false)
	if len(f.registrations) != 2 || !reflect.DeepEqual(f.registrations[0], f.registrations[1]) || f.beats != 1 {
		t.Fatal("retry changed registration identity or failed to heartbeat")
	}
	f.failStop = true
	f.invoke(t, "SubagentStop", "")
	if f.childDisk(t).Closed {
		t.Fatal("failed stop was persisted as success")
	}
	f.failStop = false
	f.rt().heartbeatSubagents(t.Context(), p, false)
	if f.stops != 2 || !f.childDisk(t).Closed {
		t.Fatal("parent heartbeat did not retry stop")
	}
}

func TestClaudeSubagentHookCancellation(t *testing.T) {
	f := newSubagentFixture(t, "scout")
	f.entered, f.release = make(chan struct{}), make(chan struct{})
	entered := f.entered
	rt := f.rt()
	rt.stdin = strings.NewReader(`{"hook_event_name":"SubagentStart","session_id":"` + indexSourceID + `","agent_id":"agent-fixture","agent_type":"Explore"}`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.runSubagentHook(ctx, "SubagentStart") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not reach barrier")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wrong cancellation failure: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled hook remained blocked")
	}
	if f.stops != 0 || f.beats != 0 {
		t.Fatal("canceled registration continued work")
	}
}

// Real API, auth and database: the resulting rows are the same generations
// returned to Agents, with an independent worker proof and no vendor collision.
func TestClaudeSubagentHookEndToEnd(t *testing.T) {
	config := setupHookTest(t)
	useIndexHome(t)
	d := dbtest.Open(t)
	if err := db.EnsureTenant(t.Context(), d.App, "aeon", "Aeon"); err != nil {
		t.Fatal(err)
	}
	base := renameServer(t, d, "aeon", true)
	key := mintAgent(t, base, "subagent-parent")
	seedProject(t, base, key.Token)
	t.Setenv("AEON_URL", base)
	t.Setenv("AEON_API_KEY", key.Token)
	c := client.New(base, key.Token)
	rt := &runtime{personClient: c, stdout: new(bytes.Buffer), stderr: new(bytes.Buffer)}
	project, err := rt.harnessProjectCtx(t.Context(), "AEON")
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runCLI([]string{"aeon", "--config", config, "--json", "issue", "create", "--project", "AEON", "--title", "Subagent hook test", "--type", "work"}, "")
	var ticket apiNode
	if code != 0 || json.Unmarshal([]byte(out), &ticket) != nil || !validUUID(ticket.ID) {
		t.Fatalf("ticket fixture failed: %s", stderr)
	}
	var parent harness.Session
	body := map[string]any{
		"agent_principal_id": key.PrincipalID, "harness": "claude", "host": "subagent-test",
		"management_mode": "unmanaged", "role": "worker", "ticket_node_id": ticket.ID, "work_shape": "ship",
		"harness_session_ref": "fixture-parent-ref-00000000000000001", "worker_lease": "fixture-parent-lease-00000000000000001", "vendor_session_ref": indexSourceID,
	}
	if err := c.Do(t.Context(), http.MethodPost, harnessPath(project, ""), body, &parent); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeIndexState(t, dir, parent.ID, "Parent", `"project_id":"`+project+`"`)
	publishIndex(t, indexSourceID, dir)
	f := &subagentFixture{dir: dir, config: config}
	if stderr := f.invoke(t, "SubagentStart", ""); stderr != "" {
		t.Fatal(stderr)
	}
	childDisk := f.childDisk(t)
	var child harness.Session
	if err := c.Do(t.Context(), http.MethodGet, harnessPath(project, childDisk.SessionID), nil, &child); err != nil {
		t.Fatal(err)
	}
	if child.ParentID == nil || *child.ParentID != parent.ID || child.TicketNodeID == nil || *child.TicketNodeID != ticket.ID || child.WorkShape != "ship" || child.Role != "worker" || child.Harness != "claude" || child.Phase != "working" || child.ActivitySequence != 1 || child.StoppedAt != nil || child.HasVendorSessionRef {
		t.Fatal("real child lost metadata, liveness or parent vendor isolation")
	}
	var sessions []harness.Session
	if err := c.Do(t.Context(), http.MethodGet, harnessPath(project, ""), nil, &sessions); err != nil || len(sessions) != 2 {
		t.Fatal("Agents session inventory did not contain parent and child")
	}
	var agents struct {
		Items []harness.SessionSummary `json:"items"`
	}
	if err := c.Do(t.Context(), http.MethodGet, "/api/harness-sessions?project="+project+"&view=current", nil, &agents); err != nil {
		t.Fatal(err)
	}
	visible := false
	for _, item := range agents.Items {
		if item.ID == child.ID && item.ParentID != nil && *item.ParentID == parent.ID && item.Ticket != nil && item.Ticket.ID == ticket.ID {
			visible = true
		}
	}
	if !visible {
		t.Fatal("child was not visible under its parent in the Agents API")
	}
	if stderr := f.invoke(t, "SubagentStop", ""); stderr != "" {
		t.Fatal(stderr)
	}
	if err := c.Do(t.Context(), http.MethodGet, harnessPath(project, child.ID), nil, &child); err != nil {
		t.Fatal(err)
	}
	if child.StoppedAt == nil || child.Phase != "stopped" || !f.childDisk(t).Closed {
		t.Fatal("real child was not stopped by the hook")
	}
}

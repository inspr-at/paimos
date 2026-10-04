// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/reportercontract"
)

func TestHeartbeatStatusNotesUseSharedPrivacyFilter(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "progress.json")
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(home)
	o.StatusFile = path
	session := heartbeatSession{id: transcriptSessionID, lease: "synthetic-generation-lease-0000000000"}
	for _, note := range []string{"AKIAIOSFODNN7EXAMPLE", "https://user:pass@host/a", "FOO=secret", "FOO=example", "A\u0301KIAIOSFODNN7EXAMPLE", "abcdefghijkl\u0301mnopqrstuvwx", "A\u20ddKIAIOSFODNN7EXAMPLE", "s\u200bk-live", strings.Repeat("Reviewing the change. ", 8) + "AKIAIOSFODNN7EXAMPLE", "Reviewing the change", "Editing planning.ts"} {
		raw, _ := json.Marshal(map[string]any{"note": note})
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := rt.heartbeatBeat(t.Context(), o, heartbeatDeps{}, &session); err != nil {
			t.Fatal(err)
		}
		body := calls[len(calls)-1].body
		if note == "Reviewing the change" || note == "Editing planning.ts" {
			if body["activity_note"] != note {
				t.Fatal("public status-file note was dropped")
			}
		} else if _, sent := body["activity_note"]; sent {
			t.Fatal("unsafe status-file note was sent")
		}
	}
}

// The CLI's separate harness transport also accepts a response's new required
// field when an older consumer type does not declare it.
func TestHarnessLegacyDecoderIgnoresFinished(t *testing.T) {
	for _, finished := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(method+"/"+map[bool]string{false: "false", true: "true"}[finished], func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set(reportercontract.Header, reportercontract.HarnessSession)
					_ = json.NewEncoder(w).Encode(harness.Session{ID: transcriptSessionID, Finished: finished})
				}))
				defer server.Close()
				rt, _, _ := heartbeatRuntime(t, server)
				var legacy struct {
					ID string `json:"id"`
				}
				if err := rt.harnessDo(method, harnessPath(transcriptProjectID, transcriptSessionID), "", nil, &legacy); err != nil {
					t.Fatal(err)
				}
				if legacy.ID != transcriptSessionID {
					t.Fatalf("existing response field lost: %q", legacy.ID)
				}
			})
		}
	}
}

func TestStatusFileProgressParsingAndFence(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "progress.json")
	for _, tc := range []struct {
		raw     string
		ok      bool
		pct     *int
		minutes *float64
	}{
		{`{"pct":40,"remaining_min":25,"note":"Building"}`, true, ptr(40), ptr(25.0)},
		{`{"pct":0,"remaining_min":0}`, true, ptr(0), ptr(0.0)},
		{`{"pct":100,"remaining_min":0.5}`, true, ptr(100), ptr(.5)},
		{`{"pct":101,"remaining_min":-1}`, false, nil, nil},
		{`{"pct":1.5,"remaining_min":"25"}`, false, nil, nil},
		{`{"remaining_min":525601}`, false, nil, nil},
		{`{"remaining_min":525600}`, false, nil, nil},
		{`{"remaining_min":524160}`, true, nil, ptr(524160.0)},
		{`{"remaining_min":1e999}`, false, nil, nil},
		{`{"remaining_min":NaN}`, false, nil, nil},
		{`{"remaining_min":"NaN"}`, false, nil, nil},
		{`{`, false, nil, nil},
		{`null`, false, nil, nil},
		{`{"pct":null,"remaining_min":null}`, true, nil, nil},
	} {
		if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
			t.Fatal(err)
		}
		got, ok := readAgentStatus(heartbeatOptions{StatusFile: path})
		if ok != tc.ok || !samePointer(got.Progress, tc.pct) || !samePointer(got.Remaining, tc.minutes) {
			t.Fatalf("%s: %+v, %t", tc.raw, got, ok)
		}
	}
	for _, denied := range []string{filepath.Join(home, "absent.json"), filepath.Join(home, "auth.json"), filepath.Join(home, "secrets", "progress.json")} {
		if _, ok := readAgentStatus(heartbeatOptions{StatusFile: denied}); ok {
			t.Fatal("accepted missing or credential path")
		}
	}
	linked := filepath.Join(home, "linked.json")
	if err := os.Symlink(path, linked); err == nil {
		if _, ok := readAgentStatus(heartbeatOptions{StatusFile: linked}); ok {
			t.Fatal("followed symlink")
		}
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", 65537)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readAgentStatus(heartbeatOptions{StatusFile: path}); ok {
		t.Fatal("accepted oversized status")
	}
}

func ptr[T any](value T) *T { return &value }
func samePointer[T comparable](a, b *T) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func TestReadAgentStatusKeepsTheValidFieldWhenTheOtherIsInvalid(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "progress.json")
	for _, tc := range []struct {
		raw     string
		pct     *int
		minutes *float64
	}{
		{`{"pct":40,"remaining_min":"25"}`, ptr(40), nil},
		{`{"pct":101,"remaining_min":25}`, nil, ptr(25.0)},
	} {
		if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
			t.Fatal(err)
		}
		got, ok := readAgentStatus(heartbeatOptions{StatusFile: path})
		if ok || !samePointer(got.Progress, tc.pct) || !samePointer(got.Remaining, tc.minutes) {
			t.Fatalf("%s: ok=%t %+v", tc.raw, ok, got)
		}
	}
}

func TestHeartbeatRestartWithoutTicketKeepsEstimates(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "progress.json")
	if err := os.WriteFile(path, []byte(`{"pct":40,"remaining_min":25,"note":"Current step"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/api/nodes" && r.URL.Query().Get("q") == "AEON-465" {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{
				"id": transcriptEntryID, "key": "AEON-465", "kind_id": "ticket-kind", "title": "ETA",
			}}})
			return true
		}
		return false
	})
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(home)
	o.StatusFile = path
	o.Ticket = "AEON-465"
	o.Shape = "ship"
	deps := heartbeatDeps{alive: func(int) bool { return true }}
	session, created, err := rt.openHeartbeatSession(t.Context(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !created || session.disk.BoundTicket != "AEON-465" {
		session.hold.release()
		t.Fatalf("registration did not persist the ticket: created=%t ticket=%q", created, session.disk.BoundTicket)
	}
	if err := rt.heartbeatBeat(t.Context(), o, deps, &session); err != nil {
		session.hold.release()
		t.Fatal(err)
	}
	if err := saveHeartbeatSession(&session); err != nil {
		session.hold.release()
		t.Fatal(err)
	}
	session.hold.release()

	o.Ticket = ""
	o.Shape = ""
	resumed, created, err := rt.openHeartbeatSession(t.Context(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.hold.release()
	if created || resumed.disk.BoundTicket != "AEON-465" {
		t.Fatalf("restart lost the bound ticket: created=%t ticket=%q", created, resumed.disk.BoundTicket)
	}
	if err := rt.heartbeatBeat(t.Context(), o, deps, &resumed); err != nil {
		t.Fatal(err)
	}
	beats := hbWhere(calls, http.MethodPost, "/heartbeat")
	if len(beats) != 2 {
		t.Fatalf("beats %d", len(beats))
	}
	registrations := 0
	for _, call := range calls {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/harness-sessions") {
			registrations++
			if call.body["ticket_node_id"] != transcriptEntryID {
				t.Fatalf("registration ticket %#v", call.body["ticket_node_id"])
			}
		}
	}
	if registrations != 1 {
		t.Fatalf("restart registered again: %d", registrations)
	}
	for i, beat := range beats {
		if beat.body["progress_pct"] != float64(40) {
			t.Fatalf("beat %d dropped progress: %#v", i, beat.body["progress_pct"])
		}
		ready, err := time.Parse(time.RFC3339Nano, beat.body["eta_ready_at"].(string))
		if err != nil || ready.IsZero() {
			t.Fatalf("beat %d dropped ETA: %#v %v", i, beat.body["eta_ready_at"], err)
		}
	}
}

func TestHeartbeatLegacyStateBackfillsBoundTicket(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "progress.json")
	if err := os.WriteFile(path, []byte(`{"pct":40,"remaining_min":25,"note":"Current step"}`), 0600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(home, "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	lease := strings.Repeat("ab", 32)
	if err := os.WriteFile(filepath.Join(stateDir, "lease.key"), []byte(lease+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "session.id"), []byte(transcriptSessionID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy := `{"schema":"` + heartbeatSchema + `","session_id":"` + transcriptSessionID + `","project_id":"` + transcriptProjectID + `","sequence":4}`
	if strings.Contains(legacy, "bound_ticket") {
		t.Fatal("fixture accidentally included a bound ticket")
	}
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), []byte(legacy+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sessionGets := 0
	var calls []hbCall
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/api/projects/"+transcriptProjectID+"/harness-sessions/"+transcriptSessionID {
			sessionGets++
			body := map[string]any{"id": transcriptSessionID, "activity_sequence": 4}
			if sessionGets == 1 {
				body["ticket_node_id"] = transcriptEntryID
			}
			_ = json.NewEncoder(w).Encode(body)
			return true
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/nodes/"+transcriptEntryID {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": transcriptEntryID, "key": "AEON-465", "kind_id": "ticket-kind", "title": "ETA",
			})
			return true
		}
		return false
	})
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(home)
	o.StateDir = stateDir
	o.StatusFile = path
	deps := heartbeatDeps{alive: func(int) bool { return true }}
	session, created, err := rt.openHeartbeatSession(t.Context(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if created || session.disk.BoundTicket != "AEON-465" {
		session.hold.release()
		t.Fatalf("legacy state was not backfilled: created=%t ticket=%q", created, session.disk.BoundTicket)
	}
	raw, err := session.hold.readFile("state.json", 1<<20)
	session.hold.release()
	if err != nil || !strings.Contains(string(raw), `"bound_ticket":"AEON-465"`) {
		t.Fatalf("bound ticket was not persisted: %s %v", raw, err)
	}
	resumed, created, err := rt.openHeartbeatSession(t.Context(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.hold.release()
	if created || resumed.disk.BoundTicket != "AEON-465" || sessionGets != 1 {
		t.Fatalf("persisted ticket was not reused: created=%t ticket=%q gets=%d", created, resumed.disk.BoundTicket, sessionGets)
	}
	if err := rt.heartbeatBeat(t.Context(), o, deps, &resumed); err != nil {
		t.Fatal(err)
	}
	beats := hbWhere(calls, http.MethodPost, "/heartbeat")
	if len(beats) != 1 || beats[0].body["progress_pct"] != float64(40) {
		t.Fatalf("legacy restart dropped progress: %#v", beats)
	}
	ready, err := time.Parse(time.RFC3339Nano, beats[0].body["eta_ready_at"].(string))
	if err != nil || ready.IsZero() {
		t.Fatalf("legacy restart dropped ETA: %#v %v", beats[0].body["eta_ready_at"], err)
	}
}

func TestHeartbeatStatusFileSentOnEachBeatAndMissingDoesNotFail(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "progress.json")
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(home)
	o.StatusFile = path
	o.Ticket = "AEON-1"
	session := heartbeatSession{id: transcriptSessionID, lease: "synthetic-generation-lease-0000000000"}
	for _, pct := range []int{0, 60, 100} {
		raw, _ := json.Marshal(map[string]any{"pct": pct, "remaining_min": 25, "note": "Current step"})
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.heartbeatBeat(t.Context(), o, heartbeatDeps{}, &session); err != nil {
			t.Fatal(err)
		}
		body := calls[len(calls)-1].body
		if body["progress_pct"] != float64(pct) || body["activity_note"] != "Current step" {
			t.Fatal(body)
		}
		ready, err := time.Parse(time.RFC3339Nano, body["eta_ready_at"].(string))
		if err != nil || !ready.Equal(st.ModTime().Add(25*time.Minute)) {
			t.Fatal("wrong ready ETA", ready, err)
		}
	}
	for _, raw := range []string{"{", "null"} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := rt.heartbeatBeat(context.Background(), o, heartbeatDeps{}, &session); err != nil {
			t.Fatal(err)
		}
	}
	o.StatusFile = filepath.Join(home, "missing.json")
	if err := rt.heartbeatBeat(t.Context(), o, heartbeatDeps{}, &session); err != nil {
		t.Fatal(err)
	}
	if strings.Count(stderr.String(), "status_file_unavailable") != 1 {
		t.Fatal("status warnings not throttled", stderr.String())
	}
	if calls[len(calls)-1].body["progress_pct"] != nil || calls[len(calls)-1].body["eta_ready_at"] != nil {
		t.Fatal("missing status fabricated progress")
	}
}

func TestHeartbeatWarningsRateLimitedAcrossRestart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	warnings := []harness.EstimateWarning{{Code: "missing_eta", Hint: "Report ready ETA"}, {Code: "missing_progress", Hint: "Report percent"}, {Code: "missing_eta", Hint: "Duplicate"}}
	var at map[string]time.Time
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	rt.printEstimateWarnings(warnings, &at, now)
	rt.printEstimateWarnings(warnings, &at, now.Add(9*time.Minute))
	if strings.Count(stderr.String(), "missing_eta") != 1 || strings.Count(stderr.String(), "missing_progress") != 1 {
		t.Fatal(stderr.String())
	}
	raw, _ := json.Marshal(heartbeatDisk{WarningAt: at})
	var resumed heartbeatDisk
	if err := json.Unmarshal(raw, &resumed); err != nil {
		t.Fatal(err)
	}
	rt.printEstimateWarnings(warnings, &resumed.WarningAt, now.Add(10*time.Minute))
	if strings.Count(stderr.String(), "missing_eta") != 2 {
		t.Fatal(stderr.String())
	}
	stderr.Reset()
	home := t.TempDir()
	response := map[string]any{"warnings": warnings}
	rt.printHeartbeatWarningsHome(response, transcriptSessionID, home)
	rt.printHeartbeatWarningsHome(response, transcriptSessionID, home)
	if strings.Count(stderr.String(), "missing_eta") != 1 {
		t.Fatal("one-shot receipts did not survive invocation", stderr.String())
	}
	receipts := filepath.Join(home, ".aeon", "heartbeat-warnings")
	for _, path := range []string{filepath.Join(home, ".aeon"), receipts, filepath.Join(receipts, transcriptSessionID)} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0700 {
			t.Fatalf("receipt directory is not private: %s, %v", path, err)
		}
	}
}

func TestHeartbeatWarningReceiptsOnceWhenAeonIs0755(t *testing.T) {
	home := t.TempDir()
	aeon := filepath.Join(home, ".aeon")
	if err := os.Mkdir(aeon, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(aeon, 0755); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	warnings := []harness.EstimateWarning{{Code: "missing_eta", Hint: "Report ready ETA"}, {Code: "missing_progress", Hint: "Report percent"}}
	response := map[string]any{"warnings": warnings}
	rt.printHeartbeatWarningsHome(response, transcriptSessionID, home)
	rt.printHeartbeatWarningsHome(response, transcriptSessionID, home)
	if strings.Count(stderr.String(), "missing_eta") != 1 || strings.Count(stderr.String(), "missing_progress") != 1 {
		t.Fatal("0755 home reprinted warning codes", stderr.String())
	}
	st, err := os.Stat(aeon)
	if err != nil || st.Mode().Perm() != 0755 {
		t.Fatalf("config directory was relabeled: %v %v", st.Mode().Perm(), err)
	}
	receipts := filepath.Join(aeon, "heartbeat-warnings")
	for _, path := range []string{receipts, filepath.Join(receipts, transcriptSessionID)} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("receipt child is not a private directory: %s %v", path, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(aeon, ".heartbeat-warnings-"+transcriptSessionID)); !os.IsNotExist(err) {
		t.Fatal("receipt was written directly into the 0755 directory", err)
	}
}

func TestHeartbeatWarningReceiptMigratesLegacyTimestamps(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC()
	recent := now.Add(-2 * time.Minute)
	stale := now.Add(-11 * time.Minute)
	writeLegacyWarningReceipt(t, home, map[string]time.Time{
		"missing_eta":      recent,
		"stale_code":       stale,
		"missing_progress": now.Add(time.Minute),
	})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	warnings := []harness.EstimateWarning{
		{Code: "missing_eta", Hint: "Report ready ETA"},
		{Code: "missing_progress", Hint: "Report percent"},
		{Code: "stale_code", Hint: "Old warning"},
	}
	response := map[string]any{"warnings": warnings}
	rt.printHeartbeatWarningsHome(response, transcriptSessionID, home)
	if strings.Count(stderr.String(), "missing_eta") != 0 {
		t.Fatal("upgrade repeated a warning still inside the interval", stderr.String())
	}
	if strings.Count(stderr.String(), "missing_progress") != 1 || strings.Count(stderr.String(), "stale_code") != 1 {
		t.Fatal("stale or future legacy stamps suppressed a warning", stderr.String())
	}
	stderr.Reset()
	rt.printHeartbeatWarningsHome(response, transcriptSessionID, home)
	if stderr.Len() != 0 {
		t.Fatal("migrated receipts did not throttle the next invocation", stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(home, ".aeon", "heartbeat-warnings", transcriptSessionID, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk heartbeatDisk
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if !disk.WarningAt["missing_eta"].Equal(recent) {
		t.Fatalf("migrated stamp %s, want %s", disk.WarningAt["missing_eta"], recent)
	}
	if _, err := os.Lstat(filepath.Join(home, ".aeon", ".heartbeat-warnings-"+transcriptSessionID, "state.json")); err != nil {
		t.Fatal("legacy receipt was removed", err)
	}

	linkedHome := t.TempDir()
	aeon := filepath.Join(linkedHome, ".aeon")
	if err := os.Mkdir(aeon, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeLegacyWarningReceipt(t, outside, map[string]time.Time{"missing_eta": recent})
	if err := os.Symlink(filepath.Join(outside, ".aeon", ".heartbeat-warnings-"+transcriptSessionID), filepath.Join(aeon, ".heartbeat-warnings-"+transcriptSessionID)); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	rt.printHeartbeatWarningsHome(response, transcriptSessionID, linkedHome)
	if strings.Count(stderr.String(), "missing_eta") != 1 {
		t.Fatal("followed a legacy receipt symlink", stderr.String())
	}
}

func writeLegacyWarningReceipt(t *testing.T, home string, at map[string]time.Time) {
	t.Helper()
	aeon := filepath.Join(home, ".aeon")
	if err := os.MkdirAll(aeon, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(aeon, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(aeon, ".heartbeat-warnings-"+transcriptSessionID)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(heartbeatDisk{WarningAt: at})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatStatusFileStallsBecomeOverdueAndWarn(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "progress.json")
	if err := os.WriteFile(path, []byte(`{"pct":40,"remaining_min":25}`), 0600); err != nil {
		t.Fatal(err)
	}
	modified := time.Now().UTC().Add(-31 * time.Minute)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(home)
	o.StatusFile, o.Ticket = path, "AEON-1"
	session := heartbeatSession{id: transcriptSessionID, lease: "synthetic-generation-lease-0000000000"}
	for i := 0; i < 2; i++ {
		if err := rt.heartbeatBeat(t.Context(), o, heartbeatDeps{}, &session); err != nil {
			t.Fatal(err)
		}
		ready, err := time.Parse(time.RFC3339Nano, calls[len(calls)-1].body["eta_ready_at"].(string))
		if err != nil || !ready.Equal(st.ModTime().Add(25*time.Minute)) || !ready.Before(time.Now()) {
			t.Fatal("stuck worker's ETA moved forward", ready, err)
		}
	}
	if strings.Count(stderr.String(), "stale_progress") != 1 {
		t.Fatal("stale warning was missing or repeated", stderr.String())
	}
	for _, modified := range []time.Time{time.Now().Add(-32 * 24 * time.Hour), time.Now().Add(366 * 24 * time.Hour)} {
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
		if err := rt.heartbeatBeat(t.Context(), o, heartbeatDeps{}, &session); err != nil {
			t.Fatal(err)
		}
		if _, ok := calls[len(calls)-1].body["eta_ready_at"]; ok {
			t.Fatal("sent an ETA outside the server window")
		}
	}
}

func TestHeartbeatInvalidStatusValuesNeverFailBeat(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "progress.json")
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(home)
	o.StatusFile, o.Ticket = path, "AEON-1"
	session := heartbeatSession{id: transcriptSessionID, lease: "synthetic-generation-lease-0000000000"}
	for _, value := range []string{"-1", "NaN", "1e999", "525600", `"25"`, `"NaN"`} {
		if err := os.WriteFile(path, []byte(`{"remaining_min":`+value+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := rt.heartbeatBeat(t.Context(), o, heartbeatDeps{}, &session); err != nil {
			t.Fatal(err)
		}
		if _, ok := calls[len(calls)-1].body["eta_ready_at"]; ok {
			t.Fatal("forwarded invalid remaining minutes", value)
		}
	}
	if strings.Count(stderr.String(), "status_file_unavailable") != 1 {
		t.Fatal("invalid values did not share a throttled warning", stderr.String())
	}
}

func TestHeartbeatCoordinatorOnlyReadsExplicitStatusFile(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(home)
	o.Role, o.Worktree = "coordinator", home
	session := heartbeatSession{id: transcriptSessionID, lease: "synthetic-generation-lease-0000000000"}
	if err := rt.heartbeatBeat(t.Context(), o, heartbeatDeps{}, &session); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "status_file_unavailable") {
		t.Fatal("coordinator warned about a default status file")
	}
	path := filepath.Join(home, ".agent-status.json")
	if err := os.WriteFile(path, []byte(`{"note":"Worker status","pct":40,"remaining_min":25}`), 0600); err != nil {
		t.Fatal(err)
	}
	if status, ok := readAgentStatus(o); !ok || status.Note != "" || status.Progress != nil {
		t.Fatal("coordinator read the worker's default status", status)
	}
	o.StatusFile = path
	if err := rt.heartbeatBeat(t.Context(), o, heartbeatDeps{}, &session); err != nil {
		t.Fatal(err)
	}
	body := calls[len(calls)-1].body
	if body["activity_note"] != "Worker status" || body["progress_pct"] != nil || body["eta_ready_at"] != nil {
		t.Fatal("explicit coordinator status was not respected", body)
	}
	o.StatusFile = filepath.Join(home, "missing.json")
	if err := rt.heartbeatBeat(t.Context(), o, heartbeatDeps{}, &session); err != nil {
		t.Fatal(err)
	}
	if strings.Count(stderr.String(), "status_file_unavailable") != 1 {
		t.Fatal("explicit coordinator status did not warn", stderr.String())
	}
}

func TestHeartbeatRejectedStatusEstimatesRetryWithoutBlockingLiveness(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "progress.json")
	if err := os.WriteFile(path, []byte(`{"pct":0,"remaining_min":25,"note":"Current step"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var calls []hbCall
	base := heartbeatFixture(t, &calls, "", "")
	defer base.Close()
	var beats []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/heartbeat") {
			base.Config.Handler.ServeHTTP(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		beats = append(beats, body)
		w.Header().Set("Content-Type", "application/json")
		if body["progress_pct"] != nil || body["eta_ready_at"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"ETA needs a bound ticket"}`))
			return
		}
		_, _ = w.Write([]byte(`{"warnings":[]}`))
	}))
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(home)
	o.StatusFile, o.Ticket = path, "AEON-1"
	session := heartbeatSession{id: transcriptSessionID, lease: "synthetic-generation-lease-0000000000"}
	for i := 0; i < 2; i++ {
		if err := rt.heartbeatBeat(t.Context(), o, heartbeatDeps{}, &session); err != nil {
			t.Fatal(err)
		}
	}
	if len(beats) != 4 || session.disk.Sequence != 2 {
		t.Fatalf("wrong retry count or sequence: %d, %d", len(beats), session.disk.Sequence)
	}
	for i := 0; i < len(beats); i += 2 {
		if beats[i]["activity_sequence"] != beats[i+1]["activity_sequence"] || beats[i+1]["activity_note"] != "Current step" {
			t.Fatal("retry lost the sequence or status note", beats)
		}
	}
	if strings.Count(stderr.String(), "status_file_unavailable") != 1 {
		t.Fatal("rejected status warning was not throttled", stderr.String())
	}
}

func TestHeartbeatResponseWarningsDoNotBlockTheLoop(t *testing.T) {
	var calls []hbCall
	base := heartbeatFixture(t, &calls, "", "")
	defer base.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/heartbeat") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"warnings": []harness.EstimateWarning{{Code: "missing_eta", Hint: "Report ready ETA"}}})
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(t.TempDir())
	session := heartbeatSession{id: transcriptSessionID, lease: "synthetic-generation-lease-0000000000"}
	for i := 0; i < 2; i++ {
		if err := rt.heartbeatBeat(t.Context(), o, heartbeatDeps{}, &session); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(stderr.String(), "missing_eta") != 1 {
		t.Fatal("response warnings were lost or repeated", stderr.String())
	}
}

func TestHeartbeatWarningReceiptsStayUnderPrivateHome(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Chdir(cwd)
	hold, err := openHeartbeatWarningHold(home, transcriptSessionID)
	if err != nil {
		t.Fatal(err)
	}
	hold.release()
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 0 {
		t.Fatal("warning receipt touched the current directory", err, entries)
	}
	if _, err := openHeartbeatWarningHold(".", transcriptSessionID); err == nil {
		t.Fatal("accepted a relative receipt home")
	}
	if _, err := openHeartbeatWarningHold(home, "../../outside"); err == nil {
		t.Fatal("accepted an invalid receipt session")
	}
	for _, mode := range []os.FileMode{0775, 0777} {
		unsafeHome := t.TempDir()
		unsafeDir := filepath.Join(unsafeHome, ".aeon")
		if err := os.Mkdir(unsafeDir, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(unsafeDir, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := openHeartbeatWarningHold(unsafeHome, transcriptSessionID); err == nil {
			t.Fatalf("accepted receipt root mode %o", mode)
		}
	}
	linkHome := t.TempDir()
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, "real-aeon"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "real-aeon"), filepath.Join(linkHome, ".aeon")); err != nil {
		t.Fatal(err)
	}
	if _, err := openHeartbeatWarningHold(linkHome, transcriptSessionID); err == nil {
		t.Fatal("followed a symlinked receipt root")
	}
}

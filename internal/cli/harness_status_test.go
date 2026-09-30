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
)

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
	for _, path := range []string{filepath.Join(home, ".aeon"), filepath.Join(home, ".aeon", ".heartbeat-warnings-"+transcriptSessionID)} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0700 {
			t.Fatalf("receipt directory is not private: %s, %v", path, err)
		}
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
	unsafeHome := t.TempDir()
	unsafeDir := filepath.Join(unsafeHome, ".aeon")
	if err := os.Mkdir(unsafeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := openHeartbeatWarningHold(unsafeHome, transcriptSessionID); err == nil {
		t.Fatal("accepted a non-private receipt root")
	}
}

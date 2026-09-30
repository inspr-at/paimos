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
		{`{"remaining_min":1e999}`, false, nil, nil},
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
		before := time.Now()
		if err := rt.heartbeatBeat(t.Context(), o, heartbeatDeps{}, &session); err != nil {
			t.Fatal(err)
		}
		body := calls[len(calls)-1].body
		if body["progress_pct"] != float64(pct) || body["activity_note"] != "Current step" {
			t.Fatal(body)
		}
		ready, err := time.Parse(time.RFC3339Nano, body["eta_ready_at"].(string))
		if err != nil || ready.Before(before.Add(25*time.Minute)) || ready.After(time.Now().Add(25*time.Minute)) {
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
	lease := filepath.Join(t.TempDir(), "worker-lease")
	response := map[string]any{"warnings": warnings}
	rt.printHeartbeatWarnings(response, transcriptSessionID, lease)
	rt.printHeartbeatWarnings(response, transcriptSessionID, lease)
	if strings.Count(stderr.String(), "missing_eta") != 1 {
		t.Fatal("one-shot receipts did not survive invocation", stderr.String())
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

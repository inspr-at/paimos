// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHarnessHeartbeatThrottledActivity(t *testing.T) {
	isolate(t)
	var calls []transcriptRequest
	srv := transcriptFixture(t, "memory", "note", &calls)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	dir := t.TempDir()
	lease := filepath.Join(dir, "lease")
	if err := os.WriteFile(lease, []byte("sc1-test-lease-00000000000000000001"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI([]string{"paimos", "--config", filepath.Join(dir, "missing"), "--json", "harness", "heartbeat", "--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", lease, "--phase", "working", "--activity", "throttled", "--activity-sequence", "2", "--label", "  Renamed session  "}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("heartbeat exit %d: %s", code, stderr)
	}
	if len(calls) == 0 || calls[len(calls)-1].body["activity"] != "throttled" {
		t.Fatal("CLI did not forward throttled activity")
	}
	if calls[len(calls)-1].body["display_label"] != "  Renamed session  " {
		t.Fatal("CLI did not forward session label")
	}
	base := []string{"paimos", "--config", filepath.Join(dir, "missing"), "--json", "harness", "heartbeat", "--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", lease, "--phase", "working", "--activity", "throttled", "--activity-sequence", "3"}
	code, _, stderr = runCLI(append(append([]string{}, base...), "--label", ""), "")
	if code != 0 || stderr != "" {
		t.Fatalf("clear label exit %d: %s", code, stderr)
	}
	if value, ok := calls[len(calls)-1].body["display_label"]; !ok || value != "" {
		t.Fatalf("CLI did not forward empty label: %v", calls[len(calls)-1].body)
	}
	code, _, stderr = runCLI(base, "")
	if code != 0 || stderr != "" {
		t.Fatalf("omitted label exit %d: %s", code, stderr)
	}
	if _, ok := calls[len(calls)-1].body["display_label"]; ok {
		t.Fatal("CLI sent an omitted label")
	}
	if _, ok := calls[len(calls)-1].body["eta_ready_at"]; ok {
		t.Fatal("CLI sent an omitted ETA")
	}
}

func TestHarnessHeartbeatEtaFlags(t *testing.T) {
	isolate(t)
	var calls []transcriptRequest
	srv := transcriptFixture(t, "memory", "note", &calls)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	dir := t.TempDir()
	lease := filepath.Join(dir, "lease")
	if err := os.WriteFile(lease, []byte("sc1-test-lease-00000000000000000001"), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"paimos", "--config", filepath.Join(dir, "missing"), "--json", "harness", "heartbeat", "--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", lease, "--phase", "working", "--activity-sequence", "4"}
	before := time.Now().Add(25 * time.Minute)
	code, _, stderr := runCLI(append(append([]string{}, base...), "--eta-ready", "+25m", "--progress", "40"), "")
	if code != 0 || stderr != "" {
		t.Fatalf("eta heartbeat exit %d: %s", code, stderr)
	}
	body := calls[len(calls)-1].body
	if body["progress_pct"] != float64(40) {
		t.Fatalf("progress %#v", body["progress_pct"])
	}
	ready, _ := time.Parse(time.RFC3339, body["eta_ready_at"].(string))
	if ready.Sub(before) < -time.Minute || ready.Sub(before) > 2*time.Minute {
		t.Fatalf("eta %s not about 25 minutes ahead of %s", ready, before)
	}
	if _, ok := body["eta_live_at"]; ok {
		t.Fatal("live ETA was not requested")
	}
	code, _, stderr = runCLI(append(append([]string{}, base...), "--progress", "0"), "")
	if code != 0 || stderr != "" {
		t.Fatalf("zero progress exit %d: %s", code, stderr)
	}
	if calls[len(calls)-1].body["progress_pct"] != float64(0) {
		t.Fatalf("progress 0 became %#v", calls[len(calls)-1].body["progress_pct"])
	}
	code, _, _ = runCLI(append(append([]string{}, base...), "--progress", "101"), "")
	if code == 0 {
		t.Fatal("progress 101 was accepted")
	}
}

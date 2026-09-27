// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os"
	"path/filepath"
	"testing"
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
	code, _, stderr := runCLI([]string{"paimos", "--config", filepath.Join(dir, "missing"), "--json", "harness", "heartbeat", "--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", lease, "--phase", "working", "--activity", "throttled", "--activity-sequence", "2"}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("heartbeat exit %d: %s", code, stderr)
	}
	if len(calls) == 0 || calls[len(calls)-1].body["activity"] != "throttled" {
		t.Fatal("CLI did not forward throttled activity")
	}
}

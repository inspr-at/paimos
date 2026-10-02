// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/harness"
)

func TestHarnessPauseCLIRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		path string
	}{
		{"single", []string{"pause", "--session", transcriptSessionID, "--reason", "Reboot"}, harnessPath(transcriptProjectID, transcriptSessionID) + "/pause"},
		{"all", []string{"pause", "--all", "--except", transcriptSessionID, "--deadline-minutes", "5"}, "/api/projects/" + transcriptProjectID + "/harness-sessions/pause"},
		{"resume", []string{"resume", "--session", transcriptSessionID}, harnessPath(transcriptProjectID, transcriptSessionID) + "/resume"},
		{"resume all", []string{"resume", "--all"}, "/api/projects/" + transcriptProjectID + "/harness-sessions/resume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []hbCall
			srv := heartbeatFixture(t, &calls, "", "")
			defer srv.Close()
			rt, _, _ := heartbeatRuntime(t, srv)
			args := append([]string{"aeon", "harness"}, tc.args...)
			args = append(args, "--project", "AEON")
			if err := rt.execute(args); err != nil {
				t.Fatal(err)
			}
			last := calls[len(calls)-1]
			if last.method != "POST" || last.path != tc.path {
				t.Fatal(last)
			}
			if tc.name == "all" && (last.body["deadline_minutes"] != float64(5) || len(last.body["except"].([]any)) != 1) {
				t.Fatal(last.body)
			}
		})
	}
}

func TestHarnessPauseCLIRejectsAmbiguousTargets(t *testing.T) {
	for _, args := range [][]string{
		{"pause"}, {"pause", "--all", "--session", transcriptSessionID}, {"pause", "--session", "invalid"},
		{"resume", "--session", transcriptSessionID, "--except", transcriptEntryID},
		{"pause", "--all", "--deadline-minutes", "61"},
		{"resume", "--all", "--registration-file", "private.json"},
		{"pause", "--all", "--coordinator-session", transcriptSessionID},
	} {
		var calls []hbCall
		srv := heartbeatFixture(t, &calls, "", "")
		rt, _, _ := heartbeatRuntime(t, srv)
		if err := rt.execute(append([]string{"aeon", "harness"}, args...)); err == nil {
			t.Fatalf("accepted %v", args)
		}
		if len(calls) != 0 {
			t.Fatal("invalid target reached API")
		}
		srv.Close()
	}
}

func TestHarnessPauseCLIWorkerPlanAndHandover(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	dir := t.TempDir()
	leasePath := filepath.Join(dir, "lease")
	if err := os.WriteFile(leasePath, []byte(strings.Repeat("l", 40)), 0600); err != nil {
		t.Fatal(err)
	}
	common := []string{"--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", leasePath}
	args := append([]string{"aeon", "harness", "pause-plan", "--control-id", transcriptEntryID, "--handover-point", "After the commit"}, common...)
	if err := rt.execute(args); err != nil {
		t.Fatal(err)
	}
	if calls[len(calls)-1].body["control_id"] != transcriptEntryID {
		t.Fatal(calls)
	}
	notePath := filepath.Join(dir, "handover.json")
	note := `{"state":"Saved","next_steps":["Run checks"],"open_questions":[],"worktree_state":"clean"}`
	if err := os.WriteFile(notePath, []byte(note), 0600); err != nil {
		t.Fatal(err)
	}
	args = append([]string{"aeon", "harness", "mark-stopped", "--reason", "paused", "--handover-file", notePath}, common...)
	if err := rt.execute(args); err != nil {
		t.Fatal(err)
	}
	last := calls[len(calls)-1]
	if last.path != harnessPath(transcriptProjectID, transcriptSessionID)+"/stop" || last.body["handover"].(map[string]any)["state"] != "Saved" {
		t.Fatal(last)
	}
}

func TestPrintHeartbeatPauseAndBoundedHandover(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, out, stderr := heartbeatRuntime(t, srv)
	pause := &harness.Pause{ControlID: transcriptEntryID, State: "requested"}
	rt.printHeartbeatPause(transcriptSessionID, pause, true)
	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["type"] != "pause_requested" || record["session_id"] != transcriptSessionID {
		t.Fatal(record)
	}
	rt.printHeartbeatPause(transcriptSessionID, pause, false)
	if !strings.Contains(stderr.String(), "commit WIP") {
		t.Fatal(stderr.String())
	}
	for _, raw := range []string{strings.Repeat("x", 16001), `{} {}`, `{"unknown":"value"}`} {
		rt.stdin = strings.NewReader(raw)
		if _, err := rt.readHandover("-"); err == nil {
			t.Fatalf("accepted unsafe note %.30s", raw)
		}
	}
}

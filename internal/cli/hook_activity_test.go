// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/internal/agentactivity"
)

func TestHookActivityStaysInItsLivePrivateGeneration(t *testing.T) {
	setupHookTest(t)
	t.Setenv("HOME", t.TempDir())
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "activity-state")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := openHeartbeatHold(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.release()
	s := heartbeatSession{id: hookSessionID, hold: hold}
	if err = saveHeartbeatSession(&s); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_SESSION_STATE_DIR", dir)
	policy := func(mode string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"session_id": hookSessionID, "mode": mode})
		if err := hold.writeFile("activity-mode.json", raw); err != nil {
			t.Fatal(err)
		}
	}
	writeHookActivity("", hookSessionID, "Running Go tests")
	if _, err = hold.readFile("activity.json", 512); !os.IsNotExist(err) {
		t.Fatal("unknown policy collected activity")
	}
	policy(agentactivity.Summary)
	writeHookActivity("", hookSessionID, "Running Go tests")
	raw, err := hold.readFile("activity.json", 512)
	if err != nil {
		t.Fatal(err)
	}
	var a hookActivity
	if json.Unmarshal(raw, &a) != nil || a.Session != hookSessionID || a.Activity.Text != "Running Go tests" {
		t.Fatal("hook projection missing")
	}
	policy(agentactivity.Off)
	writeHookActivity("", hookSessionID, "Pushing")
	writeHookActivity("", hookMessageID, "Committing")
	writeHookActivity("", hookSessionID, "PRIVATE_ARGUMENT")
	after, _ := hold.readFile("activity.json", 512)
	if string(raw) != string(after) {
		t.Fatal("off, foreign generation or arbitrary text changed cache")
	}
	policy(agentactivity.Summary)
	s.disk.Terminal = true
	if err = saveHeartbeatSession(&s); err != nil {
		t.Fatal(err)
	}
	writeHookActivity("", hookSessionID, "Committing")
	after, _ = hold.readFile("activity.json", 512)
	if string(raw) != string(after) {
		t.Fatal("closed generation collected activity")
	}
}

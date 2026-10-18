// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"testing"
)

func TestPauseLevelsAndLeavingCLI(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		method, path string
	}{
		{[]string{"pause", "--project", "AEON", "--session", transcriptSessionID, "--level", "wrap_up", "--note", "Save WIP"}, "POST", harnessPath(transcriptProjectID, transcriptSessionID) + "/pause"},
		{[]string{"pause-default"}, "GET", "/api/me/agent-pause-settings"},
		{[]string{"pause-default", "--level", "pause_quickly"}, "PUT", "/api/me/agent-pause-settings"},
		{[]string{"leaving-at", "--at", "2026-10-02T10:00:00Z", "--note", "Tomorrow"}, "PUT", "/api/me/leaving-at"},
		{[]string{"leaving-at", "--off"}, "DELETE", "/api/me/leaving-at"},
		{[]string{"leaving-at"}, "GET", "/api/me/leaving-at"},
	} {
		t.Run(tc.path+tc.method, func(t *testing.T) {
			var calls []hbCall
			srv := heartbeatFixture(t, &calls, "", "")
			defer srv.Close()
			rt, _, _ := heartbeatRuntime(t, srv)
			if err := rt.execute(append([]string{"aeon", "harness"}, tc.args...)); err != nil {
				t.Fatal(err)
			}
			last := calls[len(calls)-1]
			if last.method != tc.method || last.path != tc.path {
				t.Fatal(last)
			}
			if tc.args[0] == "pause" && (last.body["level"] != "wrap_up" || last.body["note"] != "Save WIP") {
				t.Fatal(last.body)
			}
		})
	}
	for _, args := range [][]string{{"pause-default", "--level", "bad"}, {"leaving-at", "--at", "not a timestamp"}, {"leaving-at", "--off", "--at", "2026-10-02T10:00:00Z"}, {"pause", "--all", "--level", "bad"}} {
		var calls []hbCall
		srv := heartbeatFixture(t, &calls, "", "")
		rt, _, _ := heartbeatRuntime(t, srv)
		if err := rt.execute(append([]string{"aeon", "harness"}, args...)); err == nil || len(calls) != 0 {
			t.Fatalf("accepted %v", args)
		}
		srv.Close()
	}
}

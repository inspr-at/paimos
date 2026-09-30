// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import "testing"

func TestCapacityHandoffKeepsWorkspaceBranchAndStoppedProcess(t *testing.T) {
	r := Record{RunID: "old", WorkOrderID: "order", TenantID: "tenant", PrincipalID: "agent", Workspace: "/workspace", LaunchBranch: "work/ticket", State: "failed", ExitObserved: true}
	previous := &owned{record: r}
	s := &Supervisor{tenantID: "tenant", principalID: "agent", workspace: "/workspace", runs: map[string]*owned{"old": previous}}
	run := Run{RetryOfRunID: "old", WorkOrderID: "order"}
	if err := s.validateCapacityHandoff(run, "work/ticket"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Record){func(r *Record) { r.LaunchBranch = "work/other" }, func(r *Record) { r.Workspace = "/other" }, func(r *Record) { r.State = "running" }, func(r *Record) { r.WorkOrderID = "other" }, func(r *Record) { r.LaunchBranch = "" }, func(r *Record) { r.ExitObserved = false }} {
		changed := r
		mutate(&changed)
		previous.record = changed
		if err := s.validateCapacityHandoff(run, "work/ticket"); err == nil {
			t.Fatal("handoff safety fence bypassed")
		}
	}
}

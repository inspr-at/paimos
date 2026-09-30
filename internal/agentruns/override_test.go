// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"github.com/inspr-at/paimos/internal/agentruns"
	"testing"
)

func TestRunNowIsPersonOnlyAuditedAndRunScoped(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	body := map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "capacity_override": "now"}
	f.call(t, f.agent, "POST", "/api/work-orders/"+o.NodeID+"/runs", body, 403, nil)
	var run agentruns.Run
	f.call(t, f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", body, 201, &run)
	if run.CapacityOverride != "now" {
		t.Fatal("override missing")
	}
	ordinary := f.run(t, o)
	if ordinary.CapacityOverride != "" {
		t.Fatal("override leaked into next run")
	}
	path := "/api/runs/" + ordinary.ID + "/capacity-override"
	f.call(t, f.agent, "POST", path, map[string]string{"capacity_override": "now"}, 403, nil)
	f.call(t, f.foreign, "POST", path, map[string]string{"capacity_override": "now"}, 404, nil)
	f.call(t, f.person, "POST", path, map[string]string{"capacity_override": "sprint"}, 400, nil)
	f.call(t, f.person, "POST", path, map[string]string{"capacity_override": "now"}, 200, &ordinary)
	f.call(t, f.person, "POST", path, map[string]string{"capacity_override": "now"}, 200, nil)
	if f.count(t, f.person, `SELECT count(*) FROM events WHERE type='run.capacity_override'`) != 1 {
		t.Fatal("override event missing or replay duplicated it")
	}
	claimed := f.claim(t, ordinary)
	f.call(t, f.person, "POST", "/api/runs/"+claimed.ID+"/capacity-override", map[string]string{"capacity_override": "now"}, 409, nil)
}

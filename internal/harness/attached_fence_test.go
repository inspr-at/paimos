// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import "testing"

func TestUnmanagedCannotDrainOrCompleteManagedDelivery(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "attached-fence-unmanaged-lease-00000001"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "attached-fence-ref", "worker_lease": lease, "advertised_capabilities": []string{"inbox"}}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	expect(t, f.call(f.agent, "POST", base+"/"+id+"/drain", map[string]any{}, lease), 409)
	expect(t, f.call(f.agent, "POST", base+"/"+id+"/complete-delivery", map[string]any{"delivery_id": uid(), "cursor": 1}, lease), 409)
}

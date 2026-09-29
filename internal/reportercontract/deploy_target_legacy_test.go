// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/deploytarget"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/inspr-at/paimos/internal/stagehandoff"
)

// Frozen by encoding the types at stable100 (d14c3c5), before AEON-211.
// Compare full bytes, including order, nulls, omissions and the HTTP newline.
// A target must not change PHAROS's strictly decoded stage-handoffs/1.0 body.
func TestDeployTargetBytesMatchStable100(t *testing.T) {
	const deploy = `{"id":"","project_node_id":"","release_node_id":"","stage":"deploy","operation":"deploy","plugin_id":"pharos","attempt":0,"authority_epoch":0,"superseded_by":null,"authority_open":false,"journey_revision":0,"state":"","expires_at":"0001-01-01T00:00:00Z","evidence_ceiling":["deployment"],"plan_digest":"","predecessor_digest":"","context_digest":"","prerequisite_seal_sha256":""}`
	const verify = `{"id":"","project_node_id":"","release_node_id":"","stage":"deploy","operation":"verify","plugin_id":"pharos","attempt":0,"authority_epoch":0,"superseded_by":null,"authority_open":false,"journey_revision":0,"state":"","expires_at":"0001-01-01T00:00:00Z","evidence_ceiling":["verification"],"plan_digest":"","predecessor_digest":"","context_digest":"","prerequisite_seal_sha256":""}`
	const prepare = `{"id":"","project_node_id":"","release_node_id":"","stage":"access","operation":"prepare","plugin_id":"janus","attempt":0,"authority_epoch":0,"superseded_by":null,"authority_open":false,"journey_revision":0,"state":"","expires_at":"0001-01-01T00:00:00Z","evidence_ceiling":["authorization","credential_handoff"],"plan_digest":"","predecessor_digest":"","context_digest":"","prerequisite_seal_sha256":""}`
	const apply = `{"id":"","project_node_id":"","release_node_id":"","stage":"access","operation":"apply","plugin_id":"janus","attempt":0,"authority_epoch":0,"superseded_by":null,"authority_open":false,"journey_revision":0,"state":"","expires_at":"0001-01-01T00:00:00Z","evidence_ceiling":["authorization"],"plan_digest":"","predecessor_digest":"","context_digest":"","prerequisite_seal_sha256":""}`
	const approval = `{"id":"","agent_principal_id":"","risk":"high","scope":"journey.deploy","resource_kind":"node","resource_id":null,"run_id":null,"rationale":"Deploy this release","expires_at":"0001-01-01T00:00:00Z","proposed_at":"0001-01-01T00:00:00Z","decision":null,"decided_by_principal_id":null}`
	const stage = `{"key":"deploy","state":"current","gate_scope":"journey.deploy","gate_approval_id":null,"gate_live":false,"handoff_id":null,"handoff_attempt":null,"handoff_authority_epoch":null}`

	target := &deploytarget.Target{Hosts: []string{"edge-1"}, Service: "aeon", Change: "Update image"}
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"targetless deploy", stagehandoff.Handoff{Stage: "deploy", Operation: "deploy", PluginID: "pharos", EvidenceCeiling: []string{"deployment"}}, deploy},
		{"targeted deploy", stagehandoff.Handoff{Stage: "deploy", Operation: "deploy", PluginID: "pharos", EvidenceCeiling: []string{"deployment"}, Target: target, TargetDigestSHA256: digest}, deploy},
		{"targetless verify", stagehandoff.Handoff{Stage: "deploy", Operation: "verify", PluginID: "pharos", EvidenceCeiling: []string{"verification"}}, verify},
		{"verify with inherited target", stagehandoff.Handoff{Stage: "deploy", Operation: "verify", PluginID: "pharos", EvidenceCeiling: []string{"verification"}, Target: target, TargetDigestSHA256: digest}, verify},
		{"Janus prepare", stagehandoff.Handoff{Stage: "access", Operation: "prepare", PluginID: "janus", EvidenceCeiling: []string{"authorization", "credential_handoff"}}, prepare},
		{"Janus apply", stagehandoff.Handoff{Stage: "access", Operation: "apply", PluginID: "janus", EvidenceCeiling: []string{"authorization"}}, apply},
		{"targetless approval", approvals.Approval{Scope: "journey.deploy", ResourceKind: "node", Risk: "high", Rationale: "Deploy this release"}, approval},
		{"targetless journey stage", journey.JourneyStage{Key: "deploy", State: "current", GateScope: "journey.deploy"}, stage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := json.NewEncoder(&out).Encode(tc.value); err != nil {
				t.Fatal(err)
			}
			if out.String() != tc.want+"\n" {
				t.Fatalf("stable100 bytes changed:\n%s", out.String())
			}
		})
	}
	if reportercontract.StageHandoffs != "stage-handoffs/1.0" {
		t.Fatalf("unchanged body must retain 1.0: %s", reportercontract.StageHandoffs)
	}
}

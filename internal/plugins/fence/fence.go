// SPDX-License-Identifier: AGPL-3.0-only

// Package fence is the shared vocabulary for first-party plugins.
// Pharos and Janus both import it; it imports neither of them.
package fence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Permission names the registry accepts. A manifest ceiling and a tenant
// grant are subsets of this list; any other name is rejected at startup.
const (
	PermNodesContribute    = "nodes.contribute"
	PermViewsProvide       = "views.provide"
	PermStepsEvaluate      = "steps.evaluate"
	PermStepsRequest       = "steps.request"
	PermStepsApply         = "steps.apply"
	PermToolsInvoke        = "tools.invoke"
	PermIntegrationsCall   = "integrations.call"
	PermJobsRun            = "jobs.run"
	PermStageDeploy        = "stage.deploy"
	PermStageVerify        = "stage.verify"
	PermStageAccessPrepare = "stage.access_prepare"
	PermStageAccessApply   = "stage.access_apply"
	PermIntakeRead         = "intake.read"
	PermIntakeWrite        = "intake.write"
)

// Gates a workflow step may declare.
const (
	GateArtifactIdentity    = "artifact_identity"
	GateBackupReady         = "backup_ready"
	GateReadiness           = "readiness"
	GateLaunchAdmission     = "launch_admission"
	GatePersonDecision      = "person_decision"
	GatePrerequisiteSeal    = "prerequisite_seal"
	GateDeploymentSucceeded = "deployment_succeeded"
	GateBoundedPermit       = "bounded_permit"
	GateObservedState       = "observed_state"
)

// Blocker codes recorded on a refused stage result. They match the R3 contract.
const (
	BlockerDependencyPending = "dependency_pending"
	BlockerDependencyFailed  = "dependency_failed"
	BlockerReporterStale     = "reporter_stale"
	BlockerExternalWaiting   = "external_waiting"
	BlockerPolicyRefused     = "policy_refused"
)

// Evidence kinds a stage plugin may report.
const (
	KindDeployment        = "deployment"
	KindVerification      = "verification"
	KindAuthorization     = "authorization"
	KindCredentialHandoff = "credential_handoff"
)

var permissions = map[string]struct{}{
	PermNodesContribute: {}, PermViewsProvide: {}, PermStepsEvaluate: {},
	PermStepsRequest: {}, PermStepsApply: {}, PermToolsInvoke: {},
	PermIntegrationsCall: {}, PermJobsRun: {}, PermStageDeploy: {},
	PermStageVerify: {}, PermStageAccessPrepare: {}, PermStageAccessApply: {},
	PermIntakeRead: {}, PermIntakeWrite: {},
}

var gates = map[string]struct{}{
	GateArtifactIdentity: {}, GateBackupReady: {}, GateReadiness: {},
	GateLaunchAdmission: {}, GatePersonDecision: {}, GatePrerequisiteSeal: {},
	GateDeploymentSucceeded: {}, GateBoundedPermit: {}, GateObservedState: {},
}

var evidenceKinds = map[string]struct{}{
	KindDeployment: {}, KindVerification: {}, KindAuthorization: {}, KindCredentialHandoff: {},
}

// KnownPermission reports whether name is a platform permission.
func KnownPermission(name string) bool { _, ok := permissions[name]; return ok }

// KnownGate reports whether name is a workflow gate.
func KnownGate(name string) bool { _, ok := gates[name]; return ok }

// KnownEvidence reports whether kind is a stage evidence kind.
func KnownEvidence(kind string) bool { _, ok := evidenceKinds[kind]; return ok }

// Check is one required or optional prerequisite fact. Seal hashes these
// fields only; evidence text never enters the preimage.
type Check struct {
	Kind      string `json:"kind"`
	Required  bool   `json:"required"`
	Succeeded bool   `json:"succeeded"`
}

// Seal hashes a prerequisite set. An empty set or a set with no required
// check cannot prove a prerequisite.
func Seal(checks []Check) (string, error) {
	if len(checks) == 0 {
		return "", fmt.Errorf("empty dependency set cannot prove a prerequisite")
	}
	seen := make(map[string]struct{}, len(checks))
	required := 0
	cloned := make([]Check, len(checks))
	for i, c := range checks {
		if !KnownEvidence(c.Kind) {
			return "", fmt.Errorf("unknown evidence kind %q", c.Kind)
		}
		if _, ok := seen[c.Kind]; ok {
			return "", fmt.Errorf("duplicate evidence kind %q", c.Kind)
		}
		seen[c.Kind] = struct{}{}
		if c.Required {
			required++
		}
		cloned[i] = c
	}
	if required == 0 {
		return "", fmt.Errorf("optional-only dependency set cannot prove a prerequisite")
	}
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].Kind < cloned[j].Kind })
	body, err := json.Marshal(cloned)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimehealth

import (
	"context"
	"github.com/inspr-at/paimos/backend/agentd"
	"time"
)

// ConsumerExtensions uses content-free evidence from the exact daemon already
// verified by Doctor against the declarative service. Missing/old evidence is
// unknown; a live reporter or target inventory cannot substitute for it.
func ConsumerExtensions(_ context.Context, status agentd.RuntimeStatus) []Layer {
	result := layer("consumers", Unknown, "consumer_supervision_unavailable", "install matching daemon and CLI consumer support")
	seen := map[string]bool{}
	for _, e := range status.Consumers {
		if e.Generation != status.DaemonID {
			return []Layer{layer("consumers", ActionRequired, "consumer_generation_mismatch", "inspect runtime ownership before retrying")}
		}
		seen[e.Kind] = true
		if e.Reason == "legacy_handoff_required" {
			return []Layer{layer("consumers", ActionRequired, "legacy_handoff_required", "stop only the operator-owned listener terminal/service, drain its active lease and reconcile uncertain effects; preview runtime handoff before configuring this generation")}
		}
		if e.State == "circuit_open" {
			return []Layer{layer("consumers", ActionRequired, "consumer_circuit_open", "inspect the private consumer circuit and reconcile any unknown effect before handoff")}
		}
		if e.State == "backoff" {
			result = layer("consumers", ActionRequired, "consumer_recovering", "wait for bounded recovery; inspect worker lease and target binding if retries exhaust")
		}
		if e.State == "unavailable" && e.Reason == "authority_unavailable" {
			result = layer("consumers", ActionRequired, "consumer_authority_unavailable", "integrate generation-fenced fallback and attention server/client support; legacy listeners cannot be safely handed off yet")
		}
		if e.State == "unavailable" && e.Reason == "no_owned_registered_generation" && result.State != ActionRequired {
			result = layer("consumers", Unknown, "no_owned_registered_generation", "start an authorized managed generation")
		}
		if e.State != "ready" || e.LastSuccess.IsZero() || time.Since(e.LastSuccess) > 45*time.Second || e.LastSuccess.After(time.Now().Add(5*time.Second)) {
			continue
		}
	}
	if result.State != ActionRequired && seen["primary"] && seen["fallback"] && seen["attention"] {
		ready := true
		for _, e := range status.Consumers {
			ready = ready && e.State == "ready" && !e.LastSuccess.IsZero() && time.Since(e.LastSuccess) < 45*time.Second && !e.LastSuccess.After(time.Now().Add(5*time.Second))
		}
		if ready {
			result = layer("consumers", Known, "owned_consumers_ready", "")
		}
	}
	return []Layer{result}
}

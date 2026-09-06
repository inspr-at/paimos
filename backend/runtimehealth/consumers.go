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
func ConsumerExtensions(ctx context.Context, status agentd.RuntimeStatus) []Layer {
	filtered := status
	filtered.Consumers = nil
	intent := layer("browser_intents", Unknown, "browser_intents_unavailable", "configure the reviewed daemon lifecycle project/account/profile/workspace mapping")
	seen, ready := false, true
	for _, e := range status.Consumers {
		if e.Kind != "intents" {
			filtered.Consumers = append(filtered.Consumers, e)
			continue
		}
		seen = true
		ready = ready && e.Generation == status.DaemonID && e.State == "ready" && !e.LastSuccess.IsZero() && time.Since(e.LastSuccess) < 45*time.Second && !e.LastSuccess.After(time.Now().Add(5*time.Second))
		if e.Reason == "outcome_unknown" {
			intent = layer("browser_intents", ActionRequired, "lifecycle_outcome_unknown", "reconcile the quarantined lifecycle effect receipt with its server intent before treating the runtime as healthy")
		}
		if e.Reason == "ownership_lost" {
			intent = layer("browser_intents", ActionRequired, "runtime_authority_expired", "reconcile outstanding intents and restart the reviewed daemon; old child mappings cannot be adopted")
		}
	}
	if seen && ready {
		intent = layer("browser_intents", Known, "owned_lifecycle_executor_ready", "")
	}
	primary := layer("primary_inbox", Unknown, "primary_inbox_unavailable", "start an owned managed generation and verify its reporter binding")
	primarySeen, primaryReady := false, true
	for _, e := range filtered.Consumers {
		if e.Kind != "primary" {
			continue
		}
		primarySeen = true
		primaryReady = primaryReady && e.Generation == status.DaemonID && e.State == "ready" && !e.LastSuccess.IsZero() && time.Since(e.LastSuccess) < 45*time.Second && !e.LastSuccess.After(time.Now().Add(5*time.Second))
	}
	if primarySeen && primaryReady {
		primary = layer("primary_inbox", Known, "owned_primary_inbox_ready", "ordinary messages and controls are available; optional simple fallback and attention use the reviewed receiver-setup command from your worker or orchestrator start result")
	}
	return append(consumerExtensions(ctx, filtered), intent, primary)
}

func consumerExtensions(_ context.Context, status agentd.RuntimeStatus) []Layer {
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
			result = layer("consumers", ActionRequired, "consumer_authority_unavailable", "verify the configured runtime mapping, private leases and exact receiver target; preview legacy handoff if older listeners still hold work")
		}
		if e.State == "unavailable" && e.Reason == "receiver_not_configured" {
			result = layer("consumers", ActionRequired, "receiver_not_configured", "configure the authorized simple fallback and instance attention receiver targets")
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

// Public target inventory cannot establish ownership. The same selected project
// must have fresh evidence from this verified daemon for both fenced consumers.
func attestedTargets(status agentd.RuntimeStatus, project int64, now time.Time) bool {
	if project <= 0 {
		return false
	}
	seen := map[string]bool{}
	for _, e := range status.Consumers {
		if e.ProjectID != project || (e.Kind != "fallback" && e.Kind != "attention") {
			continue
		}
		if e.Generation != status.DaemonID || e.State != "ready" || e.LastSuccess.IsZero() || now.Sub(e.LastSuccess) >= 45*time.Second || e.LastSuccess.After(now.Add(5*time.Second)) {
			return false
		}
		seen[e.Kind] = true
	}
	return seen["fallback"] && seen["attention"]
}

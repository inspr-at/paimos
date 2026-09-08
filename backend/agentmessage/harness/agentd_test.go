// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"context"
	"errors"
	"testing"
)

func TestAgentdManagedDeliveryRequiresLeasedSteerCorrelation(t *testing.T) {
	target := `{"socket":"/tmp/agentd.sock","session_id":"019d1234-1234-7123-8123-123456789abc"}`
	plugin := AgentdCodexPlugin{}
	for _, request := range []DeliverRequest{
		{Level: LevelSteer, TargetRef: target, Body: "body"},
		{Level: LevelSimple, TargetRef: target, Body: "body", CorrelationID: "delivery-1"},
	} {
		_, err := plugin.Deliver(context.Background(), request)
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("request=%+v error=%v", request, err)
		}
		if request.Level == LevelSimple && (!unavailable.Reroute || unavailable.FallbackReason != "not_steerable") {
			t.Fatalf("simple request unavailable=%+v", unavailable)
		}
	}
}

func TestAgentdCursorPluginRefusesSameTurnSteer(t *testing.T) {
	plugin := AgentdCursorPlugin{}
	if plugin.Name() != AdapterAgentdCursor || plugin.Kind() != KindAgentdSession || plugin.MaximumLevel() != LevelSimple {
		t.Fatalf("plugin=%s/%s/%s", plugin.Name(), plugin.Kind(), plugin.MaximumLevel())
	}
	target := `{"socket":"/tmp/agentd.sock","session_id":"019d1234-1234-7123-8123-123456789abc"}`
	_, err := plugin.Deliver(context.Background(), DeliverRequest{Level: LevelSteer, TargetRef: target, Body: "mid-turn", CorrelationID: "delivery-1"})
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || unavailable.FallbackReason != "unsupported" || !unavailable.Reroute {
		t.Fatalf("steer unavailable=%v err=%v", unavailable, err)
	}
}

func TestAgentdClaudePluginIsDistinctFromUnmanagedClaude(t *testing.T) {
	plugin := AgentdClaudePlugin{}
	if plugin.Name() != AdapterAgentdClaude || plugin.Kind() != KindAgentdSession || plugin.MaximumLevel() != LevelSteer {
		t.Fatalf("plugin=%s/%s/%s", plugin.Name(), plugin.Kind(), plugin.MaximumLevel())
	}
	if AdapterAgentdClaude == AdapterClaudeResume || AdapterAgentdClaude == AdapterClaudeChannel {
		t.Fatal("owned Claude delivery must not masquerade as unmanaged session delivery")
	}
}

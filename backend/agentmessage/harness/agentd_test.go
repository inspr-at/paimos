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

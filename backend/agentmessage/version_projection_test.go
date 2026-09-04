// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeV1ProjectionExcludesV2ReplyFacts(t *testing.T) {
	envelope := Envelope{
		Cursor: 1, MessageID: "message", ContextID: "PAI", Role: "agent",
		Parts: []TextPart{{Kind: "text", Text: "body"}}, Metadata: map[string]any{},
		From: "codex:sender", To: "codex:receiver", ThreadID: "thread", Hop: 1,
		Delivered: true, ExpectsReply: true, HumanResolutionOutcome: "resolved",
		CreatedAt: "2026-09-04T00:00:00Z", DeliveryLevel: "simple", DeliveryFallback: "simple",
	}
	v1, err := json.Marshal(envelope.V1())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"expects_reply", "human_resolution_outcome"} {
		if strings.Contains(string(v1), forbidden) {
			t.Fatalf("v2 field %q leaked into v1 projection: %s", forbidden, v1)
		}
	}
	v2, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v2), `"expects_reply":true`) || !strings.Contains(string(v2), `"human_resolution_outcome":"resolved"`) {
		t.Fatalf("v2 projection lost reply facts: %s", v2)
	}
}

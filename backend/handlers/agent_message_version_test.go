// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentMessageSendVersionBoundaryIsStrict(t *testing.T) {
	const body = `{"to":"codex:worker","body":"hello","expects_reply":true}`
	legacyRequest := httptest.NewRequest("POST", "/api/projects/1/messages", strings.NewReader(body))
	legacyResponse := httptest.NewRecorder()
	var legacy sendEnvelopeRequest
	if decodeAgentMessageRequest(legacyResponse, legacyRequest, &legacy) {
		t.Fatal("frozen v1 accepted expects_reply")
	}
	if legacyResponse.Code != 400 {
		t.Fatalf("v1 status=%d body=%q", legacyResponse.Code, legacyResponse.Body.String())
	}

	v2Request := httptest.NewRequest("POST", "/api/v2/projects/1/messages", strings.NewReader(body))
	v2Response := httptest.NewRecorder()
	var v2 sendEnvelopeRequestV2
	if !decodeAgentMessageRequest(v2Response, v2Request, &v2) || !v2.ExpectsReply {
		t.Fatalf("v2 request rejected: status=%d body=%q parsed=%#v", v2Response.Code, v2Response.Body.String(), v2)
	}
}

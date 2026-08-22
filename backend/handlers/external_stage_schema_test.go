// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/inspr-at/paimos/backend/auth"
)

func TestExternalStageOpenAPIClosesSemanticAndAdministrativeShapes(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	janus := schemas["ExternalStageJanusEvidence"].(map[string]any)
	if branches, ok := janus["oneOf"].([]any); !ok || len(branches) != 4 {
		t.Fatalf("Janus exact kind/result/value branches=%v", janus["oneOf"])
	}
	report := schemas["ExternalStageReportRequest"].(map[string]any)
	if conditions, ok := report["allOf"].([]any); !ok || len(conditions) != 8 {
		t.Fatalf("report semantic conditions=%v", report["allOf"])
	}
	prerequisiteSet := schemas["ExternalStagePrerequisiteSetRequest"].(map[string]any)
	required := prerequisiteSet["required"].([]any)
	foundPrerequisites := false
	for _, name := range required {
		foundPrerequisites = foundPrerequisites || name == "prerequisites"
	}
	items := prerequisiteSet["properties"].(map[string]any)["prerequisites"].(map[string]any)
	if !foundPrerequisites || items["minItems"] != float64(0) || items["maxItems"] != float64(16) {
		t.Fatalf("explicit 0-16 prerequisites are not frozen: required=%v schema=%v", required, items)
	}
	revoke := schemas["ExternalStageRevokeRequest"].(map[string]any)
	epoch := revoke["properties"].(map[string]any)["expected_credential_epoch"].(map[string]any)
	if epoch["minimum"] != float64(0) {
		t.Fatalf("unminted handoff revoke minimum=%v", epoch["minimum"])
	}
	activation := schemas["ExternalStageOwnerActivationRequest"].(map[string]any)
	if activation["additionalProperties"] != false {
		t.Fatalf("owner activation must be closed: %v", activation["additionalProperties"])
	}
	wantActivationRequired := map[string]bool{
		"reporter_registration_id": true, "stage_key": true, "expected_attempt_number": true,
		"expected_plan_revision": true, "expected_current_execution": true,
		"expected_current_authority_epoch": true,
	}
	for _, field := range activation["required"].([]any) {
		delete(wantActivationRequired, field.(string))
	}
	if len(wantActivationRequired) != 0 {
		t.Fatalf("owner activation missing required fields: %v", wantActivationRequired)
	}
	activationProperties := activation["properties"].(map[string]any)
	for _, field := range []string{"reporter_registration_id", "expected_attempt_number", "expected_plan_revision"} {
		if activationProperties[field].(map[string]any)["minimum"] != float64(1) {
			t.Fatalf("owner activation %s minimum=%v", field, activationProperties[field])
		}
	}
	stageEnum := activationProperties["stage_key"].(map[string]any)["enum"].([]any)
	if len(stageEnum) != 2 || stageEnum[0] != "deployment" || stageEnum[1] != "verification" {
		t.Fatalf("owner activation stage enum=%v", stageEnum)
	}
	casBranches, ok := activation["oneOf"].([]any)
	if !ok || len(casBranches) != 2 {
		t.Fatalf("owner activation CAS branches=%v", activation["oneOf"])
	}
	zeroCAS := casBranches[0].(map[string]any)["properties"].(map[string]any)
	positiveCAS := casBranches[1].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"expected_current_execution", "expected_current_authority_epoch"} {
		if activationProperties[field].(map[string]any)["minimum"] != float64(0) ||
			zeroCAS[field].(map[string]any)["const"] != float64(0) ||
			positiveCAS[field].(map[string]any)["minimum"] != float64(1) {
			t.Fatalf("owner activation paired CAS %s root=%v zero=%v positive=%v",
				field, activationProperties[field], zeroCAS[field], positiveCAS[field])
		}
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{
		"/api/agent-mode/deliveries/{deliveryKey}/external-reporter-registrations",
		"/api/agent-mode/deliveries/{deliveryKey}/external-reporter-registrations/{registrationID}/revoke",
		"/api/agent-mode/deliveries/{deliveryKey}/external-prerequisite-sets",
		"/api/agent-mode/deliveries/{deliveryKey}/external-owner-activations",
	} {
		if paths[path] == nil {
			t.Fatalf("administrative path %q missing", path)
		}
	}
}

func TestInternalExternalStageRateLimitBoundsSessionAndAPIKeyPrincipals(t *testing.T) {
	internalExternalStageRates.Lock()
	internalExternalStageRates.entries = make(map[string]externalStageRateEntry)
	internalExternalStageRates.Unlock()
	t.Cleanup(func() {
		internalExternalStageRates.Lock()
		internalExternalStageRates.entries = make(map[string]externalStageRateEntry)
		internalExternalStageRates.Unlock()
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := internalExternalStageRateLimit(next)
	session, err := auth.NewSessionPrincipal("81000000-0000-4000-8000-000000000899", 1, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 121; index++ {
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		request = request.WithContext(auth.WithPrincipal(request.Context(), session))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		want := http.StatusNoContent
		if index == 120 {
			want = http.StatusTooManyRequests
		}
		if recorder.Code != want || (want == http.StatusTooManyRequests && recorder.Header().Get("Retry-After") != "60") {
			t.Fatalf("session request %d status=%d retry=%q", index+1, recorder.Code, recorder.Header().Get("Retry-After"))
		}
	}
	apiKey, err := auth.NewAPIKeyPrincipal(899, 1, auth.ScopeSet{auth.ScopeAgentControlsWrite: {}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), apiKey))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("API key did not receive an independent admission bucket: %d", recorder.Code)
	}
}

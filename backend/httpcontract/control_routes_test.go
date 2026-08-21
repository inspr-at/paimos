// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package httpcontract

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Every frozen family, spelled out. This list is the contract: a family
// that stops classifying stops being redacted, and no test elsewhere
// would notice.
func TestClassifyControlPathCoversEveryFrozenFamily(t *testing.T) {
	cases := []struct {
		path string
		want ControlRouteClass
	}{
		{"/api/agent-mode/deliveries/PAI-809-42/control-capability-grants", ControlRouteDeliveryCapabilityGrants},
		{"/api/agent-mode/deliveries/PAI-809-42/control-commands", ControlRouteDeliveryCommands},
		{"/api/agent-mode/control-capability-grants/01JD8K3P0000000000000000AB", ControlRouteCapabilityGrantDetail},
		{"/api/agent-mode/control-commands/9f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f", ControlRouteCommandDetail},
		{"/api/runs/17/control-capability-leases", ControlRouteRunCapabilityLeases},
		{"/api/runs/17/input-requests", ControlRouteRunInputRequests},
		{"/api/runs/17/control-commands", ControlRouteRunCommands},
		{"/api/control-capability-leases/17", ControlRouteCapabilityLeaseDetail},
		{"/api/control-commands/17", ControlRouteCommandRootDetail},
		{"/api/agent-mode/deliveries/PAI-810-42/external-stage-handoffs", ControlRouteExternalHandoffCreate},
		{"/api/agent-mode/external-stage-handoffs/01K35P6YRG00000000000000AB/mint", ControlRouteExternalHandoffMint},
		{"/api/agent-mode/external-stage-handoffs/01K35P6YRG00000000000000AB/rotate", ControlRouteExternalHandoffRotate},
		{"/api/agent-mode/external-stage-handoffs/01K35P6YRG00000000000000AB/revoke", ControlRouteExternalHandoffRevoke},
		{"/api/external-stage/handoffs/01K35P6YRG00000000000000AB", ControlRouteExternalHandoffPull},
		{"/api/external-stage/handoffs/01K35P6YRG00000000000000AB/accept", ControlRouteExternalHandoffAccept},
		{"/api/external-stage/handoffs/01K35P6YRG00000000000000AB/reports", ControlRouteExternalHandoffReport},
	}
	seen := map[ControlRouteClass]struct{}{}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			class, ok := ClassifyControlPath(tc.path)
			if !ok {
				t.Fatalf("frozen control family was not classified: %s", tc.path)
			}
			if class != tc.want {
				t.Fatalf("class = %q, want %q", class, tc.want)
			}
		})
		seen[tc.want] = struct{}{}
	}
	for _, class := range ControlRouteClasses() {
		if _, ok := seen[class]; !ok {
			t.Fatalf("route class %q has no covering case", class)
		}
	}
}

// A near-miss keeps ordinary behavior — including ordinary logging and
// ordinary session-activity persistence. Classification must come from
// exact structure, never from a substring or a prefix.
func TestClassifyControlPathRejectsNearMisses(t *testing.T) {
	nearMisses := []string{
		// Missing or empty parameter segment.
		"/api/control-commands",
		"/api/control-commands/",
		"/api/control-capability-leases",
		"/api/agent-mode/control-commands",
		"/api/agent-mode/control-capability-grants",
		"/api/agent-mode/deliveries//control-commands",
		"/api/runs//input-requests",
		// Extra segment beyond the family.
		"/api/control-commands/17/extra",
		"/api/agent-mode/control-capability-grants/17/audit",
		"/api/runs/17/control-commands/9",
		"/api/runs/17/input-requests/9",
		"/api/agent-mode/deliveries/PAI-1/PAI-2/control-commands",
		// Adjacent spellings.
		"/api/control-commandsx/17",
		"/api/xcontrol-commands/17",
		"/api/control-command/17",
		"/api/control-capability-lease/17",
		"/api/runs/17/input-request",
		"/api/runs/17/control-capability-lease",
		"/api/agent-mode/deliveries/PAI-1/control-commands-extra",
		"/api/agent-mode/deliveries/PAI-1/control-capability-grant",
		// Wrong prefix or missing prefix.
		"/apix/control-commands/17",
		"/control-commands/17",
		"api/control-commands/17",
		"//api/control-commands/17",
		"/api//control-commands/17",
		// Case is structure here, not decoration.
		"/API/control-commands/17",
		"/api/Control-Commands/17",
		"/api/agent-mode/Deliveries/PAI-1/control-commands",
		// Neighbouring real routes that must keep behaving normally.
		"/api/runs/17/telemetry",
		"/api/agent-mode/deliveries/PAI-1",
		"/api/agent-mode/deliveries",
		"/api/issues/17/implement",
		"/api/agent-mode/deliveries/PAI-1/external-stage-handoff",
		"/api/agent-mode/external-stage-handoffs/opaque",
		"/api/agent-mode/external-stage-handoffs/opaque/action",
		"/api/external-stage/handoffs",
		"/api/external-stage/handoffs/opaque/",
		"/api/external-stage/handoffs/opaque/report",
		"/api/external-stage/handoffs/opaque/rotate",
		"",
		"/",
	}
	for _, path := range nearMisses {
		t.Run(path, func(t *testing.T) {
			if class, ok := ClassifyControlPath(path); ok {
				t.Fatalf("near-miss %q was classified as control (%q)", path, class)
			}
		})
	}
}

// A trailing slash is a different route to the mux, so it must be a
// different route here — but the classifier must not be fooled by
// percent-encoding in either direction.
func TestClassifyControlRequestHandlesEncodedPaths(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   ControlRouteClass
		ok     bool
	}{
		{
			name:   "encoded slash stays one opaque parameter",
			target: "/api/control-commands/a%2Fb",
			want:   ControlRouteCommandRootDetail,
			ok:     true,
		},
		{name: "encoded literal segment is a chi near-miss", target: "/api/%63ontrol-commands/17", ok: false},
		{
			name:   "query string never participates",
			target: "/api/runs/17/input-requests?delivery=PAI-809&token=secret",
			want:   ControlRouteRunInputRequests,
			ok:     true,
		},
		{
			name:   "trailing slash is a different route",
			target: "/api/control-commands/17/",
			ok:     false,
		},
		{name: "encoded structural slash is a chi near-miss", target: "/api/control-commands%2F17", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", tc.target, nil)
			class, ok := ClassifyControlRequest(request)
			if ok != tc.ok {
				t.Fatalf("classified=%v want %v (class=%q)", ok, tc.ok, class)
			}
			if tc.ok && class != tc.want {
				t.Fatalf("class = %q, want %q", class, tc.want)
			}
			if IsControlRequest(request) != tc.ok {
				t.Fatal("IsControlRequest disagreed with ClassifyControlRequest")
			}
		})
	}
	if _, ok := ClassifyControlRequest(nil); ok {
		t.Fatal("nil request classified as control")
	}
}

func TestClassifyControlRequestMatchesChiDispatch(t *testing.T) {
	router := chi.NewRouter()
	hit := false
	for _, pattern := range []string{
		"/api/agent-mode/deliveries/{deliveryKey}/control-capability-grants",
		"/api/agent-mode/deliveries/{deliveryKey}/control-commands",
		"/api/agent-mode/control-capability-grants/{id}",
		"/api/agent-mode/control-commands/{id}",
		"/api/runs/{id}/control-capability-leases",
		"/api/runs/{id}/input-requests",
		"/api/runs/{id}/control-commands",
		"/api/control-capability-leases/{id}",
		"/api/control-commands/{id}",
		"/api/agent-mode/deliveries/{deliveryKey}/external-stage-handoffs",
		"/api/agent-mode/external-stage-handoffs/{handoffID}/mint",
		"/api/agent-mode/external-stage-handoffs/{handoffID}/rotate",
		"/api/agent-mode/external-stage-handoffs/{handoffID}/revoke",
		"/api/external-stage/handoffs/{handoffID}",
		"/api/external-stage/handoffs/{handoffID}/accept",
		"/api/external-stage/handoffs/{handoffID}/reports",
	} {
		router.Handle(pattern, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	}

	targets := []string{
		"/api/control-commands/17",
		"/api/control-commands/a%2Fb",
		"/api/control-commands/%2e%2e",
		"/api/runs/17/input-requests?opaque=1",
		"/api/%63ontrol-commands/17",
		"/api/control-commands%2F17",
		"/api/control-commands/17/",
		"/api/control-commands//17",
		"/api/control-commands/a/../b",
		"/api/Control-Commands/17",
		"/api/control-commands/17/extra",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			hit = false
			request := httptest.NewRequest(http.MethodPost, target, nil)
			router.ServeHTTP(httptest.NewRecorder(), request)
			if classified := IsControlRequest(request); classified != hit {
				t.Fatalf("classifier=%v chi_dispatch=%v raw=%q path=%q", classified, hit,
					request.URL.RawPath, request.URL.Path)
			}
		})
	}
}

func TestAgentModeNotFoundOmitsControlInstance(t *testing.T) {
	const pathCanary = "CANARY-CONTROL-NOT-FOUND-809"
	const queryCanary = "CANARY-CONTROL-NOT-FOUND-QUERY-809"
	request := httptest.NewRequest(http.MethodGet,
		"/api/agent-mode/control-commands/"+pathCanary+"?secret="+queryCanary, nil)
	recorder := httptest.NewRecorder()
	recorder.Header().Set(requestIDHeader, "server-request-809")
	WriteAgentModeNotFound(recorder, request)

	response := recorder.Body.String()
	for _, canary := range []string{pathCanary, queryCanary, `"instance"`} {
		if strings.Contains(response, canary) {
			t.Fatalf("control not-found response leaked %q: %s", canary, response)
		}
	}
	if recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("control not-found response was cacheable")
	}

	ordinary := httptest.NewRecorder()
	ordinaryRequest := httptest.NewRequest(http.MethodGet, "/api/agent-mode/deliveries/missing?view=1", nil)
	WriteAgentModeNotFound(ordinary, ordinaryRequest)
	if !strings.Contains(ordinary.Body.String(), `"instance":"/api/agent-mode/deliveries/missing?view=1"`) {
		t.Fatalf("ordinary Agent Mode 404 lost its instance: %s", ordinary.Body.String())
	}
}

// The label vocabulary is the thing that ends up in a log line, so it has
// to be closed and free of anything a caller could influence.
func TestControlRouteClassesAreClosedAndOpaque(t *testing.T) {
	classes := ControlRouteClasses()
	if len(classes) != len(controlRoutes) {
		t.Fatalf("got %d classes for %d families — a family lost its own label", len(classes), len(controlRoutes))
	}
	seen := map[ControlRouteClass]struct{}{}
	for _, class := range classes {
		if _, dup := seen[class]; dup {
			t.Fatalf("duplicate route class %q", class)
		}
		seen[class] = struct{}{}
		label := string(class)
		if label == "" {
			t.Fatal("empty route class")
		}
		if strings.ContainsAny(label, " \t\r\n\"/{}%") {
			t.Fatalf("route class %q is not a safe log token", label)
		}
	}
}

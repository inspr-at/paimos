// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// controlLogLine is the whole permitted shape of a control access-log
// line: five fields, all of them either compile-time constants or
// bounded tokens. Anything else in the line is a leak.
var controlLogLine = regexp.MustCompile(
	`^control: method=[A-Z]+ route_class=[a-z_.]+ request_id=[A-Za-z0-9._:-]+ status=\d{3} duration_ms=\d+$`)

func captureHandlerLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	return &logs
}

// countOrdinaryLoggerCalls replaces the chi access log with a counter, so
// a test can prove a control request never reached it. Must run before
// ControlAwareRequestLogger is constructed — it captures the seam.
func countOrdinaryLoggerCalls(t *testing.T, calls *int) {
	t.Helper()
	previous := ordinaryRequestLogger
	ordinaryRequestLogger = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*calls++
			next.ServeHTTP(w, r)
		})
	}
	t.Cleanup(func() { ordinaryRequestLogger = previous })
}

const (
	logCanaryDelivery    = "CANARY-LOG-DELIVERY-809"
	logCanaryQuery       = "CANARY-LOG-QUERY-809"
	logCanaryOrigin      = "https://attacker.example/CANARY-LOG-ORIGIN-809"
	logCanarySession     = "CANARY-LOG-SESSION-809"
	logCanaryIdempotency = "CANARY-LOG-IDEMPOTENCY-809"
	logCanaryBearer      = "CANARY-LOG-BEARER-809"
	logCanaryBody        = "CANARY-LOG-BODY-809"
)

func hostileControlRequest(path string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path+"?delivery="+logCanaryQuery,
		strings.NewReader(`{"note":"`+logCanaryBody+`"}`))
	request.Host = "canary-host-809.attacker.example"
	request.Header.Set("Origin", logCanaryOrigin)
	request.Header.Set("Referer", logCanaryOrigin)
	request.Header.Set("X-PAIMOS-Session-Id", logCanarySession)
	request.Header.Set("Idempotency-Key", logCanaryIdempotency)
	request.Header.Set("Authorization", "Bearer "+logCanaryBearer)
	return request
}

func TestControlRequestsBypassTheOrdinaryAccessLog(t *testing.T) {
	paths := []string{
		"/api/agent-mode/deliveries/" + logCanaryDelivery + "/control-capability-grants",
		"/api/agent-mode/deliveries/" + logCanaryDelivery + "/control-commands",
		"/api/agent-mode/control-capability-grants/" + logCanaryDelivery,
		"/api/agent-mode/control-commands/" + logCanaryDelivery,
		"/api/runs/" + logCanaryDelivery + "/control-capability-leases",
		"/api/runs/" + logCanaryDelivery + "/input-requests",
		"/api/runs/" + logCanaryDelivery + "/control-commands",
		"/api/control-capability-leases/" + logCanaryDelivery,
		"/api/control-commands/" + logCanaryDelivery,
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			ordinaryCalls := 0
			countOrdinaryLoggerCalls(t, &ordinaryCalls)
			logs := captureHandlerLog(t)

			handler := ControlAwareRequestLogger(RequestIDMiddleware(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusAccepted)
				})))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, hostileControlRequest(path))

			if ordinaryCalls != 0 {
				t.Fatal("control request reached chi's access log")
			}
			if recorder.Code != http.StatusAccepted {
				t.Fatalf("status=%d — logging changed the response", recorder.Code)
			}
			line := strings.TrimSpace(logs.String())
			if !controlLogLine.MatchString(line) {
				t.Fatalf("control log line is not the permitted five-field shape: %q", line)
			}
			for _, canary := range []string{
				logCanaryDelivery, logCanaryQuery, logCanaryOrigin, logCanarySession,
				logCanaryIdempotency, logCanaryBearer, logCanaryBody,
				"attacker.example", path,
			} {
				if strings.Contains(line, canary) {
					t.Fatalf("control log line leaked %q: %q", canary, line)
				}
			}
			if !strings.Contains(line, "status=202") {
				t.Fatalf("control log line lost the status: %q", line)
			}
		})
	}
}

func TestOrdinaryRequestsKeepTheOrdinaryAccessLog(t *testing.T) {
	for _, path := range []string{
		"/api/tags",
		"/api/control-commands/17/extra",
		"/api/control-commands",
		"/api/runs/17/telemetry",
	} {
		t.Run(path, func(t *testing.T) {
			ordinaryCalls := 0
			countOrdinaryLoggerCalls(t, &ordinaryCalls)
			logs := captureHandlerLog(t)

			handler := ControlAwareRequestLogger(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			handler.ServeHTTP(httptest.NewRecorder(), hostileControlRequest(path))

			if ordinaryCalls != 1 {
				t.Fatalf("ordinary route did not reach chi's access log (calls=%d)", ordinaryCalls)
			}
			if strings.Contains(logs.String(), "control:") {
				t.Fatalf("ordinary route was logged as control: %q", logs.String())
			}
		})
	}
}

// The logger still bounds request ids defensively, while the normal router
// composition replaces caller-provided control ids before the logger sees
// them. This keeps the log safe even if a future mount omits or reorders the
// request-id middleware.
func TestControlLogRequestIDIsBounded(t *testing.T) {
	cases := []struct {
		name     string
		supplied string
		want     string
	}{
		{"canonical id survives", "req-01JD8K3P.abc:1", "req-01JD8K3P.abc:1"},
		{"spaces are dropped", "req 809 injected status=200", "unavailable"},
		{"quotes are dropped", `req"809`, "unavailable"},
		{"equals sign is dropped", "route_class=spoofed", "unavailable"},
		{"overlong is dropped", strings.Repeat("a", 65), "unavailable"},
		{"empty is dropped", "", "unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeLogRequestID(tc.supplied); got != tc.want {
				t.Fatalf("safeLogRequestID(%q) = %q, want %q", tc.supplied, got, tc.want)
			}
		})
	}

	logs := captureHandlerLog(t)
	handler := ControlAwareRequestLogger(RequestIDMiddleware(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	request := hostileControlRequest("/api/control-commands/17")
	request.Header.Set(RequestIDHeader, `spoofed" status=500 secret=`+logCanaryBearer)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	line := strings.TrimSpace(logs.String())
	if !controlLogLine.MatchString(line) {
		t.Fatalf("hostile request id broke the line shape: %q", line)
	}
	if strings.Contains(line, logCanaryBearer) || strings.Contains(line, "spoofed") {
		t.Fatalf("hostile request id was echoed: %q", line)
	}
	if strings.Contains(line, "request_id=unavailable") {
		t.Fatalf("request-id middleware did not mint a server correlation id: %q", line)
	}
}

// The method vocabulary is closed, so an exotic verb cannot author part
// of the line either.
func TestControlLogMethodVocabularyIsClosed(t *testing.T) {
	for _, method := range []string{
		http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost,
		http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		if got := safeLogMethod(method); got != method {
			t.Fatalf("safeLogMethod(%q) = %q", method, got)
		}
	}
	for _, method := range []string{"PROPFIND", "TRACE", "CONNECT", "post", ""} {
		if got := safeLogMethod(method); got != "OTHER" {
			t.Fatalf("safeLogMethod(%q) = %q, want OTHER", method, got)
		}
	}
}

// A control route must not lose streaming just because it is logged
// privately: the wrapper still forwards Flush.
func TestControlLoggerPreservesFlusher(t *testing.T) {
	captureHandlerLog(t)
	flushable := false
	handler := ControlAwareRequestLogger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, flushable = w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), hostileControlRequest("/api/runs/17/input-requests"))
	if !flushable {
		t.Fatal("control handler lost http.Flusher")
	}
}

// A handler that never writes still yields the status net/http will send.
func TestControlLoggerDefaultsToTwoHundred(t *testing.T) {
	logs := captureHandlerLog(t)
	handler := ControlAwareRequestLogger(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	handler.ServeHTTP(httptest.NewRecorder(), hostileControlRequest("/api/control-commands/17"))
	if !strings.Contains(logs.String(), "status=200") {
		t.Fatalf("unwritten response was not logged as 200: %q", logs.String())
	}
}

func TestControlAwareRecovererDoesNotLogArbitraryPanicValues(t *testing.T) {
	const panicCanary = "CANARY-CONTROL-PANIC-809"
	logs := captureHandlerLog(t)
	handler := ClassifiedControlCachePolicyMiddleware(
		ControlAwareRequestLogger(
			ControlAwareRecoverer(
				RequestIDMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					panic("provider failure: " + panicCanary)
				})),
			),
		),
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, hostileControlRequest("/api/control-commands/17"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want 500", recorder.Code)
	}
	if recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("recovered control panic was cacheable")
	}
	if strings.Contains(logs.String(), panicCanary) {
		t.Fatalf("recovered panic value reached logs: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "status=500") {
		t.Fatalf("safe access log lost recovered status: %q", logs.String())
	}
}

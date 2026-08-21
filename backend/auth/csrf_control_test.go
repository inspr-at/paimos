// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

// controlCSRFCanaries are values a hostile caller controls. Each one is
// distinctive enough that finding it in a log line is unambiguous.
var controlCSRFCanaries = map[string]string{
	"origin":     "https://attacker.example/CANARY-ORIGIN-809",
	"referer":    "https://attacker.example/CANARY-REFERER-809",
	"host":       "canary-host-809.attacker.example",
	"session":    "CANARY-SESSION-ID-809",
	"idempotent": "CANARY-IDEMPOTENCY-KEY-809",
	"token":      "CANARY-CSRF-TOKEN-809",
	"delivery":   "CANARY-DELIVERY-KEY-809",
	"query":      "CANARY-QUERY-VALUE-809",
	"bearer":     "CANARY-BEARER-809",
	"forwarded":  `CANARY-FORWARDED-IP-809" route_class=forged`,
	"real_ip":    "CANARY-REAL-IP-809",
}

func captureAuthLog(t *testing.T) *bytes.Buffer {
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

// newHostileControlRequest builds a session-authenticated mutation on a
// control route with every caller-controlled field set to a canary.
func newHostileControlRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost,
		path+"?delivery="+controlCSRFCanaries["query"], strings.NewReader(`{"note":"`+controlCSRFCanaries["query"]+`"}`))
	request.Host = controlCSRFCanaries["host"]
	request.Header.Set("Origin", controlCSRFCanaries["origin"])
	request.Header.Set("Referer", controlCSRFCanaries["referer"])
	request.Header.Set("X-PAIMOS-Session-Id", controlCSRFCanaries["session"])
	request.Header.Set("Idempotency-Key", controlCSRFCanaries["idempotent"])
	request.Header.Set("Authorization", "Bearer "+controlCSRFCanaries["bearer"])
	request.Header.Set("X-Forwarded-For", controlCSRFCanaries["forwarded"])
	request.Header.Set("X-Real-IP", controlCSRFCanaries["real_ip"])
	request.Header.Set(CSRFHeaderName, controlCSRFCanaries["token"])
	return request.WithContext(withSessionAuth(request.Context(), "server-side-token"))
}

func TestSafeControlClientIPCanonicalizesOrOmits(t *testing.T) {
	cases := []struct {
		name      string
		forwarded string
		realIP    string
		remote    string
		want      string
	}{
		{name: "ipv4", forwarded: "203.0.113.9", want: "203.0.113.9"},
		{name: "ipv6 canonicalized", forwarded: "2001:0db8:0:0:0:0:0:1", want: "2001:db8::1"},
		{name: "first forwarded address", forwarded: "203.0.113.10, 198.51.100.2", want: "203.0.113.10"},
		{name: "real ip fallback", realIP: "198.51.100.7", want: "198.51.100.7"},
		{name: "remote fallback", remote: "192.0.2.22:4567", want: "192.0.2.22"},
		{name: "arbitrary forwarded text", forwarded: controlCSRFCanaries["forwarded"], want: "unavailable"},
		{name: "host and port in forwarding header", forwarded: "203.0.113.9:443", want: "unavailable"},
		{name: "overlong", forwarded: strings.Repeat("1", 256), want: "unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/control-commands/17", nil)
			request.Header.Set("X-Forwarded-For", tc.forwarded)
			request.Header.Set("X-Real-IP", tc.realIP)
			if tc.remote != "" {
				request.RemoteAddr = tc.remote
			}
			if got := safeControlClientIP(request); got != tc.want {
				t.Fatalf("safeControlClientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

func assertNoCanaries(t *testing.T, label, text string) {
	t.Helper()
	for name, value := range controlCSRFCanaries {
		if strings.Contains(text, value) {
			t.Fatalf("%s leaked the %s canary: %q", label, name, text)
		}
	}
}

// The gate still fires on a control route, and the record of it names
// nothing the caller supplied.
func TestCSRFOnControlRoutesStaysEnforcedAndLogsOnlySafeFields(t *testing.T) {
	for _, path := range []string{
		"/api/agent-mode/deliveries/" + controlCSRFCanaries["delivery"] + "/control-capability-grants",
		"/api/agent-mode/deliveries/" + controlCSRFCanaries["delivery"] + "/control-commands",
		"/api/agent-mode/control-capability-grants/" + controlCSRFCanaries["delivery"],
		"/api/agent-mode/control-commands/" + controlCSRFCanaries["delivery"],
		"/api/runs/" + controlCSRFCanaries["delivery"] + "/control-capability-leases",
		"/api/runs/" + controlCSRFCanaries["delivery"] + "/input-requests",
		"/api/runs/" + controlCSRFCanaries["delivery"] + "/control-commands",
		"/api/control-capability-leases/" + controlCSRFCanaries["delivery"],
		"/api/control-commands/" + controlCSRFCanaries["delivery"],
	} {
		t.Run(path, func(t *testing.T) {
			logs := captureAuthLog(t)
			request := newHostileControlRequest(t, path)
			recorder := httptest.NewRecorder()
			CSRFMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("cross-origin control mutation reached the handler")
			})).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status=%d, want 403 — control classification must not exempt CSRF", recorder.Code)
			}
			text := logs.String()
			if !strings.Contains(text, "csrf_origin_blocked") {
				t.Fatalf("rejection was not audited: %q", text)
			}
			if !strings.Contains(text, "route_class=") {
				t.Fatalf("rejection log carries no route class: %q", text)
			}
			assertNoCanaries(t, "csrf origin rejection", text)
		})
	}
}

// Same for the token branch: same-origin, wrong token.
func TestCSRFTokenMismatchOnControlRouteRedactsTheRecord(t *testing.T) {
	logs := captureAuthLog(t)
	request := newHostileControlRequest(t, "/api/control-commands/"+controlCSRFCanaries["delivery"])
	// Make the origin check pass so the token branch is the one that fires.
	request.Header.Set("Origin", "https://"+request.Host)
	request.Header.Del("Referer")

	recorder := httptest.NewRecorder()
	CSRFMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("mismatched CSRF token reached the handler")
	})).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", recorder.Code)
	}
	text := logs.String()
	if !strings.Contains(text, "csrf_token_mismatch") || !strings.Contains(text, "route_class=control_command") {
		t.Fatalf("token mismatch log is missing its safe fields: %q", text)
	}
	for _, canary := range []string{controlCSRFCanaries["token"], controlCSRFCanaries["delivery"],
		controlCSRFCanaries["session"], controlCSRFCanaries["idempotent"], controlCSRFCanaries["query"]} {
		if strings.Contains(text, canary) {
			t.Fatalf("token mismatch log leaked %q: %q", canary, text)
		}
	}
}

// A valid same-origin control mutation still passes. Redaction is about
// the record, not the decision.
func TestCSRFAcceptsValidControlMutation(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/control-commands/17", nil)
	request.Host = "paimos.example"
	request.Header.Set("Origin", "https://paimos.example")
	request.Header.Set(CSRFHeaderName, "matching-token")
	request = request.WithContext(withSessionAuth(request.Context(), "matching-token"))

	passed := false
	recorder := httptest.NewRecorder()
	CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		passed = true
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)

	if !passed || recorder.Code != http.StatusNoContent {
		t.Fatalf("valid control mutation was refused: passed=%v status=%d", passed, recorder.Code)
	}
}

// A near-miss path keeps the verbose diagnostic that ordinary routes
// depend on — redaction must not bleed outward.
func TestCSRFNearMissPathKeepsOrdinaryDiagnostic(t *testing.T) {
	logs := captureAuthLog(t)
	request := newHostileControlRequest(t, "/api/control-commands/17/extra")
	recorder := httptest.NewRecorder()
	CSRFMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("cross-origin mutation reached the handler")
	})).ServeHTTP(recorder, request)

	text := logs.String()
	if !strings.Contains(text, controlCSRFCanaries["origin"]) ||
		!strings.Contains(text, controlCSRFCanaries["referer"]) ||
		!strings.Contains(text, controlCSRFCanaries["host"]) {
		t.Fatalf("near-miss route lost the ordinary CSRF diagnostic: %q", text)
	}
	if strings.Contains(text, "route_class=") {
		t.Fatalf("near-miss route was treated as control: %q", text)
	}
}

func TestMustChangePasswordProblemDoesNotReflectControlURIOrRequestID(t *testing.T) {
	const pathCanary = "CANARY-MUST-CHANGE-PATH-809"
	const queryCanary = "CANARY-MUST-CHANGE-QUERY-809"
	const requestIDCanary = "CANARY-MUST-CHANGE-REQUEST-ID-809"
	request := httptest.NewRequest(http.MethodPost,
		"/api/control-commands/"+pathCanary+"?secret="+queryCanary, nil)
	request.Header.Set("X-PAIMOS-Request-Id", requestIDCanary)
	recorder := httptest.NewRecorder()
	recorder.Header().Set("X-PAIMOS-Request-Id", "server-request-809")
	writeMustChangePasswordProblem(recorder, request)

	response := recorder.Body.String()
	for _, canary := range []string{pathCanary, queryCanary, requestIDCanary, `"instance"`} {
		if strings.Contains(response, canary) {
			t.Fatalf("control password-gate response leaked %q: %s", canary, response)
		}
	}
	if !strings.Contains(response, `"request_id":"server-request-809"`) {
		t.Fatalf("control password-gate response lost server request id: %s", response)
	}

	ordinaryRequest := httptest.NewRequest(http.MethodPost, "/api/issues/17?view=1", nil)
	ordinary := httptest.NewRecorder()
	writeMustChangePasswordProblem(ordinary, ordinaryRequest)
	if !strings.Contains(ordinary.Body.String(), `"instance":"/api/issues/17?view=1"`) {
		t.Fatalf("ordinary password-gate response lost instance: %s", ordinary.Body.String())
	}
}

func TestImpersonationAuditRedactsControlEndpointAndCallerRequestID(t *testing.T) {
	setupPrincipalTestDB(t)
	actorID := insertPrincipalUser(t, "control-audit-actor")
	targetID := insertPrincipalUser(t, "control-audit-target")
	const pathCanary = "CANARY-IMPERSONATION-PATH-809"
	const queryCanary = "CANARY-IMPERSONATION-QUERY-809"
	const requestIDCanary = "CANARY-IMPERSONATION-REQUEST-ID-809"
	const serverRequestID = "server-request-impersonation-809"

	request := httptest.NewRequest(http.MethodPost,
		"/api/control-commands/"+pathCanary+"?secret="+queryCanary, nil)
	request.Header.Set("X-PAIMOS-Request-Id", requestIDCanary)
	responseHeaders := http.Header{}
	responseHeaders.Set("X-PAIMOS-Request-Id", serverRequestID)
	requestID := requestIDForImpersonationAudit(request, responseHeaders)
	if requestID != serverRequestID {
		t.Fatalf("control audit request id = %q, want server id", requestID)
	}

	recordImpersonatedActionAudit(request, &sessionRecord{
		actor:         &models.User{ID: actorID},
		user:          &models.User{ID: targetID},
		impersonating: true,
	}, http.StatusForbidden, requestID)

	var endpoint, storedRequestID string
	if err := db.DB.QueryRow(`
		SELECT endpoint,request_id FROM super_admin_audit
		WHERE capability=? ORDER BY id DESC LIMIT 1
	`, CapabilityImpersonationAction).Scan(&endpoint, &storedRequestID); err != nil {
		t.Fatal(err)
	}
	if endpoint != "POST control_command" || storedRequestID != serverRequestID {
		t.Fatalf("control impersonation audit = endpoint %q request_id %q", endpoint, storedRequestID)
	}
	for _, canary := range []string{pathCanary, queryCanary, requestIDCanary} {
		if strings.Contains(endpoint+storedRequestID, canary) {
			t.Fatalf("control impersonation audit persisted %q", canary)
		}
	}
}

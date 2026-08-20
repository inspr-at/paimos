// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// PAI-294: OpenAPI coverage guard.
//
// Policy: /api/openapi.json is the PAIMOS *public / stable* scriptable
// contract — a deliberately curated subset of the canonical resource
// surface. Internal one-off admin tooling (imports, dev test reports,
// branding writes, SSO/TOTP management, AI ops, …) is omitted by design
// and is NOT part of the stability contract.
//
// This guard enforces the half of that policy a machine can: every path
// the published contract claims must resolve to a real registered route,
// so the spec can never silently lie after a route is renamed or removed.
// The complementary half — "new *public* routes get documented" — is a
// review rule (see CONTRIBUTING.md), because the public/internal split is
// a human judgement, not a prefix.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/deliverytrust"
	"github.com/inspr-at/paimos/backend/handlers"
)

var paramSegment = regexp.MustCompile(`\{[^}]*\}`)

// normalizeAPIPath canonicalizes a path for structural comparison: chi
// regex constraints are stripped and every {param} is collapsed to {} so
// param-name differences (chi {issueID} vs spec {id}) don't matter; a
// trailing slash is trimmed.
func normalizeAPIPath(p string) string {
	p = paramSegment.ReplaceAllString(p, "{}")
	if len(p) > len("/api") && strings.HasSuffix(p, "/") {
		p = strings.TrimRight(p, "/")
	}
	return p
}

// registeredAPIPaths walks the real route tree (built via mountAPI — no DB
// or server needed, since registration never executes handlers).
func registeredAPIPaths(t *testing.T) map[string]bool {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api", mountAPI)
	paths := map[string]bool{}
	err := chi.Walk(r, func(_ string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route = normalizeAPIPath(route); strings.HasPrefix(route, "/api") {
			paths[route] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	return paths
}

func registeredAPIOperations(t *testing.T) map[string]map[string]bool {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api", mountAPI)
	ops := map[string]map[string]bool{}
	err := chi.Walk(r, func(method string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = normalizeAPIPath(route)
		if !strings.HasPrefix(route, "/api") {
			return nil
		}
		method = strings.ToLower(method)
		if ops[route] == nil {
			ops[route] = map[string]bool{}
		}
		ops[route][method] = true
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	return ops
}

// documentedAPIPaths reads the published contract through the public handler.
func documentedAPIPaths(t *testing.T) map[string]bool {
	t.Helper()
	rec := httptest.NewRecorder()
	handlers.GetOpenAPI(rec, httptest.NewRequest(http.MethodGet, "/api/openapi.json", nil))
	var spec struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("openapi.json unmarshal: %v", err)
	}
	out := map[string]bool{}
	for p := range spec.Paths {
		out[normalizeAPIPath(p)] = true
	}
	return out
}

var openAPIMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true, "options": true, "head": true, "patch": true, "trace": true,
}

type openAPIDocument struct {
	Paths map[string]map[string]json.RawMessage `json:"paths"`
	raw   map[string]any
}

func documentedAPIDocument(t *testing.T) openAPIDocument {
	t.Helper()
	rec := httptest.NewRecorder()
	handlers.GetOpenAPI(rec, httptest.NewRequest(http.MethodGet, "/api/openapi.json", nil))
	var doc openAPIDocument
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("openapi.json unmarshal paths: %v", err)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc.raw); err != nil {
		t.Fatalf("openapi.json unmarshal raw: %v", err)
	}
	return doc
}

func documentedAPIOperations(t *testing.T) map[string]map[string]json.RawMessage {
	t.Helper()
	doc := documentedAPIDocument(t)
	out := map[string]map[string]json.RawMessage{}
	for p, methods := range doc.Paths {
		np := normalizeAPIPath(p)
		for method, op := range methods {
			method = strings.ToLower(method)
			if !openAPIMethods[method] {
				continue
			}
			if out[np] == nil {
				out[np] = map[string]json.RawMessage{}
			}
			out[np][method] = op
		}
	}
	return out
}

func TestOpenAPIContractRoutesExist(t *testing.T) {
	registered := registeredAPIPaths(t)
	documented := documentedAPIPaths(t)

	if len(documented) == 0 {
		t.Fatal("openapi.json documents no paths — the embedded contract looks empty")
	}

	var stale []string
	for p := range documented {
		if !registered[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(stale)
	for _, p := range stale {
		t.Errorf("PAI-294: openapi.json documents %q but no matching route is registered — "+
			"the published contract is stale; update backend/handlers/openapi.json", p)
	}
}

func TestOpenAPIContractMethodsExist(t *testing.T) {
	registered := registeredAPIOperations(t)
	documented := documentedAPIOperations(t)

	stale := staleOpenAPIMethods(registered, documented)
	for _, op := range stale {
		t.Errorf("PAI-624: openapi.json documents %q but no matching route method is registered", op)
	}
}

func TestAgentModeOpenAPIRouteCoverageIsBidirectional(t *testing.T) {
	registered := registeredAPIOperations(t)
	documented := documentedAPIOperations(t)
	wantRegistered := map[string]bool{}
	wantDocumented := map[string]bool{}
	for path, methods := range registered {
		if !strings.Contains(path, "/agent-mode/") && path != "/api/agent-mode" {
			continue
		}
		for method := range methods {
			wantRegistered[strings.ToUpper(method)+" "+path] = true
		}
	}
	for path, methods := range documented {
		if !strings.Contains(path, "/agent-mode/") && path != "/api/agent-mode" {
			continue
		}
		for method := range methods {
			wantDocumented[strings.ToUpper(method)+" "+path] = true
		}
	}
	if len(wantRegistered) != len(wantDocumented) {
		t.Fatalf("Agent Mode route coverage registered=%v documented=%v", wantRegistered, wantDocumented)
	}
	for operation := range wantRegistered {
		if !wantDocumented[operation] {
			t.Errorf("mounted Agent Mode operation is undocumented: %s", operation)
		}
	}
	for operation := range wantDocumented {
		if !wantRegistered[operation] {
			t.Errorf("documented Agent Mode operation is not mounted: %s", operation)
		}
	}
}

func TestAgentModeOpenAPIErrorAndStreamSemanticsArePinned(t *testing.T) {
	type response struct {
		Description string `json:"description"`
		Headers     map[string]struct {
			Schema struct {
				Const string `json:"const"`
			} `json:"schema"`
		} `json:"headers"`
		Content map[string]json.RawMessage `json:"content"`
	}
	type operation struct {
		Description string              `json:"description"`
		Responses   map[string]response `json:"responses"`
		SSEEvents   map[string]struct {
			ID any `json:"id"`
		} `json:"x-sse-events"`
	}
	doc := documentedAPIDocument(t)
	components, ok := doc.raw["components"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI components are missing")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI schemas are missing")
	}
	snapshotSchema, ok := schemas["AgentModeSnapshot"].(map[string]any)
	if !ok {
		t.Fatal("AgentModeSnapshot schema is missing")
	}
	required, _ := snapshotSchema["required"].([]any)
	selectedRequired := false
	for _, field := range required {
		selectedRequired = selectedRequired || field == "selected_delivery"
	}
	properties, _ := snapshotSchema["properties"].(map[string]any)
	selectedProperty, _ := properties["selected_delivery"].(map[string]any)
	if !selectedRequired || !strings.Contains(fmt.Sprint(selectedProperty["description"]), "empty string") {
		t.Fatalf("selected_delivery must be required with empty-history semantics: required=%v property=%v",
			required, selectedProperty)
	}
	paths := []string{
		"/api/agent-mode/deliveries",
		"/api/agent-mode/projects/{projectID}/deliveries",
		"/api/agent-mode/deliveries/{deliveryKey}",
		"/api/agent-mode/deliveries/events",
	}
	operations := make(map[string]operation, len(paths))
	for _, path := range paths {
		raw := doc.Paths[path]["get"]
		var op operation
		if err := json.Unmarshal(raw, &op); err != nil {
			t.Fatalf("%s operation: %v", path, err)
		}
		operations[path] = op
		for _, status := range []string{"403", "500"} {
			response, ok := op.Responses[status]
			if !ok || response.Headers["Cache-Control"].Schema.Const != "private, no-store" ||
				response.Content["application/problem+json"] == nil {
				t.Errorf("%s %s must be private/no-store problem+json: %+v", path, status, response)
			}
		}
	}
	events := operations["/api/agent-mode/deliveries/events"]
	stream := events.Responses["200"]
	if stream.Headers["Cache-Control"].Schema.Const != "private, no-store, no-transform" ||
		stream.Content["text/event-stream"] == nil || events.SSEEvents["reset"].ID != nil {
		t.Fatalf("events 200/reset contract is not pinned: response=%+v events=%+v", stream, events.SSEEvents)
	}
	for _, phrase := range []string{
		"storage invariant discovered before response headers is a private 500 problem",
		"already-established session emits one identity-free reset and closes",
		"fresh stream whose authorized scope exceeds 1,000 candidates returns a private 400",
		"resumed stream normalizes that changed scope to the same generic reset",
	} {
		if !strings.Contains(events.Description, phrase) {
			t.Errorf("events description lacks %q", phrase)
		}
	}
	for _, path := range []string{
		"/api/agent-mode/deliveries",
		"/api/agent-mode/projects/{projectID}/deliveries",
	} {
		if !strings.Contains(operations[path].Description, "1,000 authorized candidate roots") ||
			!strings.Contains(operations[path].Responses["400"].Description, "1,000") {
			t.Errorf("%s does not pin the explicit candidate ceiling", path)
		}
	}
	if !strings.Contains(operations["/api/agent-mode/deliveries/{deliveryKey}"].Description,
		"before the 1,000-candidate portfolio ceiling") ||
		!strings.Contains(events.Responses["400"].Description, "fresh authorized scope exceeding 1,000 candidates") {
		t.Error("detail/events candidate-ceiling semantics are not pinned")
	}
}

func TestAgentModeVoiceOpenAPIIsClosedTemplateOnlyAndNonCacheable(t *testing.T) {
	doc := documentedAPIDocument(t)
	components, _ := doc.raw["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	speak, _ := schemas["AgentModeVoiceSpeakRequest"].(map[string]any)
	transcript, _ := schemas["AgentModeVoiceTranscript"].(map[string]any)
	if speak == nil || transcript == nil || speak["additionalProperties"] != false || transcript["additionalProperties"] != false {
		t.Fatalf("voice schemas must be present and closed: speak=%v transcript=%v", speak, transcript)
	}
	required, _ := speak["required"].([]any)
	if fmt.Sprint(required) != "[template delivery_id delivery_revision candidate_ids locale]" {
		t.Fatalf("speak required=%v", required)
	}
	properties, _ := speak["properties"].(map[string]any)
	template, _ := properties["template"].(map[string]any)
	if got := fmt.Sprint(template["enum"]); got != "[status note_ready clarification]" {
		t.Fatalf("template enum=%s", got)
	}
	candidates, _ := properties["candidate_ids"].(map[string]any)
	if candidates["maxItems"] != float64(3) || candidates["uniqueItems"] != true {
		t.Fatalf("candidate_ids=%v", candidates)
	}
	transcriptProperties, _ := transcript["properties"].(map[string]any)
	final, _ := transcriptProperties["final"].(map[string]any)
	if final["const"] != true {
		t.Fatalf("transcript final=%v", final)
	}
	// PAI-808: the handler truncates to 8192 UTF-8 bytes. maxLength counts code
	// points, so the number alone under-specifies the contract — the description
	// must name the authoritative byte bound too.
	text, _ := transcriptProperties["text"].(map[string]any)
	if text["maxLength"] != float64(8192) {
		t.Fatalf("transcript text maxLength=%v, want 8192", text["maxLength"])
	}
	description := fmt.Sprint(text["description"])
	for _, phrase := range []string{"8192 UTF-8 bytes", "code points"} {
		if !strings.Contains(description, phrase) {
			t.Fatalf("transcript text description lacks %q: %q", phrase, description)
		}
	}

	for _, path := range []string{"/api/agent-mode/voice/transcribe", "/api/agent-mode/voice/speak"} {
		operationRaw := doc.Paths[path]["post"]
		var operation struct {
			Description string `json:"description"`
			Responses   map[string]struct {
				Headers map[string]struct {
					Schema struct {
						Const string `json:"const"`
					} `json:"schema"`
				} `json:"headers"`
			} `json:"responses"`
		}
		if err := json.Unmarshal(operationRaw, &operation); err != nil {
			t.Fatal(err)
		}
		statuses := []string{"200", "400", "401", "403", "404", "429", "500", "502", "503"}
		if path == "/api/agent-mode/voice/transcribe" {
			statuses = append(statuses, "413", "415", "422")
		} else {
			statuses = append(statuses, "409")
		}
		for _, status := range statuses {
			response, exists := operation.Responses[status]
			if !exists || response.Headers["Cache-Control"].Schema.Const != "private, no-store" {
				t.Fatalf("%s response %s is not private/no-store: %+v", path, status, response)
			}
		}
		if path == "/api/agent-mode/voice/speak" && !strings.Contains(operation.Description, "Never accepts caller text") {
			t.Fatalf("speak description does not pin template-only input: %q", operation.Description)
		}
	}
}

// PAI-808: the Agent Mode client treats X-Permissions-Epoch as a hard
// precondition on a 2xx — the SPA's local access cache is only invalidatable
// if every success carries a comparable epoch — and consumes
// X-Session-Expires-At opportunistically. auth.Middleware already guarantees
// both (epoch on every authenticated response, expiry only on the
// session-cookie branch), so the published contract has to say so on exactly
// the success responses the client depends on: the /auth/me refresh the epoch
// change itself triggers, STT, TTS, the selector-independent project list, and
// the delivery snapshot the production Agent Mode loader fetches through the
// same epoch-tracking api client.
//
// The list is exact in both directions. /auth/me is the hinge: the client
// re-fetches it precisely when the epoch it compares has moved, so a success
// there that carried no response-local epoch would leave the refresh unable to
// confirm it had caught up. Sitting behind auth.Middleware is what makes the
// headers guaranteed, so the unauthenticated half of the auth surface —
// /auth/login, which runs *before* any middleware can know the caller — must
// never claim them.
const (
	openAPIPermissionsEpochHeader = "X-Permissions-Epoch"
	openAPISessionExpiresHeader   = "X-Session-Expires-At"
	openAPIPermissionsEpochRef    = "#/components/headers/PermissionsEpoch"
	openAPISessionExpiresRef      = "#/components/headers/SessionExpiresAt"
)

func TestOpenAPIAuthContextHeadersArePinnedOnAuthenticatedSuccess(t *testing.T) {
	doc := documentedAPIDocument(t)
	want := map[string]string{
		openAPIPermissionsEpochHeader: openAPIPermissionsEpochRef,
		openAPISessionExpiresHeader:   openAPISessionExpiresRef,
	}
	targets := []struct{ method, path string }{
		{"get", "/api/auth/me"},
		{"post", "/api/agent-mode/voice/transcribe"},
		{"post", "/api/agent-mode/voice/speak"},
		{"get", "/api/projects"},
		{"get", "/api/agent-mode/deliveries"},
	}
	for _, target := range targets {
		operation := strings.ToUpper(target.method) + " " + target.path
		raw, ok := doc.Paths[target.path][target.method]
		if !ok {
			t.Fatalf("%s is undocumented", operation)
		}
		var parsed struct {
			Responses map[string]struct {
				Headers map[string]struct {
					Ref string `json:"$ref"`
				} `json:"headers"`
			} `json:"responses"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("%s operation: %v", operation, err)
		}
		success, ok := parsed.Responses["200"]
		if !ok {
			t.Fatalf("%s documents no 200 response", operation)
		}
		for header, ref := range want {
			got, exists := success.Headers[header]
			if !exists {
				t.Errorf("%s 200 does not document %s; documented headers: %v",
					operation, header, sortedKeys(success.Headers))
				continue
			}
			if got.Ref != ref {
				t.Errorf("%s 200 header %s = {\"$ref\": %q}, want %q — the reusable component is the single "+
					"point of truth for the epoch/expiry contract", operation, header, got.Ref, ref)
			}
		}
	}

	// The other direction: nothing outside that list may claim the headers.
	// auth.Middleware is what guarantees them, so a response reachable without
	// it — the /auth/login success that establishes the session in the first
	// place, or any error status the middleware short-circuits — would be
	// promising a header no code path writes.
	allowed := map[string]bool{}
	for _, target := range targets {
		allowed[strings.ToUpper(target.method)+" "+target.path+" 200"] = true
	}
	for _, claim := range responsesClaimingAuthContextHeaders(t, doc) {
		if !allowed[claim] {
			t.Errorf("%s documents an auth-context header, but it is not one of the authenticated successes "+
				"auth.Middleware covers — only %v may claim the epoch/expiry contract",
				claim, sortedKeys(allowed))
		}
	}
}

// responsesClaimingAuthContextHeaders walks every documented response and
// returns "METHOD /path status" for each one that names either auth-context
// header, however it spells the reference.
func responsesClaimingAuthContextHeaders(t *testing.T, doc openAPIDocument) []string {
	t.Helper()
	paths, ok := doc.raw["paths"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI paths are missing")
	}
	claims := []string{}
	for path, rawMethods := range paths {
		methods, ok := rawMethods.(map[string]any)
		if !ok {
			continue
		}
		for method, rawOperation := range methods {
			if !openAPIMethods[strings.ToLower(method)] {
				continue
			}
			operation, ok := rawOperation.(map[string]any)
			if !ok {
				continue
			}
			responses, ok := operation["responses"].(map[string]any)
			if !ok {
				continue
			}
			for status, rawResponse := range responses {
				response, ok := rawResponse.(map[string]any)
				if !ok {
					continue
				}
				headers, ok := response["headers"].(map[string]any)
				if !ok {
					continue
				}
				for _, header := range []string{openAPIPermissionsEpochHeader, openAPISessionExpiresHeader} {
					if _, claimed := headers[header]; claimed {
						claims = append(claims, strings.ToUpper(method)+" "+path+" "+status)
						break
					}
				}
			}
		}
	}
	sort.Strings(claims)
	return claims
}

// isCanonicalNonNegativeInt64 is the oracle the documented epoch pattern has
// to agree with: exactly the strings strconv.FormatInt emits for a
// non-negative int64. auth.Middleware renders the permissions_epoch column
// that way, so nothing else can ever appear on the wire.
func isCanonicalNonNegativeInt64(value string) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return false
	}
	return strconv.FormatInt(parsed, 10) == value
}

// permissionsEpochProbes are the inputs that discriminate the epoch pattern:
// ordinary counters, the non-canonical renderings a looser regex waves
// through, the neighbourhood of the int64 ceiling, digit-length sweeps either
// side of MaxInt64's 19 digits, and every single-digit perturbation of
// MaxInt64 — each of which straddles one alternative's boundary.
func permissionsEpochProbes() []string {
	const maxInt64 = "9223372036854775807"
	probes := []string{
		"", " ", "01", "007", "+1", "-1", "1.0", "1e3", " 1", "1 ", "abc", "0x1", "1\n2",
		"9223372036854775808",  // MaxInt64+1: what the open-ended pattern used to accept
		"9223372036854775810",  // just past the ceiling in the last-digit branch
		"9999999999999999999",  // 19 digits, far past the ceiling
		"18446744073709551615", // MaxUint64: the column's width, not its type
	}
	for _, value := range []int64{0, 1, 9, 10, 42, 1234567890, math.MaxInt64 - 1, math.MaxInt64} {
		probes = append(probes, strconv.FormatInt(value, 10))
	}
	for i := range maxInt64 {
		for digit := '0'; digit <= '9'; digit++ {
			head := maxInt64[:i] + string(digit)
			tail := len(maxInt64) - i - 1
			// Keep MaxInt64's own tail, then saturate that tail low and high:
			// a free-digit run in the pattern is only exercised by probes that
			// put both a 0 and a 9 in every one of its positions.
			probes = append(probes,
				head+maxInt64[i+1:],
				head+strings.Repeat("0", tail),
				head+strings.Repeat("9", tail),
			)
		}
	}
	for length := 1; length <= 21; length++ {
		probes = append(probes, strings.Repeat("9", length), "1"+strings.Repeat("0", length-1))
	}
	return probes
}

func sortedKeys[V any](set map[string]V) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestOpenAPIAuthContextHeaderComponentsFreezeCanonicalShape(t *testing.T) {
	doc := documentedAPIDocument(t)
	components, ok := doc.raw["components"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI components are missing")
	}
	headers, ok := components["headers"].(map[string]any)
	if !ok {
		t.Fatal("components.headers is missing — the epoch/expiry contract has no reusable definition")
	}

	epoch, ok := headers["PermissionsEpoch"].(map[string]any)
	if !ok {
		t.Fatal("PermissionsEpoch header component is missing")
	}
	if epoch["required"] != true {
		t.Errorf("PermissionsEpoch required=%v, want true — auth.Middleware sets the header on every "+
			"authenticated response, API-key and session alike", epoch["required"])
	}
	epochSchema, _ := epoch["schema"].(map[string]any)
	if epochSchema["type"] != "string" {
		t.Fatalf("PermissionsEpoch schema=%v, want a string (HTTP headers are strings; the client parses it itself)",
			epochSchema)
	}
	pattern, _ := epochSchema["pattern"].(string)
	if !strings.HasPrefix(pattern, "^") || !strings.HasSuffix(pattern, "$") {
		t.Fatalf("PermissionsEpoch pattern=%q must be anchored — JSON Schema `pattern` is a substring "+
			"match, so an unanchored pattern accepts any header that merely contains a number", pattern)
	}
	epochRE, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("PermissionsEpoch pattern does not compile: %v", err)
	}
	// The pattern spells the int64 ceiling out digit by digit, so freezing the
	// literal here would only make it unreadable. What the contract actually
	// promises is exact agreement with strconv: the header matches iff it is a
	// canonical strconv.FormatInt rendering of a non-negative int64, which is
	// precisely what auth.Middleware writes. Check that against the strconv
	// oracle over inputs chosen to sit on the pattern's boundaries — every
	// single-digit perturbation of MaxInt64 lands on a different alternative,
	// so widening or narrowing any one character class fails here.
	for _, probe := range permissionsEpochProbes() {
		want := isCanonicalNonNegativeInt64(probe)
		if got := epochRE.MatchString(probe); got != want {
			verb := "accepts"
			why := "clients compare the raw header verbatim, and the counter is a signed int64 " +
				"column, so anything strconv cannot round-trip is unreachable at runtime"
			if want {
				verb, why = "rejects", "auth.Middleware can emit this exact rendering"
			}
			t.Errorf("PermissionsEpoch pattern %s epoch %q — %s", verb, probe, why)
		}
	}
	if description := fmt.Sprint(epoch["description"]); !strings.Contains(description, "every authenticated response") {
		t.Errorf("PermissionsEpoch description does not pin the always-present guarantee: %q", description)
	}

	session, ok := headers["SessionExpiresAt"].(map[string]any)
	if !ok {
		t.Fatal("SessionExpiresAt header component is missing")
	}
	if required, present := session["required"]; present && required != false {
		t.Errorf("SessionExpiresAt required=%v, want false or absent — auth.Middleware only sets the header on "+
			"the session-cookie branch, never for API-key callers", required)
	}
	sessionSchema, _ := session["schema"].(map[string]any)
	if sessionSchema["type"] != "string" || sessionSchema["format"] != "date-time" {
		t.Fatalf("SessionExpiresAt schema=%v, want {\"type\": \"string\", \"format\": \"date-time\"} — the "+
			"middleware emits time.RFC3339", sessionSchema)
	}
	if _, over := sessionSchema["pattern"]; over {
		t.Errorf("SessionExpiresAt is pinned by format alone; a second pattern constraint can only drift: %v",
			sessionSchema)
	}
	description := fmt.Sprint(session["description"])
	for _, phrase := range []string{"session cookie", "optional", "RFC 3339"} {
		if !strings.Contains(description, phrase) {
			t.Errorf("SessionExpiresAt description lacks %q: %q", phrase, description)
		}
	}
}

// PAI-808: production agent-side clients ask for the full working set with a
// literal GET /api/projects?status=all, but the operation documented no query
// parameters at all — so by the published contract that request was an
// undocumented extension, and a caller reading only the spec would fetch the
// active-only default and silently miss every frozen and archived project.
//
// Pin the filter exactly as ListProjects implements it. The concrete states
// come from the same vocabulary the /api/schema response publishes, so
// a new lifecycle status can never be added to the runtime without this
// failing. `all` is the one value that is not a status: it is a handler-level
// alias for the normal lifecycle states, and it deliberately stops short of
// `deleted`, which stays behind the explicit trash view. Getting that wrong in
// either direction is silent — an over-broad `all` leaks soft-deleted projects
// into ordinary listings, an under-broad one hides live work — so the
// documented semantics are asserted, not just the enum.
func TestOpenAPIProjectListStatusFilterMatchesRuntime(t *testing.T) {
	doc := documentedAPIDocument(t)
	raw, ok := doc.Paths["/api/projects"]["get"]
	if !ok {
		t.Fatal("GET /api/projects is undocumented")
	}
	type openAPIParameter struct {
		Name        string `json:"name"`
		In          string `json:"in"`
		Required    *bool  `json:"required"`
		Description string `json:"description"`
		Schema      struct {
			Type    string   `json:"type"`
			Enum    []string `json:"enum"`
			Default *string  `json:"default"`
		} `json:"schema"`
	}
	var operation struct {
		Parameters []openAPIParameter `json:"parameters"`
	}
	if err := json.Unmarshal(raw, &operation); err != nil {
		t.Fatalf("GET /api/projects operation: %v", err)
	}

	var status *openAPIParameter
	documented := make([]string, 0, len(operation.Parameters))
	for i, parameter := range operation.Parameters {
		documented = append(documented, parameter.In+":"+parameter.Name)
		if parameter.Name == "status" && parameter.In == "query" {
			status = &operation.Parameters[i]
		}
	}
	if status == nil {
		t.Fatalf("GET /api/projects documents no status query parameter; documented parameters: %v — "+
			"ListProjects reads it from r.URL.Query(), and agent-side callers send ?status=all", documented)
	}
	if status.Required != nil && *status.Required {
		t.Errorf("status required=%v, want false or absent — omitting it is legal and means active",
			*status.Required)
	}
	if status.Schema.Type != "string" {
		t.Errorf("status schema type=%q, want string — it arrives as a raw query value and is compared "+
			"verbatim against the lifecycle vocabulary", status.Schema.Type)
	}

	// The concrete states are the runtime's own vocabulary, in its own order;
	// `all` is the handler-level alias appended to it.
	runtimeStates := handlers.Schema.Enums["project_status"]
	if len(runtimeStates) == 0 {
		t.Fatal("handlers.Schema.Enums[\"project_status\"] is empty — the oracle this test compares against is gone")
	}
	wantEnum := append(append([]string(nil), runtimeStates...), "all")
	if !reflect.DeepEqual(status.Schema.Enum, wantEnum) {
		t.Errorf("status enum = %v, want %v — the concrete values are handlers.Schema.Enums[\"project_status\"], "+
			"which is what validProjectStatus allowlists, plus the `all` alias", status.Schema.Enum, wantEnum)
	}
	if status.Schema.Default == nil || *status.Schema.Default != "active" {
		t.Errorf("status default = %v, want \"active\" — ListProjects substitutes active for an empty status, "+
			"so a caller that omits the parameter never sees frozen or archived projects",
			status.Schema.Default)
	}

	// `all` is the whole reason agent-side callers pass the parameter, and its
	// boundary is the part a reader cannot infer from the enum: it covers the
	// normal lifecycle states and stops at deleted.
	description := status.Description
	if !strings.Contains(description, "`all`") {
		t.Fatalf("status description does not explain the `all` alias: %q", description)
	}
	for _, state := range []string{"active", "frozen", "archived"} {
		if !strings.Contains(description, "`"+state+"`") {
			t.Errorf("status description does not name `%s` among the states `all` returns: %q",
				state, description)
		}
	}
	if !strings.Contains(description, "excludes `deleted`") {
		t.Errorf("status description does not pin that `all` excludes `deleted`: %q — soft-deleted projects "+
			"are an explicit trash view, so `all` returning them would be a privacy-relevant contract change",
			description)
	}
}

func TestAgentModeOpenAPITrustVocabularyMatchesDeliveryTrust(t *testing.T) {
	doc := documentedAPIDocument(t)
	components, ok := doc.raw["components"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI components are missing")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI schemas are missing")
	}
	trust, ok := schemas["AgentModeTrust"].(map[string]any)
	if !ok {
		t.Fatal("AgentModeTrust schema is missing")
	}
	properties, ok := trust["properties"].(map[string]any)
	if !ok {
		t.Fatal("AgentModeTrust properties are missing")
	}
	suppression, _ := properties["suppression"].(map[string]any)
	flags, _ := properties["flags"].(map[string]any)
	flagItems, _ := flags["items"].(map[string]any)

	wantSuppressions := []string{
		string(deliverytrust.SuppressTerminalComplete), string(deliverytrust.SuppressCancelled),
		string(deliverytrust.SuppressTerminalFailed), string(deliverytrust.SuppressWaitingOnHuman),
		string(deliverytrust.SuppressBlocked), string(deliverytrust.SuppressStale),
		string(deliverytrust.SuppressUnknownReporter), string(deliverytrust.SuppressNoSignal),
		string(deliverytrust.SuppressEstimateExpired), string(deliverytrust.SuppressOutlierHeavy),
		string(deliverytrust.SuppressInsufficientBasis), string(deliverytrust.SuppressMissingContributor),
	}
	wantFlags := []string{
		string(deliverytrust.FlagSourceBackslideIgnored), string(deliverytrust.FlagAgentHistoryDisagreement),
		string(deliverytrust.FlagHistoryQualityDowngraded), string(deliverytrust.FlagHistoryOutlierHeavy),
		string(deliverytrust.FlagHistoryInsufficientBasis), string(deliverytrust.FlagOwnerEstimateInvalid),
		string(deliverytrust.FlagOwnerEstimateExpired), string(deliverytrust.FlagDeployedUnverified),
		string(deliverytrust.FlagFailedNeedsRetry),
	}
	if got := openAPIStringEnum(t, suppression["enum"]); !reflect.DeepEqual(got, wantSuppressions) {
		t.Fatalf("AgentModeTrust suppression enum=%v, want deliverytrust vocabulary %v", got, wantSuppressions)
	}
	if got := openAPIStringEnum(t, flagItems["enum"]); !reflect.DeepEqual(got, wantFlags) {
		t.Fatalf("AgentModeTrust flags enum=%v, want deliverytrust vocabulary %v", got, wantFlags)
	}
}

func openAPIStringEnum(t *testing.T, raw any) []string {
	t.Helper()
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("OpenAPI enum is missing or not an array: %T", raw)
	}
	out := make([]string, len(values))
	for index, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("OpenAPI enum item %d is %T, want string", index, value)
		}
		out[index] = text
	}
	return out
}

func staleOpenAPIMethods(registered map[string]map[string]bool, documented map[string]map[string]json.RawMessage) []string {
	var stale []string
	for p, methods := range documented {
		for method := range methods {
			if !registered[p][method] {
				stale = append(stale, strings.ToUpper(method)+" "+p)
			}
		}
	}
	sort.Strings(stale)
	return stale
}

func TestOpenAPIContractMethodGuardDetectsMismatch(t *testing.T) {
	stale := staleOpenAPIMethods(
		map[string]map[string]bool{"/api/probe": {"get": true}},
		map[string]map[string]json.RawMessage{"/api/probe": {"post": json.RawMessage(`{}`)}},
	)
	if len(stale) != 1 || stale[0] != "POST /api/probe" {
		t.Fatalf("stale methods = %v, want [POST /api/probe]", stale)
	}
}

func TestOpenAPIContractSchemaRefsResolve(t *testing.T) {
	doc := documentedAPIDocument(t)
	missing := missingOpenAPIRefs(t, doc)
	for _, ref := range missing {
		t.Errorf("PAI-624: unresolved OpenAPI $ref %s", ref)
	}
}

func missingOpenAPIRefs(t *testing.T, doc openAPIDocument) []string {
	t.Helper()
	var missing []string
	for path, methods := range doc.Paths {
		for method, raw := range methods {
			method = strings.ToLower(method)
			if !openAPIMethods[method] {
				continue
			}
			var op any
			if err := json.Unmarshal(raw, &op); err != nil {
				t.Fatalf("operation %s %s unmarshal: %v", method, path, err)
			}
			for _, ref := range collectOpenAPIRefs(op) {
				if !jsonPointerExists(doc.raw, ref) {
					missing = append(missing, strings.ToUpper(method)+" "+path+" -> "+ref)
				}
			}
		}
	}
	sort.Strings(missing)
	return missing
}

func TestOpenAPIContractSchemaRefGuardDetectsMissingRef(t *testing.T) {
	rawOperation := json.RawMessage(`{
		"responses": {
			"200": {
				"description": "ok",
				"content": {
					"application/json": {
						"schema": {"$ref": "#/components/schemas/MissingProbe"}
					}
				}
			}
		}
	}`)
	doc := openAPIDocument{
		Paths: map[string]map[string]json.RawMessage{
			"/api/probe": {"get": rawOperation},
		},
		raw: map[string]any{
			"paths": map[string]any{},
			"components": map[string]any{
				"schemas": map[string]any{},
			},
		},
	}
	missing := missingOpenAPIRefs(t, doc)
	if len(missing) != 1 || missing[0] != "GET /api/probe -> #/components/schemas/MissingProbe" {
		t.Fatalf("missing refs = %v, want missing probe ref", missing)
	}
}

func collectOpenAPIRefs(v any) []string {
	switch x := v.(type) {
	case map[string]any:
		refs := []string{}
		for k, val := range x {
			if k == "$ref" {
				if s, ok := val.(string); ok {
					refs = append(refs, s)
				}
				continue
			}
			refs = append(refs, collectOpenAPIRefs(val)...)
		}
		return refs
	case []any:
		refs := []string{}
		for _, item := range x {
			refs = append(refs, collectOpenAPIRefs(item)...)
		}
		return refs
	default:
		return nil
	}
}

func jsonPointerExists(root map[string]any, ref string) bool {
	if !strings.HasPrefix(ref, "#/") {
		return false
	}
	var cur any = root
	for _, rawPart := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(rawPart, "~1", "/"), "~0", "~")
		obj, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = obj[part]
		if !ok {
			return false
		}
	}
	return true
}

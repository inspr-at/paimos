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
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
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

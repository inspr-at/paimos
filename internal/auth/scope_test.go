// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
)

type nativeUnreadBody struct{ reads int }

func (b *nativeUnreadBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("disabled native route read its body")
}
func (*nativeUnreadBody) Close() error { return nil }

// Risk: middleware rejects declared disabled routes before their handlers, or
// native authentication errors drift from the published contract. Exercise real
// cookie/key lookup, live roles, pairing and the complete production HTTP chain.
func TestNativeRoutesThroughProductionAuthentication(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "native-auth", "Native authentication")
	m := newMod(t, Config{Env: envDev})
	ownerID, ownerIdentity := signinPerson(t, tid, "native-owner", "Native owner", "owner@example.test", "owner@example.test", "super_admin")
	ownerCookie, err := m.startSession(t.Context(), ownerIdentity, tid, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	deniedID, deniedIdentity := signinPerson(t, tid, "native-customer", "Native customer", "customer@example.test", "customer@example.test", "customer")
	deniedCookie, err := m.startSession(t.Context(), deniedIdentity, tid, deniedID)
	if err != nil {
		t.Fatal(err)
	}
	keyOwner := tenant.Principal{TenantID: tid, KeyCreatorID: ownerID}
	create := func(name string, scopes []string) keyRecord {
		t.Helper()
		key, err := m.createAgentKey(t.Context(), keyOwner, name, "", scopes, nil)
		if err != nil {
			t.Fatal("create synthetic native key:", err)
		}
		return key
	}
	agent := create("native-delegated", []string{"harness.read", "harness.control", "harness.worker", "chat.send"})
	empty := create("native-empty", []string{})
	inbox := create("native-inbox", []string{"inbox.send"})
	paired := create("native-daemon", agentpairing.RuntimePermissions)
	var computer string
	if err := db.InTenant(t.Context(), appPool, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state)
			VALUES($1,gen_random_uuid(),'126612661',$2,$2,$2,'{}','synthetic','redeemed') RETURNING id::text`, tid, strings.Repeat("a", 64)).Scan(&computer); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,setup_state)
			VALUES($1,$2,$2,$3,$4,'native-test-daemon',$5,'connected')`, tid, computer, paired.PrincipalID, paired.ID, strings.Repeat("b", 64))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Seeding is complete. Requests use the production tenant boundary too.
	m.inTenant = db.InTenant
	catalog := doctrine.New(appPool, doctrine.Options{})
	server := &httpapi.Server{
		Pool:       appPool,
		Modules:    []httpapi.Module{m, nativesessions.NewDisabled()},
		Middleware: []func(http.Handler) http.Handler{m.Middleware, catalog.CatalogMiddleware, events.PositionMiddleware(appPool)},
	}
	handler := server.Handler()
	validator, err := nativecontract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	check := func(t *testing.T, method, path, cookie, bearer string, status int, reason nativecontract.Reason) {
		t.Helper()
		body := &nativeUnreadBody{}
		r := httptest.NewRequest(method, path, nil)
		r.Body = body
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: status %d, want %d; %s", method, path, w.Code, status, w.Body.String())
		}
		if w.Header().Get(reportercontract.Header) != reportercontract.NativeSession || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
			t.Fatal("native contract/cache/content header missing at production boundary")
		}
		if err := validator.ValidateJSON("NativeError", w.Body.Bytes()); err != nil {
			t.Fatal("invalid native authentication envelope:", err)
		}
		var got nativecontract.Error
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Reason != reason || got.RequestID != nil || got.Retryable {
			t.Fatalf("wrong native failure: %s", w.Body.String())
		}
		if body.reads != 0 {
			t.Fatal("production disabled route consumed its body")
		}
	}
	var before int
	if err := adminPool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1`, tid).Scan(&before); err != nil {
		t.Fatal(err)
	}
	routes := nativecontract.PublicRoutes()
	if len(routes) != 22 {
		t.Fatalf("native auth regression covers %d routes, want 22", len(routes))
	}
	personOnly := map[string]bool{
		"nativeAttach": true, "nativeAttachRenew": true, "nativeDetach": true,
		"nativeTake": true, "nativeReturn": true, "nativeApprovalsApprovalDecision": true, "endNativeSessionControl": true,
	}
	for _, route := range routes {
		t.Run(route.OperationID, func(t *testing.T) {
			path := routeValue.ReplaceAllString(route.Path, id)
			check(t, route.Method, path, ownerCookie, "", http.StatusServiceUnavailable, nativecontract.ReasonFeatureDisabled)
			check(t, route.Method, path, "", "", http.StatusUnauthorized, nativecontract.ReasonGrantMissing)
			check(t, route.Method, path, "invalid-session", "", http.StatusUnauthorized, nativecontract.ReasonGrantMissing)
			check(t, route.Method, path, deniedCookie, "", http.StatusForbidden, nativecontract.ReasonGrantMissing)
			check(t, route.Method, path, "", empty.Token, http.StatusForbidden, nativecontract.ReasonGrantMissing)
			status, reason := http.StatusServiceUnavailable, nativecontract.ReasonFeatureDisabled
			if personOnly[route.OperationID] {
				status, reason = http.StatusForbidden, nativecontract.ReasonGrantMissing
			}
			check(t, route.Method, path, "", agent.Token, status, reason)
			status, reason = http.StatusForbidden, nativecontract.ReasonGrantMissing
			if strings.HasPrefix(route.Path, "/api/agentd/native-sessions/") {
				status, reason = http.StatusServiceUnavailable, nativecontract.ReasonFeatureDisabled
			}
			check(t, route.Method, path, "", paired.Token, status, reason)
		})
	}
	check(t, http.MethodPost, "/api/projects/"+id+"/native-sessions/"+id+"/input", "", inbox.Token, http.StatusServiceUnavailable, nativecontract.ReasonFeatureDisabled)
	for _, view := range []string{"expert", "terminal"} {
		check(t, http.MethodGet, "/api/projects/"+id+"/native-sessions/"+id+"/stream?view="+view, "", agent.Token, http.StatusForbidden, nativecontract.ReasonWrongPrincipal)
	}
	// Exact patterns only: unknown descendants/methods and existing routes keep
	// the established authorization status and ordinary error body/header.
	for _, route := range []string{
		"POST /api/projects/" + id + "/native-sessions/" + id + "/input/extra",
		"POST /api/agentd/native-sessions/commands", "GET /api/agentd/native-sessions/claim",
		"GET /api/agentd/native-sessions/commands/extra", "GET /api/agent-keys",
	} {
		method, path, _ := strings.Cut(route, " ")
		for _, authenticated := range []bool{false, true} {
			r := httptest.NewRequest(method, path, nil)
			if authenticated {
				r.Header.Set("Authorization", "Bearer "+paired.Token)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := http.StatusUnauthorized
			if authenticated {
				want = http.StatusForbidden
			}
			if w.Code != want || w.Header().Get(reportercontract.Header) != "" || strings.Contains(w.Body.String(), `"reason"`) {
				t.Fatalf("non-native route changed: %s HTTP %d %s", route, w.Code, w.Body.String())
			}
		}
	}
	var after, sessions, runs int
	if err := adminPool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM events WHERE tenant_id=$1),
		(SELECT count(*) FROM harness_sessions WHERE tenant_id=$1),(SELECT count(*) FROM agent_runs WHERE tenant_id=$1)`, tid).Scan(&after, &sessions, &runs); err != nil {
		t.Fatal(err)
	}
	if before != after || sessions != 0 || runs != 0 {
		t.Fatalf("disabled native effects: events %d -> %d, sessions %d, runs %d", before, after, sessions, runs)
	}
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_pairing_computers SET state='revoked' WHERE id=$1`, computer); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/agentd/native-sessions/claim", nil)
	r.Header.Set("Authorization", "Bearer "+paired.Token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), `"pairing_revoked"`) {
		t.Fatalf("native allowance bypassed pairing revocation: HTTP %d %s", w.Code, w.Body.String())
	}
}

// Agent keys reach shared project groups (AEON-136) through the views scope,
// like saved views and preferences.
func TestCoreAgentScopeCoversProjectGroups(t *testing.T) {
	for _, tc := range []struct{ method, path, want string }{
		{http.MethodGet, "/api/project-groups", "views.read"},
		{http.MethodPost, "/api/project-groups", "views.write"},
		{http.MethodPost, "/api/project-groups/assign", "views.write"},
		{http.MethodPatch, "/api/project-groups/7d0d6f36-6f55-4b43-9f42-2a4d7a8a0a11", "views.write"},
		{http.MethodDelete, "/api/project-groups/7d0d6f36-6f55-4b43-9f42-2a4d7a8a0a11", "views.write"},
		{http.MethodGet, "/api/preferences/projects", "views.read"},
	} {
		got, controlled := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if !controlled || got != tc.want {
			t.Fatalf("%s %s = %q (controlled %v), want %q", tc.method, tc.path, got, controlled, tc.want)
		}
	}
}

func TestAgentScopeSeparatesProjectSubpathsAndUnknownRoutes(t *testing.T) {
	project := "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for _, tc := range []struct{ method, path, want string }{
		{"GET", "/api/projects", "nodes.read"},
		{"GET", "/api/projects/lookup?ref=AEON", "nodes.read"},
		{"HEAD", "/api/projects/lookup?ref=AEON", "nodes.read"},
		{"POST", "/api/projects/lookup", ""},
		{"PUT", "/api/projects/lookup", ""},
		{"PATCH", "/api/projects/lookup", ""},
		{"DELETE", "/api/projects/lookup", ""},
		{"GET", "/api/projects/lookup/extra", ""},
		{"GET", "/api/projects/unlisted", ""},
		{"GET", "/api/releases", "releases.read"},
		{"HEAD", "/api/releases", "releases.read"},
		{"GET", "/api/releases/260929113854.0.0", "releases.read"},
		{"HEAD", "/api/releases/260929113854.0.0", "releases.read"},
		{"POST", "/api/releases", ""},
		{"DELETE", "/api/releases/260929113854.0.0", ""},
		{"GET", "/api/releases/260929113854.0.0/presentation", ""},
		{"PUT", "/api/releases/260929113854.0.0/presentation", ""},
		{"DELETE", "/api/releases/260929113854.0.0/presentation", ""},
		{"GET", project + "/messages/listen", "inbox.read"},
		{"GET", "/api/inbox/messages/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/receipt", "inbox.receipt"},
		{"POST", project + "/messages", "inbox.send"},
		{"GET", project + "/intake", "intake.read"},
		{"POST", project + "/intake/sources", "intake.write"},
		{"GET", project + "/journey", "journey.read"},
		{"GET", project + "/requirements", "journey.read"},
		{"GET", project + "/releases/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/walker", "journey.read"},
		{"GET", project + "/instruction-provenance", "harness.read"},
		{"HEAD", project + "/instruction-provenance", "harness.read"},
		{"POST", project + "/instruction-provenance", ""},
		{"GET", project + "/instruction-provenance/extra", ""},
		{"GET", project + "/release-memberships", "releases.read"},
		{"GET", project + "/releases/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/note-snapshot", "releases.read"},
		{"POST", project + "/releases/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/note-snapshot", ""},
		{"HEAD", project + "/release-memberships", "releases.read"},
		{"POST", project + "/release-memberships", ""},
		{"GET", project + "/release-memberships/extra", ""},
		{"GET", "/api/knowledge/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "knowledge.read"},
		{"GET", "/api/knowledge/learnings", "knowledge.read"},
		{"POST", "/api/knowledge/learnings/n-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/accept", ""},
		{"POST", "/api/knowledge/learnings/n-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/dismiss", ""},
		{"POST", "/api/knowledge/learnings/n-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/draft", ""},
		{"PUT", "/api/knowledge/learnings/n-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/recommendation", "knowledge.write"},
		{"GET", "/api/tickets/graph", "nodes.read"},
		{"GET", "/api/tickets", ""},
		{"POST", "/api/knowledge", "knowledge.write"},
		{"GET", "/api/models/resolve", "models.read"},
		{"GET", "/api/model-prices", "harness.read"},
		{"HEAD", "/api/model-prices", "harness.read"},
		{"POST", "/api/model-prices", ""},
		{"PUT", "/api/model-prices", ""},
		{"GET", "/api/model-prices/other", ""},
		{"GET", "/api/plugins", "plugins.read"},
		{"POST", project + "/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/heartbeat", "harness.worker"},
		{"GET", "/api/harness-sessions/live", "harness.read"},
		{"GET", "/api/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/delivery-rating", ""},
		{"PUT", "/api/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/delivery-rating", ""},
		{"DELETE", "/api/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/delivery-rating", ""},
		{"GET", "/api/nodes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/delivery-ratings", ""},
		{"GET", "/api/usage/dashboard", "harness.read"},
		{"GET", "/api/nodes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/time-totals", "hours.read"},
		{"POST", "/api/time-entries", "hours.write"},
		{"POST", "/api/approvals/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/decision", ""},
		{"POST", project + "/requirements", ""},
		{"PUT", "/api/plugins/foo/installation", ""},
		{"GET", "/api/unlisted", ""},
		{"GET", "/api/me", selfScope},
		{"GET", "/api/status/help", authz.AuthenticatedRoute},
		{"HEAD", "/api/status/help", authz.AuthenticatedRoute},
		{"GET", "/api/status/help?project_id=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", authz.AuthenticatedRoute},
		{"POST", "/api/status/help", ""},
		{"PUT", "/api/status/help", ""},
		{"PATCH", "/api/status/help", ""},
		{"DELETE", "/api/status/help", ""},
		{"GET", "/api/status/help/extra", ""},
		{"GET", "/api/status/other", ""},
		{"GET", "/api/me/profile", ""},
		{"GET", "/api/tags", "nodes.read"},
		{"PATCH", "/api/tags/bug", "nodes.configure"},
		{"GET", "/api/work-orders/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/runs", "run.read"},
		{"POST", "/api/work-orders/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/runs", "run.create"},
	} {
		got, _ := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if got != tc.want {
			t.Errorf("%s %s = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

var registeredRoute = regexp.MustCompile(`^(GET|HEAD|POST|PUT|PATCH|DELETE) /api/`)
var routeValue = regexp.MustCompile(`\{[^}]+\}`)
var policyMux = func() *http.ServeMux {
	mux := http.NewServeMux()
	for pattern := range authz.RoutePermissions {
		mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	return mux
}()

func setPolicyPattern(r *http.Request) {
	_, r.Pattern = policyMux.Handler(r)
}

// Source registration literals include the route arrays mounted by the work
// order, run and harness modules. This catches additions without a scope map.
func registeredAPIRoutes(t *testing.T) []string {
	t.Helper()
	set := map[string]bool{}
	err := filepath.WalkDir("../", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err == nil && registeredRoute.MatchString(value) {
				set[value] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	routes := make([]string, 0, len(set))
	for route := range set {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	if len(routes) < 100 {
		t.Fatalf("only %d registered API routes found", len(routes))
	}
	return routes
}

func TestEmptyAgentKeyDeniedAcrossRegisteredAPIRoutes(t *testing.T) {
	reset(t)
	tenantID := insertTenant(t, "scope-audit", "Scope audit")
	m := newMod(t, Config{})
	key, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID, KeyCreatorID: keyTestPerson(t, m.pool, tenantID)}, "empty-audit", "", []string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, pattern := range registeredAPIRoutes(t) {
		method, path, _ := strings.Cut(pattern, " ")
		path = routeValue.ReplaceAllString(path, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
		req := httptest.NewRequest(method, path, bytes.NewReader(nil))
		setPolicyPattern(req)
		req.Header.Set("Authorization", "Bearer "+key.Token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		want := http.StatusForbidden
		if pattern == "GET /api/me" || pattern == "GET /api/status/help" {
			want = http.StatusNoContent // Only these two reads need no key scope.
		}
		if pattern == "GET /api/inbox/messages/{messageId}/receipt" {
			want = http.StatusNotFound // Sender-only receipts conceal permission denial.
		}
		if res.Code != want {
			t.Errorf("%s: %d %s", pattern, res.Code, res.Body.String())
		}
	}
}

func TestCoordinatorScopesReachWorkRoutes(t *testing.T) {
	reset(t)
	tenantID := insertTenant(t, "coordinator-audit", "Coordinator audit")
	m := newMod(t, Config{})
	key, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID, KeyCreatorID: keyTestPerson(t, m.pool, tenantID)}, "aeon-coordinator", "", []string{"harness.worker", "inbox.read", "inbox.send", "nodes.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, route := range []string{
		"POST /api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/heartbeat",
		"GET /api/inbox/messages", "POST /api/inbox/messages/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/ack",
		"POST /api/inbox/messages", "POST /api/inbox/session-binding",
		"GET /api/nodes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"GET /api/projects/lookup?ref=AEON", "HEAD /api/projects/lookup?ref=AEON",
		"GET /api/projects", "GET /api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/messages/listen",
		"POST /api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/messages/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/ack",
	} {
		method, path, _ := strings.Cut(route, " ")
		req := httptest.NewRequest(method, path, nil)
		setPolicyPattern(req)
		req.Header.Set("Authorization", "Bearer "+key.Token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusNoContent {
			t.Errorf("%s: %d %s", route, res.Code, res.Body.String())
		}
	}
}

func TestAccountMetadataScopeAtMiddleware(t *testing.T) {
	reset(t)
	tenantID := insertTenant(t, "account-metadata-scope", "Account metadata scope")
	m := newMod(t, Config{})
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		name   string
		scopes []string
		status int
	}{
		{"manager", []string{"account.manage"}, http.StatusNoContent},
		{"reader", []string{"account.read"}, http.StatusForbidden},
		{"empty", []string{}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID, KeyCreatorID: keyTestPerson(t, m.pool, tenantID)}, tc.name, "", tc.scopes, nil)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPut, "/api/agent-accounts/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/metadata", nil)
			setPolicyPattern(req)
			req.Header.Set("Authorization", "Bearer "+key.Token)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != tc.status {
				t.Fatalf("metadata middleware: got %d want %d", res.Code, tc.status)
			}
		})
	}
}

func TestServicePrincipalsCannotReceiveAgentKeys(t *testing.T) {
	reset(t)
	tenantID := insertTenant(t, "service-audit", "Service audit")
	m := newMod(t, Config{})
	for _, role := range []string{"system", "importer", "operator", "embedding", "quote_public_service", "quote_confirmation_service", "portal_public_service"} {
		t.Run(role, func(t *testing.T) {
			name := "service-" + role
			err := testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'agent',$2,ARRAY[$3]::text[])`, tenantID, name, role)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID, KeyCreatorID: keyTestPerson(t, m.pool, tenantID)}, name, "", []string{"nodes.read"}, nil); !errors.Is(err, errServicePrincipal) {
				t.Fatalf("service key error: %v", err)
			}
		})
	}
	err := testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','person-service',ARRAY['system']::text[])`, tenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID, KeyCreatorID: keyTestPerson(t, m.pool, tenantID)}, "person-service", "", nil, nil); !errors.Is(err, errServicePrincipal) {
		t.Fatalf("person service name collision: %v", err)
	}
	// A colliding ordinary agent must not hide a service principal of the same name.
	normalKey, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID, KeyCreatorID: keyTestPerson(t, m.pool, tenantID)}, "collision", "", []string{"nodes.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'agent','collision',ARRAY['operator']::text[])`, tenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID, KeyCreatorID: keyTestPerson(t, m.pool, tenantID)}, "collision", "", nil, nil); !errors.Is(err, errServicePrincipal) {
		t.Fatalf("collision key error: %v", err)
	}
	if got := countInTenant(t, tenantID, `SELECT count(*) FROM agent_keys WHERE name='collision'`); got != 1 {
		t.Fatalf("collision keys: %d", got)
	}
	err = testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE principals SET roles=ARRAY['system']::text[] WHERE id=$1::uuid`, normalKey.PrincipalID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	prefix, secret, ok := parseBearer("Bearer " + normalKey.Token)
	if !ok {
		t.Fatal("minted bearer invalid")
	}
	if _, authenticated, err := m.authenticateAgent(httptest.NewRequest(http.MethodGet, "/api/me", nil).Context(), prefix, secret); err != nil || authenticated {
		t.Fatalf("promoted service key authenticated: %v, %v", authenticated, err)
	}
}

func TestNativeCandidateArtifactAgentCeiling(t *testing.T) {
	path := "/api/projects/11111111-1111-4111-8111-111111111111/releases/22222222-2222-4222-8222-222222222222/candidate-artifact"
	for _, tc := range []struct{ method, want string }{{"GET", "stage_handoffs.read"}, {"PUT", "stage_handoffs.write"}} {
		got, controlled := coreAgentScope(httptest.NewRequest(tc.method, path, nil))
		if !controlled || got != tc.want {
			t.Fatalf("%s: %s %v", tc.method, got, controlled)
		}
		if agentHasScope([]string{"journey.read", "stage.deploy"}, got) {
			t.Fatalf("unrelated scopes allow %s", tc.method)
		}
	}
}
